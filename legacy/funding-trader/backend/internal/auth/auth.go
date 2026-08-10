package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const DefaultPrincipalID = "principal-default"

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidSession     = errors.New("invalid session")
	ErrInvalidCSRF        = errors.New("invalid csrf")
)

const dummyPasswordHash = "$2a$10$7EqJtq98hPqEX7fNZaFWoOHIhi6qMt4u9yjIzhP4sJy.6WjeTi4tC"

type User struct {
	ID           string
	Email        string
	PasswordHash string
	LastLoginAt  *time.Time
	CreatedAt    time.Time
}

type Principal struct {
	ID          string
	Email       string
	Mode        string
	LastLoginAt *time.Time
}

type Session struct {
	Token     string
	CSRFToken string
	ExpiresAt time.Time
}

type LoginResult struct {
	Principal Principal
	Session   Session
}

type CurrentSessionResult struct {
	Principal Principal
	Session   Session
}

type StoredSession struct {
	ID                string
	UserID            string
	TokenDigest       string
	CSRFDigest        string
	ExpiresAt         time.Time
	RevokedAt         *time.Time
	CreatedAt         time.Time
	LastAuthenticated time.Time
	Mode              string
}

type AuditEvent struct {
	RequestID   string
	PrincipalID string
	Mode        string
	Action      string
	Outcome     string
	Email       string
	TargetID    string
	ExecutionID string
	CreatedAt   time.Time
}

type Store interface {
	UserByEmail(context.Context, string) (*User, error)
	UserByID(context.Context, string) (*User, error)
	UserCount(context.Context) (int, error)
	EnsureBootstrapUser(context.Context, User) error
	CreateUser(context.Context, User) error
	UpdateLastLogin(context.Context, string, time.Time) error
	RevokeSessionsForUser(context.Context, string, time.Time) error
	CreateSession(context.Context, StoredSession) error
	RecordLoginSuccess(context.Context, string, StoredSession, time.Time, AuditEvent) (*User, error)
	SessionByTokenDigest(context.Context, string) (*StoredSession, error)
	RevokeSession(context.Context, string, time.Time) error
	AppendAudit(context.Context, AuditEvent) error
}

type Service struct {
	store        Store
	mode         string
	ttl          time.Duration
	cookieSecure bool
	now          func() time.Time
}

func NewService(store Store, mode string, ttl time.Duration, cookieSecure bool) *Service {
	return &Service{store: store, mode: mode, ttl: ttl, cookieSecure: cookieSecure, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) SetClock(clock func() time.Time) {
	s.now = clock
}

func (s *Service) CookieSecure() bool {
	return s.cookieSecure
}

func (s *Service) Bootstrap(ctx context.Context, email, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	now := s.now()
	return s.store.EnsureBootstrapUser(ctx, User{ID: DefaultPrincipalID, Email: strings.ToLower(strings.TrimSpace(email)), PasswordHash: hash, CreatedAt: now})
}

func (s *Service) Login(ctx context.Context, email, password, requestID string) (LoginResult, error) {
	now := s.now()
	user, err := s.store.UserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if errors.Is(err, ErrInvalidCredentials) {
		_ = CheckPassword(dummyPasswordHash, password)
		_ = s.store.AppendAudit(ctx, AuditEvent{
			RequestID: requestID, Mode: s.mode,
			Action: "auth.login", Outcome: "failure",
			Email: strings.ToLower(strings.TrimSpace(email)), CreatedAt: now,
		})
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}
	if CheckPassword(user.PasswordHash, password) != nil {
		_ = s.store.AppendAudit(ctx, AuditEvent{
			RequestID: requestID, PrincipalID: user.ID, Mode: s.mode,
			Action: "auth.login", Outcome: "failure",
			Email: strings.ToLower(strings.TrimSpace(email)), CreatedAt: now,
		})
		return LoginResult{}, ErrInvalidCredentials
	}
	token, err := randomToken()
	if err != nil {
		return LoginResult{}, err
	}
	sessionID, err := randomSessionID()
	if err != nil {
		return LoginResult{}, err
	}
	csrf := CSRFTokenForSessionToken(token)
	expiresAt := now.Add(s.ttl).UTC()
	session := StoredSession{
		ID: sessionID, UserID: user.ID, TokenDigest: DigestToken(token), CSRFDigest: DigestToken(csrf), ExpiresAt: expiresAt, CreatedAt: now, LastAuthenticated: now,
		Mode: s.mode,
	}
	audit := AuditEvent{
		RequestID: requestID, PrincipalID: user.ID, Mode: s.mode,
		Action: "auth.login", Outcome: "success", Email: user.Email, CreatedAt: now,
	}
	user, err = s.store.RecordLoginSuccess(ctx, user.ID, session, now, audit)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Principal: Principal{ID: user.ID, Email: user.Email, Mode: s.mode, LastLoginAt: user.LastLoginAt}, Session: Session{Token: token, CSRFToken: csrf, ExpiresAt: expiresAt}}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	result, err := s.CurrentSession(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	return result.Principal, nil
}

func (s *Service) CurrentSession(ctx context.Context, token string) (CurrentSessionResult, error) {
	if token == "" {
		return CurrentSessionResult{}, ErrInvalidSession
	}
	session, err := s.store.SessionByTokenDigest(ctx, DigestToken(token))
	if err != nil || session.Mode != s.mode || session.RevokedAt != nil || !s.now().Before(session.ExpiresAt) {
		return CurrentSessionResult{}, ErrInvalidSession
	}
	user, err := s.userByID(ctx, session.UserID)
	if err != nil {
		return CurrentSessionResult{}, ErrInvalidSession
	}
	principal := Principal{ID: user.ID, Email: user.Email, Mode: s.mode, LastLoginAt: user.LastLoginAt}
	return CurrentSessionResult{Principal: principal, Session: Session{Token: token, CSRFToken: CSRFTokenForSessionToken(token), ExpiresAt: session.ExpiresAt.UTC()}}, nil
}

func (s *Service) RequireCSRF(ctx context.Context, token, csrf string) error {
	session, err := s.store.SessionByTokenDigest(ctx, DigestToken(token))
	if err != nil || session.Mode != s.mode || session.RevokedAt != nil || !s.now().Before(session.ExpiresAt) {
		return ErrInvalidSession
	}
	if subtle.ConstantTimeCompare([]byte(session.CSRFDigest), []byte(DigestToken(csrf))) != 1 {
		return ErrInvalidCSRF
	}
	return nil
}

func (s *Service) Logout(ctx context.Context, token, csrf, requestID string) error {
	if token == "" {
		_ = s.store.AppendAudit(ctx, AuditEvent{
			RequestID: requestID, Mode: s.mode,
			Action: "auth.logout", Outcome: "invalid-session", CreatedAt: s.now(),
		})
		return ErrInvalidSession
	}
	session, err := s.store.SessionByTokenDigest(ctx, DigestToken(token))
	if err != nil || session.Mode != s.mode {
		_ = s.store.AppendAudit(ctx, AuditEvent{
			RequestID: requestID, Mode: s.mode,
			Action: "auth.logout", Outcome: "invalid-session", CreatedAt: s.now(),
		})
		return ErrInvalidSession
	}
	auditScope := AuditEvent{PrincipalID: session.UserID, Mode: s.mode}
	if subtle.ConstantTimeCompare([]byte(session.CSRFDigest), []byte(DigestToken(csrf))) != 1 {
		auditScope.RequestID = requestID
		auditScope.Action = "auth.logout"
		auditScope.Outcome = "failure"
		auditScope.CreatedAt = s.now()
		_ = s.store.AppendAudit(ctx, auditScope)
		return ErrInvalidCSRF
	}
	if session.RevokedAt != nil || !s.now().Before(session.ExpiresAt) {
		auditScope.RequestID = requestID
		auditScope.Action = "auth.logout"
		auditScope.Outcome = "already-inactive"
		auditScope.CreatedAt = s.now()
		_ = s.store.AppendAudit(ctx, auditScope)
		return nil
	}
	if err := s.store.RevokeSession(ctx, DigestToken(token), s.now()); err != nil {
		if errors.Is(err, ErrInvalidSession) {
			return nil
		}
		return err
	}
	auditScope.RequestID = requestID
	auditScope.Action = "auth.logout"
	auditScope.Outcome = "success"
	auditScope.CreatedAt = s.now()
	_ = s.store.AppendAudit(ctx, auditScope)
	return nil
}

func (s *Service) AuditSecurityOutcome(ctx context.Context, requestID, action, outcome, _ string) error {
	return s.store.AppendAudit(ctx, AuditEvent{
		RequestID: requestID, Mode: s.mode,
		Action: action, Outcome: outcome, CreatedAt: s.now(),
	})
}

func (s *Service) userByID(ctx context.Context, id string) (*User, error) {
	return s.store.UserByID(ctx, id)
}

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func CheckPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

func DigestToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func CSRFTokenForSessionToken(token string) string {
	sum := sha256.Sum256([]byte("funding-trader.csrf.v1:" + token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomSessionID() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random session id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

type MemoryStore struct {
	mu       sync.Mutex
	users    map[string]User
	sessions map[string]StoredSession
	audit    []AuditEvent
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{users: map[string]User{}, sessions: map[string]StoredSession{}}
}

func (m *MemoryStore) UserByEmail(_ context.Context, email string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.users[strings.ToLower(email)]
	if !ok {
		return nil, ErrInvalidCredentials
	}
	copy := user
	return &copy, nil
}

func (m *MemoryStore) UserByID(_ context.Context, id string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, user := range m.users {
		if user.ID == id {
			copy := user
			return &copy, nil
		}
	}
	return nil, ErrInvalidCredentials
}

func (m *MemoryStore) UserCount(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users), nil
}

func (m *MemoryStore) CreateUser(_ context.Context, user User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[strings.ToLower(user.Email)] = user
	return nil
}

func (m *MemoryStore) EnsureBootstrapUser(_ context.Context, user User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.users) == 0 {
		m.users[strings.ToLower(user.Email)] = user
	}
	return nil
}

func (m *MemoryStore) UpdateLastLogin(_ context.Context, userID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, user := range m.users {
		if user.ID == userID {
			value := at
			user.LastLoginAt = &value
			m.users[key] = user
			return nil
		}
	}
	return ErrInvalidCredentials
}

func (m *MemoryStore) RevokeSessionsForUser(_ context.Context, userID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, session := range m.sessions {
		if session.UserID == userID && session.RevokedAt == nil {
			value := at
			session.RevokedAt = &value
			m.sessions[key] = session
		}
	}
	return nil
}

func (m *MemoryStore) CreateSession(_ context.Context, session StoredSession) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.TokenDigest] = session
	return nil
}

func (m *MemoryStore) RecordLoginSuccess(_ context.Context, userID string, session StoredSession, lastLogin time.Time, audit AuditEvent) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var foundKey string
	var found User
	for key, user := range m.users {
		if user.ID == userID {
			foundKey = key
			found = user
			break
		}
	}
	if foundKey == "" {
		return nil, ErrInvalidCredentials
	}
	for key, existing := range m.sessions {
		if existing.UserID == userID && existing.RevokedAt == nil {
			value := lastLogin
			existing.RevokedAt = &value
			m.sessions[key] = existing
		}
	}
	value := lastLogin
	found.LastLoginAt = &value
	m.users[foundKey] = found
	m.sessions[session.TokenDigest] = session
	m.audit = append(m.audit, audit)
	copy := found
	return &copy, nil
}

func (m *MemoryStore) SessionByTokenDigest(_ context.Context, digest string) (*StoredSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[digest]
	if !ok {
		return nil, ErrInvalidSession
	}
	copy := session
	return &copy, nil
}

func (m *MemoryStore) RevokeSession(_ context.Context, digest string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[digest]
	if !ok {
		return ErrInvalidSession
	}
	session.RevokedAt = &at
	m.sessions[digest] = session
	return nil
}

func (m *MemoryStore) AppendAudit(_ context.Context, event AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, event)
	return nil
}

func (m *MemoryStore) AuditEvents() []AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]AuditEvent, len(m.audit))
	copy(copied, m.audit)
	return copied
}

func (m *MemoryStore) Sessions() []StoredSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]StoredSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		copied = append(copied, session)
	}
	return copied
}
