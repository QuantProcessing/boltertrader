// Package cache is the runtime's authoritative in-memory state store for orders,
// positions, and balances. TradingNode serializes live and reconciliation
// writes; an RWMutex also keeps direct users and reporting readers safe.
package cache

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/accounting"
	"github.com/QuantProcessing/boltertrader/runtime/orderstate"
	"github.com/shopspring/decimal"
)

// orderKey identifies an order inside one logical runtime account and one ID
// namespace. Client and venue IDs may contain the same text while referring to
// different orders, so the namespace is part of the key.
type orderKey struct {
	accountID string
	namespace string
	id        string
}

const (
	orderNamespaceClient = "client"
	orderNamespaceVenue  = "venue"
)

// positionKey identifies a position by account, instrument and side (hedge mode
// can hold a long and a short leg for the same instrument simultaneously).
type positionKey struct {
	accountID  string
	instrument string
	side       enums.PositionSide
}

type balanceKey struct {
	accountID string
	currency  string
}

// ErrFillOrderIdentityConflict means a fill's non-empty account, instrument,
// side, client alias, or venue alias contradicts the cached order identity.
// Callers must fail closed instead of applying or materializing such a fill.
var ErrFillOrderIdentityConflict = errors.New("cache: fill order identity conflict")

// ErrOrderIdentityConflict means an order update would bind a known client ID
// to a different known venue order ID (or vice versa). Checked runtime paths
// fail closed instead of silently dropping such an update.
var ErrOrderIdentityConflict = errors.New("cache: order identity conflict")

// ErrOrderClientIDExists means a submit tried to reserve a ClientID already
// represented by an order in the same logical account scope.
var ErrOrderClientIDExists = errors.New("cache: order client id already exists")

type orderMergeCandidate struct {
	key   orderKey
	order model.Order
}

type orderUpsertPlan struct {
	key     orderKey
	order   model.Order
	deletes []orderKey
}

type terminalOrderRef struct {
	key     orderKey
	version uint64
}

// Cache holds the live trading state.
type Cache struct {
	// orderMu serializes every order mutation and order-identity transaction.
	// Transaction callbacks run without mu so durable stores may read unrelated
	// or pre-commit cache state without deadlocking. They must not invoke an
	// order-mutating Cache method because orderMu remains held.
	orderMu           sync.Mutex
	mu                sync.RWMutex
	orders            map[orderKey]model.Order
	positions         map[positionKey]model.Position
	balances          map[balanceKey]model.AccountBalance
	market            map[string]*marketState // keyed by InstrumentID.String()
	accounts          map[string]accounting.Account
	accountByVenue    map[string]map[string]struct{}
	accountStaleAfter time.Duration
	terminalLimit     int
	terminalCount     int
	terminalVersion   uint64
	terminalVersions  map[orderKey]uint64
	terminalOrder     []terminalOrderRef
}

// defaultTerminalOrderLimit bounds queryable terminal order history. Open and
// UNKNOWN orders are reconciliation state and are never counted against it.
const defaultTerminalOrderLimit = 100_000

// New returns an empty Cache.
func New() *Cache {
	return NewWithTerminalOrderLimit(defaultTerminalOrderLimit)
}

// NewWithTerminalOrderLimit returns an empty Cache that retains at most limit
// evictable terminal orders (FILLED/CANCELED/REJECTED/EXPIRED). Nonterminal and
// UNKNOWN orders remain until authoritative reconciliation resolves them. Once
// a terminal order leaves this FIFO history window, Order lookups no longer
// return it. A non-positive limit uses the conservative production default.
func NewWithTerminalOrderLimit(limit int) *Cache {
	if limit <= 0 {
		limit = defaultTerminalOrderLimit
	}
	return &Cache{
		orders:            make(map[orderKey]model.Order),
		positions:         make(map[positionKey]model.Position),
		balances:          make(map[balanceKey]model.AccountBalance),
		market:            make(map[string]*marketState),
		accounts:          make(map[string]accounting.Account),
		accountByVenue:    make(map[string]map[string]struct{}),
		accountStaleAfter: accounting.DefaultStaleAfter,
		terminalLimit:     limit,
		terminalVersions:  make(map[orderKey]uint64),
	}
}

func (c *Cache) SetAccountStaleAfter(staleAfter time.Duration) {
	if staleAfter <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accountStaleAfter = staleAfter
}

func keyForOrder(o model.Order) orderKey {
	if o.Request.ClientID != "" {
		return orderKey{accountID: o.Request.AccountID, namespace: orderNamespaceClient, id: o.Request.ClientID}
	}
	return orderKey{accountID: o.Request.AccountID, namespace: orderNamespaceVenue, id: o.VenueOrderID}
}

func orderLookupMatches(o model.Order, key string) bool {
	return key != "" && (o.Request.ClientID == key || o.VenueOrderID == key)
}

func ordersShareTypedIdentity(left, right model.Order) bool {
	return (left.Request.ClientID != "" && left.Request.ClientID == right.Request.ClientID) ||
		(left.VenueOrderID != "" && left.VenueOrderID == right.VenueOrderID)
}

func orderAliasesCompatible(left, right model.Order) bool {
	return (left.Request.ClientID == "" || right.Request.ClientID == "" || left.Request.ClientID == right.Request.ClientID) &&
		(left.VenueOrderID == "" || right.VenueOrderID == "" || left.VenueOrderID == right.VenueOrderID)
}

func orderAccountIDsMergeable(a, b string) bool {
	return a == "" || b == "" || a == b
}

func orderAccountMatches(accountID string, o model.Order) bool {
	return accountID == "" || o.Request.AccountID == "" || o.Request.AccountID == accountID
}

func orderCandidateAccountID(candidate orderMergeCandidate) string {
	if candidate.order.Request.AccountID != "" {
		return candidate.order.Request.AccountID
	}
	return candidate.key.accountID
}

func orderMergeCandidatesUnambiguous(incomingAccountID string, candidates []orderMergeCandidate) bool {
	scope := incomingAccountID
	for _, candidate := range candidates {
		accountID := orderCandidateAccountID(candidate)
		if accountID == "" {
			continue
		}
		if scope == "" {
			scope = accountID
			continue
		}
		if scope != accountID {
			return false
		}
	}
	return true
}

// UpsertOrder inserts or replaces an order. Called from the bus goroutine.
func (c *Cache) UpsertOrder(o model.Order) {
	_ = c.UpsertOrderChecked(o)
}

// UpsertOrderChecked inserts or replaces an order and reports typed identity
// collisions that the compatibility UpsertOrder API historically ignored.
func (c *Cache) UpsertOrderChecked(o model.Order) error {
	c.orderMu.Lock()
	defer c.orderMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.upsertOrderLocked(o, orderstate.Merge)
}

// CommitOrderUpsertChecked validates an order update, invokes commit while the
// order identity namespace remains locked, and applies the update only after
// commit succeeds. commit may read Cache, but must not invoke an order-mutating
// Cache method. This gives execution paths one linearization point for durable
// command results and their cache effect: a competing order cannot claim an
// alias between the two operations.
func (c *Cache) CommitOrderUpsertChecked(o model.Order, commit func() error) error {
	c.orderMu.Lock()
	defer c.orderMu.Unlock()
	c.mu.Lock()
	plan, err := c.prepareOrderUpsertLocked(o, orderstate.Merge)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if commit != nil {
		if err := commit(); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.applyOrderUpsertPlanLocked(plan)
	c.mu.Unlock()
	return nil
}

// CommitOrderMutationByClientIDChecked reads the latest canonical order for a
// logical ClientID while the order namespace is locked, derives a same-identity
// replacement with mutate, durably commits it, and only then publishes it.
// This is the command-response transaction for same-incarnation Cancel/Modify:
// venue events that arrived during the request are therefore preserved instead
// of being overwritten by the pre-request snapshot. mutate and commit may read
// Cache, but neither may invoke an order-mutating Cache method.
func (c *Cache) CommitOrderMutationByClientIDChecked(
	accountID, clientID, expectedVenueOrderID string,
	mutate func(model.Order) (model.Order, error),
	commit func() error,
) (model.Order, error) {
	if strings.TrimSpace(clientID) == "" {
		return model.Order{}, orderIdentityConflict("order mutation requires a client id")
	}
	if mutate == nil {
		return model.Order{}, errors.New("cache: order mutation callback is nil")
	}

	c.orderMu.Lock()
	defer c.orderMu.Unlock()

	c.mu.Lock()
	candidate, found, ambiguous := c.typedOrderCandidateLocked(accountID, orderNamespaceClient, clientID)
	if ambiguous || !found {
		c.mu.Unlock()
		return model.Order{}, orderIdentityConflict(
			"logical client id %q is not uniquely cached in account %q",
			clientID,
			accountID,
		)
	}
	current := candidate.order
	if expectedVenueOrderID != "" && current.VenueOrderID != expectedVenueOrderID {
		c.mu.Unlock()
		return model.Order{}, orderIdentityConflict(
			"logical client id %q moved from expected venue order id %q to %q",
			clientID,
			expectedVenueOrderID,
			current.VenueOrderID,
		)
	}
	c.mu.Unlock()

	next, err := mutate(current)
	if err != nil {
		return model.Order{}, err
	}
	if next.Request.ClientID == "" {
		next.Request.ClientID = clientID
	}
	if next.VenueOrderID == "" {
		next.VenueOrderID = current.VenueOrderID
	}
	currentAccountID := current.Request.AccountID
	if currentAccountID == "" {
		currentAccountID = candidate.key.accountID
	}
	if next.Request.AccountID == "" {
		next.Request.AccountID = currentAccountID
	}
	if next.Request.ClientID != clientID ||
		next.VenueOrderID != current.VenueOrderID ||
		next.Request.AccountID != currentAccountID ||
		!orderCoreIdentityCompatible(current, next) {
		return model.Order{}, orderIdentityConflict(
			"mutation changed the identity of logical client id %q",
			clientID,
		)
	}
	if keyForOrder(next) != candidate.key {
		return model.Order{}, orderIdentityConflict(
			"mutation changed the typed cache key for logical client id %q",
			clientID,
		)
	}
	if commit != nil {
		if err := commit(); err != nil {
			return model.Order{}, err
		}
	}

	c.mu.Lock()
	c.applyOrderUpsertPlanLocked(orderUpsertPlan{key: candidate.key, order: next})
	c.mu.Unlock()
	return next, nil
}

// ValidateOrderUpsertAndCommit holds the order identity namespace stable while
// commit runs but deliberately leaves cache state unchanged. It is used for
// ambiguous or rejected command responses whose aliases must be checked before
// a durable result is written but are not authoritative cache state. commit may
// read Cache, but must not invoke an order-mutating Cache method.
func (c *Cache) ValidateOrderUpsertAndCommit(o model.Order, commit func() error) error {
	c.orderMu.Lock()
	defer c.orderMu.Unlock()
	c.mu.Lock()
	_, err := c.prepareOrderUpsertLocked(o, orderstate.Merge)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if commit == nil {
		return nil
	}
	return commit()
}

// CommitOrderVenueAliasChangeChecked atomically replaces the venue-order alias
// of one known logical ClientID, invokes commit, and applies the replacement.
// It is intentionally separate from ordinary UpsertOrderChecked: changing a
// non-empty venue alias is valid for confirmed replacement-style Modify
// commands, but must remain an identity conflict everywhere else. commit may
// read Cache, but must not invoke an order-mutating Cache method.
func (c *Cache) CommitOrderVenueAliasChangeChecked(
	accountID, clientID, expectedVenueOrderID string,
	replacement model.Order,
	commit func() error,
) error {
	c.orderMu.Lock()
	defer c.orderMu.Unlock()
	c.mu.Lock()
	plan, err := c.prepareOrderVenueAliasChangeLocked(accountID, clientID, expectedVenueOrderID, replacement)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if commit != nil {
		if err := commit(); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.applyOrderUpsertPlanLocked(plan)
	c.mu.Unlock()
	return nil
}

// ValidateOrderVenueAliasChangeAndCommit verifies a possible replacement alias
// and keeps the namespace locked through commit without changing the canonical
// order. Ambiguous and rejected Modify results use this path because the old
// order may still be live. commit may read Cache, but must not invoke an
// order-mutating Cache method.
func (c *Cache) ValidateOrderVenueAliasChangeAndCommit(
	accountID, clientID, expectedVenueOrderID string,
	replacement model.Order,
	commit func() error,
) error {
	c.orderMu.Lock()
	defer c.orderMu.Unlock()
	c.mu.Lock()
	_, err := c.prepareOrderVenueAliasChangeLocked(accountID, clientID, expectedVenueOrderID, replacement)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if commit == nil {
		return nil
	}
	return commit()
}

// InsertPendingOrderIfAbsent atomically establishes the PendingNew cache
// record that linearizes a submit's venue handoff. It never merges with an
// existing ClientID, so an order observed while intent journaling was blocked
// prevents the submit from crossing the venue boundary.
func (c *Cache) InsertPendingOrderIfAbsent(o model.Order) error {
	if strings.TrimSpace(o.Request.ClientID) == "" {
		return fmt.Errorf("%w: empty client id", ErrOrderClientIDExists)
	}
	if o.Status != enums.StatusPendingNew {
		return fmt.Errorf("cache: pending order insertion requires PENDING_NEW status, got %s", o.Status)
	}
	c.orderMu.Lock()
	defer c.orderMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	_, found, ambiguous := c.typedOrderCandidateLocked(o.Request.AccountID, orderNamespaceClient, o.Request.ClientID)
	if found || ambiguous {
		return fmt.Errorf("%w %q", ErrOrderClientIDExists, o.Request.ClientID)
	}
	plan, err := c.prepareOrderUpsertLocked(o, orderstate.Merge)
	if err != nil {
		return err
	}
	c.applyOrderUpsertPlanLocked(plan)
	return nil
}

// ValidateOrderUpsert performs the same typed identity checks as
// UpsertOrderChecked without mutating cache state. Execution uses it before
// durably recording a venue acknowledgement, then performs a checked upsert.
func (c *Cache) ValidateOrderUpsert(o model.Order) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, err := c.prepareOrderUpsertLocked(o, orderstate.Merge)
	return err
}

// UpsertOrderSnapshot applies an authoritative cumulative venue snapshot.
// observedAt is the time through which the report is known to be current; a
// locally applied order event newer than that point is preserved.
func (c *Cache) UpsertOrderSnapshot(o model.Order, observedAt time.Time) {
	c.orderMu.Lock()
	defer c.orderMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.upsertOrderLocked(o, func(existing, incoming model.Order) model.Order {
		return orderstate.MergeSnapshot(existing, incoming, observedAt)
	})
}

func (c *Cache) upsertOrderLocked(o model.Order, merge func(model.Order, model.Order) model.Order) error {
	plan, err := c.prepareOrderUpsertLocked(o, merge)
	if err != nil {
		return err
	}
	c.applyOrderUpsertPlanLocked(plan)
	return nil
}

func (c *Cache) prepareOrderUpsertLocked(o model.Order, merge func(model.Order, model.Order) model.Order) (orderUpsertPlan, error) {
	k := keyForOrder(o)
	if existing, ok := c.orders[k]; ok {
		if !orderAliasesCompatible(existing, o) {
			return orderUpsertPlan{}, orderIdentityConflict("order %q conflicts with the existing typed identity", k.id)
		}
		o = merge(existing, o)
	}
	var candidates []orderMergeCandidate
	for key, existing := range c.orders {
		if key == k || !orderAccountIDsMergeable(key.accountID, k.accountID) {
			continue
		}
		if ordersShareTypedIdentity(existing, o) {
			if !orderAliasesCompatible(existing, o) {
				return orderUpsertPlan{}, orderIdentityConflict("client id %q and venue order id %q conflict with different cached aliases", o.Request.ClientID, o.VenueOrderID)
			}
			candidates = append(candidates, orderMergeCandidate{key: key, order: existing})
		}
	}
	unambiguous := orderMergeCandidatesUnambiguous(k.accountID, candidates)
	if !unambiguous && k.accountID == "" {
		return orderUpsertPlan{}, orderIdentityConflict("order aliases are ambiguous across accounts")
	}
	plan := orderUpsertPlan{}
	if unambiguous {
		for _, candidate := range candidates {
			o = merge(candidate.order, o)
			plan.deletes = append(plan.deletes, candidate.key)
		}
	}
	finalKey := keyForOrder(o)
	if finalKey != k {
		plan.deletes = append(plan.deletes, k)
	}
	plan.key = finalKey
	plan.order = o
	return plan, nil
}

func (c *Cache) prepareOrderVenueAliasChangeLocked(
	accountID, clientID, expectedVenueOrderID string,
	replacement model.Order,
) (orderUpsertPlan, error) {
	if strings.TrimSpace(clientID) == "" {
		return orderUpsertPlan{}, orderIdentityConflict("venue alias change requires a client id")
	}
	if replacement.Request.ClientID != "" && replacement.Request.ClientID != clientID {
		return orderUpsertPlan{}, orderIdentityConflict(
			"replacement client id %q does not match logical client id %q",
			replacement.Request.ClientID,
			clientID,
		)
	}
	if replacement.Request.AccountID != "" && accountID != "" && replacement.Request.AccountID != accountID {
		return orderUpsertPlan{}, orderIdentityConflict(
			"replacement account %q does not match logical account %q",
			replacement.Request.AccountID,
			accountID,
		)
	}

	currentCandidate, found, ambiguous := c.typedOrderCandidateLocked(accountID, orderNamespaceClient, clientID)
	if ambiguous || !found {
		return orderUpsertPlan{}, orderIdentityConflict(
			"logical client id %q is not uniquely cached in account %q",
			clientID,
			accountID,
		)
	}
	current := currentCandidate.order
	if expectedVenueOrderID != "" && current.VenueOrderID != expectedVenueOrderID && current.VenueOrderID != replacement.VenueOrderID {
		return orderUpsertPlan{}, orderIdentityConflict(
			"logical client id %q moved from expected venue order id %q to %q",
			clientID,
			expectedVenueOrderID,
			current.VenueOrderID,
		)
	}
	if replacement.VenueOrderID == "" {
		replacement.VenueOrderID = current.VenueOrderID
	}
	if replacement.Request.AccountID == "" {
		replacement.Request.AccountID = current.Request.AccountID
		if replacement.Request.AccountID == "" {
			replacement.Request.AccountID = accountID
		}
	}
	replacement.Request.ClientID = clientID
	if replacement.VenueOrderID == current.VenueOrderID {
		return c.prepareOrderUpsertLocked(replacement, orderstate.Merge)
	}
	if strings.TrimSpace(replacement.VenueOrderID) == "" {
		return orderUpsertPlan{}, orderIdentityConflict("replacement venue order id is empty")
	}

	venueCandidate, venueFound, venueAmbiguous := c.typedOrderCandidateLocked(accountID, orderNamespaceVenue, replacement.VenueOrderID)
	if venueAmbiguous {
		return orderUpsertPlan{}, orderIdentityConflict(
			"replacement venue order id %q is ambiguous in account %q",
			replacement.VenueOrderID,
			accountID,
		)
	}
	if venueFound && venueCandidate.key != currentCandidate.key {
		ownerClientID := venueCandidate.order.Request.ClientID
		if ownerClientID != "" && ownerClientID != clientID {
			return orderUpsertPlan{}, orderIdentityConflict(
				"replacement venue order id %q belongs to client id %q",
				replacement.VenueOrderID,
				ownerClientID,
			)
		}
		if !orderCoreIdentityCompatible(current, venueCandidate.order) {
			return orderUpsertPlan{}, orderIdentityConflict(
				"replacement venue order id %q conflicts with cached order identity",
				replacement.VenueOrderID,
			)
		}
	}

	// A changed venue ID is a new venue-order incarnation. The old alias may
	// already have received CANCELED/FILLED evidence from a cancel-replace, so
	// only its logical request fields may enrich the replacement. Venue-scoped
	// lifecycle state (status, fills, timestamps and reject reason) must not
	// leak across the alias boundary.
	replacement.Request = mergeReplacementRequest(current.Request, replacement.Request)
	merged := replacement
	if venueFound && venueCandidate.key != currentCandidate.key {
		venueOrder := venueCandidate.order
		if venueOrder.Request.AccountID == "" {
			venueOrder.Request.AccountID = merged.Request.AccountID
		}
		venueOrder.Request.ClientID = clientID
		// Both records now describe the replacement alias. Treat already-seen
		// stream evidence as existing state and the confirmed response as the
		// incoming authoritative acknowledgement, while retaining larger
		// cumulative fill evidence through orderstate.Merge.
		merged = orderstate.Merge(venueOrder, merged)
	}
	merged.Request.ClientID = clientID
	if merged.Request.AccountID == "" {
		merged.Request.AccountID = accountID
	}
	merged.VenueOrderID = replacement.VenueOrderID
	if merged.FilledQty.IsPositive() && merged.Status != enums.StatusPartiallyFilled && !orderstate.IsTerminal(merged.Status) {
		merged.Status = enums.StatusPartiallyFilled
	}

	plan := orderUpsertPlan{key: keyForOrder(merged), order: merged}
	if currentCandidate.key != plan.key {
		plan.deletes = append(plan.deletes, currentCandidate.key)
	}
	if venueFound && venueCandidate.key != plan.key {
		plan.deletes = append(plan.deletes, venueCandidate.key)
	}
	return plan, nil
}

func mergeReplacementRequest(base, incoming model.OrderRequest) model.OrderRequest {
	out := base
	if incoming.AccountID != "" {
		out.AccountID = incoming.AccountID
	}
	if incoming.InstrumentID != (model.InstrumentID{}) {
		out.InstrumentID = incoming.InstrumentID
	}
	if incoming.ClientID != "" {
		out.ClientID = incoming.ClientID
	}
	if incoming.Side != enums.SideUnknown {
		out.Side = incoming.Side
	}
	if incoming.Type != enums.TypeUnknown {
		out.Type = incoming.Type
	}
	if incoming.TIF != enums.TifUnknown {
		out.TIF = incoming.TIF
	}
	if !incoming.Quantity.IsZero() {
		out.Quantity = incoming.Quantity
	}
	if !incoming.Price.IsZero() {
		out.Price = incoming.Price
	}
	if !incoming.TriggerPrice.IsZero() {
		out.TriggerPrice = incoming.TriggerPrice
	}
	if !incoming.ActivationPrice.IsZero() {
		out.ActivationPrice = incoming.ActivationPrice
	}
	if !incoming.TrailingOffsetBps.IsZero() {
		out.TrailingOffsetBps = incoming.TrailingOffsetBps
	}
	if incoming.PositionSide != enums.PosNet || out.PositionSide == enums.PosNet {
		out.PositionSide = incoming.PositionSide
	}
	if incoming.ReduceOnly {
		out.ReduceOnly = true
	}
	if incoming.Venue != nil {
		out.Venue = incoming.Venue
	}
	return out
}

func orderCoreIdentityCompatible(left, right model.Order) bool {
	return orderAccountIDsMergeable(left.Request.AccountID, right.Request.AccountID) &&
		(left.Request.InstrumentID == (model.InstrumentID{}) || right.Request.InstrumentID == (model.InstrumentID{}) || left.Request.InstrumentID == right.Request.InstrumentID) &&
		(left.Request.Side == enums.SideUnknown || right.Request.Side == enums.SideUnknown || left.Request.Side == right.Request.Side)
}

func (c *Cache) applyOrderUpsertPlanLocked(plan orderUpsertPlan) {
	deleted := make(map[orderKey]struct{}, len(plan.deletes))
	for _, key := range plan.deletes {
		if key == plan.key {
			continue
		}
		if _, seen := deleted[key]; seen {
			continue
		}
		deleted[key] = struct{}{}
		c.deleteOrderLocked(key)
	}
	c.forgetTerminalLocked(plan.key)
	c.orders[plan.key] = plan.order
	c.trackTerminalLocked(plan.key, plan.order)
	c.compactTerminalOrderLocked()
}

func (c *Cache) deleteOrderLocked(key orderKey) {
	delete(c.orders, key)
	c.forgetTerminalLocked(key)
}

func (c *Cache) forgetTerminalLocked(key orderKey) {
	if _, tracked := c.terminalVersions[key]; !tracked {
		return
	}
	delete(c.terminalVersions, key)
	c.terminalCount--
}

func (c *Cache) trackTerminalLocked(key orderKey, order model.Order) {
	if order.Status == enums.StatusUnknown || !isTerminal(order.Status) {
		return
	}
	c.terminalVersion++
	version := c.terminalVersion
	c.terminalVersions[key] = version
	c.terminalOrder = append(c.terminalOrder, terminalOrderRef{key: key, version: version})
	c.terminalCount++
	for c.terminalCount > c.terminalLimit && len(c.terminalOrder) > 0 {
		oldest := c.terminalOrder[0]
		c.terminalOrder = c.terminalOrder[1:]
		if current, ok := c.terminalVersions[oldest.key]; !ok || current != oldest.version {
			continue
		}
		delete(c.orders, oldest.key)
		delete(c.terminalVersions, oldest.key)
		c.terminalCount--
	}
}

func (c *Cache) compactTerminalOrderLocked() {
	maxRefs := c.terminalLimit * 2
	if len(c.terminalOrder) <= maxRefs {
		return
	}
	compacted := make([]terminalOrderRef, 0, c.terminalCount)
	for _, ref := range c.terminalOrder {
		if current, ok := c.terminalVersions[ref.key]; ok && current == ref.version {
			compacted = append(compacted, ref)
		}
	}
	c.terminalOrder = compacted
}

// Order returns the order for a client or venue id.
func (c *Cache) Order(key string) (model.Order, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out model.Order
	found := false
	for _, o := range c.orders {
		if !orderLookupMatches(o, key) {
			continue
		}
		if found {
			return model.Order{}, false
		}
		out = o
		found = true
	}
	return out, found
}

// OrderForAccount returns an order for a client or venue id inside one account.
func (c *Cache) OrderForAccount(accountID, key string) (model.Order, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.orderForAccountLocked(accountID, key)
}

// OrderByClientIDForAccount resolves only the client-order-id namespace.
// Use this when the caller knows the identifier came from OrderRequest.ClientID;
// OrderForAccount intentionally remains ambiguous when the same text is also a
// different order's venue ID.
func (c *Cache) OrderByClientIDForAccount(accountID, clientID string) (model.Order, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	candidate, found, ambiguous := c.typedOrderCandidateLocked(accountID, orderNamespaceClient, clientID)
	return candidate.order, found && !ambiguous
}

// OrderByVenueOrderIDForAccount resolves only the venue-order-id namespace.
func (c *Cache) OrderByVenueOrderIDForAccount(accountID, venueOrderID string) (model.Order, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	candidate, found, ambiguous := c.typedOrderCandidateLocked(accountID, orderNamespaceVenue, venueOrderID)
	return candidate.order, found && !ambiguous
}

func (c *Cache) orderForAccountLocked(accountID, key string) (model.Order, bool) {
	var out model.Order
	found := false
	for _, o := range c.orders {
		if !orderLookupMatches(o, key) || !orderAccountMatches(accountID, o) {
			continue
		}
		if found {
			return model.Order{}, false
		}
		out = o
		found = true
	}
	return out, found
}

// ResolveOrderForFill resolves ClientID and VenueOrderID in their own typed
// namespaces. expectedAccountID is the single-account runtime scope. The
// lookup is read-only; callers persist learned aliases only after every
// downstream identity guard accepts the fill.
func (c *Cache) ResolveOrderForFill(expectedAccountID string, fill model.Fill) (model.Order, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.resolveOrderForFillLocked(expectedAccountID, fill)
}

// CommitOrderIdentityForFill persists identity fields learned from an already
// accepted fill. Callers must run all other identity guards before committing;
// ResolveOrderForFill is deliberately read-only so a later guard cannot leave a
// rejected alias in the authoritative cache.
func (c *Cache) CommitOrderIdentityForFill(expectedAccountID string, fill model.Fill) (model.Order, bool, error) {
	c.orderMu.Lock()
	defer c.orderMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	order, found, err := c.resolveOrderForFillLocked(expectedAccountID, fill)
	if err != nil || !found {
		return order, found, err
	}
	if err := c.upsertOrderLocked(order, orderstate.Merge); err != nil {
		return model.Order{}, false, err
	}
	return order, true, nil
}

// CommitAcceptedFill atomically resolves or materializes an order, persists any
// learned aliases, and (for a new fill) applies only the incremental quantity
// not already covered by cumulative venue state. No cache state changes when a
// typed identity check fails.
func (c *Cache) CommitAcceptedFill(
	expectedAccountID string,
	fill model.Fill,
	materialized *model.Order,
	apply bool,
	previouslyAppliedQty decimal.Decimal,
	at time.Time,
) (model.Order, bool, error) {
	c.orderMu.Lock()
	defer c.orderMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	order, found, err := c.resolveOrderForFillLocked(expectedAccountID, fill)
	if err != nil {
		return model.Order{}, false, err
	}
	if !found {
		if materialized == nil {
			return model.Order{}, false, nil
		}
		order = *materialized
		if expected := strings.TrimSpace(expectedAccountID); expected != "" && order.Request.AccountID != "" && order.Request.AccountID != expected {
			return model.Order{}, false, fillIdentityConflict("materialized order account %q does not match runtime account %q", order.Request.AccountID, expected)
		}
		if err := validateFillAgainstOrder(fill, order); err != nil {
			return model.Order{}, false, err
		}
	}
	if apply {
		orderFill := fill
		coveredByCumulativeState := order.FilledQty.Sub(previouslyAppliedQty)
		if coveredByCumulativeState.IsPositive() {
			if coveredByCumulativeState.GreaterThanOrEqual(orderFill.Quantity) {
				orderFill.Quantity = decimal.Zero
			} else {
				orderFill.Quantity = orderFill.Quantity.Sub(coveredByCumulativeState)
			}
		}
		if orderFill.Quantity.IsPositive() {
			order = orderstate.ApplyFill(order, orderFill, at)
		}
	}
	if err := c.upsertOrderLocked(order, orderstate.Merge); err != nil {
		return model.Order{}, false, err
	}
	return c.resolveOrderForFillLocked(expectedAccountID, fill)
}

func (c *Cache) resolveOrderForFillLocked(expectedAccountID string, fill model.Fill) (model.Order, bool, error) {
	expectedAccountID = strings.TrimSpace(expectedAccountID)
	fillAccountID := strings.TrimSpace(fill.AccountID)
	if expectedAccountID != "" && fillAccountID != "" && expectedAccountID != fillAccountID {
		return model.Order{}, false, fillIdentityConflict("fill account %q does not match runtime account %q", fillAccountID, expectedAccountID)
	}
	accountID := fillAccountID
	if accountID == "" {
		accountID = expectedAccountID
	}

	clientCandidate, clientFound, err := c.typedFillCandidateLocked(accountID, "client", fill.ClientID)
	if err != nil {
		return model.Order{}, false, err
	}
	venueCandidate, venueFound, err := c.typedFillCandidateLocked(accountID, "venue", fill.VenueOrderID)
	if err != nil {
		return model.Order{}, false, err
	}
	if clientFound && venueFound && clientCandidate.key != venueCandidate.key {
		return model.Order{}, false, fillIdentityConflict("client id %q and venue order id %q resolve to different orders", fill.ClientID, fill.VenueOrderID)
	}

	var candidate orderMergeCandidate
	switch {
	case clientFound:
		candidate = clientCandidate
	case venueFound:
		candidate = venueCandidate
	default:
		return model.Order{}, false, nil
	}
	if err := validateFillAgainstOrder(fill, candidate.order); err != nil {
		return model.Order{}, false, err
	}
	enriched := enrichOrderIdentityFromFill(candidate.order, fill, accountID)
	return enriched, true, nil
}

func enrichOrderIdentityFromFill(order model.Order, fill model.Fill, accountID string) model.Order {
	if order.Request.AccountID == "" {
		order.Request.AccountID = fill.AccountID
		if order.Request.AccountID == "" {
			order.Request.AccountID = accountID
		}
	}
	if order.Request.InstrumentID == (model.InstrumentID{}) {
		order.Request.InstrumentID = fill.InstrumentID
	}
	if order.Request.ClientID == "" {
		order.Request.ClientID = fill.ClientID
	}
	if order.VenueOrderID == "" {
		order.VenueOrderID = fill.VenueOrderID
	}
	if order.Request.Side == enums.SideUnknown {
		order.Request.Side = fill.Side
	}
	return order
}

func (c *Cache) typedFillCandidateLocked(accountID, namespace, id string) (orderMergeCandidate, bool, error) {
	candidate, found, ambiguous := c.typedOrderCandidateLocked(accountID, namespace, id)
	if ambiguous {
		return orderMergeCandidate{}, false, fillIdentityConflict("%s id %q is ambiguous inside account %q", namespace, id, accountID)
	}
	return candidate, found, nil
}

func (c *Cache) typedOrderCandidateLocked(accountID, namespace, id string) (orderMergeCandidate, bool, bool) {
	if strings.TrimSpace(id) == "" {
		return orderMergeCandidate{}, false, false
	}
	var candidate orderMergeCandidate
	found := false
	for key, order := range c.orders {
		if !orderAccountMatches(accountID, order) {
			continue
		}
		matches := false
		switch namespace {
		case "client":
			matches = order.Request.ClientID == id
		case "venue":
			matches = order.VenueOrderID == id
		}
		if !matches {
			continue
		}
		if found && candidate.key != key {
			return orderMergeCandidate{}, false, true
		}
		candidate = orderMergeCandidate{key: key, order: order}
		found = true
	}
	return candidate, found, false
}

func validateFillAgainstOrder(fill model.Fill, order model.Order) error {
	if fill.AccountID != "" && order.Request.AccountID != "" && fill.AccountID != order.Request.AccountID {
		return fillIdentityConflict("fill account %q does not match order account %q", fill.AccountID, order.Request.AccountID)
	}
	if fill.InstrumentID != (model.InstrumentID{}) && order.Request.InstrumentID != (model.InstrumentID{}) && fill.InstrumentID != order.Request.InstrumentID {
		return fillIdentityConflict("fill instrument %s does not match order instrument %s", fill.InstrumentID, order.Request.InstrumentID)
	}
	if fill.Side != enums.SideUnknown && order.Request.Side != enums.SideUnknown && fill.Side != order.Request.Side {
		return fillIdentityConflict("fill side %s does not match order side %s", fill.Side, order.Request.Side)
	}
	if fill.ClientID != "" && order.Request.ClientID != "" && fill.ClientID != order.Request.ClientID {
		return fillIdentityConflict("fill client id %q does not match order client id %q", fill.ClientID, order.Request.ClientID)
	}
	if fill.VenueOrderID != "" && order.VenueOrderID != "" && fill.VenueOrderID != order.VenueOrderID {
		return fillIdentityConflict("fill venue order id %q does not match cached venue order id %q", fill.VenueOrderID, order.VenueOrderID)
	}
	return nil
}

func fillIdentityConflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrFillOrderIdentityConflict, fmt.Sprintf(format, args...))
}

func orderIdentityConflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrOrderIdentityConflict, fmt.Sprintf(format, args...))
}

// ApplyConfirmedCancel records an authoritative successful venue cancellation.
// It advances the order atomically without letting clock skew between local and
// venue timestamps discard the confirmed terminal state. A FILLED order is not
// downgraded if a concurrent fill won the cancel race.
func (c *Cache) ApplyConfirmedCancel(accountID, key string, at time.Time) bool {
	c.orderMu.Lock()
	defer c.orderMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.orderForAccountLocked(accountID, key)
	return c.applyConfirmedCancelLocked(o, ok, at)
}

// ApplyConfirmedCancelByClientID is the typed counterpart used by execution
// commands, whose input is always a client ID.
func (c *Cache) ApplyConfirmedCancelByClientID(accountID, clientID string, at time.Time) bool {
	c.orderMu.Lock()
	defer c.orderMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	candidate, found, ambiguous := c.typedOrderCandidateLocked(accountID, orderNamespaceClient, clientID)
	return c.applyConfirmedCancelLocked(candidate.order, found && !ambiguous, at)
}

func (c *Cache) applyConfirmedCancelLocked(o model.Order, ok bool, at time.Time) bool {
	if !ok {
		return false
	}
	o.Status = enums.StatusCanceled
	if o.UpdatedAt.IsZero() || at.After(o.UpdatedAt) {
		o.UpdatedAt = at
	}
	return c.upsertOrderLocked(o, orderstate.Merge) == nil
}

// Orders returns a snapshot slice of all known orders.
func (c *Cache) Orders() []model.Order {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]model.Order, 0, len(c.orders))
	for _, o := range c.orders {
		out = append(out, o)
	}
	return out
}

// OpenOrders returns orders not in a terminal state.
func (c *Cache) OpenOrders() []model.Order {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []model.Order
	for _, o := range c.orders {
		if !isTerminal(o.Status) {
			out = append(out, o)
		}
	}
	return out
}

func isTerminal(s enums.OrderStatus) bool {
	return orderstate.IsTerminal(s)
}

// UpsertPosition inserts or replaces a position. A flat (zero-quantity) position
// is removed. Called from the bus goroutine.
func (c *Cache) UpsertPosition(p model.Position) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := positionKey{accountID: p.AccountID, instrument: p.InstrumentID.String(), side: p.Side}
	if existing, ok := c.positions[k]; ok && venueUpdateOlder(p.UpdatedAt, existing.UpdatedAt) {
		return
	}
	if p.Quantity.IsZero() {
		delete(c.positions, k)
		return
	}
	c.positions[k] = p
}

// Position returns the position for an instrument/side.
func (c *Cache) Position(id model.InstrumentID, side enums.PositionSide) (model.Position, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out model.Position
	found := false
	for key, p := range c.positions {
		if key.instrument != id.String() || key.side != side {
			continue
		}
		if found {
			return model.Position{}, false
		}
		out = p
		found = true
	}
	return out, found
}

func (c *Cache) PositionForAccount(accountID string, id model.InstrumentID, side enums.PositionSide) (model.Position, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.positions[positionKey{accountID: accountID, instrument: id.String(), side: side}]
	return p, ok
}

// Positions returns a snapshot slice of all non-flat positions.
func (c *Cache) Positions() []model.Position {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]model.Position, 0, len(c.positions))
	for _, p := range c.positions {
		out = append(out, p)
	}
	return out
}

// UpsertBalance inserts or replaces a per-currency balance. Called from the bus
// goroutine.
func (c *Cache) UpsertBalance(b model.AccountBalance) {
	_ = c.ApplyBalance(b)
}

func (c *Cache) ApplyBalance(b model.AccountBalance) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b = b.Normalized()
	k := balanceKey{accountID: b.AccountID, currency: b.Currency}
	if existing, ok := c.balances[k]; ok && venueUpdateOlder(b.UpdatedAt, existing.UpdatedAt) {
		return nil
	}
	if acct, ok := c.accounts[b.AccountID]; ok {
		if err := acct.ApplyBalance(b); err != nil {
			return err
		}
	}
	c.balances[k] = b
	return nil
}

func venueUpdateOlder(incoming, current time.Time) bool {
	return !incoming.IsZero() && !current.IsZero() && incoming.Before(current)
}

// Balance returns the balance for a currency.
func (c *Cache) Balance(currency string) (model.AccountBalance, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out model.AccountBalance
	found := false
	for key, b := range c.balances {
		if key.currency != currency {
			continue
		}
		if found {
			return model.AccountBalance{}, false
		}
		out = b
		found = true
	}
	return out, found
}

func (c *Cache) BalanceForAccount(accountID, currency string) (model.AccountBalance, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	b, ok := c.balances[balanceKey{accountID: accountID, currency: currency}]
	return b, ok
}

// Balances returns a snapshot slice of all balances.
func (c *Cache) Balances() []model.AccountBalance {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]model.AccountBalance, 0, len(c.balances))
	for _, b := range c.balances {
		out = append(out, b)
	}
	return out
}

func (c *Cache) ApplyAccountState(state model.AccountState) error {
	return c.ApplyAccountStateAt(state, time.Now())
}

func (c *Cache) ApplyAccountStateAt(state model.AccountState, appliedAt time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	acct, ok := c.accounts[state.AccountID]
	if !ok {
		var err error
		acct, err = accounting.New(state, c.accountStaleAfter, appliedAt)
		if err != nil {
			return err
		}
		c.accounts[state.AccountID] = acct
		c.indexAccountByVenue(state.Venue, state.AccountID)
	} else if err := acct.Apply(state, appliedAt); err != nil {
		return err
	}
	for key := range c.balances {
		if key.accountID == state.AccountID {
			delete(c.balances, key)
		}
	}
	for _, bal := range acct.Balances() {
		bal = bal.Normalized()
		if bal.AccountID == "" {
			bal.AccountID = state.AccountID
		}
		c.balances[balanceKey{accountID: bal.AccountID, currency: bal.Currency}] = bal
	}
	return nil
}

func (c *Cache) MarkAccountReconciled(accountID string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if acct, ok := c.accounts[accountID]; ok {
		acct.MarkReconciled(at)
	}
}

func (c *Cache) Account(accountID string) (accounting.Account, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	acct, ok := c.accounts[accountID]
	return acct, ok
}

func (c *Cache) AccountForVenue(venue string) (accounting.Account, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	accountIDs := c.accountByVenue[venue]
	if len(accountIDs) != 1 {
		return nil, false
	}
	var accountID string
	for id := range accountIDs {
		accountID = id
	}
	acct, ok := c.accounts[accountID]
	return acct, ok
}

func (c *Cache) AccountIDsForVenue(venue string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	accountIDs := c.accountByVenue[venue]
	out := make([]string, 0, len(accountIDs))
	for id := range accountIDs {
		out = append(out, id)
	}
	return out
}

func (c *Cache) Accounts() []accounting.Account {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]accounting.Account, 0, len(c.accounts))
	for _, acct := range c.accounts {
		out = append(out, acct)
	}
	return out
}

func (c *Cache) indexAccountByVenue(venue, accountID string) {
	if venue == "" {
		return
	}
	if c.accountByVenue[venue] == nil {
		c.accountByVenue[venue] = make(map[string]struct{})
	}
	c.accountByVenue[venue][accountID] = struct{}{}
}
