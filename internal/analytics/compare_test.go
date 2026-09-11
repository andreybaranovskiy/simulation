package analytics

import (
	"math"
	"testing"
)

func TestSampleStatistics(t *testing.T) {
	// A textbook set: mean 5, sample standard deviation 2.
	s := NewSample([]float64{3, 5, 7, 5, 5})

	if s.N != 5 {
		t.Errorf("N = %d, want 5", s.N)
	}
	if math.Abs(s.Mean-5) > 1e-9 {
		t.Errorf("mean = %g, want 5", s.Mean)
	}
	if math.Abs(s.StdDev-math.Sqrt(2)) > 1e-9 {
		t.Errorf("standard deviation = %g, want %g", s.StdDev, math.Sqrt(2))
	}
	if s.Min != 3 || s.Max != 7 {
		t.Errorf("range is %g to %g, want 3 to 7", s.Min, s.Max)
	}
	if !s.HasInterval {
		t.Error("five replications should support an interval")
	}
	if !(s.CILow < s.Mean && s.Mean < s.CIHigh) {
		t.Errorf("the interval %g to %g does not contain the mean %g", s.CILow, s.CIHigh, s.Mean)
	}
}

// A single run cannot support an interval, and the flag is what stops the
// collapsed interval from being read as certainty.
func TestSingleReplicationHasNoInterval(t *testing.T) {
	s := NewSample([]float64{42})

	if s.HasInterval {
		t.Error("one replication should not claim an interval")
	}
	if s.Mean != 42 || s.StdDev != 0 {
		t.Errorf("mean %g, standard deviation %g; want 42 and 0", s.Mean, s.StdDev)
	}
	if s.CILow != 42 || s.CIHigh != 42 {
		t.Error("with no interval the bounds should sit on the mean")
	}
}

func TestEmptySampleIsHarmless(t *testing.T) {
	s := NewSample(nil)

	if s.N != 0 || s.Mean != 0 || s.HasInterval {
		t.Errorf("an empty sample produced %+v", s)
	}
}

// The point of the whole file: a difference smaller than the spread must not be
// reported as a finding.
func TestNoiseIsNotAFinding(t *testing.T) {
	// Two scenarios whose means differ by 2 while each varies by far more.
	baseline := NewSample([]float64{100, 130, 70, 115, 85})
	other := NewSample([]float64{102, 128, 74, 110, 96})

	d := Compare(baseline, other, Lower)

	if !d.HasInterval {
		t.Fatal("five replications a side should support an interval")
	}
	if d.Distinguishable {
		t.Errorf("a %.1f difference against a spread of ~%.0f was called distinguishable",
			d.Delta, baseline.StdDev)
	}
	if d.Better != nil {
		t.Error("an indistinguishable difference must not be labelled better or worse")
	}
}

// And a difference that clearly exceeds the spread must be reported.
func TestARealDifferenceIsFound(t *testing.T) {
	baseline := NewSample([]float64{100, 102, 98, 101, 99})
	other := NewSample([]float64{60, 62, 58, 61, 59})

	d := Compare(baseline, other, Lower)

	if !d.Distinguishable {
		t.Errorf("a 40-unit drop against a spread of ~1.6 was not called distinguishable: %+v", d)
	}
	if d.Better == nil || !*d.Better {
		t.Error("a large drop in a lower-is-better measure should be marked better")
	}
	if math.Abs(d.Delta-(-40)) > 1e-9 {
		t.Errorf("delta = %g, want -40", d.Delta)
	}
	if math.Abs(d.Percent-(-40)) > 0.5 {
		t.Errorf("percent = %g, want about -40", d.Percent)
	}
	// The interval must bracket the delta and stay on one side of zero.
	if !(d.CILow < d.Delta && d.Delta < d.CIHigh) {
		t.Errorf("the interval %g to %g does not contain the delta %g", d.CILow, d.CIHigh, d.Delta)
	}
	if d.CIHigh >= 0 {
		t.Errorf("the interval %g to %g touches zero for a clear improvement", d.CILow, d.CIHigh)
	}
}

// Direction is a property of the measure. The same drop is an improvement in
// one and a regression in the other.
func TestDirectionFollowsTheMeasure(t *testing.T) {
	baseline := NewSample([]float64{100, 101, 99, 100, 100})
	other := NewSample([]float64{60, 61, 59, 60, 60})

	lower := Compare(baseline, other, Lower)
	higher := Compare(baseline, other, Higher)

	if lower.Better == nil || !*lower.Better {
		t.Error("a drop in a lower-is-better measure should be better")
	}
	if higher.Better == nil || *higher.Better {
		t.Error("the same drop in a higher-is-better measure should be worse")
	}

	// A measure with no declared direction, such as a count of arrivals, gets
	// no verdict at all.
	neither := Compare(baseline, other, "")
	if neither.Better != nil {
		t.Error("a measure with no direction should not be judged")
	}
}

// A comparison against a single run still reports the delta, but must not
// claim the difference is real.
func TestSingleRunComparisonMakesNoClaim(t *testing.T) {
	d := Compare(NewSample([]float64{100}), NewSample([]float64{60}), Lower)

	if d.Delta != -40 {
		t.Errorf("delta = %g, want -40", d.Delta)
	}
	if d.HasInterval || d.Distinguishable {
		t.Error("one run a side cannot support an interval or a claim")
	}
	if d.Better != nil {
		t.Error("without an interval there is no basis to call it better")
	}
}

// Welch's interval must widen when the two sides have unequal variance, which
// is the case it exists for.
func TestUnequalVarianceWidensTheInterval(t *testing.T) {
	baseline := NewSample([]float64{100, 100, 100, 100, 100, 100})

	tight := Compare(baseline, NewSample([]float64{90, 90, 90, 90, 90, 90}), Lower)
	loose := Compare(baseline, NewSample([]float64{60, 120, 75, 105, 80, 100}), Lower)

	tightWidth := tight.CIHigh - tight.CILow
	looseWidth := loose.CIHigh - loose.CILow

	if looseWidth <= tightWidth {
		t.Errorf("the noisy comparison's interval (%.2f) is not wider than the tight one (%.2f)",
			looseWidth, tightWidth)
	}
	if !tight.Distinguishable {
		t.Error("a consistent 10-unit difference should be distinguishable")
	}
}

func TestCriticalValuesAreConservative(t *testing.T) {
	// Fewer degrees of freedom must never give a narrower interval.
	previous := math.Inf(1)
	for _, degrees := range []int{1, 2, 5, 10, 20, 30, 60, 120, 1000} {
		v := tCritical95(degrees)
		if v > previous {
			t.Errorf("t(%d) = %g is larger than the value for fewer degrees of freedom (%g)",
				degrees, v, previous)
		}
		if v < 1.959 {
			t.Errorf("t(%d) = %g is below the large-sample limit of 1.96", degrees, v)
		}
		previous = v
	}
}

func TestComparisonNoteMatchesTheEvidence(t *testing.T) {
	if note := ComparisonNote(1); !contains(note, "once") {
		t.Errorf("the single-run note does not say the scenario was run once: %q", note)
	}
	if note := ComparisonNote(3); !contains(note, "wide") {
		t.Errorf("the few-replications note does not warn about width: %q", note)
	}
	if note := ComparisonNote(10); !contains(note, "95%") {
		t.Errorf("the many-replications note does not state the confidence level: %q", note)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
