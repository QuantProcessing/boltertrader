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
)

type countingLease struct {
	releases atomic.Int32
}

func (l *countingLease) Release() { l.releases.Add(1) }

type preTradeValidatorFunc func(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error)

func (f preTradeValidatorFunc) ValidatePreTrade(ctx context.Context, req model.OrderRequest, inst *model.Instrument) (contract.PreTradeLease, error) {
	return f(ctx, req, inst)
}

func TestConfiguredVenueValidatorRequiresContextCheck(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceDefault, now, "BTC")
	calls := atomic.Int32{}
	validator := preTradeValidatorFunc(func(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
		calls.Add(1)
		return nil, nil
	})
	e := New(Limits{}, c).
		WithClock(func() time.Time { return now }).
		WithVenuePreTradeValidator(validator, enums.KindPerp)
	req := buy("1", "100")
	req.ClientID = "context-required"

	if err := e.Check(req, nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("plain Check should fail closed for validator-backed kind, got %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("validator calls=%d, want 0 from plain Check", got)
	}
	lease, err := e.CheckContext(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("context check: %v", err)
	}
	if lease != nil {
		t.Fatalf("lease=%T, want nil from validator", lease)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("validator calls=%d, want 1", got)
	}
}

func TestVenueValidatorRunsAfterLocalChecksAndReplacesBalanceClaim(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	// The account is fresh and margin-capable but deliberately has no USDT free
	// balance. Venue validation, not a synthetic BalanceFree claim, owns capacity.
	applyMarginAccount(t, c, model.AccountIDBinanceDefault, now, "BTC")
	calls := atomic.Int32{}
	validator := preTradeValidatorFunc(func(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
		calls.Add(1)
		return nil, nil
	})
	e := New(Limits{MaxOrderQty: d("2")}, c).
		WithClock(func() time.Time { return now }).
		RequireAccountState().
		WithVenuePreTradeValidator(validator, enums.KindPerp)

	tooLarge := buy("3", "100")
	tooLarge.ClientID = "too-large"
	if _, err := e.CheckContext(context.Background(), tooLarge, nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("oversized order should fail locally, got %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("validator calls=%d, want 0 after local rejection", got)
	}

	valid := buy("1", "100")
	valid.ClientID = "venue-capacity"
	if _, err := e.CheckContext(context.Background(), valid, nil); err != nil {
		t.Fatalf("venue-backed capacity should not require BalanceFree: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("validator calls=%d, want 1", got)
	}
}

func TestVenueValidatorRejectsStaleAccountBeforeVenueIO(t *testing.T) {
	eventTime := time.Unix(100, 0)
	now := eventTime.Add(time.Hour)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceDefault, eventTime, "BTC")
	calls := atomic.Int32{}
	validator := preTradeValidatorFunc(func(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
		calls.Add(1)
		return nil, nil
	})
	e := New(Limits{}, c).
		WithClock(func() time.Time { return now }).
		WithVenuePreTradeValidator(validator, enums.KindPerp)
	req := buy("1", "100")
	req.ClientID = "stale-before-venue"

	if _, err := e.CheckContext(context.Background(), req, nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("stale account should fail closed, got %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("validator calls=%d, want 0 for stale local account", got)
	}
}

func TestVenueValidatorReservesClientIDBeforeConcurrentVenueIO(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceDefault, now, "BTC")
	entered := make(chan struct{})
	unblock := make(chan struct{})
	calls := atomic.Int32{}
	validator := preTradeValidatorFunc(func(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
		calls.Add(1)
		close(entered)
		<-unblock
		return nil, nil
	})
	e := New(Limits{}, c).
		WithClock(func() time.Time { return now }).
		WithVenuePreTradeValidator(validator, enums.KindPerp)
	req := buy("1", "100")
	req.ClientID = "concurrent-reservation"
	firstDone := make(chan error, 1)
	go func() {
		_, err := e.CheckContext(context.Background(), req, nil)
		firstDone <- err
	}()
	<-entered

	if _, err := e.CheckContext(context.Background(), req, nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("concurrent duplicate should fail closed, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("validator calls=%d, want only the reserved request", got)
	}
	close(unblock)
	if err := <-firstDone; err != nil {
		t.Fatalf("reserved request failed: %v", err)
	}
}

func TestVenueValidatorDoesNotHoldRiskMutexDuringIO(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceDefault, now, "BTC")
	entered := make(chan struct{})
	unblock := make(chan struct{})
	validator := preTradeValidatorFunc(func(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
		close(entered)
		<-unblock
		return nil, nil
	})
	e := New(Limits{}, c).
		WithClock(func() time.Time { return now }).
		WithVenuePreTradeValidator(validator, enums.KindPerp)
	req := buy("1", "100")
	req.ClientID = "unlocked-io"
	done := make(chan error, 1)
	go func() {
		_, err := e.CheckContext(context.Background(), req, nil)
		done <- err
	}()
	<-entered

	tripped := make(chan struct{})
	go func() {
		e.Trip()
		close(tripped)
	}()
	select {
	case <-tripped:
	case <-time.After(time.Second):
		t.Fatal("Trip blocked while venue validator was in I/O")
	}
	close(unblock)
	if err := <-done; !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("context check after concurrent Trip should fail closed, got %v", err)
	}
}

func TestVenueValidatorLeaseCleanupOnErrorAndCancellation(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceDefault, now, "BTC")
	validationErr := errors.New("venue validation failed")
	errorLease := &countingLease{}
	e := New(Limits{}, c).
		WithClock(func() time.Time { return now }).
		WithVenuePreTradeValidator(preTradeValidatorFunc(func(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
			return errorLease, validationErr
		}), enums.KindPerp)
	req := buy("1", "100")
	req.ClientID = "validator-error"
	if lease, err := e.CheckContext(context.Background(), req, nil); !errors.Is(err, validationErr) || lease != nil {
		t.Fatalf("validator error result lease=%T err=%v", lease, err)
	}
	if got := errorLease.releases.Load(); got != 1 {
		t.Fatalf("error lease releases=%d, want 1", got)
	}

	cancelLease := &countingLease{}
	ctx, cancel := context.WithCancel(context.Background())
	e.WithVenuePreTradeValidator(preTradeValidatorFunc(func(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
		cancel()
		return cancelLease, nil
	}), enums.KindPerp)
	req.ClientID = "validator-cancel"
	if lease, err := e.CheckContext(ctx, req, nil); !errors.Is(err, context.Canceled) || lease != nil {
		t.Fatalf("canceled validation result lease=%T err=%v", lease, err)
	}
	if got := cancelLease.releases.Load(); got != 1 {
		t.Fatalf("canceled lease releases=%d, want 1", got)
	}

	// A failed/canceled validation must roll back its temporary client-ID claim.
	e.WithVenuePreTradeValidator(preTradeValidatorFunc(func(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
		return nil, nil
	}), enums.KindPerp)
	if _, err := e.CheckContext(context.Background(), req, nil); err != nil {
		t.Fatalf("retry after canceled validation should be allowed: %v", err)
	}
}

func TestSuccessfulVenueValidatorTransfersLeaseOwnership(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceDefault, now, "BTC")
	want := &countingLease{}
	e := New(Limits{}, c).
		WithClock(func() time.Time { return now }).
		WithVenuePreTradeValidator(preTradeValidatorFunc(func(context.Context, model.OrderRequest, *model.Instrument) (contract.PreTradeLease, error) {
			return want, nil
		}), enums.KindPerp)
	req := buy("1", "100")
	req.ClientID = "lease-transfer"

	got, err := e.CheckContext(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("context check: %v", err)
	}
	if got != want {
		t.Fatalf("lease=%T %p, want %p", got, got, want)
	}
	if releases := want.releases.Load(); releases != 0 {
		t.Fatalf("lease released before handoff: %d", releases)
	}
}
