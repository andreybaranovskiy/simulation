// Package runner starts and supervises simulation processes.
//
// Every run is its own process. That is forced by godes keeping its clock in
// package-level globals, but it is also what makes the rest possible: a
// per-run timeout and memory cap, cancellation that actually stops the work,
// replications spread across cores, and later the isolation boundary that
// uploaded Go models need.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/analytics"
	"github.com/andreybaranovskiy/simulation/internal/config"
	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
	"github.com/andreybaranovskiy/simulation/internal/engine/templates"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/runstore"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

// Dispatcher owns the run queue. One runs per server.
type Dispatcher struct {
	cfg   config.Config
	store *store.Store
	log   *slog.Logger

	// dataDir is where run directories live.
	dataDir string
	// runnerPath is the simrunner executable.
	runnerPath string

	// slots bounds how many simulations run at once.
	slots chan struct{}

	// active tracks in-flight runs so they can be cancelled.
	mu     sync.Mutex
	active map[string]context.CancelFunc

	// Events broadcasts progress to whoever is watching.
	events *Broker

	wake chan struct{}
	wg   sync.WaitGroup
}

func New(cfg config.Config, st *store.Store, log *slog.Logger) (*Dispatcher, error) {
	runnerPath, err := resolveRunnerPath(cfg.Engine.RunnerPath)
	if err != nil {
		return nil, err
	}

	d := &Dispatcher{
		cfg:        cfg,
		store:      st,
		log:        log,
		dataDir:    cfg.Storage.DataDir,
		runnerPath: runnerPath,
		slots:      make(chan struct{}, cfg.Engine.MaxConcurrentRuns),
		active:     make(map[string]context.CancelFunc),
		events:     NewBroker(),
		wake:       make(chan struct{}, 1),
	}

	log.Info("run dispatcher ready",
		"runner", runnerPath, "concurrency", cfg.Engine.MaxConcurrentRuns)
	return d, nil
}

// resolveRunnerPath finds simrunner. Empty configuration looks next to the
// server binary, which is where an install puts it.
func resolveRunnerPath(configured string) (string, error) {
	candidates := []string{}

	if configured != "" {
		candidates = append(candidates, configured)
	} else {
		if exe, err := os.Executable(); err == nil {
			dir := filepath.Dir(exe)
			candidates = append(candidates,
				filepath.Join(dir, "simrunner"+exeSuffix()),
				// During development the server runs from the repository root
				// and the binaries land in bin/.
				filepath.Join(dir, "bin", "simrunner"+exeSuffix()))
		}
		candidates = append(candidates, filepath.Join("bin", "simrunner"+exeSuffix()))
	}

	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return filepath.Abs(path)
		}
	}

	return "", fmt.Errorf("could not find the simrunner executable; "+
		"build it with 'go build -o bin/simrunner ./cmd/simrunner' or set engine.runner_path. Looked in: %v",
		candidates)
}

// Events exposes the progress broker so the API can stream to clients.
func (d *Dispatcher) Events() *Broker { return d.events }

// Start begins processing the queue and returns immediately.
func (d *Dispatcher) Start(ctx context.Context) {
	// Runs that were in flight when the server stopped are gone: their
	// processes died with it. Marking them failed is the honest report, and it
	// stops the UI showing a progress bar that will never move again.
	if n, err := d.store.Runs.ReclaimOrphans(ctx); err != nil {
		d.log.Warn("could not reclaim orphaned runs", "error", err)
	} else if n > 0 {
		d.log.Warn("marked runs failed because the server restarted while they were running", "count", n)
	}

	d.wg.Add(1)
	go d.loop(ctx)
}

// Stop waits for the dispatch loop to finish. In-flight runs are cancelled by
// the context the caller passed to Start.
func (d *Dispatcher) Stop() {
	d.wg.Wait()
	d.events.Close()
}

// Nudge tells the dispatcher there may be new work, so a freshly queued run
// starts immediately rather than at the next poll.
func (d *Dispatcher) Nudge() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// loop pulls queued runs and starts them, one per free slot.
//
// It polls as well as waiting for a nudge. Polling is the backstop: a nudge
// can be missed if the queue was full at the time, and a run left queued
// forever with idle workers is worse than a query a few seconds apart.
func (d *Dispatcher) loop(ctx context.Context) {
	defer d.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		d.drainQueue(ctx)

		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) drainQueue(ctx context.Context) {
	for {
		select {
		case d.slots <- struct{}{}:
		default:
			// Every worker is busy. The run stays queued and is picked up as
			// soon as one frees.
			return
		}

		run, err := d.store.Runs.ClaimNext(ctx)
		if err != nil {
			<-d.slots
			if !errors.Is(err, store.ErrNotFound) {
				d.log.Error("could not claim a queued run", "error", err)
			}
			return
		}

		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer func() { <-d.slots }()
			d.execute(ctx, run)
		}()
	}
}

// Enqueue creates the run rows for a scenario and wakes the dispatcher.
//
// A scenario with replications produces several runs, each with a seed derived
// from the scenario's. Derived rather than random, so the whole set is
// reproducible from one number.
func (d *Dispatcher) Enqueue(ctx context.Context, scenario *model.Scenario, userID string) ([]model.Run, error) {
	replications := scenario.Replications
	if replications < 1 {
		replications = 1
	}

	baseSeed := uint64(time.Now().UnixNano())
	if scenario.Seed != nil {
		baseSeed = *scenario.Seed
	}

	runs := make([]model.Run, 0, replications)

	for i := 0; i < replications; i++ {
		r := model.Run{
			ProjectID:   scenario.ProjectID,
			ScenarioID:  scenario.ID,
			Replication: i,
			Seed:        seedFor(baseSeed, i),
			Status:      model.RunQueued,
			CreatedBy:   userID,
		}
		if err := d.store.Runs.Create(ctx, &r); err != nil {
			return runs, err
		}

		runs = append(runs, r)
		d.events.Publish(Event{
			Type: EventQueued, RunID: r.ID, ScenarioID: scenario.ID,
			ProjectID: scenario.ProjectID, Status: string(model.RunQueued),
		})
	}

	d.Nudge()
	return runs, nil
}

// Cancel stops a run, whether it is queued or already executing.
func (d *Dispatcher) Cancel(ctx context.Context, runID string) (bool, error) {
	d.mu.Lock()
	cancel, running := d.active[runID]
	d.mu.Unlock()

	if running {
		// Killing the process is what actually stops the work; the row update
		// below records why it stopped.
		cancel()
	}

	changed, err := d.store.Runs.Cancel(ctx, runID)
	if err != nil {
		return false, err
	}
	if changed {
		d.events.Publish(Event{Type: EventFinished, RunID: runID, Status: string(model.RunCanceled)})
	}
	return changed, nil
}

// RunDir is where a run's artifacts live, relative to the data directory.
func RunDirFor(runID string) string {
	// Sharding by the first two characters keeps any one directory from
	// growing past a few thousand entries on an installation with many runs.
	return filepath.Join("runs", runID[:2], runID)
}

// AbsRunDir resolves a run's artifact directory on disk.
func (d *Dispatcher) AbsRunDir(run *model.Run) string {
	dir := run.ArtifactDir
	if dir == "" {
		dir = RunDirFor(run.ID)
	}
	return filepath.Join(d.dataDir, filepath.FromSlash(dir))
}

// seedFor derives a replication's seed from the scenario's. Hashing rather
// than adding keeps the streams well separated: neighbouring seeds would
// otherwise produce correlated runs, which defeats the point of replicating.
func seedFor(base uint64, replication int) uint64 {
	if replication == 0 {
		return base
	}
	x := base ^ (uint64(replication+1) * 0x9E3779B97F4A7C15)
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// resolveModel turns a stored model plus a scenario's parameters into
// something the runner can execute, and reports how to invoke it.
type invocation struct {
	// templateKey and params drive a built-in template.
	templateKey string
	params      map[string]float64
	// specPath is a written-out model spec, for a spec model.
	specPath string
}

func (d *Dispatcher) resolveModel(ctx context.Context, scenario *model.Scenario, runDir string) (*invocation, error) {
	m, err := d.store.Models.ByID(ctx, scenario.ModelID)
	if err != nil {
		return nil, fmt.Errorf("load the model: %w", err)
	}

	params := map[string]float64{}
	if len(scenario.Params) > 0 {
		if err := json.Unmarshal(scenario.Params, &params); err != nil {
			return nil, fmt.Errorf("the scenario's parameters are not a set of numbers: %w", err)
		}
	}

	switch m.Source {
	case model.SourceTemplate:
		tpl, ok := templates.Get(m.TemplateKey)
		if !ok {
			return nil, fmt.Errorf("the model uses template %q, which this build does not have", m.TemplateKey)
		}
		// Building here as well as in the runner catches a bad parameter
		// before a process is started, so the error reaches the user as a
		// rejected request rather than a failed run.
		if _, err := tpl.Build(params); err != nil {
			return nil, err
		}
		return &invocation{templateKey: m.TemplateKey, params: params}, nil

	case model.SourceSpec:
		if len(m.Spec) == 0 {
			return nil, fmt.Errorf("the model has no definition")
		}

		parsed, err := spec.Parse(m.Spec)
		if err != nil {
			return nil, err
		}
		if unknown := parsed.ApplyParams(params); len(unknown) > 0 {
			return nil, fmt.Errorf("the scenario sets parameters the model does not have: %v", unknown)
		}

		// The runner reads a file rather than a command line, because a model
		// is far larger than any argument limit.
		data, err := parsed.Marshal()
		if err != nil {
			return nil, err
		}
		path := filepath.Join(runDir, "input-model.json")
		if err := os.WriteFile(path, data, 0o640); err != nil {
			return nil, fmt.Errorf("write the model for the runner: %w", err)
		}
		return &invocation{specPath: path}, nil

	case model.SourceGo:
		return nil, fmt.Errorf("running uploaded Go models is not enabled in this build")

	case model.SourceAnimation:
		return nil, fmt.Errorf("an imported animation is played back, not run")
	}

	return nil, fmt.Errorf("unknown model source %q", m.Source)
}

// saveKPIs mirrors a finished run's KPIs into the database.
func (d *Dispatcher) saveKPIs(ctx context.Context, runID, absDir string) error {
	data, err := os.ReadFile(runstore.Dir(absDir).Agg(runstore.KPIFile))
	if err != nil {
		return err
	}

	var set analytics.KPISet
	if err := json.Unmarshal(data, &set); err != nil {
		return fmt.Errorf("parse the run's KPIs: %w", err)
	}
	return d.store.Runs.SaveKPIs(ctx, runID, set.KPIs)
}
