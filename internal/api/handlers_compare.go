package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/andreybaranovskiy/simulation/internal/analytics"
	"github.com/andreybaranovskiy/simulation/internal/model"
)

// maxCompared bounds a comparison. Past a handful of columns a delta table
// stops being readable, and the chart forms behind it cap lower still.
const maxCompared = 6

// ComparisonResponse is what the comparison view reads.
type ComparisonResponse struct {
	Scenarios []ComparedScenario `json:"scenarios"`
	/** BaselineID is the scenario every difference is measured against. */
	BaselineID string          `json:"baselineId"`
	KPIs       []ComparedKPI   `json:"kpis"`
	Note       string          `json:"note"`
	Warnings   []string        `json:"warnings,omitempty"`
	Params     []ComparedParam `json:"params"`
}

// ComparedScenario is one column.
type ComparedScenario struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	RunIDs []string `json:"runIds"`
	// Replications is how many finished runs went into the statistics, which
	// is not always what the scenario asked for.
	Replications int `json:"replications"`
	// SampleRunID is a run to open for playback or a heatmap.
	SampleRunID string             `json:"sampleRunId"`
	Params      map[string]float64 `json:"params"`
}

// ComparedParam is a parameter that differs between the scenarios, which is
// what the comparison is actually about.
type ComparedParam struct {
	ID     string             `json:"id"`
	Values map[string]float64 `json:"values"`
	// Differs is false for parameters every scenario shares. They are listed
	// anyway so a reader can confirm what was held constant.
	Differs bool `json:"differs"`
}

// ComparedKPI is one measure across every scenario.
type ComparedKPI struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Unit     string `json:"unit"`
	Group    string `json:"group"`
	Better   string `json:"better,omitempty"`
	Decimals int    `json:"decimals"`
	Headline bool   `json:"headline"`

	Samples     map[string]analytics.Sample     `json:"samples"`
	Differences map[string]analytics.Difference `json:"differences"`
}

// handleCompare aggregates several scenarios' runs into one table.
//
// Scenarios rather than runs, because a scenario's replications are the same
// experiment repeated and the spread between them is exactly what says whether
// a difference between scenarios means anything.
func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()

	ids := splitIDs(r.URL.Query().Get("scenarios"))
	if len(ids) < 2 {
		return badRequest("Choose at least two scenarios to compare.")
	}
	if len(ids) > maxCompared {
		return badRequest("Compare at most %d scenarios at once.", maxCompared)
	}

	response := ComparisonResponse{
		Scenarios: []ComparedScenario{},
		KPIs:      []ComparedKPI{},
		Params:    []ComparedParam{},
	}

	var all []collectedScenario
	minReplications := 0

	for _, id := range ids {
		scenario, err := s.store.Scenarios.ByID(ctx, id)
		if err != nil {
			if isNotFound(err) {
				return notFound("One of those scenarios")
			}
			return err
		}
		if scenario.ProjectID != projectID {
			return notFound("One of those scenarios")
		}

		runs, err := s.store.Runs.ListForScenario(ctx, scenario.ID)
		if err != nil {
			return err
		}

		// Only finished runs carry results. A failed or cancelled run in the
		// set would otherwise quietly drag a mean towards whatever partial
		// numbers it managed to write.
		var runIDs []string
		for _, run := range runs {
			if run.Status == model.RunDone {
				runIDs = append(runIDs, run.ID)
			}
		}

		if len(runIDs) == 0 {
			response.Warnings = append(response.Warnings,
				scenario.Name+" has no finished runs, so it is not in the comparison.")
			continue
		}

		rows, err := s.store.Runs.KPIsForRuns(ctx, runIDs)
		if err != nil {
			return err
		}

		item := collectedScenario{
			scenario: scenario,
			kpis:     make(map[string][]float64),
			meta:     make(map[string]model.RunKPI),
		}
		for _, row := range rows {
			item.kpis[row.Key] = append(item.kpis[row.Key], row.Value)
			item.meta[row.Key] = row
		}

		all = append(all, item)

		if minReplications == 0 || len(runIDs) < minReplications {
			minReplications = len(runIDs)
		}

		params := map[string]float64{}
		if len(scenario.Params) > 0 {
			_ = json.Unmarshal(scenario.Params, &params)
		}

		response.Scenarios = append(response.Scenarios, ComparedScenario{
			ID:           scenario.ID,
			Name:         scenario.Name,
			RunIDs:       runIDs,
			Replications: len(runIDs),
			SampleRunID:  runIDs[0],
			Params:       params,
		})
	}

	if len(all) < 2 {
		return badRequest("At least two of the chosen scenarios need a finished run.")
	}

	// The first scenario is the baseline. Making it the first rather than the
	// best keeps the table stable as results change: a baseline that moved
	// would silently rewrite every other column.
	response.BaselineID = all[0].scenario.ID
	response.Note = analytics.ComparisonNote(minReplications)

	response.KPIs = buildComparedKPIs(all)
	response.Params = buildComparedParams(response.Scenarios)

	writeJSON(w, http.StatusOK, response)
	return nil
}

// collectedScenario holds one scenario's replications before they are
// transposed into one row per measure, which is the shape a delta table reads
// in.
type collectedScenario struct {
	scenario *model.Scenario
	// kpis holds every replication's value for each measure.
	kpis map[string][]float64
	// meta keeps one row per measure for its label, unit and direction, which
	// are the same across replications.
	meta map[string]model.RunKPI
}

func buildComparedKPIs(all []collectedScenario) []ComparedKPI {
	// Only measures every scenario reports are comparable. One that appears in
	// half the columns would produce a table with holes in it and deltas
	// against nothing.
	shared := map[string]int{}
	for _, item := range all {
		for key := range item.kpis {
			shared[key]++
		}
	}

	var keys []string
	for key, count := range shared {
		if count == len(all) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	baseline := all[0]
	out := make([]ComparedKPI, 0, len(keys))

	for _, key := range keys {
		meta := baseline.meta[key]

		kpi := ComparedKPI{
			Key:         key,
			Label:       meta.Label,
			Unit:        meta.Unit,
			Group:       meta.Group,
			Better:      meta.Better,
			Decimals:    meta.Decimals,
			Headline:    meta.Headline,
			Samples:     make(map[string]analytics.Sample, len(all)),
			Differences: make(map[string]analytics.Difference, len(all)-1),
		}

		baselineSample := analytics.NewSample(baseline.kpis[key])

		for i, item := range all {
			sample := analytics.NewSample(item.kpis[key])
			kpi.Samples[item.scenario.ID] = sample

			if i > 0 {
				kpi.Differences[item.scenario.ID] = analytics.Compare(baselineSample, sample, meta.Better)
			}
		}

		out = append(out, kpi)
	}

	return out
}

// buildComparedParams lists what the scenarios were configured with, marking
// the ones that actually differ. That list is the comparison's premise: if
// nothing differs, any gap in the results is noise by construction.
func buildComparedParams(scenarios []ComparedScenario) []ComparedParam {
	seen := map[string]bool{}
	for _, scenario := range scenarios {
		for key := range scenario.Params {
			seen[key] = true
		}
	}

	var keys []string
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]ComparedParam, 0, len(keys))

	for _, key := range keys {
		param := ComparedParam{ID: key, Values: map[string]float64{}}

		var first float64
		var haveFirst bool

		for _, scenario := range scenarios {
			value := scenario.Params[key]
			param.Values[scenario.ID] = value

			if !haveFirst {
				first, haveFirst = value, true
			} else if value != first {
				param.Differs = true
			}
		}

		out = append(out, param)
	}

	// The parameters that differ come first: they are what the comparison is
	// about, and the rest are there to confirm what was held constant.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Differs && !out[j].Differs
	})

	return out
}

func splitIDs(raw string) []string {
	var out []string
	seen := map[string]bool{}

	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
