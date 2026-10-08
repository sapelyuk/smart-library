// Package service holds the use cases of the user service.
//
// Every method takes the caller as a domain.Principal, so an authorisation
// decision is part of the use case and cannot be forgotten by one transport while
// the other remembers it. The package knows nothing about gRPC codes, HTTP status
// numbers or SQL: it speaks domain types and domain errors, and the handler in
// front of it decides how each error is presented.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/user-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/user-service/internal/events"
	"github.com/sapelyuk/smart-library/services/user-service/internal/repository"
	"github.com/sapelyuk/smart-library/services/user-service/internal/security"
)

// Limits of the listing: the default is what a screen shows, the maximum is what
// one request may ask for.
const (
	DefaultListLimit = 20
	MaxListLimit     = 100
)

// Service is the use case layer of the user service.
type Service struct {
	users      repository.UserRepository
	sessions   repository.SessionRepository
	events     events.Publisher
	policy     domain.PasswordPolicy
	hashParams security.Parameters
	sessionTTL time.Duration
	now        func() time.Time
	log        *slog.Logger
}

// Config carries the knobs the composition root decides about.
type Config struct {
	// SessionTTL is how long a bearer token stays valid after a sign in.
	SessionTTL time.Duration

	// PasswordPolicy is the shape a password must have. Zero means the defaults
	// of the domain.
	PasswordPolicy domain.PasswordPolicy

	// HashParameters is the argon2id profile new hashes are made with. Zero
	// means the defaults of the security package; a test overrides it to stay
	// fast.
	HashParameters security.Parameters

	// Now is the clock of the service, nil means time.Now.
	Now func() time.Time

	// Logger receives the operational events of the use cases, nil means
	// slog.Default.
	Logger *slog.Logger
}

// New wires a service. The repositories and the event publisher are required,
// the rest has defaults.
func New(users repository.UserRepository, sessions repository.SessionRepository, publisher events.Publisher, cfg Config) (*Service, error) {
	if users == nil {
		return nil, errors.New("service: user repository is required")
	}

	if sessions == nil {
		return nil, errors.New("service: session repository is required")
	}

	if publisher == nil {
		return nil, errors.New("service: event publisher is required")
	}

	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 24 * time.Hour
	}

	if cfg.PasswordPolicy.MinLength == 0 {
		cfg.PasswordPolicy = domain.DefaultPasswordPolicy()
	}

	if cfg.HashParameters == (security.Parameters{}) {
		cfg.HashParameters = security.DefaultParameters
	}

	if err := cfg.HashParameters.Validate(); err != nil {
		return nil, err
	}

	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Service{
		users:      users,
		sessions:   sessions,
		events:     publisher,
		policy:     cfg.PasswordPolicy,
		hashParams: cfg.HashParameters,
		sessionTTL: cfg.SessionTTL,
		now:        cfg.Now,
		log:        cfg.Logger,
	}, nil
}

// RegisterInput is the self service sign up of a reader.
type RegisterInput struct {
	Email    string
	Password string
	FullName string
	Phone    string
}

// Register creates a reader account.
//
// The role is not part of the input on purpose: whoever registers gets the reader
// role, and only a librarian can raise it afterwards.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*domain.User, error) {
	if err := s.policy.Validate(in.Password); err != nil {
		return nil, err
	}

	hash, err := s.hash(in.Password)
	if err != nil {
		return nil, err
	}

	user, err := s.newUser(in.Email, hash, in.FullName, in.Phone, domain.RoleReader)
	if err != nil {
		return nil, err
	}

	if err := s.users.Create(ctx, user); err != nil {
		return nil, err
	}

	s.log.Info("user registered", "user_id", user.ID, "role", string(user.Role))
	s.publish(ctx, events.TypeUserRegistered, userPayload(user))

	return user, nil
}

// CreateUserInput is the librarian side creation of an account.
type CreateUserInput struct {
	Email    string
	Password string
	FullName string
	Phone    string
	Role     domain.Role
}

// CreateUser creates an account of any role. Creating a librarian is a librarian's
// job, which is how the first one has to come from the environment.
func (s *Service) CreateUser(ctx context.Context, caller domain.Principal, in CreateUserInput) (*domain.User, error) {
	if err := caller.RequireLibrarian("create an account"); err != nil {
		return nil, err
	}

	if err := s.policy.Validate(in.Password); err != nil {
		return nil, err
	}

	role := in.Role
	if role == "" {
		role = domain.RoleReader
	}

	hash, err := s.hash(in.Password)
	if err != nil {
		return nil, err
	}

	user, err := s.newUser(in.Email, hash, in.FullName, in.Phone, role)
	if err != nil {
		return nil, err
	}

	if err := s.users.Create(ctx, user); err != nil {
		return nil, err
	}

	s.log.Info("user created", "user_id", user.ID, "role", string(user.Role), "by", caller.UserID)
	s.publish(ctx, events.TypeUserRegistered, userPayload(user))

	return user, nil
}

// Credentials is the result of a successful sign in.
type Credentials struct {
	// Token is the bearer token. It is returned once and never stored in clear,
	// so losing this response means losing the session.
	Token   string
	Session *domain.Session
	User    *domain.User
}

// Login checks the credentials and issues a bearer token.
//
// A wrong email and a wrong password produce the same error and cost the same
// amount of work: when the account is missing, the password is still verified
// against a dummy hash, so probing the endpoint cannot enumerate the accounts of
// the library by response time.
func (s *Service) Login(ctx context.Context, email, password string) (*Credentials, error) {
	user, err := s.findByEmail(ctx, email)

	switch {
	case errors.Is(err, domain.ErrInvalidEmail), errors.Is(err, domain.ErrUserNotFound):
		security.VerifyDummy(password)

		return nil, domain.ErrInvalidCredentials
	case err != nil:
		return nil, err
	}

	if verifyErr := security.VerifyPassword(password, user.PasswordHash); verifyErr != nil {
		if errors.Is(verifyErr, security.ErrWrongPassword) {
			return nil, domain.ErrInvalidCredentials
		}

		return nil, fmt.Errorf("service: login: %w", verifyErr)
	}

	if !user.IsActive() {
		return nil, domain.ErrDeactivated
	}

	token, err := security.NewToken()
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	session := domain.NewSession(user.ID, security.HashToken(token), s.sessionTTL, now)

	if err := s.sessions.Create(ctx, session); err != nil {
		return nil, fmt.Errorf("service: login: %w", err)
	}

	user.MarkLogin(now)
	user.UpdatedAt = now

	// A password hashed under a profile the service has outgrown is replaced at
	// the only moment the plaintext is available.
	if security.NeedsRehash(user.PasswordHash) {
		if hash, hashErr := s.hash(password); hashErr == nil {
			if setErr := user.SetPasswordHash(hash); setErr != nil {
				return nil, setErr
			}

			s.log.Info("password hash upgraded", "user_id", user.ID)
		}
	}

	if err := s.users.Update(ctx, user); err != nil {
		// The credentials were right and the token is stored; losing the login
		// timestamp is not a reason to refuse the sign in.
		s.log.Warn("user: cannot record the login time", "user_id", user.ID, "error", err)
	}

	s.log.Info("user logged in", "user_id", user.ID, "session_id", session.ID)
	s.publish(ctx, events.TypeUserSessionCreated, sessionPayload(session))

	return &Credentials{Token: token, Session: session, User: user}, nil
}

// Logout revokes every session of the caller.
//
// A token is bound to a device, not to a request, and a reader who signs out of a
// shared computer expects no other tab to survive it.
func (s *Service) Logout(ctx context.Context, caller domain.Principal) error {
	if caller.SessionID == uuid.Nil {
		return domain.ErrUnauthenticated
	}

	if err := s.sessions.DeleteByUser(ctx, caller.UserID); err != nil {
		return err
	}

	s.log.Info("user logged out", "user_id", caller.UserID, "session_id", caller.SessionID)

	return nil
}

// Authenticate turns a bearer token into the principal of the request. It is the
// only path to a principal, so a token that is unknown, expired or bound to a
// blocked account cannot produce one.
//
// AuthenticateToken of the API is the same call: the internal gRPC clients of the
// other services resolve a token this way too.
func (s *Service) Authenticate(ctx context.Context, token string) (domain.Principal, error) {
	principal, _, err := s.AuthenticateSession(ctx, token)

	return principal, err
}

// AuthenticateSession is Authenticate with the session it resolved, which is what
// a caller that has to report the expiry of a token needs. Keeping it separate
// rather than widening Principal means the interceptor never sees a session it has
// no business carrying around.
func (s *Service) AuthenticateSession(ctx context.Context, token string) (domain.Principal, *domain.Session, error) {
	raw := strings.TrimSpace(token)
	if raw == "" {
		return domain.Principal{}, nil, domain.ErrUnauthenticated
	}

	session, err := s.sessions.GetByTokenHash(ctx, security.HashToken(raw))
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			return domain.Principal{}, nil, domain.ErrUnauthenticated
		}

		return domain.Principal{}, nil, fmt.Errorf("service: authenticate: %w", err)
	}

	now := s.now().UTC()
	if session.ExpiredAt(now) {
		// The row goes away on the way out, so an expired token cannot be
		// presented again before the purge job has run.
		if deleteErr := s.sessions.DeleteByTokenHash(ctx, session.TokenHash); deleteErr != nil {
			s.log.Warn("user: cannot drop an expired session", "session_id", session.ID, "error", deleteErr)
		}

		return domain.Principal{}, nil, domain.ErrSessionExpired
	}

	user, err := s.users.GetByID(ctx, session.UserID)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return domain.Principal{}, nil, domain.ErrUnauthenticated
		}

		return domain.Principal{}, nil, fmt.Errorf("service: authenticate: %w", err)
	}

	if !user.IsActive() {
		return domain.Principal{}, nil, domain.ErrDeactivated
	}

	return domain.Principal{
		UserID:    user.ID,
		SessionID: session.ID,
		Email:     user.Email,
		Role:      user.Role,
	}, session, nil
}

// GetCurrentUser returns the account behind the bearer token.
func (s *Service) GetCurrentUser(ctx context.Context, caller domain.Principal) (*domain.User, error) {
	return s.users.GetByID(ctx, caller.UserID)
}

// GetUser returns one account. A reader may ask for own account only.
func (s *Service) GetUser(ctx context.Context, caller domain.Principal, id uuid.UUID) (*domain.User, error) {
	if err := caller.RequireAccountAccess(id); err != nil {
		return nil, err
	}

	return s.users.GetByID(ctx, id)
}

// ListUsers returns one page of the accounts, librarians only.
func (s *Service) ListUsers(ctx context.Context, caller domain.Principal, filter domain.UserFilter) ([]*domain.User, int, error) {
	if err := caller.RequireLibrarian("list the accounts"); err != nil {
		return nil, 0, err
	}

	filter.Limit = normalizeLimit(filter.Limit)
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	return s.users.List(ctx, filter)
}

// UpdateUser changes the profile, the role or the status of an account.
func (s *Service) UpdateUser(ctx context.Context, caller domain.Principal, id uuid.UUID, update domain.UserUpdate) (*domain.User, error) {
	if update.IsEmpty() {
		return nil, fmt.Errorf("%w: nothing to update", domain.ErrPermissionDenied)
	}

	if err := caller.RequireUpdateAccess(id, update); err != nil {
		return nil, err
	}

	user, err := s.users.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// The status carried by an update goes through the same rule as
	// DeactivateUser, so the two paths cannot disagree.
	statusChanged := update.Status != nil && *update.Status != user.Status

	if statusChanged {
		if err := caller.RequireStatusChange(id); err != nil {
			return nil, err
		}
	}

	if err := user.Apply(update); err != nil {
		return nil, err
	}

	user.UpdatedAt = s.now().UTC()

	if err := s.users.Update(ctx, user); err != nil {
		return nil, err
	}

	// An account that went inactive must not keep the sessions issued while it
	// was active.
	if update.Status != nil && !user.IsActive() {
		if err := s.sessions.DeleteByUser(ctx, user.ID); err != nil {
			return nil, err
		}
	}

	s.log.Info("user updated", "user_id", user.ID, "by", caller.UserID)

	if statusChanged {
		s.publish(ctx, events.TypeUserStatusChanged, userPayload(user))
	}

	return user, nil
}

// ChangePassword replaces the password of an account.
//
// byOwner tells the rules apart: the owner proves the old password, a librarian
// resets one without it. Both revoke the other sessions of the account, so a
// stolen token stops working the moment the password changes.
func (s *Service) ChangePassword(
	ctx context.Context,
	caller domain.Principal,
	id uuid.UUID,
	oldPassword, newPassword string,
	byOwner bool,
) (*domain.User, error) {
	if err := caller.RequirePasswordChange(id, byOwner); err != nil {
		return nil, err
	}

	user, err := s.users.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if byOwner {
		if verifyErr := security.VerifyPassword(oldPassword, user.PasswordHash); verifyErr != nil {
			if errors.Is(verifyErr, security.ErrWrongPassword) {
				return nil, domain.ErrInvalidCredentials
			}

			return nil, fmt.Errorf("service: change password: %w", verifyErr)
		}
	}

	if err := s.policy.Validate(newPassword); err != nil {
		return nil, err
	}

	hash, err := s.hash(newPassword)
	if err != nil {
		return nil, err
	}

	if err := user.SetPasswordHash(hash); err != nil {
		return nil, err
	}

	user.UpdatedAt = s.now().UTC()

	if err := s.users.Update(ctx, user); err != nil {
		return nil, err
	}

	// The caller keeps own session when the caller is the owner: the request that
	// just proved the old password should not have to sign in again.
	if err := s.sessions.DeleteByUserExcept(ctx, user.ID, caller.SessionID); err != nil {
		return nil, err
	}

	s.log.Info("password changed", "user_id", user.ID, "by", caller.UserID, "by_owner", byOwner)

	return user, nil
}

// DeactivateUser blocks an account and revokes its sessions.
func (s *Service) DeactivateUser(ctx context.Context, caller domain.Principal, id uuid.UUID) (*domain.User, error) {
	return s.changeStatus(ctx, caller, id, false)
}

// RestoreUser unblocks an account.
func (s *Service) RestoreUser(ctx context.Context, caller domain.Principal, id uuid.UUID) (*domain.User, error) {
	return s.changeStatus(ctx, caller, id, true)
}

// changeStatus is the single path that changes the status of an account, so the
// self lockout rule and the session revocation cannot be applied twice or missed.
func (s *Service) changeStatus(ctx context.Context, caller domain.Principal, id uuid.UUID, activate bool) (*domain.User, error) {
	if err := caller.RequireStatusChange(id); err != nil {
		return nil, err
	}

	user, err := s.users.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if activate {
		user.Restore()
	} else {
		user.Deactivate()
	}

	user.UpdatedAt = s.now().UTC()

	if err := s.users.Update(ctx, user); err != nil {
		return nil, err
	}

	if !activate {
		if err := s.sessions.DeleteByUser(ctx, user.ID); err != nil {
			return nil, err
		}
	}

	s.log.Info("user status changed",
		"user_id", user.ID, "status", string(user.Status), "by", caller.UserID)
	s.publish(ctx, events.TypeUserStatusChanged, userPayload(user))

	return user, nil
}

// PurgeExpiredSessions drops the sessions that outlived their validity. The
// maintenance loop of the composition root runs it on a ticker; the storage is
// asked directly because the service contract deliberately has no idea about
// housekeeping.
func (s *Service) PurgeExpiredSessions(ctx context.Context) error {
	purger, ok := s.sessions.(interface {
		PurgeExpired(context.Context, time.Time) (int64, error)
	})
	if !ok {
		return nil
	}

	deleted, err := purger.PurgeExpired(ctx, s.now().UTC())
	if err != nil {
		return err
	}

	if deleted > 0 {
		s.log.Info("expired sessions purged", "deleted", deleted)
	}

	return nil
}

// findByEmail normalises the address before the lookup: the stored value is the
// normalized one, so the query has to use the same form.
func (s *Service) findByEmail(ctx context.Context, raw string) (*domain.User, error) {
	email, err := domain.ParseEmail(raw)
	if err != nil {
		return nil, err
	}

	return s.users.GetByEmail(ctx, email)
}

// hash produces the stored encoding of a password with the profile of the service.
func (s *Service) hash(password string) (string, error) {
	hash, err := security.HashPasswordWith(s.hashParams, password)
	if err != nil {
		return "", fmt.Errorf("service: hash password: %w", err)
	}

	return hash, nil
}

// newUser builds an entity and stamps the clock of the service on it, so a test
// with a frozen clock gets deterministic timestamps.
func (s *Service) newUser(email, hash, fullName, phone string, role domain.Role) (*domain.User, error) {
	user, err := domain.NewUser(email, hash, fullName, phone, role)
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	user.CreatedAt = now
	user.UpdatedAt = now

	return user, nil
}

// publish sends a domain event. Publishing is best effort: the account is
// already written and a broker that is down must not fail the operation, so the
// error is logged and the use case carries on.
func (s *Service) publish(ctx context.Context, eventType string, payload any) {
	if err := s.events.Publish(ctx, eventType, payload); err != nil {
		s.log.ErrorContext(ctx, "cannot publish a domain event",
			"event_type", eventType, "error", err)
	}
}

// userPayload renders the public fields of an account. The password hash is
// deliberately left out.
func userPayload(user *domain.User) events.UserPayload {
	return events.UserPayload{
		UserID: user.ID.String(),
		Email:  string(user.Email),
		Role:   string(user.Role),
		Status: string(user.Status),
	}
}

// sessionPayload renders the identifiers of a freshly created session. The token
// itself never leaves the response that carried it.
func sessionPayload(session *domain.Session) events.SessionPayload {
	return events.SessionPayload{
		UserID:    session.UserID.String(),
		SessionID: session.ID.String(),
		ExpiresAt: session.ExpiresAt.UTC(),
	}
}

func normalizeLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultListLimit
	case limit > MaxListLimit:
		return MaxListLimit
	default:
		return limit
	}
}
