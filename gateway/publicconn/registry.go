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

type Connector interface {
	Refresh(context.Context) ([]exchange.Instrument, error)
	Close() error
}

type Lease interface {
	ConnectorID() string
	Snapshot() (catalog.Snapshot, error)
	Release()
}

type Registry struct {
	mu      sync.Mutex
	entries map[routeprofile.Key]*entry
	factory Factory
	trusted *routeprofile.Registry
}

type entry struct {
	key            routeprofile.Key
	id             string
	connector      Connector
	refCount       int
	catalog        *catalog.Service
	accountID      domain.AccountID
	refreshing     bool
	refreshDone    chan struct{}
	refreshWaiters int
}

type lease struct {
	once     sync.Once
	registry *Registry
	key      routeprofile.Key
	entry    *entry
}

func NewRegistry(factory Factory, trusted *routeprofile.Registry) (*Registry, error) {
	if factory == nil {
		return nil, ErrConnectorUnavailable
	}
	if trusted == nil {
		trusted = routeprofile.DefaultRegistry()
	}
	return &Registry{entries: make(map[routeprofile.Key]*entry), factory: factory, trusted: trusted}, nil
}

func (registry *Registry) Acquire(ctx context.Context, key routeprofile.Key) (Lease, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrConnectorUnavailable
	}
	profile, err := registry.trusted.Resolve(key.Mode, key.Venue, key.RouteProfileID)
	if err != nil {
		return nil, ErrConnectorUnavailable
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if existing := registry.entries[key]; existing != nil {
		existing.refCount++
		return &lease{registry: registry, key: key, entry: existing}, nil
	}
	connector, err := registry.factory.Build(ctx, key)
	if err != nil || connector == nil {
		return nil, ErrConnectorUnavailable
	}
	id := connectorID(key)
	service, err := catalog.NewService(2*time.Minute, time.Now)
	if err != nil {
		_ = connector.Close()
		return nil, ErrConnectorUnavailable
	}
	accountID := domain.AccountID(id)
	scope := catalog.Scope{
		Venue: exchange.Venue(profile.Venue), Environment: exchange.Environment(profile.NativeEnvironment),
		Product: exchange.Product(profile.Product), Settlement: profile.SettlementAsset,
	}
	if err := service.RegisterAccount(accountID, scope); err != nil {
		_ = connector.Close()
		return nil, ErrConnectorUnavailable
	}
	created := &entry{key: key, id: id, connector: connector, refCount: 1, catalog: service, accountID: accountID}
	registry.entries[key] = created
	return &lease{registry: registry, key: key, entry: created}, nil
}

func (registry *Registry) Refresh(ctx context.Context, key routeprofile.Key) (catalog.Snapshot, error) {
	registry.mu.Lock()
	current := registry.entries[key]
	if current == nil {
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

func (lease *lease) Release() {
	lease.once.Do(func() {
		lease.registry.mu.Lock()
		defer lease.registry.mu.Unlock()
		current := lease.registry.entries[lease.key]
		if current != lease.entry {
			return
		}
		current.refCount--
		if current.refCount == 0 {
			delete(lease.registry.entries, lease.key)
			_ = current.connector.Close()
		}
	})
}

func (registry *Registry) Close() error {
	registry.mu.Lock()
	entries := registry.entries
	registry.entries = make(map[routeprofile.Key]*entry)
	registry.mu.Unlock()
	var closeErr error
	for _, current := range entries {
		if err := current.connector.Close(); err != nil {
			closeErr = errors.Join(closeErr, ErrConnectorUnavailable)
		}
	}
	return closeErr
}

func connectorID(key routeprofile.Key) string {
	sum := sha256.Sum256([]byte(string(key.Mode) + "\x00" + key.Venue + "\x00" + key.RouteProfileID))
	return fmt.Sprintf("public-%s", hex.EncodeToString(sum[:8]))
}
