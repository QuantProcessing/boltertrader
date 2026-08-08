package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/QuantProcessing/boltertrader/backend/internal/httpapi"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/audit"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/accounting"
)

type accountingFactObserver struct {
	mu        sync.Mutex
	activity  httpapi.ActivityPublisher
	audit     *audit.Service
	published map[string]struct{}
}

func newAccountingFactObserver(activity httpapi.ActivityPublisher, auditService *audit.Service) (*accountingFactObserver, error) {
	if activity == nil || auditService == nil {
		return nil, fmt.Errorf("funding accounting observer dependencies are required")
	}
	return &accountingFactObserver{activity: activity, audit: auditService, published: make(map[string]struct{})}, nil
}

func (observer *accountingFactObserver) AccountingFactApplied(ctx context.Context, applied accounting.AppliedFact) error {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	action, activityKind := accountingEffectNames(applied.Kind)
	if action == "" || applied.PrincipalID == "" || applied.RequestID == "" {
		return fmt.Errorf("funding accounting effect attribution is incomplete")
	}
	records, err := observer.audit.List(ctx, audit.Filter{ResourceID: applied.FactID, Limit: 500})
	if err != nil {
		return err
	}
	seenAudit := false
	for _, record := range records {
		if record.PrincipalID == applied.PrincipalID && record.Action == action && record.ResourceType == "funding_accounting_fact" && record.Outcome == "accepted" && record.RequestID == applied.RequestID {
			seenAudit = true
			break
		}
	}
	if !seenAudit {
		if _, err := observer.audit.Record(ctx, audit.Entry{PrincipalID: applied.PrincipalID, Action: action, ResourceType: "funding_accounting_fact", ResourceID: applied.FactID, Outcome: "accepted", RequestID: applied.RequestID}); err != nil {
			return err
		}
	}
	if _, exists := observer.published[applied.FactID]; exists {
		return nil
	}
	if _, err := observer.activity.Publish(httpapi.ActivityRecord{
		PrincipalID: applied.PrincipalID, Mode: applied.Mode, Kind: activityKind, ProductID: applied.ProductID,
		AccountID: applied.AccountID, ResourceID: applied.FactID,
		Payload: map[string]any{"factId": applied.FactID, "lineageId": applied.LineageID, "positionId": applied.PositionID, "instrumentId": applied.InstrumentID, "effectiveAt": applied.EffectiveAt},
	}); err != nil {
		return err
	}
	observer.published[applied.FactID] = struct{}{}
	return nil
}

func accountingEffectNames(kind accounting.Kind) (string, string) {
	switch kind {
	case accounting.KindMark:
		return "funding.mark.ingest", "funding.mark.updated"
	case accounting.KindFunding:
		return "funding.payment.ingest", "funding.payment.recorded"
	default:
		return "", ""
	}
}
