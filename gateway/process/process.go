package process

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/config"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
)

type PostgresPool interface {
	Ping(context.Context) error
	Close()
}

type AccountManager interface {
	Start(context.Context) error
	AccountIDs() []domain.AccountID
	Close() error
}

type OrderCoordinator interface {
	Start(context.Context, []domain.AccountID) error
	Close(context.Context) error
}

type Dependencies struct {
	CheckCredentialResolver  func(context.Context) error
	ResolveSecret            func(context.Context, secret.Ref) (string, error)
	ConnectPostgres          func(context.Context, string) (PostgresPool, error)
	ValidateDatabaseIdentity func(context.Context, PostgresPool) error
	Migrate                  func(context.Context, PostgresPool) error
	BuildAccountManager      func(context.Context) (AccountManager, error)
	RestoreAccountManager    func(context.Context, PostgresPool, AccountManager) error
	BuildOrderCoordinator    func(context.Context, PostgresPool, AccountManager) (OrderCoordinator, error)
	Listen                   func(network, address string) (net.Listener, error)
	HandlerFactory           func(*health.Monitor, PostgresPool, AccountManager, OrderCoordinator) (http.Handler, error)
}

type Service struct {
	mu        sync.Mutex
	monitor   *health.Monitor
	listener  net.Listener
	server    *http.Server
	pool      PostgresPool
	accounts  AccountManager
	orders    OrderCoordinator
	serveDone chan error
	cleanup   cleanupState
}

type cleanupState struct {
	server   bool
	listener bool
	accounts bool
	orders   bool
	pool     bool
}

func Start(ctx context.Context, file config.File, deps Dependencies) (*Service, error) {
	if err := file.Validate(); err != nil {
		return nil, safeErr("validate config", err)
	}
	if err := validateDeps(deps); err != nil {
		return nil, err
	}
	if deps.CheckCredentialResolver != nil {
		if err := deps.CheckCredentialResolver(ctx); err != nil {
			return nil, safeErr("check credential resolver", err)
		}
	}
	resolvedSecrets, err := resolveAllSecrets(ctx, file, deps.ResolveSecret)
	if err != nil {
		return nil, err
	}
	dsn := resolvedSecrets[file.Postgres.DSNRef.RawForProvider()]
	defer clearSecrets(resolvedSecrets)
	pool, err := deps.ConnectPostgres(ctx, dsn)
	if err != nil {
		return nil, safeErr("connect postgres", err)
	}
	service := &Service{monitor: health.NewMonitor(), pool: pool}
	defer func() {
		if err != nil {
			_ = service.Shutdown(context.Background())
		}
	}()
	if err = pool.Ping(ctx); err != nil {
		return nil, safeErr("ping postgres", err)
	}
	if err = deps.ValidateDatabaseIdentity(ctx, pool); err != nil {
		return nil, safeErr("validate postgres identity", err)
	}
	service.monitor.SetPostgresDependency(poolDependency{pool: pool})
	if err = deps.Migrate(ctx, pool); err != nil {
		return nil, safeErr("migrate postgres", err)
	}
	accountManager, buildErr := deps.BuildAccountManager(ctx)
	if buildErr != nil || accountManager == nil {
		if buildErr == nil {
			buildErr = fmt.Errorf("account manager is nil")
		}
		err = safeErr("build account manager", buildErr)
		return nil, err
	}
	service.accounts = accountManager
	if startErr := service.accounts.Start(ctx); startErr != nil {
		err = safeErr("start account manager", startErr)
		return nil, err
	}
	if restoreErr := deps.RestoreAccountManager(ctx, pool, service.accounts); restoreErr != nil {
		err = safeErr("restore account manager", restoreErr)
		return nil, err
	}
	orderCoordinator, buildOrderErr := deps.BuildOrderCoordinator(ctx, pool, service.accounts)
	if buildOrderErr != nil || orderCoordinator == nil {
		if buildOrderErr == nil {
			buildOrderErr = fmt.Errorf("order coordinator is nil")
		}
		err = safeErr("build order coordinator", buildOrderErr)
		return nil, err
	}
	service.orders = orderCoordinator
	if startErr := service.orders.Start(ctx, service.accounts.AccountIDs()); startErr != nil {
		err = safeErr("start order coordinator", startErr)
		return nil, err
	}
	var handler http.Handler
	if deps.HandlerFactory != nil {
		handler, err = deps.HandlerFactory(service.monitor, service.pool, service.accounts, service.orders)
		if err != nil || handler == nil {
			if err == nil {
				err = fmt.Errorf("HTTP handler is nil")
			}
			return nil, safeErr("build HTTP handler", err)
		}
	}
	listener, err := deps.Listen("tcp", file.Server.Listen)
	if err != nil {
		return nil, safeErr("listen", err)
	}
	service.listener = &closeOnceListener{Listener: listener}
	if handler != nil {
		service.server = &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
		}
		service.serveDone = make(chan error, 1)
		go func() {
			serveErr := service.server.Serve(service.listener)
			if errors.Is(serveErr, http.ErrServerClosed) {
				serveErr = nil
			} else if serveErr != nil {
				serveErr = safeErr("serve", serveErr)
			}
			service.serveDone <- serveErr
		}()
	}
	return service, nil
}

func (service *Service) Health() *health.Monitor {
	return service.monitor
}

func (service *Service) Done() <-chan error {
	if service == nil {
		return nil
	}
	return service.serveDone
}

func (service *Service) Shutdown(ctx context.Context) error {
	service.mu.Lock()
	defer service.mu.Unlock()

	if service.monitor != nil {
		service.monitor.SetPostgresHealthy(false)
	}
	var errs []error
	if !service.cleanup.server {
		if service.server == nil {
			service.cleanup.server = true
		} else if err := service.server.Shutdown(ctx); err != nil {
			errs = append(errs, safeErr("shutdown http", err))
		} else {
			service.cleanup.server = true
		}
	}
	if !service.cleanup.listener {
		if service.listener == nil {
			service.cleanup.listener = true
		} else if err := service.listener.Close(); err != nil {
			errs = append(errs, safeErr("close listener", err))
		} else {
			service.cleanup.listener = true
		}
	}
	if !service.cleanup.orders {
		if service.orders == nil {
			service.cleanup.orders = true
		} else if err := service.orders.Close(ctx); err != nil {
			errs = append(errs, safeErr("close order coordinator", err))
		} else {
			service.cleanup.orders = true
		}
	}
	if !service.cleanup.accounts && service.cleanup.orders {
		if service.accounts == nil {
			service.cleanup.accounts = true
		} else if err := service.accounts.Close(); err != nil {
			errs = append(errs, safeErr("close account manager", err))
		} else {
			service.cleanup.accounts = true
		}
	}
	if !service.cleanup.pool && service.readyToClosePool() {
		if service.pool == nil {
			service.cleanup.pool = true
		} else {
			service.pool.Close()
			service.cleanup.pool = true
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (service *Service) readyToClosePool() bool {
	if !service.cleanup.server || !service.cleanup.listener || !service.cleanup.orders || !service.cleanup.accounts {
		return false
	}
	return true
}

func validateDeps(deps Dependencies) error {
	if deps.ResolveSecret == nil || deps.ConnectPostgres == nil || deps.ValidateDatabaseIdentity == nil || deps.Migrate == nil || deps.BuildAccountManager == nil || deps.RestoreAccountManager == nil || deps.BuildOrderCoordinator == nil || deps.Listen == nil {
		return fmt.Errorf("process dependencies are incomplete")
	}
	return nil
}

func clearSecrets(values map[string]string) {
	for key := range values {
		values[key] = ""
		delete(values, key)
	}
}

func resolveAllSecrets(ctx context.Context, file config.File, resolve func(context.Context, secret.Ref) (string, error)) (map[string]string, error) {
	refs := file.SecretRefs()
	values := make(map[string]string, len(refs))
	for _, ref := range refs {
		value, err := resolve(ctx, ref)
		if err != nil {
			return nil, safeErr("resolve secret", err)
		}
		values[ref.RawForProvider()] = value
	}
	return values, nil
}

func safeErr(stage string, _ error) error {
	return fmt.Errorf("%s failed", stage)
}

type poolDependency struct {
	pool PostgresPool
}

func (dependency poolDependency) Healthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return dependency.pool != nil && dependency.pool.Ping(ctx) == nil
}

type closeOnceListener struct {
	net.Listener
	mu     sync.Mutex
	closed bool
}

func (listener *closeOnceListener) Close() error {
	listener.mu.Lock()
	defer listener.mu.Unlock()
	if listener.closed {
		return nil
	}
	err := listener.Listener.Close()
	if err == nil || errors.Is(err, net.ErrClosed) {
		listener.closed = true
		return nil
	}
	return err
}
