package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/andreybaranovskiy/simulation/internal/db"
	"github.com/andreybaranovskiy/simulation/internal/model"
)

type AuditStore struct{ db *db.DB }

// Action names used across the server. Keeping them as constants means the
// admin log filter has a fixed vocabulary to offer.
const (
	ActionUserRegister     = "user.register"
	ActionUserLogin        = "user.login"
	ActionUserLoginFailed  = "user.login_failed"
	ActionUserLogout       = "user.logout"
	ActionUserPassword     = "user.password_change"
	ActionUserFlagsChanged = "user.flags_changed"

	ActionProjectCreate  = "project.create"
	ActionProjectUpdate  = "project.update"
	ActionProjectArchive = "project.archive"
	ActionProjectRestore = "project.restore"
	ActionProjectDelete  = "project.delete"
	ActionMemberSet      = "project.member_set"
	ActionMemberRemove   = "project.member_remove"

	ActionAssetUpload = "asset.upload"
	ActionAssetDelete = "asset.delete"

	ActionSitePlanCreate    = "site_plan.create"
	ActionSitePlanCalibrate = "site_plan.calibrate"
	ActionSitePlanDelete    = "site_plan.delete"
)

// Entry is the input to Record. Everything except Action is optional.
type Entry struct {
	UserID     string
	ProjectID  string
	Action     string
	TargetKind string
	TargetID   string
	Detail     any
	IP         string
}

// Record writes an audit row. Audit logging must never break the request that
// triggered it, so the error is returned for logging but callers are expected
// to carry on.
func (s *AuditStore) Record(ctx context.Context, e Entry) error {
	var detail any
	if e.Detail != nil {
		encoded, err := json.Marshal(e.Detail)
		if err != nil {
			return fmt.Errorf("encode audit detail: %w", err)
		}
		detail = encoded
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (at, user_id, project_id, action, target_kind, target_id, detail, ip)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		now(), nullIfEmpty(e.UserID), nullIfEmpty(e.ProjectID),
		e.Action, e.TargetKind, e.TargetID, detail, truncate(e.IP, 45))
	if err != nil {
		return fmt.Errorf("record audit entry: %w", mapErr(err))
	}
	return nil
}

// ListFilter narrows the admin log view.
type ListFilter struct {
	ProjectID string
	UserID    string
	Action    string
	Limit     int
	Offset    int
}

func (s *AuditStore) List(ctx context.Context, f ListFilter) ([]model.AuditEntry, error) {
	query := `SELECT id, at, user_id, project_id, action, target_kind, target_id, detail, ip
	          FROM audit_log WHERE 1 = 1`
	args := []any{}

	if f.ProjectID != "" {
		query += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
	if f.UserID != "" {
		query += ` AND user_id = ?`
		args = append(args, f.UserID)
	}
	if f.Action != "" {
		query += ` AND action = ?`
		args = append(args, f.Action)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	query += ` ORDER BY at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit log: %w", mapErr(err))
	}
	defer rows.Close()

	out := []model.AuditEntry{}
	for rows.Next() {
		var e model.AuditEntry
		var userID, projectID sql.NullString
		var detail []byte

		if err := rows.Scan(&e.ID, &e.At, &userID, &projectID,
			&e.Action, &e.TargetKind, &e.TargetID, &detail, &e.IP); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", mapErr(err))
		}

		e.At = e.At.UTC()
		e.UserID = strPtr(userID)
		e.ProjectID = strPtr(projectID)
		if len(detail) > 0 {
			e.Detail = append([]byte(nil), detail...)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
