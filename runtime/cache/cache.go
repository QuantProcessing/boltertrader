// Package cache is the runtime's authoritative in-memory state store for orders,
// positions, and balances. It is written only from the bus goroutine (the
// single serialization point) but guarded by an RWMutex so strategies and
// reporting code on other goroutines can read consistent snapshots.
package cache

import (
	"sync"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
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
}

// New returns an empty Cache.
func New() *Cache {
	return &Cache{
		orders:    make(map[orderKey]model.Order),
		positions: make(map[positionKey]model.Position),
		balances:  make(map[string]model.AccountBalance),
		market:    make(map[string]*marketState),
	}
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
	switch s {
	case enums.StatusFilled, enums.StatusCanceled, enums.StatusRejected, enums.StatusExpired:
		return true
	default:
		return false
	}
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
	c.balances[b.Currency] = b
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
