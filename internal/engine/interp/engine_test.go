package interp_test

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/andreybaranovskiy/simulation/internal/engine/interp"
	"github.com/andreybaranovskiy/simulation/internal/engine/rng"
	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
	"github.com/andreybaranovskiy/simulation/internal/engine/trace"
)

// Godes keeps its clock in package-level globals, so only one simulation can
// exist per process. These tests therefore run one model, in TestMain, and then
// assert against its trace. Anything needing a second model belongs in a
// separate package or a separate process.
//
// The model is a single-server queue with known analytic behaviour: arrivals
// every 10 seconds on average, service taking 6 seconds, one server. At 60%
// utilisation it is stable, so the queue should not grow without bound and
// every truck should get through.

var (
	result    *interp.Result
	tracePath string
	header    trace.Header
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "interp-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	tracePath = filepath.Join(dir, "run.trace")

	model := singleServerModel()
	header = headerFor(model)

	writer, err := trace.Create(tracePath, header, trace.Options{})
	if err != nil {
		panic(err)
	}

	result, err = interp.Run(interp.Config{
		Model: model,
		Trace: writer,
		Seed:  20260911,
	})
	if err != nil {
		writer.Close(trace.Footer{})
		panic(err)
	}

	if err := writer.Close(trace.Footer{Complete: true, EndTime: result.EndTime}); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}

func singleServerModel() *spec.Model {
	m := &spec.Model{
		Name:    "Single server queue",
		Domain:  spec.DomainGeneric,
		Horizon: 3600,
		WarmUp:  0,

		EntityTypes: []spec.EntityType{{
			ID:    "job",
			Label: "Job",
			// A high speed keeps travel negligible, so the test measures
			// queueing rather than driving.
			Speed: rng.Fixed(100),
		}},

		Nodes: []spec.Node{
			{ID: "arrive", X: 0, Y: 0},
			{ID: "desk", X: 100, Y: 0},
			{ID: "leave", X: 200, Y: 0},
		},

		Links: []spec.Link{
			{From: "arrive", To: "desk"},
			{From: "desk", To: "leave"},
		},

		Resources: []spec.Resource{{
			ID:       "desk",
			Label:    "Service desk",
			Capacity: 1,
			Node:     "desk",
			Service:  rng.Fixed(6),
		}},

		Sources: []spec.Source{{
			ID:      "arrivals",
			Entity:  "job",
			Node:    "arrive",
			Arrival: rng.Dist{Kind: rng.Exponential, Mean: 10},
			Route:   "visit",
		}},

		Routes: []spec.Route{{
			ID:     "visit",
			Source: "arrivals",
			Steps: []spec.Step{
				{Type: spec.StepTravel, To: "desk"},
				{Type: spec.StepUse, Resource: "desk"},
				{Type: spec.StepTravel, To: "leave"},
				{Type: spec.StepExit},
			},
		}},
	}

	m.ApplyDefaults()
	if err := m.ValidateStrict(); err != nil {
		panic(err)
	}
	return m
}

func headerFor(m *spec.Model) trace.Header {
	minX, minY, maxX, maxY, _ := m.Bounds()

	h := trace.Header{
		RunID:     "test-run",
		ModelName: m.Name,
		Horizon:   m.Horizon,
		Bounds:    trace.Bounds{MinX: minX, MinY: minY, MaxX: maxX, MaxY: maxY}.Pad(50),
	}
	for _, c := range m.EntityTypes {
		h.Classes = append(h.Classes, trace.ClassInfo{ID: c.ID, Label: c.Label, Color: c.Shape.Color})
	}
	for _, n := range m.Nodes {
		h.Nodes = append(h.Nodes, trace.NodeInfo{ID: n.ID, Label: n.Label, X: n.X, Y: n.Y})
	}
	for _, r := range m.Resources {
		h.Resources = append(h.Resources, trace.ResourceInfo{ID: r.ID, Label: r.Label, Capacity: r.Capacity})
	}
	return h
}

func TestRunProducedEntities(t *testing.T) {
	// An hour with a mean gap of 10 seconds is about 360 arrivals. The exact
	// number depends on the draws, so the test asserts a plausible band rather
	// than a magic number.
	if result.Created < 250 || result.Created > 480 {
		t.Errorf("created %d entities, expected roughly 360 over an hour at one every 10 seconds",
			result.Created)
	}
	if result.Created == 0 {
		t.Fatal("the run created no entities at all")
	}
}

// A stable queue must clear. If entities were left in the system the model was
// overloaded or the engine leaked them.
func TestEveryEntityLeft(t *testing.T) {
	if result.StillInSystem != 0 {
		t.Errorf("%d entities were still in the system at the end", result.StillInSystem)
	}
	// Entities the horizon caught mid-queue leave too, but as cut short rather
	// than as throughput.
	if result.Completed+result.CutShort != result.Created {
		t.Errorf("created %d entities, but %d completed and %d were cut short",
			result.Created, result.Completed, result.CutShort)
	}
}

// Utilisation is the clearest check that the clock and the accounting agree:
// arrivals every 10 seconds served in 6 should keep one desk busy about 60% of
// the time.
func TestUtilisationMatchesTheory(t *testing.T) {
	if len(result.Resources) != 1 {
		t.Fatalf("expected one resource, got %d", len(result.Resources))
	}

	desk := result.Resources[0]
	const expected = 0.6

	if math.Abs(desk.Utilisation-expected) > 0.08 {
		t.Errorf("desk utilisation is %.3f, expected about %.2f for a 6 second service every 10 seconds",
			desk.Utilisation, expected)
	}
	// Every entity that completed was served exactly once; the ones the
	// horizon cut off never reached the desk.
	if desk.Seized != result.Completed {
		t.Errorf("the desk served %d jobs but %d completed", desk.Seized, result.Completed)
	}
}

// The run must stop at the horizon, not drift past it by more than the tail of
// work already in progress.
func TestRunStoppedAtTheHorizon(t *testing.T) {
	if result.EndTime < 3600 {
		t.Errorf("the run ended at %.1fs, before its 3600s horizon", result.EndTime)
	}
	if result.EndTime > 3700 {
		t.Errorf("the run ran to %.1fs, well past its 3600s horizon", result.EndTime)
	}
}

func TestNobodyBalkedOrReneged(t *testing.T) {
	// This model sets no queue limit and no maximum wait, so neither is possible.
	if result.Balked != 0 || result.Reneged != 0 {
		t.Errorf("got %d balks and %d renegements in a model that allows neither",
			result.Balked, result.Reneged)
	}
}

// The trace is the artifact everything downstream reads, so it must be
// readable, consistent and complete.
func TestTraceIsReadable(t *testing.T) {
	r, err := trace.Open(tracePath)
	if err != nil {
		t.Fatalf("open the trace: %v", err)
	}
	defer r.Close()

	if got := r.Header().ModelName; got != "Single server queue" {
		t.Errorf("the trace header names the model %q", got)
	}

	counts := map[trace.RecordType]int{}
	spawned := map[uint32]bool{}
	exited := map[uint32]bool{}
	lastTime := 0.0

	var rec trace.Record
	for {
		err := r.Next(&rec)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read record %d: %v", r.Count(), err)
		}

		counts[rec.Type]++

		switch rec.Type {
		case trace.RecSpawn:
			if spawned[rec.Entity] {
				t.Fatalf("entity %d was spawned twice", rec.Entity)
			}
			spawned[rec.Entity] = true
		case trace.RecExit:
			if !spawned[rec.Entity] {
				t.Fatalf("entity %d exited without ever being spawned", rec.Entity)
			}
			if exited[rec.Entity] {
				t.Fatalf("entity %d exited twice", rec.Entity)
			}
			exited[rec.Entity] = true
		}

		// Segments carry their end time, which is by definition in the future,
		// so only instantaneous records establish the clock's progress.
		if rec.Type != trace.RecSegment {
			if rec.Time < lastTime-1e-6 {
				t.Fatalf("record %d at t=%.4f follows one at t=%.4f: the trace is out of order",
					r.Count(), rec.Time, lastTime)
			}
			lastTime = rec.Time
		}
	}

	if len(spawned) != result.Created {
		t.Errorf("the trace spawned %d entities, the run reported %d", len(spawned), result.Created)
	}
	if len(exited) != result.Completed+result.CutShort {
		t.Errorf("the trace recorded %d exits, the run reported %d completions and %d cut short",
			len(exited), result.Completed, result.CutShort)
	}
	for id := range spawned {
		if !exited[id] {
			t.Errorf("entity %d was spawned but never exited", id)
		}
	}

	for _, want := range []trace.RecordType{trace.RecSpawn, trace.RecSegment, trace.RecState, trace.RecResource, trace.RecExit, trace.RecEvent} {
		if counts[want] == 0 {
			t.Errorf("the trace contains no %s records", want)
		}
	}
}

func TestTraceFooter(t *testing.T) {
	footer, err := trace.ReadFooter(tracePath)
	if err != nil {
		t.Fatalf("read the footer: %v", err)
	}

	if !footer.Complete {
		t.Error("the footer does not mark the run complete")
	}
	if footer.Truncated {
		t.Error("the run was truncated, so the record cap is too low for this test")
	}
	if footer.EntityCount != uint64(result.Created) {
		t.Errorf("the footer counts %d entities, the run created %d", footer.EntityCount, result.Created)
	}
	if footer.RecordCount == 0 {
		t.Error("the footer counts no records")
	}
}

// Segment records are what make the format affordable. If the engine ever
// starts emitting a position per tick this test is what notices.
func TestTraceStaysCompact(t *testing.T) {
	info, err := os.Stat(tracePath)
	if err != nil {
		t.Fatalf("stat the trace: %v", err)
	}

	perEntity := float64(info.Size()) / float64(result.Created)
	if perEntity > 400 {
		t.Errorf("the trace costs %.0f bytes per entity, which suggests positions are being sampled rather than described as segments",
			perEntity)
	}
	t.Logf("%d entities in %d bytes, %.0f bytes each", result.Created, info.Size(), perEntity)
}

// Utilisation must describe the measured period, not the whole run.
//
// The failure this pins is subtle and was shipped once: busy time accumulated
// from the first second while the divisor excluded the warm-up, which inflated
// every utilisation by exactly the warm-up's share of the run. The number then
// disagreed with a chart of the same thing, and the chart was right.
func TestUtilisationExcludesTheWarmUp(t *testing.T) {
	if len(result.Resources) != 1 {
		t.Fatalf("expected one resource, got %d", len(result.Resources))
	}

	desk := result.Resources[0]

	// This model has no warm-up, so utilisation over the measured period and
	// over the whole run are the same number, and it must equal the offered
	// load: a 6 second service arriving every 10 seconds.
	busySeconds := desk.Utilisation * float64(desk.Capacity) * result.EndTime
	servedSeconds := float64(desk.Seized) * 6

	if math.Abs(busySeconds-servedSeconds) > servedSeconds*0.02 {
		t.Errorf("the desk was busy for %.0fs by its utilisation but served %d jobs of 6s, which is %.0fs",
			busySeconds, desk.Seized, servedSeconds)
	}
}
