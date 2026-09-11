package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/runstore"
)

// execute runs one simulation from start to finished row.
func (d *Dispatcher) execute(parent context.Context, run *model.Run) {
	started := time.Now()

	// The run gets its own cancellable context so it can be stopped
	// individually, and its own timeout so a model that never terminates does
	// not hold a worker slot forever.
	ctx, cancel := context.WithTimeout(parent, d.cfg.Engine.RunTimeout)
	defer cancel()

	d.mu.Lock()
	d.active[run.ID] = cancel
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		delete(d.active, run.ID)
		d.mu.Unlock()
	}()

	d.events.Publish(Event{
		Type: EventStarted, RunID: run.ID, ScenarioID: run.ScenarioID,
		ProjectID: run.ProjectID, Status: string(model.RunActive),
	})

	err := d.runOne(ctx, run)

	run.DurationMS = time.Since(started).Milliseconds()

	switch {
	case err == nil:
		run.Status = model.RunDone
		run.Progress = 1

	case ctx.Err() == context.DeadlineExceeded:
		run.Status = model.RunFailed
		run.Error = fmt.Sprintf("The run exceeded its %s time limit and was stopped.", d.cfg.Engine.RunTimeout)

	case ctx.Err() == context.Canceled:
		run.Status = model.RunCanceled
		run.Error = ""

	default:
		run.Status = model.RunFailed
		run.Error = err.Error()
	}

	// The finishing write must not be abandoned because the run's own context
	// was cancelled, or a cancelled run would sit at "running" forever.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
	defer finishCancel()

	if err := d.store.Runs.Finish(finishCtx, run); err != nil {
		d.log.Error("could not record the run's outcome", "run", run.ID, "error", err)
	}

	if run.Status == model.RunDone {
		if err := d.saveKPIs(finishCtx, run.ID, d.AbsRunDir(run)); err != nil {
			d.log.Warn("could not store the run's KPIs", "run", run.ID, "error", err)
		}
		if err := d.store.Projects.Touch(finishCtx, run.ProjectID); err != nil {
			d.log.Debug("could not touch the project", "error", err)
		}
	}

	d.log.Info("run finished",
		"run", run.ID, "status", run.Status,
		"entities", run.EntityCount, "took", time.Since(started).Round(time.Millisecond),
		"error", run.Error)

	d.events.Publish(Event{
		Type: EventFinished, RunID: run.ID, ScenarioID: run.ScenarioID,
		ProjectID: run.ProjectID, Status: string(run.Status),
		Progress: run.Progress, Error: run.Error,
	})
}

func (d *Dispatcher) runOne(ctx context.Context, run *model.Run) error {
	scenario, err := d.store.Scenarios.ByID(ctx, run.ScenarioID)
	if err != nil {
		return fmt.Errorf("load the scenario: %w", err)
	}

	relDir := RunDirFor(run.ID)
	absDir := filepath.Join(d.dataDir, filepath.FromSlash(relDir))

	if err := os.MkdirAll(absDir, 0o750); err != nil {
		return fmt.Errorf("create the run directory: %w", err)
	}
	run.ArtifactDir = filepath.ToSlash(relDir)

	invocation, err := d.resolveModel(ctx, scenario, absDir)
	if err != nil {
		return err
	}

	args := []string{
		"-out", absDir,
		"-seed", strconv.FormatUint(run.Seed, 10),
		"-replication", strconv.Itoa(run.Replication),
		"-run-id", run.ID,
		"-scenario-id", scenario.ID,
		"-quiet",
	}

	if invocation.templateKey != "" {
		args = append(args, "-template", invocation.templateKey)
		for key, value := range invocation.params {
			args = append(args, "-set", fmt.Sprintf("%s=%g", key, value))
		}
	} else {
		args = append(args, "-spec", invocation.specPath)
	}

	cmd := exec.CommandContext(ctx, d.runnerPath, args...)
	cmd.Dir = absDir

	// The runner writes progress and errors to stderr, line by line, and
	// nothing to stdout that the server needs.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	prepareProcess(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the simulation process: %w", err)
	}

	// The memory cap is applied once the process exists. A failure to apply it
	// is logged rather than fatal: an uncapped run is worse than a capped one,
	// but far better than refusing to run at all on a machine where job
	// objects are unavailable.
	releaseLimit, err := limitProcess(cmd.Process.Pid, d.cfg.Engine.RunMemoryLimitBytes)
	if err != nil {
		d.log.Warn("could not apply the run memory limit", "run", run.ID, "error", err)
		releaseLimit = func() {}
	}
	defer releaseLimit()

	tail := d.followProgress(ctx, run, stderr)

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The runner's own message is far more useful than "exit status 1".
		if message := lastMeaningfulLine(tail); message != "" {
			return fmt.Errorf("%s", message)
		}
		return fmt.Errorf("the simulation process failed: %w", err)
	}

	return d.finishArtifacts(ctx, run, absDir)
}

// followProgress reads the runner's stderr and mirrors it into the run row and
// the event stream. It returns the tail of the output so a failure can be
// reported in the runner's own words.
func (d *Dispatcher) followProgress(ctx context.Context, run *model.Run, stderr io.Reader) []string {
	const tailSize = 12
	tail := make([]string, 0, tailSize)

	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lastWrite := time.Now()

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if len(tail) == tailSize {
			tail = tail[1:]
		}
		tail = append(tail, line)

		progress, ok := parseProgress(line)
		if !ok {
			continue
		}

		run.Progress = progress.fraction
		run.SimTime = progress.simTime
		run.EntityCount = progress.entities
		run.RecordCount = progress.records

		// The database write is rate-limited; the event stream is not, so the
		// UI stays smooth without a write per update.
		d.events.Publish(Event{
			Type: EventProgress, RunID: run.ID, ScenarioID: run.ScenarioID,
			ProjectID: run.ProjectID, Status: string(model.RunActive),
			Progress: progress.fraction, SimTime: progress.simTime,
			Entities: progress.entities, Records: progress.records,
		})

		if time.Since(lastWrite) > 2*time.Second {
			lastWrite = time.Now()
			if err := d.store.Runs.UpdateProgress(ctx, run.ID, model.RunActive,
				progress.fraction, progress.simTime, progress.entities, progress.records); err != nil {
				d.log.Debug("could not record run progress", "run", run.ID, "error", err)
			}
		}
	}

	return tail
}

// finishArtifacts records what the run produced. The runner already built the
// playback chunks and aggregates, so this reads the manifest rather than
// repeating the work.
func (d *Dispatcher) finishArtifacts(ctx context.Context, run *model.Run, absDir string) error {
	d.events.Publish(Event{
		Type: EventProgress, RunID: run.ID, ScenarioID: run.ScenarioID,
		ProjectID: run.ProjectID, Status: string(model.RunBuilding), Progress: 0.95,
	})

	dir := runstore.Dir(absDir)

	manifest, err := dir.ReadManifest()
	if err != nil {
		return fmt.Errorf("the run produced no readable manifest: %w", err)
	}

	run.EntityCount = uint64(manifest.Counts.Entities)
	run.RecordCount = manifest.Counts.Records
	run.ArtifactBytes = manifest.Counts.TotalBytes
	run.EngineVersion = manifest.EngineVersion
	run.SimTime = manifest.EndTime
	run.Warnings = marshalWarnings(manifest.Warnings)

	_ = ctx
	return nil
}

type progressLine struct {
	fraction float64
	simTime  float64
	entities uint64
	records  uint64
}

// parseProgress reads the runner's progress format:
//
//	12.5%  t=1234.5  entities=42  records=9001
//
// A line that does not match is ordinary output, not an error.
func parseProgress(line string) (progressLine, bool) {
	if !strings.Contains(line, "entities=") {
		return progressLine{}, false
	}

	var out progressLine
	matched := false

	for _, field := range strings.Fields(line) {
		switch {
		case strings.HasSuffix(field, "%"):
			if v, err := strconv.ParseFloat(strings.TrimSuffix(field, "%"), 64); err == nil {
				out.fraction = v / 100
				matched = true
			}
		case strings.HasPrefix(field, "t="):
			out.simTime = parseSeconds(strings.TrimPrefix(field, "t="))
		case strings.HasPrefix(field, "entities="):
			if v, err := strconv.ParseUint(strings.TrimPrefix(field, "entities="), 10, 64); err == nil {
				out.entities = v
				matched = true
			}
		case strings.HasPrefix(field, "records="):
			if v, err := strconv.ParseUint(strings.TrimPrefix(field, "records="), 10, 64); err == nil {
				out.records = v
			}
		}
	}

	return out, matched
}

// parseSeconds reads the runner's duration format: 12.3s, 4m05s or 2h30m.
func parseSeconds(s string) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSuffix(s, "s"), 64); err == nil {
		return v
	}

	total := 0.0
	number := ""

	for _, r := range s {
		switch r {
		case 'h':
			total += parseFloat(number) * 3600
			number = ""
		case 'm':
			total += parseFloat(number) * 60
			number = ""
		case 's':
			total += parseFloat(number)
			number = ""
		default:
			number += string(r)
		}
	}
	return total
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// lastMeaningfulLine picks the line most likely to explain a failure, skipping
// the progress chatter that precedes it.
func lastMeaningfulLine(tail []string) string {
	for i := len(tail) - 1; i >= 0; i-- {
		line := tail[i]
		if line == "" || strings.Contains(line, "entities=") {
			continue
		}
		return strings.TrimPrefix(line, "simrunner: ")
	}
	return ""
}

func marshalWarnings(warnings []string) []byte {
	if len(warnings) == 0 {
		return nil
	}
	out := `["`
	for i, w := range warnings {
		if i > 0 {
			out += `","`
		}
		out += strings.ReplaceAll(strings.ReplaceAll(w, `\`, `\\`), `"`, `\"`)
	}
	return []byte(out + `"]`)
}
