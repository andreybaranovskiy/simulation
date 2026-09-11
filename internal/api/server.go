package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/blobstore"
	"github.com/andreybaranovskiy/simulation/internal/config"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/runner"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

// Version is stamped at build time with -ldflags and reported by /api/health.
var Version = "dev"

// Server holds everything the handlers need. It is created once at startup.
type Server struct {
	cfg   config.Config
	store *store.Store
	auth  *auth.Service
	blobs *blobstore.Store
	log   *slog.Logger

	// dispatcher is nil when the simulation engine could not be started, which
	// is recoverable: the rest of the API still works and the run endpoints
	// say plainly that the engine is unavailable.
	dispatcher *runner.Dispatcher

	static http.Handler
}

func NewServer(cfg config.Config, st *store.Store, authSvc *auth.Service, blobs *blobstore.Store,
	dispatcher *runner.Dispatcher, log *slog.Logger) *Server {

	s := &Server{
		cfg: cfg, store: st, auth: authSvc, blobs: blobs,
		dispatcher: dispatcher, log: log,
	}
	s.static = newStaticHandler(cfg.Server.WebRoot, log)
	return s
}

// Handler builds the full routing tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public endpoints.
	mux.HandleFunc("GET /api/health", s.wrap(s.handleHealth))
	mux.HandleFunc("GET /api/auth/config", s.wrap(s.handleAuthConfig))
	mux.HandleFunc("POST /api/auth/register", s.wrap(s.handleRegister))
	mux.HandleFunc("POST /api/auth/login", s.wrap(s.handleLogin))
	mux.HandleFunc("POST /api/auth/logout", s.wrap(s.handleLogout))

	// Authenticated endpoints.
	authed := func(h handlerFunc) http.HandlerFunc {
		return s.wrap(s.requireUser(h))
	}
	mux.HandleFunc("GET /api/auth/me", authed(s.handleMe))
	mux.HandleFunc("PATCH /api/auth/me", authed(s.handleUpdateMe))
	mux.HandleFunc("POST /api/auth/password", authed(s.handleChangePassword))
	mux.HandleFunc("GET /api/users/search", authed(s.handleSearchUsers))

	// The template set is a property of the build, not of a project, and the
	// model editor needs it before a project exists.
	mux.HandleFunc("GET /api/templates", authed(s.handleListTemplates))
	mux.HandleFunc("GET /api/templates/{templateKey}", authed(s.handleGetTemplate))
	mux.HandleFunc("POST /api/models/validate", authed(s.handleValidateModel))

	mux.HandleFunc("GET /api/projects", authed(s.handleListProjects))
	mux.HandleFunc("POST /api/projects", authed(s.handleCreateProject))

	// Project-scoped endpoints resolve membership once, in requireProject.
	project := func(minimum model.Role, h handlerFunc) http.HandlerFunc {
		return s.wrap(s.requireUser(s.requireProject(minimum, h)))
	}
	mux.HandleFunc("GET /api/projects/{projectID}", project(model.RoleViewer, s.handleGetProject))
	mux.HandleFunc("PATCH /api/projects/{projectID}", project(model.RoleEditor, s.handleUpdateProject))
	mux.HandleFunc("DELETE /api/projects/{projectID}", project(model.RoleOwner, s.handleDeleteProject))
	mux.HandleFunc("POST /api/projects/{projectID}/archive", project(model.RoleOwner, s.handleArchiveProject))
	mux.HandleFunc("POST /api/projects/{projectID}/restore", project(model.RoleOwner, s.handleRestoreProject))

	mux.HandleFunc("GET /api/projects/{projectID}/members", project(model.RoleViewer, s.handleListMembers))
	mux.HandleFunc("PUT /api/projects/{projectID}/members", project(model.RoleOwner, s.handleSetMember))
	mux.HandleFunc("DELETE /api/projects/{projectID}/members/{userID}", project(model.RoleOwner, s.handleRemoveMember))

	mux.HandleFunc("GET /api/projects/{projectID}/assets", project(model.RoleViewer, s.handleListAssets))
	mux.HandleFunc("POST /api/projects/{projectID}/assets", project(model.RoleEditor, s.handleUploadAsset))
	mux.HandleFunc("GET /api/projects/{projectID}/assets/{assetID}", project(model.RoleViewer, s.handleGetAsset))
	mux.HandleFunc("GET /api/projects/{projectID}/assets/{assetID}/content", project(model.RoleViewer, s.handleDownloadAsset))
	mux.HandleFunc("DELETE /api/projects/{projectID}/assets/{assetID}", project(model.RoleEditor, s.handleDeleteAsset))

	mux.HandleFunc("GET /api/projects/{projectID}/plans", project(model.RoleViewer, s.handleListSitePlans))
	mux.HandleFunc("POST /api/projects/{projectID}/plans", project(model.RoleEditor, s.handleCreateSitePlan))
	mux.HandleFunc("GET /api/projects/{projectID}/plans/{planID}", project(model.RoleViewer, s.handleGetSitePlan))
	mux.HandleFunc("PUT /api/projects/{projectID}/plans/{planID}", project(model.RoleEditor, s.handleUpdateSitePlan))
	mux.HandleFunc("DELETE /api/projects/{projectID}/plans/{planID}", project(model.RoleEditor, s.handleDeleteSitePlan))

	mux.HandleFunc("GET /api/projects/{projectID}/models", project(model.RoleViewer, s.handleListModels))
	mux.HandleFunc("POST /api/projects/{projectID}/models", project(model.RoleEditor, s.handleCreateModel))
	mux.HandleFunc("GET /api/projects/{projectID}/models/{modelID}", project(model.RoleViewer, s.handleGetModel))
	mux.HandleFunc("PATCH /api/projects/{projectID}/models/{modelID}", project(model.RoleEditor, s.handleUpdateModel))
	mux.HandleFunc("DELETE /api/projects/{projectID}/models/{modelID}", project(model.RoleEditor, s.handleDeleteModel))

	mux.HandleFunc("GET /api/projects/{projectID}/scenarios", project(model.RoleViewer, s.handleListScenarios))
	mux.HandleFunc("POST /api/projects/{projectID}/scenarios", project(model.RoleEditor, s.handleCreateScenario))
	mux.HandleFunc("GET /api/projects/{projectID}/scenarios/{scenarioID}", project(model.RoleViewer, s.handleGetScenario))
	mux.HandleFunc("PATCH /api/projects/{projectID}/scenarios/{scenarioID}", project(model.RoleEditor, s.handleUpdateScenario))
	mux.HandleFunc("POST /api/projects/{projectID}/scenarios/{scenarioID}/duplicate", project(model.RoleEditor, s.handleDuplicateScenario))
	mux.HandleFunc("POST /api/projects/{projectID}/scenarios/{scenarioID}/archive", project(model.RoleEditor, s.handleArchiveScenario))
	mux.HandleFunc("DELETE /api/projects/{projectID}/scenarios/{scenarioID}", project(model.RoleEditor, s.handleDeleteScenario))

	// Starting a run is an editor action; watching one is not.
	mux.HandleFunc("POST /api/projects/{projectID}/scenarios/{scenarioID}/run", project(model.RoleEditor, s.handleStartRun))

	mux.HandleFunc("GET /api/projects/{projectID}/runs", project(model.RoleViewer, s.handleListRuns))
	mux.HandleFunc("GET /api/projects/{projectID}/runs/{runID}", project(model.RoleViewer, s.handleGetRun))
	mux.HandleFunc("GET /api/projects/{projectID}/runs/{runID}/kpis", project(model.RoleViewer, s.handleRunKPIs))
	mux.HandleFunc("POST /api/projects/{projectID}/runs/{runID}/cancel", project(model.RoleEditor, s.handleCancelRun))
	mux.HandleFunc("DELETE /api/projects/{projectID}/runs/{runID}", project(model.RoleEditor, s.handleDeleteRun))

	// The viewer streams a run's artifacts through here rather than from a
	// directory IIS can reach, so results stay behind the same membership
	// check as everything else.
	mux.HandleFunc("GET /api/projects/{projectID}/runs/{runID}/artifacts/{path...}",
		project(model.RoleViewer, s.handleRunArtifact))

	// Importing a pre-computed animation creates its model, scenario and run in
	// one step, because there are no choices to make in between.
	mux.HandleFunc("POST /api/projects/{projectID}/imports/animation", project(model.RoleEditor, s.handleImportAnimation))

	// Comparison is a read of several scenarios at once, so it sits beside
	// them rather than under any one of them.
	mux.HandleFunc("GET /api/projects/{projectID}/compare", project(model.RoleViewer, s.handleCompare))

	mux.HandleFunc("GET /api/projects/{projectID}/events", project(model.RoleViewer, s.handleRunEvents))

	// Administration.
	admin := func(h handlerFunc) http.HandlerFunc {
		return s.wrap(s.requireUser(s.requireAdmin(h)))
	}
	mux.HandleFunc("GET /api/admin/users", admin(s.handleAdminListUsers))
	mux.HandleFunc("POST /api/admin/users", admin(s.handleAdminCreateUser))
	mux.HandleFunc("PATCH /api/admin/users/{userID}", admin(s.handleAdminUpdateUser))
	mux.HandleFunc("POST /api/admin/users/{userID}/password", admin(s.handleAdminSetPassword))
	mux.HandleFunc("GET /api/admin/audit", admin(s.handleAdminAuditLog))

	// Anything not under /api is the single-page app.
	mux.Handle("/", s.static)

	return s.withMiddleware(mux)
}

// withMiddleware wraps the router in the cross-cutting concerns, outermost
// first: panic recovery, request logging, then security headers.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return s.recoverPanics(s.logRequests(s.securityHeaders(next)))
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				// http.ErrAbortHandler is the documented way to abandon a
				// response; it is not a bug and must propagate.
				if p == http.ErrAbortHandler {
					panic(p)
				}
				s.log.Error("panic in handler",
					"method", r.Method, "path", r.URL.Path, "panic", p,
					"stack", stackTrace())
				writeJSON(w, http.StatusInternalServerError, ErrorBody{
					Error: "Something went wrong on the server.",
					Code:  "internal",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		// Static asset noise would drown the log; only API traffic is recorded.
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			return
		}
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.written,
			"duration", time.Since(start).Round(time.Millisecond),
			"ip", s.clientIP(r))
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "same-origin")
		if s.cfg.Auth.SecureCookies {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// requireUser rejects anonymous requests and attaches the identity.
func (s *Server) requireUser(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		identity, err := s.identify(w, r)
		if err != nil {
			return err
		}
		if identity == nil {
			return unauthorized("")
		}
		return next(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
	}
}

func (s *Server) requireAdmin(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if !auth.IsAdmin(r.Context()) {
			return forbidden("That area is for administrators.")
		}
		return next(w, r)
	}
}

// requireProject resolves the caller's role in the addressed project and
// enforces the minimum. A non-member gets 404, not 403, so project ids cannot
// be probed for existence.
func (s *Server) requireProject(minimum model.Role, next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		projectID := r.PathValue("projectID")
		if projectID == "" {
			return badRequest("A project id is required.")
		}

		userID := auth.UserID(r.Context())
		role, err := s.store.Projects.RoleFor(r.Context(), projectID, userID)
		if err != nil {
			// Administrators can reach any project, but only after the normal
			// membership lookup misses, so the common path stays one query.
			if isNotFound(err) && auth.IsAdmin(r.Context()) {
				if _, aerr := s.store.Projects.ByIDAdmin(r.Context(), projectID); aerr == nil {
					role = model.RoleOwner
				} else {
					return notFound("That project")
				}
			} else if isNotFound(err) {
				return notFound("That project")
			} else {
				return err
			}
		}

		if !role.AtLeast(minimum) {
			return forbidden("You need " + string(minimum) + " access for that.")
		}
		return next(w, r.WithContext(auth.WithProjectRole(r.Context(), role)))
	}
}

// identify resolves the session cookie. It also refreshes the cookie when the
// session's expiry was extended, so an active user is never logged out mid-work.
func (s *Server) identify(w http.ResponseWriter, r *http.Request) (*auth.Identity, error) {
	cookie, err := r.Cookie(s.cfg.Auth.CookieName)
	if err != nil {
		return nil, nil
	}

	identity, extended, err := s.auth.Resolve(r.Context(), cookie.Value)
	if err != nil {
		return nil, internal(err)
	}
	if identity == nil {
		// Clear a stale cookie so the browser stops sending it.
		s.clearSessionCookie(w)
		return nil, nil
	}
	if extended {
		s.setSessionCookie(w, cookie.Value, identity.Session.ExpiresAt)
	}
	return identity, nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.Auth.CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.Auth.SecureCookies,
		// Lax still sends the cookie on top-level navigation, which is what a
		// user following a link to a report needs, while blocking the
		// cross-site POSTs that CSRF relies on.
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.Auth.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.Auth.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

// clientIP reads the proxy header only when the deployment says a trusted
// proxy is in front; otherwise it uses the socket address.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.Server.TrustProxyHeaders {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first, _, _ := strings.Cut(fwd, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// audit records an action, logging rather than failing when the write fails:
// losing an audit row must not lose the user's work.
func (s *Server) audit(ctx context.Context, e store.Entry) {
	if err := s.store.Audit.Record(ctx, e); err != nil {
		s.log.Warn("could not write audit entry", "action", e.Action, "error", err)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

// Flush and Unwrap keep streaming responses (progress events, large artifact
// reads) working through the wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
