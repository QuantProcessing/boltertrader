package session

import (
	"context"
	"errors"
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
		Mode:         "test",
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
	if login.Principal.ID != DefaultPrincipalID || login.Principal.Mode != "test" {
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
		Mode:         "test",
		TTL:          time.Hour,
	}
	for name, mutate := range map[string]func(*Options){
		"username":      func(options *Options) { options.Username = "" },
		"password hash": func(options *Options) { options.PasswordHash = "" },
		"mode":          func(options *Options) { options.Mode = "demo" },
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
