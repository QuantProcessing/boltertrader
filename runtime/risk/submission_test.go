package risk

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
)

var _ exec.SubmissionRiskChecker = (*Engine)(nil)

func TestCheckSubmissionReservesExposureUntilRelease(t *testing.T) {
	engine := New(Limits{MaxPositionQty: d("1")}, cache.New())
	first := buy("1", "100")
	first.ClientID = "reserved-first"
	release, err := engine.CheckSubmission(context.Background(), first, nil)
	if err != nil {
		t.Fatalf("first CheckSubmission: %v", err)
	}
	if release == nil {
		t.Fatal("CheckSubmission returned nil release")
	}

	second := buy("1", "100")
	second.ClientID = "reserved-second"
	if _, err := engine.CheckSubmission(context.Background(), second, nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("second CheckSubmission crossed reserved exposure: %v", err)
	}

	release()
	release() // release is intentionally idempotent.
	third := buy("1", "100")
	third.ClientID = "reserved-third"
	thirdRelease, err := engine.CheckSubmission(context.Background(), third, nil)
	if err != nil {
		t.Fatalf("CheckSubmission after release: %v", err)
	}
	thirdRelease()
}

func TestCheckSubmissionCancellationLeavesNoClientIDClaim(t *testing.T) {
	engine := New(Limits{}, cache.New())
	req := buy("1", "100")
	req.ClientID = "canceled-risk"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if release, err := engine.CheckSubmission(ctx, req, nil); release != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled CheckSubmission release=%v err=%v", release != nil, err)
	}
	release, err := engine.CheckSubmission(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("retry after cancellation: %v", err)
	}
	release()
}

func TestConfiguredRiskConcurrentSpotOrdersCannotOversubscribeFreeBalance(t *testing.T) {
	cached := cache.New()
	now := time.Unix(100, 0)
	applySpotCashAccount(t, cached, now, "100", "1")
	engine := configuredRisk(Limits{}, cached, now, enums.KindSpot)
	instrument := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}
	first := model.OrderRequest{
		InstrumentID: spotInst, ClientID: "spot-reserved-first", Side: enums.SideBuy,
		Quantity: d("0.6"), Price: d("100"),
	}
	release, err := engine.CheckSubmission(context.Background(), first, instrument)
	if err != nil {
		t.Fatalf("first CheckSubmission: %v", err)
	}
	second := first
	second.ClientID = "spot-reserved-second"
	second.Quantity = d("0.5")
	if _, err := engine.CheckSubmission(context.Background(), second, instrument); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("second CheckSubmission oversubscribed quote free balance: %v", err)
	}
	release()
	secondRelease, err := engine.CheckSubmission(context.Background(), second, instrument)
	if err != nil {
		t.Fatalf("CheckSubmission after release: %v", err)
	}
	secondRelease()
}

func TestConfiguredRiskConcurrentPerpOrdersRespectPositionAndNotionalLimits(t *testing.T) {
	engine := New(Limits{MaxPositionQty: d("1"), MaxOrderNotional: d("100")}, cache.New())
	first := buy("1", "100")
	first.ClientID = "perp-reserved-first"
	release, err := engine.CheckSubmission(context.Background(), first, nil)
	if err != nil {
		t.Fatalf("first CheckSubmission: %v", err)
	}
	second := buy("1", "100")
	second.ClientID = "perp-reserved-second"
	if _, err := engine.CheckSubmission(context.Background(), second, nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("second CheckSubmission crossed reserved position exposure: %v", err)
	}
	tooLarge := buy("1", "101")
	tooLarge.ClientID = "perp-notional-too-large"
	if _, err := engine.CheckSubmission(context.Background(), tooLarge, nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("CheckSubmission crossed per-order notional limit: %v", err)
	}
	release()
}

func TestCheckSubmissionConcurrentKillSwitchChangesAreRaceSafe(t *testing.T) {
	engine := New(Limits{}, cache.New())
	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range iterations {
			engine.Trip()
			engine.Reset()
		}
	}()
	go func() {
		defer wg.Done()
		for i := range iterations {
			req := buy("1", "100")
			req.ClientID = fmt.Sprintf("kill-switch-race-%d", i)
			release, err := engine.CheckSubmission(context.Background(), req, nil)
			if err == nil {
				release()
				continue
			}
			if !errors.Is(err, ErrRiskRejected) {
				t.Errorf("CheckSubmission err=%v, want nil or ErrRiskRejected", err)
			}
		}
	}()
	wg.Wait()
}
