// Package runstore owns a run's artifacts on disk: what a finished simulation
// leaves behind and how the viewer reads it back.
//
// The engine writes one trace. That trace is an intermediate: it is ordered by
// simulation events, which is the wrong shape for a viewer that needs to jump
// to an arbitrary moment. The build pass in this package turns it into
// time-bucketed chunks the browser can fetch by window, plus the aggregates
// the dashboards and reports read.
//
// Nothing here loads a whole run into memory, in either direction.
package runstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// File and directory names inside a run. They are fixed so the server can
// serve them by path without a lookup.
const (
	TraceFile    = "run.trace"
	ResultFile   = "result.json"
	ModelFile    = "model.json"
	ManifestFile = "manifest.json"

	FramesDir = "frames"
	AggDir    = "agg"

	KPIFile      = "kpi.json"
	SeriesFile   = "series.json"
	GanttFile    = "gantt.json"
	PathsFile    = "paths.json"
	HeatmapsFile = "heatmaps.json"
)

// ManifestVersion is bumped when the artifact layout changes, so a viewer
// refuses a run it cannot read rather than misdrawing it.
const ManifestVersion = 1

// Manifest is the index the viewer reads first. It describes the whole run in
// one small file: what is in it, how it is chunked, and what else is available.
type Manifest struct {
	Version int    `json:"version"`
	RunID   string `json:"runId"`

	ModelName string `json:"modelName"`
	Domain    string `json:"domain,omitempty"`

	Seed          uint64 `json:"seed"`
	Replication   int    `json:"replication"`
	EngineVersion string `json:"engineVersion"`
	BuiltAt       string `json:"builtAt"`

	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`
	WarmUp    float64 `json:"warmUp,omitempty"`

	Bounds Bounds `json:"bounds"`

	// Levels describe the playback chunks. Level 0 has every entity; higher
	// levels thin them out for an overview of a crowded run.
	Levels []Level `json:"levels"`

	// Static tables the viewer needs to draw anything.
	Classes   []ClassInfo    `json:"classes"`
	Nodes     []NodeInfo     `json:"nodes"`
	Resources []ResourceInfo `json:"resources"`
	Zones     []ZoneInfo     `json:"zones,omitempty"`

	Counts Counts `json:"counts"`

	// Heatmaps lists the grids that were built, so the UI offers only metrics
	// that exist for this run.
	Heatmaps []HeatmapInfo `json:"heatmaps,omitempty"`

	// Available flags which aggregate files were produced.
	Available Available `json:"available"`

	// Warnings carry anything the reader should know, such as a run that hit
	// its entity limit and is therefore a lower bound.
	Warnings []string `json:"warnings,omitempty"`
}

type Bounds struct {
	MinX float64 `json:"minX"`
	MinY float64 `json:"minY"`
	MinZ float64 `json:"minZ"`
	MaxX float64 `json:"maxX"`
	MaxY float64 `json:"maxY"`
	MaxZ float64 `json:"maxZ"`
}

func (b Bounds) Width() float64  { return b.MaxX - b.MinX }
func (b Bounds) Height() float64 { return b.MaxY - b.MinY }

// Level is one playback fidelity.
type Level struct {
	Index int `json:"index"`
	// Stride is the entity thinning factor. Level 0 has stride 1, meaning
	// every entity. A level with stride 4 keeps one entity in four, chosen by
	// id so an entity never flickers between chunks.
	Stride int `json:"stride"`
	// ChunkSeconds is how much simulated time one chunk file covers.
	ChunkSeconds float64 `json:"chunkSeconds"`
	ChunkCount   int     `json:"chunkCount"`
	// Bytes is the level's total size, so the viewer can choose a level it can
	// afford to stream.
	Bytes int64 `json:"bytes"`
	// PeakLive is the largest number of entities on screen at once in this
	// level, which is what decides whether a browser can render it.
	PeakLive int `json:"peakLive"`
}

// ChunkPath is where a chunk lives, relative to the run directory.
func (l Level) ChunkPath(index int) string {
	return fmt.Sprintf("%s/lod%d/%05d.bin", FramesDir, l.Index, index)
}

// ChunkFor returns the chunk index covering a moment.
func (l Level) ChunkFor(t, startTime float64) int {
	if l.ChunkSeconds <= 0 {
		return 0
	}
	idx := int((t - startTime) / l.ChunkSeconds)
	if idx < 0 {
		return 0
	}
	if idx >= l.ChunkCount {
		return l.ChunkCount - 1
	}
	return idx
}

type ClassInfo struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Color  string  `json:"color"`
	Shape  string  `json:"shape"`
	Length float64 `json:"length"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Model  string  `json:"model,omitempty"`
	// Count is how many of this class the run created.
	Count int `json:"count"`
}

type NodeInfo struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Z     float64 `json:"z"`
}

type ResourceInfo struct {
	ID       string  `json:"id"`
	Label    string  `json:"label"`
	Capacity int     `json:"capacity"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
}

type ZoneInfo struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Color  string  `json:"color,omitempty"`
}

type Counts struct {
	Entities   int    `json:"entities"`
	Records    uint64 `json:"records"`
	Spans      uint64 `json:"spans"`
	Chunks     int    `json:"chunks"`
	TotalBytes int64  `json:"totalBytes"`
}

// HeatmapInfo describes one built grid.
type HeatmapInfo struct {
	Metric string `json:"metric"`
	Label  string `json:"label"`
	Unit   string `json:"unit"`
	// Description says what the numbers mean, because a heatmap with no
	// explanation is a picture rather than a finding.
	Description string `json:"description"`
	Cols        int    `json:"cols"`
	Rows        int    `json:"rows"`
	// CellMeters is the grid resolution on the ground.
	CellMeters float64 `json:"cellMeters"`
	// Buckets is how many time slices the metric was split into, so the UI can
	// scrub a heatmap through the run rather than only showing a total.
	Buckets       int     `json:"buckets"`
	BucketSeconds float64 `json:"bucketSeconds"`
	Max           float64 `json:"max"`
	Total         float64 `json:"total"`
}

type Available struct {
	Series   bool `json:"series"`
	Gantt    bool `json:"gantt"`
	Paths    bool `json:"paths"`
	Heatmaps bool `json:"heatmaps"`
	KPIs     bool `json:"kpis"`
}

// Dir is a run's directory on disk.
type Dir string

func (d Dir) Path(parts ...string) string {
	return filepath.Join(append([]string{string(d)}, parts...)...)
}

func (d Dir) Trace() string    { return d.Path(TraceFile) }
func (d Dir) Manifest() string { return d.Path(ManifestFile) }
func (d Dir) Result() string   { return d.Path(ResultFile) }
func (d Dir) Model() string    { return d.Path(ModelFile) }
func (d Dir) Agg(name string) string {
	return d.Path(AggDir, name)
}

func (d Dir) Ensure() error {
	for _, sub := range []string{"", AggDir, FramesDir} {
		if err := os.MkdirAll(d.Path(sub), 0o750); err != nil {
			return fmt.Errorf("create run directory: %w", err)
		}
	}
	return nil
}

// WriteManifest stores the index. It is written last, so its presence is the
// signal that a run finished building.
func (d Dir) WriteManifest(m *Manifest) error {
	m.Version = ManifestVersion
	m.BuiltAt = time.Now().UTC().Format(time.RFC3339)
	return writeJSON(d.Manifest(), m)
}

// ReadManifest loads a run's index.
func (d Dir) ReadManifest() (*Manifest, error) {
	data, err := os.ReadFile(d.Manifest())
	if err != nil {
		return nil, err
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse the run manifest: %w", err)
	}
	if m.Version != ManifestVersion {
		return nil, fmt.Errorf("this build reads run format %d, the run is format %d",
			ManifestVersion, m.Version)
	}
	return &m, nil
}

// Built reports whether a run has a manifest, which is the marker that its
// artifacts are complete.
func (d Dir) Built() bool {
	_, err := os.Stat(d.Manifest())
	return err == nil
}

func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

// dirSize totals a directory tree, used to report what a run costs on disk.
func dirSize(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}
