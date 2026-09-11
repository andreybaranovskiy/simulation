// Package interp executes a declarative model on the godes simulation engine.
//
// Godes keeps its clock and runner registry in package-level globals, so one
// process can host exactly one simulation. That is why a run is its own
// operating-system process rather than a goroutine in the server: it gives the
// isolation the engine's design requires anyway, and along with it per-run
// timeouts, memory caps and clean cancellation.
//
// Everything random comes from internal/engine/rng rather than from godes, so
// a run is reproducible from its stored seed.
package interp

import (
	"fmt"
	"math"
	"time"

	"github.com/agoussia/godes"

	"github.com/andreybaranovskiy/simulation/internal/engine/rng"
	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
	"github.com/andreybaranovskiy/simulation/internal/engine/trace"
)

// EngineVersion is recorded with every run, so a result can always be tied to
// the code that produced it.
const EngineVersion = "interp/1"

// Config controls one execution.
type Config struct {
	Model *spec.Model
	Trace *trace.Writer

	Seed        uint64
	Replication int

	// MaxEntities stops a model whose arrival rate outruns its service rate
	// from consuming memory without bound. Zero uses a default derived from
	// the model's own estimate.
	MaxEntities int

	// Progress is called periodically with the simulation clock, so the server
	// can show a live position rather than an indeterminate spinner.
	Progress func(simTime float64, entities, records uint64)
	// ProgressInterval is how often Progress fires in wall-clock time.
	ProgressInterval time.Duration
}

// Result summarises a finished run.
type Result struct {
	EndTime   float64
	Created   int
	Completed int
	Balked    int
	Reneged   int
	// CutShort counts entities still waiting when the horizon arrived. They
	// are neither throughput nor a queue that cleared, and reporting them
	// separately is what keeps the other numbers honest.
	CutShort      int
	StillInSystem int
	WallTime      time.Duration
	Resources     []ResourceResult
	// Waits holds every recorded queue wait, used for percentiles.
	Waits map[string][]float64
	// SystemTimes is how long each completed entity spent in the model.
	SystemTimes []float64
	// LimitHit reports that MaxEntities stopped entity creation early, which
	// makes the result a lower bound rather than an answer.
	LimitHit bool
}

// ResourceResult is one resource's aggregate behaviour over the measured
// period.
type ResourceResult struct {
	ID          string
	Label       string
	Capacity    int
	Seized      int
	Balked      int
	Reneged     int
	Utilisation float64
	AvgQueue    float64
	PeakQueue   int
	DowntimeSec float64
}

type engine struct {
	model *spec.Model
	idx   *spec.Index
	net   *network
	trace *trace.Writer
	rng   *rng.Registry

	resources    map[string]*resource
	resourceList []*resource
	// linkLocks are the implicit resources behind capacity-limited links,
	// which is how a single-lane road produces congestion.
	linkLocks map[*spec.Link]*resource

	classIndex map[string]uint16

	nextID   uint32
	sequence uint64

	created     int
	completed   int
	cutShort    int
	stillActive int
	maxEntities int
	limitHit    bool

	// sourcesActive counts sources still generating. Together with
	// stillActive it tells the background runners when there is nothing left
	// to keep the simulation alive for.
	sourcesActive int

	// draining is set once the horizon passes. Remaining work then resolves
	// without advancing the clock, so the process can exit.
	draining bool

	warmUp   float64
	horizon  float64
	endTime  float64
	waits    map[string][]float64
	sysTimes []float64

	progress         func(float64, uint64, uint64)
	progressInterval time.Duration
	lastProgress     time.Time
}

// Run executes a model to completion. It must be called from the goroutine
// that owns the godes model, which in practice means the process's main
// goroutine, and only once per process.
func Run(cfg Config) (*Result, error) {
	if cfg.Model == nil {
		return nil, fmt.Errorf("no model given")
	}
	if cfg.Trace == nil {
		return nil, fmt.Errorf("no trace writer given")
	}
	if err := cfg.Model.ValidateStrict(); err != nil {
		return nil, err
	}

	net, err := buildNetwork(cfg.Model)
	if err != nil {
		return nil, err
	}

	e := &engine{
		model:            cfg.Model,
		idx:              cfg.Model.BuildIndex(),
		net:              net,
		trace:            cfg.Trace,
		rng:              rng.NewRegistry(cfg.Seed),
		resources:        make(map[string]*resource, len(cfg.Model.Resources)),
		linkLocks:        make(map[*spec.Link]*resource),
		classIndex:       make(map[string]uint16, len(cfg.Model.EntityTypes)),
		warmUp:           cfg.Model.WarmUp,
		horizon:          cfg.Model.Horizon,
		waits:            make(map[string][]float64),
		maxEntities:      cfg.MaxEntities,
		progress:         cfg.Progress,
		progressInterval: cfg.ProgressInterval,
	}

	if e.maxEntities <= 0 {
		e.maxEntities = defaultEntityLimit(cfg.Model)
	}
	if e.progressInterval <= 0 {
		e.progressInterval = 500 * time.Millisecond
	}

	e.buildResources()
	for i, c := range cfg.Model.EntityTypes {
		e.classIndex[c.ID] = uint16(i)
	}

	started := time.Now()
	e.execute()

	result := e.collect()
	result.WallTime = time.Since(started)
	return result, e.trace.Err()
}

func (e *engine) buildResources() {
	for i := range e.model.Resources {
		r := &e.model.Resources[i]

		var x, y, z float64
		if node := e.idx.Nodes[r.Node]; node != nil {
			x, y, z = node.X, node.Y, node.Z
		}

		res := newResource(r, uint16(i), x, y, z)
		e.resources[r.ID] = res
		e.resourceList = append(e.resourceList, res)
	}

	// Capacity-limited links become resources too. They are given indices
	// after the declared resources so the trace's resource table still lines
	// up with the model's.
	next := uint16(len(e.model.Resources))
	for i := range e.model.Links {
		l := &e.model.Links[i]
		if l.Capacity <= 0 {
			continue
		}
		lockSpec := &spec.Resource{
			ID:       fmt.Sprintf("link:%s->%s", l.From, l.To),
			Label:    fmt.Sprintf("Link %s to %s", l.From, l.To),
			Capacity: l.Capacity,
		}
		e.linkLocks[l] = newResource(lockSpec, next, 0, 0, 0)
		next++
	}
}

// execute drives the godes model: start the background runners, start the
// sources, then let the clock run to the horizon.
func (e *engine) execute() {
	godes.Run()

	for i := range e.model.Sources {
		src := &e.model.Sources[i]
		route := e.model.RouteFor(src.ID)
		if route == nil {
			continue
		}
		e.sourcesActive++
		godes.AddRunner(&sourceRunner{Runner: &godes.Runner{}, engine: e, spec: src, route: route})
	}

	for _, r := range e.resourceList {
		if len(r.spec.Shifts) > 0 {
			godes.AddRunner(&shiftRunner{Runner: &godes.Runner{}, engine: e, resource: r})
		}
		if r.spec.Failure != nil {
			godes.AddRunner(&failureRunner{Runner: &godes.Runner{}, engine: e, resource: r})
		}
	}

	if e.horizon > 0 {
		// The clock runs in the main goroutine while the runners work. When
		// it reaches the horizon every source stops creating entities, and
		// WaitUntilDone lets the entities already in the system finish.
		godes.Advance(e.horizon)
		e.endTime = e.horizon

		// Entities queued for a resource that is closed at the horizon would
		// wait forever, and godes will not return until every runner has
		// finished. Draining releases them so the run can end.
		e.drain()
	}

	godes.WaitUntilDone()

	if now := godes.GetSystemTime(); now > e.endTime {
		e.endTime = now
	}

	// Close the books on every resource so the integrated statistics cover the
	// whole run rather than stopping at the last change.
	for _, r := range e.resourceList {
		r.accountUntil(e.endTime)
	}
	for _, r := range e.linkLocks {
		r.accountUntil(e.endTime)
	}
}

func (e *engine) collect() *Result {
	res := &Result{
		EndTime:       e.endTime,
		Created:       e.created,
		Completed:     e.completed,
		CutShort:      e.cutShort,
		StillInSystem: e.stillActive,
		Waits:         e.waits,
		SystemTimes:   e.sysTimes,
		LimitHit:      e.limitHit,
	}

	// The measured period excludes the warm-up, because a queueing system
	// starts empty and that transient is not what anyone wants to report.
	measured := e.endTime - e.warmUp
	if measured <= 0 {
		measured = e.endTime
	}
	if measured <= 0 {
		measured = 1
	}

	for _, r := range e.resourceList {
		res.Balked += r.balked
		res.Reneged += r.reneged

		utilisation := 0.0
		if r.capacity > 0 {
			utilisation = r.busyTimeSum / (float64(r.capacity) * measured)
		}

		res.Resources = append(res.Resources, ResourceResult{
			ID:          r.spec.ID,
			Label:       r.spec.Label,
			Capacity:    r.capacity,
			Seized:      r.seized,
			Balked:      r.balked,
			Reneged:     r.reneged,
			Utilisation: clamp01(utilisation),
			AvgQueue:    r.queueArea / measured,
			PeakQueue:   r.peakQueue,
			DowntimeSec: r.downtime,
		})
	}
	return res
}

// allResources covers the declared resources and the implicit link locks.
func (e *engine) allResources() []*resource {
	out := make([]*resource, 0, len(e.resourceList)+len(e.linkLocks))
	out = append(out, e.resourceList...)
	for _, r := range e.linkLocks {
		out = append(out, r)
	}
	return out
}

// idle reports that no source will produce another entity and none remain in
// the system, which is when the background runners may stop.
func (e *engine) idle() bool {
	return e.sourcesActive == 0 && e.stillActive == 0
}

func (e *engine) nextEntityID() uint32 {
	e.nextID++
	return e.nextID
}

func (e *engine) nextSequence() uint64 {
	e.sequence++
	return e.sequence
}

func (e *engine) recordWait(r *resource, wait float64) {
	if godes.GetSystemTime() < e.warmUp {
		return
	}
	e.waits[r.spec.ID] = append(e.waits[r.spec.ID], wait)
}

// reportProgress is called from the entity runners. It is rate-limited by wall
// clock so a fast model does not spend its time writing progress updates.
func (e *engine) reportProgress() {
	if e.progress == nil {
		return
	}
	if time.Since(e.lastProgress) < e.progressInterval {
		return
	}
	e.lastProgress = time.Now()

	records, entities, _, simTime := e.trace.Stats()
	e.progress(simTime, entities, records)
}

// defaultEntityLimit caps entity creation at a generous multiple of what the
// model expects, so an unstable model fails with a clear message instead of
// exhausting memory.
func defaultEntityLimit(m *spec.Model) int {
	expected := m.ExpectedArrivals()
	limit := expected * 4
	if limit < 100_000 {
		limit = 100_000
	}
	if limit > 20_000_000 {
		limit = 20_000_000
	}
	return limit
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
