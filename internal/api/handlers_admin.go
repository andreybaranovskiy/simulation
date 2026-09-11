package api

import (
	"net/http"

	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit := queryInt(q.Get("limit"), 50, 1, 200)
	offset := queryInt(q.Get("offset"), 0, 0, 1_000_000)

	users, err := s.store.Users.List(r.Context(), limit, offset)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, users)
	return nil
}

type adminCreateUserRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
	IsAdmin     bool   `json:"isAdmin"`
}

// handleAdminCreateUser is how accounts are made when self-registration is off.
func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) error {
	var req adminCreateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	user, err := s.auth.CreateUserAsAdmin(r.Context(), req.Email, req.Password,
		trimTo(req.DisplayName, 120), req.IsAdmin)
	if err != nil {
		return err
	}

	s.audit(r.Context(), store.Entry{
		UserID: auth.UserID(r.Context()), Action: store.ActionUserRegister,
		TargetKind: "user", TargetID: user.ID,
		Detail: map[string]any{"email": user.Email, "isAdmin": user.IsAdmin, "by": "admin"},
		IP:     s.clientIP(r),
	})

	writeJSON(w, http.StatusCreated, user)
	return nil
}

type adminUpdateUserRequest struct {
	IsAdmin     bool `json:"isAdmin"`
	CanUploadGo bool `json:"canUploadGo"`
	IsActive    bool `json:"isActive"`
}

// handleAdminUpdateUser sets the three account flags, including the per-account
// half of the Go-model upload gate.
func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) error {
	targetID := r.PathValue("userID")
	ctx := r.Context()

	var req adminUpdateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	target, err := s.store.Users.ByID(ctx, targetID)
	if err != nil {
		if isNotFound(err) {
			return notFound("That account")
		}
		return err
	}

	// Losing the last administrator would leave nobody able to restore one.
	losingAdmin := target.IsAdmin && target.IsActive && (!req.IsAdmin || !req.IsActive)
	if losingAdmin {
		admins, err := s.store.Users.CountAdmins(ctx)
		if err != nil {
			return err
		}
		if admins <= 1 {
			return conflict("This is the last active administrator.")
		}
	}

	if err := s.store.Users.SetFlags(ctx, targetID, req.IsAdmin, req.CanUploadGo, req.IsActive); err != nil {
		return err
	}

	// A disabled account should lose access immediately, not at session expiry.
	if !req.IsActive {
		if err := s.store.Sessions.DeleteForUser(ctx, targetID); err != nil {
			s.log.Warn("could not end sessions for disabled account", "user", targetID, "error", err)
		}
	}

	s.audit(ctx, store.Entry{
		UserID: auth.UserID(ctx), Action: store.ActionUserFlagsChanged,
		TargetKind: "user", TargetID: targetID,
		Detail: map[string]bool{"isAdmin": req.IsAdmin, "canUploadGo": req.CanUploadGo, "isActive": req.IsActive},
		IP:     s.clientIP(r),
	})

	updated, err := s.store.Users.ByID(ctx, targetID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, updated)
	return nil
}

type adminSetPasswordRequest struct {
	NewPassword string `json:"newPassword"`
}

func (s *Server) handleAdminSetPassword(w http.ResponseWriter, r *http.Request) error {
	targetID := r.PathValue("userID")

	var req adminSetPasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	if _, err := s.store.Users.ByID(r.Context(), targetID); err != nil {
		if isNotFound(err) {
			return notFound("That account")
		}
		return err
	}

	if err := s.auth.SetPassword(r.Context(), targetID, req.NewPassword); err != nil {
		return err
	}

	s.audit(r.Context(), store.Entry{
		UserID: auth.UserID(r.Context()), Action: store.ActionUserPassword,
		TargetKind: "user", TargetID: targetID,
		Detail: map[string]string{"by": "admin"}, IP: s.clientIP(r),
	})

	writeNoContent(w)
	return nil
}

func (s *Server) handleAdminAuditLog(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()

	entries, err := s.store.Audit.List(r.Context(), store.ListFilter{
		ProjectID: q.Get("projectId"),
		UserID:    q.Get("userId"),
		Action:    q.Get("action"),
		Limit:     queryInt(q.Get("limit"), 100, 1, 500),
		Offset:    queryInt(q.Get("offset"), 0, 0, 1_000_000),
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, entries)
	return nil
}
