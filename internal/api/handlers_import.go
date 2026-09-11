package api

import (
	"net/http"

	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

type importAnimationRequest struct {
	AssetID string `json:"assetId"`
	Name    string `json:"name"`
}

// handleImportAnimation turns an uploaded animation file into something
// viewable.
//
// It creates the model, a scenario and a run in one step. A pre-computed
// animation has no parameters to vary, so making the user assemble those three
// things by hand would be ceremony with no choices in it.
func (s *Server) handleImportAnimation(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()

	if s.dispatcher == nil {
		return &APIError{
			Status: http.StatusServiceUnavailable, Code: "engine_unavailable",
			Message: "The import service is not available on this server.",
		}
	}

	var req importAnimationRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.AssetID == "" {
		return invalidFields(map[string]string{"assetId": "Choose an uploaded animation file."})
	}

	asset, err := s.store.Assets.ByID(ctx, req.AssetID)
	if err != nil {
		if isNotFound(err) {
			return invalidFields(map[string]string{"assetId": "No such file."})
		}
		return err
	}
	if asset.ProjectID != projectID {
		return invalidFields(map[string]string{"assetId": "That file belongs to another project."})
	}
	if asset.Kind != model.AssetAnimationJSON {
		return invalidFields(map[string]string{
			"assetId": "That file was not uploaded as an animation.",
		})
	}

	name := trimTo(req.Name, 160)
	if name == "" {
		name = asset.OriginalName
	}

	userID := auth.UserID(ctx)

	m := &model.SimModel{
		ProjectID:   projectID,
		Name:        name,
		Description: "Imported from " + asset.OriginalName,
		Source:      model.SourceAnimation,
		Domain:      "generic",
		AssetID:     &asset.ID,
		CreatedBy:   userID,
	}
	if err := s.store.Models.Create(ctx, m); err != nil {
		return err
	}

	scenario := &model.Scenario{
		ProjectID:    projectID,
		ModelID:      m.ID,
		Name:         name,
		Description:  "Playback of an imported animation.",
		Params:       []byte("{}"),
		Replications: 1,
		CreatedBy:    userID,
	}
	if err := s.store.Scenarios.Create(ctx, scenario); err != nil {
		return err
	}

	file, err := s.blobs.Open(asset.StoragePath)
	if err != nil {
		return internal(err)
	}
	defer file.Close()

	// The conversion runs inline rather than in the background. It is bounded
	// by the file's size and takes about half a second for a 28 MB export, so
	// the caller gets the finished run rather than something to poll.
	run, err := s.dispatcher.ImportAnimation(ctx, scenario, file, userID)
	if err != nil {
		return &APIError{
			Status: http.StatusUnprocessableEntity, Code: "import_failed",
			Message: "That file could not be imported: " + err.Error(),
			Cause:   err,
		}
	}

	s.audit(ctx, store.Entry{
		UserID: userID, ProjectID: projectID, Action: "animation.import",
		TargetKind: "run", TargetID: run.ID,
		Detail: map[string]any{"asset": asset.OriginalName, "entities": run.EntityCount},
		IP:     s.clientIP(r),
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"model":    m,
		"scenario": scenario,
		"run":      run,
	})
	return nil
}
