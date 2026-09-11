// Command simrunner executes exactly one simulation and exits.
//
// It is a separate process rather than a goroutine in the server because godes
// keeps its clock and runner registry in package-level globals: one simulation
// per process is a property of the engine, not a choice. Running out of process
// also gives per-run timeouts, memory caps, clean cancellation, replications
// across cores, and the isolation boundary that uploaded Go models need.
//
// The server invokes it. It is also usable directly, which is the quickest way
// to try a model:
//
//	simrunner -template container_terminal -out ./run1
//	simrunner -template container_terminal -set gateLanes=5 -set yardCranes=4 -out ./run2
//	simrunner -spec my-model.yaml -seed 42 -out ./run3
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/engine/interp"
	"github.com/andreybaranovskiy/simulation/internal/engine/rng"
	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
	"github.com/andreybaranovskiy/simulation/internal/engine/templates"
	"github.com/andreybaranovskiy/simulation/internal/engine/trace"
	"github.com/andreybaranovskiy/simulation/internal/runstore"
)

var version = "dev"

// TraceName and ResultName are the files a run directory always contains. The
// server looks for them by name.
const (
	TraceName  = "run.trace"
	ResultName = "result.json"
	ModelName  = "model.json"
)

type paramFlags map[string]float64

func (p paramFlags) String() string { return "" }

func (p paramFlags) Set(raw string) error {
	key, value, found := strings.Cut(raw, "=")
	if !found {
		return fmt.Errorf("expected name=value, got %q", raw)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return fmt.Errorf("%q is not a number", value)
	}
	p[strings.TrimSpace(key)] = v
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "simrunner:", err)
		os.Exit(1)
	}
}

func run() error {
	params := paramFlags{}

	var (
		templateKey = flag.String("template", "", "template key to build the model from")
		specPath    = flag.String("spec", "", "path to a model spec (JSON or YAML)")
		outDir      = flag.String("out", "", "directory to write the run into (required)")
		seedFlag    = flag.String("seed", "", "run seed: a number, or text hashed into one. Omitted means a seed from the clock")
		replication = flag.Int("replication", 0, "replication number, which shifts the seed")
		runID       = flag.String("run-id", "", "run identifier recorded in the trace")
		scenarioID  = flag.String("scenario-id", "", "scenario identifier recorded in the trace")
		maxRecords  = flag.Uint64("max-records", 200_000_000, "stop after this many trace records")
		maxEntities = flag.Int("max-entities", 0, "stop creating entities after this many. 0 derives a limit from the model")
		listFlag    = flag.Bool("list", false, "list the available templates and exit")
		describe    = flag.String("describe", "", "print a template's parameters and exit")
		dumpModel   = flag.Bool("dump-model", false, "write the resolved model to stdout and exit without running")
		skipBuild   = flag.Bool("no-build", false, "write the trace but skip building the viewer artifacts")
		keepTrace   = flag.Bool("keep-trace", true, "keep the raw event trace after building the artifacts")
		quiet       = flag.Bool("quiet", false, "suppress progress output")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Var(params, "set", "set a template parameter as name=value; repeatable")
	flag.Parse()

	switch {
	case *showVersion:
		fmt.Println(version)
		return nil
	case *listFlag:
		return listTemplates()
	case *describe != "":
		return describeTemplate(*describe)
	}

	model, err := loadModel(*templateKey, *specPath, params)
	if err != nil {
		return err
	}

	if *dumpModel {
		data, err := model.Marshal()
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	if *outDir == "" {
		return fmt.Errorf("give an output directory with -out")
	}
	if err := os.MkdirAll(*outDir, 0o750); err != nil {
		return fmt.Errorf("create the output directory: %w", err)
	}

	seed := resolveSeed(*seedFlag, *replication)

	// The resolved model is stored alongside the run. A result nobody can tie
	// back to the exact model that produced it is not reproducible.
	if data, err := model.Marshal(); err == nil {
		_ = os.WriteFile(filepath.Join(*outDir, ModelName), data, 0o640)
	}

	tracePath := filepath.Join(*outDir, TraceName)
	header := buildHeader(model, *runID, *scenarioID, seed, *replication)

	writer, err := trace.Create(tracePath, header, trace.Options{MaxRecords: *maxRecords})
	if err != nil {
		return err
	}

	if !*quiet {
		fmt.Fprintf(os.Stderr, "running %q: horizon %s, about %d arrivals expected, seed %d\n",
			model.Name, formatSeconds(model.Horizon), model.ExpectedArrivals(), seed)
	}

	started := time.Now()

	result, runErr := interp.Run(interp.Config{
		Model:       model,
		Trace:       writer,
		Seed:        seed,
		Replication: *replication,
		MaxEntities: *maxEntities,
		Progress:    progressReporter(*quiet, model.Horizon),
	})

	footer := trace.Footer{
		Complete:    runErr == nil,
		WallSeconds: time.Since(started).Seconds(),
	}
	if runErr != nil {
		footer.Error = runErr.Error()
	}
	if result != nil {
		footer.EndTime = result.EndTime
	}

	closeErr := writer.Close(footer)

	if runErr != nil {
		return runErr
	}
	if closeErr != nil {
		return closeErr
	}

	if err := writeResult(filepath.Join(*outDir, ResultName), model, result, seed, *replication); err != nil {
		return err
	}

	if !*quiet {
		printSummary(os.Stderr, model, result)
	}

	if *skipBuild {
		return nil
	}

	// Building here rather than in the server keeps the whole cost of a run
	// inside the process that is already bounded by a timeout and a memory
	// cap, and it means a finished run directory is immediately viewable.
	if !*quiet {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "building viewer artifacts...")
	}

	manifest, err := runstore.Build(runstore.Dir(*outDir), runstore.BuildOptions{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Progress: buildProgress(*quiet),
	})
	if err != nil {
		return fmt.Errorf("build the viewer artifacts: %w", err)
	}

	if !*keepTrace {
		// The trace is an intermediate. Once the chunks and aggregates exist
		// nothing reads it again, and on a large run it is the biggest file in
		// the directory.
		_ = os.Remove(filepath.Join(*outDir, runstore.TraceFile))
	}

	if !*quiet {
		printArtifacts(os.Stderr, manifest)
	}
	return nil
}

func loadModel(templateKey, specPath string, params paramFlags) (*spec.Model, error) {
	switch {
	case templateKey != "" && specPath != "":
		return nil, fmt.Errorf("give either -template or -spec, not both")

	case templateKey != "":
		tpl, ok := templates.Get(templateKey)
		if !ok {
			return nil, fmt.Errorf("no template named %q; run with -list to see them", templateKey)
		}
		return tpl.Build(params)

	case specPath != "":
		data, err := os.ReadFile(specPath)
		if err != nil {
			return nil, fmt.Errorf("read the model: %w", err)
		}
		model, err := spec.Parse(data)
		if err != nil {
			return nil, err
		}
		if len(params) > 0 {
			if unknown := model.ApplyParams(params); len(unknown) > 0 {
				return nil, fmt.Errorf("the model has no parameter(s): %s", strings.Join(unknown, ", "))
			}
		}
		return model, nil

	default:
		return nil, fmt.Errorf("give a model with -template or -spec; run with -list to see the templates")
	}
}

// resolveSeed turns the flag into a number. A replication offsets the seed so
// repeated runs of one scenario differ, while staying a pure function of the
// scenario's own seed and therefore reproducible.
func resolveSeed(raw string, replication int) uint64 {
	var base uint64

	switch {
	case raw == "":
		base = uint64(time.Now().UnixNano())
	default:
		if n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64); err == nil {
			base = n
		} else {
			base = rng.SeedFromString(raw)
		}
	}

	if replication > 0 {
		base = rng.SeedFromString(fmt.Sprintf("%d:%d", base, replication))
	}
	return base
}

func buildHeader(m *spec.Model, runID, scenarioID string, seed uint64, replication int) trace.Header {
	minX, minY, maxX, maxY, ok := m.Bounds()
	if !ok {
		minX, minY, maxX, maxY = 0, 0, 100, 100
	}

	h := trace.Header{
		RunID:         runID,
		ScenarioID:    scenarioID,
		ModelName:     m.Name,
		Domain:        string(m.Domain),
		Seed:          seed,
		Replication:   replication,
		EngineVersion: interp.EngineVersion,
		Horizon:       m.Horizon,
		WarmUp:        m.WarmUp,
		// A margin keeps entities that sit slightly outside the node extent
		// representable once positions are quantised for the viewer.
		Bounds:    trace.Bounds{MinX: minX, MinY: minY, MaxX: maxX, MaxY: maxY}.Pad(25),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}

	for _, c := range m.EntityTypes {
		h.Classes = append(h.Classes, trace.ClassInfo{
			ID: c.ID, Label: c.Label, Color: c.Shape.Color, Shape: c.Shape.Type,
			Length: c.Shape.Length, Width: c.Shape.Width, Height: c.Shape.Height,
			Model: c.Shape.Model,
		})
	}
	for _, n := range m.Nodes {
		h.Nodes = append(h.Nodes, trace.NodeInfo{ID: n.ID, Label: n.Label, X: n.X, Y: n.Y, Z: n.Z})
	}

	index := m.BuildIndex()
	for _, r := range m.Resources {
		info := trace.ResourceInfo{ID: r.ID, Label: r.Label, Capacity: r.Capacity, Node: r.Node}
		if node := index.Nodes[r.Node]; node != nil {
			info.X, info.Y = node.X, node.Y
		}
		h.Resources = append(h.Resources, info)
	}
	for _, z := range m.Zones {
		h.Zones = append(h.Zones, trace.ZoneInfo{
			ID: z.ID, Label: z.Label, X: z.X, Y: z.Y,
			Width: z.Width, Height: z.Height, Color: z.Color,
		})
	}
	return h
}

// RunResult is what the server reads once a run finishes.
type RunResult struct {
	Model         spec.Summary            `json:"model"`
	Seed          uint64                  `json:"seed"`
	Replication   int                     `json:"replication"`
	EngineVersion string                  `json:"engineVersion"`
	EndTime       float64                 `json:"endTime"`
	WallSeconds   float64                 `json:"wallSeconds"`
	Created       int                     `json:"created"`
	Completed     int                     `json:"completed"`
	CutShort      int                     `json:"cutShort"`
	Balked        int                     `json:"balked"`
	Reneged       int                     `json:"reneged"`
	StillInSystem int                     `json:"stillInSystem"`
	LimitHit      bool                    `json:"limitHit,omitempty"`
	Resources     []interp.ResourceResult `json:"resources"`
	SystemTime    Percentiles             `json:"systemTime"`
	Waits         map[string]Percentiles  `json:"waits"`
}

// Percentiles summarises a distribution. A mean alone hides the tail, and in a
// queueing system the tail is the part anyone complains about.
type Percentiles struct {
	Count  int     `json:"count"`
	Mean   float64 `json:"mean"`
	Min    float64 `json:"min"`
	P50    float64 `json:"p50"`
	P90    float64 `json:"p90"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	Max    float64 `json:"max"`
	StdDev float64 `json:"stdDev"`
}

func writeResult(path string, m *spec.Model, r *interp.Result, seed uint64, replication int) error {
	out := RunResult{
		Model:         m.Summary(),
		Seed:          seed,
		Replication:   replication,
		EngineVersion: interp.EngineVersion,
		EndTime:       r.EndTime,
		WallSeconds:   r.WallTime.Seconds(),
		Created:       r.Created,
		Completed:     r.Completed,
		CutShort:      r.CutShort,
		Balked:        r.Balked,
		Reneged:       r.Reneged,
		StillInSystem: r.StillInSystem,
		LimitHit:      r.LimitHit,
		Resources:     r.Resources,
		SystemTime:    summarise(r.SystemTimes),
		Waits:         make(map[string]Percentiles, len(r.Waits)),
	}

	for id, values := range r.Waits {
		out.Waits[id] = summarise(values)
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the result: %w", err)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		return fmt.Errorf("write the result: %w", err)
	}
	return nil
}

func summarise(values []float64) Percentiles {
	if len(values) == 0 {
		return Percentiles{}
	}

	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)

	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	mean := sum / float64(len(sorted))

	variance := 0.0
	for _, v := range sorted {
		d := v - mean
		variance += d * d
	}
	variance /= float64(len(sorted))

	return Percentiles{
		Count:  len(sorted),
		Mean:   mean,
		Min:    sorted[0],
		P50:    percentile(sorted, 0.50),
		P90:    percentile(sorted, 0.90),
		P95:    percentile(sorted, 0.95),
		P99:    percentile(sorted, 0.99),
		Max:    sorted[len(sorted)-1],
		StdDev: sqrt(variance),
	}
}

// percentile uses the nearest-rank method on an already sorted slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p*float64(len(sorted)-1) + 0.5)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func sqrt(v float64) float64 {
	if v <= 0 {
		return 0
	}
	// Newton's method converges in a handful of steps and avoids importing
	// math for one call in a file that otherwise needs none.
	x := v
	for i := 0; i < 24; i++ {
		x = 0.5 * (x + v/x)
	}
	return x
}

func progressReporter(quiet bool, horizon float64) func(float64, uint64, uint64) {
	if quiet {
		return nil
	}
	return func(simTime float64, entities, records uint64) {
		percent := 0.0
		if horizon > 0 {
			percent = simTime / horizon * 100
		}
		// A carriage return keeps the progress on one line in a terminal; the
		// server reads stderr line by line and simply sees updates.
		fmt.Fprintf(os.Stderr, "\r  %5.1f%%  t=%-10s entities=%-8d records=%-10d",
			percent, formatSeconds(simTime), entities, records)
	}
}

func printSummary(w *os.File, m *spec.Model, r *interp.Result) {
	fmt.Fprintf(w, "\r%-60s\r", "")
	fmt.Fprintf(w, "finished in %s of wall clock\n", r.WallTime.Round(time.Millisecond))
	fmt.Fprintf(w, "  created   %d\n", r.Created)
	fmt.Fprintf(w, "  completed %d\n", r.Completed)

	if r.CutShort > 0 {
		fmt.Fprintf(w, "  cut short %d (still waiting when the horizon arrived)\n", r.CutShort)
	}
	if r.Balked > 0 {
		fmt.Fprintf(w, "  balked    %d (arrived to a full queue)\n", r.Balked)
	}
	if r.Reneged > 0 {
		fmt.Fprintf(w, "  gave up   %d (waited past the limit)\n", r.Reneged)
	}
	if r.LimitHit {
		fmt.Fprintln(w, "  WARNING: the entity limit stopped arrivals early, so these numbers are a lower bound")
	}

	if len(r.SystemTimes) > 0 {
		p := summarise(r.SystemTimes)
		fmt.Fprintf(w, "  time in system: mean %s, median %s, 95th %s\n",
			formatSeconds(p.Mean), formatSeconds(p.P50), formatSeconds(p.P95))
	}

	fmt.Fprintln(w, "\n  resource               util   avg queue   peak")
	for _, res := range r.Resources {
		fmt.Fprintf(w, "  %-20s  %5.1f%%  %9.2f  %5d\n",
			truncate(res.Label, 20), res.Utilisation*100, res.AvgQueue, res.PeakQueue)
	}
	_ = m
}

func listTemplates() error {
	for _, t := range templates.All() {
		fmt.Printf("%-22s %s\n", t.Key, t.Name)
		fmt.Printf("%-22s %s\n", "", t.Description)
		fmt.Println()
	}
	return nil
}

func describeTemplate(key string) error {
	t, ok := templates.Get(key)
	if !ok {
		return fmt.Errorf("no template named %q; run with -list to see them", key)
	}

	fmt.Printf("%s\n%s\n\n", t.Name, t.Description)

	group := ""
	for _, p := range t.Params {
		if p.Group != group {
			group = p.Group
			fmt.Printf("%s\n", group)
		}
		unit := p.Unit
		if unit != "" {
			unit = " " + unit
		}
		fmt.Printf("  %-22s %g%s  (%g to %g)  %s\n",
			p.ID, p.Default, unit, p.Min, p.Max, p.Label)
		if p.Description != "" {
			fmt.Printf("  %-22s %s\n", "", p.Description)
		}
	}
	return nil
}

// formatSeconds renders a duration the way a planner reads one.
func formatSeconds(s float64) string {
	if s < 60 {
		return fmt.Sprintf("%.1fs", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm%02ds", int(s)/60, int(s)%60)
	}
	return fmt.Sprintf("%dh%02dm", int(s)/3600, (int(s)%3600)/60)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "."
}
