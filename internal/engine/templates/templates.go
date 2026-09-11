// Package templates provides ready-made models for the three supported
// domains.
//
// A template is a function from a typed parameter set to a spec.Model. That
// shape matters: because a scenario is a set of parameter values rather than a
// hand-edited model, two scenarios of the same template are directly
// comparable, and every template gets the KPI dashboard, heatmaps and reports
// without writing anything domain-specific.
package templates

import (
	"fmt"
	"sort"

	"github.com/andreybaranovskiy/simulation/internal/engine/rng"
	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
)

// Template describes one model generator and the knobs it exposes.
type Template struct {
	Key         string      `json:"key"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Domain      spec.Domain `json:"domain"`
	Params      []ParamDef  `json:"params"`

	build func(values map[string]float64) (*spec.Model, error)
}

// ParamDef is one tunable value, with the bounds and units the editor needs to
// render a sensible control.
type ParamDef struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Description string  `json:"description,omitempty"`
	Default     float64 `json:"default"`
	Min         float64 `json:"min"`
	Max         float64 `json:"max"`
	Step        float64 `json:"step,omitempty"`
	Unit        string  `json:"unit,omitempty"`
	// Integer marks a value that must be whole, such as a lane count.
	Integer bool `json:"integer,omitempty"`
	// Group lets the editor section a long parameter list.
	Group string `json:"group,omitempty"`
}

var registry = map[string]*Template{}

func register(t *Template) {
	registry[t.Key] = t
}

// Get returns a template by key.
func Get(key string) (*Template, bool) {
	t, ok := registry[key]
	return t, ok
}

// All lists the templates, in a stable order.
func All() []*Template {
	out := make([]*Template, 0, len(registry))
	for _, t := range registry {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Defaults returns the template's parameter values as shipped.
func (t *Template) Defaults() map[string]float64 {
	out := make(map[string]float64, len(t.Params))
	for _, p := range t.Params {
		out[p.ID] = p.Default
	}
	return out
}

// Build produces a model from parameter values. Missing values fall back to
// the defaults and out-of-range values are rejected, so a scenario cannot
// silently produce a model nobody intended.
func (t *Template) Build(values map[string]float64) (*spec.Model, error) {
	merged := t.Defaults()

	for _, p := range t.Params {
		v, given := values[p.ID]
		if !given {
			continue
		}
		if v < p.Min || v > p.Max {
			return nil, fmt.Errorf("%s is %g, outside the allowed range %g to %g",
				p.Label, v, p.Min, p.Max)
		}
		merged[p.ID] = v
	}

	for key := range values {
		if !t.hasParam(key) {
			return nil, fmt.Errorf("template %q has no parameter %q", t.Key, key)
		}
	}

	model, err := t.build(merged)
	if err != nil {
		return nil, err
	}

	model.ApplyDefaults()
	if err := model.ValidateStrict(); err != nil {
		return nil, fmt.Errorf("the template produced an invalid model: %w", err)
	}
	return model, nil
}

func (t *Template) hasParam(id string) bool {
	for _, p := range t.Params {
		if p.ID == id {
			return true
		}
	}
	return false
}

// specParams turns the template's definitions into the model's own parameter
// list, so a report can show what a run was configured with.
func (t *Template) specParams(values map[string]float64) []spec.Param {
	out := make([]spec.Param, 0, len(t.Params))
	for _, p := range t.Params {
		min, max := p.Min, p.Max
		out = append(out, spec.Param{
			ID:          p.ID,
			Label:       p.Label,
			Value:       values[p.ID],
			Min:         &min,
			Max:         &max,
			Unit:        p.Unit,
			Description: p.Description,
		})
	}
	return out
}

// intOf rounds a parameter that must be whole, with a floor so a zero-capacity
// resource can never reach the model.
func intOf(v float64, minimum int) int {
	n := int(v + 0.5)
	if n < minimum {
		return minimum
	}
	return n
}

// clampWarmUp keeps the warm-up below the horizon.
//
// The two are independent parameters with fixed ranges, so nothing stops a
// user asking for a twelve-hour warm-up on an eight-hour run. Rejecting the
// model would be technically correct and useless; capping the warm-up at half
// the horizon leaves a measured period either way.
func clampWarmUp(warmUpSeconds, horizonSeconds float64) float64 {
	if warmUpSeconds < 0 {
		return 0
	}
	if limit := horizonSeconds / 2; warmUpSeconds > limit {
		return limit
	}
	return warmUpSeconds
}

// tri is the triangular distribution, which is what a planner can actually
// estimate: a best case, a typical case and a worst case.
func tri(min, mode, max float64) rng.Dist {
	return rng.Dist{Kind: rng.Triangular, Min: min, Mode: mode, Max: max}
}

// triAround spreads a service time around a stated mean, skewed right the way
// real service times are: most jobs near the mode, a thin tail of slow ones.
//
// The multipliers are chosen so the mean of the distribution equals the value
// passed in, since (0.6 + 0.9 + 1.5) / 3 is exactly 1. Picking them any other
// way would make a parameter labelled "mean service time" quietly mean
// something larger, which would make every capacity sized from it wrong.
func triAround(mean float64) rng.Dist {
	return tri(mean*0.6, mean*0.9, mean*1.5)
}

// triAroundWide is triAround with a longer tail, for a step whose duration
// varies a lot. Its multipliers also average to 1.
func triAroundWide(mean float64) rng.Dist {
	return tri(mean*0.4, mean*0.8, mean*1.8)
}

func expo(mean float64) rng.Dist {
	return rng.Dist{Kind: rng.Exponential, Mean: mean}
}

// normPositive is a normal distribution clamped away from zero, used for
// speeds and service times where a negative draw is meaningless.
func normPositive(mean, sd, floor float64) rng.Dist {
	return rng.Dist{Kind: rng.Normal, Mean: mean, SD: sd, ClampMin: &floor}
}
