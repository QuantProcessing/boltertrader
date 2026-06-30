package contract

import "context"

// Reconnectable is an OPTIONAL capability an adapter's clients may implement to
// expose connection health and a manual reconnect. The runtime type-asserts for
// it; adapters whose transport auto-reconnects internally need not implement it.
//
// After a reconnect the runtime triggers reconciliation (a REST snapshot pass)
// because websocket gaps may have dropped order/position updates.
type Reconnectable interface {
	// Connected reports whether the underlying transport is currently up.
	Connected() bool
	// Reconnect forces a reconnect and re-subscribe. It blocks until connected
	// or ctx is cancelled.
	Reconnect(ctx context.Context) error
}
