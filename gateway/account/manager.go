package account

import (
	"context"
	"crypto/sha256"
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
	"github.com/QuantProcessing/boltertrader/gateway/publicconn"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
)

var (
	ErrUnknownAccount    = errors.New("unknown account")
	ErrProductMismatch   = errors.New("account product mismatch")
	ErrManagerClosed     = errors.New("account manager closed")
	ErrRuntimeExists     = errors.New("runtime account already exists")
	ErrRuntimeBlocked    = errors.New("runtime transition blocked")
	ErrStaleWriteFence   = errors.New("stale runtime write fence")
	ErrLiveWriteDisabled = errors.New("live private writes disabled")
)

const (
	ReasonCapabilityNotPromoted  = "capability_not_promoted"
	ReasonClientUnavailable      = "client_unavailable"
	ReasonExactLookupUnavailable = "exact_lookup_unavailable"
	ReasonManagerClosed          = "manager_closed"
)

type Options struct {
	Secrets         secret.Provider
	ExchangeCatalog exchange.InstrumentCatalog
	GatewayCatalog  *catalog.Service
	HTTPClient      *http.Client
	RefreshInterval time.Duration
	AllowLiveWrites bool

	RuntimeGroupBuilder     RuntimeGroupBuilder
	PublicConnectors        *publicconn.Registry
	RequirePublicConnectors bool
}

type WriteFence struct {
	AccountID domain.AccountID `json:"accountId"`
	RuntimeID string           `json:"runtimeId"`
}

// RuntimeAccount reports the local connection; it is not desired state.
type RuntimeAccount struct {
	AccountID        domain.AccountID `json:"accountId"`
	RuntimeID        string           `json:"runtimeId"`
	Venue            string           `json:"venue"`
	VenueEnvironment string           `json:"venueEnvironment"`
	Ready            bool             `json:"ready"`
	Reason           string           `json:"reason,omitempty"`
	ObservedAt       time.Time        `json:"observedAt"`
}

func (runtime RuntimeAccount) Fence() WriteFence {
	return WriteFence{AccountID: runtime.AccountID, RuntimeID: runtime.RuntimeID}
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

type RuntimeAccountObserver interface {
	RegisterAccount(domain.AccountID) error
	UnregisterAccount(domain.AccountID)
}

type Manager struct {
	mu              sync.RWMutex
	runtimeMu       sync.Mutex
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
	runtimes        map[domain.AccountID]RuntimeAccount
	allowLiveWrites bool

	groupMu                 sync.Mutex
	groups                  map[domain.AccountID]*accountRuntime
	groupBuilder            RuntimeGroupBuilder
	publicConnectors        *publicconn.Registry
	requirePublicConnectors bool
	observerMu              sync.RWMutex
	runtimeObserver         RuntimeAccountObserver
}

type entry struct {
	resource   Resource
	capability capability.Row
	handle     clientHandle
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
	if options.GatewayCatalog == nil || options.HTTPClient == nil ||
		(len(accounts) > 0 && (options.Secrets == nil || options.ExchangeCatalog == nil)) ||
		(options.RequirePublicConnectors && (options.PublicConnectors == nil || options.RuntimeGroupBuilder == nil)) {
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
		runtimes:        make(map[domain.AccountID]RuntimeAccount),
		allowLiveWrites: options.AllowLiveWrites,

		groups:                  make(map[domain.AccountID]*accountRuntime),
		groupBuilder:            options.RuntimeGroupBuilder,
		publicConnectors:        options.PublicConnectors,
		requirePublicConnectors: options.RequirePublicConnectors,
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
			capability: row,
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
	return manager.SpotRoute(accountID, "")
}

// SpotRoute resolves the spot client for one named route. An empty settlement
// asks for "the account's only spot route" and stays hostile to ambiguity the
// way Spot always has; naming one picks it out exactly.
func (manager *Manager) SpotRoute(accountID domain.AccountID, settlement string) (exchange.SpotClient, error) {
	manager.mu.RLock()
	if manager.closed {
		manager.mu.RUnlock()
		return nil, ErrManagerClosed
	}
	entry, ok := manager.entries[accountID]
	if ok {
		client, clientOK := entry.handle.spot()
		manager.mu.RUnlock()
		if !clientOK {
			return nil, ErrProductMismatch
		}
		return client, nil
	}
	manager.mu.RUnlock()
	route, err := manager.productClient(accountID, exchange.ProductSpot, settlement)
	if err != nil {
		return nil, err
	}
	client, ok := route.SpotClient()
	if !ok || client == nil {
		return nil, ErrProductMismatch
	}
	return client, nil
}

func (manager *Manager) Perp(accountID domain.AccountID) (exchange.PerpClient, error) {
	return manager.PerpRoute(accountID, "")
}

// PerpRoute resolves the perpetual client for one named route. Callers that
// already hold a Resource — every reader that walks Accounts() — have its
// settlement and should pass it: an account may run several perpetual routes
// at once (bybit settles in both USDT and USDC), and asking by account alone
// is then an ambiguous question this refuses to answer.
func (manager *Manager) PerpRoute(accountID domain.AccountID, settlement string) (exchange.PerpClient, error) {
	manager.mu.RLock()
	if manager.closed {
		manager.mu.RUnlock()
		return nil, ErrManagerClosed
	}
	entry, ok := manager.entries[accountID]
	if ok {
		client, clientOK := entry.handle.perp()
		manager.mu.RUnlock()
		if !clientOK {
			return nil, ErrProductMismatch
		}
		return client, nil
	}
	manager.mu.RUnlock()
	route, err := manager.productClient(accountID, exchange.ProductPerp, settlement)
	if err != nil {
		return nil, err
	}
	client, ok := route.PerpClient()
	if !ok || client == nil {
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
	if ok {
		return manager.catalog.Snapshot(accountID)
	}
	return manager.dynamicSnapshot(accountID)
}

func (manager *Manager) Capability(accountID domain.AccountID) (capability.Row, error) {
	rows, err := manager.Capabilities(accountID)
	if err != nil {
		return capability.Row{}, err
	}
	if len(rows) != 1 {
		return capability.Row{}, ErrProductMismatch
	}
	return rows[0], nil
}

// RouteCapability answers the same question as Capability for one route of an
// account rather than for the account as a whole. The distinction only matters
// where an account runs more than one route — bybit settles perpetuals in both
// USDT and USDC under a single set of credentials — but there Capability cannot
// answer at all: it sees two rows and reports ErrProductMismatch, which turned
// every funding read on such an account into a 400. Either hint may be empty,
// so a single-route account is still answerable without knowing its shape.
func (manager *Manager) RouteCapability(accountID domain.AccountID, product, settlement string) (capability.Row, error) {
	rows, err := manager.Capabilities(accountID)
	if err != nil {
		return capability.Row{}, err
	}
	var matched capability.Row
	found := false
	for _, row := range rows {
		if product != "" && row.Product != product {
			continue
		}
		if settlement != "" && row.Settlement != settlement {
			continue
		}
		if found {
			return capability.Row{}, ErrProductMismatch
		}
		matched, found = row, true
	}
	if !found {
		return capability.Row{}, ErrProductMismatch
	}
	return matched, nil
}

func (manager *Manager) Capabilities(accountID domain.AccountID) ([]capability.Row, error) {
	manager.mu.RLock()
	entry, ok := manager.entries[accountID]
	if ok {
		manager.mu.RUnlock()
		return []capability.Row{entry.capability}, nil
	}
	manager.mu.RUnlock()
	accesses, err := manager.activeRouteAccesses(accountID)
	if err != nil {
		return nil, err
	}
	rows := make([]capability.Row, 0, len(accesses))
	for _, access := range accesses {
		row, found := routeCapability(access.venue, access.definition.Product, access.definition.SettlementAsset)
		if !found {
			return nil, ErrProductMismatch
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].RowID < rows[j].RowID })
	return rows, nil
}

func (manager *Manager) Status(accountID domain.AccountID) (Status, error) {
	manager.mu.RLock()
	_, static := manager.entries[accountID]
	if static {
		defer manager.mu.RUnlock()
		return manager.statusLocked(accountID, true)
	}
	closed := manager.closed
	manager.mu.RUnlock()
	if closed {
		return Status{}, ErrManagerClosed
	}
	return manager.dynamicStatus(accountID, true)
}

// RecoveryStatus reports whether the account can perform exact reconciliation.
// Private-stream continuity is deliberately independent from write admission.
func (manager *Manager) RecoveryStatus(accountID domain.AccountID) (Status, error) {
	manager.mu.RLock()
	_, static := manager.entries[accountID]
	if static {
		defer manager.mu.RUnlock()
		return manager.statusLocked(accountID, false)
	}
	closed := manager.closed
	manager.mu.RUnlock()
	if closed {
		return Status{}, ErrManagerClosed
	}
	return manager.dynamicStatus(accountID, false)
}

func (manager *Manager) statusLocked(accountID domain.AccountID, _ bool) (Status, error) {
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
	// No row is promoted for writes until exact reconciliation evidence exists.
	status.WriteReady = entry.capability.TradingSupported
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
	manager.groupMu.Lock()
	dynamicIDs := make([]domain.AccountID, 0, len(manager.groups))
	for accountID, runtime := range manager.groups {
		if runtime != nil && runtime.active.Load() != nil {
			dynamicIDs = append(dynamicIDs, accountID)
		}
	}
	manager.groupMu.Unlock()
	for _, accountID := range dynamicIDs {
		statuses, err := manager.dynamicRouteStatuses(accountID, true)
		if err != nil {
			continue
		}
		for _, status := range statuses {
			resources = append(resources, status.Resource)
		}
	}
	sort.SliceStable(resources, func(i, j int) bool {
		if resources[i].AccountID != resources[j].AccountID {
			return resources[i].AccountID < resources[j].AccountID
		}
		if resources[i].Product != resources[j].Product {
			return resources[i].Product < resources[j].Product
		}
		return resources[i].Settlement < resources[j].Settlement
	})
	return resources
}

func (manager *Manager) AccountIDs() []domain.AccountID {
	manager.mu.RLock()
	accountIDs := append([]domain.AccountID(nil), manager.order...)
	dynamic := make([]domain.AccountID, 0, len(manager.runtimes))
	for accountID := range manager.runtimes {
		dynamic = append(dynamic, accountID)
	}
	manager.mu.RUnlock()

	manager.groupMu.Lock()
	for accountID, runtime := range manager.groups {
		if runtime != nil && runtime.active.Load() != nil {
			dynamic = append(dynamic, accountID)
		}
	}
	manager.groupMu.Unlock()

	seen := make(map[domain.AccountID]struct{}, len(accountIDs)+len(dynamic))
	for _, accountID := range accountIDs {
		seen[accountID] = struct{}{}
	}
	sort.Slice(dynamic, func(i, j int) bool { return dynamic[i] < dynamic[j] })
	for _, accountID := range dynamic {
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		accountIDs = append(accountIDs, accountID)
	}
	return accountIDs
}

func (manager *Manager) Resolve(ctx context.Context, accountID domain.AccountID, instrumentID domain.InstrumentID) (exchange.Instrument, error) {
	manager.mu.RLock()
	if manager.closed {
		manager.mu.RUnlock()
		return exchange.Instrument{}, ErrManagerClosed
	}
	_, ok := manager.entries[accountID]
	manager.mu.RUnlock()
	if ok {
		return manager.catalog.Resolve(ctx, accountID, instrumentID)
	}
	accesses, err := manager.activeRouteAccesses(accountID)
	if err != nil {
		return exchange.Instrument{}, err
	}
	for _, access := range accesses {
		if access.publicLease == nil {
			continue
		}
		instrument, resolveErr := access.publicLease.Resolve(ctx, instrumentID)
		if resolveErr == nil {
			return instrument, nil
		}
	}
	return exchange.Instrument{}, fmt.Errorf("%w: instrument is not in an active route", catalog.ErrCatalogUnavailable)
}

func (manager *Manager) dynamicSnapshot(accountID domain.AccountID) (catalog.Snapshot, error) {
	accesses, err := manager.activeRouteAccesses(accountID)
	if err != nil {
		return catalog.Snapshot{}, err
	}
	byID := make(map[domain.InstrumentID]catalog.Instrument)
	revisions := make([]string, 0, len(accesses))
	result := catalog.Snapshot{}
	for _, access := range accesses {
		if access.publicLease == nil {
			return catalog.Snapshot{}, catalog.ErrCatalogUnavailable
		}
		snapshot, snapshotErr := access.publicLease.Snapshot()
		if snapshotErr != nil {
			return catalog.Snapshot{}, snapshotErr
		}
		if len(accesses) == 1 {
			result.Scope = snapshot.Scope
		}
		if snapshot.Generation > result.Generation {
			result.Generation = snapshot.Generation
		}
		if result.LoadedAt.IsZero() || snapshot.LoadedAt.Before(result.LoadedAt) {
			result.LoadedAt = snapshot.LoadedAt
		}
		revisions = append(revisions, snapshot.Revision)
		for _, instrument := range snapshot.Instruments {
			byID[instrument.ID] = instrument
		}
	}
	sort.Strings(revisions)
	revision := sha256.Sum256([]byte(strings.Join(revisions, "\x00")))
	result.Revision = fmt.Sprintf("runtime-%x", revision[:16])
	result.Instruments = make([]catalog.Instrument, 0, len(byID))
	for _, instrument := range byID {
		result.Instruments = append(result.Instruments, instrument)
	}
	sort.Slice(result.Instruments, func(i, j int) bool { return result.Instruments[i].ID < result.Instruments[j].ID })
	return result, nil
}

func (manager *Manager) dynamicStatus(accountID domain.AccountID, requirePrivateStream bool) (Status, error) {
	statuses, err := manager.dynamicRouteStatuses(accountID, requirePrivateStream)
	if err != nil {
		return Status{}, err
	}
	if len(statuses) == 1 {
		return statuses[0], nil
	}
	result := Status{Resource: Resource{AccountID: accountID, Venue: statuses[0].Venue, Environment: statuses[0].Environment}, ClientReady: true}
	result.ReadReady, result.WriteReady = true, true
	for _, status := range statuses {
		result.ClientReady = result.ClientReady && status.ClientReady
		result.ReadReady = result.ReadReady && status.ReadReady
		result.WriteReady = result.WriteReady && status.WriteReady
		if result.Reason == "" && status.Reason != "" {
			result.Reason = status.Reason
		}
	}
	result.Resource.ReadReady = result.ReadReady
	result.Resource.WriteReady = result.WriteReady
	return result, nil
}

func (manager *Manager) RouteStatus(accountID domain.AccountID, product, settlement string) (Status, error) {
	manager.mu.RLock()
	if entry, ok := manager.entries[accountID]; ok {
		if string(entry.resource.Product) != product || entry.resource.Settlement != settlement {
			manager.mu.RUnlock()
			return Status{}, ErrProductMismatch
		}
		status, err := manager.statusLocked(accountID, true)
		manager.mu.RUnlock()
		return status, err
	}
	manager.mu.RUnlock()
	statuses, err := manager.dynamicRouteStatuses(accountID, true)
	if err != nil {
		return Status{}, err
	}
	for _, status := range statuses {
		if string(status.Product) == product && status.Settlement == settlement {
			return status, nil
		}
	}
	return Status{}, ErrRuntimeBlocked
}

func (manager *Manager) RecoveryRouteStatus(accountID domain.AccountID, product, settlement string) (Status, error) {
	manager.mu.RLock()
	if entry, ok := manager.entries[accountID]; ok {
		if string(entry.resource.Product) != product || entry.resource.Settlement != settlement {
			manager.mu.RUnlock()
			return Status{}, ErrProductMismatch
		}
		status, err := manager.statusLocked(accountID, false)
		manager.mu.RUnlock()
		return status, err
	}
	manager.mu.RUnlock()
	statuses, err := manager.dynamicRouteStatuses(accountID, false)
	if err != nil {
		return Status{}, err
	}
	for _, status := range statuses {
		if string(status.Product) == product && status.Settlement == settlement {
			return status, nil
		}
	}
	return Status{}, ErrRuntimeBlocked
}

func (manager *Manager) dynamicRouteStatuses(accountID domain.AccountID, requirePrivateStream bool) ([]Status, error) {
	accesses, err := manager.activeRouteAccesses(accountID)
	if err != nil {
		return nil, err
	}
	statuses := make([]Status, 0, len(accesses))
	for _, access := range accesses {
		statuses = append(statuses, manager.dynamicRouteStatus(access, requirePrivateStream))
	}
	return statuses, nil
}

func (manager *Manager) dynamicRouteStatus(access activeRouteAccess, enforceWritePolicy bool) Status {
	resource := Resource{
		AccountID: access.accountID, Venue: access.venue, Product: exchange.Product(access.definition.Product),
		Environment: access.environment, Settlement: access.definition.SettlementAsset,
	}
	status := Status{Resource: resource}
	clientRoute, clientReady := access.handle.(ConnectionClientRoute)
	status.ClientReady = clientReady
	if access.publicLease != nil {
		status.Catalog = access.publicLease.Status()
	}
	if !clientReady {
		status.Reason = ReasonClientUnavailable
	} else if !status.Catalog.ReadReady {
		status.Reason = status.Catalog.Reason
	} else {
		status.ReadReady = true
		switch {
		case !clientRoute.ExactLookupReady():
			status.Reason = ReasonExactLookupUnavailable
		case enforceWritePolicy && !manager.allowLiveWrites:
			status.Reason = ErrLiveWriteDisabled.Error()
		default:
			status.WriteReady = true
		}
	}
	status.Resource.ReadReady = status.ReadReady
	status.Resource.WriteReady = status.WriteReady
	status.Resource.Reason = status.Reason
	return status
}

func routeCapability(venue exchange.Venue, product, settlement string) (capability.Row, bool) {
	for _, row := range capability.StaticMatrix().Rows {
		if row.Venue == string(venue) && row.Product == product && row.Settlement == settlement {
			return row, true
		}
	}
	return capability.Row{}, false
}

func (manager *Manager) Runtime(accountID domain.AccountID) (RuntimeAccount, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	runtime, ok := manager.runtimes[accountID]
	if !ok {
		return RuntimeAccount{}, ErrUnknownAccount
	}
	return runtime, nil
}

func (manager *Manager) Runtimes() []RuntimeAccount {
	manager.mu.RLock()
	result := make([]RuntimeAccount, 0, len(manager.runtimes))
	for _, runtime := range manager.runtimes {
		result = append(result, runtime)
	}
	manager.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].AccountID < result[j].AccountID })
	return result
}

// RuntimeReady reports control-plane availability. Account route eligibility
// is reported separately by each runtime.
func (manager *Manager) RuntimeReady() bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return !manager.closed
}

func (manager *Manager) RuntimeReadiness() (bool, int, int) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closed {
		return false, 0, 0
	}
	active, ready := 0, 0
	for _, runtime := range manager.runtimes {
		active++
		if runtime.Ready {
			ready++
		}
	}
	return true, active, ready
}

func (manager *Manager) CheckWriteFence(fence WriteFence) error {
	manager.mu.RLock()
	if manager.closed {
		manager.mu.RUnlock()
		return ErrManagerClosed
	}
	runtime, ok := manager.runtimes[fence.AccountID]
	allowLiveWrites := manager.allowLiveWrites
	manager.mu.RUnlock()
	if ok {
		if !runtime.Ready || runtime.Fence() != fence {
			return ErrStaleWriteFence
		}
	} else {
		active := manager.ActiveSnapshot(fence.AccountID)
		if active == nil || active.Fence != (domain.AccountFence{
			AccountID: fence.AccountID, RuntimeID: fence.RuntimeID,
		}) {
			return ErrStaleWriteFence
		}
	}
	// Every runtime reaches a real venue, so the gate is the operator switch and
	// nothing else. It used to also consult the runtime's own mode, back when a
	// test runtime could pass a fence the switch would otherwise have refused.
	if !allowLiveWrites {
		return ErrLiveWriteDisabled
	}
	return nil
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
	manager.groupMu.Lock()
	groups := make([]*RuntimeGroup, 0)
	seenGroups := make(map[*RuntimeGroup]struct{})
	for _, runtime := range manager.groups {
		appendGroup := func(group *RuntimeGroup) {
			if group == nil {
				return
			}
			if _, exists := seenGroups[group]; exists {
				return
			}
			seenGroups[group] = struct{}{}
			groups = append(groups, group)
		}
		appendGroup(runtime.activeGroup)
	}
	manager.groupMu.Unlock()
	for _, group := range groups {
		if err := group.destroyConfirmed(context.Background()); err != nil {
			failed = true
		}
	}
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
	if !failed && manager.publicConnectors != nil {
		if err := manager.publicConnectors.Close(); err != nil {
			failed = true
		}
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
