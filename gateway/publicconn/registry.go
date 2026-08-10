package publicconn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

var ErrConnectorUnavailable = errors.New("public connector unavailable")

const (
	defaultCatalogMaxAge   = 2 * time.Minute
	defaultRefreshInterval = time.Minute
)

type Connector interface {
	Refresh(context.Context) ([]exchange.Instrument, error)
	Close() error
}

type Lease interface {
	ConnectorID() string
	Snapshot() (catalog.Snapshot, error)
	Status() catalog.Status
	Resolve(context.Context, domain.InstrumentID) (exchange.Instrument, error)
	Release()
}

type Registry struct {
	mu              sync.Mutex
	entries         map[routeprofile.Key]*entry
	factory         Factory
	trusted         *routeprofile.Registry
	refreshInterval time.Duration
	catalogMaxAge   time.Duration
	now             func() time.Time
	closed          bool
	closeDone       chan struct{}
	closeErr        error
	cleanup         sync.WaitGroup
	cleanupErr      error
}

type entry struct {
	key             routeprofile.Key
	id              string
	connector       Connector
	refCount        int
	catalog         *catalog.Service
	accountID       domain.AccountID
	refreshing      bool
	refreshDone     chan struct{}
	refreshWaiters  int
	refreshCancel   context.CancelFunc
	refreshLoopDone chan struct{}
	closing         bool
	closeDone       chan struct{}
}

type registryOptions struct {
	refreshInterval time.Duration
	catalogMaxAge   time.Duration
	now             func() time.Time
}

type lease struct {
	once     sync.Once
	registry *Registry
	key      routeprofile.Key
	entry    *entry
}

func NewRegistry(factory Factory, trusted *routeprofile.Registry) (*Registry, error) {
	return newRegistry(factory, trusted, registryOptions{
		refreshInterval: defaultRefreshInterval,
		catalogMaxAge:   defaultCatalogMaxAge,
		now:             time.Now,
	})
}

func newRegistry(factory Factory, trusted *routeprofile.Registry, options registryOptions) (*Registry, error) {
	if factory == nil {
		return nil, ErrConnectorUnavailable
	}
	if trusted == nil {
		trusted = routeprofile.DefaultRegistry()
	}
	if options.refreshInterval <= 0 || options.catalogMaxAge <= options.refreshInterval || options.now == nil {
		return nil, ErrConnectorUnavailable
	}
	return &Registry{
		entries:         make(map[routeprofile.Key]*entry),
		factory:         factory,
		trusted:         trusted,
		refreshInterval: options.refreshInterval,
		catalogMaxAge:   options.catalogMaxAge,
		now:             options.now,
	}, nil
}

func (registry *Registry) Acquire(ctx context.Context, key routeprofile.Key) (Lease, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrConnectorUnavailable
	}
	profile, err := registry.trusted.Resolve(key.Mode, key.Venue, key.RouteProfileID)
	if err != nil {
		return nil, ErrConnectorUnavailable
	}
	for {
		registry.mu.Lock()
		if registry.closed {
			registry.mu.Unlock()
			return nil, ErrConnectorUnavailable
		}
		if existing := registry.entries[key]; existing != nil {
			if existing.closing {
				done := existing.closeDone
				registry.mu.Unlock()
				select {
				case <-ctx.Done():
					return nil, ErrConnectorUnavailable
				case <-done:
					continue
				}
			}
			existing.refCount++
			registry.mu.Unlock()
			return &lease{registry: registry, key: key, entry: existing}, nil
		}
		connector, err := registry.factory.Build(ctx, key)
		if err != nil || connector == nil {
			registry.mu.Unlock()
			return nil, ErrConnectorUnavailable
		}
		id := connectorID(key)
		service, err := catalog.NewService(registry.catalogMaxAge, registry.now)
		if err != nil {
			registry.mu.Unlock()
			_ = connector.Close()
			return nil, ErrConnectorUnavailable
		}
		accountID := domain.AccountID(id)
		scope := catalog.Scope{
			Venue: exchange.Venue(profile.Venue), Environment: exchange.Environment(profile.NativeEnvironment),
			Product: exchange.Product(profile.Product), Settlement: profile.SettlementAsset,
		}
		if err := service.RegisterAccount(accountID, scope); err != nil {
			registry.mu.Unlock()
			_ = connector.Close()
			return nil, ErrConnectorUnavailable
		}
		refreshContext, cancelRefresh := context.WithCancel(context.Background())
		created := &entry{
			key: key, id: id, connector: connector, refCount: 1, catalog: service, accountID: accountID,
			refreshCancel: cancelRefresh, refreshLoopDone: make(chan struct{}), closeDone: make(chan struct{}),
		}
		registry.entries[key] = created
		registry.mu.Unlock()
		go registry.runRefreshLoop(refreshContext, created)
		return &lease{registry: registry, key: key, entry: created}, nil
	}
}

func (registry *Registry) runRefreshLoop(ctx context.Context, current *entry) {
	defer close(current.refreshLoopDone)
	ticker := time.NewTicker(registry.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = registry.Refresh(ctx, current.key)
		}
	}
}

func (registry *Registry) Refresh(ctx context.Context, key routeprofile.Key) (catalog.Snapshot, error) {
	if ctx == nil || ctx.Err() != nil {
		return catalog.Snapshot{}, ErrConnectorUnavailable
	}
	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		return catalog.Snapshot{}, ErrConnectorUnavailable
	}
	current := registry.entries[key]
	if current == nil || current.closing {
		registry.mu.Unlock()
		return catalog.Snapshot{}, ErrConnectorUnavailable
	}
	if current.refreshing {
		done := current.refreshDone
		current.refreshWaiters++
		registry.mu.Unlock()
		defer func() {
			registry.mu.Lock()
			current.refreshWaiters--
			registry.mu.Unlock()
		}()
		select {
		case <-ctx.Done():
			return catalog.Snapshot{}, ctx.Err()
		case <-done:
			return current.catalog.Snapshot(current.accountID)
		}
	}
	current.refreshing = true
	current.refreshDone = make(chan struct{})
	done := current.refreshDone
	registry.mu.Unlock()

	instruments, err := current.connector.Refresh(ctx)
	var snapshot catalog.Snapshot
	if err == nil {
		snapshot, err = current.catalog.Publish(current.accountID, instruments)
	} else {
		current.catalog.MarkRefreshError(current.accountID)
	}
	registry.mu.Lock()
	current.refreshing = false
	close(done)
	registry.mu.Unlock()
	if err != nil {
		return catalog.Snapshot{}, ErrConnectorUnavailable
	}
	return snapshot, nil
}

func (lease *lease) ConnectorID() string { return lease.entry.id }

func (lease *lease) Snapshot() (catalog.Snapshot, error) {
	return lease.entry.catalog.Snapshot(lease.entry.accountID)
}

func (lease *lease) Status() catalog.Status {
	return lease.entry.catalog.Status(lease.entry.accountID)
}

func (lease *lease) Resolve(ctx context.Context, instrumentID domain.InstrumentID) (exchange.Instrument, error) {
	return lease.entry.catalog.Resolve(ctx, lease.entry.accountID, instrumentID)
}

func (lease *lease) Release() {
	lease.once.Do(func() {
		lease.registry.mu.Lock()
		current := lease.registry.entries[lease.key]
		if current != lease.entry {
			done := lease.entry.closeDone
			closing := lease.entry.closing
			lease.registry.mu.Unlock()
			if closing {
				<-done
			}
			return
		}
		current.refCount--
		if current.refCount > 0 {
			lease.registry.mu.Unlock()
			return
		}
		current.closing = true
		current.refreshCancel()
		lease.registry.cleanup.Add(1)
		refreshDone := inFlightRefresh(current)
		lease.registry.mu.Unlock()
		lease.registry.finishEntryClose(current, refreshDone)
		lease.registry.mu.Lock()
		if lease.registry.entries[lease.key] == current {
			delete(lease.registry.entries, lease.key)
		}
		lease.registry.mu.Unlock()
		lease.registry.cleanup.Done()
	})
}

func (registry *Registry) Close() error {
	registry.mu.Lock()
	if registry.closed {
		done := registry.closeDone
		registry.mu.Unlock()
		<-done
		registry.mu.Lock()
		err := registry.closeErr
		registry.mu.Unlock()
		return err
	}
	registry.closed = true
	registry.closeDone = make(chan struct{})
	entries := registry.entries
	registry.entries = make(map[routeprofile.Key]*entry)
	refreshes := make(map[*entry]<-chan struct{}, len(entries))
	for _, current := range entries {
		if current.closing {
			continue
		}
		current.closing = true
		current.refreshCancel()
		registry.cleanup.Add(1)
		refreshes[current] = inFlightRefresh(current)
	}
	registry.mu.Unlock()
	for current, refreshDone := range refreshes {
		registry.finishEntryClose(current, refreshDone)
		registry.cleanup.Done()
	}
	registry.cleanup.Wait()
	registry.mu.Lock()
	registry.closeErr = registry.cleanupErr
	close(registry.closeDone)
	closeErr := registry.closeErr
	registry.mu.Unlock()
	return closeErr
}

func inFlightRefresh(current *entry) <-chan struct{} {
	if current.refreshing {
		return current.refreshDone
	}
	return nil
}

func (registry *Registry) finishEntryClose(current *entry, refreshDone <-chan struct{}) {
	<-current.refreshLoopDone
	if refreshDone != nil {
		<-refreshDone
	}
	err := current.connector.Close()
	registry.mu.Lock()
	if err != nil {
		registry.cleanupErr = errors.Join(registry.cleanupErr, ErrConnectorUnavailable)
	}
	close(current.closeDone)
	registry.mu.Unlock()
}

func connectorID(key routeprofile.Key) string {
	sum := sha256.Sum256([]byte(string(key.Mode) + "\x00" + key.Venue + "\x00" + key.RouteProfileID))
	return fmt.Sprintf("public-%s", hex.EncodeToString(sum[:8]))
}
