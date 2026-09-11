package api

import (
	"net/http"

	"github.com/andreybaranovskiy/simulation/internal/auth"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) error {
	includeArchived := queryBool(r.URL.Query().Get("archived"))

	projects, err := s.store.Projects.ListForUser(r.Context(), auth.UserID(r.Context()), includeArchived)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, projects)
	return nil
}

type projectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) error {
	var req projectRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	name := trimTo(req.Name, 160)
	if name == "" {
		return invalidFields(map[string]string{"name": "A project name is required."})
	}

	p := &model.Project{
		Name:        name,
		Description: trimTo(req.Description, 4000),
		OwnerID:     auth.UserID(r.Context()),
	}
	if err := s.store.Projects.Create(r.Context(), p); err != nil {
		return err
	}

	s.audit(r.Context(), store.Entry{
		UserID: p.OwnerID, ProjectID: p.ID, Action: store.ActionProjectCreate,
		TargetKind: "project", TargetID: p.ID, Detail: map[string]string{"name": p.Name},
		IP: s.clientIP(r),
	})

	writeJSON(w, http.StatusCreated, p)
	return nil
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")

	p, err := s.store.Projects.ByID(r.Context(), projectID, auth.UserID(r.Context()))
	if err != nil {
		// An administrator viewing a project they are not a member of still
		// reached this handler, so fall back to the unscoped read.
		if isNotFound(err) && auth.IsAdmin(r.Context()) {
			p, err = s.store.Projects.ByIDAdmin(r.Context(), projectID)
			if err != nil {
				return err
			}
			p.Role = auth.ProjectRole(r.Context())
		} else {
			return err
		}
	}
	writeJSON(w, http.StatusOK, p)
	return nil
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")

	var req projectRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	name := trimTo(req.Name, 160)
	if name == "" {
		return invalidFields(map[string]string{"name": "A project name is required."})
	}

	if err := s.store.Projects.Update(r.Context(), projectID, name, trimTo(req.Description, 4000)); err != nil {
		return err
	}

	s.audit(r.Context(), store.Entry{
		UserID: auth.UserID(r.Context()), ProjectID: projectID, Action: store.ActionProjectUpdate,
		TargetKind: "project", TargetID: projectID, IP: s.clientIP(r),
	})

	p, err := s.store.Projects.ByIDAdmin(r.Context(), projectID)
	if err != nil {
		return err
	}
	p.Role = auth.ProjectRole(r.Context())
	writeJSON(w, http.StatusOK, p)
	return nil
}

func (s *Server) handleArchiveProject(w http.ResponseWriter, r *http.Request) error {
	return s.setProjectArchived(w, r, true, store.ActionProjectArchive)
}

func (s *Server) handleRestoreProject(w http.ResponseWriter, r *http.Request) error {
	return s.setProjectArchived(w, r, false, store.ActionProjectRestore)
}

func (s *Server) setProjectArchived(w http.ResponseWriter, r *http.Request, archived bool, action string) error {
	projectID := r.PathValue("projectID")

	if err := s.store.Projects.SetArchived(r.Context(), projectID, archived); err != nil {
		return err
	}

	s.audit(r.Context(), store.Entry{
		UserID: auth.UserID(r.Context()), ProjectID: projectID, Action: action,
		TargetKind: "project", TargetID: projectID, IP: s.clientIP(r),
	})

	writeNoContent(w)
	return nil
}

// handleDeleteProject removes a project for good. Asset rows cascade in the
// database; the blobs they pointed at are removed here, but only those no
// other project still shares.
func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()

	assets, err := s.store.Assets.List(ctx, projectID, "")
	if err != nil {
		return err
	}

	if err := s.store.Projects.Delete(ctx, projectID); err != nil {
		return err
	}

	for _, a := range assets {
		s.releaseBlob(ctx, a.SHA256, a.StoragePath)
	}

	s.audit(ctx, store.Entry{
		UserID: auth.UserID(ctx), Action: store.ActionProjectDelete,
		TargetKind: "project", TargetID: projectID,
		Detail: map[string]int{"assetsRemoved": len(assets)}, IP: s.clientIP(r),
	})

	writeNoContent(w)
	return nil
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) error {
	members, err := s.store.Projects.Members(r.Context(), r.PathValue("projectID"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, members)
	return nil
}

type setMemberRequest struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

// handleSetMember adds a member or changes their role. It accepts either a
// user id or an email so the UI can invite without a lookup round trip.
func (s *Server) handleSetMember(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	ctx := r.Context()

	var req setMemberRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}

	role, ok := model.ParseRole(req.Role)
	if !ok {
		return invalidFields(map[string]string{"role": "Use owner, editor or viewer."})
	}

	userID := req.UserID
	if userID == "" {
		if req.Email == "" {
			return invalidFields(map[string]string{"email": "Give a user id or an email address."})
		}
		u, err := s.store.Users.ByEmail(ctx, auth.NormalizeEmail(req.Email))
		if err != nil {
			if isNotFound(err) {
				return invalidFields(map[string]string{"email": "No account with that email address."})
			}
			return err
		}
		userID = u.ID
	}

	// Demoting the last owner would leave the project unmanageable.
	if role != model.RoleOwner {
		current, err := s.store.Projects.RoleFor(ctx, projectID, userID)
		if err == nil && current == model.RoleOwner {
			owners, err := s.store.Projects.CountOwners(ctx, projectID)
			if err != nil {
				return err
			}
			if owners <= 1 {
				return conflict("A project must keep at least one owner.")
			}
		}
	}

	if err := s.store.Projects.SetMember(ctx, projectID, userID, role); err != nil {
		if isForeignKey(err) {
			return invalidFields(map[string]string{"userId": "No such account."})
		}
		return err
	}

	s.audit(ctx, store.Entry{
		UserID: auth.UserID(ctx), ProjectID: projectID, Action: store.ActionMemberSet,
		TargetKind: "user", TargetID: userID,
		Detail: map[string]string{"role": string(role)}, IP: s.clientIP(r),
	})

	members, err := s.store.Projects.Members(ctx, projectID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, members)
	return nil
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) error {
	projectID := r.PathValue("projectID")
	userID := r.PathValue("userID")
	ctx := r.Context()

	current, err := s.store.Projects.RoleFor(ctx, projectID, userID)
	if err != nil {
		if isNotFound(err) {
			return notFound("That member")
		}
		return err
	}

	if current == model.RoleOwner {
		owners, err := s.store.Projects.CountOwners(ctx, projectID)
		if err != nil {
			return err
		}
		if owners <= 1 {
			return conflict("A project must keep at least one owner.")
		}
	}

	if err := s.store.Projects.RemoveMember(ctx, projectID, userID); err != nil {
		return err
	}

	s.audit(ctx, store.Entry{
		UserID: auth.UserID(ctx), ProjectID: projectID, Action: store.ActionMemberRemove,
		TargetKind: "user", TargetID: userID, IP: s.clientIP(r),
	})

	writeNoContent(w)
	return nil
}
