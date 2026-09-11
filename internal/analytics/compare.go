package analytics

import (
	"math"
	"sort"
)

// Sample is one scenario's replications of a single measure.
//
// A simulation is stochastic, so one run is one draw. Everything in this file
// exists to keep that fact visible: a difference between two scenarios only
// means something once it is large compared with how much the same scenario
// varies against itself.
type Sample struct {
	Values []float64 `json:"values"`
	N      int       `json:"n"`

	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stdDev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`

	// CILow and CIHigh bound the mean at 95% confidence. With a single
	// replication there is no spread to estimate and both equal the mean; the
	// HasInterval flag is what tells a reader that, rather than the interval
	// silently collapsing and looking like certainty.
	CILow       float64 `json:"ciLow"`
	CIHigh      float64 `json:"ciHigh"`
	HasInterval bool    `json:"hasInterval"`
}

// Summarise computes a sample's statistics.
func NewSample(values []float64) Sample {
	s := Sample{Values: values, N: len(values)}
	if s.N == 0 {
		return s
	}

	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	s.Min, s.Max = sorted[0], sorted[len(sorted)-1]

	sum := 0.0
	for _, v := range values {
		sum += v
	}
	s.Mean = sum / float64(s.N)

	if s.N < 2 {
		s.CILow, s.CIHigh = s.Mean, s.Mean
		return s
	}

	// The sample standard deviation, with Bessel's correction: these are
	// replications drawn from a process, not the whole population.
	variance := 0.0
	for _, v := range values {
		d := v - s.Mean
		variance += d * d
	}
	variance /= float64(s.N - 1)
	s.StdDev = math.Sqrt(variance)

	margin := tCritical95(s.N-1) * s.StdDev / math.Sqrt(float64(s.N))
	s.CILow = s.Mean - margin
	s.CIHigh = s.Mean + margin
	s.HasInterval = true

	return s
}

// Difference compares one scenario's sample against a baseline.
type Difference struct {
	Delta   float64 `json:"delta"`
	Percent float64 `json:"percent"`

	// CILow and CIHigh bound the difference of the means at 95% confidence.
	CILow  float64 `json:"ciLow"`
	CIHigh float64 `json:"ciHigh"`

	// Distinguishable reports whether the interval excludes zero. When it does
	// not, the two scenarios differ by less than their own run-to-run spread,
	// and reporting the delta as a result would be reading noise.
	Distinguishable bool `json:"distinguishable"`
	// HasInterval is false when either side has too few replications to
	// estimate spread. The delta is still shown; the claim is not.
	HasInterval bool `json:"hasInterval"`

	// Better says whether the change is an improvement, which only makes sense
	// once the difference is distinguishable and the measure declares a
	// direction.
	Better *bool `json:"better,omitempty"`
}

// Compare computes the difference between a sample and a baseline.
//
// Welch's interval rather than Student's, because two scenarios routinely have
// different variances: a configuration that removes a bottleneck is usually
// more consistent as well as faster, and assuming equal variances would
// understate the interval exactly when it matters.
func Compare(baseline, other Sample, better string) Difference {
	d := Difference{Delta: other.Mean - baseline.Mean}

	if baseline.Mean != 0 {
		d.Percent = d.Delta / math.Abs(baseline.Mean) * 100
	}

	if baseline.N < 2 || other.N < 2 {
		return d
	}

	va := baseline.StdDev * baseline.StdDev / float64(baseline.N)
	vb := other.StdDev * other.StdDev / float64(other.N)
	standardError := math.Sqrt(va + vb)

	if standardError == 0 {
		// Both scenarios were perfectly consistent. The difference is then
		// exactly what it appears to be, with no interval around it.
		d.HasInterval = true
		d.CILow, d.CIHigh = d.Delta, d.Delta
		d.Distinguishable = d.Delta != 0
		d.setBetter(better)
		return d
	}

	degrees := welchDegreesOfFreedom(baseline, other, va, vb)
	margin := tCritical95(int(math.Round(degrees))) * standardError

	d.CILow = d.Delta - margin
	d.CIHigh = d.Delta + margin
	d.HasInterval = true
	d.Distinguishable = (d.CILow > 0) == (d.CIHigh > 0)

	d.setBetter(better)
	return d
}

func (d *Difference) setBetter(better string) {
	if !d.Distinguishable || better == "" || d.Delta == 0 {
		return
	}

	improved := (better == Lower && d.Delta < 0) || (better == Higher && d.Delta > 0)
	d.Better = &improved
}

// welchDegreesOfFreedom is the Welch-Satterthwaite approximation.
func welchDegreesOfFreedom(a, b Sample, va, vb float64) float64 {
	numerator := (va + vb) * (va + vb)

	denominator := 0.0
	if a.N > 1 {
		denominator += va * va / float64(a.N-1)
	}
	if b.N > 1 {
		denominator += vb * vb / float64(b.N-1)
	}

	if denominator == 0 {
		return 1
	}
	return numerator / denominator
}

// tCritical95 is the two-tailed 95% critical value of Student's t.
//
// A table rather than a function: the values are exact where it matters most,
// at the small replication counts anyone actually runs, and a closed-form
// approximation is both less accurate there and harder to check.
var tTable95 = map[int]float64{
	1: 12.706, 2: 4.303, 3: 3.182, 4: 2.776, 5: 2.571,
	6: 2.447, 7: 2.365, 8: 2.306, 9: 2.262, 10: 2.228,
	11: 2.201, 12: 2.179, 13: 2.160, 14: 2.145, 15: 2.131,
	16: 2.120, 17: 2.110, 18: 2.101, 19: 2.093, 20: 2.086,
	21: 2.080, 22: 2.074, 23: 2.069, 24: 2.064, 25: 2.060,
	26: 2.056, 27: 2.052, 28: 2.048, 29: 2.045, 30: 2.042,
	40: 2.021, 50: 2.009, 60: 2.000, 80: 1.990, 100: 1.984,
}

func tCritical95(degrees int) float64 {
	if degrees < 1 {
		return tTable95[1]
	}
	if v, ok := tTable95[degrees]; ok {
		return v
	}

	// Between tabulated points, take the next larger listed value. Erring
	// towards a wider interval is the safe direction: it makes the tool slower
	// to claim a difference, never quicker.
	best := 1.960 // the large-sample limit
	bestAt := math.MaxInt32

	for at, v := range tTable95 {
		if at >= degrees && at < bestAt {
			best, bestAt = v, at
		}
	}
	return best
}

// ComparisonNote explains what a comparison can and cannot support, in the
// words a reader needs rather than as a statistic they have to interpret.
func ComparisonNote(replications int) string {
	switch {
	case replications < 2:
		return "Each scenario was run once. A single run is one draw from a random process, " +
			"so a difference here could be the model or could be the seed. Raise the " +
			"replication count to tell those apart."
	case replications < 5:
		return "With only a few replications the intervals are wide, so small differences " +
			"will not be distinguishable from run-to-run variation."
	default:
		return "Differences marked as distinguishable sit outside the run-to-run variation " +
			"of the scenarios themselves, at 95% confidence."
	}
}
