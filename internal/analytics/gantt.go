package analytics

import "math"

// Gantt records what each resource was doing over time: serving, idle, or out
// of action. It is the chart that shows a bottleneck as a solid bar while the
// resources around it sit empty, which is the picture that makes a capacity
// argument without a word of explanation.
type Gantt struct {
	// MaxIntervals caps a resource's bar. A run with millions of changes would
	// otherwise produce a chart no browser can draw and no eye can read.
	MaxIntervals int

	// MeasureFrom is when the summary percentages start counting: the end of
	// the warm-up, matching what the engine reports. The bar still draws the
	// whole run, because hiding the start would leave a reader wondering what
	// the chart was not showing.
	MeasureFrom float64

	resources []*ganttResource
	index     map[string]*ganttResource
}

// GanttInterval is one stretch of unchanged occupancy.
type GanttInterval struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	// Busy is how many units of the resource were in use.
	Busy int `json:"busy"`
	// Queued is how many entities were waiting.
	Queued int `json:"queued"`
	// Down marks the resource being closed or broken, which reads very
	// differently from being idle.
	Down bool `json:"down,omitempty"`
}

// GanttRow is one resource's timeline plus the summary printed beside it.
type GanttRow struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Capacity  int     `json:"capacity"`
	Busy      float64 `json:"busyFraction"`
	Idle      float64 `json:"idleFraction"`
	Down      float64 `json:"downFraction"`
	PeakBusy  int     `json:"peakBusy"`
	PeakQueue int     `json:"peakQueue"`

	Intervals []GanttInterval `json:"intervals"`
	// Merged reports that intervals were combined to fit the cap, so a reader
	// knows the bar is a summary rather than every change.
	Merged bool `json:"merged,omitempty"`
}

type ganttResource struct {
	id       string
	label    string
	capacity int

	intervals []GanttInterval
	current   GanttInterval
	open      bool

	busyTime  float64
	downTime  float64
	peakBusy  int
	peakQueue int

	// minWidth grows as intervals accumulate, discarding changes too brief to
	// see rather than keeping millions of slivers.
	minWidth float64
}

func NewGantt(maxIntervals int) *Gantt {
	if maxIntervals <= 0 {
		maxIntervals = 4000
	}
	return &Gantt{
		MaxIntervals: maxIntervals,
		index:        make(map[string]*ganttResource),
	}
}

// Declare registers a resource before any observation, so a resource that was
// never touched still appears in the chart as an empty row. An idle resource
// is a finding.
func (g *Gantt) Declare(id, label string, capacity int) {
	if _, ok := g.index[id]; ok {
		return
	}
	r := &ganttResource{id: id, label: label, capacity: capacity}
	g.index[id] = r
	g.resources = append(g.resources, r)
}

// Observe records a resource's occupancy at a moment.
func (g *Gantt) Observe(id string, t float64, busy, queued int, down bool) {
	r, ok := g.index[id]
	if !ok {
		g.Declare(id, id, 0)
		r = g.index[id]
	}

	if busy > r.peakBusy {
		r.peakBusy = busy
	}
	if queued > r.peakQueue {
		r.peakQueue = queued
	}

	if !r.open {
		r.current = GanttInterval{Start: t, End: t, Busy: busy, Queued: queued, Down: down}
		r.open = true
		return
	}

	if r.current.Busy == busy && r.current.Queued == queued && r.current.Down == down {
		r.current.End = t
		return
	}

	r.current.End = t
	r.closeInterval(g.MaxIntervals, g.MeasureFrom)
	r.current = GanttInterval{Start: t, End: t, Busy: busy, Queued: queued, Down: down}
}

// closeInterval files the current stretch, dropping anything narrower than the
// current threshold and widening that threshold when the row gets too long.
func (r *ganttResource) closeInterval(maxIntervals int, measureFrom float64) {
	width := r.current.End - r.current.Start

	from := r.current.Start
	if from < measureFrom {
		from = measureFrom
	}
	if r.current.End > from {
		r.busyTime += float64(r.current.Busy) * (r.current.End - from)
	}

	if r.current.Down {
		r.downTime += width
	}

	if width < r.minWidth {
		// Too brief to draw. It still counted towards the totals above, so the
		// summary stays exact even though the bar is simplified.
		return
	}

	if n := len(r.intervals); n > 0 {
		last := &r.intervals[n-1]
		if last.Busy == r.current.Busy && last.Queued == r.current.Queued && last.Down == r.current.Down {
			last.End = r.current.End
			return
		}
	}

	r.intervals = append(r.intervals, r.current)

	if len(r.intervals) > maxIntervals {
		r.compact(maxIntervals)
	}
}

// compact halves a row by dropping its narrowest intervals and raising the
// threshold, so a long run degrades gracefully instead of without bound.
func (r *ganttResource) compact(maxIntervals int) {
	widest := 0.0
	for _, iv := range r.intervals {
		if w := iv.End - iv.Start; w > widest {
			widest = w
		}
	}

	if r.minWidth <= 0 {
		r.minWidth = widest / 200
	} else {
		r.minWidth *= 2
	}
	if r.minWidth <= 0 {
		r.minWidth = 1e-6
	}

	kept := r.intervals[:0]
	for _, iv := range r.intervals {
		if iv.End-iv.Start < r.minWidth {
			continue
		}
		if n := len(kept); n > 0 {
			last := &kept[n-1]
			if last.Busy == iv.Busy && last.Queued == iv.Queued && last.Down == iv.Down {
				last.End = iv.End
				continue
			}
		}
		kept = append(kept, iv)
	}
	r.intervals = kept

	// If dropping narrow intervals was not enough, merge neighbours until it is.
	for len(r.intervals) > maxIntervals {
		merged := r.intervals[:0]
		for i := 0; i < len(r.intervals); i += 2 {
			iv := r.intervals[i]
			if i+1 < len(r.intervals) {
				next := r.intervals[i+1]
				iv.End = next.End
				// The busier of the pair is kept, so merging never makes a
				// bottleneck look calmer than it was.
				if next.Busy > iv.Busy {
					iv.Busy = next.Busy
				}
				if next.Queued > iv.Queued {
					iv.Queued = next.Queued
				}
				iv.Down = iv.Down && next.Down
			}
			merged = append(merged, iv)
		}
		r.intervals = merged
	}
}

// Close finishes every open interval at the run's end.
func (g *Gantt) Close(endTime float64) {
	for _, r := range g.resources {
		if r.open {
			if endTime > r.current.End {
				r.current.End = endTime
			}
			r.closeInterval(g.MaxIntervals, g.MeasureFrom)
			r.open = false
		}
	}
}

// Rows returns the finished chart.
func (g *Gantt) Rows(startTime, endTime float64) []GanttRow {
	// The percentages describe the measured period, the same window the
	// engine's utilisation KPI uses, so the chart and the number agree.
	measureStart := math.Max(startTime, g.MeasureFrom)
	duration := math.Max(endTime-measureStart, 1)
	out := make([]GanttRow, 0, len(g.resources))

	for _, r := range g.resources {
		capacity := r.capacity
		if capacity <= 0 {
			capacity = 1
		}

		busy := r.busyTime / (float64(capacity) * duration)
		down := r.downTime / duration

		row := GanttRow{
			ID: r.id, Label: r.label, Capacity: r.capacity,
			Busy:      clamp01(busy),
			Down:      clamp01(down),
			PeakBusy:  r.peakBusy,
			PeakQueue: r.peakQueue,
			Intervals: r.intervals,
			Merged:    r.minWidth > 0,
		}
		row.Idle = clamp01(1 - row.Busy - row.Down)

		if row.Intervals == nil {
			row.Intervals = []GanttInterval{}
		}
		out = append(out, row)
	}
	return out
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
