// Package rng provides the seeded random sources and probability
// distributions the simulation engine draws from.
//
// Godes ships its own distributions, but they seed from a package-level
// counter or the wall clock, which makes a run impossible to reproduce from a
// scenario's stored seed. Reproducibility is the whole point of storing a
// seed, so the engine uses godes for scheduling and this package for
// randomness.
//
// Each stream is derived from the run seed and a name, so adding a new random
// draw somewhere in a model does not shift the numbers every other part of the
// model receives. That property is what lets two scenarios that differ in one
// parameter stay comparable.
package rng

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
)

// Source is a named, independently seeded random stream.
type Source struct {
	name string
	r    *rand.Rand
}

// Registry hands out one Source per name for a given run seed.
type Registry struct {
	seed    uint64
	streams map[string]*Source
}

func NewRegistry(seed uint64) *Registry {
	return &Registry{seed: seed, streams: make(map[string]*Source)}
}

// Stream returns the stream for name, creating it on first use. Calling it
// twice with the same name returns the same stream, so a resource's service
// times all come from one sequence.
func (reg *Registry) Stream(name string) *Source {
	if s, ok := reg.streams[name]; ok {
		return s
	}

	// Mixing the run seed with a hash of the name gives every stream an
	// independent starting point that is still a pure function of the seed.
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	nameHash := h.Sum64()

	s := &Source{
		name: name,
		r:    rand.New(rand.NewPCG(reg.seed^nameHash, splitMix64(reg.seed+nameHash))),
	}
	reg.streams[name] = s
	return s
}

// Seed reports the run seed the registry was built with.
func (reg *Registry) Seed() uint64 { return reg.seed }

// StreamNames lists the streams created so far, sorted, for run provenance.
func (reg *Registry) StreamNames() []string {
	names := make([]string, 0, len(reg.streams))
	for n := range reg.streams {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (s *Source) Float64() float64     { return s.r.Float64() }
func (s *Source) IntN(n int) int       { return s.r.IntN(n) }
func (s *Source) NormFloat64() float64 { return s.r.NormFloat64() }
func (s *Source) Name() string         { return s.name }

// Kind names a probability distribution in a model spec.
type Kind string

const (
	Constant    Kind = "constant"
	Uniform     Kind = "uniform"
	Normal      Kind = "normal"
	Exponential Kind = "exponential"
	Triangular  Kind = "triangular"
	LogNormal   Kind = "lognormal"
	// Empirical draws from a weighted set of values, which is how measured
	// data from a real site gets into a model without fitting a curve to it.
	Empirical Kind = "empirical"
)

// Dist describes a distribution declaratively. Only the fields relevant to
// Kind are read, so a spec stays readable:
//
//	{ "distribution": "triangular", "min": 40, "mode": 70, "max": 180 }
type Dist struct {
	Kind Kind `json:"distribution" yaml:"distribution"`

	Value float64 `json:"value,omitempty" yaml:"value,omitempty"` // constant
	Min   float64 `json:"min,omitempty" yaml:"min,omitempty"`
	Max   float64 `json:"max,omitempty" yaml:"max,omitempty"`
	Mean  float64 `json:"mean,omitempty" yaml:"mean,omitempty"`
	SD    float64 `json:"sd,omitempty" yaml:"sd,omitempty"`
	Mode  float64 `json:"mode,omitempty" yaml:"mode,omitempty"`

	// Values and Weights back the empirical distribution. Weights may be
	// omitted for a uniform choice among Values.
	Values  []float64 `json:"values,omitempty" yaml:"values,omitempty"`
	Weights []float64 `json:"weights,omitempty" yaml:"weights,omitempty"`

	// Clamp bounds every draw. It exists because a normal distribution will
	// eventually produce a negative service time, and a negative duration
	// would run the simulation clock backwards.
	ClampMin *float64 `json:"clampMin,omitempty" yaml:"clampMin,omitempty"`
	ClampMax *float64 `json:"clampMax,omitempty" yaml:"clampMax,omitempty"`
}

// Fixed builds a constant distribution, the common case in a simple model.
func Fixed(v float64) Dist { return Dist{Kind: Constant, Value: v} }

// Validate reports whether the parameters make sense for the kind. Catching
// this when a model is saved is far better than producing a NaN duration an
// hour into a run.
func (d Dist) Validate() error {
	switch d.Kind {
	case "", Constant:
		if !finite(d.Value) {
			return fmt.Errorf("constant distribution needs a finite value")
		}
	case Uniform:
		if !finite(d.Min) || !finite(d.Max) {
			return fmt.Errorf("uniform distribution needs finite min and max")
		}
		if d.Max < d.Min {
			return fmt.Errorf("uniform distribution has max %g below min %g", d.Max, d.Min)
		}
	case Normal:
		if !finite(d.Mean) || !finite(d.SD) {
			return fmt.Errorf("normal distribution needs finite mean and sd")
		}
		if d.SD < 0 {
			return fmt.Errorf("normal distribution has a negative sd %g", d.SD)
		}
	case Exponential:
		// A spec gives the mean because that is the quantity a planner
		// measures; the rate is derived.
		if !finite(d.Mean) || d.Mean <= 0 {
			return fmt.Errorf("exponential distribution needs a positive mean")
		}
	case Triangular:
		if !finite(d.Min) || !finite(d.Mode) || !finite(d.Max) {
			return fmt.Errorf("triangular distribution needs finite min, mode and max")
		}
		if !(d.Min <= d.Mode && d.Mode <= d.Max) {
			return fmt.Errorf("triangular distribution needs min <= mode <= max, got %g, %g, %g",
				d.Min, d.Mode, d.Max)
		}
		if d.Min == d.Max {
			return fmt.Errorf("triangular distribution has zero width")
		}
	case LogNormal:
		if !finite(d.Mean) || d.Mean <= 0 {
			return fmt.Errorf("lognormal distribution needs a positive mean")
		}
		if !finite(d.SD) || d.SD < 0 {
			return fmt.Errorf("lognormal distribution needs a non-negative sd")
		}
	case Empirical:
		if len(d.Values) == 0 {
			return fmt.Errorf("empirical distribution needs at least one value")
		}
		if len(d.Weights) > 0 && len(d.Weights) != len(d.Values) {
			return fmt.Errorf("empirical distribution has %d values but %d weights",
				len(d.Values), len(d.Weights))
		}
		total := 0.0
		for i, w := range d.Weights {
			if w < 0 || !finite(w) {
				return fmt.Errorf("empirical weight %d is %g, which is not a valid weight", i, w)
			}
			total += w
		}
		if len(d.Weights) > 0 && total <= 0 {
			return fmt.Errorf("empirical weights sum to zero")
		}
	default:
		return fmt.Errorf("unknown distribution %q, expected one of %s", d.Kind, strings.Join(KindNames(), ", "))
	}

	if d.ClampMin != nil && d.ClampMax != nil && *d.ClampMin > *d.ClampMax {
		return fmt.Errorf("clampMin %g is above clampMax %g", *d.ClampMin, *d.ClampMax)
	}
	return nil
}

// KindNames lists the supported distributions, for error messages and for the
// model editor's dropdown.
func KindNames() []string {
	return []string{
		string(Constant), string(Uniform), string(Normal),
		string(Exponential), string(Triangular), string(LogNormal), string(Empirical),
	}
}

// Sample draws one value from s. Validate is assumed to have passed.
func (d Dist) Sample(s *Source) float64 {
	var v float64

	switch d.Kind {
	case "", Constant:
		v = d.Value
	case Uniform:
		v = d.Min + s.Float64()*(d.Max-d.Min)
	case Normal:
		v = d.Mean + s.NormFloat64()*d.SD
	case Exponential:
		// ExpFloat64 has rate 1, so scaling by the mean gives the requested
		// distribution.
		v = d.Mean * expFloat64(s)
	case Triangular:
		v = triangular(s.Float64(), d.Min, d.Mode, d.Max)
	case LogNormal:
		mu, sigma := logNormalParams(d.Mean, d.SD)
		v = math.Exp(mu + sigma*s.NormFloat64())
	case Empirical:
		v = empirical(s, d.Values, d.Weights)
	}

	if d.ClampMin != nil && v < *d.ClampMin {
		v = *d.ClampMin
	}
	if d.ClampMax != nil && v > *d.ClampMax {
		v = *d.ClampMax
	}
	return v
}

// SampleDuration draws a value intended as a length of time and refuses to
// return a negative one. A negative duration would move the simulation clock
// backwards, which godes cannot represent and which no model means.
func (d Dist) SampleDuration(s *Source) float64 {
	v := d.Sample(s)
	if v < 0 || math.IsNaN(v) {
		return 0
	}
	if math.IsInf(v, 1) {
		return math.MaxFloat64 / 4
	}
	return v
}

// ExpectedValue is the analytic mean, used to size buffers and to show a
// model's nominal behaviour in the editor without running it.
func (d Dist) ExpectedValue() float64 {
	switch d.Kind {
	case "", Constant:
		return d.Value
	case Uniform:
		return (d.Min + d.Max) / 2
	case Normal, Exponential, LogNormal:
		return d.Mean
	case Triangular:
		return (d.Min + d.Mode + d.Max) / 3
	case Empirical:
		return empiricalMean(d.Values, d.Weights)
	}
	return 0
}

// String renders a distribution the way it reads in a report appendix.
func (d Dist) String() string {
	switch d.Kind {
	case "", Constant:
		return fmt.Sprintf("%g", d.Value)
	case Uniform:
		return fmt.Sprintf("uniform(%g, %g)", d.Min, d.Max)
	case Normal:
		return fmt.Sprintf("normal(mean %g, sd %g)", d.Mean, d.SD)
	case Exponential:
		return fmt.Sprintf("exponential(mean %g)", d.Mean)
	case Triangular:
		return fmt.Sprintf("triangular(%g, %g, %g)", d.Min, d.Mode, d.Max)
	case LogNormal:
		return fmt.Sprintf("lognormal(mean %g, sd %g)", d.Mean, d.SD)
	case Empirical:
		return fmt.Sprintf("empirical(%d values)", len(d.Values))
	}
	return string(d.Kind)
}

// triangular inverts the triangular CDF, which is exact and needs one draw
// rather than the rejection loop a naive implementation would use.
func triangular(u, min, mode, max float64) float64 {
	width := max - min
	if width <= 0 {
		return min
	}
	split := (mode - min) / width

	if u < split {
		return min + math.Sqrt(u*width*(mode-min))
	}
	return max - math.Sqrt((1-u)*width*(max-mode))
}

// logNormalParams converts the mean and standard deviation a planner measures
// into the underlying normal's parameters, which is what the formula needs.
func logNormalParams(mean, sd float64) (mu, sigma float64) {
	if mean <= 0 {
		return 0, 0
	}
	variance := sd * sd
	sigma = math.Sqrt(math.Log(1 + variance/(mean*mean)))
	mu = math.Log(mean) - sigma*sigma/2
	return mu, sigma
}

func empirical(s *Source, values, weights []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if len(weights) == 0 {
		return values[s.IntN(len(values))]
	}

	total := 0.0
	for _, w := range weights {
		total += w
	}
	target := s.Float64() * total

	for i, w := range weights {
		target -= w
		if target <= 0 {
			return values[i]
		}
	}
	return values[len(values)-1]
}

func empiricalMean(values, weights []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if len(weights) == 0 {
		sum := 0.0
		for _, v := range values {
			sum += v
		}
		return sum / float64(len(values))
	}

	sum, total := 0.0, 0.0
	for i, v := range values {
		sum += v * weights[i]
		total += weights[i]
	}
	if total == 0 {
		return 0
	}
	return sum / total
}

// expFloat64 draws from an exponential distribution with rate 1.
func expFloat64(s *Source) float64 {
	// Guard against Float64 returning exactly 0, where the logarithm diverges.
	u := s.Float64()
	for u == 0 {
		u = s.Float64()
	}
	return -math.Log(u)
}

// splitMix64 mixes a seed so the two PCG parameters are not correlated.
func splitMix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	z := x
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// SeedFromString derives a run seed from text, so a scenario can be given a
// memorable seed instead of a number.
func SeedFromString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// SeedBytes renders a seed for storage alongside a run.
func SeedBytes(seed uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, seed)
	return b
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
