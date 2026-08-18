package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestServiceSingleUserLifecycleAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	passwordHash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	service, err := New(Options{
		Username:     "owner",
		PasswordHash: passwordHash,
		TTL:          time.Hour,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := service.Login(context.Background(), "owner", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v, want ErrInvalidCredentials", err)
	}
	login, err := service.Login(context.Background(), " owner ", "correct horse battery staple")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if login.Principal.ID != DefaultPrincipalID {
		t.Fatalf("principal = %#v", login.Principal)
	}
	if login.Session.Token == "" || login.Session.CSRFToken == "" || login.Session.Token == login.Session.CSRFToken {
		t.Fatalf("invalid session tokens: %#v", login.Session)
	}

	current, err := service.Current(context.Background(), login.Session.Token)
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if current.Session.CSRFToken != login.Session.CSRFToken {
		t.Fatalf("csrf token changed: got %q want %q", current.Session.CSRFToken, login.Session.CSRFToken)
	}
	if err := service.RequireCSRF(context.Background(), login.Session.Token, "wrong"); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("wrong csrf error = %v, want ErrInvalidCSRF", err)
	}
	if err := service.Logout(context.Background(), login.Session.Token, login.Session.CSRFToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := service.Current(context.Background(), login.Session.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked session error = %v, want ErrInvalidSession", err)
	}

	expiring, err := service.Login(context.Background(), "owner", "correct horse battery staple")
	if err != nil {
		t.Fatalf("second Login() error = %v", err)
	}
	now = now.Add(time.Hour)
	if _, err := service.Current(context.Background(), expiring.Session.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired session error = %v, want ErrInvalidSession", err)
	}
}

func TestNewRejectsIncompleteOrUnsafeOptions(t *testing.T) {
	passwordHash, err := HashPassword("password verifier")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	valid := Options{
		Username:     "owner",
		PasswordHash: passwordHash,
		TTL:          time.Hour,
	}
	for name, mutate := range map[string]func(*Options){
		"username":      func(options *Options) { options.Username = "" },
		"password hash": func(options *Options) { options.PasswordHash = "" },
		"ttl":           func(options *Options) { options.TTL = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			options := valid
			mutate(&options)
			if _, err := New(options); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestServicePersistsOnlyDigestsAcrossInstances(t *testing.T) {
	passwordHash, _ := HashPassword("password verifier")
	store := NewMemoryStore()
	options := Options{Username: "owner", PasswordHash: passwordHash, TTL: time.Hour, Store: store}
	first, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	login, err := first.Login(context.Background(), "owner", "password verifier")
	if err != nil {
		t.Fatal(err)
	}
	if store.ContainsRaw(login.Session.Token) || store.ContainsRaw(login.Session.CSRFToken) {
		t.Fatal("session store persisted a raw bearer or CSRF token")
	}
	second, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Current(context.Background(), login.Session.Token); err != nil {
		t.Fatalf("Current after service restart: %v", err)
	}
}

func TestServiceAuthenticatesPersistedIdentity(t *testing.T) {
	configuredHash, _ := HashPassword("unused bootstrap password")
	persistedHash, _ := HashPassword("persisted password")
	identities := NewMemoryIdentityStore(Identity{PrincipalID: "principal_1", Username: "persisted-owner", PasswordHash: persistedHash})
	service, err := New(Options{Username: "bootstrap", PasswordHash: configuredHash, TTL: time.Hour, Identities: identities})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Login(context.Background(), "persisted-owner", "persisted password")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.Principal.ID != "principal_1" || result.Principal.Username != "persisted-owner" {
		t.Fatalf("principal = %#v", result.Principal)
	}
	if _, err := service.Login(context.Background(), "missing", "persisted password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("missing identity error = %v", err)
	}
}

type failingIdentityStore struct{}

func (failingIdentityStore) FindByUsername(context.Context, string) (Identity, error) {
	return Identity{}, errors.New("database unavailable with private detail")
}

func TestServiceDistinguishesIdentityStoreFailureFromBadCredentials(t *testing.T) {
	hash, _ := HashPassword("bootstrap password")
	service, err := New(Options{Username: "bootstrap", PasswordHash: hash, TTL: time.Hour, Identities: failingIdentityStore{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Login(context.Background(), "owner", "password"); !errors.Is(err, ErrIdentityUnavailable) || strings.Contains(err.Error(), "private detail") {
		t.Fatalf("Login error = %v", err)
	}
}
