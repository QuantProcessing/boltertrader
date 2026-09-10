package event

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

type PositionFreshness string

const (
	PositionSyncing     PositionFreshness = "syncing"
	PositionLive        PositionFreshness = "live"
	PositionDelayed     PositionFreshness = "delayed"
	PositionStale       PositionFreshness = "stale"
	PositionUnavailable PositionFreshness = "unavailable"
)

type PositionOperation string

const (
	PositionUpsert PositionOperation = "upsert"
	PositionRemove PositionOperation = "remove"
)

type ProjectedPosition struct {
	AccountID       domain.AccountID       `json:"account_id"`
	RouteID         domain.RouteID         `json:"route_id"`
	InstrumentID    domain.InstrumentID    `json:"instrument_id"`
	PositionSlot    exchange.PositionSlot  `json:"position_slot"`
	Position        exchange.Position      `json:"position"`
	ResourceVersion domain.ResourceVersion `json:"resource_version"`
	ObservedAt      time.Time              `json:"observed_at"`
	VerifiedAt      time.Time              `json:"verified_at"`
}

type PositionSeed struct {
	RouteID      domain.RouteID
	InstrumentID domain.InstrumentID
	Position     exchange.Position
	ObservedAt   time.Time
}

type PositionSnapshot struct {
	AccountID  domain.AccountID    `json:"account_id"`
	Freshness  PositionFreshness   `json:"freshness"`
	Reason     string              `json:"reason,omitempty"`
	VerifiedAt time.Time           `json:"verified_at"`
	Items      []ProjectedPosition `json:"items"`
}

type positionKey struct {
	accountID    domain.AccountID
	routeID      domain.RouteID
	instrumentID domain.InstrumentID
	slot         exchange.PositionSlot
}

type positionAccount struct {
	principal   domain.PrincipalID
	initialized bool
	freshness   PositionFreshness
	reason      string
	verifiedAt  time.Time
	items       map[positionKey]ProjectedPosition
}

type positionMutation struct {
	operation PositionOperation
	item      ProjectedPosition
	reason    string
}

// PositionProjection is the Gateway-owned current-position view. REST
// snapshots and WebSocket events are merged by entity observation time; a REST
// request never blocks the live stream and cannot overwrite a newer event.
type PositionProjection struct {
	hub *Hub
	now func() time.Time

	mu       sync.RWMutex
	accounts map[domain.AccountID]*positionAccount
	versions map[positionKey]domain.ResourceVersion
}

func NewPositionProjection(hub *Hub, now func() time.Time) (*PositionProjection, error) {
	if hub == nil {
		return nil, fmt.Errorf("position projection event hub is required")
	}
	if now == nil {
		now = time.Now
	}
	return &PositionProjection{
		hub: hub, now: now, accounts: make(map[domain.AccountID]*positionAccount),
		versions: make(map[positionKey]domain.ResourceVersion),
	}, nil
}

func (projection *PositionProjection) RemoveAccount(accountID domain.AccountID) {
	projection.mu.Lock()
	defer projection.mu.Unlock()
	delete(projection.accounts, accountID)
	for key := range projection.versions {
		if key.accountID == accountID {
			delete(projection.versions, key)
		}
	}
}

// Apply consumes one instrument-scoped venue event. EventSnapshot is a
// complete set for that route/instrument and removes absent slots; EventDelta
// changes only the named slots. A zero quantity is an explicit close.
func (projection *PositionProjection) Apply(
	principal domain.PrincipalID,
	accountID domain.AccountID,
	routeID domain.RouteID,
	instrumentID domain.InstrumentID,
	event exchange.PositionEvent,
) error {
	if err := validatePositionScope(principal, accountID, routeID, instrumentID); err != nil {
		return err
	}
	if event.Kind != exchange.EventSnapshot && event.Kind != exchange.EventDelta {
		return fmt.Errorf("position event kind is invalid")
	}
	verifiedAt := projection.now().UTC()
	observedAt := event.Time.UTC()
	if observedAt.IsZero() {
		observedAt = verifiedAt
	}
	positions, err := normalizePositions(event.Positions)
	if err != nil {
		return err
	}

	projection.mu.Lock()
	account, err := projection.accountLocked(principal, accountID)
	if err != nil {
		projection.mu.Unlock()
		return err
	}
	account.initialized = true
	account.freshness = PositionLive
	account.reason = ""
	if verifiedAt.After(account.verifiedAt) {
		account.verifiedAt = verifiedAt
	}
	mutations := projection.applyLocked(account, accountID, routeID, instrumentID, event.Kind, positions, observedAt, verifiedAt)
	projection.mu.Unlock()
	return projection.publish(principal, mutations)
}

// ReplaceAccount installs one complete REST snapshot and publishes the exact
// upserts/removes needed by existing subscribers. It is intentionally account
// wide: an empty complete snapshot is the only safe proof that every previous
// row should be removed.
func (projection *PositionProjection) ReplaceAccount(
	principal domain.PrincipalID,
	accountID domain.AccountID,
	seeds []PositionSeed,
) (PositionSnapshot, error) {
	return projection.replaceAccount(principal, accountID, seeds, projection.now().UTC())
}

// SynchronizeAccount records the REST request boundary, loads without holding
// a projection lock, then merges the response. Rows changed by WebSocket after
// the boundary win even when the REST response arrives later.
func (projection *PositionProjection) SynchronizeAccount(
	principal domain.PrincipalID,
	accountID domain.AccountID,
	load func() ([]PositionSeed, error),
) (PositionSnapshot, error) {
	if load == nil {
		return PositionSnapshot{}, fmt.Errorf("position snapshot loader is required")
	}
	boundary := projection.now().UTC()
	seeds, err := load()
	if err != nil {
		return PositionSnapshot{}, err
	}
	return projection.replaceAccount(principal, accountID, seeds, boundary)
}

func (projection *PositionProjection) replaceAccount(
	principal domain.PrincipalID,
	accountID domain.AccountID,
	seeds []PositionSeed,
	boundary time.Time,
) (PositionSnapshot, error) {
	if err := principal.Validate(); err != nil {
		return PositionSnapshot{}, err
	}
	if err := accountID.Validate(); err != nil {
		return PositionSnapshot{}, err
	}
	if boundary.IsZero() {
		boundary = projection.now().UTC()
	}
	boundary = boundary.UTC()
	normalized, err := normalizePositionSeeds(seeds, boundary)
	if err != nil {
		return PositionSnapshot{}, err
	}

	projection.mu.Lock()
	account, err := projection.accountLocked(principal, accountID)
	if err != nil {
		projection.mu.Unlock()
		return PositionSnapshot{}, err
	}
	incoming := make(map[positionKey]PositionSeed, len(normalized))
	seen := make(map[positionKey]struct{}, len(normalized))
	for _, seed := range normalized {
		key := positionKey{accountID: accountID, routeID: seed.RouteID, instrumentID: seed.InstrumentID, slot: seed.Position.PositionSlot}
		if _, duplicate := seen[key]; duplicate {
			projection.mu.Unlock()
			return PositionSnapshot{}, fmt.Errorf("position snapshot contains a duplicate slot")
		}
		seen[key] = struct{}{}
		if !seed.Position.Quantity.IsZero() {
			incoming[key] = seed
		}
	}
	mutations := make([]positionMutation, 0, len(account.items)+len(incoming))
	for key, existing := range account.items {
		if _, keep := incoming[key]; keep {
			continue
		}
		if existing.ObservedAt.After(boundary) {
			continue
		}
		version := projection.nextVersionLocked(key)
		existing.ResourceVersion = version
		existing.ObservedAt = boundary
		existing.VerifiedAt = boundary
		delete(account.items, key)
		mutations = append(mutations, positionMutation{operation: PositionRemove, item: existing, reason: "snapshot_absent"})
	}
	for key, seed := range incoming {
		if existing, exists := account.items[key]; exists && seed.ObservedAt.Before(existing.ObservedAt) {
			continue
		}
		version := projection.nextVersionLocked(key)
		item := ProjectedPosition{
			AccountID: accountID, RouteID: seed.RouteID, InstrumentID: seed.InstrumentID,
			PositionSlot: seed.Position.PositionSlot, Position: seed.Position, ResourceVersion: version,
			ObservedAt: seed.ObservedAt, VerifiedAt: boundary,
		}
		account.items[key] = item
		mutations = append(mutations, positionMutation{operation: PositionUpsert, item: item})
	}
	account.initialized = true
	account.freshness = PositionLive
	account.reason = ""
	if boundary.After(account.verifiedAt) {
		account.verifiedAt = boundary
	}
	projection.mu.Unlock()

	if err := projection.publish(principal, mutations); err != nil {
		return PositionSnapshot{}, err
	}
	return projection.snapshot(principal, accountID)
}

func (projection *PositionProjection) Snapshot(principal domain.PrincipalID, accountID domain.AccountID) (PositionSnapshot, error) {
	return projection.snapshot(principal, accountID)
}

func (projection *PositionProjection) snapshot(principal domain.PrincipalID, accountID domain.AccountID) (PositionSnapshot, error) {
	if err := principal.Validate(); err != nil {
		return PositionSnapshot{}, err
	}
	if err := accountID.Validate(); err != nil {
		return PositionSnapshot{}, err
	}
	projection.mu.RLock()
	defer projection.mu.RUnlock()
	account := projection.accounts[accountID]
	if account == nil || account.principal != principal || !account.initialized {
		return PositionSnapshot{}, fmt.Errorf("position projection is not synchronized")
	}
	items := make([]ProjectedPosition, 0, len(account.items))
	for _, item := range account.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].RouteID != items[j].RouteID {
			return items[i].RouteID < items[j].RouteID
		}
		if items[i].InstrumentID != items[j].InstrumentID {
			return items[i].InstrumentID < items[j].InstrumentID
		}
		return items[i].PositionSlot < items[j].PositionSlot
	})
	return PositionSnapshot{
		AccountID: accountID,
		Freshness: account.freshness, Reason: account.reason, VerifiedAt: account.verifiedAt, Items: items,
	}, nil
}

func (projection *PositionProjection) applyLocked(
	account *positionAccount,
	accountID domain.AccountID,
	routeID domain.RouteID,
	instrumentID domain.InstrumentID,
	kind exchange.EventKind,
	positions []exchange.Position,
	observedAt time.Time,
	verifiedAt time.Time,
) []positionMutation {
	incoming := make(map[positionKey]exchange.Position, len(positions))
	for _, position := range positions {
		key := positionKey{accountID: accountID, routeID: routeID, instrumentID: instrumentID, slot: position.PositionSlot}
		incoming[key] = position
	}
	mutations := make([]positionMutation, 0, len(incoming))
	if kind == exchange.EventSnapshot {
		for key, existing := range account.items {
			if key.routeID != routeID || key.instrumentID != instrumentID {
				continue
			}
			if _, keep := incoming[key]; keep {
				continue
			}
			if existing.ObservedAt.After(observedAt) {
				continue
			}
			existing.ResourceVersion = projection.nextVersionLocked(key)
			existing.ObservedAt = observedAt
			existing.VerifiedAt = verifiedAt
			delete(account.items, key)
			mutations = append(mutations, positionMutation{operation: PositionRemove, item: existing, reason: "snapshot_absent"})
		}
	}
	for key, position := range incoming {
		existing, exists := account.items[key]
		if exists && observedAt.Before(existing.ObservedAt) {
			continue
		}
		if position.Quantity.IsZero() && !exists {
			continue
		}
		version := projection.nextVersionLocked(key)
		item := ProjectedPosition{
			AccountID: accountID, RouteID: routeID, InstrumentID: instrumentID,
			PositionSlot: position.PositionSlot, Position: position, ResourceVersion: version,
			ObservedAt: observedAt, VerifiedAt: verifiedAt,
		}
		if position.Quantity.IsZero() {
			if exists {
				existing.ResourceVersion = version
				existing.ObservedAt = observedAt
				existing.VerifiedAt = verifiedAt
				item = existing
			}
			delete(account.items, key)
			mutations = append(mutations, positionMutation{operation: PositionRemove, item: item, reason: "closed"})
			continue
		}
		account.items[key] = item
		mutations = append(mutations, positionMutation{operation: PositionUpsert, item: item})
	}
	return mutations
}

func (projection *PositionProjection) publish(principal domain.PrincipalID, mutations []positionMutation) error {
	for _, mutation := range mutations {
		payload, err := json.Marshal(PositionPayload{
			AccountID: mutation.item.AccountID, RouteID: mutation.item.RouteID,
			InstrumentID: mutation.item.InstrumentID, PositionSlot: mutation.item.PositionSlot,
			Operation: mutation.operation, Position: mutation.item.Position, Reason: mutation.reason,
		})
		if err != nil {
			return err
		}
		eventID, err := projection.hub.identity.EventID()
		if err != nil {
			return err
		}
		if _, err := projection.hub.Publish(PublishRequest{
			PrincipalID: principal, AccountID: mutation.item.AccountID, EventID: eventID,
			ResourceVersion: mutation.item.ResourceVersion, Kind: KindPosition,
			OccurredAt: mutation.item.ObservedAt, Payload: payload,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (projection *PositionProjection) accountLocked(principal domain.PrincipalID, accountID domain.AccountID) (*positionAccount, error) {
	account := projection.accounts[accountID]
	if account == nil {
		account = &positionAccount{
			principal: principal, freshness: PositionSyncing,
			items: make(map[positionKey]ProjectedPosition),
		}
		projection.accounts[accountID] = account
		return account, nil
	}
	if account.principal != principal {
		return nil, fmt.Errorf("position projection principal conflicts with account ownership")
	}
	return account, nil
}

func (projection *PositionProjection) nextVersionLocked(key positionKey) domain.ResourceVersion {
	projection.versions[key]++
	return projection.versions[key]
}

func validatePositionScope(principal domain.PrincipalID, accountID domain.AccountID, routeID domain.RouteID, instrumentID domain.InstrumentID) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if err := accountID.Validate(); err != nil {
		return err
	}
	if err := routeID.Validate(); err != nil {
		return err
	}
	return instrumentID.Validate()
}

func normalizePositions(positions []exchange.Position) ([]exchange.Position, error) {
	hasLong := false
	hasShort := false
	for _, position := range positions {
		hasLong = hasLong || position.PositionSlot == exchange.PositionSlotLong || position.Side == exchange.SideBuy
		hasShort = hasShort || position.PositionSlot == exchange.PositionSlotShort || position.Side == exchange.SideSell
	}
	heuristicHedge := len(positions) > 1 && hasLong && hasShort
	result := make([]exchange.Position, 0, len(positions))
	seen := make(map[exchange.PositionSlot]struct{}, len(positions))
	for _, position := range positions {
		if position.Side != exchange.SideBuy && position.Side != exchange.SideSell && !position.Quantity.IsZero() {
			return nil, fmt.Errorf("position side is invalid")
		}
		slot := position.PositionSlot
		if !slot.Valid() {
			slot = exchange.PositionSlotNet
			if heuristicHedge {
				if position.Side == exchange.SideBuy {
					slot = exchange.PositionSlotLong
				} else if position.Side == exchange.SideSell {
					slot = exchange.PositionSlotShort
				}
			}
		}
		if _, duplicate := seen[slot]; duplicate {
			return nil, fmt.Errorf("position event contains a duplicate slot")
		}
		seen[slot] = struct{}{}
		position.PositionSlot = slot
		if position.Side == exchange.SideSell {
			position.Quantity = position.Quantity.Abs().Neg()
		} else if position.Side == exchange.SideBuy {
			position.Quantity = position.Quantity.Abs()
		}
		result = append(result, position)
	}
	return result, nil
}

func normalizePositionSeeds(seeds []PositionSeed, verifiedAt time.Time) ([]PositionSeed, error) {
	type scope struct {
		routeID      domain.RouteID
		instrumentID domain.InstrumentID
	}
	groups := make(map[scope][]int)
	normalized := append([]PositionSeed(nil), seeds...)
	for index, seed := range normalized {
		if err := seed.RouteID.Validate(); err != nil {
			return nil, err
		}
		if err := seed.InstrumentID.Validate(); err != nil {
			return nil, err
		}
		groups[scope{routeID: seed.RouteID, instrumentID: seed.InstrumentID}] = append(
			groups[scope{routeID: seed.RouteID, instrumentID: seed.InstrumentID}], index,
		)
		if seed.ObservedAt.IsZero() {
			normalized[index].ObservedAt = verifiedAt
		} else {
			normalized[index].ObservedAt = seed.ObservedAt.UTC()
		}
	}
	for _, indexes := range groups {
		positions := make([]exchange.Position, 0, len(indexes))
		for _, index := range indexes {
			positions = append(positions, normalized[index].Position)
		}
		positions, err := normalizePositions(positions)
		if err != nil {
			return nil, err
		}
		for offset, index := range indexes {
			normalized[index].Position = positions[offset]
		}
	}
	return normalized, nil
}
