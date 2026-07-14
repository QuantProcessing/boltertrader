package risk

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
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

	if err := e.Check(req("unknown"), nil); err != nil {
		t.Fatalf("reserve unknown: %v", err)
	}
	c.UpsertOrder(model.Order{Request: req("unknown"), Status: enums.StatusUnknown})
	if err := e.Check(req("inactive"), nil); err != nil {
		t.Fatalf("reserve inactive: %v", err)
	}
	if err := e.Check(req("recent"), nil); err != nil {
		t.Fatalf("reserve recent: %v", err)
	}

	if got := len(e.seen); got != 2 {
		t.Fatalf("dedupe entries=%d, want limit 2 with protected UNKNOWN order", got)
	}
	if err := e.Check(req("unknown"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("UNKNOWN order client ID should remain reserved, got %v", err)
	}
	if err := e.Check(req("recent"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("recent client ID should remain reserved, got %v", err)
	}
	if err := e.Check(req("inactive"), nil); err != nil {
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
	if err := e.Check(req("protected"), nil); err != nil {
		t.Fatal(err)
	}
	c.UpsertOrder(model.Order{Request: req("protected"), Status: enums.StatusNew})
	if err := e.Check(req("just-reserved"), nil); err != nil {
		t.Fatal(err)
	}
	if err := e.Check(req("just-reserved"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("new reservation must be duplicate-protected until a later window advance, got %v", err)
	}
}

func TestClientIDRetentionKeepsEarlierConcurrentVenueValidation(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceDefault, now, "BTC")
	request := func(clientID string) model.OrderRequest {
		return model.OrderRequest{
			InstrumentID: model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp},
			ClientID:     clientID,
			Side:         enums.SideBuy,
			Quantity:     decimal.NewFromInt(1),
			Price:        decimal.NewFromInt(100),
		}
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	calls := atomic.Int32{}
	validator := preTradeValidatorFunc(func(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
		if calls.Add(1) <= 2 {
			entered <- struct{}{}
			<-release
		}
		return nil, nil
	})
	e := New(Limits{}, c).
		WithClock(func() time.Time { return now }).
		WithClientIDRetentionLimit(1).
		WithVenuePreTradeValidator(validator, enums.KindPerp)
	done := make(chan error, 2)
	for _, clientID := range []string{"validating-first", "validating-second"} {
		req := request(clientID)
		go func() {
			_, err := e.CheckContext(context.Background(), req, nil)
			done <- err
		}()
		<-entered
	}

	if _, err := e.CheckContext(context.Background(), request("validating-first"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("earlier validation client ID was evicted while venue I/O was in flight: %v", err)
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("venue validation: %v", err)
		}
	}
}
