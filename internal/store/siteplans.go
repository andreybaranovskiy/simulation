package store

import (
	"context"
	"fmt"

	"github.com/andreybaranovskiy/simulation/internal/db"
	"github.com/andreybaranovskiy/simulation/internal/model"
)

type SitePlanStore struct{ db *db.DB }

const sitePlanColumns = `id, project_id, asset_id, name, image_width, image_height,
	meters_per_pixel, origin_px_x, origin_px_y, rotation_deg, flip_y, calibration,
	created_at, updated_at`

func (s *SitePlanStore) Create(ctx context.Context, p *model.SitePlan) error {
	if p.ID == "" {
		p.ID = NewID()
	}
	ts := now()
	p.CreatedAt, p.UpdatedAt = ts, ts

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO site_plans (`+sitePlanColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.ProjectID, p.AssetID, p.Name, p.ImageWidth, p.ImageHeight,
		p.MetersPerPixel, p.OriginPxX, p.OriginPxY, p.RotationDeg, p.FlipY,
		jsonOrNil(p.Calibration), p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create site plan: %w", mapErr(err))
	}
	return nil
}

func (s *SitePlanStore) ByID(ctx context.Context, id string) (*model.SitePlan, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+sitePlanColumns+` FROM site_plans WHERE id = ?`, id)
	var p model.SitePlan
	if err := scanSitePlan(row, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *SitePlanStore) List(ctx context.Context, projectID string) ([]model.SitePlan, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sitePlanColumns+` FROM site_plans WHERE project_id = ? ORDER BY created_at`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("list site plans: %w", mapErr(err))
	}
	defer rows.Close()

	out := []model.SitePlan{}
	for rows.Next() {
		var p model.SitePlan
		if err := scanSitePlan(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateCalibration writes the georeference. It is separate from the rest of
// the row because calibrating is its own user action, repeated until the scale
// bar measures correctly.
func (s *SitePlanStore) UpdateCalibration(ctx context.Context, p *model.SitePlan) error {
	p.UpdatedAt = now()
	res, err := s.db.ExecContext(ctx,
		`UPDATE site_plans
		 SET name = ?, meters_per_pixel = ?, origin_px_x = ?, origin_px_y = ?,
		     rotation_deg = ?, flip_y = ?, calibration = ?, updated_at = ?
		 WHERE id = ?`,
		p.Name, p.MetersPerPixel, p.OriginPxX, p.OriginPxY,
		p.RotationDeg, p.FlipY, jsonOrNil(p.Calibration), p.UpdatedAt, p.ID)
	return affectedOne(res, err, "update site plan")
}

func (s *SitePlanStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM site_plans WHERE id = ?`, id)
	return affectedOne(res, err, "delete site plan")
}

func scanSitePlan(sc scanner, p *model.SitePlan) error {
	var calibration []byte
	err := sc.Scan(&p.ID, &p.ProjectID, &p.AssetID, &p.Name, &p.ImageWidth, &p.ImageHeight,
		&p.MetersPerPixel, &p.OriginPxX, &p.OriginPxY, &p.RotationDeg, &p.FlipY,
		&calibration, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("scan site plan: %w", mapErr(err))
	}
	if len(calibration) > 0 {
		p.Calibration = append([]byte(nil), calibration...)
	}
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return nil
}

// jsonOrNil keeps empty JSON columns NULL rather than storing the string
// "null", which MySQL would accept but which reads back as a valid JSON value.
func jsonOrNil(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}
