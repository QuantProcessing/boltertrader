// Package bus provides the runtime's single-consumer event fan-in. It merges the
// three per-client contract event streams (market, execution, account) into one
// goroutine that dispatches to registered handlers. Because every handler runs
// on this one goroutine, downstream state (the Cache, Portfolio) needs no
// internal locking against the event path — the bus IS the serialization point,
// the Go equivalent of NautilusTrader's single-threaded MessageBus.
package bus

import (
	"context"

	"github.com/QuantProcessing/boltertrader/core/contract"
)

// Handlers are the callbacks invoked on the bus goroutine for each event class.
// Any handler may be nil.
type Handlers struct {
	OnMarket  func(contract.MarketEvent)
	OnExec    func(contract.ExecEvent)
	OnAccount func(contract.AccountEvent)
}

// Bus fans in the three contract event channels. A nil channel is simply never
// selected, so a market-data-only or execution-only node is valid.
type Bus struct {
	market  <-chan contract.MarketEvent
	exec    <-chan contract.ExecEvent
	account <-chan contract.AccountEvent
}

// New builds a Bus over the given channels. Any channel may be nil.
func New(market <-chan contract.MarketEvent, exec <-chan contract.ExecEvent, account <-chan contract.AccountEvent) *Bus {
	return &Bus{market: market, exec: exec, account: account}
}

// Run consumes events until ctx is cancelled or every non-nil channel has
// closed, dispatching each to the matching handler. It blocks on the calling
// goroutine; run it with `go bus.Run(...)`.
func (b *Bus) Run(ctx context.Context, h Handlers) {
	market, exec, account := b.market, b.exec, b.account
	for {
		if market == nil && exec == nil && account == nil {
			return // all sources drained
		}
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-market:
			if !ok {
				market = nil
				continue
			}
			if h.OnMarket != nil {
				h.OnMarket(ev)
			}
		case ev, ok := <-exec:
			if !ok {
				exec = nil
				continue
			}
			if h.OnExec != nil {
				h.OnExec(ev)
			}
		case ev, ok := <-account:
			if !ok {
				account = nil
				continue
			}
			if h.OnAccount != nil {
				h.OnAccount(ev)
			}
		}
	}
}
