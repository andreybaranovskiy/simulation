package auth

import (
	"context"

	"github.com/andreybaranovskiy/simulation/internal/model"
)

type contextKey struct{ name string }

var (
	identityKey = contextKey{"identity"}
	roleKey     = contextKey{"project-role"}
)

// WithIdentity attaches the authenticated caller to a request context.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// FromContext returns the authenticated caller, or nil for an anonymous request.
func FromContext(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey).(*Identity)
	return id
}

// UserID is a convenience for handlers that only need the caller's id.
func UserID(ctx context.Context) string {
	if id := FromContext(ctx); id != nil && id.User != nil {
		return id.User.ID
	}
	return ""
}

// IsAdmin reports whether the caller is a server administrator.
func IsAdmin(ctx context.Context) bool {
	id := FromContext(ctx)
	return id != nil && id.User != nil && id.User.IsAdmin
}

// WithProjectRole records the role the project middleware resolved, so the
// handler does not repeat the lookup.
func WithProjectRole(ctx context.Context, role model.Role) context.Context {
	return context.WithValue(ctx, roleKey, role)
}

// ProjectRole returns the caller's role in the project being addressed.
func ProjectRole(ctx context.Context) model.Role {
	role, _ := ctx.Value(roleKey).(model.Role)
	return role
}
