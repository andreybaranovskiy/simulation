package interp

import (
	"github.com/agoussia/godes"

	"github.com/andreybaranovskiy/simulation/internal/engine/rng"

	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
	"github.com/andreybaranovskiy/simulation/internal/engine/trace"
)

// entity is one moving thing in the model. Godes gives each its own goroutine,
// and Run walks the entity's route from arrival to exit.
type entity struct {
	*godes.Runner

	engine *engine
	id     uint32
	class  *spec.EntityType
	route  *spec.Route

	// node is where the entity currently is, as a network index.
	node int
	// x, y and z are its exact position, which differs from the node while it
	// is queued beside a resource.
	x, y, z float64

	speed    float64
	priority int

	createdAt float64

	// held tracks resources seized but not yet released, so an entity that
	// leaves early still gives everything back.
	held []*resource

	// cutShort marks an entity the end of the run removed while it was still
	// waiting. It left the model without finishing its route, so it counts
	// towards neither throughput nor the time-in-system distribution.
	cutShort bool
}

// Run is the godes entry point for an entity. Everything an entity ever does
// happens inside this call.
func (ent *entity) Run() {
	e := ent.engine

	start := godes.GetSystemTime()
	ent.createdAt = start

	e.trace.Spawn(start, ent.id, e.classIndex[ent.class.ID], ent.x, ent.y, ent.z)
	e.trace.State(start, ent.id, trace.StateIdle)

	// runSteps reports false when the entity has already left, via an exit
	// step or because it was turned away. Finishing again would record a
	// second departure and double-count the entity.
	if ent.runSteps(ent.route.Steps) {
		// A route that runs out of steps without an explicit exit still leaves.
		ent.finish()
	}
}

// runSteps performs a step list. It returns false when the entity has left the
// model, so an enclosing list stops rather than acting on a departed entity.
func (ent *entity) runSteps(steps []spec.Step) bool {
	for i := range steps {
		if !ent.runStep(&steps[i]) {
			return false
		}
	}
	return true
}

func (ent *entity) runStep(step *spec.Step) bool {
	e := ent.engine

	if step.Priority != nil {
		ent.priority = *step.Priority
	}

	switch step.Type {
	case spec.StepTravel:
		return ent.travel(step.To)

	case spec.StepSeize:
		return ent.seize(step.Resource)

	case spec.StepRelease:
		ent.releaseHeld(step.Resource)
		return true

	case spec.StepDelay:
		ent.delay(step.Duration, trace.StateServing)
		return true

	case spec.StepUse:
		if !ent.seize(step.Resource) {
			return false
		}
		duration := step.Duration
		if duration == nil {
			if r := e.idx.Resources[step.Resource]; r != nil {
				duration = &r.Service
			}
		}
		ent.delay(duration, trace.StateServing)
		ent.releaseHeld(step.Resource)
		return true

	case spec.StepBranch:
		return ent.branch(step)

	case spec.StepExit:
		ent.finish()
		return false
	}
	return true
}

// travel moves the entity to a node over the link network, one leg at a time.
// Each leg emits a single motion segment, so a long journey costs a handful of
// bytes regardless of how long it takes.
func (ent *entity) travel(nodeID string) bool {
	e := ent.engine

	dest, ok := e.net.index(nodeID)
	if !ok {
		return true
	}
	if dest == ent.node {
		return true
	}

	legs := e.net.route(ent.node, dest)
	if legs == nil {
		// Validation rejects unreachable destinations, so this only happens
		// for a model changed under a running process. Teleporting is the
		// least damaging response: the entity continues rather than hanging.
		ent.node = dest
		ent.x, ent.y, ent.z = e.net.position(dest)
		e.trace.Segment(godes.GetSystemTime(), ent.id, ent.x, ent.y, ent.z)
		return true
	}

	e.trace.State(godes.GetSystemTime(), ent.id, trace.StateTravelling)

	for _, l := range legs {
		if !ent.travelLeg(l) {
			return false
		}
	}

	e.trace.State(godes.GetSystemTime(), ent.id, trace.StateIdle)
	e.reportProgress()
	return true
}

func (ent *entity) travelLeg(l leg) bool {
	e := ent.engine

	// A capacity-limited link is held for the whole traversal. This is what
	// turns a narrow road into a queue rather than a suggestion.
	var lock *resource
	if l.link != nil {
		if held, ok := e.linkLocks[l.link]; ok {
			lock = held
			// A link lock declares no queue limit and no maximum wait, so the
			// only outcome is a grant; the entity waits for its turn on the
			// road rather than being turned away.
			e.seize(lock, ent)
			e.trace.State(godes.GetSystemTime(), ent.id, trace.StateTravelling)
		}
	}

	if e.draining {
		// Past the horizon the run is winding down. Moving the entity without
		// advancing the clock lets it reach its exit instead of blocking
		// shutdown on a journey nobody will measure.
		ent.node = l.to
		ent.x, ent.y, ent.z = e.net.position(l.to)
		if lock != nil {
			e.release(lock, ent)
		}
		return true
	}

	speed := ent.speed
	if l.speedLimit > 0 && l.speedLimit < speed {
		speed = l.speedLimit
	}
	if speed <= 0 {
		speed = 1
	}

	duration := l.distance / speed
	arrival := godes.GetSystemTime() + duration

	x, y, z := e.net.position(l.to)
	e.trace.Segment(arrival, ent.id, x, y, z)

	godes.Advance(duration)

	ent.node = l.to
	ent.x, ent.y, ent.z = x, y, z

	if lock != nil {
		e.release(lock, ent)
	}
	return true
}

// seize acquires a resource, handling the two ways it can fail. Both end the
// entity's journey: it was turned away or it gave up, and either way it leaves.
func (ent *entity) seize(resourceID string) bool {
	e := ent.engine

	r := e.resources[resourceID]
	if r == nil {
		return true
	}

	switch e.seize(r, ent) {
	case seizeGranted:
		ent.held = append(ent.held, r)
		e.trace.State(godes.GetSystemTime(), ent.id, trace.StateServing)
		return true

	case seizeTerminated:
		ent.cutShort = true
		ent.finish()
		return false

	case seizeBalked, seizeReneged:
		ent.finish()
		return false
	}
	return true
}

func (ent *entity) releaseHeld(resourceID string) {
	e := ent.engine

	for i, r := range ent.held {
		if r.spec.ID != resourceID {
			continue
		}
		e.release(r, ent)
		ent.held = append(ent.held[:i], ent.held[i+1:]...)
		e.trace.State(godes.GetSystemTime(), ent.id, trace.StateIdle)
		return
	}
}

// delay holds the entity in place for a distributed time.
func (ent *entity) delay(d *rng.Dist, state trace.State) {
	if d == nil {
		return
	}
	e := ent.engine

	if e.draining {
		return
	}

	duration := d.SampleDuration(e.rng.Stream("delay:" + ent.class.ID))
	if duration <= 0 {
		return
	}

	e.trace.State(godes.GetSystemTime(), ent.id, state)
	// A stationary segment keeps the entity's position defined while it waits,
	// which is what lets a dwell-time heatmap show where time is actually
	// spent rather than only where movement happens.
	e.trace.Segment(godes.GetSystemTime()+duration, ent.id, ent.x, ent.y, ent.z)

	godes.Advance(duration)
	e.reportProgress()
}

// branch picks one alternative by weight and performs its steps.
func (ent *entity) branch(step *spec.Step) bool {
	e := ent.engine

	total := 0.0
	for _, b := range step.Branches {
		total += b.Weight
	}
	if total <= 0 {
		return true
	}

	pick := e.rng.Stream("branch:"+ent.route.ID).Float64() * total
	for i := range step.Branches {
		pick -= step.Branches[i].Weight
		if pick <= 0 {
			return ent.runSteps(step.Branches[i].Steps)
		}
	}
	return ent.runSteps(step.Branches[len(step.Branches)-1].Steps)
}

// holdAt parks the entity at a position, used while it waits in a queue. The
// far-future endpoint is replaced by the next segment the entity writes, so
// the interpolator always has somewhere to put it.
func (ent *entity) holdAt(e *engine, x, y, z float64) {
	ent.x, ent.y, ent.z = x, y, z
	e.trace.Segment(godes.GetSystemTime(), ent.id, x, y, z)
}

// finish removes the entity, giving back anything it still holds. Releasing on
// the way out is not tidiness: a resource an exiting entity kept would block
// every entity behind it for the rest of the run.
func (ent *entity) finish() {
	e := ent.engine

	for i := len(ent.held) - 1; i >= 0; i-- {
		e.release(ent.held[i], ent)
	}
	ent.held = nil

	now := godes.GetSystemTime()
	e.trace.Exit(now, ent.id)

	e.stillActive--

	if ent.cutShort {
		e.cutShort++
		return
	}

	e.completed++
	if now >= e.warmUp {
		e.sysTimes = append(e.sysTimes, now-ent.createdAt)
	}
}
