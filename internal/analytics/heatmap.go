// Package analytics turns a run's event trace into the numbers and grids the
// dashboards, heatmaps and reports display.
//
// Every accumulator here is fed by a single streaming pass over the trace, so
// a run of tens of millions of events costs one read and a bounded amount of
// memory. None of them holds the trace.
package analytics

import (
	"math"
	"sort"
)

// Metric names a heatmap layer.
type Metric string

const (
	// Traffic is distance travelled through each cell. It shows the routes
	// that are actually used, as opposed to the ones that were drawn.
	MetricTraffic Metric = "traffic"
	// Dwell is time spent stationary in each cell. It shows where work and
	// waiting happen, which traffic alone hides.
	MetricDwell Metric = "dwell"
	// Occupancy is entity-seconds per cell, moving or not: overall crowding.
	MetricOccupancy Metric = "occupancy"
	// Congestion is time spent queued or blocked. It is the layer that answers
	// where the problem is, rather than where the activity is.
	MetricCongestion Metric = "congestion"
)

// MetricInfo describes a layer for the UI. A heatmap with no explanation is a
// picture; the description is what makes it a finding.
type MetricInfo struct {
	Metric      Metric
	Label       string
	Unit        string
	Description string
}

func Metrics() []MetricInfo {
	return []MetricInfo{
		{MetricTraffic, "Traffic", "m", "Distance travelled through each cell. Shows the routes actually used."},
		{MetricDwell, "Dwell time", "s", "Time spent stationary in each cell. Shows where work and waiting happen."},
		{MetricOccupancy, "Occupancy", "entity-s", "Entity-seconds in each cell, moving or not. Overall crowding."},
		{MetricCongestion, "Congestion", "s", "Time spent queued or blocked. Where the delay is, not where the activity is."},
	}
}

// GridSpec fixes a heatmap's geometry and time slicing.
type GridSpec struct {
	MinX, MinY float64
	Cols, Rows int
	// CellMeters is the resolution on the ground.
	CellMeters float64
	// Buckets slices the run in time so the UI can scrub a heatmap rather than
	// only showing a total for the whole run.
	Buckets       int
	BucketSeconds float64
	StartTime     float64
}

// NewGridSpec sizes a grid for a run's extent.
//
// The resolution is chosen from the site's size rather than fixed, because a
// 40 metre warehouse aisle and a 900 metre terminal need different cells to
// say anything. Aiming for roughly targetCols across keeps a heatmap legible
// at either scale.
func NewGridSpec(minX, minY, maxX, maxY, startTime, endTime float64, targetCols, targetBuckets int) GridSpec {
	width := math.Max(maxX-minX, 1)
	height := math.Max(maxY-minY, 1)

	if targetCols < 8 {
		targetCols = 8
	}
	cell := width / float64(targetCols)
	if cell <= 0 {
		cell = 1
	}
	// Round to something a legend can print without a string of decimals.
	cell = roundNice(cell)

	cols := int(math.Ceil(width/cell)) + 1
	rows := int(math.Ceil(height/cell)) + 1

	// A grid this fine stops being readable and starts being expensive.
	const maxCells = 400_000
	for cols*rows > maxCells {
		cell *= 2
		cols = int(math.Ceil(width/cell)) + 1
		rows = int(math.Ceil(height/cell)) + 1
	}

	duration := math.Max(endTime-startTime, 1)
	if targetBuckets < 1 {
		targetBuckets = 1
	}
	bucketSeconds := duration / float64(targetBuckets)

	return GridSpec{
		MinX: minX, MinY: minY,
		Cols: cols, Rows: rows,
		CellMeters:    cell,
		Buckets:       targetBuckets,
		BucketSeconds: bucketSeconds,
		StartTime:     startTime,
	}
}

// cellOf maps a world position to a grid index, reporting whether it is inside.
func (g GridSpec) cellOf(x, y float64) (int, int, bool) {
	col := int((x - g.MinX) / g.CellMeters)
	row := int((y - g.MinY) / g.CellMeters)
	if col < 0 || row < 0 || col >= g.Cols || row >= g.Rows {
		return 0, 0, false
	}
	return col, row, true
}

func (g GridSpec) bucketOf(t float64) int {
	if g.BucketSeconds <= 0 {
		return 0
	}
	b := int((t - g.StartTime) / g.BucketSeconds)
	if b < 0 {
		return 0
	}
	if b >= g.Buckets {
		return g.Buckets - 1
	}
	return b
}

// Heatmap accumulates one metric over the grid.
//
// Values are stored dense per bucket. A sparse map would use less memory on a
// thin run but would cost a hash per sample on a dense one, and the dense case
// is the one that has to stay fast.
type Heatmap struct {
	Spec   GridSpec
	Metric Metric
	// values is indexed bucket*Cols*Rows + row*Cols + col.
	values []float32
	total  float64
	max    float64
}

func NewHeatmap(spec GridSpec, metric Metric) *Heatmap {
	return &Heatmap{
		Spec:   spec,
		Metric: metric,
		values: make([]float32, spec.Buckets*spec.Cols*spec.Rows),
	}
}

func (h *Heatmap) add(col, row, bucket int, value float64) {
	if value <= 0 {
		return
	}
	idx := bucket*h.Spec.Cols*h.Spec.Rows + row*h.Spec.Cols + col
	if idx < 0 || idx >= len(h.values) {
		return
	}

	h.values[idx] += float32(value)
	h.total += value
	if v := float64(h.values[idx]); v > h.max {
		h.max = v
	}
}

// AddPoint credits a value to the cell containing a position.
func (h *Heatmap) AddPoint(x, y, t, value float64) {
	col, row, ok := h.Spec.cellOf(x, y)
	if !ok {
		return
	}
	h.add(col, row, h.Spec.bucketOf(t), value)
}

// AddSegment spreads a value along a straight line, so a journey credits every
// cell it crosses rather than only its endpoints.
//
// Sampling along the line is used rather than an exact grid traversal. The
// step is half a cell, which is fine enough that no crossed cell is skipped,
// and it is far simpler than the exact algorithm for no visible difference on
// a heatmap that is smoothed for display anyway.
func (h *Heatmap) AddSegment(x0, y0, x1, y1, t0, t1, total float64) {
	dx, dy := x1-x0, y1-y0
	length := math.Hypot(dx, dy)

	if length <= 0 {
		h.AddPoint(x0, y0, t0, total)
		return
	}

	steps := int(length/(h.Spec.CellMeters/2)) + 1
	// A single journey should not be able to cost unbounded work.
	const maxSteps = 4096
	if steps > maxSteps {
		steps = maxSteps
	}

	share := total / float64(steps)
	for i := 0; i < steps; i++ {
		f := (float64(i) + 0.5) / float64(steps)
		h.AddPoint(x0+dx*f, y0+dy*f, t0+(t1-t0)*f, share)
	}
}

func (h *Heatmap) Total() float64 { return h.total }

// Max is the largest value in the whole-run grid.
//
// It is deliberately not the largest value in any single time bucket, which is
// what the running counter tracks. The default view sums the buckets, so a
// per-bucket maximum would be below values a reader can plainly see, and any
// colour scale built from it would clip.
func (h *Heatmap) Max() float64 {
	max := 0.0
	for _, v := range h.Totals() {
		if float64(v) > max {
			max = float64(v)
		}
	}
	return max
}

// MaxBucket is the largest value in any single time slice, which is the right
// scale when the UI is scrubbing one bucket at a time.
func (h *Heatmap) MaxBucket() float64 { return h.max }

// Empty reports whether nothing was ever credited, so a layer with no data is
// not offered to the user as if it were a result.
func (h *Heatmap) Empty() bool { return h.total == 0 }

// Bucket returns one time slice's values, row-major.
func (h *Heatmap) Bucket(i int) []float32 {
	size := h.Spec.Cols * h.Spec.Rows
	start := i * size
	if start < 0 || start+size > len(h.values) {
		return nil
	}
	return h.values[start : start+size]
}

// Totals collapses every bucket into one grid, which is what the default view
// and the report page show.
func (h *Heatmap) Totals() []float32 {
	size := h.Spec.Cols * h.Spec.Rows
	out := make([]float32, size)

	for b := 0; b < h.Spec.Buckets; b++ {
		bucket := h.Bucket(b)
		for i, v := range bucket {
			out[i] += v
		}
	}
	return out
}

// Percentile of the non-empty cells. Heatmaps are nearly always dominated by a
// few extreme cells, so colouring against the maximum washes everything else
// out. The 95th percentile is what the UI scales against instead.
func (h *Heatmap) Percentile(p float64) float64 {
	totals := h.Totals()

	nonEmpty := make([]float64, 0, len(totals))
	for _, v := range totals {
		if v > 0 {
			nonEmpty = append(nonEmpty, float64(v))
		}
	}
	if len(nonEmpty) == 0 {
		return 0
	}

	sort.Float64s(nonEmpty)
	idx := int(p*float64(len(nonEmpty)-1) + 0.5)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(nonEmpty) {
		idx = len(nonEmpty) - 1
	}
	return nonEmpty[idx]
}

// roundNice snaps a cell size to 1, 2, 5 times a power of ten, so a legend
// reads "5 m" rather than "4.7382 m".
func roundNice(v float64) float64 {
	if v <= 0 {
		return 1
	}
	exp := math.Floor(math.Log10(v))
	base := math.Pow(10, exp)

	switch f := v / base; {
	case f <= 1.5:
		return base
	case f <= 3.5:
		return 2 * base
	case f <= 7.5:
		return 5 * base
	default:
		return 10 * base
	}
}
