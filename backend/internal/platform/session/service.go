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
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const DefaultPrincipalID = "browser-session"

const dummyPasswordHash = "$2a$10$7EqJtq98hPqEX7fNZaFWoOHIhi6qMt4u9yjIzhP4sJy.6WjeTi4tC"

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidCSRF        = errors.New("invalid csrf token")
	ErrInvalidSession     = errors.New("invalid session")
)

type Options struct {
	Username     string
	PasswordHash string
	Mode         string
	TTL          time.Duration
	CookieSecure bool
	Now          func() time.Time
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

type record struct {
	principal  Principal
	csrfDigest [sha256.Size]byte
	expiresAt  time.Time
}

type Service struct {
	username     string
	passwordHash string
	mode         string
	ttl          time.Duration
	cookieSecure bool
	now          func() time.Time

	mu       sync.Mutex
	sessions map[[sha256.Size]byte]record
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
	return &Service{
		username: username, passwordHash: options.PasswordHash, mode: options.Mode,
		ttl: options.TTL, cookieSecure: options.CookieSecure, now: now,
		sessions: make(map[[sha256.Size]byte]record),
	}, nil
}

func (service *Service) Login(_ context.Context, username, password string) (Result, error) {
	usernameMatches := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(username)), []byte(service.username)) == 1
	hash := service.passwordHash
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
	principal := Principal{ID: DefaultPrincipalID, Mode: service.mode, Username: service.username}
	service.mu.Lock()
	service.sessions[sha256.Sum256([]byte(token))] = record{
		principal: principal, csrfDigest: sha256.Sum256([]byte(csrf)), expiresAt: expiresAt,
	}
	service.mu.Unlock()
	return Result{Principal: principal, Session: Session{Token: token, CSRFToken: csrf, ExpiresAt: expiresAt}}, nil
}

func (service *Service) Current(_ context.Context, token string) (Result, error) {
	digest := sha256.Sum256([]byte(token))
	service.mu.Lock()
	stored, ok := service.sessions[digest]
	if !ok || !service.now().Before(stored.expiresAt) {
		delete(service.sessions, digest)
		service.mu.Unlock()
		return Result{}, ErrInvalidSession
	}
	service.mu.Unlock()
	return Result{
		Principal: stored.principal,
		Session:   Session{Token: token, CSRFToken: csrfToken(token), ExpiresAt: stored.expiresAt},
	}, nil
}

func (service *Service) RequireCSRF(ctx context.Context, token, csrf string) error {
	if _, err := service.Current(ctx, token); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(token))
	candidate := sha256.Sum256([]byte(csrf))
	service.mu.Lock()
	stored, ok := service.sessions[digest]
	service.mu.Unlock()
	if !ok || subtle.ConstantTimeCompare(candidate[:], stored.csrfDigest[:]) != 1 {
		return ErrInvalidCSRF
	}
	return nil
}

func (service *Service) Logout(ctx context.Context, token, csrf string) error {
	if err := service.RequireCSRF(ctx, token, csrf); err != nil {
		return err
	}
	service.mu.Lock()
	delete(service.sessions, sha256.Sum256([]byte(token)))
	service.mu.Unlock()
	return nil
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
