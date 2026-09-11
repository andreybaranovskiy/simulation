package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
	"github.com/andreybaranovskiy/simulation/internal/engine/templates"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

// handleListTemplates describes the built-in models. It is not project-scoped
// because the template set is a property of the build, and the model editor
// needs it before a project exists.
func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, templates.All())
	return nil
}

func (s *Server) handleGetTemplate(w http.ResponseWriter, r *http.Request) error {
	tpl, ok := templates.Get(r.PathValue("templateKey"))
	if !ok {
		return notFound("That template")
	}
	writeJSON(w, http.StatusOK, tpl)
	return nil
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) error {
	models, err := s.store.Models.List(r.Context(), r.PathValue("projectID"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, models)
	return nil
}

type createModelRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Source      string          `json:"source"`
	TemplateKey string          `json:"templateKey"`
	Spec        json.RawMessage `json:"spec"`
	AssetID     string          `json:"assetId"`
}

func (s *Server) handleCreateModel(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()

	var req createModelRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	source, ok := model.ParseModelSource(req.Source)
	if !ok {
		return invalidFields(map[string]string{
			"source": "Use template, spec, go_source or animation.",
		})
	}

	m := &model.SimModel{
		ProjectID:   projectID,
		Name:        trimTo(req.Name, 160),
		Description: trimTo(req.Description, 4000),
		Source:      source,
		CreatedBy:   auth.UserID(ctx),
	}

	switch source {
	case model.SourceTemplate:
		tpl, ok := templates.Get(req.TemplateKey)
		if !ok {
			return invalidFields(map[string]string{"templateKey": "No template with that key."})
		}
		m.TemplateKey = tpl.Key
		m.Domain = string(tpl.Domain)
		if m.Name == "" {
			m.Name = tpl.Name
		}
		if m.Description == "" {
			m.Description = tpl.Description
		}

	case model.SourceSpec:
		// A spec is validated before it is stored. Saving a model that cannot
		// run would move the failure to the moment someone starts a run, which
		// is the worst time to discover it.
		parsed, err := s.loadSpecFor(ctx, projectID, req)
		if err != nil {
			return err
		}
		encoded, err := parsed.Marshal()
		if err != nil {
			return internal(err)
		}
		m.Spec = encoded
		m.Domain = string(parsed.Domain)
		if m.Name == "" {
			m.Name = parsed.Name
		}

	case model.SourceGo:
		if err := s.checkGoUploadAllowed(ctx); err != nil {
			return err
		}
		if req.AssetID == "" {
			return invalidFields(map[string]string{"assetId": "Choose an uploaded Go model."})
		}
		m.AssetID = &req.AssetID
		m.Domain = "generic"

	case model.SourceAnimation:
		if req.AssetID == "" {
			return invalidFields(map[string]string{"assetId": "Choose an uploaded animation file."})
		}
		m.AssetID = &req.AssetID
		m.Domain = "generic"
	}

	if m.Name == "" {
		return invalidFields(map[string]string{"name": "A model name is required."})
	}

	if err := s.store.Models.Create(ctx, m); err != nil {
		return err
	}

	s.audit(ctx, store.Entry{
		UserID: m.CreatedBy, ProjectID: projectID, Action: "model.create",
		TargetKind: "model", TargetID: m.ID,
		Detail: map[string]string{"name": m.Name, "source": string(m.Source)},
		IP:     s.clientIP(r),
	})

	writeJSON(w, http.StatusCreated, m)
	return nil
}

// loadSpecFor resolves a spec model's definition, which may arrive inline or
// as a previously uploaded asset.
func (s *Server) loadSpecFor(ctx context.Context, projectID string, req createModelRequest) (*spec.Model, error) {
	if len(req.Spec) > 0 {
		parsed, err := spec.Parse(req.Spec)
		if err != nil {
			return nil, specError(err)
		}
		return parsed, nil
	}

	if req.AssetID == "" {
		return nil, invalidFields(map[string]string{
			"spec": "Give a model definition, or choose an uploaded one.",
		})
	}

	asset, err := s.store.Assets.ByID(ctx, req.AssetID)
	if err != nil {
		if isNotFound(err) {
			return nil, invalidFields(map[string]string{"assetId": "No such file."})
		}
		return nil, err
	}
	if asset.ProjectID != projectID {
		return nil, invalidFields(map[string]string{"assetId": "That file belongs to another project."})
	}

	file, err := s.blobs.Open(asset.StoragePath)
	if err != nil {
		return nil, internal(err)
	}
	defer file.Close()

	parsed, err := spec.ParseReader(file)
	if err != nil {
		return nil, specError(err)
	}
	return parsed, nil
}

func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request) error {
	m, err := s.modelInProject(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, m)
	return nil
}

type updateModelRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Spec        json.RawMessage `json:"spec"`
}

func (s *Server) handleUpdateModel(w http.ResponseWriter, r *http.Request) error {
	m, err := s.modelInProject(r)
	if err != nil {
		return err
	}
	ctx := r.Context()

	var req updateModelRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	if name := trimTo(req.Name, 160); name != "" {
		m.Name = name
	}
	m.Description = trimTo(req.Description, 4000)

	if len(req.Spec) > 0 {
		if m.Source != model.SourceSpec {
			return badRequest("Only a spec model has a definition to edit.")
		}
		parsed, err := spec.Parse(req.Spec)
		if err != nil {
			return specError(err)
		}
		encoded, err := parsed.Marshal()
		if err != nil {
			return internal(err)
		}
		m.Spec = encoded
		m.Domain = string(parsed.Domain)
	}

	if err := s.store.Models.Update(ctx, m); err != nil {
		return err
	}

	updated, err := s.store.Models.ByID(ctx, m.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, updated)
	return nil
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) error {
	m, err := s.modelInProject(r)
	if err != nil {
		return err
	}

	if err := s.store.Models.Delete(r.Context(), m.ID); err != nil {
		return err
	}

	s.audit(r.Context(), store.Entry{
		UserID: auth.UserID(r.Context()), ProjectID: m.ProjectID, Action: "model.delete",
		TargetKind: "model", TargetID: m.ID, IP: s.clientIP(r),
	})

	writeNoContent(w)
	return nil
}

// handleValidateModel checks a definition without storing it, so the editor
// can show problems as they are made rather than on save.
func (s *Server) handleValidateModel(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if len(req.Spec) == 0 {
		return invalidFields(map[string]string{"spec": "Give a model definition."})
	}

	var parsed spec.Model
	if err := json.Unmarshal(req.Spec, &parsed); err != nil {
		writeJSON(w, http.StatusOK, validationResponse{
			Valid: false,
			Problems: []spec.Problem{{
				Path: "", Message: "The definition is not valid JSON: " + err.Error(),
			}},
		})
		return nil
	}

	parsed.ApplyDefaults()
	problems := parsed.Validate()

	response := validationResponse{Valid: true, Problems: problems, Summary: parsed.Summary()}
	for _, p := range problems {
		if !p.Warning {
			response.Valid = false
			break
		}
	}

	writeJSON(w, http.StatusOK, response)
	return nil
}

type validationResponse struct {
	Valid    bool           `json:"valid"`
	Problems []spec.Problem `json:"problems"`
	Summary  spec.Summary   `json:"summary"`
}

func (s *Server) modelInProject(r *http.Request) (*model.SimModel, error) {
	m, err := s.store.Models.ByID(r.Context(), r.PathValue("modelID"))
	if err != nil {
		if isNotFound(err) {
			return nil, notFound("That model")
		}
		return nil, err
	}
	if m.ProjectID != r.PathValue("projectID") {
		return nil, notFound("That model")
	}
	return m, nil
}

// specError turns a validation failure into a response that lists every
// problem, so a user fixes a model in one pass.
func specError(err error) error {
	var ve *spec.ValidationError
	if errors.As(err, &ve) {
		return &APIError{
			Status: http.StatusUnprocessableEntity, Code: "invalid_model",
			Message: "The model has problems that would stop it running.",
			Fields:  problemFields(ve.Problems),
			Cause:   err,
		}
	}
	return invalidFields(map[string]string{"spec": err.Error()})
}

func problemFields(problems []spec.Problem) map[string]string {
	out := make(map[string]string, len(problems))
	for _, p := range problems {
		if p.Warning {
			continue
		}
		key := p.Path
		if key == "" {
			key = "model"
		}
		out[key] = p.Message
	}
	return out
}
