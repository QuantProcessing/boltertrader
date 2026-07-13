package nado

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func TestNadoPreTradeSafetyReviewedBlockers(t *testing.T) {
	t.Run("product capability trading follows submit backend", func(t *testing.T) {
		exec, deps, _ := newPreTradeSafetyExec(t, enums.KindSpot)
		exec.pretrade = deps
		caps := exec.Capabilities()
		if !caps.Trading.Submit || len(caps.Products) != 1 || !caps.Products[0].Trading {
			t.Fatalf("capability mismatch: %+v", caps)
		}
	})

	t.Run("spot max and prepared payload use documented no-leverage flag", func(t *testing.T) {
		exec, deps, req := newPreTradeSafetyExec(t, enums.KindSpot)
		if _, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID)); err != nil {
			t.Fatalf("ValidatePreTrade: %v", err)
		}
		if deps.maxReq.SpotLeverage == nil || *deps.maxReq.SpotLeverage || deps.maxReq.BorrowMargin != nil {
			t.Fatalf("max_order_size spot flags mismatch: %+v", deps.maxReq)
		}
		if deps.input.SpotLeverage == nil || *deps.input.SpotLeverage || deps.input.BorrowMargin != nil {
			t.Fatalf("prepared spot flags mismatch: %+v", deps.input)
		}
	})

	t.Run("duplicate prepared client id fails without replacing active lease", func(t *testing.T) {
		exec, deps, req := newPreTradeSafetyExec(t, enums.KindSpot)
		first := deps.prepared
		if _, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID)); err != nil {
			t.Fatalf("first ValidatePreTrade: %v", err)
		}
		replacement := preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "replacement")
		deps.prepared = replacement
		callsBefore := deps.prepareCalls
		if _, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID)); err == nil {
			t.Fatal("duplicate ValidatePreTrade succeeded")
		}
		if deps.prepareCalls != callsBefore {
			t.Fatalf("duplicate performed venue prepare: before=%d after=%d", callsBefore, deps.prepareCalls)
		}
		deps.executed = &sdk.PlaceOrderResponse{Digest: "original"}
		order, err := exec.Submit(context.Background(), req)
		if err != nil {
			t.Fatalf("Submit after duplicate: %v", err)
		}
		if order.VenueOrderID != "original" || first.Signature != "" {
			t.Fatalf("original lease not consumed/redacted correctly order=%+v original=%+v", order, first)
		}
	})

	t.Run("capacity full fails closed without evicting active lease", func(t *testing.T) {
		exec, deps, req := newPreTradeSafetyExec(t, enums.KindSpot)
		exec.prepared = newPreparedOrderCache(1, time.Minute)
		if _, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID)); err != nil {
			t.Fatalf("first ValidatePreTrade: %v", err)
		}
		second := req
		second.ClientID = "client-pretrade-2"
		deps.prepared = preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "second")
		if _, err := exec.ValidatePreTrade(context.Background(), second, mustNadoInstrument(t, exec, second.InstrumentID)); err == nil || !strings.Contains(err.Error(), "capacity") {
			t.Fatalf("capacity-full err=%v", err)
		}
		deps.executed = &sdk.PlaceOrderResponse{Digest: "first"}
		if _, err := exec.Submit(context.Background(), req); err != nil {
			t.Fatalf("first active lease was evicted: %v", err)
		}
	})

	t.Run("unsupported trigger fields rejected locally and prepared matching is exact", func(t *testing.T) {
		exec, deps, req := newPreTradeSafetyExec(t, enums.KindSpot)
		req.TriggerPrice = decimal.RequireFromString("10")
		if _, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID)); err == nil {
			t.Fatal("trigger order was accepted")
		}
		req.TriggerPrice = decimal.Zero
		if _, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID)); err != nil {
			t.Fatalf("ValidatePreTrade: %v", err)
		}
		changed := req
		changed.ActivationPrice = decimal.RequireFromString("9")
		deps.executed = &sdk.PlaceOrderResponse{Digest: "should-not-execute"}
		if _, err := exec.Submit(context.Background(), changed); err == nil {
			t.Fatal("mismatched request consumed prepared state")
		}
	})

	t.Run("terminal tombstones expire", func(t *testing.T) {
		clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 6, 0, 0, 0, time.UTC))
		exec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
		deps := newRecordingPreTradeDeps()
		exec.pretrade = deps
		exec.prepared = newPreparedOrderCache(8, time.Second)
		req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
		lease, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID))
		if err != nil {
			t.Fatalf("ValidatePreTrade: %v", err)
		}
		lease.Release()
		clk.Advance(2 * time.Second)
		deps.prepared = preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "after-ttl")
		if _, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID)); err != nil {
			t.Fatalf("terminal tombstone did not expire: %v", err)
		}
	})

	t.Run("expired prepared payload fails without runtime revalidation", func(t *testing.T) {
		clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 6, 0, 0, 0, time.UTC))
		exec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
		deps := newRecordingPreTradeDeps()
		exec.pretrade = deps
		exec.prepared = newPreparedOrderCache(8, time.Second)
		req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
		if _, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID)); err != nil {
			t.Fatalf("ValidatePreTrade: %v", err)
		}
		clk.Advance(2 * time.Second)
		callsBefore := append([]string(nil), deps.calls...)

		if _, err := exec.SubmitPrepared(context.Background(), req); err == nil || !errors.Is(err, contract.ErrPreparedStateUnavailable) {
			t.Fatalf("expired prepared submit err=%v", err)
		}
		if len(deps.calls) != len(callsBefore) {
			t.Fatalf("expired runtime submit reconstructed venue validation: before=%v after=%v", callsBefore, deps.calls)
		}
		if got := exec.preparedLen(); got != 0 {
			t.Fatalf("expired prepared entries=%d, want 0", got)
		}
	})

	t.Run("terminal tombstones are bounded and do not evict active leases", func(t *testing.T) {
		clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 6, 0, 0, 0, time.UTC))
		exec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
		deps := newRecordingPreTradeDeps()
		exec.pretrade = deps
		exec.prepared = newPreparedOrderCache(2, time.Minute)
		first := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
		lease, err := exec.ValidatePreTrade(context.Background(), first, mustNadoInstrument(t, exec, first.InstrumentID))
		if err != nil {
			t.Fatalf("first ValidatePreTrade: %v", err)
		}
		lease.Release()
		second := first
		second.ClientID = "client-pretrade-2"
		deps.prepared = preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "second")
		lease2, err := exec.ValidatePreTrade(context.Background(), second, mustNadoInstrument(t, exec, second.InstrumentID))
		if err != nil {
			t.Fatalf("second ValidatePreTrade: %v", err)
		}
		third := first
		third.ClientID = "client-pretrade-3"
		deps.prepared = preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "third")
		if _, err := exec.ValidatePreTrade(context.Background(), third, mustNadoInstrument(t, exec, third.InstrumentID)); err == nil || !strings.Contains(err.Error(), "capacity") {
			t.Fatalf("active lease was evicted or capacity not enforced: %v", err)
		}
		lease2.Release()
		deps.prepared = preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "third-after-release")
		if _, err := exec.ValidatePreTrade(context.Background(), third, mustNadoInstrument(t, exec, third.InstrumentID)); err != nil {
			t.Fatalf("oldest tombstone was not evicted to keep bounded throughput: %v", err)
		}
	})
}

func TestNadoPreTradeConcurrentReservationAllowsOneVenueIO(t *testing.T) {
	exec, deps, req := newPreTradeSafetyExec(t, enums.KindSpot)
	deps.blockPrepare = make(chan struct{})
	started := make(chan struct{})
	deps.onPrepare = func() {
		close(started)
		<-deps.blockPrepare
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID))
		}(i)
		if i == 0 {
			<-started
		}
	}
	close(deps.blockPrepare)
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 || deps.prepareCalls != 1 {
		t.Fatalf("successes=%d prepareCalls=%d errs=%v", successes, deps.prepareCalls, errs)
	}
}

func newPreTradeSafetyExec(t *testing.T, kind enums.InstrumentKind) (*executionClient, *recordingPreTradeDeps, model.OrderRequest) {
	t.Helper()
	exec := newExecutionClient(nil, nadoTestProvider(), clock.NewSimulatedClock(time.Date(2026, 7, 10, 6, 0, 0, 0, time.UTC)), kind, AccountIDUnified)
	deps := newRecordingPreTradeDeps()
	exec.pretrade = deps
	req := nadoTestOrderRequest(kind, enums.SideBuy)
	return exec, deps, req
}

func newRecordingPreTradeDeps() *recordingPreTradeDeps {
	return &recordingPreTradeDeps{
		sender:     "sender-1",
		maxSizeX18: "5000000000000000000",
		prepared:   preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "digest-safe"),
		executed:   &sdk.PlaceOrderResponse{Digest: "digest-safe"},
	}
}
