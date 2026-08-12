package app

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
)

// reconciliationDispatcher routes a reconciled command back to the product that
// issued it. Manual is the only product today, and an unknown product is an
// error rather than a no-op: silently dropping a reconciliation would leave the
// command ledger permanently disagreeing with the Gateway.
type reconciliationDispatcher struct {
	manual *manual.Service
}

func (dispatcher reconciliationDispatcher) RecordReconciliation(ctx context.Context, command orders.Command, handoff orders.HandoffResult) (orders.Command, error) {
	if command.ProductID != "manual" {
		return command, fmt.Errorf("unsupported reconciliation product")
	}
	return dispatcher.manual.RecordReconciliation(ctx, command, handoff)
}
