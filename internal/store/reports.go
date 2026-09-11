package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andreybaranovskiy/simulation/internal/db"
	"github.com/andreybaranovskiy/simulation/internal/model"
)

type ReportStore struct{ db *db.DB }

const reportColumns = `id, project_id, name, subtitle, kind, scenario_ids,
	sections, created_by, created_at, updated_at`

func (s *ReportStore) Create(ctx context.Context, r *model.Report) error {
	if r.ID == "" {
		r.ID = NewID()
	}
	ts := now()
	r.CreatedAt, r.UpdatedAt = ts, ts

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO reports (`+reportColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ProjectID, r.Name, r.Subtitle, string(r.Kind),
		marshalStrings(r.ScenarioIDs), marshalStrings(r.Sections),
		r.CreatedBy, r.CreatedAt, r.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create report: %w", mapErr(err))
	}
	return nil
}

func (s *ReportStore) ByID(ctx context.Context, id string) (*model.Report, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+reportColumns+` FROM reports WHERE id = ?`, id)
	var r model.Report
	if err := scanReport(row, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *ReportStore) List(ctx context.Context, projectID string) ([]model.Report, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+reportColumns+` FROM reports WHERE project_id = ? ORDER BY updated_at DESC`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("list reports: %w", mapErr(err))
	}
	defer rows.Close()

	out := []model.Report{}
	for rows.Next() {
		var r model.Report
		if err := scanReport(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *ReportStore) Update(ctx context.Context, r *model.Report) error {
	r.UpdatedAt = now()
	res, err := s.db.ExecContext(ctx,
		`UPDATE reports
		 SET name = ?, subtitle = ?, scenario_ids = ?, sections = ?, updated_at = ?
		 WHERE id = ?`,
		r.Name, r.Subtitle, marshalStrings(r.ScenarioIDs), marshalStrings(r.Sections),
		r.UpdatedAt, r.ID)
	return affectedOne(res, err, "update report")
}

func (s *ReportStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM reports WHERE id = ?`, id)
	return affectedOne(res, err, "delete report")
}

func scanReport(sc scanner, r *model.Report) error {
	var kind string
	var scenarioIDs, sections []byte

	err := sc.Scan(&r.ID, &r.ProjectID, &r.Name, &r.Subtitle, &kind,
		&scenarioIDs, &sections, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return mapErr(err)
	}

	r.Kind = model.ReportKind(kind)
	r.ScenarioIDs = unmarshalStrings(scenarioIDs)
	r.Sections = unmarshalStrings(sections)
	r.CreatedAt = r.CreatedAt.UTC()
	r.UpdatedAt = r.UpdatedAt.UTC()
	return nil
}

// marshalStrings stores a string slice as a JSON array, never NULL: an empty
// selection is a real value here, meaning "no sections chosen", and has to
// survive the round trip as [] rather than come back as nil.
func marshalStrings(values []string) []byte {
	if values == nil {
		values = []string{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return []byte("[]")
	}
	return data
}

func unmarshalStrings(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return []string{}
	}
	return out
}
