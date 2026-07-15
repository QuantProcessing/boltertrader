package risk

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/shopspring/decimal"
)

func TestClientIDDedupeWindowRetainsUnknownOrdersAndEvictsInactiveHistory(t *testing.T) {
	c := cache.New()
	e := New(Limits{}, c).WithClientIDRetentionLimit(2)
	req := func(clientID string) model.OrderRequest {
		return model.OrderRequest{
			InstrumentID: model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
			ClientID:     clientID,
			Side:         enums.SideBuy,
			Quantity:     decimal.NewFromInt(1),
			Price:        decimal.NewFromInt(100),
		}
	}

	if err := checkRisk(e, req("unknown"), nil); err != nil {
		t.Fatalf("reserve unknown: %v", err)
	}
	c.UpsertOrder(model.Order{Request: req("unknown"), Status: enums.StatusUnknown})
	if err := checkRisk(e, req("inactive"), nil); err != nil {
		t.Fatalf("reserve inactive: %v", err)
	}
	if err := checkRisk(e, req("recent"), nil); err != nil {
		t.Fatalf("reserve recent: %v", err)
	}

	if got := len(e.seen); got != 2 {
		t.Fatalf("dedupe entries=%d, want limit 2 with protected UNKNOWN order", got)
	}
	if err := checkRisk(e, req("unknown"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("UNKNOWN order client ID should remain reserved, got %v", err)
	}
	if err := checkRisk(e, req("recent"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("recent client ID should remain reserved, got %v", err)
	}
	if err := checkRisk(e, req("inactive"), nil); err != nil {
		t.Fatalf("inactive oldest client ID should leave bounded window: %v", err)
	}
}

func TestClientIDDedupeDoesNotImmediatelyEvictNewReservationWhenProtectedIDsFillWindow(t *testing.T) {
	c := cache.New()
	e := New(Limits{}, c).WithClientIDRetentionLimit(1)
	req := func(clientID string) model.OrderRequest {
		return model.OrderRequest{
			InstrumentID: model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
			ClientID:     clientID,
			Side:         enums.SideBuy,
			Quantity:     decimal.NewFromInt(1),
			Price:        decimal.NewFromInt(100),
		}
	}
	if err := checkRisk(e, req("protected"), nil); err != nil {
		t.Fatal(err)
	}
	c.UpsertOrder(model.Order{Request: req("protected"), Status: enums.StatusNew})
	if err := checkRisk(e, req("just-reserved"), nil); err != nil {
		t.Fatal(err)
	}
	if err := checkRisk(e, req("just-reserved"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("new reservation must be duplicate-protected until a later window advance, got %v", err)
	}
}

func TestClientIDRetentionKeepsEarlierConcurrentSubmissionReservation(t *testing.T) {
	c := cache.New()
	request := func(clientID string) model.OrderRequest {
		return model.OrderRequest{
			InstrumentID: model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp},
			ClientID:     clientID,
			Side:         enums.SideBuy,
			Quantity:     decimal.NewFromInt(1),
			Price:        decimal.NewFromInt(100),
		}
	}
	e := New(Limits{}, c).
		WithClientIDRetentionLimit(1)
	releases := make([]func(), 0, 2)
	for _, clientID := range []string{"validating-first", "validating-second"} {
		release, err := e.CheckSubmission(context.Background(), request(clientID), nil)
		if err != nil {
			t.Fatalf("reserve %s: %v", clientID, err)
		}
		releases = append(releases, release)
	}

	if _, err := e.CheckSubmission(context.Background(), request("validating-first"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("earlier client ID was evicted while submission was reserved: %v", err)
	}
	for _, release := range releases {
		release()
	}
}
