package model

import (
	"encoding/json"
	"strings"
	"time"
)

// ModelSource says how a simulation model is defined. It decides what the
// server does to turn the model into something runnable.
type ModelSource string

const (
	// SourceTemplate is parameters over a built-in generator.
	SourceTemplate ModelSource = "template"
	// SourceSpec is a declarative model, authored in the editor or uploaded.
	SourceSpec ModelSource = "spec"
	// SourceGo is user-supplied Go compiled on the server. Admin-gated.
	SourceGo ModelSource = "go_source"
	// SourceAnimation is a pre-computed playback with no engine involved,
	// which is how the original viewer's JSON format is imported.
	SourceAnimation ModelSource = "animation"
)

func (s ModelSource) Valid() bool {
	switch s {
	case SourceTemplate, SourceSpec, SourceGo, SourceAnimation:
		return true
	}
	return false
}

// Runnable reports whether the source produces a simulation. An imported
// animation is played back, not run, so it has no scenarios to vary.
func (s ModelSource) Runnable() bool {
	return s == SourceTemplate || s == SourceSpec || s == SourceGo
}

func ParseModelSource(s string) (ModelSource, bool) {
	v := ModelSource(strings.ToLower(strings.TrimSpace(s)))
	return v, v.Valid()
}

// SimModel is a simulation model in a project.
type SimModel struct {
	ID          string      `json:"id"`
	ProjectID   string      `json:"projectId"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Source      ModelSource `json:"source"`
	TemplateKey string      `json:"templateKey,omitempty"`
	Domain      string      `json:"domain"`

	// Spec is the resolved declarative model, present only for spec models.
	// A template model leaves it empty on purpose: the generator plus the
	// scenario's parameters reproduce it exactly, and a stored copy would be
	// free to drift away from the generator.
	Spec json.RawMessage `json:"spec,omitempty"`

	AssetID   *string   `json:"assetId,omitempty"`
	Version   int       `json:"version"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// ScenarioCount is filled in by list queries so the UI can show it
	// without a second round trip.
	ScenarioCount int `json:"scenarioCount,omitempty"`
}

// Scenario is a model plus the parameter values that make one case of it.
type Scenario struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId"`
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description"`

	Params json.RawMessage `json:"params"`

	// Seed fixes the random stream. Nil draws one at run time, which makes the
	// run unreproducible unless the seed it used is read back from the run.
	Seed         *uint64 `json:"seed,omitempty"`
	Replications int     `json:"replications"`

	SitePlanID *string `json:"sitePlanId,omitempty"`

	SortOrder  int        `json:"sortOrder"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
	CreatedBy  string     `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`

	// ModelName and LatestRun are filled in by list queries.
	ModelName string `json:"modelName,omitempty"`
	RunCount  int    `json:"runCount,omitempty"`
	LatestRun *Run   `json:"latestRun,omitempty"`
}

// RunStatus is where a run is in its life.
type RunStatus string

const (
	RunQueued RunStatus = "queued"
	RunActive RunStatus = "running"
	// RunBuilding is turning the trace into playback chunks and aggregates. It
	// is a separate state because on a long run it takes real time, and a user
	// watching deserves to know which half of the work is happening.
	RunBuilding RunStatus = "building"
	RunDone     RunStatus = "done"
	RunFailed   RunStatus = "failed"
	RunCanceled RunStatus = "canceled"
)

func (s RunStatus) Valid() bool {
	switch s {
	case RunQueued, RunActive, RunBuilding, RunDone, RunFailed, RunCanceled:
		return true
	}
	return false
}

// Terminal reports whether a run has finished, one way or another.
func (s RunStatus) Terminal() bool {
	return s == RunDone || s == RunFailed || s == RunCanceled
}

// InFlight reports whether a run is occupying a worker slot.
func (s RunStatus) InFlight() bool {
	return s == RunActive || s == RunBuilding
}

// Run is one execution of a scenario.
type Run struct {
	ID         string `json:"id"`
	ProjectID  string `json:"projectId"`
	ScenarioID string `json:"scenarioId"`

	Replication int       `json:"replication"`
	Seed        uint64    `json:"seed"`
	Status      RunStatus `json:"status"`

	QueuedAt   time.Time  `json:"queuedAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`

	Progress    float64 `json:"progress"`
	SimTime     float64 `json:"simTime"`
	EntityCount uint64  `json:"entityCount"`
	RecordCount uint64  `json:"recordCount"`

	DurationMS    int64  `json:"durationMs"`
	ArtifactBytes int64  `json:"artifactBytes"`
	EngineVersion string `json:"engineVersion,omitempty"`
	ArtifactDir   string `json:"-"`

	Error    string          `json:"error,omitempty"`
	Warnings json.RawMessage `json:"warnings,omitempty"`

	CreatedBy string `json:"createdBy"`

	// ScenarioName is filled in by list queries.
	ScenarioName string `json:"scenarioName,omitempty"`
}

// Viewable reports whether a run has artifacts to open. A failed run may still
// have partial output, but nothing that should be presented as a result.
func (r Run) Viewable() bool {
	return r.Status == RunDone && r.ArtifactDir != ""
}

// RunKPI is one headline number, mirrored out of a run's aggregate file so
// comparing many runs is a single indexed query.
type RunKPI struct {
	RunID      string  `json:"runId"`
	Key        string  `json:"key"`
	Value      float64 `json:"value"`
	Label      string  `json:"label"`
	Unit       string  `json:"unit"`
	Group      string  `json:"group"`
	Better     string  `json:"better,omitempty"`
	Decimals   int     `json:"decimals"`
	Headline   bool    `json:"headline"`
	ResourceID string  `json:"resourceId,omitempty"`
}
