package api

import (
	"net/http"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

type healthResponse struct {
	Status   string `json:"status"`
	Version  string `json:"version"`
	Database string `json:"database"`
	Time     string `json:"time"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) error {
	resp := healthResponse{
		Status:   "ok",
		Version:  Version,
		Database: "ok",
		Time:     time.Now().UTC().Format(time.RFC3339),
	}

	ctx, cancel := contextWithTimeout(r, 3*time.Second)
	defer cancel()

	if err := s.store.DB.PingContext(ctx); err != nil {
		resp.Status = "degraded"
		resp.Database = "unreachable"
		writeJSON(w, http.StatusServiceUnavailable, resp)
		return nil
	}

	writeJSON(w, http.StatusOK, resp)
	return nil
}

type authConfigResponse struct {
	Provider          string `json:"provider"`
	AllowRegistration bool   `json:"allowRegistration"`
	MinPasswordLength int    `json:"minPasswordLength"`
}

// handleAuthConfig lets the login page render the right controls before anyone
// has signed in.
func (s *Server) handleAuthConfig(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, authConfigResponse{
		Provider:          s.auth.ProviderName(),
		AllowRegistration: s.cfg.Auth.AllowRegistration,
		MinPasswordLength: auth.MinPasswordLength,
	})
	return nil
}

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

type sessionResponse struct {
	User      *model.User `json:"user"`
	ExpiresAt time.Time   `json:"expiresAt"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) error {
	var req registerRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	user, err := s.auth.Register(r.Context(), req.Email, req.Password, trimTo(req.DisplayName, 120))
	if err != nil {
		return err
	}

	info, err := s.auth.OpenSession(r.Context(), user.ID, r.UserAgent(), s.clientIP(r))
	if err != nil {
		return err
	}

	s.audit(r.Context(), store.Entry{
		UserID: user.ID, Action: store.ActionUserRegister,
		TargetKind: "user", TargetID: user.ID, IP: s.clientIP(r),
	})

	s.setSessionCookie(w, info.Token, info.ExpiresAt)
	writeJSON(w, http.StatusCreated, sessionResponse{User: user, ExpiresAt: info.ExpiresAt})
	return nil
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	user, info, err := s.auth.Login(r.Context(), req.Email, req.Password, r.UserAgent(), s.clientIP(r))
	if err != nil {
		// Failed attempts are recorded by email rather than user id, since the
		// account may not exist. This is what makes brute force visible.
		s.audit(r.Context(), store.Entry{
			Action: store.ActionUserLoginFailed, TargetKind: "email",
			TargetID: auth.NormalizeEmail(req.Email), IP: s.clientIP(r),
		})
		return err
	}

	s.audit(r.Context(), store.Entry{
		UserID: user.ID, Action: store.ActionUserLogin,
		TargetKind: "user", TargetID: user.ID, IP: s.clientIP(r),
	})

	s.setSessionCookie(w, info.Token, info.ExpiresAt)
	writeJSON(w, http.StatusOK, sessionResponse{User: user, ExpiresAt: info.ExpiresAt})
	return nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) error {
	if cookie, err := r.Cookie(s.cfg.Auth.CookieName); err == nil {
		if identity, _, rerr := s.auth.Resolve(r.Context(), cookie.Value); rerr == nil && identity != nil {
			s.audit(r.Context(), store.Entry{
				UserID: identity.User.ID, Action: store.ActionUserLogout,
				TargetKind: "user", TargetID: identity.User.ID, IP: s.clientIP(r),
			})
		}
		if err := s.auth.Logout(r.Context(), cookie.Value); err != nil {
			s.log.Warn("could not delete session", "error", err)
		}
	}

	s.clearSessionCookie(w)
	writeNoContent(w)
	return nil
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) error {
	identity := auth.FromContext(r.Context())
	writeJSON(w, http.StatusOK, sessionResponse{
		User:      identity.User,
		ExpiresAt: identity.Session.ExpiresAt,
	})
	return nil
}

type updateMeRequest struct {
	DisplayName string `json:"displayName"`
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) error {
	var req updateMeRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	name := trimTo(req.DisplayName, 120)
	if name == "" {
		return invalidFields(map[string]string{"displayName": "A display name is required."})
	}

	userID := auth.UserID(r.Context())
	if err := s.store.Users.UpdateProfile(r.Context(), userID, name); err != nil {
		return err
	}

	user, err := s.store.Users.ByID(r.Context(), userID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, user)
	return nil
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) error {
	var req changePasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	userID := auth.UserID(r.Context())
	if err := s.auth.ChangePassword(r.Context(), userID, req.CurrentPassword, req.NewPassword); err != nil {
		return err
	}

	s.audit(r.Context(), store.Entry{
		UserID: userID, Action: store.ActionUserPassword,
		TargetKind: "user", TargetID: userID, IP: s.clientIP(r),
	})

	// Changing the password ended every session, including this one.
	s.clearSessionCookie(w)
	writeNoContent(w)
	return nil
}

type userSummary struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

// handleSearchUsers backs the "add a member" picker. It returns only the three
// fields that picker shows, so one project's members cannot be used to read
// account flags for the whole server.
func (s *Server) handleSearchUsers(w http.ResponseWriter, r *http.Request) error {
	prefix := auth.NormalizeEmail(r.URL.Query().Get("q"))
	if len(prefix) < 3 {
		writeJSON(w, http.StatusOK, []userSummary{})
		return nil
	}

	users, err := s.store.Users.SearchByEmail(r.Context(), prefix, 20)
	if err != nil {
		return err
	}

	out := make([]userSummary, 0, len(users))
	for _, u := range users {
		out = append(out, userSummary{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
