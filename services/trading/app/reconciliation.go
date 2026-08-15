package app

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/services/trading/arbitrage"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
)

// reconciliationDispatcher routes a reconciled command back to the product that
// issued it. An unknown product is an error rather than a no-op: silently
// dropping a reconciliation would leave the command ledger permanently
// disagreeing with the Gateway.
//
// Dispatch is by ProductID rather than by asking each service whether the
// command looks like one of its own, because the two products share a command
// shape — an arbitrage leg is a manual order plus a leg label — and a
// shape-based guess would hand arbitrage's fills to manual's ledger writer.
type reconciliationDispatcher struct {
	manual    *manual.Service
	arbitrage *arbitrage.Service
}

func (dispatcher reconciliationDispatcher) RecordReconciliation(ctx context.Context, command orders.Command, handoff orders.HandoffResult) (orders.Command, error) {
	switch command.ProductID {
	case "manual":
		if dispatcher.manual == nil {
			return command, fmt.Errorf("unsupported reconciliation product")
		}
		return dispatcher.manual.RecordReconciliation(ctx, command, handoff)
	case arbitrage.ProductID:
		if dispatcher.arbitrage == nil {
			return command, fmt.Errorf("unsupported reconciliation product")
		}
		return dispatcher.arbitrage.RecordReconciliation(ctx, command, handoff)
	default:
		return command, fmt.Errorf("unsupported reconciliation product")
	}
}
