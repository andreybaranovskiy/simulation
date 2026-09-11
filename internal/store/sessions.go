package store

import (
	"context"
	"fmt"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/db"
	"github.com/andreybaranovskiy/simulation/internal/model"
)

type SessionStore struct{ db *db.DB }

// Create stores a session. s.ID must already be the hash of the cookie token.
func (st *SessionStore) Create(ctx context.Context, s *model.Session) error {
	ts := now()
	s.CreatedAt, s.LastSeenAt = ts, ts

	_, err := st.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, created_at, last_seen_at, expires_at, user_agent, ip)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.UserID, s.CreatedAt, s.LastSeenAt, s.ExpiresAt.UTC(), truncate(s.UserAgent, 255), truncate(s.IP, 45))
	if err != nil {
		return fmt.Errorf("create session: %w", mapErr(err))
	}
	return nil
}

// Resolve looks up a session together with its user in one round trip, which
// is what every authenticated request does. Expired rows are treated as
// missing rather than returned for the caller to check.
func (st *SessionStore) Resolve(ctx context.Context, sessionID string) (*model.Session, *model.User, error) {
	const query = `
SELECT s.id, s.user_id, s.created_at, s.last_seen_at, s.expires_at, s.user_agent, s.ip,
       u.id, u.email, u.email_norm, u.password_hash, u.display_name,
       u.is_admin, u.can_upload_go, u.is_active, u.created_at, u.updated_at
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.id = ? AND s.expires_at > ?`

	var s model.Session
	var u model.User

	err := st.db.QueryRowContext(ctx, query, sessionID, now()).Scan(
		&s.ID, &s.UserID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.UserAgent, &s.IP,
		&u.ID, &u.Email, &u.EmailNorm, &u.PasswordHash, &u.DisplayName,
		&u.IsAdmin, &u.CanUploadGo, &u.IsActive, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, nil, mapErr(err)
	}

	s.CreatedAt, s.LastSeenAt, s.ExpiresAt = s.CreatedAt.UTC(), s.LastSeenAt.UTC(), s.ExpiresAt.UTC()
	u.CreatedAt, u.UpdatedAt = u.CreatedAt.UTC(), u.UpdatedAt.UTC()
	return &s, &u, nil
}

// Touch slides the last-seen timestamp and, when the session is more than
// halfway through its life, extends the expiry. Refreshing only in the second
// half avoids a write on every single request.
func (st *SessionStore) Touch(ctx context.Context, sessionID string, ttl time.Duration) (time.Time, bool, error) {
	ts := now()
	var expires time.Time

	err := st.db.QueryRowContext(ctx,
		`SELECT expires_at FROM sessions WHERE id = ?`, sessionID).Scan(&expires)
	if err != nil {
		return time.Time{}, false, mapErr(err)
	}
	expires = expires.UTC()

	remaining := expires.Sub(ts)
	if remaining > ttl/2 {
		_, err := st.db.ExecContext(ctx,
			`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, ts, sessionID)
		return expires, false, mapErr(err)
	}

	newExpiry := ts.Add(ttl)
	_, err = st.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
		ts, newExpiry, sessionID)
	if err != nil {
		return time.Time{}, false, mapErr(err)
	}
	return newExpiry, true, nil
}

func (st *SessionStore) Delete(ctx context.Context, sessionID string) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("delete session: %w", mapErr(err))
	}
	return nil
}

// DeleteForUser ends every session a user has, used on password change and
// when an admin disables an account.
func (st *SessionStore) DeleteForUser(ctx context.Context, userID string) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("delete user sessions: %w", mapErr(err))
	}
	return nil
}

// DeleteExpired is called periodically by the server's janitor.
func (st *SessionStore) DeleteExpired(ctx context.Context) (int64, error) {
	res, err := st.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now())
	if err != nil {
		return 0, fmt.Errorf("purge sessions: %w", mapErr(err))
	}
	return res.RowsAffected()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
