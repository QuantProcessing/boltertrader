package account

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/config"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
)

var (
	ErrUnknownAccount  = errors.New("unknown account")
	ErrProductMismatch = errors.New("account product mismatch")
	ErrManagerClosed   = errors.New("account manager closed")
)

const (
	ReasonCapabilityNotPromoted  = "capability_not_promoted"
	ReasonClientUnavailable      = "client_unavailable"
	ReasonExactLookupUnavailable = "exact_lookup_unavailable"
	ReasonManagerClosed          = "manager_closed"
	ReasonPrivateStreamGap       = "private_stream_gap"
	ReasonPrivateStreamStarting  = "private_stream_starting"
)

type Options struct {
	Secrets         secret.Provider
	ExchangeCatalog exchange.InstrumentCatalog
	GatewayCatalog  *catalog.Service
	HTTPClient      *http.Client
	RefreshInterval time.Duration
}

type Resource struct {
	AccountID   domain.AccountID     `json:"account_id"`
	Venue       exchange.Venue       `json:"venue"`
	Product     exchange.Product     `json:"product"`
	Environment exchange.Environment `json:"environment"`
	Settlement  string               `json:"settlement,omitempty"`
	ReadReady   bool                 `json:"read_ready"`
	WriteReady  bool                 `json:"write_ready"`
	Reason      string               `json:"reason,omitempty"`
}

type Status struct {
	Resource
	ClientReady bool           `json:"client_ready"`
	Catalog     catalog.Status `json:"catalog"`
}

type RefreshResult struct {
	AccountID domain.AccountID
	Snapshot  catalog.Snapshot
	Err       error
}

type Manager struct {
	mu              sync.RWMutex
	entries         map[domain.AccountID]*entry
	order           []domain.AccountID
	catalog         *catalog.Service
	closed          bool
	closeMu         sync.Mutex
	cleanup         map[domain.AccountID]bool
	refreshInterval time.Duration
	refreshCancel   context.CancelFunc
	refreshDone     chan struct{}
	started         bool
}

type entry struct {
	resource            Resource
	capability          capability.Row
	handle              clientHandle
	privateStreamReady  bool
	privateStreamReason string
}

type clientHandle interface {
	refresh(context.Context) ([]exchange.Instrument, error)
	close() error
	spot() (exchange.SpotClient, bool)
	perp() (exchange.PerpClient, bool)
	exactLookupReady() bool
}

type clientBuilder func(context.Context, config.AccountConfig, Options) (clientHandle, error)

func NewManager(ctx context.Context, accounts []config.AccountConfig, options Options) (*Manager, error) {
	if options.Secrets == nil || options.ExchangeCatalog == nil || options.GatewayCatalog == nil || options.HTTPClient == nil {
		return nil, fmt.Errorf("account manager options are incomplete")
	}
	return newManager(ctx, accounts, options, buildFactoryClient)
}

func newManager(ctx context.Context, accounts []config.AccountConfig, options Options, build clientBuilder) (*Manager, error) {
	if ctx == nil {
		return nil, fmt.Errorf("account manager context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("account manager startup canceled")
	}
	if options.GatewayCatalog == nil || build == nil {
		return nil, fmt.Errorf("account manager dependencies are incomplete")
	}
	manager := &Manager{
		entries:         make(map[domain.AccountID]*entry, len(accounts)),
		order:           make([]domain.AccountID, 0, len(accounts)),
		catalog:         options.GatewayCatalog,
		cleanup:         make(map[domain.AccountID]bool, len(accounts)),
		refreshInterval: options.RefreshInterval,
	}
	if manager.refreshInterval <= 0 {
		manager.refreshInterval = 2 * time.Minute
	}
	scopes := make(map[domain.AccountID]catalog.Scope, len(accounts))
	for _, accountConfig := range accounts {
		accountID := domain.AccountID(accountConfig.AccountID)
		if err := accountID.Validate(); err != nil {
			manager.closeBuilt()
			return nil, fmt.Errorf("build account manager failed")
		}
		if _, exists := manager.entries[accountID]; exists {
			manager.closeBuilt()
			return nil, fmt.Errorf("build account manager failed")
		}
		scope, row, err := accountContract(accountConfig)
		if err != nil {
			return nil, fmt.Errorf("build account manager failed")
		}
		scopes[accountID] = scope
		manager.entries[accountID] = &entry{
			resource: Resource{
				AccountID: accountID, Venue: scope.Venue, Product: scope.Product,
				Environment: scope.Environment, Settlement: scope.Settlement,
			},
			capability:         row,
			privateStreamReady: true,
		}
		manager.order = append(manager.order, accountID)
	}
	for index, accountConfig := range accounts {
		accountID := manager.order[index]
		handle, err := build(ctx, accountConfig, options)
		if err != nil || handle == nil {
			manager.closeBuilt()
			return nil, fmt.Errorf("build account manager failed")
		}
		manager.entries[accountID].handle = handle
	}
	if err := manager.catalog.RegisterAccounts(scopes); err != nil {
		manager.closeBuilt()
		return nil, fmt.Errorf("build account manager failed")
	}
	return manager, nil
}

func (manager *Manager) Spot(accountID domain.AccountID) (exchange.SpotClient, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closed {
		return nil, ErrManagerClosed
	}
	entry, ok := manager.entries[accountID]
	if !ok {
		return nil, ErrUnknownAccount
	}
	client, ok := entry.handle.spot()
	if !ok {
		return nil, ErrProductMismatch
	}
	return client, nil
}

func (manager *Manager) Perp(accountID domain.AccountID) (exchange.PerpClient, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closed {
		return nil, ErrManagerClosed
	}
	entry, ok := manager.entries[accountID]
	if !ok {
		return nil, ErrUnknownAccount
	}
	client, ok := entry.handle.perp()
	if !ok {
		return nil, ErrProductMismatch
	}
	return client, nil
}

func (manager *Manager) Refresh(ctx context.Context, accountID domain.AccountID) (catalog.Snapshot, error) {
	if ctx == nil {
		return catalog.Snapshot{}, fmt.Errorf("refresh account context is required")
	}
	manager.mu.RLock()
	if manager.closed {
		manager.mu.RUnlock()
		return catalog.Snapshot{}, ErrManagerClosed
	}
	entry, ok := manager.entries[accountID]
	if !ok {
		manager.mu.RUnlock()
		return catalog.Snapshot{}, ErrUnknownAccount
	}
	instruments, err := entry.handle.refresh(ctx)
	manager.mu.RUnlock()
	if err != nil {
		manager.catalog.MarkRefreshError(accountID)
		return catalog.Snapshot{}, fmt.Errorf("refresh account instruments failed")
	}
	snapshot, err := manager.catalog.Publish(accountID, instruments)
	if err != nil {
		return catalog.Snapshot{}, fmt.Errorf("publish account instruments failed")
	}
	return snapshot, nil
}

func (manager *Manager) RefreshAll(ctx context.Context) []RefreshResult {
	manager.mu.RLock()
	accountIDs := append([]domain.AccountID(nil), manager.order...)
	manager.mu.RUnlock()
	results := make([]RefreshResult, len(accountIDs))
	var wait sync.WaitGroup
	for index, accountID := range accountIDs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			snapshot, err := manager.Refresh(ctx, accountID)
			results[index] = RefreshResult{AccountID: accountID, Snapshot: snapshot, Err: err}
		}()
	}
	wait.Wait()
	return results
}

// Start performs the first Catalog load before returning, then owns a bounded
// periodic refresh loop. Per-account failures remain isolated in readiness.
func (manager *Manager) Start(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("account manager context is required")
	}
	manager.closeMu.Lock()
	defer manager.closeMu.Unlock()
	manager.mu.RLock()
	closed := manager.closed
	manager.mu.RUnlock()
	if closed {
		return ErrManagerClosed
	}
	if manager.started {
		return nil
	}
	manager.RefreshAll(ctx)
	if ctx.Err() != nil {
		return fmt.Errorf("initial account refresh canceled")
	}
	loopCtx, cancel := context.WithCancel(ctx)
	manager.refreshCancel = cancel
	manager.refreshDone = make(chan struct{})
	manager.started = true
	go manager.refreshLoop(loopCtx, manager.refreshDone)
	return nil
}

func (manager *Manager) refreshLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(manager.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			manager.RefreshAll(ctx)
		}
	}
}

func (manager *Manager) Instruments(accountID domain.AccountID) (catalog.Snapshot, error) {
	manager.mu.RLock()
	if manager.closed {
		manager.mu.RUnlock()
		return catalog.Snapshot{}, ErrManagerClosed
	}
	_, ok := manager.entries[accountID]
	manager.mu.RUnlock()
	if !ok {
		return catalog.Snapshot{}, ErrUnknownAccount
	}
	return manager.catalog.Snapshot(accountID)
}

func (manager *Manager) Capability(accountID domain.AccountID) (capability.Row, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	entry, ok := manager.entries[accountID]
	if !ok {
		return capability.Row{}, ErrUnknownAccount
	}
	return entry.capability, nil
}

func (manager *Manager) SetPrivateStreamState(accountID domain.AccountID, ready bool, reason string) error {
	if err := accountID.Validate(); err != nil {
		return err
	}
	if ready && reason != "" {
		return fmt.Errorf("ready private stream cannot have a reason")
	}
	if !ready && strings.TrimSpace(reason) == "" {
		return fmt.Errorf("private stream reason is required")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return ErrManagerClosed
	}
	entry, ok := manager.entries[accountID]
	if !ok {
		return ErrUnknownAccount
	}
	entry.privateStreamReady = ready
	entry.privateStreamReason = reason
	return nil
}

func (manager *Manager) Status(accountID domain.AccountID) (Status, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.statusLocked(accountID, true)
}

// RecoveryStatus reports whether the account can perform exact reconciliation
// while deliberately ignoring private-stream continuity. New writes continue
// to use Status and therefore remain blocked until the stream is restored.
func (manager *Manager) RecoveryStatus(accountID domain.AccountID) (Status, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.statusLocked(accountID, false)
}

func (manager *Manager) statusLocked(accountID domain.AccountID, requirePrivateStream bool) (Status, error) {
	entry, ok := manager.entries[accountID]
	if !ok {
		return Status{}, ErrUnknownAccount
	}
	status := Status{Resource: entry.resource, ClientReady: entry.handle != nil, Catalog: manager.catalog.Status(accountID)}
	if manager.closed {
		status.Reason = ReasonManagerClosed
		return status, nil
	}
	if !status.ClientReady {
		status.Reason = ReasonClientUnavailable
		return status, nil
	}
	if !status.Catalog.ReadReady {
		status.Reason = status.Catalog.Reason
		return status, nil
	}
	status.ReadReady = true
	if requirePrivateStream && !entry.privateStreamReady {
		status.Reason = entry.privateStreamReason
		return status, nil
	}
	// No row is promoted for writes until exact reconciliation evidence exists.
	status.WriteReady = entry.capability.WriteReadyDefault
	if !status.WriteReady {
		status.Reason = ReasonCapabilityNotPromoted
		return status, nil
	}
	if !entry.handle.exactLookupReady() {
		status.WriteReady = false
		status.Reason = ReasonExactLookupUnavailable
	}
	return status, nil
}

func (manager *Manager) Accounts() []Resource {
	manager.mu.RLock()
	accountIDs := append([]domain.AccountID(nil), manager.order...)
	manager.mu.RUnlock()
	resources := make([]Resource, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		status, err := manager.Status(accountID)
		if err == nil {
			resources = append(resources, status.Resource)
		}
	}
	sort.SliceStable(resources, func(i, j int) bool { return resources[i].AccountID < resources[j].AccountID })
	return resources
}

func (manager *Manager) AccountIDs() []domain.AccountID {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return append([]domain.AccountID(nil), manager.order...)
}

func (manager *Manager) Resolve(ctx context.Context, accountID domain.AccountID, instrumentID domain.InstrumentID) (exchange.Instrument, error) {
	manager.mu.RLock()
	if manager.closed {
		manager.mu.RUnlock()
		return exchange.Instrument{}, ErrManagerClosed
	}
	_, ok := manager.entries[accountID]
	manager.mu.RUnlock()
	if !ok {
		return exchange.Instrument{}, ErrUnknownAccount
	}
	return manager.catalog.Resolve(ctx, accountID, instrumentID)
}

func (manager *Manager) Close() error {
	manager.closeMu.Lock()
	defer manager.closeMu.Unlock()
	manager.mu.Lock()
	manager.closed = true
	manager.mu.Unlock()
	if manager.refreshCancel != nil {
		manager.refreshCancel()
	}
	if manager.refreshDone != nil {
		<-manager.refreshDone
		manager.refreshDone = nil
	}
	var failed bool
	for index := len(manager.order) - 1; index >= 0; index-- {
		accountID := manager.order[index]
		if manager.cleanup[accountID] {
			continue
		}
		if err := manager.entries[accountID].handle.close(); err != nil {
			failed = true
			continue
		}
		manager.cleanup[accountID] = true
	}
	if failed {
		return fmt.Errorf("close account clients failed")
	}
	return nil
}

func (manager *Manager) closeBuilt() {
	for index := len(manager.order) - 1; index >= 0; index-- {
		handle := manager.entries[manager.order[index]].handle
		if handle != nil {
			_ = handle.close()
		}
	}
}

func (manager *Manager) String() string {
	if manager == nil {
		return "gateway/account.Manager{nil}"
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return fmt.Sprintf("gateway/account.Manager{accounts:%d, credentials:redacted}", len(manager.entries))
}

func (manager *Manager) GoString() string { return manager.String() }

func accountContract(accountConfig config.AccountConfig) (catalog.Scope, capability.Row, error) {
	scope := catalog.Scope{
		Venue: exchange.Venue(accountConfig.Venue), Environment: exchange.Environment(accountConfig.Environment),
		Product: exchange.Product(accountConfig.Product), Settlement: accountConfig.Settlement,
	}
	for _, row := range capability.StaticMatrix().Rows {
		if row.Venue == accountConfig.Venue && row.Product == accountConfig.Product && row.Settlement == accountConfig.Settlement {
			return scope, row, nil
		}
	}
	return catalog.Scope{}, capability.Row{}, fmt.Errorf("unsupported account profile")
}
