package api

import (
	"encoding/json"
	"math"
	"net/http"

	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

func (s *Server) handleListSitePlans(w http.ResponseWriter, r *http.Request) error {
	plans, err := s.store.SitePlans.List(r.Context(), r.PathValue("projectID"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, plans)
	return nil
}

// createSitePlanRequest turns an uploaded image into a georeferenced plan. Only
// the asset is required: a plan starts at one metre per pixel and is calibrated
// afterwards.
type createSitePlanRequest struct {
	AssetID string `json:"assetId"`
	Name    string `json:"name"`
}

func (s *Server) handleCreateSitePlan(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()

	var req createSitePlanRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.AssetID == "" {
		return invalidFields(map[string]string{"assetId": "Choose an uploaded plan image."})
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
	if asset.Kind != model.AssetPlanImage {
		return invalidFields(map[string]string{"assetId": "That file is not a plan image."})
	}

	width, height := imageDimensions(asset.Meta)
	if width <= 0 || height <= 0 {
		return invalidFields(map[string]string{"assetId": "That image has no readable dimensions."})
	}

	name := trimTo(req.Name, 160)
	if name == "" {
		name = asset.OriginalName
	}

	plan := &model.SitePlan{
		ProjectID:   projectID,
		AssetID:     asset.ID,
		Name:        name,
		ImageWidth:  width,
		ImageHeight: height,
		// Identity transform until someone calibrates: one pixel is one metre,
		// the origin is the top-left corner, and image Y is flipped so world Y
		// grows upward the way the 3D scene expects.
		MetersPerPixel: 1,
		FlipY:          true,
	}
	if err := s.store.SitePlans.Create(ctx, plan); err != nil {
		return err
	}

	s.audit(ctx, store.Entry{
		UserID: auth.UserID(ctx), ProjectID: projectID, Action: store.ActionSitePlanCreate,
		TargetKind: "site_plan", TargetID: plan.ID, IP: s.clientIP(r),
	})

	writeJSON(w, http.StatusCreated, plan)
	return nil
}

func (s *Server) handleGetSitePlan(w http.ResponseWriter, r *http.Request) error {
	plan, err := s.sitePlanInProject(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, plan)
	return nil
}

// updateSitePlanRequest carries the georeference. The UI writes these values
// from the draw-to-calibrate tool and then lets the user edit them directly,
// so both paths use the same endpoint.
type updateSitePlanRequest struct {
	Name           string          `json:"name"`
	MetersPerPixel float64         `json:"metersPerPixel"`
	OriginPxX      float64         `json:"originPxX"`
	OriginPxY      float64         `json:"originPxY"`
	RotationDeg    float64         `json:"rotationDeg"`
	FlipY          bool            `json:"flipY"`
	Calibration    json.RawMessage `json:"calibration"`
}

func (s *Server) handleUpdateSitePlan(w http.ResponseWriter, r *http.Request) error {
	plan, err := s.sitePlanInProject(r)
	if err != nil {
		return err
	}
	ctx := r.Context()

	var req updateSitePlanRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	fields := map[string]string{}
	if req.MetersPerPixel <= 0 || math.IsInf(req.MetersPerPixel, 0) || math.IsNaN(req.MetersPerPixel) {
		fields["metersPerPixel"] = "Must be a positive number."
	}
	if !isFinite(req.OriginPxX) || !isFinite(req.OriginPxY) {
		fields["originPxX"] = "Must be a number."
	}
	if !isFinite(req.RotationDeg) || req.RotationDeg < -360 || req.RotationDeg > 360 {
		fields["rotationDeg"] = "Must be between -360 and 360."
	}
	if len(fields) > 0 {
		return invalidFields(fields)
	}

	if name := trimTo(req.Name, 160); name != "" {
		plan.Name = name
	}
	plan.MetersPerPixel = req.MetersPerPixel
	plan.OriginPxX = req.OriginPxX
	plan.OriginPxY = req.OriginPxY
	plan.RotationDeg = req.RotationDeg
	plan.FlipY = req.FlipY
	plan.Calibration = req.Calibration

	if err := s.store.SitePlans.UpdateCalibration(ctx, plan); err != nil {
		return err
	}

	s.audit(ctx, store.Entry{
		UserID: auth.UserID(ctx), ProjectID: plan.ProjectID, Action: store.ActionSitePlanCalibrate,
		TargetKind: "site_plan", TargetID: plan.ID,
		Detail: map[string]float64{"metersPerPixel": plan.MetersPerPixel, "rotationDeg": plan.RotationDeg},
		IP:     s.clientIP(r),
	})

	writeJSON(w, http.StatusOK, plan)
	return nil
}

func (s *Server) handleDeleteSitePlan(w http.ResponseWriter, r *http.Request) error {
	plan, err := s.sitePlanInProject(r)
	if err != nil {
		return err
	}

	if err := s.store.SitePlans.Delete(r.Context(), plan.ID); err != nil {
		return err
	}

	s.audit(r.Context(), store.Entry{
		UserID: auth.UserID(r.Context()), ProjectID: plan.ProjectID, Action: store.ActionSitePlanDelete,
		TargetKind: "site_plan", TargetID: plan.ID, IP: s.clientIP(r),
	})

	writeNoContent(w)
	return nil
}

func (s *Server) sitePlanInProject(r *http.Request) (*model.SitePlan, error) {
	plan, err := s.store.SitePlans.ByID(r.Context(), r.PathValue("planID"))
	if err != nil {
		if isNotFound(err) {
			return nil, notFound("That plan")
		}
		return nil, err
	}
	if plan.ProjectID != r.PathValue("projectID") {
		return nil, notFound("That plan")
	}
	return plan, nil
}

// imageDimensions reads the width and height describeAsset recorded at upload.
func imageDimensions(meta json.RawMessage) (int, int) {
	if len(meta) == 0 {
		return 0, 0
	}
	var m struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	if err := json.Unmarshal(meta, &m); err != nil {
		return 0, 0
	}
	return m.Width, m.Height
}

func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
