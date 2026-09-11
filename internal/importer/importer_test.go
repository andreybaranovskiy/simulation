package importer_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreybaranovskiy/simulation/internal/importer"
	"github.com/andreybaranovskiy/simulation/internal/runstore"
)

const sample = `{
  "animation": {"name": "Test animation", "time_scale": 1.0},
  "objects": [
    {"id": 1, "type": "container_truck", "x": 0, "y": 0, "z": 0, "color": "red"},
    {"id": 2, "type": "container_truck", "x": 0, "y": 0, "z": 10, "color": "black"},
    {"id": 3, "type": "crane", "x": 50, "y": 0, "z": 50, "color": "blue"}
  ],
  "transition": [
    {"time": 10, "objId": 1, "type": "move", "x": 100, "y": 0, "z": 0},
    {"time": 10, "objId": 1, "type": "rotation", "x": 0, "y": 90, "z": 0},
    {"time": 20, "objId": 1, "type": "move", "x": 200, "y": 0, "z": 0},
    {"time": 15, "objId": 2, "type": "move", "x": 100, "y": 0, "z": 10},
    {"time": 30, "objId": 3, "type": "move", "x": 60, "y": 0, "z": 50}
  ]
}`

func importSample(t *testing.T, body string) (runstore.Dir, *importer.Result) {
	t.Helper()

	dir := runstore.Dir(t.TempDir())
	result, err := importer.Import(strings.NewReader(body), dir, "import-test",
		runstore.BuildOptions{
			ChunkSeconds: 10,
			Levels:       1,
			Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return dir, result
}

func TestImportReadsTheLegacyFormat(t *testing.T) {
	_, result := importSample(t, sample)

	if result.Name != "Test animation" {
		t.Errorf("name is %q", result.Name)
	}
	if result.Objects != 3 {
		t.Errorf("imported %d objects, want 3", result.Objects)
	}
	if result.Moves != 4 {
		t.Errorf("imported %d moves, want 4", result.Moves)
	}
	// The rotation transition is not a movement and must be counted as ignored
	// rather than silently dropped.
	if result.Ignored != 1 {
		t.Errorf("ignored %d transitions, want 1 rotation", result.Ignored)
	}
	if result.Duration != 30 {
		t.Errorf("duration is %g, want 30", result.Duration)
	}

	// Two truck colours and a crane make three classes, so the legend
	// distinguishes them.
	if len(result.Classes) != 3 {
		t.Errorf("built %d classes, want 3: %v", len(result.Classes), result.Classes)
	}
}

func TestImportProducesPlayableArtifacts(t *testing.T) {
	dir, _ := importSample(t, sample)

	m, err := dir.ReadManifest()
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	if m.Counts.Entities != 3 {
		t.Errorf("manifest counts %d entities, want 3", m.Counts.Entities)
	}
	if m.EndTime != 30 {
		t.Errorf("manifest end time is %g, want 30", m.EndTime)
	}
	if len(m.Levels) == 0 || m.Levels[0].ChunkCount == 0 {
		t.Fatal("no playback chunks were produced")
	}

	// An imported animation must be indistinguishable from a simulated run to
	// everything downstream, which means the aggregates have to exist.
	for _, name := range []string{runstore.KPIFile, runstore.SeriesFile, runstore.PathsFile} {
		if _, err := os.Stat(dir.Path(runstore.AggDir, name)); err != nil {
			t.Errorf("the import produced no %s", name)
		}
	}

	// The traffic heatmap should have something in it: three objects moved.
	if len(m.Heatmaps) == 0 {
		t.Error("the import produced no heatmaps")
	}
}

func TestImportedPositionsAreRight(t *testing.T) {
	dir, _ := importSample(t, sample)

	m, _ := dir.ReadManifest()
	level := m.Levels[0]

	// Object 1 travels from (0,0) to (100,0) over the first ten seconds, so at
	// t=5 it should be half-way.
	chunk, err := runstore.ReadChunk(dir.Path(level.ChunkPath(0)))
	if err != nil {
		t.Fatalf("read chunk: %v", err)
	}

	var found bool
	for _, s := range chunk.Spans {
		if s.ID == 1 && s.EndTime == 10 {
			found = true
			if s.X != 100 {
				t.Errorf("object 1 ends its first span at x=%g, want 100", s.X)
			}
		}
	}
	if !found {
		t.Error("object 1's first movement is missing from the opening chunk")
	}
}

func TestImportRejectsRubbish(t *testing.T) {
	dir := runstore.Dir(t.TempDir())
	opts := runstore.BuildOptions{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if _, err := importer.Import(strings.NewReader("not json at all"), dir, "x", opts); err == nil {
		t.Error("Import accepted something that is not JSON")
	}
	if _, err := importer.Import(strings.NewReader(`{"objects":[]}`), dir, "x", opts); err == nil {
		t.Error("Import accepted a file with no objects")
	}
}

// Ids may be numbers or strings in the wild, and a transition that names an
// object that does not exist must not abort the import.
func TestImportHandlesAwkwardIds(t *testing.T) {
	const awkward = `{
      "animation": {"name": "Mixed ids"},
      "objects": [
        {"id": "truck-a", "type": "truck", "x": 0, "y": 0, "z": 0},
        {"id": 7, "type": "truck", "x": 0, "y": 0, "z": 5}
      ],
      "transition": [
        {"time": 5, "objId": "truck-a", "type": "move", "x": 10, "y": 0, "z": 0},
        {"time": 5, "objId": 7, "type": "move", "x": 10, "y": 0, "z": 5},
        {"time": 6, "objId": 999, "type": "move", "x": 1, "y": 0, "z": 1}
      ]
    }`

	_, result := importSample(t, awkward)

	if result.Moves != 2 {
		t.Errorf("imported %d moves, want 2", result.Moves)
	}
	if result.Ignored != 1 {
		t.Errorf("ignored %d transitions, want 1 for the unknown object", result.Ignored)
	}
}

// The real demo file is the format this importer exists for, so it is the
// thing worth testing against when it is present.
func TestImportTheRealDemoFile(t *testing.T) {
	path := filepath.Join("..", "..", "samples", "demo.json")
	file, err := os.Open(path)
	if err != nil {
		t.Skipf("samples/demo.json is not present: %v", err)
	}
	defer file.Close()

	dir := runstore.Dir(t.TempDir())
	result, err := importer.Import(file, dir, "demo-import", runstore.BuildOptions{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("import samples/demo.json: %v", err)
	}

	t.Logf("%d objects, %d transitions, %d moves, %.0fs",
		result.Objects, result.Transitions, result.Moves, result.Duration)

	if result.Objects == 0 || result.Moves == 0 {
		t.Fatal("the demo file imported nothing")
	}

	m, err := dir.ReadManifest()
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if m.Counts.Entities != result.Objects {
		t.Errorf("manifest counts %d entities, the import reported %d",
			m.Counts.Entities, result.Objects)
	}

	data, err := os.ReadFile(dir.Agg(runstore.KPIFile))
	if err != nil {
		t.Fatalf("read KPIs: %v", err)
	}
	var kpis struct {
		KPIs []struct{ Key string } `json:"kpis"`
	}
	if err := json.Unmarshal(data, &kpis); err != nil || len(kpis.KPIs) == 0 {
		t.Error("the imported demo produced no KPIs")
	}

	t.Logf("artifacts: %d chunks, %d bytes", m.Counts.Chunks, m.Counts.TotalBytes)
}
