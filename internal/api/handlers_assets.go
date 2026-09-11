package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	// Registered for their decoders: image.DecodeConfig reads the dimensions
	// of an uploaded plan without loading the whole bitmap.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/blobstore"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

func (s *Server) handleListAssets(w http.ResponseWriter, r *http.Request) error {
	kind := model.AssetKind(strings.TrimSpace(r.URL.Query().Get("kind")))
	if kind != "" && !kind.Valid() {
		return badRequest("Unknown asset kind %q.", kind)
	}

	assets, err := s.store.Assets.List(r.Context(), r.PathValue("projectID"), kind)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, assets)
	return nil
}

// handleUploadAsset accepts a raw body rather than multipart form data. A
// multipart parse would buffer the whole file before the handler sees it,
// which is the wrong shape for hundred-megabyte models; streaming the body
// straight into the content-addressed store avoids that entirely.
//
// Metadata travels in the query string and the headers:
//
//	POST /api/projects/{id}/assets?kind=plan_image&name=terminal.png
func (s *Server) handleUploadAsset(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()
	query := r.URL.Query()

	kind, ok := model.ParseAssetKind(query.Get("kind"))
	if !ok {
		return badRequest("Give a kind of plan_image, model_3d, model_spec, animation_json, go_model or other.")
	}
	if kind == model.AssetGoModel {
		if err := s.checkGoUploadAllowed(ctx); err != nil {
			return err
		}
	}

	name := sanitizeFilename(query.Get("name"))
	if name == "" {
		return badRequest("Give the original file name in the name parameter.")
	}

	if err := s.checkQuota(ctx, projectID, r.ContentLength); err != nil {
		return err
	}

	result, err := s.blobs.Put(ctx, r.Body, s.cfg.Storage.MaxUploadBytes)
	if err != nil {
		if errors.Is(err, blobstore.ErrTooLarge) {
			return &APIError{
				Status: http.StatusRequestEntityTooLarge, Code: "too_large",
				Message: fmt.Sprintf("That file is larger than the %d MB limit.", s.cfg.Storage.MaxUploadBytes>>20),
			}
		}
		return err
	}

	// The quota is re-checked now that the real size is known, since
	// Content-Length is a client-supplied hint.
	if err := s.checkQuota(ctx, projectID, result.Size); err != nil {
		s.releaseBlob(ctx, result.SHA256, result.RelPath)
		return err
	}

	meta, err := describeAsset(s.blobs, kind, result.RelPath)
	if err != nil {
		s.releaseBlob(ctx, result.SHA256, result.RelPath)
		return err
	}

	asset := &model.Asset{
		ProjectID:    projectID,
		Kind:         kind,
		OriginalName: name,
		ContentType:  contentTypeFor(name, r.Header.Get("Content-Type")),
		SizeBytes:    result.Size,
		SHA256:       result.SHA256,
		StoragePath:  result.RelPath,
		Meta:         meta,
		UploadedBy:   auth.UserID(ctx),
	}
	if err := s.store.Assets.Create(ctx, asset); err != nil {
		s.releaseBlob(ctx, result.SHA256, result.RelPath)
		return err
	}

	s.audit(ctx, store.Entry{
		UserID: asset.UploadedBy, ProjectID: projectID, Action: store.ActionAssetUpload,
		TargetKind: "asset", TargetID: asset.ID,
		Detail: map[string]any{"name": name, "kind": kind, "bytes": result.Size,
			"deduplicated": result.Deduplicated},
		IP: s.clientIP(r),
	})

	writeJSON(w, http.StatusCreated, asset)
	return nil
}

func (s *Server) handleGetAsset(w http.ResponseWriter, r *http.Request) error {
	asset, err := s.assetInProject(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, asset)
	return nil
}

// handleDownloadAsset streams a blob back. http.ServeContent gives range
// requests and conditional GETs, which is what lets the viewer seek into a
// large model instead of refetching it.
func (s *Server) handleDownloadAsset(w http.ResponseWriter, r *http.Request) error {
	asset, err := s.assetInProject(r)
	if err != nil {
		return err
	}

	file, err := s.blobs.Open(asset.StoragePath)
	if err != nil {
		return internal(fmt.Errorf("open asset %s: %w", asset.ID, err))
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return internal(err)
	}

	w.Header().Set("Content-Type", asset.ContentType)
	// The blob is immutable, so it can be cached hard. The URL contains the
	// asset id, and a new upload gets a new id.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+asset.SHA256+`"`)
	if queryBool(r.URL.Query().Get("download")) {
		w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(asset.OriginalName))
	}

	http.ServeContent(w, r, asset.OriginalName, info.ModTime(), file)
	return nil
}

func (s *Server) handleDeleteAsset(w http.ResponseWriter, r *http.Request) error {
	asset, err := s.assetInProject(r)
	if err != nil {
		return err
	}
	ctx := r.Context()

	if err := s.store.Assets.Delete(ctx, asset.ID); err != nil {
		return err
	}
	s.releaseBlob(ctx, asset.SHA256, asset.StoragePath)

	s.audit(ctx, store.Entry{
		UserID: auth.UserID(ctx), ProjectID: asset.ProjectID, Action: store.ActionAssetDelete,
		TargetKind: "asset", TargetID: asset.ID,
		Detail: map[string]string{"name": asset.OriginalName}, IP: s.clientIP(r),
	})

	writeNoContent(w)
	return nil
}

// assetInProject loads an asset and confirms it belongs to the project in the
// URL. Without that check a member of one project could read another's files
// by guessing an asset id.
func (s *Server) assetInProject(r *http.Request) (*model.Asset, error) {
	asset, err := s.store.Assets.ByID(r.Context(), r.PathValue("assetID"))
	if err != nil {
		if isNotFound(err) {
			return nil, notFound("That file")
		}
		return nil, err
	}
	if asset.ProjectID != r.PathValue("projectID") {
		return nil, notFound("That file")
	}
	return asset, nil
}

// releaseBlob removes a blob once the last asset row referencing its content
// is gone. Blobs are shared by hash, so deleting eagerly would break a
// different project's copy of the same file.
func (s *Server) releaseBlob(ctx context.Context, sha256, relPath string) {
	remaining, err := s.store.Assets.CountByHash(ctx, sha256)
	if err != nil {
		s.log.Warn("could not count blob references, leaving file in place",
			"sha256", sha256, "error", err)
		return
	}
	if remaining > 0 {
		return
	}
	if err := s.blobs.Remove(relPath); err != nil {
		s.log.Warn("could not remove blob", "path", relPath, "error", err)
	}
}

func (s *Server) checkQuota(ctx context.Context, projectID string, incoming int64) error {
	quota := s.cfg.Storage.ProjectQuotaBytes
	if quota <= 0 || incoming <= 0 {
		return nil
	}

	used, err := s.store.Assets.UsedBytes(ctx, projectID)
	if err != nil {
		return err
	}
	if used+incoming > quota {
		return &APIError{
			Status: http.StatusInsufficientStorage, Code: "quota_exceeded",
			Message: fmt.Sprintf("This project has used %d MB of its %d MB storage allowance.",
				used>>20, quota>>20),
		}
	}
	return nil
}

// checkGoUploadAllowed enforces both halves of the plugin gate: the server
// setting and the per-account permission.
func (s *Server) checkGoUploadAllowed(ctx context.Context) error {
	if !s.cfg.Plugins.Enabled {
		return forbidden("Uploading Go models is disabled on this server.")
	}
	identity := auth.FromContext(ctx)
	if identity == nil || (!identity.User.CanUploadGo && !identity.User.IsAdmin) {
		return forbidden("Your account is not allowed to upload Go models.")
	}
	return nil
}

// describeAsset extracts the metadata each kind needs at upload time. For a
// plan image that is its pixel dimensions, which the calibration tool needs
// before it can draw anything.
func describeAsset(blobs *blobstore.Store, kind model.AssetKind, relPath string) (json.RawMessage, error) {
	if kind != model.AssetPlanImage {
		return nil, nil
	}

	file, err := blobs.Open(relPath)
	if err != nil {
		return nil, internal(err)
	}
	defer file.Close()

	cfg, format, err := image.DecodeConfig(file)
	if err != nil {
		return nil, badRequest("That file could not be read as a PNG, JPEG or GIF image.")
	}

	return json.Marshal(map[string]any{
		"width":  cfg.Width,
		"height": cfg.Height,
		"format": format,
	})
}

// sanitizeFilename strips any directory component a client may have sent, so a
// name can never influence where the file lands or how it is served.
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	return trimTo(name, 255)
}

func contentTypeFor(name, declared string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".glb":
		return "model/gltf-binary"
	case ".gltf":
		return "model/gltf+json"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".go":
		return "text/plain; charset=utf-8"
	case ".zip":
		return "application/zip"
	}

	declared = strings.TrimSpace(declared)
	if declared == "" || strings.HasPrefix(declared, "multipart/") {
		return "application/octet-stream"
	}
	return declared
}
