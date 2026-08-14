package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const DefaultPrincipalID = "browser-session"

// CookieName is where the browser carries its session token. It lives here
// rather than in a transport package because every surface that reads a
// session has to agree on it, and two of them now sit in different packages.
const CookieName = "bt_session"

const dummyPasswordHash = "$2a$10$7EqJtq98hPqEX7fNZaFWoOHIhi6qMt4u9yjIzhP4sJy.6WjeTi4tC"

var (
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrInvalidCSRF         = errors.New("invalid csrf token")
	ErrInvalidSession      = errors.New("invalid session")
	ErrIdentityUnavailable = errors.New("identity store unavailable")
)

type Options struct {
	Username     string
	PasswordHash string
	Mode         string
	TTL          time.Duration
	CookieSecure bool
	Now          func() time.Time
	Store        Store
	Identities   IdentityStore
}

type Principal struct {
	ID       string `json:"id"`
	Mode     string `json:"mode"`
	Username string `json:"username"`
}

type Session struct {
	Token     string    `json:"-"`
	CSRFToken string    `json:"csrfToken"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Result struct {
	Principal Principal `json:"principal"`
	Session   Session   `json:"session"`
}

type StoredSession struct {
	TokenDigest string
	Principal   Principal
	CSRFDigest  string
	ExpiresAt   time.Time
}

type Store interface {
	Save(context.Context, StoredSession) error
	Get(context.Context, string) (StoredSession, error)
	Delete(context.Context, string) error
}

type Identity struct {
	PrincipalID  string
	Username     string
	PasswordHash string
	Mode         string
}

type IdentityStore interface {
	FindByUsername(context.Context, string, string) (Identity, error)
}

type Service struct {
	username     string
	passwordHash string
	mode         string
	ttl          time.Duration
	cookieSecure bool
	now          func() time.Time

	store      Store
	identities IdentityStore
}

func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password is required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

func New(options Options) (*Service, error) {
	username := strings.TrimSpace(options.Username)
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}
	if _, err := bcrypt.Cost([]byte(options.PasswordHash)); err != nil {
		return nil, fmt.Errorf("password hash must be a bcrypt hash")
	}
	if options.Mode != "test" && options.Mode != "live" {
		return nil, fmt.Errorf("invalid mode %q", options.Mode)
	}
	if options.TTL <= 0 {
		return nil, fmt.Errorf("ttl must be positive")
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	store := options.Store
	if store == nil {
		store = NewMemoryStore()
	}
	return &Service{
		username: username, passwordHash: options.PasswordHash, mode: options.Mode,
		ttl: options.TTL, cookieSecure: options.CookieSecure, now: now,
		store: store, identities: options.Identities,
	}, nil
}

func (service *Service) Login(ctx context.Context, username, password string) (Result, error) {
	requestedUsername := strings.TrimSpace(username)
	principalID := DefaultPrincipalID
	configuredUsername := service.username
	hash := service.passwordHash
	if service.identities != nil {
		identity, err := service.identities.FindByUsername(ctx, service.mode, requestedUsername)
		switch {
		case err == nil:
			configuredUsername, hash, principalID = identity.Username, identity.PasswordHash, identity.PrincipalID
		case errors.Is(err, ErrNotFound):
			hash = dummyPasswordHash
		default:
			return Result{}, ErrIdentityUnavailable
		}
	}
	usernameMatches := subtle.ConstantTimeCompare([]byte(requestedUsername), []byte(configuredUsername)) == 1
	if !usernameMatches {
		hash = dummyPasswordHash
	}
	passwordMatches := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	if !usernameMatches || !passwordMatches {
		return Result{}, ErrInvalidCredentials
	}
	token, err := randomToken()
	if err != nil {
		return Result{}, err
	}
	csrf := csrfToken(token)
	expiresAt := service.now().Add(service.ttl).UTC()
	principal := Principal{ID: principalID, Mode: service.mode, Username: configuredUsername}
	if err := service.store.Save(ctx, StoredSession{
		TokenDigest: digest(token), Principal: principal, CSRFDigest: digest(csrf), ExpiresAt: expiresAt,
	}); err != nil {
		return Result{}, fmt.Errorf("persist session: %w", err)
	}
	return Result{Principal: principal, Session: Session{Token: token, CSRFToken: csrf, ExpiresAt: expiresAt}}, nil
}

func (service *Service) Current(ctx context.Context, token string) (Result, error) {
	tokenDigest := digest(token)
	stored, err := service.store.Get(ctx, tokenDigest)
	if err != nil || !service.now().Before(stored.ExpiresAt) {
		_ = service.store.Delete(ctx, tokenDigest)
		return Result{}, ErrInvalidSession
	}
	return Result{
		Principal: stored.Principal,
		Session:   Session{Token: token, CSRFToken: csrfToken(token), ExpiresAt: stored.ExpiresAt},
	}, nil
}

func (service *Service) RequireCSRF(ctx context.Context, token, csrf string) error {
	if _, err := service.Current(ctx, token); err != nil {
		return err
	}
	stored, err := service.store.Get(ctx, digest(token))
	if err != nil || subtle.ConstantTimeCompare([]byte(digest(csrf)), []byte(stored.CSRFDigest)) != 1 {
		return ErrInvalidCSRF
	}
	return nil
}

func (service *Service) Logout(ctx context.Context, token, csrf string) error {
	if err := service.RequireCSRF(ctx, token, csrf); err != nil {
		return err
	}
	return service.store.Delete(ctx, digest(token))
}

func (service *Service) CookieSecure() bool { return service.cookieSecure }

func csrfToken(token string) string {
	digest := sha256.Sum256([]byte("boltertrader.platform.csrf.v1:" + token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
