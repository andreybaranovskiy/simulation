package runstore_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/andreybaranovskiy/simulation/internal/analytics"
	"github.com/andreybaranovskiy/simulation/internal/engine/trace"
	"github.com/andreybaranovskiy/simulation/internal/runstore"
)

// A synthetic trace is used rather than a real run, so the expected positions
// are known exactly and a failure points at the artifact pipeline rather than
// at the engine.
func writeTestTrace(t *testing.T, path string) trace.Header {
	t.Helper()

	header := trace.Header{
		RunID:     "artifact-test",
		ModelName: "Test model",
		Domain:    "generic",
		Horizon:   100,
		Bounds:    trace.Bounds{MinX: -10, MinY: -10, MaxX: 110, MaxY: 110},
		Classes:   []trace.ClassInfo{{ID: "item", Label: "Item", Color: "#4c8dff"}},
		Nodes: []trace.NodeInfo{
			{ID: "a", Label: "A", X: 0, Y: 0},
			{ID: "b", Label: "B", X: 100, Y: 0},
		},
		Resources: []trace.ResourceInfo{{ID: "desk", Label: "Desk", Capacity: 1, X: 100}},
	}

	w, err := trace.Create(path, header, trace.Options{})
	if err != nil {
		t.Fatalf("create trace: %v", err)
	}

	// Records are written in time order, because that is what the engine
	// produces: godes processes events chronologically, and the build pass
	// relies on it.
	//
	// Entity 1 crosses from (0,0) to (100,0) between t=0 and t=40, queues
	// until t=60, is served until t=70, then leaves. Its journey spans several
	// chunks and its wait straddles a boundary, which is the case a chunk
	// format that is not self-contained gets wrong.
	w.Spawn(0, 1, 0, 0, 0, 0)
	w.State(0, 1, trace.StateTravelling)
	w.Segment(40, 1, 100, 0, 0)
	w.Resource(0, 0, 0, 0)

	// Entity 2 lives entirely inside the first chunk.
	w.Spawn(12, 2, 0, 0, 0, 0)
	w.State(12, 2, trace.StateTravelling)
	w.Segment(18, 2, 50, 50, 0)
	w.Exit(18, 2)

	w.State(40, 1, trace.StateQueued)
	w.Resource(40, 0, 0, 1)
	// A zero-length segment at the start of the wait, as the engine emits when
	// an entity takes its place in a queue.
	w.Segment(40, 1, 100, 0, 0)

	// The grant boundary, which separates the wait from the service.
	w.Segment(60, 1, 100, 0, 0)
	w.State(60, 1, trace.StateServing)
	w.Resource(60, 0, 1, 0)
	w.Segment(70, 1, 100, 0, 0)
	w.Exit(70, 1)
	w.Resource(70, 0, 0, 0)

	// Entity 3 starts late and is still in the model at the end, so the final
	// chunks must carry it.
	w.Spawn(80, 3, 0, 0, 0, 0)
	w.State(80, 3, trace.StateTravelling)
	w.Segment(100, 3, 100, 100, 0)

	if err := w.Close(trace.Footer{Complete: true, EndTime: 100}); err != nil {
		t.Fatalf("close trace: %v", err)
	}
	return header
}

func buildTestRun(t *testing.T) runstore.Dir {
	t.Helper()

	dir := runstore.Dir(t.TempDir())
	if err := dir.Ensure(); err != nil {
		t.Fatalf("prepare run directory: %v", err)
	}

	writeTestTrace(t, dir.Trace())

	// A result file is optional; without it the build derives what it can from
	// the trace alone, which this test exercises for a subset of the checks.
	result := map[string]any{
		"seed": 1, "endTime": 100.0,
		"created": 3, "completed": 2, "cutShort": 0,
		"systemTime": map[string]any{"count": 2, "mean": 38.0, "p50": 38.0, "p95": 70.0, "max": 70.0},
		"resources": []map[string]any{
			{"ID": "desk", "Label": "Desk", "Capacity": 1, "Seized": 1, "Utilisation": 0.1, "AvgQueue": 0.2, "PeakQueue": 1},
		},
		"waits": map[string]any{
			"desk": map[string]any{"count": 1, "mean": 20.0, "p50": 20.0, "p95": 20.0, "max": 20.0},
		},
	}
	data, _ := json.Marshal(result)
	if err := os.WriteFile(dir.Result(), data, 0o640); err != nil {
		t.Fatalf("write result: %v", err)
	}

	_, err := runstore.Build(dir, runstore.BuildOptions{
		ChunkSeconds: 25,
		Levels:       2,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return dir
}

func TestBuildWritesEverything(t *testing.T) {
	dir := buildTestRun(t)

	for _, name := range []string{
		runstore.ManifestFile,
		filepath.Join(runstore.AggDir, runstore.KPIFile),
		filepath.Join(runstore.AggDir, runstore.SeriesFile),
		filepath.Join(runstore.AggDir, runstore.GanttFile),
		filepath.Join(runstore.AggDir, runstore.PathsFile),
		filepath.Join(runstore.AggDir, runstore.HeatmapsFile),
	} {
		if _, err := os.Stat(dir.Path(name)); err != nil {
			t.Errorf("the build did not produce %s: %v", name, err)
		}
	}
}

func TestManifestDescribesTheRun(t *testing.T) {
	dir := buildTestRun(t)

	m, err := dir.ReadManifest()
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	if m.EndTime != 100 {
		t.Errorf("manifest end time is %g, want 100", m.EndTime)
	}
	if len(m.Levels) != 2 {
		t.Fatalf("built %d levels, want 2", len(m.Levels))
	}
	if m.Levels[0].Stride != 1 {
		t.Errorf("level 0 has stride %d, want every entity", m.Levels[0].Stride)
	}
	if m.Counts.Entities != 3 {
		t.Errorf("manifest counts %d entities, want 3", m.Counts.Entities)
	}
	// Four chunks of 25 seconds cover a 100 second run.
	if m.Levels[0].ChunkCount < 4 {
		t.Errorf("level 0 has %d chunks, want at least 4 to cover the run", m.Levels[0].ChunkCount)
	}
	if len(m.Classes) != 1 || m.Classes[0].Count != 3 {
		t.Errorf("class table is %+v, want one class with 3 entities", m.Classes)
	}
}

// This is the test that matters. If chunk playback and the trace disagree, the
// viewer shows entities in the wrong place, and nothing downstream is
// trustworthy. Positions are reconstructed from the chunks and compared with
// the geometry the trace defines.
func TestChunkPlaybackMatchesTheTrace(t *testing.T) {
	dir := buildTestRun(t)

	m, err := dir.ReadManifest()
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	level := m.Levels[0]

	// Entity 1 travels (0,0) to (100,0) over t=0..40, then holds at (100,0).
	cases := []struct {
		t          float64
		id         uint32
		wantX      float64
		wantY      float64
		wantExists bool
	}{
		{0, 1, 0, 0, true},
		{10, 1, 25, 0, true},
		{20, 1, 50, 0, true},
		{30, 1, 75, 0, true},
		{40, 1, 100, 0, true},
		{50, 1, 100, 0, true}, // waiting, spans a chunk boundary
		{65, 1, 100, 0, true},
		{80, 1, 0, 0, false}, // left at t=70
		{15, 2, 25, 25, true},
		{90, 3, 50, 50, true}, // half-way through its 80..100 crossing
	}

	for _, c := range cases {
		x, y, ok := positionFromChunks(t, dir, level, m.StartTime, c.t, c.id)

		if ok != c.wantExists {
			t.Errorf("at t=%g entity %d present=%v, want %v", c.t, c.id, ok, c.wantExists)
			continue
		}
		if !ok {
			continue
		}
		if math.Abs(x-c.wantX) > 0.5 || math.Abs(y-c.wantY) > 0.5 {
			t.Errorf("at t=%g entity %d is at (%.2f, %.2f), want (%.2f, %.2f)",
				c.t, c.id, x, y, c.wantX, c.wantY)
		}
	}
}

// positionFromChunks does what the viewer will do: load the chunk covering a
// moment, take the live entities and the spans up to that moment, and
// interpolate.
func positionFromChunks(t *testing.T, dir runstore.Dir, level runstore.Level, startTime, at float64, id uint32) (float64, float64, bool) {
	t.Helper()

	index := level.ChunkFor(at, startTime)
	chunk, err := runstore.ReadChunk(dir.Path(level.ChunkPath(index)))
	if err != nil {
		t.Fatalf("read chunk %d: %v", index, err)
	}

	var live *runstore.Live
	for i := range chunk.Live {
		if chunk.Live[i].ID == id {
			live = &chunk.Live[i]
			break
		}
	}

	// An entity that appeared during this window starts from its spawn.
	for _, s := range chunk.Spawns {
		if s.ID == id && s.Time <= at {
			live = &runstore.Live{
				ID: s.ID, Class: s.Class,
				SpanStart: s.Time, SX: s.X, SY: s.Y, SZ: s.Z,
				SpanEnd: s.Time, EX: s.X, EY: s.Y, EZ: s.Z,
			}
		}
	}

	if live == nil {
		return 0, 0, false
	}

	for _, x := range chunk.Exits {
		if x.ID == id && x.Time <= at {
			return 0, 0, false
		}
	}

	// Walk the entity's chain of spans. A span carries only its endpoint,
	// because it begins wherever the previous one ended. So spans are consumed
	// in order, and the walk stops at the first one still in progress at the
	// moment being asked about.
	//
	// This is the algorithm the viewer runs, which is why it is spelled out
	// here rather than hidden behind a helper.
	current := *live
	for _, s := range chunk.Spans {
		if s.ID != id {
			continue
		}
		if at < current.SpanEnd {
			// Still part-way through the current span; later ones have not
			// started yet.
			break
		}
		current = runstore.Live{
			ID: id, Class: current.Class, State: current.State,
			SpanStart: current.SpanEnd,
			SX:        current.EX, SY: current.EY, SZ: current.EZ,
			SpanEnd: s.EndTime,
			EX:      s.X, EY: s.Y, EZ: s.Z,
		}
	}

	x, y, _ := current.PositionAt(at)
	return x, y, true
}

func TestChunksAreSelfContained(t *testing.T) {
	dir := buildTestRun(t)

	m, _ := dir.ReadManifest()
	level := m.Levels[0]

	// Entity 1 is in the model from t=0 to t=70, so it must appear either as a
	// live entity or as a spawn in every chunk covering that period. Without
	// that a viewer seeking into the middle of the run would see nothing.
	for i := 0; i < 3; i++ {
		chunk, err := runstore.ReadChunk(dir.Path(level.ChunkPath(i)))
		if err != nil {
			t.Fatalf("read chunk %d: %v", i, err)
		}

		found := false
		for _, l := range chunk.Live {
			if l.ID == 1 {
				found = true
			}
		}
		for _, s := range chunk.Spawns {
			if s.ID == 1 {
				found = true
			}
		}

		if !found {
			t.Errorf("chunk %d covering t=%g to %g does not mention entity 1, which is in the model throughout",
				i, chunk.Header.StartTime, chunk.Header.EndTime)
		}
	}
}

// Higher levels thin entities out for an overview. The thinning must be
// consistent, or an entity would blink in and out as the viewer scrubs.
func TestLevelStrideIsStable(t *testing.T) {
	dir := buildTestRun(t)

	m, _ := dir.ReadManifest()
	if len(m.Levels) < 2 {
		t.Skip("only one level was built")
	}
	level := m.Levels[1]

	seen := map[uint32]bool{}
	for i := 0; i < level.ChunkCount; i++ {
		chunk, err := runstore.ReadChunk(dir.Path(level.ChunkPath(i)))
		if err != nil {
			t.Fatalf("read chunk %d: %v", i, err)
		}
		for _, l := range chunk.Live {
			seen[l.ID] = true
		}
		for _, s := range chunk.Spawns {
			seen[s.ID] = true
		}
	}

	for id := range seen {
		if int(id)%level.Stride != 0 {
			t.Errorf("entity %d appears in a level with stride %d, which should not keep it",
				id, level.Stride)
		}
	}
}

func TestHeatmapsCoverTheRightGround(t *testing.T) {
	dir := buildTestRun(t)

	data, err := os.ReadFile(dir.Agg(runstore.HeatmapsFile))
	if err != nil {
		t.Fatalf("read heatmaps: %v", err)
	}

	var file runstore.HeatmapFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("parse heatmaps: %v", err)
	}

	layers := map[string]runstore.HeatmapLayer{}
	for _, l := range file.Layers {
		layers[l.Metric] = l
		if len(l.Totals) != file.Cols*file.Rows {
			t.Errorf("layer %s has %d cells, want %d", l.Metric, len(l.Totals), file.Cols*file.Rows)
		}
	}

	traffic, ok := layers[string(analytics.MetricTraffic)]
	if !ok {
		t.Fatal("no traffic layer was built")
	}

	// Entity 1 covers 100 m, entity 2 covers about 70.7 m, entity 3 covers
	// about 141.4 m. The traffic layer should total close to that.
	const want = 100 + 70.71 + 141.42
	if math.Abs(traffic.Total-want) > want*0.05 {
		t.Errorf("traffic totals %.1f m, want about %.1f m", traffic.Total, want)
	}

	// Entity 1 waits 20 seconds queued, which is the only congestion there is.
	if congestion, ok := layers[string(analytics.MetricCongestion)]; ok {
		if math.Abs(congestion.Total-20) > 1 {
			t.Errorf("congestion totals %.1f s, want 20 s from the single wait", congestion.Total)
		}
	} else {
		t.Error("no congestion layer was built, but an entity spent 20 seconds queued")
	}

	// Scaling to the maximum washes a heatmap out, so the scale must be the
	// percentile rather than the peak whenever they differ.
	if traffic.Scale > traffic.Max {
		t.Errorf("traffic scale %.2f is above its maximum %.2f", traffic.Scale, traffic.Max)
	}
}

func TestKPIsAreUsable(t *testing.T) {
	dir := buildTestRun(t)

	data, err := os.ReadFile(dir.Agg(runstore.KPIFile))
	if err != nil {
		t.Fatalf("read KPIs: %v", err)
	}

	var set analytics.KPISet
	if err := json.Unmarshal(data, &set); err != nil {
		t.Fatalf("parse KPIs: %v", err)
	}

	if len(set.KPIs) == 0 {
		t.Fatal("no KPIs were produced")
	}

	byKey := map[string]analytics.KPI{}
	for _, k := range set.KPIs {
		byKey[k.Key] = k

		if k.Label == "" {
			t.Errorf("KPI %q has no label", k.Key)
		}
		if k.Group == "" {
			t.Errorf("KPI %q has no group, so the dashboard cannot place it", k.Key)
		}
		if math.IsNaN(k.Value) || math.IsInf(k.Value, 0) {
			t.Errorf("KPI %q has a value that is not a number", k.Key)
		}
	}

	// These keys are the contract scenario comparison joins on.
	for _, required := range []string{"completed", "throughput", "wip.mean", "wip.peak"} {
		if _, ok := byKey[required]; !ok {
			t.Errorf("the KPI set is missing %q, which comparison depends on", required)
		}
	}

	if len(set.Headlines()) == 0 {
		t.Error("no KPI is marked as a headline, so a summary card would be empty")
	}
}

func TestGanttCoversEveryResource(t *testing.T) {
	dir := buildTestRun(t)

	data, err := os.ReadFile(dir.Agg(runstore.GanttFile))
	if err != nil {
		t.Fatalf("read gantt: %v", err)
	}

	var rows []analytics.GanttRow
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("parse gantt: %v", err)
	}

	if len(rows) != 1 {
		t.Fatalf("got %d gantt rows, want one per declared resource", len(rows))
	}

	row := rows[0]
	if row.ID != "desk" {
		t.Errorf("gantt row is for %q, want the desk", row.ID)
	}
	if len(row.Intervals) == 0 {
		t.Error("the desk has no intervals, so its bar would be blank")
	}

	// The three fractions describe the whole run, so they must add up.
	total := row.Busy + row.Idle + row.Down
	if math.Abs(total-1) > 0.01 {
		t.Errorf("busy %.3f + idle %.3f + down %.3f = %.3f, want 1",
			row.Busy, row.Idle, row.Down, total)
	}
}

func TestSeriesAreBucketed(t *testing.T) {
	dir := buildTestRun(t)

	data, err := os.ReadFile(dir.Agg(runstore.SeriesFile))
	if err != nil {
		t.Fatalf("read series: %v", err)
	}

	var series []analytics.Series
	if err := json.Unmarshal(data, &series); err != nil {
		t.Fatalf("parse series: %v", err)
	}

	byKey := map[string]analytics.Series{}
	for _, s := range series {
		byKey[s.Key] = s
		if len(s.Values) == 0 {
			t.Errorf("series %q has no values", s.Key)
		}
		if s.BucketSeconds <= 0 {
			t.Errorf("series %q has no bucket width, so a chart has no x axis", s.Key)
		}
	}

	arrivals, ok := byKey["arrivals"]
	if !ok {
		t.Fatal("no arrivals series was produced")
	}

	sum := 0.0
	for _, v := range arrivals.Values {
		sum += v
	}
	if sum != 3 {
		t.Errorf("the arrivals series totals %g, but three entities were created", sum)
	}
}

func TestPathsAreSampled(t *testing.T) {
	dir := buildTestRun(t)

	data, err := os.ReadFile(dir.Agg(runstore.PathsFile))
	if err != nil {
		t.Fatalf("read paths: %v", err)
	}

	var result analytics.PathsResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parse paths: %v", err)
	}

	if result.Total != 3 {
		t.Errorf("paths saw %d entities, want 3", result.Total)
	}
	if len(result.Paths) == 0 {
		t.Fatal("no paths were kept, so the spaghetti view would be empty")
	}

	for _, p := range result.Paths {
		if len(p.Points)%2 != 0 {
			t.Errorf("path %d has an odd number of coordinates", p.ID)
		}
		if len(p.Points) < 4 {
			t.Errorf("path %d has fewer than two points, which draws nothing", p.ID)
		}
		if p.Distance <= 0 {
			t.Errorf("path %d has no length", p.ID)
		}
	}
}
