// Package cache is the runtime's authoritative in-memory state store for orders,
// positions, and balances. It is written only from the bus goroutine (the
// single serialization point) but guarded by an RWMutex so strategies and
// reporting code on other goroutines can read consistent snapshots.
package cache

import (
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/accounting"
	"github.com/QuantProcessing/boltertrader/runtime/orderstate"
)

// orderKey identifies an order inside one logical runtime account. ClientID is
// preferred (assigned by us, stable across the submit/ack boundary);
// VenueOrderID is the fallback for orders we learn about only from the venue.
type orderKey struct {
	accountID string
	id        string
}

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

type orderMergeCandidate struct {
	key   orderKey
	order model.Order
}

// Cache holds the live trading state.
type Cache struct {
	mu                sync.RWMutex
	orders            map[orderKey]model.Order
	positions         map[positionKey]model.Position
	balances          map[balanceKey]model.AccountBalance
	market            map[string]*marketState // keyed by InstrumentID.String()
	accounts          map[string]accounting.Account
	accountByVenue    map[string]map[string]struct{}
	accountStaleAfter time.Duration
}

// New returns an empty Cache.
func New() *Cache {
	return &Cache{
		orders:            make(map[orderKey]model.Order),
		positions:         make(map[positionKey]model.Position),
		balances:          make(map[balanceKey]model.AccountBalance),
		market:            make(map[string]*marketState),
		accounts:          make(map[string]accounting.Account),
		accountByVenue:    make(map[string]map[string]struct{}),
		accountStaleAfter: accounting.DefaultStaleAfter,
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

func orderLookupID(o model.Order) string {
	if o.Request.ClientID != "" {
		return o.Request.ClientID
	}
	return o.VenueOrderID
}

func keyForOrder(o model.Order) orderKey {
	return orderKey{accountID: o.Request.AccountID, id: orderLookupID(o)}
}

func orderLookupMatches(k orderKey, o model.Order, key string) bool {
	return key != "" && (k.id == key || o.Request.ClientID == key || o.VenueOrderID == key)
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
	c.mu.Lock()
	defer c.mu.Unlock()
	k := keyForOrder(o)
	if existing, ok := c.orders[k]; ok {
		o = orderstate.Merge(existing, o)
	}
	var candidates []orderMergeCandidate
	for key, existing := range c.orders {
		if key == k || !orderAccountIDsMergeable(key.accountID, k.accountID) {
			continue
		}
		if orderLookupMatches(key, existing, k.id) ||
			(o.VenueOrderID != "" && existing.VenueOrderID == o.VenueOrderID) {
			candidates = append(candidates, orderMergeCandidate{key: key, order: existing})
		}
	}
	unambiguous := orderMergeCandidatesUnambiguous(k.accountID, candidates)
	if !unambiguous && k.accountID == "" {
		return
	}
	if unambiguous {
		for _, candidate := range candidates {
			o = orderstate.Merge(candidate.order, o)
			delete(c.orders, candidate.key)
		}
	}
	c.orders[keyForOrder(o)] = o
}

// Order returns the order for a client or venue id.
func (c *Cache) Order(key string) (model.Order, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out model.Order
	found := false
	for orderKey, o := range c.orders {
		if !orderLookupMatches(orderKey, o, key) {
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
	if o, ok := c.orders[orderKey{accountID: accountID, id: key}]; ok {
		return o, true
	}
	var out model.Order
	found := false
	for orderKey, o := range c.orders {
		if !orderLookupMatches(orderKey, o, key) || !orderAccountMatches(accountID, o) {
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
