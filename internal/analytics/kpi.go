package analytics

import (
	"math"
	"sort"
)

// KPI is one headline number from a run.
//
// The shape matters for comparison. A scenario comparison is a join of two
// runs' KPI lists on Key, so every KPI carries what a reader needs to judge a
// difference without knowing what the number means: its unit, whether higher
// is better, and how precisely to print it.
type KPI struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`

	// Group sections the dashboard: throughput, waiting, utilisation.
	Group string `json:"group"`
	// Better says which direction is an improvement: "lower", "higher", or
	// "" when neither is, such as a count of arrivals.
	Better string `json:"better,omitempty"`
	// Decimals is how many places to print. A queue of 3.7 is meaningful; a
	// queue of 3.7182 is noise.
	Decimals int `json:"decimals"`
	// Description explains what the number is, so a report page can stand on
	// its own for a reader who was not in the room.
	Description string `json:"description,omitempty"`
	// Headline marks the few numbers that belong on a summary card.
	Headline bool `json:"headline,omitempty"`
	// ResourceID ties a per-resource KPI back to its resource.
	ResourceID string `json:"resourceId,omitempty"`
}

// Units used across the KPI set.
const (
	UnitSeconds  = "s"
	UnitCount    = ""
	UnitPercent  = "%"
	UnitPerHour  = "/h"
	UnitEntities = "entities"
	UnitMeters   = "m"
)

// Direction constants for KPI.Better.
const (
	Lower  = "lower"
	Higher = "higher"
)

// KPISet is a run's full set, ordered for display.
type KPISet struct {
	KPIs []KPI `json:"kpis"`
	// Notes carry caveats that change how the numbers should be read, such as
	// a run that hit its entity limit.
	Notes []string `json:"notes,omitempty"`
}

// Add appends a KPI.
func (s *KPISet) Add(k KPI) {
	if math.IsNaN(k.Value) || math.IsInf(k.Value, 0) {
		k.Value = 0
	}
	s.KPIs = append(s.KPIs, k)
}

// Note records a caveat.
func (s *KPISet) Note(text string) {
	s.Notes = append(s.Notes, text)
}

// Get looks a KPI up by key.
func (s *KPISet) Get(key string) (KPI, bool) {
	for _, k := range s.KPIs {
		if k.Key == key {
			return k, true
		}
	}
	return KPI{}, false
}

// Headlines returns the summary-card set in display order.
func (s *KPISet) Headlines() []KPI {
	out := make([]KPI, 0, 6)
	for _, k := range s.KPIs {
		if k.Headline {
			out = append(out, k)
		}
	}
	return out
}

// Distribution summarises a set of samples. A mean alone hides the tail, and
// in a queueing system the tail is what people complain about.
type Distribution struct {
	Count  int     `json:"count"`
	Mean   float64 `json:"mean"`
	Min    float64 `json:"min"`
	P50    float64 `json:"p50"`
	P90    float64 `json:"p90"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	Max    float64 `json:"max"`
	StdDev float64 `json:"stdDev"`
}

// Summarise computes a distribution. It sorts a copy, leaving the caller's
// slice alone.
func Summarise(values []float64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}

	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)

	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	mean := sum / float64(len(sorted))

	variance := 0.0
	for _, v := range sorted {
		d := v - mean
		variance += d * d
	}
	variance /= float64(len(sorted))

	return Distribution{
		Count:  len(sorted),
		Mean:   mean,
		Min:    sorted[0],
		P50:    quantile(sorted, 0.50),
		P90:    quantile(sorted, 0.90),
		P95:    quantile(sorted, 0.95),
		P99:    quantile(sorted, 0.99),
		Max:    sorted[len(sorted)-1],
		StdDev: math.Sqrt(variance),
	}
}

// quantile uses the nearest-rank method on an already sorted slice.
func quantile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p*float64(len(sorted)-1) + 0.5)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// DomainLabels renames the generic KPIs for a domain, so a terminal operator
// reads "truck turnaround time" rather than "time in system".
//
// Only the labels change. The keys stay generic, which is what lets the
// comparison and report code work for every domain without knowing which one
// it is looking at.
func DomainLabels(domain string) map[string]string {
	switch domain {
	case "terminal":
		return map[string]string{
			"system_time.mean": "Mean truck turnaround",
			"system_time.p95":  "95th percentile turnaround",
			"throughput":       "Trucks processed per hour",
			"completed":        "Trucks processed",
			"created":          "Trucks arrived",
			"wip.mean":         "Trucks on site, average",
			"wip.peak":         "Trucks on site, peak",
		}
	case "warehouse":
		return map[string]string{
			"system_time.mean": "Mean order cycle time",
			"system_time.p95":  "95th percentile cycle time",
			"throughput":       "Orders shipped per hour",
			"completed":        "Orders shipped",
			"created":          "Orders released",
			"wip.mean":         "Open orders, average",
			"wip.peak":         "Open orders, peak",
		}
	}
	return nil
}

// ApplyDomainLabels rewrites labels in place for a domain.
func (s *KPISet) ApplyDomainLabels(domain string) {
	labels := DomainLabels(domain)
	if labels == nil {
		return
	}
	for i := range s.KPIs {
		if label, ok := labels[s.KPIs[i].Key]; ok {
			s.KPIs[i].Label = label
		}
	}
}
