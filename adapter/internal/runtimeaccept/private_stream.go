package runtimeaccept

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
)

type PrivateExecutionEvidence struct {
	OrderEvents int
	FillEvents  int
}

func WaitForPrivateExecutionEvidence(ctx context.Context, events <-chan contract.ExecEnvelope, id model.InstrumentID, accountID string) (PrivateExecutionEvidence, error) {
	return waitForPrivateExecutionEvidence(ctx, events, id, accountID, true)
}

func WaitForPrivateFillEvidence(ctx context.Context, events <-chan contract.ExecEnvelope, id model.InstrumentID, accountID string) (PrivateExecutionEvidence, error) {
	return waitForPrivateExecutionEvidence(ctx, events, id, accountID, false)
}

func waitForPrivateExecutionEvidence(ctx context.Context, events <-chan contract.ExecEnvelope, id model.InstrumentID, accountID string, requireOrder bool) (PrivateExecutionEvidence, error) {
	var report PrivateExecutionEvidence
	for {
		select {
		case <-ctx.Done():
			return report, fmt.Errorf("private execution stream evidence incomplete for %s/%s: require_order=%t orders=%d fills=%d: %w", accountID, id, requireOrder, report.OrderEvents, report.FillEvents, ctx.Err())
		case envelope, ok := <-events:
			if !ok {
				return report, fmt.Errorf("private execution stream closed before evidence completed for %s/%s: require_order=%t orders=%d fills=%d", accountID, id, requireOrder, report.OrderEvents, report.FillEvents)
			}
			if envelope.Source != contract.SourceAdapterStream || !envelope.Flags.Has(contract.EventFlagFromStream) {
				continue
			}
			if envelope.AccountID != accountID || envelope.InstrumentID != id {
				continue
			}
			switch envelope.Payload.(type) {
			case contract.OrderEvent:
				report.OrderEvents++
			case contract.FillEvent:
				report.FillEvents++
			}
			if report.FillEvents > 0 && (!requireOrder || report.OrderEvents > 0) {
				return report, nil
			}
		}
	}
}
