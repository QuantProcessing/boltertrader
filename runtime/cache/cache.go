// Package cache is the runtime's authoritative in-memory state store for orders,
// positions, and balances. It is written only from the bus goroutine (the
// single serialization point) but guarded by an RWMutex so strategies and
// reporting code on other goroutines can read consistent snapshots.
package cache

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/accounting"
	"github.com/QuantProcessing/boltertrader/runtime/orderstate"
)

// orderKey identifies an order. ClientID is preferred (assigned by us, stable
// across the submit/ack boundary); VenueOrderID is the fallback for orders we
// learn about only from the venue.
type orderKey = string

// positionKey identifies a position by instrument and side (hedge mode can hold
// a long and a short leg for the same instrument simultaneously).
type positionKey struct {
	instrument string
	side       enums.PositionSide
}

// Cache holds the live trading state.
type Cache struct {
	mu        sync.RWMutex
	orders    map[orderKey]model.Order
	positions map[positionKey]model.Position
	balances  map[string]model.AccountBalance // keyed by currency
	market    map[string]*marketState         // keyed by InstrumentID.String()
	accounts  map[string]accounting.Account
	// One runtime node owns one account/product per venue in this phase. A
	// second same-venue account must use a separate node until positions and
	// balances carry account IDs end to end.
	accountByVenue    map[string]string
	accountStaleAfter time.Duration
}

// New returns an empty Cache.
func New() *Cache {
	return &Cache{
		orders:            make(map[orderKey]model.Order),
		positions:         make(map[positionKey]model.Position),
		balances:          make(map[string]model.AccountBalance),
		market:            make(map[string]*marketState),
		accounts:          make(map[string]accounting.Account),
		accountByVenue:    make(map[string]string),
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

func keyForOrder(o model.Order) orderKey {
	if o.Request.ClientID != "" {
		return o.Request.ClientID
	}
	return o.VenueOrderID
}

// UpsertOrder inserts or replaces an order. Called from the bus goroutine.
func (c *Cache) UpsertOrder(o model.Order) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := keyForOrder(o)
	if existing, ok := c.orders[k]; ok {
		o = orderstate.Merge(existing, o)
	}
	if o.VenueOrderID != "" {
		for key, existing := range c.orders {
			if key == k || existing.VenueOrderID != o.VenueOrderID {
				continue
			}
			o = orderstate.Merge(existing, o)
			delete(c.orders, key)
		}
	}
	c.orders[keyForOrder(o)] = o
}

// Order returns the order for a client or venue id.
func (c *Cache) Order(key string) (model.Order, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	o, ok := c.orders[key]
	return o, ok
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
	k := positionKey{instrument: p.InstrumentID.String(), side: p.Side}
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
	p, ok := c.positions[positionKey{instrument: id.String(), side: side}]
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
	c.mu.Lock()
	defer c.mu.Unlock()
	c.balances[b.Currency] = b.Normalized()
}

// Balance returns the balance for a currency.
func (c *Cache) Balance(currency string) (model.AccountBalance, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	b, ok := c.balances[currency]
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
		if err := c.ensureVenueAccountInvariantLocked(state.Venue, state.AccountID); err != nil {
			return err
		}
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
	for _, bal := range acct.Balances() {
		c.balances[bal.Currency] = bal.Normalized()
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
	accountID, ok := c.accountByVenue[venue]
	if !ok {
		return nil, false
	}
	acct, ok := c.accounts[accountID]
	return acct, ok
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

func (c *Cache) ensureVenueAccountInvariantLocked(venue, accountID string) error {
	if venue == "" {
		return nil
	}
	existing, ok := c.accountByVenue[venue]
	if !ok || existing == accountID {
		return nil
	}
	return fmt.Errorf("cache: multiple account states for venue %s are not supported in one runtime node: existing %s new %s", venue, existing, accountID)
}

func (c *Cache) indexAccountByVenue(venue, accountID string) {
	if venue == "" {
		return
	}
	c.accountByVenue[venue] = accountID
}
