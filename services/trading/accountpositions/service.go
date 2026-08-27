package accountpositions

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

const (
	discoveryTTL      = 30 * time.Second
	integrityInterval = 60 * time.Second
	reconnectMinimum  = 2 * time.Second
	reconnectMaximum  = 30 * time.Second
)

type principalProjection struct {
	hydrated       bool
	lastDiscovered time.Time
	revision       uint64
	accounts       map[string]AccountState
	items          map[string]Item
	versions       map[string]uint64
	watermarks     map[string]entityWatermark
	connected      map[string]bool
}

type entityWatermark struct {
	at      time.Time
	version uint64
}

// Service is Trading's user-level account position read model. It owns one
// projection per principal and one Gateway account subscription per discovered
// account. A failed account is retained as stale/unavailable and never clears
// a healthy sibling account.
type Service struct {
	source Source
	store  Store
	now    func() time.Time

	ensureMu sync.Mutex
	mu       sync.RWMutex
	views    map[string]*principalProjection

	lifecycleMu sync.Mutex
	runCtx      context.Context
	watchers    map[string]context.CancelFunc
}

func NewService(source Source, store Store, now func() time.Time) (*Service, error) {
	if source == nil || store == nil {
		return nil, fmt.Errorf("account position projection dependencies are incomplete")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{
		source: source, store: store, now: now,
		views: make(map[string]*principalProjection), watchers: make(map[string]context.CancelFunc),
	}, nil
}

// Run gives lazy account supervisors a process lifetime. Accounts discovered
// before Run begins are started immediately; accounts discovered later start
// from Snapshot without opening duplicate supervisors.
func (service *Service) Run(ctx context.Context) {
	service.lifecycleMu.Lock()
	if service.runCtx != nil {
		service.lifecycleMu.Unlock()
		<-ctx.Done()
		return
	}
	service.runCtx = ctx
	service.lifecycleMu.Unlock()

	service.mu.RLock()
	known := make([][2]string, 0)
	for principal, view := range service.views {
		for accountID := range view.accounts {
			known = append(known, [2]string{principal, accountID})
		}
	}
	service.mu.RUnlock()
	for _, scope := range known {
		service.startSupervisor(scope[0], scope[1])
	}

	<-ctx.Done()
	service.lifecycleMu.Lock()
	for _, cancel := range service.watchers {
		cancel()
	}
	service.watchers = make(map[string]context.CancelFunc)
	service.lifecycleMu.Unlock()
}

func (service *Service) Snapshot(ctx context.Context, principal string, filter Filter) (Snapshot, error) {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return Snapshot{}, fmt.Errorf("account position principal is required")
	}
	if err := service.ensurePrincipal(ctx, principal); err != nil {
		return Snapshot{}, err
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	view := service.views[principal]
	if view == nil {
		return Snapshot{}, fmt.Errorf("account position projection is unavailable")
	}
	accounts := make([]AccountState, 0, len(view.accounts))
	for _, state := range view.accounts {
		if filter.AccountID != "" && state.AccountID != filter.AccountID {
			continue
		}
		accounts = append(accounts, cloneState(state))
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Venue != accounts[j].Venue {
			return accounts[i].Venue < accounts[j].Venue
		}
		return accounts[i].AccountID < accounts[j].AccountID
	})
	items := make([]Item, 0, len(view.items))
	for _, item := range view.items {
		if !matches(item, filter) {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].AccountID != items[j].AccountID {
			return items[i].AccountID < items[j].AccountID
		}
		if items[i].InstrumentID != items[j].InstrumentID {
			return items[i].InstrumentID < items[j].InstrumentID
		}
		return items[i].PositionSlot < items[j].PositionSlot
	})
	return Snapshot{
		ProjectionRevision: view.revision, VerifiedAt: snapshotVerifiedAt(accounts),
		Summary: summarize(accounts, items), Accounts: accounts, Items: items,
	}, nil
}

func (service *Service) ensurePrincipal(ctx context.Context, principal string) error {
	service.ensureMu.Lock()
	defer service.ensureMu.Unlock()
	now := service.now().UTC()
	service.mu.RLock()
	view := service.views[principal]
	freshDirectory := view != nil && view.hydrated && now.Sub(view.lastDiscovered) < discoveryTTL
	service.mu.RUnlock()
	if freshDirectory {
		return nil
	}
	if err := service.hydrate(ctx, principal); err != nil {
		return err
	}
	accounts, err := service.source.ListAccounts(ctx, principal)
	if err != nil {
		service.mu.RLock()
		usable := service.views[principal] != nil && len(service.views[principal].accounts) > 0
		service.mu.RUnlock()
		if usable {
			service.markPrincipalStale(ctx, principal, "account_directory_unavailable")
			return nil
		}
		return fmt.Errorf("list Gateway accounts: %w", err)
	}
	seen := make(map[string]Account, len(accounts))
	for index := range accounts {
		if strings.TrimSpace(accounts[index].Label) == "" {
			accounts[index].Label = accounts[index].AccountID
		}
		account := accounts[index]
		if err := validateAccount(account); err != nil {
			return err
		}
		if _, duplicate := seen[account.AccountID]; duplicate {
			return fmt.Errorf("Gateway account directory contains duplicate account")
		}
		seen[account.AccountID] = account
	}
	service.removeMissingAccounts(ctx, principal, seen)
	for _, account := range accounts {
		needsSnapshot := service.installAccountMetadata(principal, account, now)
		if needsSnapshot {
			snapshot, snapshotErr := service.source.PositionSnapshot(ctx, principal, account.AccountID)
			if snapshotErr != nil {
				service.markAccountFailure(ctx, principal, account, snapshotErr)
			} else if replaceErr := service.replaceSnapshot(ctx, principal, account, snapshot); replaceErr != nil {
				service.markAccountFailure(ctx, principal, account, replaceErr)
			}
		}
		service.startSupervisor(principal, account.AccountID)
	}
	service.mu.Lock()
	service.views[principal].lastDiscovered = now
	service.mu.Unlock()
	return nil
}

func (service *Service) hydrate(ctx context.Context, principal string) error {
	service.mu.RLock()
	view := service.views[principal]
	if view != nil && view.hydrated {
		service.mu.RUnlock()
		return nil
	}
	service.mu.RUnlock()
	states, items, err := service.store.LoadPrincipal(ctx, principal)
	if err != nil {
		return fmt.Errorf("load account position projection: %w", err)
	}
	now := service.now().UTC()
	loaded := &principalProjection{
		hydrated: true, accounts: make(map[string]AccountState), items: make(map[string]Item),
		versions: make(map[string]uint64), watermarks: make(map[string]entityWatermark), connected: make(map[string]bool),
	}
	for _, state := range states {
		state.Freshness = FreshnessStale
		state.RouteHealth = RouteDegraded
		state.WriteReady = false
		state.Reason = "service_restarted"
		state.ReceivedAt = now
		loaded.accounts[state.AccountID] = cloneState(state)
	}
	for _, item := range items {
		item.Freshness = FreshnessStale
		item.RouteHealth = RouteDegraded
		item.WriteReady = false
		item.BlockedReason = "service_restarted"
		item.ReceivedAt = now
		loaded.items[item.Key.String()] = item
		loaded.versions[item.Key.String()] = item.ResourceVersion
		loaded.watermarks[item.Key.String()] = entityWatermark{at: item.ObservedAt.UTC(), version: item.ResourceVersion}
	}
	service.mu.Lock()
	if existing := service.views[principal]; existing == nil || !existing.hydrated {
		service.views[principal] = loaded
	}
	service.mu.Unlock()
	return nil
}

func (service *Service) installAccountMetadata(principal string, account Account, now time.Time) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	view := service.views[principal]
	state, exists := view.accounts[account.AccountID]
	if !exists {
		state = AccountState{Account: account, RouteHealth: RouteDegraded, Freshness: FreshnessSyncing, ReceivedAt: now}
		view.accounts[account.AccountID] = state
		return true
	}
	state.Account = account
	view.accounts[account.AccountID] = state
	for key, item := range view.items {
		if item.AccountID == account.AccountID {
			view.items[key] = decorate(item, account, state)
		}
	}
	return state.VerifiedAt.IsZero()
}

func (service *Service) replaceSnapshot(ctx context.Context, principal string, account Account, snapshot SourceSnapshot) error {
	if snapshot.AccountID != account.AccountID || snapshot.VerifiedAt.IsZero() {
		return fmt.Errorf("Gateway position snapshot identity is invalid")
	}
	now := service.now().UTC()
	freshness := snapshot.Freshness
	if freshness == "" {
		freshness = FreshnessLive
	}
	if !freshness.Valid() {
		return fmt.Errorf("Gateway position snapshot freshness is invalid")
	}
	projected := make(map[string]Item, len(snapshot.Items))
	for _, source := range snapshot.Items {
		state := AccountState{Account: account, Freshness: freshness, VerifiedAt: snapshot.VerifiedAt.UTC()}
		item, err := projectSourceItem(account, state, source, now)
		if err != nil {
			return err
		}
		if item.Quantity == "0" {
			continue
		}
		if _, duplicate := projected[item.Key.String()]; duplicate {
			return fmt.Errorf("Gateway position snapshot contains duplicate position")
		}
		projected[item.Key.String()] = item
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	view := service.views[principal]
	if view == nil {
		return fmt.Errorf("account position principal projection is unavailable")
	}
	state := view.accounts[account.AccountID]
	state.Account, state.Freshness, state.VerifiedAt, state.ReceivedAt = account, freshness, snapshot.VerifiedAt.UTC(), now
	state.WriteReady = accountWriteReady(account, freshness)
	if view.connected[account.AccountID] {
		state.RouteHealth, state.Reason = routeHealthFor(freshness), snapshot.Reason
	} else {
		state.RouteHealth = RouteDegraded
		state.Reason = "account_realtime_disconnected"
		if freshness == FreshnessUnavailable {
			state.RouteHealth, state.Reason = RouteUnavailable, snapshot.Reason
		}
	}
	removed := make([]Item, 0)
	upserted := make([]Item, 0, len(projected))
	for key, existing := range view.items {
		if existing.AccountID != account.AccountID {
			continue
		}
		next, present := projected[key]
		if present {
			if newerEntity(next.ObservedAt, next.ResourceVersion, view.watermarks[key]) {
				next = decorate(next, account, state)
				view.items[key], view.versions[key] = next, next.ResourceVersion
				view.watermarks[key] = entityWatermark{at: next.ObservedAt, version: next.ResourceVersion}
				upserted = append(upserted, next)
			}
			delete(projected, key)
			continue
		}
		// REST absence is a tombstone only when the snapshot is at least as
		// recent as the entity it would remove. A slow REST response can never
		// erase a fill/position update that arrived over WebSocket meanwhile.
		if !snapshot.VerifiedAt.Before(existing.ObservedAt) {
			removed = append(removed, existing)
			delete(view.items, key)
			view.watermarks[key] = entityWatermark{at: snapshot.VerifiedAt.UTC(), version: existing.ResourceVersion}
		}
	}
	for key, item := range projected {
		if !newerEntity(item.ObservedAt, item.ResourceVersion, view.watermarks[key]) {
			continue
		}
		item = decorate(item, account, state)
		view.items[key], view.versions[key] = item, item.ResourceVersion
		view.watermarks[key] = entityWatermark{at: item.ObservedAt, version: item.ResourceVersion}
		upserted = append(upserted, item)
	}
	items := accountItems(view, account.AccountID)
	for index := range items {
		items[index] = decorate(items[index], account, state)
		view.items[items[index].Key.String()] = items[index]
	}
	if err := service.store.ReplaceAccount(ctx, principal, state, items); err != nil {
		return err
	}
	view.accounts[account.AccountID] = cloneState(state)
	for range removed {
		view.revision++
	}
	for range upserted {
		view.revision++
	}
	view.revision++
	return nil
}

func (service *Service) applySourceEvent(ctx context.Context, principal, accountID string, event SourceEvent) error {
	if event.Kind == SourceEventResync {
		return ErrResyncRequired
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	view := service.views[principal]
	if view == nil {
		return ErrResyncRequired
	}
	state, ok := view.accounts[accountID]
	if !ok {
		return ErrResyncRequired
	}
	now := service.now().UTC()
	if event.Kind == SourceEventConnected {
		view.connected[accountID] = true
		state.ReceivedAt = now
		if state.VerifiedAt.IsZero() {
			state.RouteHealth, state.Freshness, state.WriteReady = RouteUnavailable, FreshnessUnavailable, false
		} else {
			state.RouteHealth, state.Reason = routeHealthFor(state.Freshness), ""
			state.WriteReady = accountWriteReady(state.Account, state.Freshness)
		}
		items := accountItems(view, accountID)
		for index := range items {
			items[index] = decorate(items[index], state.Account, state)
			view.items[items[index].Key.String()] = items[index]
		}
		if err := service.store.ReplaceAccount(ctx, principal, state, items); err != nil {
			return err
		}
		view.accounts[accountID] = state
		view.revision++
		return nil
	}
	if isAccountNotification(event.Kind) {
		if event.AccountID != accountID || event.OccurredAt.IsZero() {
			return fmt.Errorf("Gateway account event identity is invalid")
		}
		return nil
	}
	if event.Kind != SourceEventPosition || event.AccountID != accountID || event.OccurredAt.IsZero() {
		return fmt.Errorf("Gateway position event identity is invalid")
	}
	if event.Operation != OperationUpsert && event.Operation != OperationRemove {
		return ErrResyncRequired
	}
	state.RouteHealth, state.Freshness, state.Reason = RouteLive, FreshnessLive, ""
	state.WriteReady, state.ReceivedAt = accountWriteReady(state.Account, FreshnessLive), now
	source := SourceItem{
		AccountID: event.AccountID, RouteID: event.RouteID, InstrumentID: event.InstrumentID,
		PositionSlot: event.PositionSlot, Position: event.Position, ResourceVersion: event.ResourceVersion,
		ObservedAt: event.OccurredAt, VerifiedAt: now,
	}
	item, err := projectSourceItem(state.Account, state, source, now)
	if err != nil {
		return err
	}
	key := item.Key.String()
	if !newerEntity(event.OccurredAt, event.ResourceVersion, view.watermarks[key]) {
		return nil
	}
	if event.Operation == OperationUpsert && item.Quantity == "0" {
		return ErrResyncRequired
	}
	if err := service.store.Apply(ctx, principal, state, event.Operation, item); err != nil {
		return err
	}
	view.accounts[accountID] = state
	view.versions[key] = event.ResourceVersion
	view.watermarks[key] = entityWatermark{at: event.OccurredAt.UTC(), version: event.ResourceVersion}
	view.revision++
	switch event.Operation {
	case OperationUpsert:
		view.items[key] = item
	case OperationRemove:
		delete(view.items, key)
	default:
		return ErrResyncRequired
	}
	return nil
}

func isAccountNotification(kind string) bool {
	switch kind {
	case SourceEventOrder, SourceEventFill, SourceEventBalance, SourceEventAccountHealth:
		return true
	default:
		return false
	}
}

func (service *Service) startSupervisor(principal, accountID string) {
	service.lifecycleMu.Lock()
	defer service.lifecycleMu.Unlock()
	if service.runCtx == nil || service.runCtx.Err() != nil {
		return
	}
	key := principal + "\x00" + accountID
	if _, exists := service.watchers[key]; exists {
		return
	}
	ctx, cancel := context.WithCancel(service.runCtx)
	service.watchers[key] = cancel
	go service.supervise(ctx, principal, accountID, key)
}

func (service *Service) supervise(ctx context.Context, principal, accountID, watcherKey string) {
	defer func() {
		service.lifecycleMu.Lock()
		delete(service.watchers, watcherKey)
		service.lifecycleMu.Unlock()
	}()
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		service.reconcileAccount(ctx, principal, accountID)
	}()
	defer func() { <-reconcileDone }()
	backoff := reconnectMinimum
	for ctx.Err() == nil {
		if _, exists := service.account(principal, accountID); !exists {
			return
		}
		connectedAt := service.now()
		err := service.source.WatchAccountEvents(ctx, principal, accountID, func(event SourceEvent) error {
			return service.applySourceEvent(ctx, principal, accountID, event)
		})
		if ctx.Err() != nil {
			return
		}
		service.markRealtimeDisconnected(ctx, principal, accountID, safeSourceReason(err))
		if service.now().Sub(connectedAt) >= integrityInterval {
			backoff = reconnectMinimum
		}
		if !wait(ctx, jitteredReconnect(backoff)) {
			return
		}
		backoff *= 2
		if backoff > reconnectMaximum {
			backoff = reconnectMaximum
		}
	}
}

func (service *Service) reconcileAccount(ctx context.Context, principal, accountID string) {
	ticker := time.NewTicker(integrityInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			account, ok := service.account(principal, accountID)
			if !ok {
				return
			}
			snapshot, err := service.source.PositionSnapshot(ctx, principal, accountID)
			if err != nil {
				service.markReconcileFailure(ctx, principal, accountID, "position_snapshot_unavailable")
				continue
			}
			if err := service.replaceSnapshot(ctx, principal, account, snapshot); err != nil {
				service.markReconcileFailure(ctx, principal, accountID, "position_snapshot_invalid")
			}
		}
	}
}

func (service *Service) markReconcileFailure(ctx context.Context, principal, accountID, reason string) {
	service.mu.RLock()
	view := service.views[principal]
	connected := view != nil && view.connected[accountID]
	service.mu.RUnlock()
	if connected {
		return
	}
	service.markAccountStale(ctx, principal, accountID, reason)
}

func jitteredReconnect(base time.Duration) time.Duration {
	spread := base / 5
	if spread <= 0 {
		return base
	}
	return base - spread + time.Duration(rand.Int64N(int64(2*spread)+1))
}

func (service *Service) markRealtimeDisconnected(ctx context.Context, principal, accountID, reason string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	view := service.views[principal]
	state, ok := view.accounts[accountID]
	if !ok {
		return
	}
	view.connected[accountID] = false
	state.RouteHealth, state.Reason, state.ReceivedAt = RouteDegraded, reason, service.now().UTC()
	if state.VerifiedAt.IsZero() {
		state.RouteHealth, state.Freshness, state.WriteReady = RouteUnavailable, FreshnessUnavailable, false
	}
	items := accountItems(view, accountID)
	for index := range items {
		items[index] = decorate(items[index], state.Account, state)
		view.items[items[index].Key.String()] = items[index]
	}
	if service.store.ReplaceAccount(ctx, principal, state, items) != nil {
		return
	}
	view.accounts[accountID] = state
	view.revision++
}

func (service *Service) markAccountFailure(ctx context.Context, principal string, account Account, cause error) {
	service.mu.RLock()
	view := service.views[principal]
	state, exists := view.accounts[account.AccountID]
	hasRows := false
	for _, item := range view.items {
		hasRows = hasRows || item.AccountID == account.AccountID
	}
	service.mu.RUnlock()
	if !exists {
		state.Account = account
	}
	if hasRows {
		service.markAccountStale(ctx, principal, account.AccountID, safeSourceReason(cause))
		return
	}
	state.RouteHealth, state.Freshness, state.WriteReady = RouteUnavailable, FreshnessUnavailable, false
	state.Reason, state.ReceivedAt = safeSourceReason(cause), service.now().UTC()
	service.persistState(ctx, principal, state)
}

func (service *Service) markAccountStale(ctx context.Context, principal, accountID, reason string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	view := service.views[principal]
	state, ok := view.accounts[accountID]
	if !ok {
		return
	}
	items := accountItems(view, accountID)
	if state.VerifiedAt.IsZero() && len(items) == 0 {
		state.RouteHealth, state.Freshness = RouteUnavailable, FreshnessUnavailable
	} else {
		state.RouteHealth, state.Freshness = RouteDegraded, FreshnessStale
	}
	state.WriteReady = accountWriteReady(state.Account, state.Freshness)
	state.Reason, state.ReceivedAt = reason, service.now().UTC()
	for index := range items {
		items[index] = decorate(items[index], state.Account, state)
		view.items[items[index].Key.String()] = items[index]
	}
	if service.store.ReplaceAccount(ctx, principal, state, items) != nil {
		return
	}
	view.accounts[accountID] = state
	view.revision++
}

func (service *Service) persistState(ctx context.Context, principal string, state AccountState) {
	service.mu.Lock()
	defer service.mu.Unlock()
	view := service.views[principal]
	if view == nil {
		return
	}
	if service.store.ReplaceAccount(ctx, principal, state, nil) != nil {
		return
	}
	view.accounts[state.AccountID] = state
	view.revision++
}

func (service *Service) markPrincipalStale(ctx context.Context, principal, reason string) {
	service.mu.RLock()
	accountIDs := make([]string, 0, len(service.views[principal].accounts))
	for accountID := range service.views[principal].accounts {
		accountIDs = append(accountIDs, accountID)
	}
	service.mu.RUnlock()
	for _, accountID := range accountIDs {
		service.markAccountStale(ctx, principal, accountID, reason)
	}
}

func (service *Service) removeMissingAccounts(ctx context.Context, principal string, known map[string]Account) {
	service.mu.RLock()
	missing := make([]string, 0)
	if view := service.views[principal]; view != nil {
		for accountID := range view.accounts {
			if _, exists := known[accountID]; !exists {
				missing = append(missing, accountID)
			}
		}
	}
	service.mu.RUnlock()
	for _, accountID := range missing {
		service.lifecycleMu.Lock()
		watcherKey := principal + "\x00" + accountID
		if cancel := service.watchers[watcherKey]; cancel != nil {
			cancel()
			delete(service.watchers, watcherKey)
		}
		service.lifecycleMu.Unlock()
		_ = service.store.RemoveAccount(ctx, principal, accountID)
		service.mu.Lock()
		view := service.views[principal]
		delete(view.accounts, accountID)
		delete(view.connected, accountID)
		for key, item := range view.items {
			if item.AccountID == accountID {
				delete(view.items, key)
				view.revision++
			}
		}
		for key := range view.versions {
			if strings.HasPrefix(key, accountID+"\x00") {
				delete(view.versions, key)
			}
		}
		for key := range view.watermarks {
			if strings.HasPrefix(key, accountID+"\x00") {
				delete(view.watermarks, key)
			}
		}
		service.mu.Unlock()
	}
}

func (service *Service) account(principal, accountID string) (Account, bool) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	view := service.views[principal]
	if view == nil {
		return Account{}, false
	}
	state, ok := view.accounts[accountID]
	return state.Account, ok
}

func projectSourceItem(account Account, state AccountState, source SourceItem, receivedAt time.Time) (Item, error) {
	slot := source.PositionSlot
	if slot == "" {
		slot = source.Position.PositionSlot
	}
	key := Key{AccountID: source.AccountID, RouteID: source.RouteID, InstrumentID: source.InstrumentID, PositionSlot: slot}
	if source.AccountID != account.AccountID || !key.Valid() || source.ResourceVersion == 0 || source.ObservedAt.IsZero() || source.VerifiedAt.IsZero() {
		return Item{}, fmt.Errorf("Gateway position item identity is invalid")
	}
	quantity, err := decimal.NewFromString(source.Position.Quantity)
	if err != nil {
		return Item{}, fmt.Errorf("Gateway position quantity is invalid")
	}
	side := source.Position.Side
	if quantity.IsNegative() {
		side = "sell"
	} else if quantity.IsPositive() {
		side = "buy"
	}
	if side != "buy" && side != "sell" && !quantity.IsZero() {
		return Item{}, fmt.Errorf("Gateway position side is invalid")
	}
	for _, value := range []string{source.Position.EntryPrice, source.Position.MarkPrice, source.Position.UnrealizedPnL} {
		if _, err := decimal.NewFromString(zeroDecimal(value)); err != nil {
			return Item{}, fmt.Errorf("Gateway position decimal is invalid")
		}
	}
	for _, value := range []OptionalDecimal{source.Position.LiquidationPrice, source.Position.Leverage, source.Position.MarginUsed} {
		if value.Valid {
			parsed, parseErr := decimal.NewFromString(value.Value)
			if parseErr != nil || parsed.IsNegative() {
				return Item{}, fmt.Errorf("Gateway position optional decimal is invalid")
			}
		}
	}
	product, settlement, symbol := instrumentDisplay(source.InstrumentID)
	if route, ok := accountRoute(account, source.RouteID); ok {
		product, settlement = route.Product, route.SettlementAsset
	}
	item := Item{
		Key: key, Product: product, SettlementAsset: settlement, Symbol: symbol, Side: side,
		Quantity: quantity.String(), EntryPrice: emptyZero(source.Position.EntryPrice), MarkPrice: emptyZero(source.Position.MarkPrice),
		UnrealizedPnL: zeroDecimal(source.Position.UnrealizedPnL), UnrealizedPnLCurrency: settlement,
		UnrealizedPnLSource: "venue_position", MarkPriceSource: "unavailable",
		ResourceVersion: source.ResourceVersion,
		ObservedAt:      source.ObservedAt.UTC(), VerifiedAt: source.VerifiedAt.UTC(), ReceivedAt: receivedAt,
	}
	if item.MarkPrice != "" {
		item.MarkPriceSource = "venue_position"
	}
	if source.Position.LiquidationPrice.Valid {
		item.LiquidationPrice = source.Position.LiquidationPrice.Value
	}
	if source.Position.Leverage.Valid {
		item.Leverage = source.Position.Leverage.Value
	}
	if source.Position.MarginUsed.Valid {
		item.MarginUsed = source.Position.MarginUsed.Value
	}
	return decorate(item, account, state), nil
}

func decorate(item Item, account Account, state AccountState) Item {
	item.AccountLabel, item.Venue, item.Environment = account.Label, account.Venue, account.Environment
	item.RouteHealth, item.Freshness, item.WriteReady = state.RouteHealth, state.Freshness, state.WriteReady
	item.BlockedReason = state.Reason
	if !state.VerifiedAt.IsZero() {
		item.VerifiedAt = state.VerifiedAt
	}
	return item
}

func validateAccount(account Account) error {
	if strings.TrimSpace(account.AccountID) == "" || strings.TrimSpace(account.Venue) == "" || strings.TrimSpace(account.Environment) == "" {
		return fmt.Errorf("Gateway account directory response is invalid")
	}
	if account.Label == "" {
		account.Label = account.AccountID
	}
	seen := make(map[string]struct{}, len(account.Routes))
	for _, route := range account.Routes {
		if strings.TrimSpace(route.RouteID) == "" || strings.TrimSpace(route.Product) == "" {
			return fmt.Errorf("Gateway account route response is invalid")
		}
		if _, duplicate := seen[route.RouteID]; duplicate {
			return fmt.Errorf("Gateway account route response contains duplicate route")
		}
		seen[route.RouteID] = struct{}{}
	}
	return nil
}

func accountWriteReady(account Account, freshness Freshness) bool {
	if freshness != FreshnessLive || account.Status != "active" {
		return false
	}
	for _, route := range account.Routes {
		if route.RequestedState == "active" {
			return true
		}
	}
	return false
}

func newerEntity(at time.Time, version uint64, current entityWatermark) bool {
	at = at.UTC()
	if current.at.IsZero() {
		return !at.IsZero()
	}
	if at.After(current.at) {
		return true
	}
	return at.Equal(current.at) && version > current.version
}

func routeHealthFor(freshness Freshness) RouteHealth {
	switch freshness {
	case FreshnessLive:
		return RouteLive
	case FreshnessUnavailable:
		return RouteUnavailable
	default:
		return RouteDegraded
	}
}

func accountRoute(account Account, routeID string) (AccountRoute, bool) {
	for _, route := range account.Routes {
		if route.RouteID == routeID {
			return route, true
		}
	}
	return AccountRoute{}, false
}

func instrumentDisplay(instrumentID string) (product, settlement, symbol string) {
	parts := strings.Split(instrumentID, ":")
	if len(parts) >= 4 {
		product = parts[2]
		symbol = parts[len(parts)-1]
	}
	if len(parts) >= 5 {
		settlement = parts[len(parts)-2]
	}
	return product, settlement, symbol
}

func matches(item Item, filter Filter) bool {
	return (filter.AccountID == "" || item.AccountID == filter.AccountID) &&
		(filter.InstrumentID == "" || item.InstrumentID == filter.InstrumentID) &&
		(filter.Venue == "" || item.Venue == filter.Venue) &&
		(filter.Product == "" || item.Product == filter.Product) &&
		(filter.Status == "" || item.Freshness == filter.Status)
}

func snapshotVerifiedAt(accounts []AccountState) time.Time {
	var verified time.Time
	for _, state := range accounts {
		if state.VerifiedAt.IsZero() {
			continue
		}
		if verified.IsZero() || state.VerifiedAt.Before(verified) {
			verified = state.VerifiedAt
		}
	}
	return verified
}

func summarize(accounts []AccountState, items []Item) Summary {
	summary := Summary{PositionCount: len(items), AccountCount: len(accounts)}
	for _, state := range accounts {
		if state.Freshness == FreshnessStale || state.Freshness == FreshnessUnavailable {
			summary.StaleAccountCount++
		}
	}
	byAsset := make(map[string]*SettlementSummary)
	for _, item := range items {
		asset := item.SettlementAsset
		if asset == "" {
			asset = "UNKNOWN"
		}
		row := byAsset[asset]
		if row == nil {
			row = &SettlementSummary{SettlementAsset: asset}
			byAsset[asset] = row
		}
		row.PositionCount++
		if (item.Freshness == FreshnessLive || item.Freshness == FreshnessDelayed) && item.UnrealizedPnLSource != "unavailable" && item.UnrealizedPnL != "" {
			row.LiveCount++
			value, err := decimal.NewFromString(zeroDecimal(item.UnrealizedPnL))
			if err == nil {
				current, _ := decimal.NewFromString(zeroDecimal(row.UnrealizedPnL))
				row.UnrealizedPnL = current.Add(value).String()
			}
		} else {
			row.StaleCount++
			row.ExcludedPnLCount++
		}
	}
	assets := make([]string, 0, len(byAsset))
	for asset := range byAsset {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	for _, asset := range assets {
		summary.BySettlementAsset = append(summary.BySettlementAsset, *byAsset[asset])
	}
	return summary
}

func accountItems(view *principalProjection, accountID string) []Item {
	items := make([]Item, 0)
	for _, item := range view.items {
		if item.AccountID == accountID {
			items = append(items, item)
		}
	}
	return items
}

func zeroDecimal(value string) string {
	if strings.TrimSpace(value) == "" {
		return "0"
	}
	return value
}

func emptyZero(value string) string {
	parsed, err := decimal.NewFromString(zeroDecimal(value))
	if err != nil || parsed.IsZero() {
		return ""
	}
	return parsed.String()
}

func safeSourceReason(err error) string {
	if errors.Is(err, ErrResyncRequired) {
		return "event_resync_required"
	}
	return "position_stream_unavailable"
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
