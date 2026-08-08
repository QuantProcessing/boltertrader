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
		PrincipalID: "principal-a", Mode: ModeTest, ProductID: "funding", Kind: KindOpen,
		IdempotencyKey: "reserve-a",
		Scope:          NettingScope{AccountID: "account-a", Venue: "binance", InstrumentID: "BTC-USDT-PERP", VenueEnvironment: "testnet"},
		Quantity:       "0.01",
	}
	first, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusHeld || first.ProductID != "funding" || first.Mode != ModeTest {
		t.Fatalf("reservation = %+v", first)
	}

	conflict := request
	conflict.ProductID = "manual"
	conflict.IdempotencyKey = "reserve-b"
	if _, err := service.Create(context.Background(), conflict); !errors.Is(err, ErrScopeOwned) {
		t.Fatalf("cross-product Create error = %v, want ErrScopeOwned", err)
	}
	if _, err := service.Release(context.Background(), "principal-a", ModeTest, first.ReservationID); err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(context.Background(), conflict)
	if err != nil || second.ProductID != "manual" {
		t.Fatalf("reservation after release = %+v, err=%v", second, err)
	}
}

func TestServiceFencesPrincipalModeAndProductOnConsume(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	held, err := service.Create(context.Background(), CreateRequest{
		PrincipalID: "principal-a", Mode: ModeTest, ProductID: "funding", Kind: KindHedge,
		IdempotencyKey: "reserve-a",
		Scope:          NettingScope{AccountID: "account-a", Venue: "okx", InstrumentID: "ETH-USDT-SWAP", VenueEnvironment: "demo"},
		Notional:       "50", Currency: "USDT",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []ConsumeRequest{
		{ReservationID: held.ReservationID, PrincipalID: "principal-b", Mode: ModeTest, ProductID: "funding"},
		{ReservationID: held.ReservationID, PrincipalID: "principal-a", Mode: ModeLive, ProductID: "funding"},
		{ReservationID: held.ReservationID, PrincipalID: "principal-a", Mode: ModeTest, ProductID: "manual"},
	} {
		if _, err := service.Consume(context.Background(), request); !errors.Is(err, ErrOwnership) {
			t.Fatalf("Consume(%+v) error = %v, want ErrOwnership", request, err)
		}
	}
	consumed, err := service.Consume(context.Background(), ConsumeRequest{
		ReservationID: held.ReservationID, PrincipalID: "principal-a", Mode: ModeTest, ProductID: "funding",
	})
	if err != nil || consumed.Status != StatusConsumed {
		t.Fatalf("consumed = %+v, err=%v", consumed, err)
	}
}

func TestServiceReplaysReservationAndRejectsChangedIdempotentBody(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	request := CreateRequest{
		PrincipalID: "principal-a", Mode: ModeTest, ProductID: "manual", Kind: KindOpen, IdempotencyKey: "reserve-a",
		Scope: NettingScope{AccountID: "account-a", Venue: "binance", InstrumentID: "BTC-USDT", VenueEnvironment: "demo"}, Quantity: "1",
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

func TestServiceRejectsCrossModeVenueEnvironment(t *testing.T) {
	service := NewService(NewMemoryStore(), time.Now)
	request := CreateRequest{PrincipalID: "principal-a", Mode: ModeTest, ProductID: "manual", Kind: KindOpen, IdempotencyKey: "a", Scope: NettingScope{AccountID: "a", Venue: "v", InstrumentID: "i", VenueEnvironment: "live"}, Quantity: "1"}
	if _, err := service.Create(context.Background(), request); err == nil {
		t.Fatal("Test reservation accepted Live environment")
	}
	request.Mode, request.Scope.VenueEnvironment = ModeLive, "testnet"
	if _, err := service.Create(context.Background(), request); err == nil {
		t.Fatal("Live reservation accepted Test environment")
	}
}

func TestFreshServicesGenerateDistinctReservationIDs(t *testing.T) {
	store := NewMemoryStore()
	create := func(service *Service, key, instrument string) Reservation {
		t.Helper()
		item, err := service.Create(context.Background(), CreateRequest{
			PrincipalID: "principal-a", Mode: ModeTest, ProductID: "manual", Kind: KindOpen, IdempotencyKey: key,
			Scope: NettingScope{AccountID: "account-a", Venue: "binance", InstrumentID: instrument, VenueEnvironment: "demo"}, Quantity: "1",
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
