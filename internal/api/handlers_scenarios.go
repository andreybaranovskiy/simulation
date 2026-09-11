package api

import (
	"encoding/json"
	"math"
	"net/http"

	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/engine/templates"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

func (s *Server) handleListScenarios(w http.ResponseWriter, r *http.Request) error {
	includeArchived := queryBool(r.URL.Query().Get("archived"))

	scenarios, err := s.store.Scenarios.List(r.Context(), r.PathValue("projectID"), includeArchived)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, scenarios)
	return nil
}

type scenarioRequest struct {
	ModelID      string             `json:"modelId"`
	Name         string             `json:"name"`
	Description  string             `json:"description"`
	Params       map[string]float64 `json:"params"`
	Seed         *uint64            `json:"seed"`
	Replications int                `json:"replications"`
	SitePlanID   string             `json:"sitePlanId"`
	SortOrder    int                `json:"sortOrder"`
}

func (s *Server) handleCreateScenario(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()

	var req scenarioRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	name := trimTo(req.Name, 160)
	if name == "" {
		return invalidFields(map[string]string{"name": "A scenario name is required."})
	}
	if req.ModelID == "" {
		return invalidFields(map[string]string{"modelId": "Choose a model."})
	}

	m, err := s.store.Models.ByID(ctx, req.ModelID)
	if err != nil {
		if isNotFound(err) {
			return invalidFields(map[string]string{"modelId": "No such model."})
		}
		return err
	}
	if m.ProjectID != projectID {
		return invalidFields(map[string]string{"modelId": "That model belongs to another project."})
	}

	params, err := s.validateParams(m, req.Params)
	if err != nil {
		return err
	}

	sc := &model.Scenario{
		ProjectID:    projectID,
		ModelID:      m.ID,
		Name:         name,
		Description:  trimTo(req.Description, 4000),
		Params:       params,
		Seed:         req.Seed,
		Replications: clampReplications(req.Replications),
		SortOrder:    req.SortOrder,
		CreatedBy:    auth.UserID(ctx),
	}
	if req.SitePlanID != "" {
		sc.SitePlanID = &req.SitePlanID
	}

	if err := s.store.Scenarios.Create(ctx, sc); err != nil {
		return err
	}

	s.audit(ctx, store.Entry{
		UserID: sc.CreatedBy, ProjectID: projectID, Action: "scenario.create",
		TargetKind: "scenario", TargetID: sc.ID,
		Detail: map[string]string{"name": sc.Name}, IP: s.clientIP(r),
	})

	writeJSON(w, http.StatusCreated, sc)
	return nil
}

// validateParams checks a scenario's values against the model's parameters, so
// a bad value is rejected at the moment it is set rather than by a failed run
// minutes later.
func (s *Server) validateParams(m *model.SimModel, params map[string]float64) (json.RawMessage, error) {
	if params == nil {
		params = map[string]float64{}
	}

	fields := map[string]string{}
	for key, value := range params {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			fields[key] = "Must be a finite number."
		}
	}
	if len(fields) > 0 {
		return nil, invalidFields(fields)
	}

	if m.Source == model.SourceTemplate {
		tpl, ok := templates.Get(m.TemplateKey)
		if !ok {
			return nil, badRequest("The model uses template %q, which this build does not have.", m.TemplateKey)
		}
		// Building the model is the real check: it applies every bound the
		// template declares and rejects unknown keys.
		if _, err := tpl.Build(params); err != nil {
			return nil, invalidFields(map[string]string{"params": err.Error()})
		}
	}

	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, internal(err)
	}
	return encoded, nil
}

func (s *Server) handleGetScenario(w http.ResponseWriter, r *http.Request) error {
	sc, err := s.scenarioInProject(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, sc)
	return nil
}

func (s *Server) handleUpdateScenario(w http.ResponseWriter, r *http.Request) error {
	sc, err := s.scenarioInProject(r)
	if err != nil {
		return err
	}
	ctx := r.Context()

	var req scenarioRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	if name := trimTo(req.Name, 160); name != "" {
		sc.Name = name
	}
	sc.Description = trimTo(req.Description, 4000)
	sc.Replications = clampReplications(req.Replications)
	sc.Seed = req.Seed
	sc.SortOrder = req.SortOrder

	if req.SitePlanID != "" {
		sc.SitePlanID = &req.SitePlanID
	} else {
		sc.SitePlanID = nil
	}

	if req.Params != nil {
		m, err := s.store.Models.ByID(ctx, sc.ModelID)
		if err != nil {
			return err
		}
		params, err := s.validateParams(m, req.Params)
		if err != nil {
			return err
		}
		sc.Params = params
	}

	if err := s.store.Scenarios.Update(ctx, sc); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, sc)
	return nil
}

// handleDuplicateScenario copies a scenario. This is how a comparison set gets
// built: start from one that works and change a single parameter.
func (s *Server) handleDuplicateScenario(w http.ResponseWriter, r *http.Request) error {
	sc, err := s.scenarioInProject(r)
	if err != nil {
		return err
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	name := trimTo(req.Name, 160)
	if name == "" {
		name = sc.Name + " (copy)"
	}

	copied, err := s.store.Scenarios.Duplicate(r.Context(), sc.ID, name, auth.UserID(r.Context()))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, copied)
	return nil
}

func (s *Server) handleArchiveScenario(w http.ResponseWriter, r *http.Request) error {
	sc, err := s.scenarioInProject(r)
	if err != nil {
		return err
	}
	if err := s.store.Scenarios.SetArchived(r.Context(), sc.ID, true); err != nil {
		return err
	}
	writeNoContent(w)
	return nil
}

func (s *Server) handleDeleteScenario(w http.ResponseWriter, r *http.Request) error {
	sc, err := s.scenarioInProject(r)
	if err != nil {
		return err
	}
	ctx := r.Context()

	// A scenario's runs cascade in the database, but their artifact
	// directories are on disk and would be orphaned without this.
	runs, err := s.store.Runs.ListForScenario(ctx, sc.ID)
	if err != nil {
		return err
	}

	if err := s.store.Scenarios.Delete(ctx, sc.ID); err != nil {
		return err
	}

	for _, run := range runs {
		s.removeRunArtifacts(&run)
	}

	s.audit(ctx, store.Entry{
		UserID: auth.UserID(ctx), ProjectID: sc.ProjectID, Action: "scenario.delete",
		TargetKind: "scenario", TargetID: sc.ID,
		Detail: map[string]int{"runsRemoved": len(runs)}, IP: s.clientIP(r),
	})

	writeNoContent(w)
	return nil
}

func (s *Server) scenarioInProject(r *http.Request) (*model.Scenario, error) {
	sc, err := s.store.Scenarios.ByID(r.Context(), r.PathValue("scenarioID"))
	if err != nil {
		if isNotFound(err) {
			return nil, notFound("That scenario")
		}
		return nil, err
	}
	if sc.ProjectID != r.PathValue("projectID") {
		return nil, notFound("That scenario")
	}
	return sc, nil
}

// clampReplications keeps the count sane. Replications multiply the work, so
// an accidental extra digit would tie up every worker for hours.
func clampReplications(n int) int {
	if n < 1 {
		return 1
	}
	if n > 50 {
		return 50
	}
	return n
}
