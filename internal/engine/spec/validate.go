package spec

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Problem is one validation finding. Path points at the offending part of the
// spec so the model editor can highlight it.
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
	// Warning marks something suspicious but runnable, such as a resource
	// nothing ever seizes.
	Warning bool `json:"warning,omitempty"`
}

func (p Problem) String() string {
	kind := "error"
	if p.Warning {
		kind = "warning"
	}
	return fmt.Sprintf("%s at %s: %s", kind, p.Path, p.Message)
}

// ValidationError carries every problem found, so a user fixes a model in one
// pass instead of rediscovering the next mistake after each save.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	lines := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		if !p.Warning {
			lines = append(lines, "  "+p.String())
		}
	}
	return fmt.Sprintf("the model has %d problem(s):\n%s", len(lines), strings.Join(lines, "\n"))
}

// Validate checks a model for everything that would make a run fail or produce
// meaningless output. Errors block a run; warnings do not.
func (m *Model) Validate() []Problem {
	v := &validator{model: m, idx: m.BuildIndex()}

	v.checkHeader()
	v.checkParams()
	v.checkEntityTypes()
	v.checkNodes()
	v.checkLinks()
	v.checkResources()
	v.checkSources()
	v.checkRoutes()
	v.checkZones()
	v.checkReachability()

	sort.SliceStable(v.problems, func(i, j int) bool {
		if v.problems[i].Warning != v.problems[j].Warning {
			return !v.problems[i].Warning
		}
		return v.problems[i].Path < v.problems[j].Path
	})
	return v.problems
}

// ValidateStrict returns an error when any blocking problem was found.
func (m *Model) ValidateStrict() error {
	problems := m.Validate()
	for _, p := range problems {
		if !p.Warning {
			return &ValidationError{Problems: problems}
		}
	}
	return nil
}

type validator struct {
	model    *Model
	idx      *Index
	problems []Problem
	// seizedResources and visitedNodes accumulate during route checking so
	// unused parts of the model can be reported afterwards.
	seizedResources map[string]bool
	visitedNodes    map[string]bool
}

func (v *validator) fail(path, format string, args ...any) {
	v.problems = append(v.problems, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) warn(path, format string, args ...any) {
	v.problems = append(v.problems, Problem{Path: path, Message: fmt.Sprintf(format, args...), Warning: true})
}

func (v *validator) checkHeader() {
	if strings.TrimSpace(v.model.Name) == "" {
		v.fail("name", "the model needs a name")
	}
	if v.model.Schema != "" && v.model.Schema != SchemaVersion {
		v.fail("schema", "this build understands %s, the model declares %s", SchemaVersion, v.model.Schema)
	}

	if v.model.Horizon < 0 || math.IsNaN(v.model.Horizon) {
		v.fail("horizon", "the horizon must be zero or a positive number of seconds")
	}
	if v.model.WarmUp < 0 {
		v.fail("warmUp", "the warm-up period cannot be negative")
	}
	if v.model.Horizon > 0 && v.model.WarmUp >= v.model.Horizon {
		v.fail("warmUp", "the warm-up of %gs covers the whole %gs horizon, so nothing would be measured",
			v.model.WarmUp, v.model.Horizon)
	}

	// An unbounded horizon only terminates if every source stops on its own.
	if v.model.Horizon == 0 {
		unbounded := false
		for _, s := range v.model.Sources {
			if s.Limit == 0 && s.Stop == 0 {
				unbounded = true
				break
			}
		}
		if unbounded {
			v.fail("horizon", "the model has no horizon and at least one source never stops, so the run would never end")
		}
	}
}

func (v *validator) checkParams() {
	seen := map[string]bool{}
	for i, p := range v.model.Params {
		path := fmt.Sprintf("params[%d]", i)

		if p.ID == "" {
			v.fail(path, "a parameter needs an id")
			continue
		}
		if seen[p.ID] {
			v.fail(path, "duplicate parameter id %q", p.ID)
		}
		seen[p.ID] = true

		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			v.fail(path, "parameter %q has a value that is not a finite number", p.ID)
		}
		if p.Min != nil && p.Value < *p.Min {
			v.fail(path, "parameter %q is %g, below its minimum of %g", p.ID, p.Value, *p.Min)
		}
		if p.Max != nil && p.Value > *p.Max {
			v.fail(path, "parameter %q is %g, above its maximum of %g", p.ID, p.Value, *p.Max)
		}
		if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
			v.fail(path, "parameter %q has a minimum above its maximum", p.ID)
		}
	}
}

func (v *validator) checkEntityTypes() {
	if len(v.model.EntityTypes) == 0 {
		v.fail("entityTypes", "the model needs at least one entity type")
	}

	seen := map[string]bool{}
	for i := range v.model.EntityTypes {
		e := &v.model.EntityTypes[i]
		path := fmt.Sprintf("entityTypes[%d]", i)

		if e.ID == "" {
			v.fail(path, "an entity type needs an id")
			continue
		}
		if seen[e.ID] {
			v.fail(path, "duplicate entity type id %q", e.ID)
		}
		seen[e.ID] = true

		if err := e.Speed.Validate(); err != nil {
			v.fail(path+".speed", "%s", err)
		}
		// A zero speed would leave an entity travelling forever.
		if e.Speed.ExpectedValue() <= 0 {
			v.fail(path+".speed", "entity type %q has a speed of %g m/s, so it would never arrive anywhere",
				e.ID, e.Speed.ExpectedValue())
		}
	}
}

func (v *validator) checkNodes() {
	if len(v.model.Nodes) == 0 {
		v.fail("nodes", "the model needs at least one node")
	}

	seen := map[string]bool{}
	for i, n := range v.model.Nodes {
		path := fmt.Sprintf("nodes[%d]", i)

		if n.ID == "" {
			v.fail(path, "a node needs an id")
			continue
		}
		if seen[n.ID] {
			v.fail(path, "duplicate node id %q", n.ID)
		}
		seen[n.ID] = true

		if math.IsNaN(n.X) || math.IsNaN(n.Y) || math.IsInf(n.X, 0) || math.IsInf(n.Y, 0) {
			v.fail(path, "node %q has coordinates that are not finite numbers", n.ID)
		}
	}
}

func (v *validator) checkLinks() {
	for i, l := range v.model.Links {
		path := fmt.Sprintf("links[%d]", i)

		if v.idx.Nodes[l.From] == nil {
			v.fail(path+".from", "link refers to unknown node %q", l.From)
		}
		if v.idx.Nodes[l.To] == nil {
			v.fail(path+".to", "link refers to unknown node %q", l.To)
		}
		if l.From == l.To {
			v.warn(path, "link joins node %q to itself", l.From)
		}
		if l.Distance < 0 {
			v.fail(path+".distance", "a link distance cannot be negative")
		}
		if l.SpeedLimit < 0 {
			v.fail(path+".speedLimit", "a speed limit cannot be negative")
		}
		if l.Capacity < 0 {
			v.fail(path+".capacity", "a link capacity cannot be negative")
		}
	}
}

func (v *validator) checkResources() {
	seen := map[string]bool{}
	for i := range v.model.Resources {
		r := &v.model.Resources[i]
		path := fmt.Sprintf("resources[%d]", i)

		if r.ID == "" {
			v.fail(path, "a resource needs an id")
			continue
		}
		if seen[r.ID] {
			v.fail(path, "duplicate resource id %q", r.ID)
		}
		seen[r.ID] = true

		if r.Capacity <= 0 {
			v.fail(path+".capacity", "resource %q has capacity %d, so nothing could ever be served",
				r.ID, r.Capacity)
		}
		if r.Node != "" && v.idx.Nodes[r.Node] == nil {
			v.fail(path+".node", "resource %q sits at unknown node %q", r.ID, r.Node)
		}
		if r.Node == "" {
			v.warn(path+".node", "resource %q has no node, so its queue and utilisation cannot be drawn on the plan", r.ID)
		}

		if err := r.Service.Validate(); err != nil {
			v.fail(path+".service", "%s", err)
		}

		switch r.Queue.Discipline {
		case "", FIFO, LIFO, Priority:
		default:
			v.fail(path+".queue.discipline", "unknown discipline %q, expected fifo, lifo or priority", r.Queue.Discipline)
		}
		if r.Queue.Capacity < 0 {
			v.fail(path+".queue.capacity", "a queue capacity cannot be negative")
		}
		if r.Queue.MaxWait < 0 {
			v.fail(path+".queue.maxWait", "a maximum wait cannot be negative")
		}

		v.checkShifts(path, r)

		if r.Failure != nil {
			if err := r.Failure.Uptime.Validate(); err != nil {
				v.fail(path+".failure.uptime", "%s", err)
			}
			if err := r.Failure.Repair.Validate(); err != nil {
				v.fail(path+".failure.repair", "%s", err)
			}
			if r.Failure.Uptime.ExpectedValue() <= 0 {
				v.fail(path+".failure.uptime", "a mean uptime of zero would break the resource immediately and forever")
			}
		}
	}
}

func (v *validator) checkShifts(path string, r *Resource) {
	for j, s := range r.Shifts {
		shiftPath := fmt.Sprintf("%s.shifts[%d]", path, j)

		if s.Start < 0 || s.End < 0 {
			v.fail(shiftPath, "shift times cannot be negative")
		}
		if s.End <= s.Start {
			v.fail(shiftPath, "shift ends at %gs, which is not after its %gs start", s.End, s.Start)
		}
		if s.End > 86400 {
			v.fail(shiftPath, "a shift is a time of day, so it must end within 86400 seconds")
		}
		for _, d := range s.Days {
			if d < 0 || d > 6 {
				v.fail(shiftPath+".days", "day %d is outside 0 to 6", d)
			}
		}
	}
}

func (v *validator) checkSources() {
	if len(v.model.Sources) == 0 {
		v.fail("sources", "the model needs at least one source, or no entities would ever arrive")
	}

	seen := map[string]bool{}
	for i := range v.model.Sources {
		s := &v.model.Sources[i]
		path := fmt.Sprintf("sources[%d]", i)

		if s.ID == "" {
			v.fail(path, "a source needs an id")
			continue
		}
		if seen[s.ID] {
			v.fail(path, "duplicate source id %q", s.ID)
		}
		seen[s.ID] = true

		if v.idx.Entities[s.Entity] == nil {
			v.fail(path+".entity", "source %q creates unknown entity type %q", s.ID, s.Entity)
		}
		if v.idx.Nodes[s.Node] == nil {
			v.fail(path+".node", "source %q starts at unknown node %q", s.ID, s.Node)
		}

		if err := s.Arrival.Validate(); err != nil {
			v.fail(path+".arrival", "%s", err)
		}
		// A mean gap of zero produces infinitely many arrivals at time zero.
		if s.Arrival.ExpectedValue() <= 0 {
			v.fail(path+".arrival", "source %q has a mean arrival gap of %g, which would create entities without end at a single instant",
				s.ID, s.Arrival.ExpectedValue())
		}

		if s.Batch.Kind != "" {
			if err := s.Batch.Validate(); err != nil {
				v.fail(path+".batch", "%s", err)
			}
			if s.Batch.ExpectedValue() > 10000 {
				v.warn(path+".batch", "a mean batch of %g entities will be slow to simulate", s.Batch.ExpectedValue())
			}
		}

		if s.Start < 0 {
			v.fail(path+".start", "a start time cannot be negative")
		}
		if s.Stop != 0 && s.Stop <= s.Start {
			v.fail(path+".stop", "source %q stops at %gs, which is not after its %gs start", s.ID, s.Stop, s.Start)
		}
		if s.Limit < 0 {
			v.fail(path+".limit", "a limit cannot be negative")
		}

		if route := v.model.RouteFor(s.ID); route == nil {
			v.fail(path, "source %q has no route: set its route field, or give a route a source of %q", s.ID, s.ID)
		}
	}
}

func (v *validator) checkRoutes() {
	v.seizedResources = map[string]bool{}
	v.visitedNodes = map[string]bool{}

	seen := map[string]bool{}
	for i := range v.model.Routes {
		r := &v.model.Routes[i]
		path := fmt.Sprintf("routes[%d]", i)

		if r.ID == "" {
			v.fail(path, "a route needs an id")
			continue
		}
		if seen[r.ID] {
			v.fail(path, "duplicate route id %q", r.ID)
		}
		seen[r.ID] = true

		if r.Source != "" && v.idx.Sources[r.Source] == nil {
			v.fail(path+".source", "route %q names unknown source %q", r.ID, r.Source)
		}
		if len(r.Steps) == 0 {
			v.fail(path+".steps", "route %q has no steps", r.ID)
			continue
		}

		// held tracks resources seized but not yet released, so a route that
		// leaks a resource is caught before it deadlocks a run.
		held := map[string]bool{}
		v.checkSteps(path+".steps", r.Steps, held, 0)

		for id := range held {
			v.fail(path, "route %q seizes resource %q and never releases it, which would block every entity behind it",
				r.ID, id)
		}
	}

	v.reportUnused()
}

// checkSteps validates a step list, recursing into branches. depth guards
// against a pathological nesting that would blow the stack.
func (v *validator) checkSteps(path string, steps []Step, held map[string]bool, depth int) {
	if depth > 8 {
		v.fail(path, "branches are nested more than 8 deep")
		return
	}

	for i, s := range steps {
		stepPath := fmt.Sprintf("%s[%d]", path, i)

		switch s.Type {
		case StepTravel:
			if v.idx.Nodes[s.To] == nil {
				v.fail(stepPath+".to", "travel to unknown node %q", s.To)
			} else {
				v.visitedNodes[s.To] = true
			}

		case StepSeize:
			v.checkResourceRef(stepPath, s.Resource)
			if held[s.Resource] {
				v.fail(stepPath, "resource %q is seized twice without being released, which deadlocks against itself", s.Resource)
			}
			held[s.Resource] = true
			v.seizedResources[s.Resource] = true

		case StepRelease:
			v.checkResourceRef(stepPath, s.Resource)
			if !held[s.Resource] {
				v.fail(stepPath, "resource %q is released without having been seized", s.Resource)
			}
			delete(held, s.Resource)

		case StepUse:
			v.checkResourceRef(stepPath, s.Resource)
			v.seizedResources[s.Resource] = true
			if s.Duration == nil {
				if r := v.idx.Resources[s.Resource]; r != nil && r.Service.Kind == "" && r.Service.Value == 0 {
					v.warn(stepPath, "neither the step nor resource %q gives a service time, so the step takes no time",
						s.Resource)
				}
			}

		case StepDelay:
			if s.Duration == nil {
				v.fail(stepPath+".duration", "a delay step needs a duration")
			}

		case StepBranch:
			if len(s.Branches) == 0 {
				v.fail(stepPath+".branches", "a branch step needs at least one branch")
			}
			total := 0.0
			for j, b := range s.Branches {
				branchPath := fmt.Sprintf("%s.branches[%d]", stepPath, j)
				if b.Weight < 0 {
					v.fail(branchPath+".weight", "a branch weight cannot be negative")
				}
				total += b.Weight

				// Each branch gets its own copy of the held set: what one
				// branch seizes says nothing about the others.
				branchHeld := copySet(held)
				v.checkSteps(branchPath+".steps", b.Steps, branchHeld, depth+1)

				for id := range branchHeld {
					if !held[id] {
						v.fail(branchPath, "this branch seizes resource %q without releasing it", id)
					}
				}
			}
			if total <= 0 {
				v.fail(stepPath+".branches", "the branch weights sum to zero, so no branch could ever be taken")
			}

		case StepExit:
			if i != len(steps)-1 {
				v.warn(stepPath, "steps after an exit are never reached")
			}
			if len(held) > 0 {
				v.fail(stepPath, "the entity exits while still holding %s", strings.Join(sortedKeys(held), ", "))
			}

		default:
			v.fail(stepPath+".type", "unknown step type %q", s.Type)
		}

		if s.Duration != nil {
			if err := s.Duration.Validate(); err != nil {
				v.fail(stepPath+".duration", "%s", err)
			}
		}
	}
}

func (v *validator) checkResourceRef(path, id string) {
	if id == "" {
		v.fail(path+".resource", "this step needs a resource")
		return
	}
	if v.idx.Resources[id] == nil {
		v.fail(path+".resource", "unknown resource %q", id)
	}
}

// reportUnused flags parts of the model nothing refers to. These are warnings:
// a model under construction legitimately has loose ends, but a resource no
// route seizes is usually a typo in an id.
func (v *validator) reportUnused() {
	for i, r := range v.model.Resources {
		if !v.seizedResources[r.ID] {
			v.warn(fmt.Sprintf("resources[%d]", i), "no route ever uses resource %q", r.ID)
		}
	}
	for i, e := range v.model.EntityTypes {
		used := false
		for _, s := range v.model.Sources {
			if s.Entity == e.ID {
				used = true
				break
			}
		}
		if !used {
			v.warn(fmt.Sprintf("entityTypes[%d]", i), "no source creates entity type %q", e.ID)
		}
	}
}

// checkReachability confirms every travel destination can actually be reached
// over the link network. Without this a model runs and then quietly strands
// its entities.
func (v *validator) checkReachability() {
	if len(v.model.Links) == 0 {
		// With no links, travel falls back to straight-line movement between
		// any two nodes, which is always possible.
		return
	}

	adjacency := make(map[string][]string, len(v.model.Nodes))
	for _, l := range v.model.Links {
		adjacency[l.From] = append(adjacency[l.From], l.To)
		if l.Bidirectional {
			adjacency[l.To] = append(adjacency[l.To], l.From)
		}
	}

	for i := range v.model.Sources {
		s := &v.model.Sources[i]
		route := v.model.RouteFor(s.ID)
		if route == nil || v.idx.Nodes[s.Node] == nil {
			continue
		}

		reachable := reachableFrom(s.Node, adjacency)
		for _, dest := range travelDestinations(route.Steps) {
			if v.idx.Nodes[dest] == nil {
				continue
			}
			if !reachable[dest] {
				v.fail(fmt.Sprintf("routes[%s]", route.ID),
					"node %q cannot be reached from source %q over the link network", dest, s.ID)
			}
			// Later steps travel onward from here, so the frontier grows.
			for n := range reachableFrom(dest, adjacency) {
				reachable[n] = true
			}
		}
	}
}

func reachableFrom(start string, adjacency map[string][]string) map[string]bool {
	seen := map[string]bool{start: true}
	stack := []string{start}

	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		for _, next := range adjacency[n] {
			if !seen[next] {
				seen[next] = true
				stack = append(stack, next)
			}
		}
	}
	return seen
}

func travelDestinations(steps []Step) []string {
	var out []string
	for _, s := range steps {
		if s.Type == StepTravel && s.To != "" {
			out = append(out, s.To)
		}
		for _, b := range s.Branches {
			out = append(out, travelDestinations(b.Steps)...)
		}
	}
	return out
}

func (v *validator) checkZones() {
	seen := map[string]bool{}
	for i, z := range v.model.Zones {
		path := fmt.Sprintf("zones[%d]", i)

		if z.ID == "" {
			v.fail(path, "a zone needs an id")
			continue
		}
		if seen[z.ID] {
			v.fail(path, "duplicate zone id %q", z.ID)
		}
		seen[z.ID] = true

		if z.Width <= 0 || z.Height <= 0 {
			v.fail(path, "zone %q has no area", z.ID)
		}
	}
}

func copySet(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sortedKeys(in map[string]bool) []string {
	out := make([]string, 0, len(in))
	for k := range in {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
