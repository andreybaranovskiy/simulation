package interp

import (
	"sort"

	"github.com/agoussia/godes"

	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
	"github.com/andreybaranovskiy/simulation/internal/engine/trace"
)

// resource is a running instance of a spec.Resource: a gate lane, a crane, a
// dock door. It is a counting semaphore with a queue in front of it.
//
// Godes schedules cooperatively and runs one runner at a time, so the counters
// here need no locking. What godes does provide is BooleanControl, which parks
// a waiting runner until the resource hands it the green light.
type resource struct {
	spec  *spec.Resource
	index uint16

	capacity int
	busy     int

	// down covers both a failure and a closed shift. A resource that is down
	// serves nobody, and its queue keeps growing.
	down bool

	queue []*waiter

	// x and y are where the resource sits, used to place queued entities.
	x, y, z float64

	// measureFrom is when the statistics start counting: the end of the
	// warm-up. A queueing system starts empty, and that transient is not the
	// steady state anyone wants reported.
	measureFrom float64

	// Counters feeding the run's KPIs.
	seized      int
	balked      int
	reneged     int
	busyTimeSum float64
	queueArea   float64
	lastChange  float64
	peakQueue   int
	downtime    float64
	downSince   float64
}

// waiter is one entity parked in a queue.
type waiter struct {
	entity   *entity
	control  *godes.BooleanControl
	priority int
	// arrival orders a FIFO queue and measures the wait.
	arrival float64
	// sequence breaks ties deterministically. Without it, two entities that
	// arrive at the same instant would be ordered by map iteration and the run
	// would not be reproducible.
	sequence uint64
	// abandoned marks a waiter that reneged, so a later release does not hand
	// the resource to an entity that has already gone.
	abandoned bool
	// terminated marks a waiter woken by the end-of-run drain rather than by
	// being granted the resource.
	terminated bool
}

func newResource(s *spec.Resource, index uint16, x, y, z float64) *resource {
	return &resource{
		spec:     s,
		index:    index,
		capacity: s.Capacity,
		x:        x,
		y:        y,
		z:        z,
	}
}

// available reports whether a seize could succeed right now.
func (r *resource) available() bool {
	return !r.down && r.busy < r.capacity
}

// queueFull reports whether an arriving entity would be turned away.
func (r *resource) queueFull() bool {
	limit := r.spec.Queue.Capacity
	return limit > 0 && r.liveQueueLen() >= limit
}

func (r *resource) liveQueueLen() int {
	n := 0
	for _, w := range r.queue {
		if !w.abandoned {
			n++
		}
	}
	return n
}

// seizeOutcome says how a seize attempt ended.
type seizeOutcome int

const (
	seizeGranted seizeOutcome = iota
	// seizeBalked means the queue was full on arrival.
	seizeBalked
	// seizeReneged means the entity waited past the queue's maximum wait.
	seizeReneged
	// seizeTerminated means the run ended while the entity was still waiting.
	// It is kept separate from a renege because it says nothing about the
	// model: counting it as a departure would inflate throughput with entities
	// that were never served.
	seizeTerminated
)

// seize acquires one unit of the resource, blocking the calling runner until
// it is free. It returns how the attempt ended so the route can react.
func (e *engine) seize(r *resource, ent *entity) seizeOutcome {
	now := godes.GetSystemTime()

	// Once the run is draining nothing new may queue. Without this an entity
	// arriving at a resource that is closed for the night would wait forever
	// and the process would never exit.
	if e.draining && !r.available() {
		return seizeTerminated
	}

	if r.available() && r.liveQueueLen() == 0 {
		r.accountUntil(now)
		r.busy++
		r.seized++
		e.trace.Event(now, ent.id, trace.EvSeize, r.index)
		e.emitResource(now, r)
		return seizeGranted
	}

	if r.queueFull() {
		r.balked++
		e.trace.Event(now, ent.id, trace.EvBalk, r.index)
		return seizeBalked
	}

	w := &waiter{
		entity:   ent,
		control:  godes.NewBooleanControl(),
		priority: ent.priority,
		arrival:  now,
		sequence: e.nextSequence(),
	}

	r.accountUntil(now)
	r.queue = append(r.queue, w)
	r.sortQueue()
	if n := r.liveQueueLen(); n > r.peakQueue {
		r.peakQueue = n
	}

	e.trace.Event(now, ent.id, trace.EvQueueEnter, r.index)
	e.trace.State(now, ent.id, trace.StateQueued)
	e.emitResource(now, r)

	// While queued the entity sits at the resource. Emitting a stationary
	// segment keeps its position defined for the whole wait, which is what
	// lets a heatmap show where time is spent rather than only where motion is.
	ent.holdAt(e, r.x, r.y, r.z)

	if maxWait := r.spec.Queue.MaxWait; maxWait > 0 {
		w.control.WaitAndTimeout(true, maxWait)

		if !w.control.GetState() {
			// The wait expired. Drop out of the queue and tell the caller.
			w.abandoned = true
			r.removeWaiter(w)
			r.reneged++

			out := godes.GetSystemTime()
			r.accountUntil(out)
			e.trace.Event(out, ent.id, trace.EvRenege, r.index)
			e.emitResource(out, r)
			return seizeReneged
		}
	} else {
		w.control.Wait(true)
	}

	if w.terminated {
		// Woken by the end-of-run drain, not granted.
		return seizeTerminated
	}

	// Released to us: the releasing entity already counted the occupancy, so
	// only the wait is recorded here.
	granted := godes.GetSystemTime()

	// A zero-length segment marks the end of the wait.
	//
	// Without it the wait and the service that follows it are one span, since
	// nothing else moves the entity between queueing and being served. The
	// post-processing pass credits a span to one state, so a merged span files
	// the whole wait under "serving" and the congestion heatmap comes out
	// empty however long the queue was.
	e.trace.Segment(granted, ent.id, ent.x, ent.y, ent.z)

	e.trace.Event(granted, ent.id, trace.EvQueueExit, r.index)
	e.trace.Event(granted, ent.id, trace.EvSeize, r.index)
	r.seized++
	e.recordWait(r, granted-w.arrival)
	return seizeGranted
}

// release gives a unit back and hands it to the next waiter, if any.
func (e *engine) release(r *resource, ent *entity) {
	now := godes.GetSystemTime()

	r.accountUntil(now)
	if r.busy > 0 {
		r.busy--
	}
	e.trace.Event(now, ent.id, trace.EvRelease, r.index)

	e.promoteNext(r, now)
	e.emitResource(now, r)
}

// promoteNext hands a free unit to the first eligible waiter. It is called
// after a release and after a resource comes back up.
func (e *engine) promoteNext(r *resource, now float64) {
	for r.available() {
		next := r.popWaiter()
		if next == nil {
			return
		}
		r.busy++
		next.control.Set(true)
		_ = now
	}
}

func (r *resource) popWaiter() *waiter {
	for len(r.queue) > 0 {
		w := r.queue[0]
		r.queue = r.queue[1:]
		if !w.abandoned {
			return w
		}
	}
	return nil
}

func (r *resource) removeWaiter(target *waiter) {
	for i, w := range r.queue {
		if w == target {
			r.queue = append(r.queue[:i], r.queue[i+1:]...)
			return
		}
	}
}

// sortQueue orders the queue for the declared discipline. FIFO is the common
// case and is already in order, so it costs nothing.
func (r *resource) sortQueue() {
	switch r.spec.Queue.Discipline {
	case spec.LIFO:
		// Most recent first; the sequence number gives a total order.
		sort.SliceStable(r.queue, func(i, j int) bool {
			return r.queue[i].sequence > r.queue[j].sequence
		})
	case spec.Priority:
		// Higher priority first, then first come first served within a band.
		sort.SliceStable(r.queue, func(i, j int) bool {
			if r.queue[i].priority != r.queue[j].priority {
				return r.queue[i].priority > r.queue[j].priority
			}
			return r.queue[i].sequence < r.queue[j].sequence
		})
	}
}

// accountUntil integrates the occupancy and queue length up to now, which is
// how time-weighted utilisation and average queue length are computed exactly
// rather than by sampling.
//
// Only the part of the interval after the warm-up counts towards the
// integrals. Accumulating over the whole run and then dividing by the measured
// period alone would inflate every utilisation by exactly the warm-up's share
// of the run, and the number would disagree with a chart of the same thing.
func (r *resource) accountUntil(now float64) {
	if now <= r.lastChange {
		r.lastChange = now
		return
	}

	from := r.lastChange
	if from < r.measureFrom {
		from = r.measureFrom
	}

	if now > from {
		measured := now - from
		r.busyTimeSum += float64(r.busy) * measured
		r.queueArea += float64(r.liveQueueLen()) * measured
	}

	// Downtime is a fact about the run rather than a statistic about steady
	// state, so it counts from the first second.
	if r.down {
		r.downtime += now - r.lastChange
	}

	r.lastChange = now
}

// setDown opens or closes a resource, for a shift boundary or a breakdown.
func (e *engine) setDown(r *resource, down bool, kind trace.EventKind) {
	now := godes.GetSystemTime()
	if r.down == down {
		return
	}

	r.accountUntil(now)
	r.down = down
	if down {
		r.downSince = now
	}

	e.trace.Event(now, 0, kind, r.index)
	e.emitResource(now, r)

	if !down {
		// Coming back up, work through whoever accumulated in the queue.
		e.promoteNext(r, now)
		e.emitResource(now, r)
	}
}

// drain wakes everyone still queued so the run can finish. A resource that is
// closed when the horizon arrives would otherwise hold its queue forever, and
// godes waits for every runner before the process can exit.
func (e *engine) drain() {
	e.draining = true

	now := godes.GetSystemTime()
	for _, r := range e.allResources() {
		r.accountUntil(now)
		for _, w := range r.queue {
			if w.abandoned {
				continue
			}
			w.terminated = true
			w.control.Set(true)
		}
		r.queue = nil
	}
}

// emitResource records the resource's occupancy and queue length. This is the
// raw material for the utilisation series, the Gantt chart and the queue
// heatmap, so it is written on every change rather than sampled.
func (e *engine) emitResource(t float64, r *resource) {
	e.trace.Resource(t, r.index, uint16(r.busy), uint16(r.liveQueueLen()))
}
