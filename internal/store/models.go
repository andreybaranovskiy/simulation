package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/andreybaranovskiy/simulation/internal/db"
	"github.com/andreybaranovskiy/simulation/internal/model"
)

type ModelStore struct{ db *db.DB }

const modelColumns = `id, project_id, name, description, source, template_key,
	domain, spec, asset_id, version, created_by, created_at, updated_at`

func (s *ModelStore) Create(ctx context.Context, m *model.SimModel) error {
	if m.ID == "" {
		m.ID = NewID()
	}
	ts := now()
	m.CreatedAt, m.UpdatedAt = ts, ts
	if m.Version == 0 {
		m.Version = 1
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO models (`+modelColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ProjectID, m.Name, m.Description, m.Source, m.TemplateKey,
		m.Domain, jsonOrNil(m.Spec), nullOrString(m.AssetID), m.Version,
		m.CreatedBy, m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create model: %w", mapErr(err))
	}
	return nil
}

func (s *ModelStore) ByID(ctx context.Context, id string) (*model.SimModel, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+modelColumns+` FROM models WHERE id = ?`, id)
	var m model.SimModel
	if err := scanModel(row, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// List returns a project's models with a scenario count each, so the list page
// does not need a query per row.
func (s *ModelStore) List(ctx context.Context, projectID string) ([]model.SimModel, error) {
	query := `
SELECT ` + prefixed("m", modelColumns) + `, COUNT(sc.id)
FROM models m
LEFT JOIN scenarios sc ON sc.model_id = m.id AND sc.archived_at IS NULL
WHERE m.project_id = ?
GROUP BY m.id
ORDER BY m.updated_at DESC`

	rows, err := s.db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", mapErr(err))
	}
	defer rows.Close()

	out := []model.SimModel{}
	for rows.Next() {
		var m model.SimModel
		var spec []byte
		var asset sql.NullString

		err := rows.Scan(&m.ID, &m.ProjectID, &m.Name, &m.Description, &m.Source,
			&m.TemplateKey, &m.Domain, &spec, &asset, &m.Version,
			&m.CreatedBy, &m.CreatedAt, &m.UpdatedAt, &m.ScenarioCount)
		if err != nil {
			return nil, fmt.Errorf("scan model: %w", mapErr(err))
		}

		if len(spec) > 0 {
			m.Spec = append([]byte(nil), spec...)
		}
		m.AssetID = strPtr(asset)
		m.CreatedAt, m.UpdatedAt = m.CreatedAt.UTC(), m.UpdatedAt.UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// Update changes the editable fields. A spec change bumps the version, so a
// run's provenance can say which revision it used.
func (s *ModelStore) Update(ctx context.Context, m *model.SimModel) error {
	m.UpdatedAt = now()

	res, err := s.db.ExecContext(ctx,
		`UPDATE models SET name = ?, description = ?, domain = ?, spec = ?,
		        version = version + 1, updated_at = ?
		 WHERE id = ?`,
		m.Name, m.Description, m.Domain, jsonOrNil(m.Spec), m.UpdatedAt, m.ID)
	return affectedOne(res, err, "update model")
}

func (s *ModelStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM models WHERE id = ?`, id)
	return affectedOne(res, err, "delete model")
}

func scanModel(sc scanner, m *model.SimModel) error {
	var spec []byte
	var asset sql.NullString

	err := sc.Scan(&m.ID, &m.ProjectID, &m.Name, &m.Description, &m.Source,
		&m.TemplateKey, &m.Domain, &spec, &asset, &m.Version,
		&m.CreatedBy, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("scan model: %w", mapErr(err))
	}

	if len(spec) > 0 {
		m.Spec = append([]byte(nil), spec...)
	}
	m.AssetID = strPtr(asset)
	m.CreatedAt, m.UpdatedAt = m.CreatedAt.UTC(), m.UpdatedAt.UTC()
	return nil
}

// prefixed qualifies a comma-separated column list with a table alias, so a
// join can reuse the same list rather than repeating it.
func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ",")
	out := make([]string, 0, len(parts))

	for _, col := range parts {
		if col = strings.TrimSpace(col); col != "" {
			out = append(out, alias+"."+col)
		}
	}
	return strings.Join(out, ", ")
}

func nullOrString(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}
