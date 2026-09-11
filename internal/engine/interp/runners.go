package interp

import (
	"math"

	"github.com/agoussia/godes"

	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
	"github.com/andreybaranovskiy/simulation/internal/engine/trace"
)

// secondsPerDay is the period a shift definition repeats over.
const secondsPerDay = 86400

// sourceRunner creates entities. It is a godes runner of its own so that
// arrivals are scheduled on the simulation clock rather than driven by a loop
// in the host.
type sourceRunner struct {
	*godes.Runner

	engine *engine
	spec   *spec.Source
	route  *spec.Route
}

func (s *sourceRunner) Run() {
	e := s.engine
	defer func() { e.sourcesActive-- }()

	arrivals := e.rng.Stream("arrival:" + s.spec.ID)
	batches := e.rng.Stream("batch:" + s.spec.ID)
	speeds := e.rng.Stream("speed:" + s.spec.Entity)

	if s.spec.Start > 0 {
		godes.Advance(s.spec.Start)
	}

	class := e.idx.Entities[s.spec.Entity]
	startNode, ok := e.net.index(s.spec.Node)
	if class == nil || !ok {
		return
	}

	created := 0

	for {
		now := godes.GetSystemTime()

		if e.horizon > 0 && now >= e.horizon {
			return
		}
		if s.spec.Stop > 0 && now >= s.spec.Stop {
			return
		}
		if s.spec.Limit > 0 && created >= s.spec.Limit {
			return
		}

		count := 1
		if s.spec.Batch.Kind != "" {
			count = int(math.Round(s.spec.Batch.Sample(batches)))
			if count < 1 {
				count = 1
			}
		}

		for i := 0; i < count; i++ {
			if s.spec.Limit > 0 && created >= s.spec.Limit {
				break
			}
			if e.created >= e.maxEntities {
				// The model is creating entities faster than it can clear
				// them. Stopping is better than exhausting memory, and the
				// result records that this happened.
				e.limitHit = true
				return
			}

			x, y, z := e.net.position(startNode)
			ent := &entity{
				Runner: &godes.Runner{},
				engine: e,
				id:     e.nextEntityID(),
				class:  class,
				route:  s.route,
				node:   startNode,
				x:      x,
				y:      y,
				z:      z,
				speed:  positiveSpeed(class.Speed.Sample(speeds)),
			}

			e.created++
			e.stillActive++
			created++
			godes.AddRunner(ent)
		}

		gap := s.spec.Arrival.SampleDuration(arrivals)
		if gap <= 0 {
			// A zero gap would spin forever at one instant. Validation rejects
			// a mean of zero, but a distribution can still draw one, so a
			// floor keeps the clock moving.
			gap = 1e-6
		}
		godes.Advance(gap)
	}
}

// shiftRunner opens and closes a resource on its working pattern. A resource
// outside its shift serves nobody, and its queue keeps growing, which is
// exactly the effect a planner wants to see.
type shiftRunner struct {
	*godes.Runner

	engine   *engine
	resource *resource
}

func (s *shiftRunner) Run() {
	e := s.engine

	// The resource starts closed unless the run begins inside a shift.
	e.setDown(s.resource, !s.inShift(0), trace.EvShiftStart)

	for {
		now := godes.GetSystemTime()
		if e.horizon > 0 && now >= e.horizon {
			return
		}
		// A model with no horizon ends when the work does. A shift runner that
		// kept scheduling boundaries would hold the simulation open forever.
		if e.idle() {
			return
		}

		next := s.nextBoundary(now)
		if next <= now {
			return
		}
		if e.horizon > 0 && next > e.horizon {
			godes.Advance(e.horizon - now)
			return
		}

		godes.Advance(next - now)

		open := s.inShift(godes.GetSystemTime())
		kind := trace.EvShiftStart
		if !open {
			kind = trace.EvShiftEnd
		}
		e.setDown(s.resource, !open, kind)
	}
}

// inShift reports whether any declared shift covers this moment.
func (s *shiftRunner) inShift(t float64) bool {
	day := int(math.Floor(t/secondsPerDay)) % 7
	if day < 0 {
		day += 7
	}
	timeOfDay := math.Mod(t, secondsPerDay)

	for _, sh := range s.resource.spec.Shifts {
		if !dayMatches(sh.Days, day) {
			continue
		}
		if timeOfDay >= sh.Start && timeOfDay < sh.End {
			return true
		}
	}
	return false
}

// nextBoundary is the next moment the resource's availability changes. It is
// computed rather than polled, so an idle night costs one scheduled event.
func (s *shiftRunner) nextBoundary(t float64) float64 {
	dayStart := math.Floor(t/secondsPerDay) * secondsPerDay
	best := math.Inf(1)

	// Look at today and the next seven days, which is enough for any weekly
	// pattern to produce at least one boundary.
	for d := 0; d <= 7; d++ {
		base := dayStart + float64(d)*secondsPerDay
		day := (int(base/secondsPerDay)%7 + 7) % 7

		for _, sh := range s.resource.spec.Shifts {
			if !dayMatches(sh.Days, day) {
				continue
			}
			for _, boundary := range []float64{base + sh.Start, base + sh.End} {
				if boundary > t && boundary < best {
					best = boundary
				}
			}
		}
	}

	if math.IsInf(best, 1) {
		return t
	}
	return best
}

func dayMatches(days []int, day int) bool {
	if len(days) == 0 {
		return true
	}
	for _, d := range days {
		if d == day {
			return true
		}
	}
	return false
}

// failureRunner alternates a resource between working and broken.
type failureRunner struct {
	*godes.Runner

	engine   *engine
	resource *resource
}

func (f *failureRunner) Run() {
	e := f.engine

	uptime := e.rng.Stream("uptime:" + f.resource.spec.ID)
	repair := e.rng.Stream("repair:" + f.resource.spec.ID)
	failure := f.resource.spec.Failure

	for {
		now := godes.GetSystemTime()
		if e.horizon > 0 && now >= e.horizon {
			return
		}
		if e.idle() {
			return
		}

		working := failure.Uptime.SampleDuration(uptime)
		if working <= 0 {
			return
		}
		godes.Advance(working)

		if e.horizon > 0 && godes.GetSystemTime() >= e.horizon {
			return
		}

		e.setDown(f.resource, true, trace.EvResourceDown)
		godes.Advance(failure.Repair.SampleDuration(repair))
		e.setDown(f.resource, false, trace.EvResourceUp)
	}
}

// positiveSpeed keeps a drawn speed usable. A normal distribution will
// eventually produce a negative or zero speed, and either would leave an
// entity travelling forever.
func positiveSpeed(v float64) float64 {
	if v <= 0 || math.IsNaN(v) {
		return 0.1
	}
	if math.IsInf(v, 1) {
		return 1000
	}
	return v
}
