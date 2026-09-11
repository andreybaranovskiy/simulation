package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/andreybaranovskiy/simulation/internal/config"
	"github.com/andreybaranovskiy/simulation/internal/model"
	"github.com/andreybaranovskiy/simulation/internal/store"
)

// Provider is the credential backend. Only LocalProvider exists today; a
// Windows Authentication provider would implement the same interface and the
// handlers would not change.
type Provider interface {
	// Authenticate verifies a credential and returns the matching user.
	Authenticate(ctx context.Context, email, password string) (*model.User, error)
	// Name identifies the provider in logs and in the login page.
	Name() string
}

// Identity is the authenticated caller attached to a request context.
type Identity struct {
	User    *model.User
	Session *model.Session
}

// Service ties the provider, session storage and cookie policy together.
type Service struct {
	store    *store.Store
	cfg      config.Auth
	log      *slog.Logger
	provider Provider
}

func NewService(st *store.Store, cfg config.Auth, log *slog.Logger) *Service {
	s := &Service{store: st, cfg: cfg, log: log}
	s.provider = &LocalProvider{store: st, cfg: cfg}
	return s
}

func (s *Service) Config() config.Auth { return s.cfg }

func (s *Service) ProviderName() string { return s.provider.Name() }

var (
	ErrEmailTaken         = errors.New("an account with that email already exists")
	ErrInvalidEmail       = errors.New("that does not look like an email address")
	ErrRegistrationClosed = errors.New("registration is disabled on this server")
)

// Register creates an account. The first account ever created becomes an
// administrator, so a fresh install is usable without a bootstrap password.
func (s *Service) Register(ctx context.Context, email, password, displayName string) (*model.User, error) {
	if !s.cfg.AllowRegistration {
		return nil, ErrRegistrationClosed
	}
	return s.CreateUser(ctx, email, password, displayName, createOptions{PromoteFirstUser: true})
}

type createOptions struct {
	// PromoteFirstUser makes this account an admin when no other exists.
	PromoteFirstUser bool
	// ForceAdmin makes the account an admin unconditionally.
	ForceAdmin bool
}

// CreateUser is the shared path for self-registration, admin-created accounts
// and the startup bootstrap.
func (s *Service) CreateUser(ctx context.Context, email, password, displayName string, opts createOptions) (*model.User, error) {
	email = strings.TrimSpace(email)
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, ErrInvalidEmail
	}

	hash, err := HashPassword(password, s.cfg.BcryptCost)
	if err != nil {
		return nil, err
	}

	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName, _, _ = strings.Cut(email, "@")
	}

	isAdmin := opts.ForceAdmin
	if opts.PromoteFirstUser && !isAdmin {
		n, err := s.store.Users.Count(ctx)
		if err != nil {
			return nil, err
		}
		isAdmin = n == 0
	}

	u := &model.User{
		Email:        email,
		EmailNorm:    NormalizeEmail(email),
		PasswordHash: hash,
		DisplayName:  displayName,
		IsAdmin:      isAdmin,
		IsActive:     true,
	}

	if err := s.store.Users.Create(ctx, u); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	return u, nil
}

// SessionInfo is what Login hands back: the raw token belongs in a cookie and
// is never persisted or logged.
type SessionInfo struct {
	Token     string
	ExpiresAt time.Time
}

// Login verifies credentials and opens a session.
func (s *Service) Login(ctx context.Context, email, password, userAgent, ip string) (*model.User, *SessionInfo, error) {
	u, err := s.provider.Authenticate(ctx, email, password)
	if err != nil {
		return nil, nil, err
	}

	info, err := s.OpenSession(ctx, u.ID, userAgent, ip)
	if err != nil {
		return nil, nil, err
	}
	return u, info, nil
}

// OpenSession issues a session for an already-authenticated user.
func (s *Service) OpenSession(ctx context.Context, userID, userAgent, ip string) (*SessionInfo, error) {
	token, id, err := NewSessionToken()
	if err != nil {
		return nil, err
	}

	expires := time.Now().UTC().Add(s.cfg.SessionTTL).Truncate(time.Millisecond)
	sess := &model.Session{
		ID:        id,
		UserID:    userID,
		ExpiresAt: expires,
		UserAgent: userAgent,
		IP:        ip,
	}
	if err := s.store.Sessions.Create(ctx, sess); err != nil {
		return nil, err
	}
	return &SessionInfo{Token: token, ExpiresAt: expires}, nil
}

// Resolve turns a cookie token into an identity, sliding the expiry when the
// session is past its half-life. A disabled account resolves to nothing, so
// deactivating a user takes effect on their next request.
func (s *Service) Resolve(ctx context.Context, token string) (*Identity, bool, error) {
	if token == "" {
		return nil, false, nil
	}

	sess, user, err := s.store.Sessions.Resolve(ctx, HashToken(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !user.IsActive {
		return nil, false, nil
	}

	newExpiry, extended, err := s.store.Sessions.Touch(ctx, sess.ID, s.cfg.SessionTTL)
	if err != nil {
		// A failed touch must not log the user out: the session is still valid.
		s.log.Warn("could not refresh session", "error", err)
		return &Identity{User: user, Session: sess}, false, nil
	}
	sess.ExpiresAt = newExpiry

	return &Identity{User: user, Session: sess}, extended, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.store.Sessions.Delete(ctx, HashToken(token))
}

// ChangePassword verifies the current password, stores the new one and ends
// every other session for that user.
func (s *Service) ChangePassword(ctx context.Context, userID, current, next string) error {
	u, err := s.store.Users.ByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := CheckPassword(u.PasswordHash, current); err != nil {
		return err
	}
	hash, err := HashPassword(next, s.cfg.BcryptCost)
	if err != nil {
		return err
	}
	if err := s.store.Users.UpdatePasswordHash(ctx, userID, hash); err != nil {
		return err
	}
	return s.store.Sessions.DeleteForUser(ctx, userID)
}

// SetPassword is the admin reset path: no current password required.
func (s *Service) SetPassword(ctx context.Context, userID, next string) error {
	hash, err := HashPassword(next, s.cfg.BcryptCost)
	if err != nil {
		return err
	}
	if err := s.store.Users.UpdatePasswordHash(ctx, userID, hash); err != nil {
		return err
	}
	return s.store.Sessions.DeleteForUser(ctx, userID)
}

// BootstrapAdmin creates the configured administrator when the instance has no
// users yet. It is a no-op on every later start.
func (s *Service) BootstrapAdmin(ctx context.Context) error {
	if s.cfg.BootstrapAdminEmail == "" {
		return nil
	}

	n, err := s.store.Users.Count(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	u, err := s.CreateUser(ctx, s.cfg.BootstrapAdminEmail, s.cfg.BootstrapAdminPassword,
		"Administrator", createOptions{ForceAdmin: true})
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}

	s.log.Info("created bootstrap administrator", "email", u.Email, "id", u.ID)
	return nil
}

// PurgeExpiredSessions is run periodically by the server janitor.
func (s *Service) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	return s.store.Sessions.DeleteExpired(ctx)
}

// LocalProvider authenticates against the password hashes in MySQL.
type LocalProvider struct {
	store *store.Store
	cfg   config.Auth
}

func (p *LocalProvider) Name() string { return "local" }

func (p *LocalProvider) Authenticate(ctx context.Context, email, password string) (*model.User, error) {
	u, err := p.store.Users.ByEmail(ctx, NormalizeEmail(email))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Spend the same work as a real comparison so response timing does
			// not reveal whether the address is registered.
			_ = CheckPassword(dummyHash, password)
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	if err := CheckPassword(u.PasswordHash, password); err != nil {
		return nil, err
	}
	if !u.IsActive {
		return nil, ErrAccountDisabled
	}

	// Transparently upgrade hashes made at an older, cheaper cost.
	if NeedsRehash(u.PasswordHash, p.cfg.BcryptCost) {
		if hash, err := HashPassword(password, p.cfg.BcryptCost); err == nil {
			_ = p.store.Users.UpdatePasswordHash(ctx, u.ID, hash)
		}
	}

	return u, nil
}

// dummyHash is a valid bcrypt hash at cost 12 of a random string, used only to
// equalise timing on unknown accounts.
const dummyHash = "$2a$12$OT5Hc8lbQ0nCLgkqB0Pz1ubNyOD0aCJ1mM5hoFJyE.EkYzpFvj4Aa"

// CreateUserAsAdmin creates an account on an administrator's behalf, bypassing
// the AllowRegistration switch that governs self-service signup.
func (s *Service) CreateUserAsAdmin(ctx context.Context, email, password, displayName string, isAdmin bool) (*model.User, error) {
	return s.CreateUser(ctx, email, password, displayName, createOptions{ForceAdmin: isAdmin})
}
