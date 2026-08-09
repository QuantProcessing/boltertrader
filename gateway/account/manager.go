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
	ErrUnknownAccount      = errors.New("unknown account")
	ErrProductMismatch     = errors.New("account product mismatch")
	ErrManagerClosed       = errors.New("account manager closed")
	ErrRuntimeExists       = errors.New("runtime account already exists")
	ErrRuntimeRevision     = errors.New("runtime desired-state revision conflict")
	ErrRuntimeModeMismatch = errors.New("runtime mode mismatch")
	ErrRuntimeBlocked      = errors.New("runtime transition blocked")
	ErrStaleWriteFence     = errors.New("stale runtime write fence")
	ErrLiveWriteDisabled   = errors.New("live private writes disabled")
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
	Secrets             secret.Provider
	ExchangeCatalog     exchange.InstrumentCatalog
	GatewayCatalog      *catalog.Service
	HTTPClient          *http.Client
	RefreshInterval     time.Duration
	RuntimeMode         RuntimeMode
	AllowLiveWrites     bool
	Lifecycle           RuntimeLifecycle
	RuntimeGroupBuilder RuntimeGroupBuilder
	CredentialResolver  ScopedCredentialResolver
	ProfileRegistry     *ProfileRegistry
	RuntimeStore        RuntimeStateStore
}

type RuntimeMode string

const (
	RuntimeModeTest RuntimeMode = "test"
	RuntimeModeLive RuntimeMode = "live"
)

type RuntimeState string

const (
	RuntimeStateActive   RuntimeState = "active"
	RuntimeStateDisabled RuntimeState = "disabled"
	RuntimeStateDraining RuntimeState = "draining"
)

type DesiredRuntime struct {
	AccountID            domain.AccountID `json:"accountId"`
	Venue                string           `json:"venue"`
	VenueEnvironment     string           `json:"venueEnvironment"`
	Mode                 RuntimeMode      `json:"-"`
	SecretRef            secret.OpaqueRef `json:"secretRef"`
	DesiredStateRevision uint64           `json:"desiredStateRevision"`
	RuntimeGeneration    uint64           `json:"runtimeGeneration"`
	CredentialVersion    uint64           `json:"credentialVersion"`
	RequestedState       RuntimeState     `json:"requestedState"`
	RotationFence        string           `json:"rotationFence,omitempty"`
}

func (desired DesiredRuntime) Fence() WriteFence {
	return WriteFence{
		AccountID: desired.AccountID, DesiredStateRevision: desired.DesiredStateRevision,
		RuntimeGeneration: desired.RuntimeGeneration, CredentialVersion: desired.CredentialVersion,
	}
}

type WriteFence struct {
	AccountID            domain.AccountID `json:"accountId"`
	DesiredStateRevision uint64           `json:"desiredStateRevision"`
	RuntimeGeneration    uint64           `json:"runtimeGeneration"`
	CredentialVersion    uint64           `json:"credentialVersion"`
}

type RuntimeBlockers struct {
	OpenOrders        int `json:"openOrders"`
	Positions         int `json:"positions"`
	UncertainCommands int `json:"uncertainCommands"`
}

func (blockers RuntimeBlockers) Any() bool {
	return blockers.OpenOrders > 0 || blockers.Positions > 0 || blockers.UncertainCommands > 0
}

type RuntimeAccount struct {
	Desired                         DesiredRuntime `json:"desired"`
	State                           RuntimeState   `json:"state"`
	Ready                           bool           `json:"ready"`
	Reason                          string         `json:"reason,omitempty"`
	RuntimeVersion                  uint64         `json:"runtimeVersion"`
	LastAppliedDesiredStateRevision uint64         `json:"lastAppliedDesiredStateRevision"`
	LastAppliedRuntimeGeneration    uint64         `json:"lastAppliedRuntimeGeneration"`
	ObservedAt                      time.Time      `json:"observedAt"`
}

type RuntimeLifecycle interface {
	Validate(context.Context, DesiredRuntime) error
	Activate(context.Context, DesiredRuntime) error
	Deactivate(context.Context, domain.AccountID) error
	Blockers(context.Context, domain.AccountID) (RuntimeBlockers, error)
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
	mu                 sync.RWMutex
	runtimeMu          sync.Mutex
	entries            map[domain.AccountID]*entry
	order              []domain.AccountID
	catalog            *catalog.Service
	closed             bool
	closeMu            sync.Mutex
	cleanup            map[domain.AccountID]bool
	refreshInterval    time.Duration
	refreshCancel      context.CancelFunc
	refreshDone        chan struct{}
	started            bool
	runtimes           map[domain.AccountID]RuntimeAccount
	runtimeMode        RuntimeMode
	allowLiveWrites    bool
	lifecycle          RuntimeLifecycle
	groupMu            sync.Mutex
	groups             map[domain.AccountID]*accountRuntime
	groupBuilder       RuntimeGroupBuilder
	credentialResolver ScopedCredentialResolver
	profileRegistry    *ProfileRegistry
	runtimeStore       RuntimeStateStore
}

type entry struct {
	resource            Resource
	capability          capability.Row
	handle              clientHandle
	privateStreamReady  bool
	privateStreamReason string
	privateSnapshotID   string
	privateSequence     uint64
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
		entries:            make(map[domain.AccountID]*entry, len(accounts)),
		order:              make([]domain.AccountID, 0, len(accounts)),
		catalog:            options.GatewayCatalog,
		cleanup:            make(map[domain.AccountID]bool, len(accounts)),
		refreshInterval:    options.RefreshInterval,
		runtimes:           make(map[domain.AccountID]RuntimeAccount),
		runtimeMode:        options.RuntimeMode,
		allowLiveWrites:    options.AllowLiveWrites,
		lifecycle:          options.Lifecycle,
		groups:             make(map[domain.AccountID]*accountRuntime),
		groupBuilder:       options.RuntimeGroupBuilder,
		credentialResolver: options.CredentialResolver,
		profileRegistry:    options.ProfileRegistry,
		runtimeStore:       options.RuntimeStore,
	}
	if manager.runtimeMode == "" {
		manager.runtimeMode = RuntimeModeTest
	}
	if manager.profileRegistry == nil {
		manager.profileRegistry = NewProfileRegistry(nil)
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
	if ready {
		entry.privateStreamReady = false
		entry.privateStreamReason = ReasonPrivateStreamStarting
		entry.privateSnapshotID = ""
		entry.privateSequence = 0
	} else {
		entry.privateStreamReady = false
		entry.privateStreamReason = reason
		entry.privateSnapshotID = ""
		entry.privateSequence = 0
	}
	return nil
}

func (manager *Manager) ObservePrivateStreamBarrier(accountID domain.AccountID, snapshotID string, sequence uint64) error {
	if err := accountID.Validate(); err != nil {
		return err
	}
	if snapshotID == "" || snapshotID != strings.TrimSpace(snapshotID) || len(snapshotID) > 256 || strings.ContainsAny(snapshotID, "\n\r\t") || sequence == 0 {
		return fmt.Errorf("private stream barrier is invalid")
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
	if entry.privateStreamReason != ReasonPrivateStreamStarting {
		return fmt.Errorf("private stream barrier is not expected")
	}
	entry.privateSnapshotID = snapshotID
	entry.privateSequence = sequence
	entry.privateStreamReady = true
	entry.privateStreamReason = ""
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

func (manager *Manager) CreateRuntime(ctx context.Context, desired DesiredRuntime) (RuntimeAccount, error) {
	if err := manager.validateDesired(desired); err != nil {
		return RuntimeAccount{}, err
	}
	if desired.RuntimeGeneration != 1 || desired.CredentialVersion != 1 || desired.RequestedState != RuntimeStateActive {
		return RuntimeAccount{}, ErrRuntimeRevision
	}
	manager.runtimeMu.Lock()
	defer manager.runtimeMu.Unlock()
	manager.mu.RLock()
	_, exists := manager.runtimes[desired.AccountID]
	closed := manager.closed
	manager.mu.RUnlock()
	if closed {
		return RuntimeAccount{}, ErrManagerClosed
	}
	if exists {
		return RuntimeAccount{}, ErrRuntimeExists
	}
	if err := manager.validateAndActivate(ctx, desired); err != nil {
		return RuntimeAccount{}, err
	}
	runtime := appliedRuntime(desired, RuntimeStateActive, true, 1)
	manager.mu.Lock()
	manager.runtimes[desired.AccountID] = runtime
	manager.mu.Unlock()
	return runtime, nil
}

func (manager *Manager) ValidateRuntime(ctx context.Context, desired DesiredRuntime) error {
	if err := manager.validateDesired(desired); err != nil {
		return err
	}
	if manager.lifecycle == nil {
		return fmt.Errorf("runtime lifecycle unavailable")
	}
	if err := manager.lifecycle.Validate(ctx, desired); err != nil {
		return fmt.Errorf("runtime validation failed")
	}
	return nil
}

func (manager *Manager) RotateRuntime(ctx context.Context, desired DesiredRuntime, expectedRevision uint64) (RuntimeAccount, error) {
	if err := manager.validateDesired(desired); err != nil {
		return RuntimeAccount{}, err
	}
	if desired.RequestedState != RuntimeStateActive || strings.TrimSpace(desired.RotationFence) == "" {
		return RuntimeAccount{}, ErrRuntimeRevision
	}
	manager.runtimeMu.Lock()
	defer manager.runtimeMu.Unlock()
	manager.mu.RLock()
	current, exists := manager.runtimes[desired.AccountID]
	closed := manager.closed
	manager.mu.RUnlock()
	if closed {
		return RuntimeAccount{}, ErrManagerClosed
	}
	if manager.lifecycle == nil {
		return RuntimeAccount{}, fmt.Errorf("runtime lifecycle unavailable")
	}
	if !exists {
		return RuntimeAccount{}, ErrUnknownAccount
	}
	if uint64(expectedRevision) < current.Desired.DesiredStateRevision ||
		desired.DesiredStateRevision != expectedRevision+1 ||
		desired.RuntimeGeneration != current.Desired.RuntimeGeneration+1 ||
		desired.CredentialVersion != current.Desired.CredentialVersion+1 {
		return RuntimeAccount{}, ErrRuntimeRevision
	}
	if err := manager.validateAndActivate(ctx, desired); err != nil {
		return RuntimeAccount{}, err
	}
	runtime := appliedRuntime(desired, RuntimeStateActive, true, current.RuntimeVersion+1)
	manager.mu.Lock()
	manager.runtimes[desired.AccountID] = runtime
	manager.mu.Unlock()
	return runtime, nil
}

func (manager *Manager) DrainRuntime(ctx context.Context, accountID domain.AccountID, expectedRevision, nextRevision uint64) (RuntimeAccount, error) {
	return manager.transitionRuntime(ctx, accountID, expectedRevision, nextRevision, RuntimeStateDraining, false)
}

func (manager *Manager) DisableRuntime(ctx context.Context, accountID domain.AccountID, expectedRevision, nextRevision uint64) (RuntimeAccount, error) {
	return manager.transitionRuntime(ctx, accountID, expectedRevision, nextRevision, RuntimeStateDisabled, true)
}

func (manager *Manager) transitionRuntime(ctx context.Context, accountID domain.AccountID, expectedRevision, nextRevision uint64, state RuntimeState, deactivate bool) (RuntimeAccount, error) {
	manager.runtimeMu.Lock()
	defer manager.runtimeMu.Unlock()
	current, err := manager.runtimeForMutation(accountID, expectedRevision)
	if err != nil {
		return RuntimeAccount{}, err
	}
	if nextRevision != current.Desired.DesiredStateRevision+1 {
		return RuntimeAccount{}, ErrRuntimeRevision
	}
	blockers, err := manager.lifecycle.Blockers(ctx, accountID)
	if err != nil {
		return RuntimeAccount{}, fmt.Errorf("runtime blocker check failed")
	}
	if blockers.Any() {
		return RuntimeAccount{}, ErrRuntimeBlocked
	}
	if deactivate {
		if err := manager.lifecycle.Deactivate(ctx, accountID); err != nil {
			return RuntimeAccount{}, fmt.Errorf("runtime deactivation failed")
		}
	}
	current.Desired.DesiredStateRevision = nextRevision
	current.Desired.RequestedState = state
	current.State = state
	current.Ready = !deactivate
	current.RuntimeVersion++
	current.LastAppliedDesiredStateRevision = current.Desired.DesiredStateRevision
	current.LastAppliedRuntimeGeneration = current.Desired.RuntimeGeneration
	current.ObservedAt = time.Now().UTC()
	manager.mu.Lock()
	manager.runtimes[accountID] = current
	manager.mu.Unlock()
	return current, nil
}

func (manager *Manager) RemoveRuntime(ctx context.Context, accountID domain.AccountID, expectedRevision uint64) error {
	manager.runtimeMu.Lock()
	defer manager.runtimeMu.Unlock()
	if _, err := manager.runtimeForMutation(accountID, expectedRevision); err != nil {
		return err
	}
	blockers, err := manager.lifecycle.Blockers(ctx, accountID)
	if err != nil {
		return fmt.Errorf("runtime blocker check failed")
	}
	if blockers.Any() {
		return ErrRuntimeBlocked
	}
	if err := manager.lifecycle.Deactivate(ctx, accountID); err != nil {
		return fmt.Errorf("runtime deactivation failed")
	}
	manager.mu.Lock()
	delete(manager.runtimes, accountID)
	manager.mu.Unlock()
	return nil
}

func (manager *Manager) RestoreRuntimes(ctx context.Context, desiredRuntimes []DesiredRuntime) error {
	manager.runtimeMu.Lock()
	defer manager.runtimeMu.Unlock()
	seen := make(map[domain.AccountID]struct{}, len(desiredRuntimes))
	restored := make([]domain.AccountID, 0, len(desiredRuntimes))
	for _, desired := range desiredRuntimes {
		if err := manager.validateDesired(desired); err != nil {
			manager.rollbackRestored(ctx, restored)
			return err
		}
		if _, duplicate := seen[desired.AccountID]; duplicate {
			manager.rollbackRestored(ctx, restored)
			return ErrRuntimeExists
		}
		seen[desired.AccountID] = struct{}{}
		runtime := appliedRuntime(desired, desired.RequestedState, false, 1)
		if desired.RequestedState == RuntimeStateActive {
			if err := manager.validateAndActivate(ctx, desired); err != nil {
				manager.rollbackRestored(ctx, restored)
				return err
			}
			runtime.Ready = true
			restored = append(restored, desired.AccountID)
		}
		manager.mu.Lock()
		manager.runtimes[desired.AccountID] = runtime
		manager.mu.Unlock()
	}
	return nil
}

func appliedRuntime(desired DesiredRuntime, state RuntimeState, ready bool, version uint64) RuntimeAccount {
	return RuntimeAccount{
		Desired: desired, State: state, Ready: ready, RuntimeVersion: version,
		LastAppliedDesiredStateRevision: desired.DesiredStateRevision,
		LastAppliedRuntimeGeneration:    desired.RuntimeGeneration,
		ObservedAt:                      time.Now().UTC(),
	}
}

func (manager *Manager) rollbackRestored(ctx context.Context, accountIDs []domain.AccountID) {
	for index := len(accountIDs) - 1; index >= 0; index-- {
		_ = manager.lifecycle.Deactivate(ctx, accountIDs[index])
		manager.mu.Lock()
		delete(manager.runtimes, accountIDs[index])
		manager.mu.Unlock()
	}
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
	sort.Slice(result, func(i, j int) bool { return result[i].Desired.AccountID < result[j].Desired.AccountID })
	return result
}

// RuntimeReady reports control-plane availability. Account route eligibility
// is reported separately by each runtime.
func (manager *Manager) RuntimeReady() bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closed {
		return false
	}
	if len(manager.runtimes) == 0 {
		return true
	}
	hasActive := false
	for _, runtime := range manager.runtimes {
		if runtime.State != RuntimeStateActive {
			continue
		}
		hasActive = true
		if !runtime.Ready {
			return false
		}
	}
	return hasActive
}

func (manager *Manager) CheckWriteFence(fence WriteFence) error {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closed {
		return ErrManagerClosed
	}
	runtime, ok := manager.runtimes[fence.AccountID]
	if !ok || runtime.State != RuntimeStateActive || runtime.Desired.Fence() != fence {
		return ErrStaleWriteFence
	}
	if runtime.Desired.Mode == RuntimeModeLive && !manager.allowLiveWrites {
		return ErrLiveWriteDisabled
	}
	return nil
}

func (manager *Manager) runtimeForMutation(accountID domain.AccountID, expectedRevision uint64) (RuntimeAccount, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.closed {
		return RuntimeAccount{}, ErrManagerClosed
	}
	if manager.lifecycle == nil {
		return RuntimeAccount{}, fmt.Errorf("runtime lifecycle unavailable")
	}
	runtime, ok := manager.runtimes[accountID]
	if !ok {
		return RuntimeAccount{}, ErrUnknownAccount
	}
	if runtime.Desired.DesiredStateRevision != expectedRevision {
		return RuntimeAccount{}, ErrRuntimeRevision
	}
	return runtime, nil
}

func (manager *Manager) validateDesired(desired DesiredRuntime) error {
	if err := desired.AccountID.Validate(); err != nil {
		return err
	}
	if err := config.ValidateRuntimeIdentity(string(manager.runtimeMode), string(desired.AccountID), desired.Venue, desired.VenueEnvironment); err != nil {
		return err
	}
	if desired.Mode != manager.runtimeMode {
		return ErrRuntimeModeMismatch
	}
	if desired.SecretRef.IsZero() || desired.DesiredStateRevision == 0 || desired.RuntimeGeneration == 0 || desired.CredentialVersion == 0 {
		return fmt.Errorf("runtime desired state is incomplete")
	}
	switch desired.RequestedState {
	case RuntimeStateActive, RuntimeStateDisabled, RuntimeStateDraining:
		return nil
	default:
		return fmt.Errorf("invalid runtime requested state")
	}
}

func (manager *Manager) validateAndActivate(ctx context.Context, desired DesiredRuntime) error {
	if manager.lifecycle == nil {
		return fmt.Errorf("runtime lifecycle unavailable")
	}
	if err := manager.lifecycle.Validate(ctx, desired); err != nil {
		return fmt.Errorf("runtime validation failed")
	}
	if err := manager.lifecycle.Activate(ctx, desired); err != nil {
		return fmt.Errorf("runtime activation failed")
	}
	return nil
}

func (manager *Manager) Close() error {
	manager.closeMu.Lock()
	defer manager.closeMu.Unlock()
	manager.mu.Lock()
	manager.closed = true
	runtimeIDs := make([]domain.AccountID, 0, len(manager.runtimes))
	for accountID, runtime := range manager.runtimes {
		if runtime.State == RuntimeStateActive {
			runtimeIDs = append(runtimeIDs, accountID)
		}
	}
	manager.mu.Unlock()
	if manager.refreshCancel != nil {
		manager.refreshCancel()
	}
	if manager.refreshDone != nil {
		<-manager.refreshDone
		manager.refreshDone = nil
	}
	var failed bool
	if manager.lifecycle != nil {
		for _, accountID := range runtimeIDs {
			if err := manager.lifecycle.Deactivate(context.Background(), accountID); err != nil {
				failed = true
			}
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
