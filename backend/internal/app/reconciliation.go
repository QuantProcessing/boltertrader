package app

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/manual"
)

type reconciliationDispatcher struct {
	manual *manual.Service
}

func (dispatcher reconciliationDispatcher) RecordReconciliation(ctx context.Context, command orders.Command, handoff orders.HandoffResult) (orders.Command, error) {
	if command.ProductID != "manual" {
		return command, fmt.Errorf("unsupported reconciliation product")
	}
	return dispatcher.manual.RecordReconciliation(ctx, command, handoff)
}
