package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/httpapi"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/audit"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/realtime"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/accounting"
	"github.com/shopspring/decimal"
)

func TestAccountingFactObserverProjectsReplayOnceToActivityAndAudit(t *testing.T) {
	now := time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)
	hub, err := realtime.NewHub(16, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	activity, err := httpapi.NewActivityLog(16, func() time.Time { return now }, hub)
	if err != nil {
		t.Fatal(err)
	}
	auditStore := audit.NewMemoryStore()
	nextID := 0
	auditService, err := audit.New(auditStore, func() time.Time { return now }, func() string { nextID++; return fmt.Sprintf("audit-%d", nextID) })
	if err != nil {
		t.Fatal(err)
	}
	observer, err := newAccountingFactObserver(activity, auditService)
	if err != nil {
		t.Fatal(err)
	}
	applied := accounting.AppliedFact{
		Fact:        accounting.Fact{FactID: "funding-1", Revision: 1, Kind: accounting.KindFunding, ProductID: "funding", Mode: "test", LineageID: "lineage-a", PositionID: "position-a", AccountID: "account-a", InstrumentID: "BTC-USDT", EffectiveAt: now, Amount: decimal.RequireFromString("-1.25"), Asset: "USDT"},
		PrincipalID: "browser-a", RequestID: "funding-accounting:position-a",
	}
	if err := observer.AccountingFactApplied(context.Background(), applied); err != nil {
		t.Fatal(err)
	}
	if err := observer.AccountingFactApplied(context.Background(), applied); err != nil {
		t.Fatalf("replay: %v", err)
	}
	activities, err := activity.ListActivity(context.Background(), "browser-a", "test", httpapi.ActivityQuery{ResourceID: "funding-1"})
	if err != nil || len(activities) != 1 || activities[0].Kind != "funding.payment.recorded" {
		t.Fatalf("activities=%+v err=%v", activities, err)
	}
	audits, err := auditService.List(context.Background(), audit.Filter{ResourceID: "funding-1", Limit: 100})
	if err != nil || len(audits) != 1 || audits[0].Action != "funding.payment.ingest" || audits[0].RequestID != "funding-accounting:position-a" {
		t.Fatalf("audits=%+v err=%v", audits, err)
	}
}
