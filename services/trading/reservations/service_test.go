package reservations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServiceRejectsCrossProductScopeConflictAndReleasesOwnership(t *testing.T) {
	now := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	service := NewService(NewMemoryStore(), func() time.Time { return now })
	request := CreateRequest{
		PrincipalID: "principal-a", ProductID: "manual", Kind: KindOpen,
		IdempotencyKey: "reserve-a",
		Scope:          NettingScope{AccountID: "account-a", Venue: "binance", InstrumentID: "BTC-USDT-PERP", VenueEnvironment: "live"},
		Quantity:       "0.01",
	}
	first, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusHeld || first.ProductID != "manual" {
		t.Fatalf("reservation = %+v", first)
	}

	conflict := request
	conflict.ProductID = "manual"
	conflict.IdempotencyKey = "reserve-b"
	if _, err := service.Create(context.Background(), conflict); !errors.Is(err, ErrScopeOwned) {
		t.Fatalf("cross-product Create error = %v, want ErrScopeOwned", err)
	}
	if _, err := service.Release(context.Background(), "principal-a", first.ReservationID); err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(context.Background(), conflict)
	if err != nil || second.ProductID != "manual" {
		t.Fatalf("reservation after release = %+v, err=%v", second, err)
	}
}

// TestServiceFencesPrincipalAndProductOnConsume had a third case: a consume
// naming the right principal and product but the wrong mode. Consume fenced on
// all three, and this test is the only place that fence was ever exercised.
// With one mode left there is nothing for a request to name wrongly, so the
// clause went from Consume and the case went from here.
func TestServiceFencesPrincipalAndProductOnConsume(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	held, err := service.Create(context.Background(), CreateRequest{
		PrincipalID: "principal-a", ProductID: "manual", Kind: KindHedge,
		IdempotencyKey: "reserve-a",
		Scope:          NettingScope{AccountID: "account-a", Venue: "okx", InstrumentID: "ETH-USDT-SWAP", VenueEnvironment: "live"},
		Notional:       "50", Currency: "USDT",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []ConsumeRequest{
		{ReservationID: held.ReservationID, PrincipalID: "principal-b", ProductID: "manual"},
		{ReservationID: held.ReservationID, PrincipalID: "principal-a", ProductID: "other"},
	} {
		if _, err := service.Consume(context.Background(), request); !errors.Is(err, ErrOwnership) {
			t.Fatalf("Consume(%+v) error = %v, want ErrOwnership", request, err)
		}
	}
	consumed, err := service.Consume(context.Background(), ConsumeRequest{
		ReservationID: held.ReservationID, PrincipalID: "principal-a", ProductID: "manual",
	})
	if err != nil || consumed.Status != StatusConsumed {
		t.Fatalf("consumed = %+v, err=%v", consumed, err)
	}
}

func TestServiceReplaysReservationAndRejectsChangedIdempotentBody(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	request := CreateRequest{
		PrincipalID: "principal-a", ProductID: "manual", Kind: KindOpen, IdempotencyKey: "reserve-a",
		Scope: NettingScope{AccountID: "account-a", Venue: "binance", InstrumentID: "BTC-USDT", VenueEnvironment: "live"}, Quantity: "1",
	}
	first, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Create(context.Background(), request)
	if err != nil || replay.ReservationID != first.ReservationID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	request.Quantity = "2"
	conflict, err := service.Create(context.Background(), request)
	if !errors.Is(err, ErrIdempotencyConflict) || conflict.ReservationID != first.ReservationID {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}

// TestServiceRejectsNonLiveVenueEnvironment keeps the refusal that outlived the
// rule it belonged to. This used to pin a cross-check — a Test reservation could
// not name a live venue, a Live one could not name anything else — but with demo
// and testnet gone there is no longer a second environment for a mode to
// disagree with, so the cross-check went and the allow-list is all that remains.
// A reservation naming an environment the platform cannot route to is still
// refused, which is the half of the old rule that still has work to do.
func TestServiceRejectsNonLiveVenueEnvironment(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	request := CreateRequest{PrincipalID: "principal-a", ProductID: "manual", Kind: KindOpen, IdempotencyKey: "a", Scope: NettingScope{AccountID: "a", Venue: "v", InstrumentID: "i"}, Quantity: "1"}
	for _, environment := range []string{"demo", "testnet", ""} {
		request.Scope.VenueEnvironment = environment
		if _, err := service.Create(context.Background(), request); err == nil {
			t.Fatalf("reservation accepted venue environment %q", environment)
		}
	}
	request.Scope.VenueEnvironment = "live"
	if _, err := service.Create(context.Background(), request); err != nil {
		t.Fatalf("reservation refused the only surviving environment: %v", err)
	}
}

func TestServiceRejectsQuantityAndNotionalTogether(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	request := CreateRequest{
		PrincipalID: "principal-a", ProductID: "manual", Kind: KindOpen, IdempotencyKey: "reserve-a",
		Scope:    NettingScope{AccountID: "account-a", Venue: "binance", InstrumentID: "BTC-USDT", VenueEnvironment: "live"},
		Quantity: "0.5", Notional: "50", Currency: "USDT",
	}
	if _, err := service.Create(context.Background(), request); err == nil {
		t.Fatal("reservation accepted quantity and notional together")
	}
}

func TestFreshServicesGenerateDistinctReservationIDs(t *testing.T) {
	store := NewMemoryStore()
	create := func(service *Service, key, instrument string) Reservation {
		t.Helper()
		item, err := service.Create(context.Background(), CreateRequest{
			PrincipalID: "principal-a", ProductID: "manual", Kind: KindOpen, IdempotencyKey: key,
			Scope: NettingScope{AccountID: "account-a", Venue: "binance", InstrumentID: instrument, VenueEnvironment: "live"}, Quantity: "1",
		})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	first := create(NewService(store, time.Now), "first", "BTC-USDT")
	second := create(NewService(store, time.Now), "second", "ETH-USDT")
	if first.ReservationID == second.ReservationID {
		t.Fatalf("fresh services generated colliding reservation id %q", first.ReservationID)
	}
}
