// Package spec defines the declarative simulation model format.
//
// One vocabulary covers container terminals, warehouses and generic queueing
// networks, because all three are the same thing underneath: entities arrive,
// travel over a network laid on the site plan, queue for resources of limited
// capacity, are served for some distributed time, and leave. Naming those
// pieces once means the analytics, heatmaps and reports work for every domain
// without knowing which one they are looking at.
//
// A spec is data. It is authored in the UI, stored as JSON, and executed by
// the interpreter in internal/engine/interp. Nothing here runs anything.
package spec

import (
	"github.com/andreybaranovskiy/simulation/internal/engine/rng"
)

// SchemaVersion is written into every spec so a stored model can be migrated
// when the format changes.
const SchemaVersion = "sim.model/v1"

// Model is a complete simulation model.
type Model struct {
	Schema      string `json:"schema" yaml:"schema"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Domain is a presentation hint that selects KPI labels and report
	// sections. It never changes how the model executes.
	Domain Domain `json:"domain,omitempty" yaml:"domain,omitempty"`

	Units Units `json:"units,omitempty" yaml:"units,omitempty"`

	// Horizon is how long to simulate, in seconds. Zero means run until
	// nothing is left to do, which only terminates for a model with finite
	// sources.
	Horizon float64 `json:"horizon" yaml:"horizon"`

	// WarmUp is a lead-in period excluded from the statistics. Queueing
	// systems start empty, which is not the steady state anyone wants to
	// report, so measurements begin after it.
	WarmUp float64 `json:"warmUp,omitempty" yaml:"warmUp,omitempty"`

	// Params are the tunable values a scenario overrides. Anywhere a number is
	// expected, a spec may instead reference one of these by name.
	Params []Param `json:"params,omitempty" yaml:"params,omitempty"`

	EntityTypes []EntityType `json:"entityTypes" yaml:"entityTypes"`
	Nodes       []Node       `json:"nodes" yaml:"nodes"`
	Links       []Link       `json:"links,omitempty" yaml:"links,omitempty"`
	Resources   []Resource   `json:"resources,omitempty" yaml:"resources,omitempty"`
	Sources     []Source     `json:"sources" yaml:"sources"`
	Routes      []Route      `json:"routes" yaml:"routes"`

	// Zones are named areas on the plan. They do not affect behaviour; they
	// group heatmap and KPI output into places a reader recognises, such as a
	// yard block or a picking aisle.
	Zones []Zone `json:"zones,omitempty" yaml:"zones,omitempty"`
}

// Domain selects the vocabulary reports use.
type Domain string

const (
	DomainTerminal  Domain = "terminal"
	DomainWarehouse Domain = "warehouse"
	DomainGeneric   Domain = "generic"
)

// Units records what the numbers mean. Only metres and seconds are supported
// internally; this exists so the UI can present feet or minutes and convert.
type Units struct {
	Length string `json:"length,omitempty" yaml:"length,omitempty"` // always "m" internally
	Time   string `json:"time,omitempty" yaml:"time,omitempty"`     // always "s" internally
}

// Param is a named number a scenario can override without editing the model.
// This is what makes scenario comparison meaningful: two runs of the same
// model differing only in declared parameters.
type Param struct {
	ID          string   `json:"id" yaml:"id"`
	Label       string   `json:"label,omitempty" yaml:"label,omitempty"`
	Value       float64  `json:"value" yaml:"value"`
	Min         *float64 `json:"min,omitempty" yaml:"min,omitempty"`
	Max         *float64 `json:"max,omitempty" yaml:"max,omitempty"`
	Unit        string   `json:"unit,omitempty" yaml:"unit,omitempty"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
}

// EntityType describes a kind of moving thing: a truck, a pallet, a customer.
type EntityType struct {
	ID    string `json:"id" yaml:"id"`
	Label string `json:"label,omitempty" yaml:"label,omitempty"`

	Shape Shape `json:"shape,omitempty" yaml:"shape,omitempty"`

	// Speed is the travel speed in metres per second. Drawing it from a
	// distribution per entity is what produces the spread of arrival times
	// that makes a queue form.
	Speed rng.Dist `json:"speed,omitempty" yaml:"speed,omitempty"`
}

// Shape is how an entity is drawn. Dimensions are metres.
type Shape struct {
	Type   string  `json:"type,omitempty" yaml:"type,omitempty"` // box, cylinder, marker
	Length float64 `json:"length,omitempty" yaml:"length,omitempty"`
	Width  float64 `json:"width,omitempty" yaml:"width,omitempty"`
	Height float64 `json:"height,omitempty" yaml:"height,omitempty"`
	Color  string  `json:"color,omitempty" yaml:"color,omitempty"`
	// Model names an uploaded GLB used instead of a primitive.
	Model string `json:"model,omitempty" yaml:"model,omitempty"`
}

// Node is a point on the site plan, in world metres. Positions come from the
// calibrated plan, which is what makes distances and therefore travel times
// real.
type Node struct {
	ID    string  `json:"id" yaml:"id"`
	Label string  `json:"label,omitempty" yaml:"label,omitempty"`
	X     float64 `json:"x" yaml:"x"`
	Y     float64 `json:"y" yaml:"y"`
	Z     float64 `json:"z,omitempty" yaml:"z,omitempty"`
}

// Link is a travelable connection between two nodes.
type Link struct {
	From string `json:"from" yaml:"from"`
	To   string `json:"to" yaml:"to"`
	// Bidirectional links are the norm for a road or an aisle.
	Bidirectional bool `json:"bidirectional,omitempty" yaml:"bidirectional,omitempty"`
	// Distance overrides the straight-line length, for a path that is not a
	// straight line between its endpoints.
	Distance float64 `json:"distance,omitempty" yaml:"distance,omitempty"`
	// SpeedLimit caps entity speed on this link, in metres per second.
	SpeedLimit float64 `json:"speedLimit,omitempty" yaml:"speedLimit,omitempty"`
	// Capacity limits how many entities may occupy the link at once. Zero
	// means unlimited. This is how a single-lane road produces congestion.
	Capacity int `json:"capacity,omitempty" yaml:"capacity,omitempty"`
}

// Discipline is the order a queue releases waiting entities in.
type Discipline string

const (
	FIFO     Discipline = "fifo"
	LIFO     Discipline = "lifo"
	Priority Discipline = "priority"
)

// Resource is anything of limited capacity that entities must wait for: a gate
// lane, a crane, a forklift, a dock door, a picker.
type Resource struct {
	ID    string `json:"id" yaml:"id"`
	Label string `json:"label,omitempty" yaml:"label,omitempty"`

	// Capacity is how many entities can be served at once.
	Capacity int `json:"capacity" yaml:"capacity"`

	// Node places the resource on the plan, so its queue and utilisation can
	// be drawn where they happen.
	Node string `json:"node,omitempty" yaml:"node,omitempty"`

	// Service is the default service time. A route step may override it.
	Service rng.Dist `json:"service,omitempty" yaml:"service,omitempty"`

	Queue QueueSpec `json:"queue,omitempty" yaml:"queue,omitempty"`

	// Shifts make the resource unavailable outside working hours. An empty
	// list means always available.
	Shifts []Shift `json:"shifts,omitempty" yaml:"shifts,omitempty"`

	// Failure models unplanned downtime.
	Failure *Failure `json:"failure,omitempty" yaml:"failure,omitempty"`
}

type QueueSpec struct {
	Discipline Discipline `json:"discipline,omitempty" yaml:"discipline,omitempty"`
	// Capacity limits how many may wait. Zero means unlimited. A full queue
	// makes arriving entities balk, which is recorded as a KPI.
	Capacity int `json:"capacity,omitempty" yaml:"capacity,omitempty"`
	// MaxWait makes an entity give up after waiting this long, in seconds.
	MaxWait float64 `json:"maxWait,omitempty" yaml:"maxWait,omitempty"`
}

// Shift is a recurring availability window, in seconds from the start of a day.
type Shift struct {
	Label string  `json:"label,omitempty" yaml:"label,omitempty"`
	Start float64 `json:"start" yaml:"start"`
	End   float64 `json:"end" yaml:"end"`
	// Days restricts the shift to particular days of the week, 0 being the
	// first day of the run. Empty means every day.
	Days []int `json:"days,omitempty" yaml:"days,omitempty"`
}

// Failure models breakdowns: the resource works for Uptime, then is out of
// action for Repair.
type Failure struct {
	Uptime rng.Dist `json:"uptime" yaml:"uptime"`
	Repair rng.Dist `json:"repair" yaml:"repair"`
}

// Source generates entities. It is where demand enters the model.
type Source struct {
	ID     string `json:"id" yaml:"id"`
	Label  string `json:"label,omitempty" yaml:"label,omitempty"`
	Entity string `json:"entity" yaml:"entity"`
	Node   string `json:"node" yaml:"node"`

	// Arrival is the gap between successive arrivals, in seconds.
	Arrival rng.Dist `json:"arrival" yaml:"arrival"`

	// Batch is how many entities arrive together. Zero or one means singly.
	Batch rng.Dist `json:"batch,omitempty" yaml:"batch,omitempty"`

	// Start delays the first arrival; Stop ends generation. Zero Stop means
	// generate for the whole horizon.
	Start float64 `json:"start,omitempty" yaml:"start,omitempty"`
	Stop  float64 `json:"stop,omitempty" yaml:"stop,omitempty"`

	// Limit caps the total entities created. Zero means unlimited.
	Limit int `json:"limit,omitempty" yaml:"limit,omitempty"`

	// Route names the route entities follow. Empty uses the route whose
	// Source field names this source.
	Route string `json:"route,omitempty" yaml:"route,omitempty"`
}

// Route is the sequence of steps an entity performs from arrival to exit.
type Route struct {
	ID     string `json:"id" yaml:"id"`
	Label  string `json:"label,omitempty" yaml:"label,omitempty"`
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
	Steps  []Step `json:"steps" yaml:"steps"`
}

// StepType is what an entity does at one point in its route.
type StepType string

const (
	// StepTravel moves the entity to a node along the link network, taking
	// time proportional to distance and speed.
	StepTravel StepType = "travel"
	// StepSeize waits for and then occupies a resource.
	StepSeize StepType = "seize"
	// StepRelease gives a held resource back.
	StepRelease StepType = "release"
	// StepDelay holds the entity in place for a distributed time. Combined
	// with seize and release this is a service operation.
	StepDelay StepType = "delay"
	// StepUse is seize, delay and release in one step, the common case.
	StepUse StepType = "use"
	// StepBranch sends the entity down one of several routes by probability.
	StepBranch StepType = "branch"
	// StepExit removes the entity and records its total time in the system.
	StepExit StepType = "exit"
)

type Step struct {
	Type  StepType `json:"type" yaml:"type"`
	Label string   `json:"label,omitempty" yaml:"label,omitempty"`

	// To is the destination node for travel.
	To string `json:"to,omitempty" yaml:"to,omitempty"`

	// Resource is the resource for seize, release and use.
	Resource string `json:"resource,omitempty" yaml:"resource,omitempty"`

	// Duration is the time for delay and use. For use, an empty Duration
	// falls back to the resource's own service distribution.
	Duration *rng.Dist `json:"duration,omitempty" yaml:"duration,omitempty"`

	// Branches are the alternatives for a branch step.
	Branches []Branch `json:"branches,omitempty" yaml:"branches,omitempty"`

	// Priority sets the entity's queue priority from here on, for resources
	// with a priority discipline. Higher goes first.
	Priority *int `json:"priority,omitempty" yaml:"priority,omitempty"`
}

// Branch is one alternative in a branch step.
type Branch struct {
	// Weight is the relative likelihood of taking this branch.
	Weight float64 `json:"weight" yaml:"weight"`
	// Steps are performed instead of continuing the parent route. Once they
	// finish the entity continues after the branch step.
	Steps []Step `json:"steps" yaml:"steps"`
	Label string `json:"label,omitempty" yaml:"label,omitempty"`
}

// Zone is a named rectangle on the plan used to group output.
type Zone struct {
	ID     string  `json:"id" yaml:"id"`
	Label  string  `json:"label,omitempty" yaml:"label,omitempty"`
	X      float64 `json:"x" yaml:"x"`
	Y      float64 `json:"y" yaml:"y"`
	Width  float64 `json:"width" yaml:"width"`
	Height float64 `json:"height" yaml:"height"`
	Color  string  `json:"color,omitempty" yaml:"color,omitempty"`
}

// NodeByID and the other lookups below are built once by Index rather than
// searched linearly, because the interpreter resolves references on every
// route step of every entity.
type Index struct {
	Nodes     map[string]*Node
	Entities  map[string]*EntityType
	Resources map[string]*Resource
	Sources   map[string]*Source
	Routes    map[string]*Route
	Params    map[string]*Param
}

func (m *Model) BuildIndex() *Index {
	idx := &Index{
		Nodes:     make(map[string]*Node, len(m.Nodes)),
		Entities:  make(map[string]*EntityType, len(m.EntityTypes)),
		Resources: make(map[string]*Resource, len(m.Resources)),
		Sources:   make(map[string]*Source, len(m.Sources)),
		Routes:    make(map[string]*Route, len(m.Routes)),
		Params:    make(map[string]*Param, len(m.Params)),
	}

	for i := range m.Nodes {
		idx.Nodes[m.Nodes[i].ID] = &m.Nodes[i]
	}
	for i := range m.EntityTypes {
		idx.Entities[m.EntityTypes[i].ID] = &m.EntityTypes[i]
	}
	for i := range m.Resources {
		idx.Resources[m.Resources[i].ID] = &m.Resources[i]
	}
	for i := range m.Sources {
		idx.Sources[m.Sources[i].ID] = &m.Sources[i]
	}
	for i := range m.Routes {
		idx.Routes[m.Routes[i].ID] = &m.Routes[i]
	}
	for i := range m.Params {
		idx.Params[m.Params[i].ID] = &m.Params[i]
	}
	return idx
}

// RouteFor resolves which route a source's entities follow.
func (m *Model) RouteFor(sourceID string) *Route {
	src := m.BuildIndex().Sources[sourceID]
	if src == nil {
		return nil
	}
	if src.Route != "" {
		return m.BuildIndex().Routes[src.Route]
	}
	for i := range m.Routes {
		if m.Routes[i].Source == sourceID {
			return &m.Routes[i]
		}
	}
	return nil
}

// ApplyParams overrides declared parameters with scenario values. Unknown keys
// are returned so the caller can report a scenario that references a parameter
// the model no longer has, rather than silently ignoring it.
func (m *Model) ApplyParams(values map[string]float64) []string {
	idx := m.BuildIndex()
	var unknown []string

	for key, value := range values {
		p, ok := idx.Params[key]
		if !ok {
			unknown = append(unknown, key)
			continue
		}
		p.Value = value
	}
	return unknown
}

// Bounds returns the extent of every node and zone in world metres, which
// sizes the heatmap grid and frames the 2D view.
func (m *Model) Bounds() (minX, minY, maxX, maxY float64, ok bool) {
	first := true
	grow := func(x, y float64) {
		if first {
			minX, minY, maxX, maxY = x, y, x, y
			first = false
			return
		}
		minX, minY = min(minX, x), min(minY, y)
		maxX, maxY = max(maxX, x), max(maxY, y)
	}

	for _, n := range m.Nodes {
		grow(n.X, n.Y)
	}
	for _, z := range m.Zones {
		grow(z.X, z.Y)
		grow(z.X+z.Width, z.Y+z.Height)
	}
	return minX, minY, maxX, maxY, !first
}
