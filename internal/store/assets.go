package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/andreybaranovskiy/simulation/internal/db"
	"github.com/andreybaranovskiy/simulation/internal/model"
)

type AssetStore struct{ db *db.DB }

const assetColumns = `id, project_id, kind, original_name, content_type,
	size_bytes, sha256, storage_path, meta, uploaded_by, created_at`

func (s *AssetStore) Create(ctx context.Context, a *model.Asset) error {
	if a.ID == "" {
		a.ID = NewID()
	}
	a.CreatedAt = now()

	var meta any
	if len(a.Meta) > 0 {
		meta = []byte(a.Meta)
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO assets (`+assetColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.ProjectID, a.Kind, a.OriginalName, a.ContentType,
		a.SizeBytes, a.SHA256, a.StoragePath, meta, a.UploadedBy, a.CreatedAt)
	if err != nil {
		return fmt.Errorf("create asset: %w", mapErr(err))
	}
	return nil
}

func (s *AssetStore) ByID(ctx context.Context, id string) (*model.Asset, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+assetColumns+` FROM assets WHERE id = ?`, id)
	var a model.Asset
	if err := scanAsset(row, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// ByHash finds an existing asset with the same content in the same project, so
// re-uploading a 300 MB GLB does not store it twice.
func (s *AssetStore) ByHash(ctx context.Context, projectID, sha256 string) (*model.Asset, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+assetColumns+` FROM assets WHERE project_id = ? AND sha256 = ? LIMIT 1`,
		projectID, sha256)
	var a model.Asset
	if err := scanAsset(row, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// List returns a project's assets, optionally filtered to one kind.
func (s *AssetStore) List(ctx context.Context, projectID string, kind model.AssetKind) ([]model.Asset, error) {
	query := `SELECT ` + assetColumns + ` FROM assets WHERE project_id = ?`
	args := []any{projectID}
	if kind != "" {
		query += ` AND kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list assets: %w", mapErr(err))
	}
	defer rows.Close()

	out := []model.Asset{}
	for rows.Next() {
		var a model.Asset
		if err := scanAsset(rows, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *AssetStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, id)
	return affectedOne(res, err, "delete asset")
}

// UsedBytes is the project's storage total, checked against the quota before
// accepting an upload.
func (s *AssetStore) UsedBytes(ctx context.Context, projectID string) (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT SUM(size_bytes) FROM assets WHERE project_id = ?`, projectID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum asset bytes: %w", mapErr(err))
	}
	return total.Int64, nil
}

// CountByHash reports how many asset rows share a content hash. The blob on
// disk is only safe to remove when the last row referencing it goes away.
func (s *AssetStore) CountByHash(ctx context.Context, sha256 string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets WHERE sha256 = ?`, sha256).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count assets by hash: %w", mapErr(err))
	}
	return n, nil
}

func scanAsset(sc scanner, a *model.Asset) error {
	var meta []byte
	err := sc.Scan(&a.ID, &a.ProjectID, &a.Kind, &a.OriginalName, &a.ContentType,
		&a.SizeBytes, &a.SHA256, &a.StoragePath, &meta, &a.UploadedBy, &a.CreatedAt)
	if err != nil {
		return fmt.Errorf("scan asset: %w", mapErr(err))
	}
	if len(meta) > 0 {
		a.Meta = append([]byte(nil), meta...)
	}
	a.CreatedAt = a.CreatedAt.UTC()
	return nil
}
