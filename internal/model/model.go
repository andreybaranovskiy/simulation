// Package model holds the domain entities shared by the store, API and engine
// layers. Types here are plain data: persistence lives in internal/store and
// transport shaping lives in internal/api.
package model

import (
	"encoding/json"
	"strings"
	"time"
)

// Role is a user's permission level inside one project.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// rank orders roles so permission checks are a comparison rather than a table.
func (r Role) rank() int {
	switch r {
	case RoleOwner:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// Valid reports whether r is one of the three defined roles.
func (r Role) Valid() bool { return r.rank() > 0 }

// AtLeast reports whether r grants everything want grants.
func (r Role) AtLeast(want Role) bool { return r.rank() >= want.rank() }

// CanRead, CanWrite and CanAdmin name the three capability tiers the API
// checks, so handlers read as intent rather than as role arithmetic.
func (r Role) CanRead() bool  { return r.AtLeast(RoleViewer) }
func (r Role) CanWrite() bool { return r.AtLeast(RoleEditor) }
func (r Role) CanAdmin() bool { return r.AtLeast(RoleOwner) }

// ParseRole validates untrusted input from the API.
func ParseRole(s string) (Role, bool) {
	r := Role(strings.ToLower(strings.TrimSpace(s)))
	return r, r.Valid()
}

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	EmailNorm    string    `json:"-"`
	PasswordHash string    `json:"-"`
	DisplayName  string    `json:"displayName"`
	IsAdmin      bool      `json:"isAdmin"`
	CanUploadGo  bool      `json:"canUploadGo"`
	IsActive     bool      `json:"isActive"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Session is a server-side session record. The raw token is never stored: ID
// is its SHA-256, so a database leak does not hand over live sessions.
type Session struct {
	ID         string    `json:"-"`
	UserID     string    `json:"userId"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	UserAgent  string    `json:"userAgent"`
	IP         string    `json:"ip"`
}

func (s Session) Expired(now time.Time) bool { return !now.Before(s.ExpiresAt) }

type Project struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	OwnerID     string     `json:"ownerId"`
	ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`

	// Role is the requesting user's role, filled in by list and get queries.
	Role Role `json:"role,omitempty"`
}

type ProjectMember struct {
	ProjectID   string    `json:"projectId"`
	UserID      string    `json:"userId"`
	Email       string    `json:"email,omitempty"`
	DisplayName string    `json:"displayName,omitempty"`
	Role        Role      `json:"role"`
	CreatedAt   time.Time `json:"createdAt"`
}

// AssetKind classifies an uploaded file. It decides which validations run on
// upload and which pickers the file appears in.
type AssetKind string

const (
	AssetPlanImage     AssetKind = "plan_image"     // PNG/JPEG floor plan or site drawing
	AssetModel3D       AssetKind = "model_3d"       // GLB/glTF environment
	AssetModelSpec     AssetKind = "model_spec"     // declarative simulation model
	AssetAnimationJSON AssetKind = "animation_json" // pre-computed playback, the legacy format
	AssetGoModel       AssetKind = "go_model"       // uploaded Go source, admin-gated
	AssetOther         AssetKind = "other"
)

func (k AssetKind) Valid() bool {
	switch k {
	case AssetPlanImage, AssetModel3D, AssetModelSpec, AssetAnimationJSON, AssetGoModel, AssetOther:
		return true
	}
	return false
}

// ParseAssetKind validates untrusted input from the API.
func ParseAssetKind(s string) (AssetKind, bool) {
	k := AssetKind(strings.ToLower(strings.TrimSpace(s)))
	return k, k.Valid()
}

type Asset struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"projectId"`
	Kind         AssetKind       `json:"kind"`
	OriginalName string          `json:"originalName"`
	ContentType  string          `json:"contentType"`
	SizeBytes    int64           `json:"sizeBytes"`
	SHA256       string          `json:"sha256"`
	StoragePath  string          `json:"-"`
	Meta         json.RawMessage `json:"meta,omitempty"`
	UploadedBy   string          `json:"uploadedBy"`
	CreatedAt    time.Time       `json:"createdAt"`
}

// SitePlan georeferences a plan image: it is what turns pixel coordinates into
// metres, and therefore what makes heatmap cells and report scale bars mean
// something. MetersPerPixel plus OriginPx and RotationDeg define the transform.
type SitePlan struct {
	ID             string  `json:"id"`
	ProjectID      string  `json:"projectId"`
	AssetID        string  `json:"assetId"`
	Name           string  `json:"name"`
	ImageWidth     int     `json:"imageWidth"`
	ImageHeight    int     `json:"imageHeight"`
	MetersPerPixel float64 `json:"metersPerPixel"`
	OriginPxX      float64 `json:"originPxX"`
	OriginPxY      float64 `json:"originPxY"`
	RotationDeg    float64 `json:"rotationDeg"`
	// FlipY is true when image Y grows downward but world Y grows upward,
	// which is the usual case for a raster plan.
	FlipY bool `json:"flipY"`
	// Calibration records how MetersPerPixel was derived (the two picked
	// points and the real-world length typed in), so the UI can redraw and
	// re-edit the measurement instead of only showing its result.
	Calibration json.RawMessage `json:"calibration,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// PixelToWorld converts a point on the plan image to world metres.
func (p SitePlan) PixelToWorld(px, py float64) (x, y float64) {
	dx := (px - p.OriginPxX) * p.MetersPerPixel
	dy := (py - p.OriginPxY) * p.MetersPerPixel
	if p.FlipY {
		dy = -dy
	}
	return rotate(dx, dy, p.RotationDeg)
}

// WorldToPixel is the inverse of PixelToWorld.
func (p SitePlan) WorldToPixel(x, y float64) (px, py float64) {
	dx, dy := rotate(x, y, -p.RotationDeg)
	if p.FlipY {
		dy = -dy
	}
	if p.MetersPerPixel == 0 {
		return p.OriginPxX, p.OriginPxY
	}
	return p.OriginPxX + dx/p.MetersPerPixel, p.OriginPxY + dy/p.MetersPerPixel
}

// AuditEntry records a state-changing action for the admin log.
type AuditEntry struct {
	ID         uint64          `json:"id"`
	At         time.Time       `json:"at"`
	UserID     *string         `json:"userId,omitempty"`
	ProjectID  *string         `json:"projectId,omitempty"`
	Action     string          `json:"action"`
	TargetKind string          `json:"targetKind,omitempty"`
	TargetID   string          `json:"targetId,omitempty"`
	Detail     json.RawMessage `json:"detail,omitempty"`
	IP         string          `json:"ip,omitempty"`
}
