package model

import "time"

// ReportKind is what a report is built from.
type ReportKind string

const (
	// ReportScenario is a single scenario: its parameters, its KPIs, and the
	// charts drawn from one representative run.
	ReportScenario ReportKind = "scenario"
	// ReportComparison sets several scenarios side by side, and is the same
	// analysis the comparison page shows, laid out for paper.
	ReportComparison ReportKind = "comparison"
)

// Report is a saved report definition. It records what to put in a report, not
// a rendered PDF: the PDF is produced on demand from the current run data, so a
// saved report always reflects the latest results rather than a frozen copy.
type Report struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"projectId"`
	Name      string     `json:"name"`
	Subtitle  string     `json:"subtitle"`
	Kind      ReportKind `json:"kind"`

	// ScenarioIDs are the scenarios the report covers, in the order they should
	// appear. A scenario report has exactly one; a comparison has two or more.
	ScenarioIDs []string `json:"scenarioIds"`

	// Sections selects which parts to include, so a report can be trimmed to
	// what matters without editing anything else. An empty set means every
	// section the kind supports.
	Sections []string `json:"sections"`

	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// ScenarioNames is filled in on read for the list view, so it does not have
	// to fetch each scenario to show what a report is about. It is not stored.
	ScenarioNames []string `json:"scenarioNames,omitempty"`
}

// ReportSection names one block of a report. The print page reads these to
// decide what to draw, and the builder shows them as toggles.
const (
	SectionSummary      = "summary"      // headline KPIs and the scenario's parameters
	SectionKPIs         = "kpis"         // the full measurement table
	SectionThroughput   = "throughput"   // arrivals and completions over time
	SectionUtilisation  = "utilisation"  // resource Gantt
	SectionHeatmap      = "heatmap"      // density over the plan
	SectionDistribution = "distribution" // replication spread with confidence
	SectionDifference   = "difference"   // where two scenarios diverge on the plan
)

// DefaultSections returns every section a report kind supports, in print order.
func DefaultSections(kind ReportKind) []string {
	switch kind {
	case ReportComparison:
		return []string{
			SectionSummary, SectionDistribution, SectionKPIs,
			SectionThroughput, SectionDifference,
		}
	default:
		return []string{
			SectionSummary, SectionKPIs, SectionThroughput,
			SectionUtilisation, SectionHeatmap,
		}
	}
}
