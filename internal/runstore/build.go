package runstore

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/analytics"
	"github.com/andreybaranovskiy/simulation/internal/engine/trace"
)

// BuildOptions tunes the post-processing pass.
type BuildOptions struct {
	// ChunkSeconds is how much simulated time one playback chunk covers.
	// Zero picks a value from the run's length.
	ChunkSeconds float64
	// Levels is how many entity-thinning levels to build. Level 0 always has
	// every entity.
	Levels int
	// SeriesBuckets and HeatmapBuckets set the time resolution of the charts
	// and the scrubbable heatmaps.
	SeriesBuckets  int
	HeatmapBuckets int
	// HeatmapCols is roughly how many cells across the site. The cell size in
	// metres is derived from it and the site's extent.
	HeatmapCols int
	// PathSamples is how many entity journeys to keep for the spaghetti view.
	PathSamples int
	// MaxGanttIntervals caps each resource's timeline.
	MaxGanttIntervals int

	Log *slog.Logger
	// Progress is called as the pass advances, so a long build is not silent.
	Progress func(fraction float64, stage string)
}

func (o *BuildOptions) applyDefaults(duration float64) {
	if o.ChunkSeconds <= 0 {
		o.ChunkSeconds = chunkSecondsFor(duration)
	}
	if o.Levels <= 0 {
		o.Levels = 3
	}
	if o.SeriesBuckets <= 0 {
		o.SeriesBuckets = 240
	}
	if o.HeatmapBuckets <= 0 {
		o.HeatmapBuckets = 12
	}
	if o.HeatmapCols <= 0 {
		o.HeatmapCols = 96
	}
	if o.PathSamples <= 0 {
		o.PathSamples = 300
	}
	if o.MaxGanttIntervals <= 0 {
		o.MaxGanttIntervals = 4000
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
}

// chunkSecondsFor keeps the chunk count in a range that is useful at both
// ends: enough chunks that seeking is cheap, few enough that the manifest and
// the request count stay small.
func chunkSecondsFor(duration float64) float64 {
	const targetChunks = 200

	seconds := duration / targetChunks
	switch {
	case seconds < 10:
		return 10
	case seconds > 600:
		return 600
	}

	// Snap to a round number so chunk boundaries land on readable times.
	for _, candidate := range []float64{10, 15, 30, 60, 120, 300, 600} {
		if seconds <= candidate {
			return candidate
		}
	}
	return 600
}

// Build turns a run's trace into the artifacts the viewer and the dashboards
// read. It streams: the trace is read once, start to finish, and nothing holds
// more than the entities currently in the model.
func Build(dir Dir, opts BuildOptions) (*Manifest, error) {
	if err := dir.Ensure(); err != nil {
		return nil, err
	}

	reader, err := trace.Open(dir.Trace())
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	header := reader.Header()

	endTime := header.Horizon
	if footer, err := trace.ReadFooter(dir.Trace()); err == nil && footer.EndTime > endTime {
		endTime = footer.EndTime
	}
	if endTime <= 0 {
		endTime = 1
	}

	opts.applyDefaults(endTime)
	started := time.Now()

	b := newBuilder(dir, header, endTime, opts)

	if err := b.run(reader); err != nil {
		return nil, err
	}

	manifest, err := b.finish()
	if err != nil {
		return nil, err
	}

	opts.Log.Info("built run artifacts",
		"entities", manifest.Counts.Entities,
		"records", manifest.Counts.Records,
		"chunks", manifest.Counts.Chunks,
		"bytes", manifest.Counts.TotalBytes,
		"took", time.Since(started).Round(time.Millisecond))

	return manifest, nil
}

// entityState is what the pass remembers about one entity in the model. Only
// live entities are held, so memory tracks concurrency rather than run length.
type entityState struct {
	class uint16
	// state is what the entity is doing. The engine emits a state change
	// before the span it describes and a span boundary when the state changes
	// while stationary, so the current state is the right one to credit a
	// closing span to.
	state uint8

	// The span the entity is currently part-way through.
	spanStart  float64
	sx, sy, sz float64
	spanEnd    float64
	ex, ey, ez float64

	createdAt float64
}

// positionAt interpolates where the entity is at a moment.
func (e *entityState) positionAt(t float64) (float64, float64, float64) {
	span := e.spanEnd - e.spanStart
	if span <= 0 || t >= e.spanEnd {
		return e.ex, e.ey, e.ez
	}
	if t <= e.spanStart {
		return e.sx, e.sy, e.sz
	}
	f := (t - e.spanStart) / span
	return e.sx + (e.ex-e.sx)*f, e.sy + (e.ey-e.sy)*f, e.sz + (e.ez-e.sz)*f
}

type builder struct {
	dir    Dir
	header trace.Header
	opts   BuildOptions

	startTime float64
	endTime   float64

	live map[uint32]*entityState

	levels []*levelWriter

	// Analytics accumulators, all fed by the same pass.
	heatmaps map[analytics.Metric]*analytics.Heatmap
	gridSpec analytics.GridSpec
	series   *analytics.Bucketer
	gantt    *analytics.Gantt
	paths    *analytics.Paths

	// Running counters.
	records     uint64
	spans       uint64
	created     int
	completed   int
	wip         int
	classCounts []int

	// Per-resource occupancy, tracked so a Gantt observation can report both
	// numbers when only one of them changed.
	resourceBusy   []int
	resourceQueued []int
	resourceDown   []bool
}

func newBuilder(dir Dir, header trace.Header, endTime float64, opts BuildOptions) *builder {
	b := &builder{
		dir:       dir,
		header:    header,
		opts:      opts,
		startTime: 0,
		endTime:   endTime,
		live:      make(map[uint32]*entityState, 1024),
		heatmaps:  make(map[analytics.Metric]*analytics.Heatmap),

		classCounts:    make([]int, len(header.Classes)),
		resourceBusy:   make([]int, len(header.Resources)+64),
		resourceQueued: make([]int, len(header.Resources)+64),
		resourceDown:   make([]bool, len(header.Resources)+64),
	}

	bounds := header.Bounds
	b.gridSpec = analytics.NewGridSpec(
		bounds.MinX, bounds.MinY, bounds.MaxX, bounds.MaxY,
		b.startTime, endTime, opts.HeatmapCols, opts.HeatmapBuckets,
	)

	for _, info := range analytics.Metrics() {
		b.heatmaps[info.Metric] = analytics.NewHeatmap(b.gridSpec, info.Metric)
	}

	b.series = analytics.NewBucketer(b.startTime, endTime, opts.SeriesBuckets)
	b.gantt = analytics.NewGantt(opts.MaxGanttIntervals)
	for _, r := range header.Resources {
		b.gantt.Declare(r.ID, r.Label, r.Capacity)
	}

	// The path sample is chosen so a busy run still keeps a readable number of
	// journeys rather than the first few hundred.
	b.paths = analytics.NewPaths(1, opts.PathSamples)

	for i := 0; i < opts.Levels; i++ {
		stride := 1 << (2 * i) // 1, 4, 16
		b.levels = append(b.levels, newLevelWriter(dir, i, stride, opts.ChunkSeconds, b.startTime, endTime))
	}

	return b
}

// run is the single streaming pass.
func (b *builder) run(reader *trace.Reader) error {
	var rec trace.Record
	lastProgress := time.Now()

	for {
		err := reader.Next(&rec)
		if err != nil {
			if err.Error() == "EOF" || isEOF(err) {
				break
			}
			return err
		}

		b.records++
		if err := b.handle(&rec); err != nil {
			return err
		}

		if b.opts.Progress != nil && time.Since(lastProgress) > 500*time.Millisecond {
			lastProgress = time.Now()
			b.opts.Progress(b.progressFraction(&rec), "reading the trace")
		}
	}

	return b.closeOut()
}

func (b *builder) progressFraction(rec *trace.Record) float64 {
	if b.endTime <= 0 {
		return 0
	}
	return math.Min(rec.Time/b.endTime, 1)
}

func (b *builder) handle(rec *trace.Record) error {
	switch rec.Type {
	case trace.RecSpawn:
		return b.onSpawn(rec)
	case trace.RecSegment:
		return b.onSegment(rec)
	case trace.RecState:
		return b.onState(rec)
	case trace.RecResource:
		b.onResource(rec)
	case trace.RecExit:
		return b.onExit(rec)
	}
	return nil
}

func (b *builder) onSpawn(rec *trace.Record) error {
	e := &entityState{
		class:     rec.Class,
		spanStart: rec.Time,
		sx:        rec.X, sy: rec.Y, sz: rec.Z,
		spanEnd: rec.Time,
		ex:      rec.X, ey: rec.Y, ez: rec.Z,
		createdAt: rec.Time,
	}
	b.live[rec.Entity] = e

	b.created++
	b.wip++
	if int(rec.Class) < len(b.classCounts) {
		b.classCounts[rec.Class]++
	}

	b.series.Count("arrivals", "Arrivals", analytics.UnitCount, rec.Time)
	b.series.SetLevel("wip", "In the system", analytics.UnitEntities, rec.Time, float64(b.wip))

	b.paths.Begin(rec.Entity, rec.Class, rec.Time, rec.X, rec.Y)

	for _, lw := range b.levels {
		if err := lw.spawn(rec.Time, rec.Entity, rec.Class, rec.X, rec.Y, rec.Z, b.live); err != nil {
			return err
		}
	}
	return nil
}

// onSegment is where most of the work happens: the previous span has just
// completed, so it can be credited to the heatmaps and the path trace.
func (b *builder) onSegment(rec *trace.Record) error {
	e, ok := b.live[rec.Entity]
	if !ok {
		// A segment for an entity that never spawned means a corrupt trace.
		// Skipping is better than inventing a position for it.
		return nil
	}

	b.spans++

	// A segment record carries the time the motion ENDS, which is in the
	// future relative to the point in the stream where it appears. The stream
	// itself is ordered by when the engine emitted each record, so the span's
	// start is the entity's current resting time, and that is what decides
	// which playback chunk the span belongs to.
	//
	// Filing a span by its end time instead would push the chunk writer ahead
	// of the stream and strand every later record in the wrong window.
	spanStartTime := e.spanEnd

	// Close the span the entity was on. Its start is where the entity was and
	// when; its end is this record.
	b.creditSpan(e, rec.Time, rec.X, rec.Y)

	e.spanStart = e.spanEnd
	e.sx, e.sy, e.sz = e.ex, e.ey, e.ez
	e.spanEnd = rec.Time
	e.ex, e.ey, e.ez = rec.X, rec.Y, rec.Z

	// A segment ending before it starts would come from a corrupt trace; a
	// zero-length one is normal and means a teleport to a queue position.
	if e.spanEnd < e.spanStart {
		e.spanStart = e.spanEnd
		e.sx, e.sy, e.sz = e.ex, e.ey, e.ez
	}

	b.paths.Extend(rec.Entity, rec.Time, rec.X, rec.Y)

	for _, lw := range b.levels {
		if err := lw.span(spanStartTime, rec.Time, rec.Entity, rec.X, rec.Y, rec.Z, b.live); err != nil {
			return err
		}
	}
	return nil
}

// creditSpan attributes a completed span to the heatmap layers.
//
// The distinction that makes these layers useful is between moving and not.
// A stationary span is dwell, which is where work and waiting happen. A moving
// span is traffic, which is where routes actually run. Occupancy takes both,
// and congestion takes only the stationary time an entity spent queued or
// blocked, which is the layer that points at a problem rather than at activity.
func (b *builder) creditSpan(e *entityState, endTime, endX, endY float64) {
	startT := e.spanEnd
	startX, startY := e.ex, e.ey

	duration := endTime - startT
	if duration < 0 {
		return
	}

	distance := math.Hypot(endX-startX, endY-startY)
	const stationaryMeters = 0.05

	if distance <= stationaryMeters {
		if duration <= 0 {
			return
		}
		b.heatmaps[analytics.MetricDwell].AddPoint(startX, startY, startT, duration)
		b.heatmaps[analytics.MetricOccupancy].AddPoint(startX, startY, startT, duration)

		if e.state == uint8(trace.StateQueued) || e.state == uint8(trace.StateBlocked) {
			b.heatmaps[analytics.MetricCongestion].AddPoint(startX, startY, startT, duration)
		}
		return
	}

	b.heatmaps[analytics.MetricTraffic].AddSegment(startX, startY, endX, endY, startT, endTime, distance)
	if duration > 0 {
		b.heatmaps[analytics.MetricOccupancy].AddSegment(startX, startY, endX, endY, startT, endTime, duration)
	}
}

func (b *builder) onState(rec *trace.Record) error {
	e, ok := b.live[rec.Entity]
	if !ok {
		return nil
	}
	e.state = uint8(rec.EntityState)

	for _, lw := range b.levels {
		if err := lw.state(rec.Time, rec.Entity, uint8(rec.EntityState), b.live); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) onResource(rec *trace.Record) {
	idx := int(rec.Resource)
	if idx < 0 {
		return
	}
	b.growResourceTables(idx)

	b.resourceBusy[idx] = int(rec.Busy)
	b.resourceQueued[idx] = int(rec.Queued)

	id, label, capacity := b.resourceInfo(idx)

	b.gantt.Observe(id, rec.Time, int(rec.Busy), int(rec.Queued), b.resourceDown[idx])

	b.series.SetLevel("queue."+id, label+" queue", analytics.UnitEntities,
		rec.Time, float64(rec.Queued))

	if capacity > 0 {
		b.series.SetLevel("util."+id, label+" in use", analytics.UnitPercent,
			rec.Time, float64(rec.Busy)/float64(capacity)*100)
	}
}

func (b *builder) growResourceTables(idx int) {
	for len(b.resourceBusy) <= idx {
		b.resourceBusy = append(b.resourceBusy, 0)
		b.resourceQueued = append(b.resourceQueued, 0)
		b.resourceDown = append(b.resourceDown, false)
	}
}

// resourceInfo resolves a numeric resource index. Indices past the declared
// resources are the implicit link locks the engine creates for
// capacity-limited links, which still deserve a name in the output.
func (b *builder) resourceInfo(idx int) (id, label string, capacity int) {
	if idx < len(b.header.Resources) {
		r := b.header.Resources[idx]
		return r.ID, r.Label, r.Capacity
	}
	name := fmt.Sprintf("link_%d", idx-len(b.header.Resources)+1)
	return name, fmt.Sprintf("Link %d", idx-len(b.header.Resources)+1), 0
}

func (b *builder) onExit(rec *trace.Record) error {
	e, ok := b.live[rec.Entity]
	if !ok {
		return nil
	}

	b.creditSpan(e, rec.Time, e.ex, e.ey)

	delete(b.live, rec.Entity)
	b.completed++
	b.wip--

	b.series.Count("departures", "Departures", analytics.UnitCount, rec.Time)
	b.series.SetLevel("wip", "In the system", analytics.UnitEntities, rec.Time, float64(b.wip))

	b.paths.End(rec.Entity, rec.Time)

	for _, lw := range b.levels {
		if err := lw.exit(rec.Time, rec.Entity, b.live); err != nil {
			return err
		}
	}
	return nil
}

// closeOut finishes every accumulator and flushes the final chunks.
func (b *builder) closeOut() error {
	for _, lw := range b.levels {
		if err := lw.close(b.live); err != nil {
			return err
		}
	}

	b.series.Close(b.endTime)
	b.gantt.Close(b.endTime)
	b.paths.Close(b.endTime)
	return nil
}

func isEOF(err error) bool {
	return err != nil && (err.Error() == "EOF")
}

// readResult loads the runner's own summary, which carries the statistics only
// the engine could compute, such as per-resource waits.
func (b *builder) readResult() (map[string]any, error) {
	data, err := os.ReadFile(b.dir.Result())
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}
