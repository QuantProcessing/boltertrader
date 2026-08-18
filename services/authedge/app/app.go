// Package app assembles the auth edge.
//
// The auth edge is the browser's only entry point and the only process that
// understands a session cookie. It does two things: it terminates the session,
// and it forwards everything else to the service that owns it. There is no
// third responsibility here — no business logic, no durable state beyond
// sessions and identities, no knowledge of what an order is.
package app

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/services/authedge/httpapi"
	"github.com/QuantProcessing/boltertrader/services/authedge/proxy"
	"github.com/QuantProcessing/boltertrader/services/authedge/session"
	authedgepostgres "github.com/QuantProcessing/boltertrader/services/authedge/store/postgres"
)

// Config is what the process supplies. Secrets arrive as bytes already read
// from a protected file rather than as paths, so this package never touches the
// filesystem and a test can build one without one.
type Config struct {
	ListenAddr        string
	BootstrapUsername string
	// BootstrapPasswordHash is a bcrypt hash. The plaintext never reaches this
	// process in any form.
	BootstrapPasswordHash string
	SessionTTL            time.Duration
	// SecureCookie marks the session cookie Secure. It used to be derived from
	// the mode — live meant a real deployment behind TLS, test meant loopback
	// plaintext — and with one mode left there is nothing to derive it from:
	// this process terminates plaintext HTTP either way and cannot see the
	// scheme the browser used. So the operator asserts it, the same way they
	// assert the two non-loopback allowances.
	SecureCookie bool

	TradingURL   string
	TradingToken []byte
	GatewayURL   string
	GatewayToken []byte
}

// App is a running auth edge.
type App struct {
	listenAddr   string
	server       *http.Server
	listener     net.Listener
	startOnce    sync.Once
	shutdownOnce sync.Once
}

// New builds the edge against an already-open sessions database. A nil database
// falls back to in-memory session storage, which is what the tests use; a
// deployment that did that would drop every session on restart, so the process
// requires one.
func New(cfg Config, database *sql.DB) (*App, error) {
	sessions, err := newSessionService(cfg, database)
	if err != nil {
		return nil, err
	}
	forwarder, err := newProxy(cfg, sessions)
	if err != nil {
		return nil, err
	}
	handler, err := httpapi.New(httpapi.Options{
		Sessions:        sessions,
		ContractVersion: httpapi.ContractVersion,
		Downstream:      forwarder.Register,
	})
	if err != nil {
		return nil, err
	}
	return &App{
		listenAddr: cfg.ListenAddr,
		server: &http.Server{
			Addr:    cfg.ListenAddr,
			Handler: handler,
			// Only the header deadline is set. A body deadline would cut off the
			// event stream this edge forwards, and a write deadline would cut it
			// off from the other side.
			ReadHeaderTimeout: 10 * time.Second,
		},
	}, nil
}

func newSessionService(cfg Config, database *sql.DB) (*session.Service, error) {
	var store session.Store = session.NewMemoryStore()
	var identities session.IdentityStore
	if database != nil {
		store = authedgepostgres.NewSessionStore(database)
		persistent := authedgepostgres.NewIdentityStore(database)
		// The bootstrap operator is seeded on every start rather than once. The
		// hash comes from configuration, so a rotated one has to overwrite the
		// stored row or the old password would keep working.
		identity := session.Identity{
			PrincipalID:  session.DefaultPrincipalID,
			Username:     cfg.BootstrapUsername,
			PasswordHash: cfg.BootstrapPasswordHash,
		}
		if err := persistent.Upsert(context.Background(), identity); err != nil {
			return nil, fmt.Errorf("seed bootstrap identity: %w", err)
		}
		identities = persistent
	}
	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return session.New(session.Options{
		Username:     cfg.BootstrapUsername,
		PasswordHash: cfg.BootstrapPasswordHash,
		TTL:          ttl,
		// A cookie sent over plaintext is a session handed to anyone on the
		// path, and this process cannot tell whether it is behind TLS. See
		// Config.SecureCookie for why the operator decides.
		CookieSecure: cfg.SecureCookie,
		Store:        store,
		Identities:   identities,
	})
}

func newProxy(cfg Config, sessions *session.Service) (*proxy.Proxy, error) {
	tradingURL, err := url.Parse(cfg.TradingURL)
	if err != nil {
		return nil, fmt.Errorf("parse trading URL: %w", err)
	}
	gatewayURL, err := url.Parse(cfg.GatewayURL)
	if err != nil {
		return nil, fmt.Errorf("parse gateway URL: %w", err)
	}
	return proxy.New(proxy.Options{
		Trading:  proxy.Upstream{BaseURL: tradingURL, Token: string(cfg.TradingToken)},
		Gateway:  proxy.Upstream{BaseURL: gatewayURL, Token: string(cfg.GatewayToken)},
		Sessions: sessions,
	})
}

// Handler exposes the assembled surface for tests that drive it without a
// listener.
func (a *App) Handler() http.Handler { return a.server.Handler }

// Addr reports the address actually bound, which is not cfg.ListenAddr when the
// configured port was 0.
func (a *App) Addr() string {
	if a.listener == nil {
		return a.listenAddr
	}
	return a.listener.Addr().String()
}

// Start serves until ctx is cancelled, then shuts down.
func (a *App) Start(ctx context.Context) error {
	var err error
	a.startOnce.Do(func() {
		a.listener, err = net.Listen("tcp", a.listenAddr)
		if err != nil {
			err = fmt.Errorf("listen on auth edge address: %w", err)
			return
		}
		go func() { _ = a.server.Serve(a.listener) }()
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.Shutdown(shutdownCtx)
}

func (a *App) Shutdown(ctx context.Context) error {
	var first error
	a.shutdownOnce.Do(func() {
		if err := a.server.Shutdown(ctx); err != nil {
			first = err
		}
		if a.listener != nil {
			_ = a.listener.Close()
		}
	})
	return first
}
