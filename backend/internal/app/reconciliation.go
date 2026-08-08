package app

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/manual"
)

type reconciliationDispatcher struct {
	manual  *manual.Service
	funding *work.FundingPayloadProcessor
}

func (dispatcher reconciliationDispatcher) RecordReconciliation(ctx context.Context, command orders.Command, handoff orders.HandoffResult) (orders.Command, error) {
	switch command.ProductID {
	case "manual":
		return dispatcher.manual.RecordReconciliation(ctx, command, handoff)
	case "funding":
		return dispatcher.funding.RecordHandoff(ctx, command, handoff)
	default:
		return command, fmt.Errorf("unsupported reconciliation product")
	}
}
