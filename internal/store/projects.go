package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/andreybaranovskiy/simulation/internal/db"
	"github.com/andreybaranovskiy/simulation/internal/model"
)

type ProjectStore struct{ db *db.DB }

const projectColumns = `id, name, description, owner_id, archived_at, created_at, updated_at`

// Create inserts the project and the owner's membership row in one
// transaction. A project with no owner membership would be invisible to its
// own creator, so the two writes must not be able to come apart.
func (s *ProjectStore) Create(ctx context.Context, p *model.Project) error {
	if p.ID == "" {
		p.ID = NewID()
	}
	ts := now()
	p.CreatedAt, p.UpdatedAt = ts, ts

	err := s.db.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects (`+projectColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Name, p.Description, p.OwnerID, nil, p.CreatedAt, p.UpdatedAt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
			p.ID, p.OwnerID, model.RoleOwner, ts)
		return err
	})
	if err != nil {
		return fmt.Errorf("create project: %w", mapErr(err))
	}

	p.Role = model.RoleOwner
	return nil
}

// ByID returns a project along with the requesting user's role in it. A user
// with no membership gets ErrNotFound rather than a permission error, so the
// API never confirms that a project id exists to someone who cannot see it.
func (s *ProjectStore) ByID(ctx context.Context, projectID, userID string) (*model.Project, error) {
	const query = `
SELECT p.id, p.name, p.description, p.owner_id, p.archived_at, p.created_at, p.updated_at, m.role
FROM projects p
JOIN project_members m ON m.project_id = p.id AND m.user_id = ?
WHERE p.id = ?`

	var p model.Project
	var archived sql.NullTime

	err := s.db.QueryRowContext(ctx, query, userID, projectID).Scan(
		&p.ID, &p.Name, &p.Description, &p.OwnerID, &archived, &p.CreatedAt, &p.UpdatedAt, &p.Role)
	if err != nil {
		return nil, mapErr(err)
	}

	p.ArchivedAt = timePtr(archived)
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return &p, nil
}

// ByIDAdmin fetches a project without a membership check, for admin tooling.
func (s *ProjectStore) ByIDAdmin(ctx context.Context, projectID string) (*model.Project, error) {
	var p model.Project
	var archived sql.NullTime

	err := s.db.QueryRowContext(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE id = ?`, projectID).Scan(
		&p.ID, &p.Name, &p.Description, &p.OwnerID, &archived, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}

	p.ArchivedAt = timePtr(archived)
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return &p, nil
}

// ListForUser returns every project the user is a member of, newest first.
func (s *ProjectStore) ListForUser(ctx context.Context, userID string, includeArchived bool) ([]model.Project, error) {
	query := `
SELECT p.id, p.name, p.description, p.owner_id, p.archived_at, p.created_at, p.updated_at, m.role
FROM projects p
JOIN project_members m ON m.project_id = p.id AND m.user_id = ?`
	if !includeArchived {
		query += ` WHERE p.archived_at IS NULL`
	}
	query += ` ORDER BY p.updated_at DESC`

	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", mapErr(err))
	}
	defer rows.Close()

	out := []model.Project{}
	for rows.Next() {
		var p model.Project
		var archived sql.NullTime
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.OwnerID,
			&archived, &p.CreatedAt, &p.UpdatedAt, &p.Role); err != nil {
			return nil, fmt.Errorf("scan project: %w", mapErr(err))
		}
		p.ArchivedAt = timePtr(archived)
		p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *ProjectStore) Update(ctx context.Context, projectID, name, description string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET name = ?, description = ?, updated_at = ? WHERE id = ?`,
		name, description, now(), projectID)
	return affectedOne(res, err, "update project")
}

// SetArchived soft-deletes or restores a project. Runs and assets are kept so
// an archived project can be brought back intact.
func (s *ProjectStore) SetArchived(ctx context.Context, projectID string, archived bool) error {
	var at any
	if archived {
		at = now()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE projects SET archived_at = ?, updated_at = ? WHERE id = ?`,
		at, now(), projectID)
	return affectedOne(res, err, "archive project")
}

// Delete removes a project permanently. Members, assets and site plans cascade;
// the caller is responsible for deleting the project's files on disk first.
func (s *ProjectStore) Delete(ctx context.Context, projectID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, projectID)
	return affectedOne(res, err, "delete project")
}

// Touch bumps updated_at so a project sorts to the top after activity inside
// it, such as a finished run.
func (s *ProjectStore) Touch(ctx context.Context, projectID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET updated_at = ? WHERE id = ?`, now(), projectID)
	return mapErr(err)
}

// RoleFor returns the user's role in a project, or ErrNotFound when they have
// none. This is the single query every permission check goes through.
func (s *ProjectStore) RoleFor(ctx context.Context, projectID, userID string) (model.Role, error) {
	var role model.Role
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM project_members WHERE project_id = ? AND user_id = ?`,
		projectID, userID).Scan(&role)
	if err != nil {
		return "", mapErr(err)
	}
	return role, nil
}

func (s *ProjectStore) Members(ctx context.Context, projectID string) ([]model.ProjectMember, error) {
	const query = `
SELECT m.project_id, m.user_id, u.email, u.display_name, m.role, m.created_at
FROM project_members m
JOIN users u ON u.id = m.user_id
WHERE m.project_id = ?
ORDER BY FIELD(m.role, 'owner', 'editor', 'viewer'), u.email`

	rows, err := s.db.QueryContext(ctx, query, projectID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", mapErr(err))
	}
	defer rows.Close()

	out := []model.ProjectMember{}
	for rows.Next() {
		var m model.ProjectMember
		if err := rows.Scan(&m.ProjectID, &m.UserID, &m.Email, &m.DisplayName, &m.Role, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", mapErr(err))
		}
		m.CreatedAt = m.CreatedAt.UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMember adds a member or changes an existing role.
func (s *ProjectStore) SetMember(ctx context.Context, projectID, userID string, role model.Role) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO project_members (project_id, user_id, role, created_at)
		 VALUES (?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE role = VALUES(role)`,
		projectID, userID, role, now())
	if err != nil {
		return fmt.Errorf("set member: %w", mapErr(err))
	}
	return nil
}

func (s *ProjectStore) RemoveMember(ctx context.Context, projectID, userID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, userID)
	return affectedOne(res, err, "remove member")
}

// CountOwners guards the last-owner rule: a project must always keep at least
// one owner, otherwise nobody can manage it again.
func (s *ProjectStore) CountOwners(ctx context.Context, projectID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_members WHERE project_id = ? AND role = 'owner'`,
		projectID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count owners: %w", mapErr(err))
	}
	return n, nil
}
