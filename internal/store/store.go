// Package store is the persistence layer: every SQL statement in the server
// lives here, and callers work in terms of model types and sentinel errors.
package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/andreybaranovskiy/simulation/internal/db"
)

// ErrNotFound is returned instead of sql.ErrNoRows so the API layer maps one
// sentinel to 404 without importing database/sql.
var ErrNotFound = errors.New("not found")

// ErrDuplicate and ErrForeignKey are re-exported so handlers import one package.
var (
	ErrDuplicate  = db.ErrDuplicate
	ErrForeignKey = db.ErrForeignKey
)

// Store bundles the per-entity repositories over one connection pool.
type Store struct {
	DB *db.DB

	Users     *UserStore
	Sessions  *SessionStore
	Projects  *ProjectStore
	Assets    *AssetStore
	SitePlans *SitePlanStore
	Models    *ModelStore
	Scenarios *ScenarioStore
	Runs      *RunStore
	Audit     *AuditStore
}

func New(database *db.DB) *Store {
	s := &Store{DB: database}
	s.Users = &UserStore{db: database}
	s.Sessions = &SessionStore{db: database}
	s.Projects = &ProjectStore{db: database}
	s.Assets = &AssetStore{db: database}
	s.SitePlans = &SitePlanStore{db: database}
	s.Models = &ModelStore{db: database}
	s.Scenarios = &ScenarioStore{db: database}
	s.Runs = &RunStore{db: database}
	s.Audit = &AuditStore{db: database}
	return s
}

// NewID returns the identifier format used for every primary key: a UUIDv4 in
// canonical text form, matching the CHAR(36) columns.
func NewID() string { return uuid.NewString() }

// now returns the current time in UTC truncated to milliseconds, which is the
// precision of the DATETIME(3) columns. Truncating here means a value written
// and then read back compares equal.
func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// mapErr converts driver and sql package errors into the package sentinels.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return db.Translate(err)
}

func strPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

func timePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	v := nt.Time.UTC()
	return &v
}
