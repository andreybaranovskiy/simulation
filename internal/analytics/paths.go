package analytics

import "math"

// Paths collects per-entity route traces, the spaghetti diagram.
//
// Every entity would be unreadable and enormous, so a sample is kept. The
// sample is chosen by entity id rather than at random, which makes it stable:
// two runs of the same scenario trace the same entities, and a reader
// comparing them is looking at like for like.
type Paths struct {
	// Sample keeps one entity in this many. One means every entity.
	Sample int
	// MaxPaths caps how many are kept at all.
	MaxPaths int
	// MinSegmentMeters drops movements too small to see, which is most of them
	// in a model where entities shuffle forward in a queue.
	MinSegmentMeters float64

	open      map[uint32]*openPath
	finished  []Path
	dropped   int
	totalSeen int
}

// Path is one entity's journey.
type Path struct {
	ID    uint32  `json:"id"`
	Class uint16  `json:"class"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	// Points are flattened x, y pairs, which is a third the size of an array of
	// objects once serialized and is what a canvas wants anyway.
	Points []float32 `json:"points"`
	// Distance is the route length in metres.
	Distance float64 `json:"distance"`
}

type openPath struct {
	class    uint16
	start    float64
	points   []float32
	lastX    float64
	lastY    float64
	distance float64
	end      float64
}

func NewPaths(sample, maxPaths int) *Paths {
	if sample < 1 {
		sample = 1
	}
	if maxPaths <= 0 {
		maxPaths = 400
	}
	return &Paths{
		Sample:           sample,
		MaxPaths:         maxPaths,
		MinSegmentMeters: 0.5,
		open:             make(map[uint32]*openPath),
	}
}

// tracks reports whether an entity is in the sample.
func (p *Paths) tracks(id uint32) bool {
	return int(id)%p.Sample == 0
}

// Begin starts a trace for an entity.
func (p *Paths) Begin(id uint32, class uint16, t, x, y float64) {
	p.totalSeen++

	if !p.tracks(id) || len(p.finished) >= p.MaxPaths {
		return
	}

	p.open[id] = &openPath{
		class:  class,
		start:  t,
		points: []float32{float32(x), float32(y)},
		lastX:  x,
		lastY:  y,
		end:    t,
	}
}

// Extend adds a point if the entity has moved far enough to be worth drawing.
func (p *Paths) Extend(id uint32, t, x, y float64) {
	op, ok := p.open[id]
	if !ok {
		return
	}

	op.end = t

	step := math.Hypot(x-op.lastX, y-op.lastY)
	if step < p.MinSegmentMeters {
		return
	}

	op.distance += step
	op.points = append(op.points, float32(x), float32(y))
	op.lastX, op.lastY = x, y

	// A path with more points than this is a scribble, not a route.
	const maxPoints = 2000
	if len(op.points) > maxPoints*2 {
		op.points = decimate(op.points)
	}
}

// End closes a trace and files it.
func (p *Paths) End(id uint32, t float64) {
	op, ok := p.open[id]
	if !ok {
		return
	}
	delete(p.open, id)

	// A path of one point is a dot, which tells a reader nothing.
	if len(op.points) < 4 {
		p.dropped++
		return
	}

	p.finished = append(p.finished, Path{
		ID: id, Class: op.class,
		Start: op.start, End: t,
		Points:   op.points,
		Distance: op.distance,
	})
}

// Close files whatever is still in flight when the run ends.
func (p *Paths) Close(endTime float64) {
	for id := range p.open {
		p.End(id, endTime)
	}
}

// Result reports the collected paths and how representative they are.
type PathsResult struct {
	Paths []Path `json:"paths"`
	// Sampled and Total let the UI say "showing 200 of 4,800 journeys" rather
	// than implying the picture is complete.
	Sampled int `json:"sampled"`
	Total   int `json:"total"`
}

func (p *Paths) Result() PathsResult {
	if p.finished == nil {
		p.finished = []Path{}
	}
	return PathsResult{
		Paths:   p.finished,
		Sampled: len(p.finished),
		Total:   p.totalSeen,
	}
}

// decimate halves a point list by dropping alternate interior points. The
// endpoints are kept, so a route still starts and ends where it did.
func decimate(points []float32) []float32 {
	if len(points) < 8 {
		return points
	}

	out := points[:2]
	for i := 2; i < len(points)-2; i += 4 {
		out = append(out, points[i], points[i+1])
	}
	return append(out, points[len(points)-2], points[len(points)-1])
}
