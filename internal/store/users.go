package store

import (
	"context"
	"fmt"

	"github.com/andreybaranovskiy/simulation/internal/db"
	"github.com/andreybaranovskiy/simulation/internal/model"
)

type UserStore struct{ db *db.DB }

const userColumns = `id, email, email_norm, password_hash, display_name,
	is_admin, can_upload_go, is_active, created_at, updated_at`

// Create inserts a user. The caller supplies an already-hashed password and a
// normalized email; this method only persists.
func (s *UserStore) Create(ctx context.Context, u *model.User) error {
	if u.ID == "" {
		u.ID = NewID()
	}
	ts := now()
	u.CreatedAt, u.UpdatedAt = ts, ts

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (`+userColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.EmailNorm, u.PasswordHash, u.DisplayName,
		u.IsAdmin, u.CanUploadGo, u.IsActive, u.CreatedAt, u.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create user: %w", mapErr(err))
	}
	return nil
}

func (s *UserStore) ByID(ctx context.Context, id string) (*model.User, error) {
	return s.scanOne(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
}

func (s *UserStore) ByEmail(ctx context.Context, emailNorm string) (*model.User, error) {
	return s.scanOne(ctx, `SELECT `+userColumns+` FROM users WHERE email_norm = ?`, emailNorm)
}

// Count reports the number of accounts, used to decide whether to bootstrap
// the first admin at startup.
func (s *UserStore) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count users: %w", mapErr(err))
	}
	return n, nil
}

func (s *UserStore) List(ctx context.Context, limit, offset int) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", mapErr(err))
	}
	defer rows.Close()

	var out []model.User
	for rows.Next() {
		var u model.User
		if err := scanUser(rows, &u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SearchByEmail finds candidates for the "add member" picker. It matches a
// prefix so the index on email_norm is usable.
func (s *UserStore) SearchByEmail(ctx context.Context, prefix string, limit int) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users
		 WHERE is_active = 1 AND email_norm LIKE CONCAT(?, '%')
		 ORDER BY email_norm LIMIT ?`,
		prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("search users: %w", mapErr(err))
	}
	defer rows.Close()

	var out []model.User
	for rows.Next() {
		var u model.User
		if err := scanUser(rows, &u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateProfile changes the display name only. Email changes go through a
// separate flow because they move the uniqueness key.
func (s *UserStore) UpdateProfile(ctx context.Context, id, displayName string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET display_name = ?, updated_at = ? WHERE id = ?`,
		displayName, now(), id)
	return affectedOne(res, err, "update profile")
}

func (s *UserStore) UpdatePasswordHash(ctx context.Context, id, hash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		hash, now(), id)
	return affectedOne(res, err, "update password")
}

// SetFlags is the admin-only switch for the three account-level permissions.
func (s *UserStore) SetFlags(ctx context.Context, id string, isAdmin, canUploadGo, isActive bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET is_admin = ?, can_upload_go = ?, is_active = ?, updated_at = ?
		 WHERE id = ?`,
		isAdmin, canUploadGo, isActive, now(), id)
	return affectedOne(res, err, "update user flags")
}

// CountAdmins guards against removing or disabling the last administrator.
func (s *UserStore) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE is_admin = 1 AND is_active = 1`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count admins: %w", mapErr(err))
	}
	return n, nil
}

func (s *UserStore) scanOne(ctx context.Context, query string, args ...any) (*model.User, error) {
	var u model.User
	row := s.db.QueryRowContext(ctx, query, args...)
	if err := scanUser(row, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// scanner covers both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanUser(sc scanner, u *model.User) error {
	err := sc.Scan(&u.ID, &u.Email, &u.EmailNorm, &u.PasswordHash, &u.DisplayName,
		&u.IsAdmin, &u.CanUploadGo, &u.IsActive, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return fmt.Errorf("scan user: %w", mapErr(err))
	}
	u.CreatedAt = u.CreatedAt.UTC()
	u.UpdatedAt = u.UpdatedAt.UTC()
	return nil
}
