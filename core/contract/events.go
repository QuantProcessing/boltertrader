// Package contract defines the venue-neutral interfaces the trading runtime
// depends on. The runtime imports ONLY core/{enums,model,clock,contract} and
// never an SDK or adapter — so a live adapter (wrapping sdk/*) and a simulated
// backtest venue both implement these interfaces and are freely swapped.
//
// Push updates are delivered as typed event sums (no interface{}) so the
// runtime can switch over them exhaustively.
package contract

import "github.com/QuantProcessing/boltertrader/core/model"

// ExecEvent is the sum of execution-stream push events (order lifecycle).
type ExecEvent interface{ isExecEvent() }

// OrderEvent reports an order lifecycle transition (new, partial, filled,
// canceled, ...).
type OrderEvent struct{ Order model.Order }

// FillEvent reports a single execution against one of our orders.
type FillEvent struct{ Fill model.Fill }

// RejectEvent reports that an order was rejected, keyed by client id.
type RejectEvent struct {
	ClientID string
	Reason   string
}

func (OrderEvent) isExecEvent()  {}
func (FillEvent) isExecEvent()   {}
func (RejectEvent) isExecEvent() {}

// AccountEvent is the sum of account-stream push events.
type AccountEvent interface{ isAccountEvent() }

// BalanceEvent reports a balance change for one currency.
type BalanceEvent struct{ Balance model.AccountBalance }

// PositionEvent reports a position change for one instrument/side.
type PositionEvent struct{ Position model.Position }

func (BalanceEvent) isAccountEvent()  {}
func (PositionEvent) isAccountEvent() {}

// MarketEvent is the sum of public market-data push events.
type MarketEvent interface{ isMarketEvent() }

// BookEvent carries an order book update (snapshot or rebuilt from diffs by
// the adapter).
type BookEvent struct{ Book model.OrderBook }

// QuoteEvent carries a top-of-book update.
type QuoteEvent struct{ Quote model.QuoteTick }

// TradeEvent carries a public trade print.
type TradeEvent struct{ Trade model.TradeTick }

func (BookEvent) isMarketEvent()  {}
func (QuoteEvent) isMarketEvent() {}
func (TradeEvent) isMarketEvent() {}
