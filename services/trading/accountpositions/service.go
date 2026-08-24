package accountpositions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/realtime"
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
}

// Service is Trading's user-level account position read model. It owns one
// projection per principal and one Gateway account subscription per discovered
// account. A failed account is retained as stale/unavailable and never clears
// a healthy sibling account.
type Service struct {
	source Source
	store  Store
	hub    *realtime.Hub
	now    func() time.Time

	ensureMu sync.Mutex
	mu       sync.RWMutex
	views    map[string]*principalProjection

	lifecycleMu sync.Mutex
	runCtx      context.Context
	watchers    map[string]context.CancelFunc
}

func NewService(source Source, store Store, hub *realtime.Hub, now func() time.Time) (*Service, error) {
	if source == nil || store == nil || hub == nil {
		return nil, fmt.Errorf("account position projection dependencies are incomplete")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{
		source: source, store: store, hub: hub, now: now,
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
		ProjectionRevision: view.revision, Cursor: service.hub.Cursor(), VerifiedAt: snapshotVerifiedAt(accounts),
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
	loaded := &principalProjection{hydrated: true, accounts: make(map[string]AccountState), items: make(map[string]Item), versions: make(map[string]uint64)}
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
	return state.Freshness != FreshnessLive || state.SourceCursor == ""
}

func (service *Service) replaceSnapshot(ctx context.Context, principal string, account Account, snapshot SourceSnapshot) error {
	if snapshot.AccountID != account.AccountID || strings.TrimSpace(snapshot.BootID) == "" || strings.TrimSpace(snapshot.Cursor) == "" || snapshot.VerifiedAt.IsZero() {
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
	state := AccountState{
		Account: account, RouteHealth: routeHealthFor(freshness), Freshness: freshness, WriteReady: accountWriteReady(account, freshness),
		Reason: snapshot.Reason, SourceBootID: snapshot.BootID, SourceSequence: snapshot.Sequence,
		SourceCursor: snapshot.Cursor, VerifiedAt: snapshot.VerifiedAt.UTC(), ReceivedAt: now,
	}
	items := make([]Item, 0, len(snapshot.Items))
	seen := make(map[string]struct{}, len(snapshot.Items))
	for _, source := range snapshot.Items {
		item, err := projectSourceItem(account, state, source, snapshot.BootID, snapshot.Sequence, now)
		if err != nil {
			return err
		}
		if item.Quantity == "0" {
			continue
		}
		if _, duplicate := seen[item.Key.String()]; duplicate {
			return fmt.Errorf("Gateway position snapshot contains duplicate position")
		}
		seen[item.Key.String()] = struct{}{}
		items = append(items, item)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	view := service.views[principal]
	if view == nil {
		return fmt.Errorf("account position principal projection is unavailable")
	}
	if err := service.store.ReplaceAccount(ctx, principal, state, items); err != nil {
		return err
	}
	removed := make([]Item, 0)
	for key, existing := range view.items {
		if existing.AccountID != account.AccountID {
			continue
		}
		if _, keep := seen[key]; !keep {
			removed = append(removed, existing)
			delete(view.items, key)
		}
	}
	view.accounts[account.AccountID] = cloneState(state)
	for key := range view.versions {
		if strings.HasPrefix(key, account.AccountID+"\x00") {
			delete(view.versions, key)
		}
	}
	for _, item := range items {
		view.items[item.Key.String()] = item
		view.versions[item.Key.String()] = item.ResourceVersion
	}
	for _, item := range removed {
		view.revision++
		_, _ = service.hub.PublishScoped(principal, "portfolio.position.remove", positionEventPayload(view.revision, OperationRemove, item, "snapshot_absent"))
	}
	for _, item := range items {
		view.revision++
		_, _ = service.hub.PublishScoped(principal, "portfolio.position.upsert", positionEventPayload(view.revision, OperationUpsert, item, ""))
	}
	view.revision++
	_, _ = service.hub.PublishScoped(principal, "portfolio.account.state", accountStatePayload(view.revision, state))
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
		// A transport connection is not proof of account state. In particular,
		// an initial REST snapshot may have failed before this watch connected;
		// promoting that account would turn an unknown state into a false empty
		// live account and persist the data loss illusion.
		if strings.TrimSpace(state.SourceBootID) == "" || strings.TrimSpace(state.SourceCursor) == "" || state.VerifiedAt.IsZero() {
			return ErrResyncRequired
		}
		state.RouteHealth, state.Freshness, state.Reason = RouteLive, FreshnessLive, ""
		state.WriteReady, state.VerifiedAt, state.ReceivedAt = accountWriteReady(state.Account, FreshnessLive), now, now
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
		_, err := service.hub.PublishScoped(principal, "portfolio.account.state", accountStatePayload(view.revision, state))
		return err
	}
	if event.Kind != SourceEventPosition || event.AccountID != accountID || event.BootID != state.SourceBootID || event.Sequence <= state.SourceSequence {
		if event.Kind == SourceEventPosition && event.BootID == state.SourceBootID && event.Sequence == state.SourceSequence {
			return nil
		}
		return ErrResyncRequired
	}
	if event.Operation != OperationUpsert && event.Operation != OperationRemove {
		return ErrResyncRequired
	}
	state.SourceSequence, state.SourceCursor = event.Sequence, event.Cursor
	state.RouteHealth, state.Freshness, state.Reason = RouteLive, FreshnessLive, ""
	state.WriteReady, state.VerifiedAt, state.ReceivedAt = accountWriteReady(state.Account, FreshnessLive), now, now
	source := SourceItem{
		AccountID: event.AccountID, RouteID: event.RouteID, InstrumentID: event.InstrumentID,
		PositionSlot: event.PositionSlot, Position: event.Position, ResourceVersion: event.ResourceVersion,
		ObservedAt: event.OccurredAt, VerifiedAt: now,
	}
	item, err := projectSourceItem(state.Account, state, source, event.BootID, event.Sequence, now)
	if err != nil {
		return err
	}
	key := item.Key.String()
	if event.ResourceVersion <= view.versions[key] {
		items := accountItems(view, accountID)
		for index := range items {
			items[index] = decorate(items[index], state.Account, state)
			view.items[items[index].Key.String()] = items[index]
		}
		if err := service.store.ReplaceAccount(ctx, principal, state, items); err != nil {
			return err
		}
		view.accounts[accountID] = state
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
	view.revision++
	switch event.Operation {
	case OperationUpsert:
		view.items[key] = item
		_, err = service.hub.PublishScoped(principal, "portfolio.position.upsert", positionEventPayload(view.revision, event.Operation, item, event.Reason))
	case OperationRemove:
		delete(view.items, key)
		_, err = service.hub.PublishScoped(principal, "portfolio.position.remove", positionEventPayload(view.revision, event.Operation, item, event.Reason))
	default:
		return ErrResyncRequired
	}
	return err
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
	backoff := reconnectMinimum
	for ctx.Err() == nil {
		service.mu.RLock()
		state, exists := service.views[principal].accounts[accountID]
		service.mu.RUnlock()
		if !exists {
			return
		}
		watchCtx, cancel := context.WithTimeout(ctx, integrityInterval)
		err := service.source.WatchPositionEvents(watchCtx, principal, accountID, state.SourceCursor, func(event SourceEvent) error {
			return service.applySourceEvent(watchCtx, principal, accountID, event)
		})
		periodic := errors.Is(watchCtx.Err(), context.DeadlineExceeded)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if !periodic {
			service.markAccountStale(ctx, principal, accountID, safeSourceReason(err))
			if !wait(ctx, backoff) {
				return
			}
			backoff *= 2
			if backoff > reconnectMaximum {
				backoff = reconnectMaximum
			}
		}
		account, ok := service.account(principal, accountID)
		if !ok {
			return
		}
		snapshot, snapshotErr := service.source.PositionSnapshot(ctx, principal, accountID)
		if snapshotErr != nil {
			service.markAccountStale(ctx, principal, accountID, "position_snapshot_unavailable")
			continue
		}
		if err := service.replaceSnapshot(ctx, principal, account, snapshot); err != nil {
			service.markAccountStale(ctx, principal, accountID, "position_snapshot_invalid")
			continue
		}
		backoff = reconnectMinimum
	}
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
	if strings.TrimSpace(state.SourceCursor) == "" && len(items) == 0 {
		state.RouteHealth, state.Freshness = RouteUnavailable, FreshnessUnavailable
	} else {
		state.RouteHealth, state.Freshness = RouteDegraded, FreshnessStale
	}
	state.WriteReady = false
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
	_, _ = service.hub.PublishScoped(principal, "portfolio.account.state", accountStatePayload(view.revision, state))
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
	_, _ = service.hub.PublishScoped(principal, "portfolio.account.state", accountStatePayload(view.revision, state))
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
		for key, item := range view.items {
			if item.AccountID == accountID {
				delete(view.items, key)
				view.revision++
				_, _ = service.hub.PublishScoped(principal, "portfolio.position.remove", positionEventPayload(view.revision, OperationRemove, item, "account_removed"))
			}
		}
		for key := range view.versions {
			if strings.HasPrefix(key, accountID+"\x00") {
				delete(view.versions, key)
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

func projectSourceItem(account Account, state AccountState, source SourceItem, bootID string, sequence uint64, receivedAt time.Time) (Item, error) {
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
		SourceBootID: bootID, SourceSequence: sequence, ResourceVersion: source.ResourceVersion,
		ObservedAt: source.ObservedAt.UTC(), VerifiedAt: source.VerifiedAt.UTC(), ReceivedAt: receivedAt,
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

func positionEventPayload(revision uint64, operation Operation, item Item, reason string) map[string]any {
	return map[string]any{"projectionRevision": revision, "operation": operation, "key": item.Key, "item": item, "reason": reason}
}

func accountStatePayload(revision uint64, state AccountState) map[string]any {
	return map[string]any{"projectionRevision": revision, "account": state}
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
