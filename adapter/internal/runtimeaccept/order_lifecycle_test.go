package runtimeaccept

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/orderstate"
	"github.com/shopspring/decimal"
)

func TestAdapterOrderLifecyclePlacesCancelsFillsAndCloses(t *testing.T) {
	exec := &recordingLifecycleExec{}
	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	result, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "test perp",
		AccountID:      "TEST:unified",
		InstrumentID:   instID,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
	})
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if result.FilledQty.String() != "0.01" {
		t.Fatalf("filled qty=%s, want 0.01", result.FilledQty)
	}
	if result.ClosedQty.String() != "0.01" {
		t.Fatalf("closed qty=%s, want 0.01", result.ClosedQty)
	}
	if len(exec.submits) != 3 {
		t.Fatalf("submits=%d, want resting/fill/close: %+v", len(exec.submits), exec.submits)
	}
	if got := exec.submits[0]; got.Side != enums.SideBuy || got.TIF != enums.TifGTX || got.Price.String() != "49000" {
		t.Fatalf("resting submit=%+v", got)
	}
	if got := exec.cancelVenueOrderID; got != "venue-1" {
		t.Fatalf("cancel venue order id=%q, want venue-1", got)
	}
	if got := exec.submits[1]; got.Side != enums.SideBuy || got.TIF != enums.TifIOC || got.Price.String() != "51000" {
		t.Fatalf("fill submit=%+v", got)
	}
	if got := exec.submits[2]; got.Side != enums.SideSell || got.TIF != enums.TifIOC || !got.ReduceOnly || got.Price.String() != "50000" {
		t.Fatalf("close submit=%+v", got)
	}
}

func TestAdapterOrderLifecycleUsesExplicitCloseQuantity(t *testing.T) {
	exec := &recordingLifecycleExec{}
	instID := model.InstrumentID{Venue: "TEST", Symbol: "ETH-USDT", Kind: enums.KindSpot}
	result, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "test spot",
		AccountID:      "TEST:cash",
		InstrumentID:   instID,
		Quantity:       decimal.RequireFromString("0.01"),
		CloseQuantity:  decimal.RequireFromString("0.009"),
		RestingPrice:   decimal.RequireFromString("4900"),
		FillPrice:      decimal.RequireFromString("5100"),
		ClosePrice:     decimal.RequireFromString("5000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
	})
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if result.ClosedQty.String() != "0.009" {
		t.Fatalf("closed qty=%s, want 0.009", result.ClosedQty)
	}
	if len(exec.submits) != 3 {
		t.Fatalf("submits=%d, want resting/fill/close: %+v", len(exec.submits), exec.submits)
	}
	if got := exec.submits[2]; got.Side != enums.SideSell || got.Quantity.String() != "0.009" || got.ReduceOnly {
		t.Fatalf("close submit=%+v", got)
	}
}

func TestAdapterOrderLifecycleClosesAuthoritativePartialIOCQuantity(t *testing.T) {
	exec := &partialOpeningLifecycleExec{partialQty: decimal.RequireFromString("0.004")}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	result, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "partial IOC open",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		CloseQuantity:  decimal.RequireFromString("0.009"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if result == nil || !result.FilledQty.Equal(exec.partialQty) || !result.ClosedQty.Equal(exec.partialQty) {
		t.Fatalf("result=%+v, want partial fill and bounded close %s", result, exec.partialQty)
	}
	if len(exec.submits) != 3 || !exec.submits[2].Quantity.Equal(exec.partialQty) {
		t.Fatalf("submits=%+v, want close capped to authoritative partial fill %s", exec.submits, exec.partialQty)
	}
}

func TestAdapterSpotOrderLifecycleScalesGuardedCloseToPartialIOCQuantity(t *testing.T) {
	exec := &partialOpeningLifecycleExec{partialQty: decimal.RequireFromString("0.004")}
	reporter := &sequenceAccountStateSource{states: []model.AccountState{
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.004", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.004", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.001", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.001", "0"),
	}}
	spec := ConfigureSpotBalanceGuard(OrderLifecycleSpec{
		Label:          "partial guarded Spot IOC",
		Venue:          "TEST",
		AccountID:      "TEST:unified",
		InstrumentID:   model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindSpot},
		Quantity:       decimal.RequireFromString("0.01"),
		CloseQuantity:  decimal.RequireFromString("0.009"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 20 * time.Millisecond,
	}, reporter, "BTC", decimal.RequireFromString("0.001"), decimal.RequireFromString("0.001"), decimal.NewFromInt(100), decimal.RequireFromString("0.001"))

	result, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	wantClose := decimal.RequireFromString("0.003")
	if result == nil || !result.ClosedQty.Equal(wantClose) || !exec.submits[2].Quantity.Equal(wantClose) {
		t.Fatalf("result=%+v submits=%+v, want guarded close scaled and floored to %s", result, exec.submits, wantClose)
	}
}

func TestGuardedSpotFullFillKeepsConfiguredCloseQuantityAtTickBoundary(t *testing.T) {
	spec := ConfigureSpotBalanceGuard(OrderLifecycleSpec{
		AccountID:      "TEST:cash",
		InstrumentID:   model.InstrumentID{Venue: "TEST", Symbol: "ASTER-USDT", Kind: enums.KindSpot},
		Quantity:       decimal.RequireFromString("8.09"),
		CloseQuantity:  decimal.RequireFromString("8.04"),
		RestingPrice:   decimal.RequireFromString("0.62"),
		FillPrice:      decimal.RequireFromString("0.64"),
		ClosePrice:     decimal.RequireFromString("0.62227"),
		CloseAfterFill: true,
	}, &sequenceAccountStateSource{}, "ASTER",
		decimal.RequireFromString("0.01"), decimal.RequireFromString("0.01"),
		decimal.NewFromInt(5), decimal.RequireFromString("0.05"))

	got, err := spec.closeQuantity(spec.Quantity)
	if err != nil {
		t.Fatalf("closeQuantity: %v", err)
	}
	if !got.Equal(spec.CloseQuantity) {
		t.Fatalf("full-fill close quantity=%s, want configured tick-aligned %s", got, spec.CloseQuantity)
	}
}

func TestAdapterSpotOrderLifecycleRejectsScaledPartialCloseBelowVenueMinimum(t *testing.T) {
	tests := []struct {
		name        string
		minQty      string
		minNotional string
		want        string
	}{
		{name: "minimum quantity", minQty: "0.004", minNotional: "1", want: "minimum quantity"},
		{name: "minimum notional", minQty: "0.001", minNotional: "200", want: "minimum notional"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &partialOpeningLifecycleExec{partialQty: decimal.RequireFromString("0.004")}
			reporter := &sequenceAccountStateSource{states: []model.AccountState{
				spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
				spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
				spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
				spotBalanceState("TEST:unified", "TEST", "BTC", "10.004", "0"),
				spotBalanceState("TEST:unified", "TEST", "BTC", "10.004", "0"),
				spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
				spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
			}}
			baseSpec := terminalFilledLifecycleSpec()
			baseSpec.CloseQuantity = decimal.RequireFromString("0.009")
			spec := ConfigureSpotBalanceGuard(baseSpec, reporter, "BTC",
				decimal.RequireFromString("0.001"), decimal.RequireFromString(tt.minQty),
				decimal.RequireFromString(tt.minNotional), decimal.RequireFromString("0.001"))
			spec.CleanupTimeout = 20 * time.Millisecond

			_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("RunAdapterOrderLifecycle err=%v, want scaled close %s failure", err, tt.want)
			}
			if len(exec.submits) != 2 {
				t.Fatalf("submits=%d, want resting and opening buy only: %+v", len(exec.submits), exec.submits)
			}
		})
	}
}

func TestAdapterOrderLifecycleRejectsTerminalZeroFillWithoutClose(t *testing.T) {
	exec := &partialOpeningLifecycleExec{}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "zero IOC open",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 10 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "terminal status") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want terminal zero-fill failure", err)
	}
	if len(exec.submits) != 2 {
		t.Fatalf("submits=%d, want resting and zero-fill IOC only: %+v", len(exec.submits), exec.submits)
	}
}

func TestAdapterOrderLifecycleHandlesStatusFilledPartialAndZeroQuantities(t *testing.T) {
	tests := []struct {
		name        string
		openingQty  decimal.Decimal
		closeQty    decimal.Decimal
		wantFilled  decimal.Decimal
		wantClosed  decimal.Decimal
		wantErr     string
		wantSubmits int
	}{
		{
			name:        "opening partial closes actual quantity",
			openingQty:  decimal.RequireFromString("0.004"),
			closeQty:    decimal.RequireFromString("0.004"),
			wantFilled:  decimal.RequireFromString("0.004"),
			wantClosed:  decimal.RequireFromString("0.004"),
			wantSubmits: 3,
		},
		{
			name:        "opening zero fails without close",
			openingQty:  decimal.Zero,
			closeQty:    decimal.Zero,
			wantErr:     "terminal status FILLED with zero fill",
			wantSubmits: 2,
		},
		{
			name:        "close partial fails with actual quantity",
			openingQty:  decimal.RequireFromString("0.01"),
			closeQty:    decimal.RequireFromString("0.004"),
			wantErr:     "partial fill 0.004/0.009",
			wantSubmits: 3,
		},
		{
			name:        "close zero fails",
			openingQty:  decimal.RequireFromString("0.01"),
			closeQty:    decimal.Zero,
			wantErr:     "terminal status FILLED with zero fill",
			wantSubmits: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := newTerminalFilledLifecycleExec(tt.openingQty, tt.closeQty)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			result, err := RunAdapterOrderLifecycle(ctx, exec, terminalFilledLifecycleSpec())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("RunAdapterOrderLifecycle err=%v, want %q", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("RunAdapterOrderLifecycle: %v", err)
				}
				if result == nil || !result.FilledQty.Equal(tt.wantFilled) || !result.ClosedQty.Equal(tt.wantClosed) {
					t.Fatalf("result=%+v, want filled=%s closed=%s", result, tt.wantFilled, tt.wantClosed)
				}
			}
			if len(exec.submits) != tt.wantSubmits {
				t.Fatalf("submits=%d, want %d: %+v", len(exec.submits), tt.wantSubmits, exec.submits)
			}
			if tt.wantErr == "" && !exec.submits[2].Quantity.Equal(tt.wantClosed) {
				t.Fatalf("close quantity=%s, want actual opening fill %s", exec.submits[2].Quantity, tt.wantClosed)
			}
		})
	}
}

func TestAdapterOrderLifecycleLogsAcceptanceEvidence(t *testing.T) {
	exec := &recordingLifecycleExec{}
	var logs []string
	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:               "test perp",
		Venue:               "TEST",
		Environment:         "Demo",
		Product:             "USDT-linear Perp/SWAP",
		AccountID:           "TEST:unified",
		InstrumentID:        instID,
		Quantity:            decimal.RequireFromString("0.01"),
		RestingPrice:        decimal.RequireFromString("49000"),
		FillPrice:           decimal.RequireFromString("51000"),
		ClosePrice:          decimal.RequireFromString("50000"),
		PositionSide:        enums.PosNet,
		CloseAfterFill:      true,
		PrivateStreamTopics: []string{"order", "execution", "position", "wallet"},
		Logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{
		"venue=TEST",
		"environment=Demo",
		"product=USDT-linear Perp/SWAP",
		"instrument=TEST:BTC-USDT:PERP",
		"account_id=TEST:unified",
		"private_stream_topics=order,execution,position,wallet",
		"resting_order",
		"venue_order_id=venue-1",
		"filled_order",
		"closed_order",
		"cleanup=no_open_orders",
		"cleanup=flat_position",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("logs missing %q:\n%s", want, joined)
		}
	}
}

func TestAdapterOrderLifecycleRejectsMismatchedAccountIDEvidence(t *testing.T) {
	exec := &mismatchedAccountLifecycleExec{}
	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "mismatched account",
		AccountID:      "TEST:unified",
		InstrumentID:   instID,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
	})
	if err == nil || !strings.Contains(err.Error(), "account_id") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want account_id mismatch", err)
	}
}

func TestAdapterOrderLifecycleRejectsWrongSubmitIdentityBeforeCancel(t *testing.T) {
	for _, mode := range []string{"client", "instrument", "order_and_error"} {
		t.Run(mode, func(t *testing.T) {
			exec := &wrongSubmitIdentityExec{mode: mode}
			spec := terminalFilledLifecycleSpec()

			_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
			if err == nil || (!strings.Contains(err.Error(), "identity") && !strings.Contains(err.Error(), "instrument")) {
				t.Fatalf("RunAdapterOrderLifecycle err=%v, want submit evidence rejection", err)
			}
			if exec.cancelVenueOrderID != "" {
				t.Fatalf("canceled unrelated venue order %q", exec.cancelVenueOrderID)
			}
		})
	}
}

func TestAdapterOrderLifecycleBindsExactSubmitIdentityBeforeSemanticRejection(t *testing.T) {
	exec := &semanticMismatchSubmitExec{}
	spec := terminalFilledLifecycleSpec()
	spec.PollInterval = time.Millisecond
	spec.CleanupTimeout = 50 * time.Millisecond

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err == nil || !strings.Contains(err.Error(), "order quantity") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want semantic quantity rejection", err)
	}
	if len(exec.canceled) == 0 || exec.canceled[0] != "semantic-venue-1" {
		t.Fatalf("canceled=%v, want exact semantic-mismatch venue order cleanup", exec.canceled)
	}
}

func TestSubmitAndWaitFilledRejectsOrderAndErrorBeforeTracking(t *testing.T) {
	exec := &wrongSubmitIdentityExec{mode: "order_and_error"}
	spec := terminalFilledLifecycleSpec()
	tracker := newLifecycleOrderTracker()

	order, filledQty, err := submitAndWaitFilled(context.Background(), exec, spec, tracker, "fill", enums.SideBuy, spec.FillPrice, false, spec.Quantity)
	if err == nil || order != nil || !filledQty.IsZero() || !strings.Contains(err.Error(), "order identity") {
		t.Fatalf("submitAndWaitFilled order=%+v filled=%s err=%v, want evidence rejection", order, filledQty, err)
	}
	tracked := tracker.byKind("fill")
	if tracked == nil || tracked.venueOrderID != "" {
		t.Fatalf("tracked=%+v, mismatched order+error must not bind venue identity", tracked)
	}
}

func TestWaitForRuntimeFilledQtyRejectsCachedInstrumentConflict(t *testing.T) {
	exec := &recordingLifecycleExec{}
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec}, clock.NewRealClock(), "runtime-cache-conflict",
		btruntime.WithAccountID("TEST:unified"),
	)
	spec := terminalFilledLifecycleSpec()
	tracker := newLifecycleOrderTracker()
	tracked := tracker.add("fill")
	tracked.request = model.OrderRequest{
		AccountID: spec.AccountID, ClientID: tracked.clientID, InstrumentID: spec.InstrumentID,
		Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifIOC, Quantity: spec.Quantity, Price: spec.FillPrice,
	}
	conflict := model.Order{Request: tracked.request, VenueOrderID: "venue-conflict", Status: enums.StatusFilled, FilledQty: spec.Quantity}
	conflict.Request.InstrumentID.Symbol = "ETH-USDT"
	node.Cache.UpsertOrder(conflict)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	order, filledQty, err := waitForRuntimeFilledQty(ctx, node, spec, tracker, tracked, tracked.clientID, spec.Quantity)
	if err == nil || order.Request.ClientID != "" || !filledQty.IsZero() || !strings.Contains(err.Error(), "instrument") {
		t.Fatalf("waitForRuntimeFilledQty order=%+v filled=%s err=%v, want cached instrument rejection", order, filledQty, err)
	}
}

func TestAdapterOrderLifecycleRejectsPreExistingPositionBeforeSubmit(t *testing.T) {
	exec := &cleanupLifecycleExec{existing: decimal.RequireFromString("0.0003")}
	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:                 "cleanup test perp",
		AccountID:             "TEST:unified",
		InstrumentID:          instID,
		Quantity:              decimal.RequireFromString("0.01"),
		RestingPrice:          decimal.RequireFromString("49000"),
		FillPrice:             decimal.RequireFromString("51000"),
		ClosePrice:            decimal.RequireFromString("50000"),
		PositionSide:          enums.PosNet,
		CloseAfterFill:        true,
		CleanExistingPosition: true,
		PollInterval:          time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "pre-existing position") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want pre-existing position rejection", err)
	}
	if len(exec.submits) != 0 {
		t.Fatalf("submits=%d, want 0 before pre-existing position rejection: %+v", len(exec.submits), exec.submits)
	}
}

func TestAdapterOrderLifecycleRejectsOffsettingPreExistingPositionsBeforeSubmit(t *testing.T) {
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	exec := &positionReportsLifecycleExec{reports: []model.PositionReport{
		{Position: model.Position{AccountID: "TEST:unified", InstrumentID: id, Side: enums.PosLong, Quantity: decimal.RequireFromString("0.01")}},
		{Position: model.Position{AccountID: "TEST:unified", InstrumentID: id, Side: enums.PosShort, Quantity: decimal.RequireFromString("-0.01")}},
	}}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:                 "offsetting pre-existing positions",
		AccountID:             "TEST:unified",
		InstrumentID:          id,
		Quantity:              decimal.RequireFromString("0.01"),
		RestingPrice:          decimal.RequireFromString("49000"),
		FillPrice:             decimal.RequireFromString("51000"),
		ClosePrice:            decimal.RequireFromString("50000"),
		PositionSide:          enums.PosNet,
		CloseAfterFill:        true,
		CleanExistingPosition: true,
		PollInterval:          time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "pre-existing position") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want pre-existing position rejection", err)
	}
	if len(exec.submits) != 0 {
		t.Fatalf("submits=%d, want 0 for offsetting pre-existing positions: %+v", len(exec.submits), exec.submits)
	}
}

func TestAdapterOrderLifecycleUsesConfiguredPerpPositionReporter(t *testing.T) {
	exec := &accountPositionLifecycleExec{}
	reporter := &lifecycleAccountPositionReporter{exec: exec}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	spec := ConfigurePerpPositionReporter(OrderLifecycleSpec{
		Label:          "account-backed position evidence",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: time.Second,
	}, reporter)

	result, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if result == nil || !result.FilledQty.Equal(decimal.RequireFromString("0.01")) || !result.ClosedQty.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("result=%+v, want account-backed fill and close evidence", result)
	}
	if got := reporter.nonZeroObservations.Load(); got == 0 {
		t.Fatal("configured position reporter never observed the lifecycle-created position")
	}
	if got := exec.executionPositionReportCalls.Load(); got != 0 {
		t.Fatalf("execution position report calls=%d, want configured account reporter only", got)
	}
}

func TestAdapterOrderLifecycleDefaultsToExecutionPositionReports(t *testing.T) {
	exec := &defaultPositionReporterExec{}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "default execution position evidence",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
	})
	if err == nil || !strings.Contains(err.Error(), "pre-existing position") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want default execution-report preflight rejection", err)
	}
	if got := exec.calls.Load(); got == 0 {
		t.Fatal("default lifecycle did not call Execution.GeneratePositionReports")
	}
}

func TestAdapterOrderLifecycleDoesNotRetryAmbiguousClose(t *testing.T) {
	exec := &closeFailureCleanupExec{}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "close failure cleanup",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 20 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "forced close failure") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want close failure", err)
	}
	if exec.existing.IsZero() || exec.reduceOnlyAttempts != 1 || exec.cancelAllCalls != 0 {
		t.Fatalf("ambiguous close must not retry: existing=%s reduceAttempts=%d cancelAll=%d", exec.existing, exec.reduceOnlyAttempts, exec.cancelAllCalls)
	}
}

func TestAdapterOrderLifecycleRetriesCloseAfterAuthoritativeTerminalZero(t *testing.T) {
	exec := newAuthoritativeTerminalZeroCloseExec(false)
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err == nil || !strings.Contains(err.Error(), "close response lost") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want original ambiguous close error", err)
	}
	exec.assertAuthoritativeZeroRetried(t)
}

func TestRuntimeOrderLifecycleRetriesCloseAfterAuthoritativeTerminalZero(t *testing.T) {
	exec := newAuthoritativeTerminalZeroCloseExec(true)
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-authoritative-terminal-zero-close",
		btruntime.WithAccountID("TEST:unified"),
	)
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp

	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
	if err == nil || !strings.Contains(err.Error(), "close response lost") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want original ambiguous close error", err)
	}
	exec.assertAuthoritativeZeroRetried(t)
}

func TestAdapterOrderLifecycleEmergencyCleanupRefusesExposureAboveLimit(t *testing.T) {
	exec := &oversizedCloseFailureCleanupExec{}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "oversized emergency cleanup",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "cleanup position limit") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want cleanup position limit rejection", err)
	}
	if exec.reduceOnlyAttempts != 1 {
		t.Fatalf("reduce-only attempts=%d, want only the failed lifecycle close and no oversized cleanup submit", exec.reduceOnlyAttempts)
	}
}

func TestAdapterOrderLifecycleEmergencyCleanupHonorsExplicitLowerLimit(t *testing.T) {
	exec := &oversizedCloseFailureCleanupExec{exposureMultiplier: decimal.RequireFromString("0.8")}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:                "explicit emergency cleanup limit",
		AccountID:            "TEST:unified",
		InstrumentID:         id,
		Quantity:             decimal.RequireFromString("0.01"),
		CleanupPositionLimit: decimal.RequireFromString("0.005"),
		RestingPrice:         decimal.RequireFromString("49000"),
		FillPrice:            decimal.RequireFromString("51000"),
		ClosePrice:           decimal.RequireFromString("50000"),
		PositionSide:         enums.PosNet,
		CloseAfterFill:       true,
		PollInterval:         time.Millisecond,
		CleanupTimeout:       time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "cleanup position limit") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want explicit cleanup position limit rejection", err)
	}
	if exec.reduceOnlyAttempts != 1 {
		t.Fatalf("reduce-only attempts=%d, want only the failed lifecycle close", exec.reduceOnlyAttempts)
	}
}

func TestAdapterOrderLifecycleEmergencyCleanupDoesNotExceedObservedFill(t *testing.T) {
	exec := &oversizedCloseFailureCleanupExec{exposureMultiplier: decimal.RequireFromString("1.5")}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:                "observed fill cleanup limit",
		AccountID:            "TEST:unified",
		InstrumentID:         id,
		Quantity:             decimal.RequireFromString("0.01"),
		CleanupPositionLimit: decimal.RequireFromString("0.02"),
		RestingPrice:         decimal.RequireFromString("49000"),
		FillPrice:            decimal.RequireFromString("51000"),
		ClosePrice:           decimal.RequireFromString("50000"),
		PositionSide:         enums.PosNet,
		CloseAfterFill:       true,
		PollInterval:         time.Millisecond,
		CleanupTimeout:       time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "cleanup position limit") || !strings.Contains(err.Error(), "observed_filled_qty=0.01") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want observed fill cleanup limit rejection", err)
	}
	if exec.reduceOnlyAttempts != 1 {
		t.Fatalf("reduce-only attempts=%d, want only the failed lifecycle close", exec.reduceOnlyAttempts)
	}
}

func TestAdapterOrderLifecycleEmergencyCleanupRefusesHedgedExposure(t *testing.T) {
	exec := &hedgedCloseFailureCleanupExec{}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "hedged emergency cleanup",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want ambiguous hedged cleanup rejection", err)
	}
	if exec.reduceOnlyAttempts != 1 {
		t.Fatalf("reduce-only attempts=%d, want only the failed lifecycle close", exec.reduceOnlyAttempts)
	}
}

func TestAdapterOrderLifecycleEmergencyCleanupRefusesShortExposure(t *testing.T) {
	exec := &shortCloseFailureCleanupExec{}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "short emergency cleanup",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "not lifecycle-created") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want non-lifecycle short cleanup rejection", err)
	}
	if exec.reduceOnlyAttempts != 1 {
		t.Fatalf("reduce-only attempts=%d, want only the failed lifecycle close", exec.reduceOnlyAttempts)
	}
}

func TestAdapterOrderLifecycleDoesNotCleanupPositionWhenFillResultIsAmbiguous(t *testing.T) {
	exec := &ambiguousFillCleanupExec{}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunAdapterOrderLifecycle(context.Background(), exec, OrderLifecycleSpec{
		Label:          "ambiguous adapter fill",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 20 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "ambiguous fill result") || !strings.Contains(err.Error(), "position cleanup not armed") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want original ambiguous-fill error plus unarmed cleanup error", err)
	}
	if got := exec.reduceOnlyAttempts.Load(); got != 0 {
		t.Fatalf("reduce-only attempts=%d, want 0 without an observed lifecycle fill", got)
	}
}

func TestAdapterOrderLifecycleRecoversAmbiguousPartialOpeningForPerpCleanup(t *testing.T) {
	exec := newAmbiguousFilledOpeningLifecycleExec(decimal.RequireFromString("0.004"), decimal.RequireFromString("0.004"))
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp
	spec.CloseAfterFill = false
	spec.CleanupTimeout = 100 * time.Millisecond

	result, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if result == nil || !result.FilledQty.Equal(decimal.RequireFromString("0.004")) {
		t.Fatalf("result=%+v, want recovered opening fill 0.004", result)
	}
	exec.assertPerpCleanup(t, decimal.RequireFromString("0.004"))
}

func TestRuntimeOrderLifecycleRecoversAmbiguousPartialOpeningForPerpCleanup(t *testing.T) {
	exec := newAmbiguousFilledOpeningLifecycleExec(decimal.RequireFromString("0.004"), decimal.RequireFromString("0.004"))
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-ambiguous-partial-opening",
		btruntime.WithAccountID("TEST:unified"),
	)
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()
	readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
	if err := WaitForActive(readyCtx, node); err != nil {
		readyCancel()
		t.Fatalf("runtime node readiness: %v", err)
	}
	readyCancel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp
	spec.CloseAfterFill = false
	spec.CleanupTimeout = 100 * time.Millisecond

	result, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
	if err != nil {
		t.Fatalf("RunRuntimeOrderLifecycle: %v", err)
	}
	if result == nil || !result.FilledQty.Equal(decimal.RequireFromString("0.004")) {
		t.Fatalf("result=%+v, want recovered runtime opening fill 0.004", result)
	}
	exec.assertPerpCleanup(t, decimal.RequireFromString("0.004"))
}

func TestAdapterSpotOrderLifecycleRecoversAmbiguousPartialOpeningWithScaledGuard(t *testing.T) {
	exec := newAmbiguousFilledOpeningLifecycleExec(decimal.RequireFromString("0.004"), decimal.RequireFromString("0.003"))
	reporter := &sequenceAccountStateSource{states: []model.AccountState{
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.004", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.004", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.001", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.001", "0"),
	}}
	spec := terminalFilledLifecycleSpec()
	spec.CloseQuantity = decimal.RequireFromString("0.009")
	spec = ConfigureSpotBalanceGuard(spec, reporter, "BTC", decimal.RequireFromString("0.001"), decimal.RequireFromString("0.001"), decimal.NewFromInt(100), decimal.RequireFromString("0.001"))

	result, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if result == nil || !result.FilledQty.Equal(decimal.RequireFromString("0.004")) || !result.ClosedQty.Equal(decimal.RequireFromString("0.003")) {
		t.Fatalf("result=%+v, want recovered fill 0.004 and scaled close 0.003", result)
	}
	if exec.sellAttempts != 1 {
		t.Fatalf("sell attempts=%d, want one guarded close", exec.sellAttempts)
	}
}

func TestAdapterSpotOrderLifecyclePreservesBaselineAndAcceptsFeeDust(t *testing.T) {
	reporter := &sequenceAccountStateSource{states: []model.AccountState{
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "11", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "11", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.005", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.005", "0"),
	}}
	exec := &recordingLifecycleExec{}
	spec := guardedSpotLifecycleSpec(reporter)

	result, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if result == nil || !result.FilledQty.Equal(decimal.NewFromInt(1)) || !result.ClosedQty.Equal(decimal.RequireFromString("0.995")) {
		t.Fatalf("result=%+v, want buy=1 close=0.995", result)
	}
	if len(exec.submits) != 3 || !exec.submits[2].Quantity.Equal(decimal.RequireFromString("0.995")) {
		t.Fatalf("submits=%+v, want exactly resting/buy1/sell0.995", exec.submits)
	}
}

func TestAdapterSpotOrderLifecycleTreatsOmittedAuthoritativeZeroBalanceAsZero(t *testing.T) {
	reporter := &sequenceAccountStateSource{states: []model.AccountState{
		spotAccountState("TEST:unified", "TEST"),
		spotAccountState("TEST:unified", "TEST"),
		spotAccountState("TEST:unified", "TEST"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "1", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "1", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "0.005", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "0.005", "0"),
	}}
	exec := &recordingLifecycleExec{}

	result, err := RunAdapterOrderLifecycle(context.Background(), exec, guardedSpotLifecycleSpec(reporter))
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if result == nil || !result.FilledQty.Equal(decimal.NewFromInt(1)) || !result.ClosedQty.Equal(decimal.RequireFromString("0.995")) {
		t.Fatalf("result=%+v, want buy=1 close=0.995 from an omitted zero baseline", result)
	}
}

func TestAdapterSpotOrderLifecycleRestingCancelRaceBlocksFill(t *testing.T) {
	reporter := &sequenceAccountStateSource{states: []model.AccountState{
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "11", "0"),
	}}
	exec := &recordingLifecycleExec{}
	spec := guardedSpotLifecycleSpec(reporter)
	spec.CleanupTimeout = 20 * time.Millisecond

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err == nil || !strings.Contains(err.Error(), "resting cancel") || !strings.Contains(err.Error(), "spot cleanup blocked") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want cancel-race and cleanup-blocked errors", err)
	}
	if len(exec.submits) != 1 {
		t.Fatalf("submits=%d, want resting only: %+v", len(exec.submits), exec.submits)
	}
}

func TestAdapterSpotOrderLifecycleUnsafeFillBalanceNeverSells(t *testing.T) {
	tests := []struct {
		name     string
		total    string
		borrowed string
		want     string
	}{
		{name: "negative delta", total: "9.9", borrowed: "0", want: "negative"},
		{name: "above observed fill", total: "11.1", borrowed: "0", want: "observed fill"},
		{name: "borrow increased", total: "11", borrowed: "0.1", want: "borrowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reporter := &sequenceAccountStateSource{states: []model.AccountState{
				spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
				spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
				spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
				spotBalanceState("TEST:unified", "TEST", "BTC", tt.total, tt.borrowed),
			}}
			exec := &recordingLifecycleExec{}
			spec := guardedSpotLifecycleSpec(reporter)
			spec.CleanupTimeout = 20 * time.Millisecond

			_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "spot cleanup blocked") {
				t.Fatalf("RunAdapterOrderLifecycle err=%v, want %q and cleanup blocked", err, tt.want)
			}
			if len(exec.submits) != 2 {
				t.Fatalf("submits=%d, want resting and buy only: %+v", len(exec.submits), exec.submits)
			}
		})
	}
}

func TestAdapterSpotOrderLifecycleAccountMismatchBlocksAllSubmits(t *testing.T) {
	reporter := &sequenceAccountStateSource{states: []model.AccountState{
		spotBalanceState("TEST:other", "TEST", "BTC", "10", "0"),
	}}
	exec := &recordingLifecycleExec{}

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, guardedSpotLifecycleSpec(reporter))
	if err == nil || !strings.Contains(err.Error(), "account mismatch") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want account mismatch", err)
	}
	if len(exec.submits) != 0 {
		t.Fatalf("submits=%d, want zero: %+v", len(exec.submits), exec.submits)
	}
}

func TestAdapterSpotOrderLifecycleRejectsMalformedAuthoritativeSnapshot(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.AccountState)
		want   string
	}{
		{name: "invalid account model", mutate: func(state *model.AccountState) { state.Type = model.AccountTypeUnknown }, want: "invalid type"},
		{name: "missing event id", mutate: func(state *model.AccountState) { state.EventID = "" }, want: "event id"},
		{name: "missing event timestamp", mutate: func(state *model.AccountState) { state.TsEvent = time.Time{} }, want: "event timestamp"},
		{name: "missing init timestamp", mutate: func(state *model.AccountState) { state.TsInit = time.Time{} }, want: "init timestamp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0")
			tt.mutate(&state)
			reporter := &sequenceAccountStateSource{states: []model.AccountState{state}}
			exec := &recordingLifecycleExec{}

			_, err := RunAdapterOrderLifecycle(context.Background(), exec, guardedSpotLifecycleSpec(reporter))
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.want) {
				t.Fatalf("RunAdapterOrderLifecycle err=%v, want malformed snapshot %q failure", err, tt.want)
			}
			if len(exec.submits) != 0 {
				t.Fatalf("submits=%d, want zero before malformed snapshot rejection: %+v", len(exec.submits), exec.submits)
			}
		})
	}
}

func TestAdapterSpotOrderLifecycleAmbiguousCloseNeverSubmitsSecondSell(t *testing.T) {
	t.Run("authoritative balance eventually returns to baseline", func(t *testing.T) {
		closeErr := errors.New("close outcome unknown")
		reporter := &sequenceAccountStateSource{states: []model.AccountState{
			spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
			spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
			spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
			spotBalanceState("TEST:unified", "TEST", "BTC", "11", "0"),
			spotBalanceState("TEST:unified", "TEST", "BTC", "11", "0"),
			spotBalanceState("TEST:unified", "TEST", "BTC", "11", "0"),
			spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
			spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		}}
		exec := &ambiguousSpotCloseExec{err: closeErr}
		spec := guardedSpotLifecycleSpec(reporter)
		spec.CleanupTimeout = 100 * time.Millisecond

		_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
		if !errors.Is(err, closeErr) || strings.Contains(err.Error(), "spot cleanup blocked") {
			t.Fatalf("RunAdapterOrderLifecycle err=%v, want original close error with proven cleanup", err)
		}
		if len(exec.submits) != 3 {
			t.Fatalf("submits=%d, want one close attempt and no recovery sell: %+v", len(exec.submits), exec.submits)
		}
	})

	t.Run("persistent ambiguity joins cleanup blocker", func(t *testing.T) {
		closeErr := errors.New("persistent close outcome unknown")
		reporter := &sequenceAccountStateSource{states: []model.AccountState{
			spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
			spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
			spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
			spotBalanceState("TEST:unified", "TEST", "BTC", "11", "0"),
			spotBalanceState("TEST:unified", "TEST", "BTC", "11", "0"),
		}}
		exec := &ambiguousSpotCloseExec{err: closeErr}
		spec := guardedSpotLifecycleSpec(reporter)
		spec.CleanupTimeout = 20 * time.Millisecond

		_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
		if !errors.Is(err, closeErr) || !strings.Contains(err.Error(), "spot cleanup blocked") {
			t.Fatalf("RunAdapterOrderLifecycle err=%v, want errors.Join(original, cleanup blocker)", err)
		}
		if len(exec.submits) != 3 {
			t.Fatalf("submits=%d, want one close attempt and no recovery sell: %+v", len(exec.submits), exec.submits)
		}
	})
}

func TestAdapterSpotOrderLifecycleRetriesOneCleanupAfterAuthoritativeZeroClose(t *testing.T) {
	for _, mode := range []string{"definitive_reject", "terminal_zero"} {
		t.Run(mode, func(t *testing.T) {
			exec := newSpotCloseFailureCleanupExec(mode)
			reporter := &spotCloseFailureBalanceReporter{exec: exec}
			spec := guardedSpotLifecycleSpec(reporter)

			_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
			if err == nil || !strings.Contains(err.Error(), "close") || strings.Contains(err.Error(), "spot cleanup blocked") {
				t.Fatalf("RunAdapterOrderLifecycle err=%v, want original close failure with successful cleanup", err)
			}
			exec.assertSingleSpotCleanup(t)
		})
	}
}

func TestRuntimeSpotOrderLifecycleRetriesOneCleanupAfterAuthoritativeZeroClose(t *testing.T) {
	for _, mode := range []string{"definitive_reject", "terminal_zero"} {
		t.Run(mode, func(t *testing.T) {
			exec := newSpotCloseFailureCleanupExec(mode)
			reporter := &spotCloseFailureBalanceReporter{exec: exec}
			node := btruntime.NewNode(
				btruntime.Clients{Execution: exec},
				clock.NewRealClock(),
				"runtime-spot-zero-close-"+mode,
				btruntime.WithAccountID("TEST:unified"),
			)
			runCtx, stop := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				node.Run(runCtx)
				close(done)
			}()
			defer func() {
				stop()
				<-done
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			spec := guardedSpotLifecycleSpec(reporter)

			_, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
			if err == nil || !strings.Contains(err.Error(), "close") || strings.Contains(err.Error(), "spot cleanup blocked") {
				t.Fatalf("RunRuntimeOrderLifecycle err=%v, want original runtime close failure with successful cleanup", err)
			}
			exec.assertSingleSpotCleanup(t)
		})
	}
}

func TestAdapterSpotOrderLifecycleSellableResidualFailsClosed(t *testing.T) {
	reporter := &sequenceAccountStateSource{states: []model.AccountState{
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "11", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "11", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.1", "0"),
	}}
	exec := &recordingLifecycleExec{}
	spec := guardedSpotLifecycleSpecWithRules(reporter, "0.9", "0.01", "0.05", "1", "0.1")
	spec.CleanupTimeout = 20 * time.Millisecond

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err == nil || !strings.Contains(err.Error(), "sellable") || !strings.Contains(err.Error(), "spot cleanup blocked") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want sellable residual blocker", err)
	}
	if len(exec.submits) != 3 {
		t.Fatalf("submits=%d, want no recovery sell: %+v", len(exec.submits), exec.submits)
	}
}

func TestCancelLifecycleOpenOrdersIgnoresUnrelatedAccountOrders(t *testing.T) {
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindSpot}
	exec := &cleanupOpenOrdersExec{open: []model.Order{
		{Request: model.OrderRequest{AccountID: "", ClientID: "manual-order", InstrumentID: id}, VenueOrderID: "manual", Status: enums.StatusNew},
		{Request: model.OrderRequest{AccountID: "TEST:other", ClientID: "strategy-order", InstrumentID: id}, VenueOrderID: "strategy", Status: enums.StatusNew},
		{Request: model.OrderRequest{AccountID: "TEST:unified", ClientID: "btac-close-1", InstrumentID: id}, VenueOrderID: "btac", Status: enums.StatusNew},
	}}
	spec := OrderLifecycleSpec{Label: "cleanup filter", AccountID: "TEST:unified", InstrumentID: id}
	tracker := newLifecycleOrderTracker()
	tracked := &trackedLifecycleOrder{
		kind:         "close",
		clientID:     "btac-close-1",
		venueOrderID: "btac",
		request:      model.OrderRequest{AccountID: "TEST:unified", ClientID: "btac-close-1", InstrumentID: id},
	}
	tracker.orders[tracked.clientID] = tracked

	if err := cancelLifecycleOpenOrders(context.Background(), exec, spec, tracker); err != nil {
		t.Fatalf("cancelLifecycleOpenOrders: %v", err)
	}
	if len(exec.canceled) != 1 || exec.canceled[0] != "btac" {
		t.Fatalf("canceled=%v, want [btac]", exec.canceled)
	}
}

func TestAdapterOrderLifecycleCleansAmbiguousSubmitByExactClientID(t *testing.T) {
	exec := newPlacedThenErroredLifecycleExec()
	spec := terminalFilledLifecycleSpec()
	spec.CleanupTimeout = 100 * time.Millisecond

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err == nil || !strings.Contains(err.Error(), "submit response lost after placement") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want ambiguous submit error", err)
	}
	exec.assertExactCleanup(t)
}

func TestRecoverAmbiguousIOCRejectsPersistentNonTerminalStatusDespiteAbsentOpenOrders(t *testing.T) {
	exec := &ambiguousIOCEvidenceExec{mode: "persistent_nonterminal"}
	spec, tracker, tracked, req := ambiguousIOCTestFixture()
	spec.CleanupTimeout = 12 * time.Millisecond

	order, filledQty, err := recoverAmbiguousIOC(context.Background(), exec, spec, tracker, tracked, req)
	if err == nil || order != nil || !filledQty.IsZero() {
		t.Fatalf("recoverAmbiguousIOC order=%+v filled=%s err=%v, want unresolved nonterminal evidence", order, filledQty, err)
	}
	if len(exec.canceled) == 0 || exec.canceled[0] != exec.venueOrderID {
		t.Fatalf("canceled=%v, want exact cancel of known venue id %q", exec.canceled, exec.venueOrderID)
	}
}

func TestExactOrderQueryIDsPreferVenueOrderID(t *testing.T) {
	tests := []struct {
		name                  string
		clientID, venueID     string
		wantClient, wantVenue string
	}{
		{name: "client before venue acknowledgement", clientID: "client-1", wantClient: "client-1"},
		{name: "venue after acknowledgement", clientID: "client-1", venueID: "venue-1", wantVenue: "venue-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientID, venueID := exactOrderQueryIDs(tt.clientID, tt.venueID)
			if clientID != tt.wantClient || venueID != tt.wantVenue {
				t.Fatalf("exactOrderQueryIDs(%q, %q)=(%q, %q), want (%q, %q)", tt.clientID, tt.venueID, clientID, venueID, tt.wantClient, tt.wantVenue)
			}
		})
	}
}

func TestWaitForFilledQtySwitchesToVenueIDLearnedFromStatus(t *testing.T) {
	exec := &venueLearningFillExec{}
	spec := terminalFilledLifecycleSpec()
	tracker := newLifecycleOrderTracker()
	tracked := tracker.add("fill")
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    spec.AccountID,
			ClientID:     tracked.clientID,
			InstrumentID: spec.InstrumentID,
			Quantity:     spec.Quantity,
		},
		Status: enums.StatusNew,
	}
	tracked.request = order.Request
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	filledQty, err := waitForFilledQty(ctx, exec, spec, tracker, tracked, order)
	if err != nil {
		t.Fatalf("waitForFilledQty: %v", err)
	}
	if !filledQty.Equal(spec.Quantity) || !exec.statusUsedClientOnly || !exec.fillUsedVenueOnly {
		t.Fatalf("filled=%s statusClientOnly=%t fillVenueOnly=%t, want learned venue query transition", filledQty, exec.statusUsedClientOnly, exec.fillUsedVenueOnly)
	}
}

func TestWaitForFilledQtyRequiresDefinitiveTerminalAfterUnknownPartial(t *testing.T) {
	exec := &unknownThenCanceledFillExec{}
	spec := terminalFilledLifecycleSpec()
	tracker := newLifecycleOrderTracker()
	tracked := tracker.add("fill")
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    spec.AccountID,
			ClientID:     tracked.clientID,
			InstrumentID: spec.InstrumentID,
			Quantity:     spec.Quantity,
		},
		VenueOrderID: "unknown-partial",
		Status:       enums.StatusUnknown,
		FilledQty:    decimal.RequireFromString("0.004"),
	}
	tracked.request = order.Request
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	filledQty, err := waitForFilledQty(ctx, exec, spec, tracker, tracked, order)
	if err != nil {
		t.Fatalf("waitForFilledQty: %v", err)
	}
	if !filledQty.Equal(decimal.RequireFromString("0.004")) || exec.statusCalls == 0 {
		t.Fatalf("filled=%s statusCalls=%d, want query through Unknown to definitive CANCELED", filledQty, exec.statusCalls)
	}
}

func TestWaitForRuntimeFilledQtyRequiresDefinitiveTerminalAfterUnknownPartial(t *testing.T) {
	exec := &recordingLifecycleExec{}
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-unknown-partial",
		btruntime.WithAccountID("TEST:unified"),
	)
	spec := terminalFilledLifecycleSpec()
	tracker := newLifecycleOrderTracker()
	tracked := tracker.add("fill")
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    spec.AccountID,
			ClientID:     tracked.clientID,
			InstrumentID: spec.InstrumentID,
			Quantity:     spec.Quantity,
		},
		VenueOrderID: "runtime-unknown-partial",
		Status:       enums.StatusUnknown,
		FilledQty:    decimal.RequireFromString("0.004"),
	}
	tracked.request = order.Request
	node.Cache.UpsertOrder(order)
	updated := make(chan struct{})
	go func() {
		time.Sleep(5 * time.Millisecond)
		order.Status = enums.StatusCanceled
		node.Cache.UpsertOrder(order)
		close(updated)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got, filledQty, err := waitForRuntimeFilledQty(ctx, node, spec, tracker, tracked, tracked.clientID, spec.Quantity)
	if err != nil {
		t.Fatalf("waitForRuntimeFilledQty: %v", err)
	}
	<-updated
	if got.Status != enums.StatusCanceled || !filledQty.Equal(decimal.RequireFromString("0.004")) {
		t.Fatalf("order=%+v filled=%s, want definitive CANCELED partial", got, filledQty)
	}
}

func TestWaitForRuntimeFilledQtyAllowsFillAfterTerminalZeroOrderEvent(t *testing.T) {
	node := btruntime.NewNode(
		btruntime.Clients{Execution: &recordingLifecycleExec{}},
		clock.NewRealClock(),
		"runtime-terminal-before-fill",
		btruntime.WithAccountID("TEST:unified"),
	)
	spec := terminalFilledLifecycleSpec()
	spec.PollInterval = 10 * time.Millisecond
	tracker := newLifecycleOrderTracker()
	tracked := tracker.add("fill")
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    spec.AccountID,
			ClientID:     tracked.clientID,
			InstrumentID: spec.InstrumentID,
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifIOC,
			Quantity:     spec.Quantity,
			Price:        spec.FillPrice,
			PositionSide: enums.PosNet,
		},
		VenueOrderID: "terminal-before-fill",
		Status:       enums.StatusFilled,
	}
	tracked.request = order.Request
	node.Cache.UpsertOrder(order)

	type result struct {
		order     model.Order
		filledQty decimal.Decimal
		err       error
	}
	resultCh := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		got, filledQty, err := waitForRuntimeFilledQty(ctx, node, spec, tracker, tracked, tracked.clientID, spec.Quantity)
		resultCh <- result{order: got, filledQty: filledQty, err: err}
	}()

	select {
	case got := <-resultCh:
		t.Fatalf("wait returned before a late fill could arrive: %+v", got)
	case <-time.After(15 * time.Millisecond):
	}
	order.FilledQty = spec.Quantity
	node.Cache.UpsertOrder(order)

	got := <-resultCh
	if got.err != nil || got.order.Status != enums.StatusFilled || !got.filledQty.Equal(spec.Quantity) {
		t.Fatalf("wait result=%+v, want terminal order with late fill quantity %s", got, spec.Quantity)
	}
}

func TestRecoverAmbiguousIOCRequiresKnownVenueExactCancelBeforeStableAbsence(t *testing.T) {
	exec := &ambiguousIOCEvidenceExec{mode: "cancel_then_absent"}
	spec, tracker, tracked, req := ambiguousIOCTestFixture()
	spec.CleanupTimeout = 50 * time.Millisecond

	order, filledQty, err := recoverAmbiguousIOC(context.Background(), exec, spec, tracker, tracked, req)
	if err != nil {
		t.Fatalf("recoverAmbiguousIOC: %v", err)
	}
	if order == nil || !filledQty.Equal(decimal.RequireFromString("0.004")) {
		t.Fatalf("order=%+v filled=%s, want recovered partial fill 0.004", order, filledQty)
	}
	if len(exec.canceled) == 0 || exec.canceled[0] != exec.venueOrderID {
		t.Fatalf("canceled=%v, want exact cancel before absence recovery", exec.canceled)
	}
	if exec.statusCalls < 3 || exec.openCalls < 3 {
		t.Fatalf("statusCalls=%d openCalls=%d, want cancel followed by stable status/open absence", exec.statusCalls, exec.openCalls)
	}
}

func TestRecoverAmbiguousIOCPreservesObservedFillWhenLaterFillQueryFails(t *testing.T) {
	exec := &ambiguousIOCEvidenceExec{mode: "fill_then_terminal"}
	spec, tracker, tracked, req := ambiguousIOCTestFixture()
	spec.CleanupTimeout = 50 * time.Millisecond

	order, filledQty, err := recoverAmbiguousIOC(context.Background(), exec, spec, tracker, tracked, req)
	if err != nil {
		t.Fatalf("recoverAmbiguousIOC: %v", err)
	}
	if order == nil || order.Status != enums.StatusCanceled || !filledQty.Equal(decimal.RequireFromString("0.004")) {
		t.Fatalf("order=%+v filled=%s, want terminal canceled with monotonic fill 0.004", order, filledQty)
	}
}

func TestRecoverAmbiguousIOCDoesNotTreatUnknownAsTerminal(t *testing.T) {
	exec := &ambiguousIOCEvidenceExec{mode: "unknown_then_terminal"}
	spec, tracker, tracked, req := ambiguousIOCTestFixture()
	spec.CleanupTimeout = 50 * time.Millisecond

	order, filledQty, err := recoverAmbiguousIOC(context.Background(), exec, spec, tracker, tracked, req)
	if err != nil {
		t.Fatalf("recoverAmbiguousIOC: %v", err)
	}
	if order == nil || order.Status != enums.StatusCanceled || !filledQty.Equal(decimal.RequireFromString("0.004")) {
		t.Fatalf("order=%+v filled=%s, want post-cancel definitive terminal fill", order, filledQty)
	}
	if len(exec.canceled) == 0 || exec.canceled[0] != exec.venueOrderID {
		t.Fatalf("canceled=%v, want Unknown status to trigger exact cancel of %q", exec.canceled, exec.venueOrderID)
	}
}

func TestRecoverAmbiguousIOCRejectsUnrelatedFillBeforeTracking(t *testing.T) {
	exec := &ambiguousIOCEvidenceExec{mode: "unrelated_fill"}
	spec, tracker, tracked, req := ambiguousIOCTestFixture()
	spec.CleanupTimeout = 20 * time.Millisecond

	order, filledQty, err := recoverAmbiguousIOC(context.Background(), exec, spec, tracker, tracked, req)
	if err == nil || order != nil || !filledQty.IsZero() || !strings.Contains(err.Error(), "fill identity") {
		t.Fatalf("recoverAmbiguousIOC order=%+v filled=%s err=%v, want exact fill identity rejection", order, filledQty, err)
	}
	if !tracked.filledQty.IsZero() {
		t.Fatalf("tracked filled quantity=%s, unrelated fill must not arm cleanup", tracked.filledQty)
	}
}

func TestWaitForFilledQtyRejectsUnrelatedFill(t *testing.T) {
	exec := &unrelatedFillReportExec{}
	spec := terminalFilledLifecycleSpec()
	tracker := newLifecycleOrderTracker()
	tracked := tracker.add("fill")
	order := model.Order{Request: model.OrderRequest{
		AccountID: spec.AccountID, ClientID: tracked.clientID, InstrumentID: spec.InstrumentID,
		Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifIOC, Quantity: spec.Quantity, Price: spec.FillPrice,
	}, VenueOrderID: "expected-venue", Status: enums.StatusNew}
	tracked.request = order.Request
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	filledQty, err := waitForFilledQty(ctx, exec, spec, tracker, tracked, order)
	if err == nil || !filledQty.IsZero() || !strings.Contains(err.Error(), "fill identity") {
		t.Fatalf("waitForFilledQty filled=%s err=%v, want exact fill identity rejection", filledQty, err)
	}
	if !tracked.filledQty.IsZero() {
		t.Fatalf("tracked filled quantity=%s, unrelated fill must not be accumulated", tracked.filledQty)
	}
}

func TestSumExactLifecycleFillsDeduplicatesStableReportIdentity(t *testing.T) {
	spec := terminalFilledLifecycleSpec()
	tracker := newLifecycleOrderTracker()
	tracked := tracker.add("fill")
	tracked.request = model.OrderRequest{
		AccountID: spec.AccountID, ClientID: tracked.clientID, InstrumentID: spec.InstrumentID,
		Side: enums.SideBuy, Quantity: spec.Quantity, Price: spec.FillPrice,
	}
	tracked.venueOrderID = "venue-1"
	report := model.FillReport{
		ReportID: "report-1", AccountID: spec.AccountID,
		Fill: model.Fill{
			AccountID: spec.AccountID, InstrumentID: spec.InstrumentID,
			ClientID: tracked.clientID, VenueOrderID: tracked.venueOrderID, TradeID: "trade-1",
			Quantity: decimal.RequireFromString("0.004"), Price: spec.FillPrice,
		},
	}

	total, err := sumExactLifecycleFills(spec, "duplicate_fill_report", tracked, []model.FillReport{report, report})
	if err != nil {
		t.Fatalf("sumExactLifecycleFills: %v", err)
	}
	if !total.Equal(decimal.RequireFromString("0.004")) || !tracked.filledQty.Equal(total) {
		t.Fatalf("total=%s tracked=%s, want duplicate counted once", total, tracked.filledQty)
	}
}

func TestSumExactLifecycleFillsPrefersTradeIdentityAcrossReportIDs(t *testing.T) {
	spec := terminalFilledLifecycleSpec()
	tracker := newLifecycleOrderTracker()
	tracked := tracker.add("fill")
	tracked.request = model.OrderRequest{
		AccountID: spec.AccountID, ClientID: tracked.clientID, InstrumentID: spec.InstrumentID,
		Side: enums.SideBuy, Quantity: spec.Quantity, Price: spec.FillPrice,
	}
	tracked.venueOrderID = "venue-1"
	first := model.FillReport{ReportID: "report-1", AccountID: spec.AccountID, Fill: model.Fill{
		AccountID: spec.AccountID, InstrumentID: spec.InstrumentID, Side: enums.SideBuy,
		ClientID: tracked.clientID, VenueOrderID: tracked.venueOrderID, TradeID: "trade-1",
		Quantity: decimal.RequireFromString("0.004"), Price: spec.FillPrice,
	}}
	second := first
	second.ReportID = "report-2"

	total, err := sumExactLifecycleFills(spec, "trade_identity", tracked, []model.FillReport{first, second})
	if err != nil {
		t.Fatalf("sumExactLifecycleFills: %v", err)
	}
	if !total.Equal(decimal.RequireFromString("0.004")) {
		t.Fatalf("total=%s, same trade with different report IDs must count once", total)
	}
}

func TestSumExactLifecycleFillsRejectsSideMismatchAndAggregateOverfill(t *testing.T) {
	spec := terminalFilledLifecycleSpec()
	newTracked := func() *trackedLifecycleOrder {
		tracker := newLifecycleOrderTracker()
		tracked := tracker.add("fill")
		tracked.request = model.OrderRequest{
			AccountID: spec.AccountID, ClientID: tracked.clientID, InstrumentID: spec.InstrumentID,
			Side: enums.SideBuy, Quantity: spec.Quantity, Price: spec.FillPrice,
		}
		tracked.venueOrderID = "venue-1"
		return tracked
	}
	makeReport := func(tracked *trackedLifecycleOrder, tradeID, qty string, side enums.OrderSide) model.FillReport {
		return model.FillReport{AccountID: spec.AccountID, Fill: model.Fill{
			AccountID: spec.AccountID, InstrumentID: spec.InstrumentID, Side: side,
			ClientID: tracked.clientID, VenueOrderID: tracked.venueOrderID, TradeID: tradeID,
			Quantity: decimal.RequireFromString(qty), Price: spec.FillPrice,
		}}
	}

	tracked := newTracked()
	if total, err := sumExactLifecycleFills(spec, "side_mismatch", tracked, []model.FillReport{makeReport(tracked, "trade-side", "0.004", enums.SideSell)}); err == nil || !total.IsZero() || !strings.Contains(err.Error(), "fill side") {
		t.Fatalf("side mismatch total=%s err=%v", total, err)
	}

	tracked = newTracked()
	reports := []model.FillReport{
		makeReport(tracked, "trade-1", "0.006", enums.SideBuy),
		makeReport(tracked, "trade-2", "0.006", enums.SideBuy),
	}
	if total, err := sumExactLifecycleFills(spec, "aggregate_overfill", tracked, reports); err == nil || !total.IsZero() || !strings.Contains(err.Error(), "aggregate fill quantity") {
		t.Fatalf("aggregate overfill total=%s err=%v", total, err)
	}
	if !tracked.filledQty.IsZero() {
		t.Fatalf("tracked fill=%s, overfill must not mutate tracker", tracked.filledQty)
	}
}

func TestSumExactLifecycleFillsRejectsMissingOrConflictingStableIdentity(t *testing.T) {
	spec := terminalFilledLifecycleSpec()
	newTracked := func() *trackedLifecycleOrder {
		tracker := newLifecycleOrderTracker()
		tracked := tracker.add("fill")
		tracked.request = model.OrderRequest{
			AccountID: spec.AccountID, ClientID: tracked.clientID, InstrumentID: spec.InstrumentID,
			Side: enums.SideBuy, Quantity: spec.Quantity, Price: spec.FillPrice,
		}
		tracked.venueOrderID = "venue-1"
		return tracked
	}
	fill := model.Fill{
		AccountID: spec.AccountID, InstrumentID: spec.InstrumentID,
		VenueOrderID: "venue-1", Quantity: decimal.RequireFromString("0.004"), Price: spec.FillPrice,
	}
	tracked := newTracked()
	fill.ClientID = tracked.clientID
	missing := model.FillReport{AccountID: spec.AccountID, Fill: fill}
	if total, err := sumExactLifecycleFills(spec, "missing_identity", tracked, []model.FillReport{missing, missing}); err == nil || !total.IsZero() || !strings.Contains(err.Error(), "stable ReportID or TradeID") {
		t.Fatalf("missing stable identity total=%s err=%v", total, err)
	}

	tracked = newTracked()
	fill.ClientID = tracked.clientID
	fill.TradeID = "trade-1"
	first := model.FillReport{AccountID: spec.AccountID, Fill: fill}
	second := first
	second.Fill.Quantity = decimal.RequireFromString("0.005")
	if total, err := sumExactLifecycleFills(spec, "conflicting_duplicate", tracked, []model.FillReport{first, second}); err == nil || !total.IsZero() || !strings.Contains(err.Error(), "conflicting duplicate") {
		t.Fatalf("conflicting duplicate total=%s err=%v", total, err)
	}
}

func TestEnsureTrackedOrderStatusReportRejectsOverfill(t *testing.T) {
	spec := terminalFilledLifecycleSpec()
	tracker := newLifecycleOrderTracker()
	tracked := tracker.add("fill")
	tracked.request = model.OrderRequest{
		AccountID: spec.AccountID, ClientID: tracked.clientID, InstrumentID: spec.InstrumentID,
		Side: enums.SideBuy, Quantity: spec.Quantity, Price: spec.FillPrice,
	}
	report := &model.OrderStatusReport{AccountID: spec.AccountID, Order: model.Order{
		Request: tracked.request, VenueOrderID: "venue-1", Status: enums.StatusFilled,
		FilledQty: spec.Quantity.Add(decimal.RequireFromString("0.001")),
	}}

	err := ensureTrackedOrderStatusReport(spec, "overfill", tracked, report)
	if err == nil || !strings.Contains(err.Error(), "exceeds order quantity") {
		t.Fatalf("ensureTrackedOrderStatusReport err=%v, want malformed overfill rejection", err)
	}
}

func TestCleanupCancelsExactOrderBeforeMalformedEvidenceFailure(t *testing.T) {
	for _, mode := range []string{"status_overfill", "aggregate_fill_overfill"} {
		t.Run(mode, func(t *testing.T) {
			spec := terminalFilledLifecycleSpec()
			tracker := newLifecycleOrderTracker()
			tracked := tracker.add("rest")
			tracked.request = model.OrderRequest{
				AccountID: spec.AccountID, ClientID: tracked.clientID, InstrumentID: spec.InstrumentID,
				Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTX,
				Quantity: spec.Quantity, Price: spec.RestingPrice, PositionSide: enums.PosNet,
			}
			tracked.venueOrderID = "malformed-known-venue"
			exec := &malformedCleanupEvidenceExec{mode: mode, tracked: tracked, spec: spec}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			err := waitForTrackedOrdersSettled(ctx, exec, spec, tracker, []*trackedLifecycleOrder{tracked}, true)
			if err == nil {
				t.Fatal("waitForTrackedOrdersSettled succeeded with malformed evidence")
			}
			if len(exec.canceled) != 1 || exec.canceled[0] != tracked.venueOrderID {
				t.Fatalf("canceled=%v, malformed evidence must follow exact cancel", exec.canceled)
			}
		})
	}
}

func TestCleanupBindsAndCancelsDiscoveredVenueIdentityBeforeMalformedStatusFailure(t *testing.T) {
	for _, mode := range []string{"status_overfill_discovered_venue", "status_overfill_discovered_venue_omitted_quantity"} {
		t.Run(mode, func(t *testing.T) {
			spec := terminalFilledLifecycleSpec()
			tracker := newLifecycleOrderTracker()
			tracked := tracker.add("rest")
			tracked.request = model.OrderRequest{
				AccountID: spec.AccountID, ClientID: tracked.clientID, InstrumentID: spec.InstrumentID,
				Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTX,
				Quantity: spec.Quantity, Price: spec.RestingPrice, PositionSide: enums.PosNet,
			}
			exec := &malformedCleanupEvidenceExec{mode: mode, tracked: tracked, spec: spec}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			err := waitForTrackedOrdersSettled(ctx, exec, spec, tracker, []*trackedLifecycleOrder{tracked}, true)
			if err == nil || !strings.Contains(err.Error(), "exceeds") || !strings.Contains(err.Error(), "quantity") {
				t.Fatalf("waitForTrackedOrdersSettled err=%v, want malformed overfill rejection", err)
			}
			const discoveredVenueOrderID = "malformed-discovered-venue"
			if tracked.venueOrderID != discoveredVenueOrderID {
				t.Fatalf("tracked venue order id=%q, want discovered %q", tracked.venueOrderID, discoveredVenueOrderID)
			}
			if len(exec.canceled) != 1 || exec.canceled[0] != discoveredVenueOrderID {
				t.Fatalf("canceled=%v, want exact discovered venue order %q canceled before rejection", exec.canceled, discoveredVenueOrderID)
			}
		})
	}
}

func TestAdapterOrderLifecycleRejectsUnrelatedExactStatus(t *testing.T) {
	exec := newUnrelatedStatusLifecycleExec()
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err == nil || !strings.Contains(err.Error(), "order identity") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want exact status identity rejection", err)
	}
	exec.assertNoSell(t)
}

func TestRuntimeOrderLifecycleRejectsUnrelatedExactStatus(t *testing.T) {
	exec := newUnrelatedStatusLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec}, clock.NewRealClock(), "runtime-unrelated-status",
		btruntime.WithAccountID("TEST:unified"),
	)
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp

	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
	if err == nil || !strings.Contains(err.Error(), "order identity") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want exact status identity rejection", err)
	}
	exec.assertNoSell(t)
}

func TestRuntimeOrderLifecycleCleansAmbiguousSubmitByExactClientID(t *testing.T) {
	exec := newPlacedThenErroredLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-ambiguous-submit-cleanup",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()
	spec := terminalFilledLifecycleSpec()
	spec.CleanupTimeout = 100 * time.Millisecond

	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
	if err == nil || !strings.Contains(err.Error(), "submit response lost after placement") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want ambiguous submit error", err)
	}
	exec.assertExactCleanup(t)
}

func TestAdapterOrderLifecycleTracksEveryAmbiguousSubmitClientID(t *testing.T) {
	for _, stage := range []string{"fill", "close"} {
		t.Run(stage, func(t *testing.T) {
			exec := newStagePlacedThenErroredLifecycleExec(stage)
			spec := terminalFilledLifecycleSpec()
			spec.CleanupTimeout = 50 * time.Millisecond

			_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
			if err == nil || !strings.Contains(err.Error(), stage+" submit response lost after placement") {
				t.Fatalf("RunAdapterOrderLifecycle err=%v, want %s ambiguous submit error", err, stage)
			}
			exec.assertExactCleanup(t)
		})
	}
}

func TestRuntimeOrderLifecycleTracksEveryAmbiguousSubmitClientID(t *testing.T) {
	for _, stage := range []string{"fill", "close"} {
		t.Run(stage, func(t *testing.T) {
			exec := newStagePlacedThenErroredLifecycleExec(stage)
			exec.emitFills = true
			node := btruntime.NewNode(
				btruntime.Clients{Execution: exec},
				clock.NewRealClock(),
				"runtime-ambiguous-"+stage,
				btruntime.WithAccountID("TEST:unified"),
			)
			runCtx, stop := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				node.Run(runCtx)
				close(done)
			}()
			defer func() {
				stop()
				<-done
			}()
			readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
			if err := WaitForActive(readyCtx, node); err != nil {
				readyCancel()
				t.Fatalf("runtime node readiness: %v", err)
			}
			readyCancel()
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			spec := terminalFilledLifecycleSpec()
			spec.CleanupTimeout = 50 * time.Millisecond

			_, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
			if err == nil || !strings.Contains(err.Error(), stage+" submit response lost after placement") {
				t.Fatalf("RunRuntimeOrderLifecycle err=%v, want %s ambiguous submit error", err, stage)
			}
			exec.assertExactCleanup(t)
		})
	}
}

func TestAdapterOrderLifecycleDoesNotDisarmRestingCleanupBeforeTerminalEvidence(t *testing.T) {
	exec := newCancelAcknowledgedButOpenLifecycleExec()
	spec := terminalFilledLifecycleSpec()
	spec.CleanupTimeout = 30 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := RunAdapterOrderLifecycle(ctx, exec, spec)
	if err == nil || !strings.Contains(err.Error(), "resting") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want resting terminal-evidence failure", err)
	}
	if len(exec.submits) != 1 {
		t.Fatalf("submits=%d, want no opening order before resting terminal evidence: %+v", len(exec.submits), exec.submits)
	}
}

func TestRuntimeOrderLifecycleDoesNotDisarmRestingCleanupBeforeTerminalEvidence(t *testing.T) {
	exec := newCancelAcknowledgedButOpenLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-resting-terminal-evidence",
		btruntime.WithAccountID("TEST:unified"),
	)
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()
	readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
	if err := WaitForActive(readyCtx, node); err != nil {
		readyCancel()
		t.Fatalf("runtime node readiness: %v", err)
	}
	readyCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	spec := terminalFilledLifecycleSpec()
	spec.CleanupTimeout = 30 * time.Millisecond

	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
	if err == nil || !strings.Contains(err.Error(), "resting") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want resting terminal-evidence failure", err)
	}
	if len(exec.submits) != 1 {
		t.Fatalf("submits=%d, want no runtime opening order before venue terminal evidence: %+v", len(exec.submits), exec.submits)
	}
}

func TestAdapterOrderLifecycleDoesNotTreatUnknownRestingStatusAsTerminal(t *testing.T) {
	exec := newUnknownRestingLifecycleExec()
	spec := terminalFilledLifecycleSpec()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	_, err := RunAdapterOrderLifecycle(ctx, exec, spec)
	if err == nil || !strings.Contains(err.Error(), "resting") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want Unknown resting reconciliation failure", err)
	}
	if len(exec.submits) != 1 || exec.cancelCalls < 2 {
		t.Fatalf("submits=%d cancelCalls=%d, want no opening and repeated exact resting cancel", len(exec.submits), exec.cancelCalls)
	}
}

func TestRuntimeOrderLifecycleDoesNotTreatUnknownRestingStatusAsTerminal(t *testing.T) {
	exec := newUnknownRestingLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-unknown-resting",
		btruntime.WithAccountID("TEST:unified"),
	)
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()
	readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
	if err := WaitForActive(readyCtx, node); err != nil {
		readyCancel()
		t.Fatalf("runtime node readiness: %v", err)
	}
	readyCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	spec := terminalFilledLifecycleSpec()

	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
	if err == nil || !strings.Contains(err.Error(), "resting") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want runtime Unknown resting reconciliation failure", err)
	}
	if len(exec.submits) != 1 || exec.cancelCalls < 2 {
		t.Fatalf("submits=%d cancelCalls=%d, want no runtime opening and repeated exact resting cancel", len(exec.submits), exec.cancelCalls)
	}
}

func TestAdapterOrderLifecycleRejectsRestingFillDiscoveredDuringCancelReconcile(t *testing.T) {
	exec := newRestingFillDuringCancelLifecycleExec()
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err == nil || !strings.Contains(err.Error(), "resting order unexpectedly filled during cancel") || !strings.Contains(err.Error(), "0.001") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want reconciled resting partial-fill rejection", err)
	}
	exec.assertRestingFillCleaned(t, true)
}

func TestRuntimeOrderLifecycleRejectsRestingFillDiscoveredDuringCancelReconcile(t *testing.T) {
	exec := newRestingFillDuringCancelLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-resting-fill-during-cancel",
		btruntime.WithAccountID("TEST:unified"),
	)
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp

	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
	if err == nil || !strings.Contains(err.Error(), "runtime resting order unexpectedly filled during cancel") || !strings.Contains(err.Error(), "0.001") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want reconciled runtime resting partial-fill rejection", err)
	}
	exec.assertRestingFillCleaned(t, true)
}

func TestAdapterSpotOrderLifecycleCleansRestingFillDiscoveredDuringCancelReconcile(t *testing.T) {
	exec := newRestingFillDuringCancelLifecycleExec()
	reporter := &lifecycleExposureBalanceReporter{exec: exec}
	spec := terminalFilledLifecycleSpec()
	spec.CloseQuantity = spec.Quantity
	spec = ConfigureSpotBalanceGuard(spec, reporter, "BTC", decimal.RequireFromString("0.001"), decimal.RequireFromString("0.001"), decimal.NewFromInt(1), decimal.Zero)

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err == nil || !strings.Contains(err.Error(), "resting order unexpectedly filled during cancel") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want reconciled Spot resting fill rejection", err)
	}
	exec.assertRestingFillCleaned(t, false)
}

func TestRuntimeSpotOrderLifecycleCleansRestingFillDiscoveredDuringCancelReconcile(t *testing.T) {
	exec := newRestingFillDuringCancelLifecycleExec()
	reporter := &lifecycleExposureBalanceReporter{exec: exec}
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-spot-resting-fill-during-cancel",
		btruntime.WithAccountID("TEST:unified"),
	)
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	spec := terminalFilledLifecycleSpec()
	spec.CloseQuantity = spec.Quantity
	spec = ConfigureSpotBalanceGuard(spec, reporter, "BTC", decimal.RequireFromString("0.001"), decimal.RequireFromString("0.001"), decimal.NewFromInt(1), decimal.Zero)

	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
	if err == nil || !strings.Contains(err.Error(), "runtime resting order unexpectedly filled during cancel") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want reconciled runtime Spot resting fill rejection", err)
	}
	exec.assertRestingFillCleaned(t, false)
}

func TestAdapterOrderLifecycleCleansLateFillAfterTerminalZeroOpening(t *testing.T) {
	exec := newTerminalZeroThenLateFillExec()
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err == nil || !strings.Contains(err.Error(), "terminal status CANCELED with zero fill") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want terminal-zero opening error", err)
	}
	exec.assertLateOpeningFillCleaned(t)
}

func TestRuntimeOrderLifecycleCleansLateFillAfterTerminalZeroOpening(t *testing.T) {
	exec := newTerminalZeroThenLateFillExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-terminal-zero-late-fill",
		btruntime.WithAccountID("TEST:unified"),
	)
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp

	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
	if err == nil || !strings.Contains(err.Error(), "terminal status CANCELED with zero fill") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want runtime terminal-zero opening error", err)
	}
	exec.assertLateOpeningFillCleaned(t)
}

func TestAdapterOrderLifecycleDoesNotCleanUnrelatedLateFill(t *testing.T) {
	exec := newUnrelatedLateFillExec()
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp

	_, err := RunAdapterOrderLifecycle(context.Background(), exec, spec)
	if err == nil || !strings.Contains(err.Error(), "fill identity") {
		t.Fatalf("RunAdapterOrderLifecycle err=%v, want unrelated fill rejection", err)
	}
	exec.assertNoCleanupSell(t)
}

func TestRuntimeOrderLifecycleDoesNotCleanUnrelatedLateFill(t *testing.T) {
	exec := newUnrelatedLateFillExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-unrelated-late-fill",
		btruntime.WithAccountID("TEST:unified"),
	)
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp

	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
	if err == nil || !strings.Contains(err.Error(), "fill identity") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want unrelated fill rejection", err)
	}
	exec.assertNoCleanupSell(t)
}

func TestAdapterOrderLifecycleRetriesSlowFillPoll(t *testing.T) {
	exec := &slowFillLifecycleExec{}
	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindSpot}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	result, err := RunAdapterOrderLifecycle(ctx, exec, OrderLifecycleSpec{
		Label:              "slow fill spot",
		AccountID:          "TEST:unified",
		InstrumentID:       instID,
		Quantity:           decimal.RequireFromString("0.01"),
		RestingPrice:       decimal.RequireFromString("49000"),
		FillPrice:          decimal.RequireFromString("51000"),
		ClosePrice:         decimal.RequireFromString("50000"),
		PositionSide:       enums.PosNet,
		CloseAfterFill:     false,
		PollInterval:       time.Millisecond,
		PollRequestTimeout: 5 * time.Millisecond,
		CleanupTimeout:     20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunAdapterOrderLifecycle: %v", err)
	}
	if !result.FilledQty.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("filled qty=%s, want 0.01", result.FilledQty)
	}
	if exec.fillReportCalls < 2 {
		t.Fatalf("fill report calls=%d, want retry after slow poll", exec.fillReportCalls)
	}
}

func TestRuntimeOrderLifecycleUsesTradingNodeExecution(t *testing.T) {
	exec := newRuntimeLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-lifecycle",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("runtime node did not stop")
		}
	}()

	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	var beforeClose atomic.Bool
	result, err := RunRuntimeOrderLifecycle(ctx, node, exec, OrderLifecycleSpec{
		Label:          "runtime test perp",
		AccountID:      "TEST:unified",
		InstrumentID:   instID,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		BeforeRuntimeClose: func(_ context.Context, qty decimal.Decimal) error {
			if !qty.Equal(decimal.RequireFromString("0.01")) {
				return fmt.Errorf("close readiness qty=%s", qty)
			}
			beforeClose.Store(true)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunRuntimeOrderLifecycle: %v", err)
	}
	if result.FilledQty.String() != "0.01" {
		t.Fatalf("filled qty=%s, want 0.01", result.FilledQty)
	}
	if !beforeClose.Load() {
		t.Fatal("runtime close readiness hook was not called")
	}
	if len(exec.submits) != 3 {
		t.Fatalf("submits=%d, want resting/fill/close: %+v", len(exec.submits), exec.submits)
	}
	if got := exec.submits[0].AccountID; got != "TEST:unified" {
		t.Fatalf("runtime submit account id=%q", got)
	}
	if got := exec.submits[2]; got.Side != enums.SideSell || !got.ReduceOnly {
		t.Fatalf("runtime close submit=%+v", got)
	}
	if open := node.Cache.OpenOrders(); len(open) != 0 {
		t.Fatalf("runtime cache open orders=%d, want 0: %+v", len(open), open)
	}
}

func TestRuntimeOrderLifecycleWaitsForActiveBeforeCloseAfterStreamRecovery(t *testing.T) {
	exec := newRuntimeLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-lifecycle-gap",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("runtime node did not stop")
		}
	}()

	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	result, err := RunRuntimeOrderLifecycle(ctx, node, exec, OrderLifecycleSpec{
		Label:          "runtime stream recovery perp",
		AccountID:      "TEST:unified",
		InstrumentID:   instID,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		BeforeRuntimeClose: func(context.Context, decimal.Decimal) error {
			exec.events <- contract.NewExecEnvelope(contract.StreamGapEvent{
				Venue: "TEST", AccountID: "TEST:unified", StreamID: "private", Generation: 1,
				Phase: contract.StreamGapStarted, Reason: "test recovery",
			})
			deadline := time.Now().Add(time.Second)
			for node.State().Trading != lifecycle.TradingReconciling {
				if time.Now().After(deadline) {
					return fmt.Errorf("runtime did not enter reconciling: %+v", node.State())
				}
				time.Sleep(time.Millisecond)
			}
			go func() {
				time.Sleep(25 * time.Millisecond)
				exec.events <- contract.NewExecEnvelope(contract.StreamGapEvent{
					Venue: "TEST", AccountID: "TEST:unified", StreamID: "private", Generation: 1,
					Phase: contract.StreamGapRecovered, Reason: "test recovery complete",
				})
			}()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunRuntimeOrderLifecycle across stream recovery: %v", err)
	}
	if result == nil || !result.ClosedQty.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("result=%+v, want fully closed lifecycle", result)
	}
	if len(exec.submits) != 3 {
		t.Fatalf("venue submits=%d, want resting/fill/close", len(exec.submits))
	}
}

func TestRuntimeOrderLifecycleAllowsReduceOnlyCloseWhileRuntimeIsReducing(t *testing.T) {
	exec := newRuntimeLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-lifecycle-reducing",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("runtime node did not stop")
		}
	}()

	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	result, err := RunRuntimeOrderLifecycle(ctx, node, exec, OrderLifecycleSpec{
		Label:          "runtime reducing perp",
		AccountID:      "TEST:unified",
		InstrumentID:   instID,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 100 * time.Millisecond,
		BeforeRuntimeClose: func(context.Context, decimal.Decimal) error {
			node.ReduceOnly("test operator restriction")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunRuntimeOrderLifecycle in reducing state: %v", err)
	}
	if result == nil || !result.ClosedQty.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("result=%+v, want reduce-only close", result)
	}
	if len(exec.submits) != 3 || !exec.submits[2].ReduceOnly {
		t.Fatalf("submits=%+v, want venue reduce-only close", exec.submits)
	}
}

func TestRuntimeSpotOrderLifecycleUsesAuthoritativeBalanceGuard(t *testing.T) {
	exec := newRuntimeLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-spot-balance-guard",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("runtime node did not stop")
		}
	}()
	reporter := &sequenceAccountStateSource{states: []model.AccountState{
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "11", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "11", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.005", "0"),
		spotBalanceState("TEST:unified", "TEST", "BTC", "10.005", "0"),
	}}

	result, err := RunRuntimeOrderLifecycle(ctx, node, exec, guardedSpotLifecycleSpec(reporter))
	if err != nil {
		t.Fatalf("RunRuntimeOrderLifecycle: %v", err)
	}
	if result == nil || !result.ClosedQty.Equal(decimal.RequireFromString("0.995")) {
		t.Fatalf("result=%+v, want guarded close quantity 0.995", result)
	}
	if len(exec.submits) != 3 {
		t.Fatalf("submits=%d, want resting/fill/close: %+v", len(exec.submits), exec.submits)
	}
}

func TestRuntimeOrderLifecycleRejectsPreExistingPositionBeforeSubmit(t *testing.T) {
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	exec := newPreExistingRuntimeLifecycleExec([]model.PositionReport{{Position: model.Position{
		AccountID:    "TEST:unified",
		InstrumentID: id,
		Side:         enums.PosLong,
		Quantity:     decimal.RequireFromString("0.01"),
	}}})
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-pre-existing",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("runtime node did not stop")
		}
	}()

	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, OrderLifecycleSpec{
		Label:          "runtime pre-existing position",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 10 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "pre-existing position") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want pre-existing position rejection", err)
	}
	if len(exec.submits) != 0 {
		t.Fatalf("venue submits=%d, want 0 before runtime pre-existing position rejection: %+v", len(exec.submits), exec.submits)
	}
}

func TestRuntimeOrderLifecycleWaitsForLateFillQuantity(t *testing.T) {
	exec := newLateFillRuntimeLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-late-fill",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("runtime node did not stop")
		}
	}()

	instID := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindSpot}
	result, err := RunRuntimeOrderLifecycle(ctx, node, exec, OrderLifecycleSpec{
		Label:          "runtime late fill spot",
		AccountID:      "TEST:unified",
		InstrumentID:   instID,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunRuntimeOrderLifecycle: %v", err)
	}
	if !result.FilledQty.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("filled qty=%s, want 0.01", result.FilledQty)
	}
	if got := exec.lateFills.Load(); got < 2 {
		t.Fatalf("late fill events=%d, want fill and close events", got)
	}
}

func TestRuntimeOrderLifecycleClosesAuthoritativePartialIOCQuantity(t *testing.T) {
	exec := newPartialOpeningRuntimeLifecycleExec(decimal.RequireFromString("0.004"))
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-partial-open",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()

	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	result, err := RunRuntimeOrderLifecycle(ctx, node, exec, OrderLifecycleSpec{
		Label:          "runtime partial IOC open",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunRuntimeOrderLifecycle: %v", err)
	}
	if result == nil || !result.FilledQty.Equal(exec.partialQty) || !result.ClosedQty.Equal(exec.partialQty) {
		t.Fatalf("result=%+v, want partial runtime fill and bounded close %s", result, exec.partialQty)
	}
	if len(exec.submits) != 3 || !exec.submits[2].Quantity.Equal(exec.partialQty) {
		t.Fatalf("submits=%+v, want runtime close capped to %s", exec.submits, exec.partialQty)
	}
}

func TestRuntimeOrderLifecycleRejectsPartialCloseFill(t *testing.T) {
	exec := newPartialCloseRuntimeLifecycleExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-partial-close",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()

	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, OrderLifecycleSpec{
		Label:          "runtime partial close",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 20 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "partial fill") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want partial fill failure", err)
	}
}

func TestRuntimeOrderLifecycleHandlesStatusFilledPartialAndZeroQuantities(t *testing.T) {
	tests := []struct {
		name        string
		openingQty  decimal.Decimal
		closeQty    decimal.Decimal
		wantFilled  decimal.Decimal
		wantClosed  decimal.Decimal
		wantErr     string
		wantSubmits int
	}{
		{
			name:        "opening partial closes actual quantity",
			openingQty:  decimal.RequireFromString("0.004"),
			closeQty:    decimal.RequireFromString("0.004"),
			wantFilled:  decimal.RequireFromString("0.004"),
			wantClosed:  decimal.RequireFromString("0.004"),
			wantSubmits: 3,
		},
		{
			name:        "opening zero fails without close",
			openingQty:  decimal.Zero,
			closeQty:    decimal.Zero,
			wantErr:     "terminal status FILLED with zero fill",
			wantSubmits: 2,
		},
		{
			name:        "close partial fails with actual quantity",
			openingQty:  decimal.RequireFromString("0.01"),
			closeQty:    decimal.RequireFromString("0.004"),
			wantErr:     "partial fill 0.004/0.009",
			wantSubmits: 3,
		},
		{
			name:        "close zero fails",
			openingQty:  decimal.RequireFromString("0.01"),
			closeQty:    decimal.Zero,
			wantErr:     "terminal status FILLED with zero fill",
			wantSubmits: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := newTerminalFilledRuntimeLifecycleExec(tt.openingQty, tt.closeQty)
			node := btruntime.NewNode(
				btruntime.Clients{Execution: exec},
				clock.NewRealClock(),
				"runtime-terminal-filled",
				btruntime.WithAccountID("TEST:unified"),
			)
			runCtx, stop := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				node.Run(runCtx)
				close(done)
			}()
			defer func() {
				stop()
				<-done
			}()
			readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
			if err := WaitForActive(readyCtx, node); err != nil {
				readyCancel()
				t.Fatalf("runtime node readiness: %v", err)
			}
			readyCancel()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			result, err := RunRuntimeOrderLifecycle(ctx, node, exec, terminalFilledLifecycleSpec())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("RunRuntimeOrderLifecycle err=%v, want %q", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("RunRuntimeOrderLifecycle: %v", err)
				}
				if result == nil || !result.FilledQty.Equal(tt.wantFilled) || !result.ClosedQty.Equal(tt.wantClosed) {
					t.Fatalf("result=%+v, want filled=%s closed=%s", result, tt.wantFilled, tt.wantClosed)
				}
			}
			if len(exec.submits) != tt.wantSubmits {
				t.Fatalf("submits=%d, want %d: %+v", len(exec.submits), tt.wantSubmits, exec.submits)
			}
		})
	}
}

func TestRuntimeOrderLifecycleDoesNotCleanupPositionWhenFillResultIsAmbiguous(t *testing.T) {
	exec := newAmbiguousFillRuntimeCleanupExec()
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-ambiguous-fill",
		btruntime.WithAccountID("TEST:unified"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("runtime node did not stop")
		}
	}()

	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, OrderLifecycleSpec{
		Label:          "ambiguous runtime fill",
		AccountID:      "TEST:unified",
		InstrumentID:   id,
		Quantity:       decimal.RequireFromString("0.01"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 20 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "runtime wait for fill") || !strings.Contains(err.Error(), "position cleanup not armed") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want original fill-wait error plus unarmed cleanup error", err)
	}
	if got := exec.reduceOnlyAttempts.Load(); got != 0 {
		t.Fatalf("reduce-only attempts=%d, want 0 without an observed lifecycle fill", got)
	}
}

func TestRuntimeOrderLifecycleDoesNotCloseBeforeOpeningFillReachesPortfolio(t *testing.T) {
	exec := newTerminalFilledLifecycleExec(
		decimal.RequireFromString("0.01"),
		decimal.RequireFromString("0.01"),
	)
	node := btruntime.NewNode(
		btruntime.Clients{Execution: exec},
		clock.NewRealClock(),
		"runtime-missing-opening-fill",
		btruntime.WithAccountID("TEST:unified"),
	)
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer func() {
		stop()
		<-done
	}()

	readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
	if err := WaitForActive(readyCtx, node); err != nil {
		readyCancel()
		t.Fatalf("runtime node readiness: %v", err)
	}
	readyCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	spec := terminalFilledLifecycleSpec()
	spec.InstrumentID.Kind = enums.KindPerp

	_, err := RunRuntimeOrderLifecycle(ctx, node, exec, spec)
	if err == nil || !strings.Contains(err.Error(), "runtime portfolio opening fill") {
		t.Fatalf("RunRuntimeOrderLifecycle err=%v, want missing opening Portfolio fill", err)
	}
	if len(exec.submits) != 2 {
		t.Fatalf("submits=%d, want only resting/opening before safety stop: %+v", len(exec.submits), exec.submits)
	}
	for _, req := range exec.submits {
		if req.Side == enums.SideSell {
			t.Fatalf("missing opening Portfolio fill authorized close: %+v", req)
		}
	}
}

type recordingLifecycleExec struct {
	submits            []model.OrderRequest
	cancelVenueOrderID string
}

func (e *recordingLifecycleExec) AccountID() string { return "TEST:unified" }

func terminalFilledLifecycleSpec() OrderLifecycleSpec {
	return OrderLifecycleSpec{
		Label:          "terminal FILLED Spot lifecycle",
		Venue:          "TEST",
		AccountID:      "TEST:unified",
		InstrumentID:   model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindSpot},
		Quantity:       decimal.RequireFromString("0.01"),
		CloseQuantity:  decimal.RequireFromString("0.009"),
		RestingPrice:   decimal.RequireFromString("49000"),
		FillPrice:      decimal.RequireFromString("51000"),
		ClosePrice:     decimal.RequireFromString("50000"),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 50 * time.Millisecond,
	}
}

type terminalFilledLifecycleExec struct {
	recordingLifecycleExec
	openingQty decimal.Decimal
	closeQty   decimal.Decimal
	orders     map[string]model.Order
	events     chan contract.ExecEnvelope
	emitFills  bool
}

func newTerminalFilledLifecycleExec(openingQty, closeQty decimal.Decimal) *terminalFilledLifecycleExec {
	return &terminalFilledLifecycleExec{
		openingQty: openingQty,
		closeQty:   closeQty,
		orders:     make(map[string]model.Order),
		events:     make(chan contract.ExecEnvelope, 8),
	}
}

func newTerminalFilledRuntimeLifecycleExec(openingQty, closeQty decimal.Decimal) *terminalFilledLifecycleExec {
	exec := newTerminalFilledLifecycleExec(openingQty, closeQty)
	exec.emitFills = true
	return exec
}

func (e *terminalFilledLifecycleExec) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submits = append(e.submits, req)
	order := model.Order{
		Request:      req,
		VenueOrderID: fmt.Sprintf("terminal-filled-%d", len(e.submits)),
		Status:       enums.StatusNew,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if req.TIF == enums.TifIOC {
		order.Status = enums.StatusFilled
		if req.Side == enums.SideBuy {
			order.FilledQty = e.openingQty
		} else {
			order.FilledQty = e.closeQty
		}
	}
	e.orders[order.VenueOrderID] = order
	if e.emitFills && order.FilledQty.IsPositive() {
		e.events <- lifecycleFillEnvelope(order, order.FilledQty)
	}
	return &order, nil
}

func (e *terminalFilledLifecycleExec) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	if err := e.recordingLifecycleExec.Cancel(ctx, id, venueOrderID); err != nil {
		return err
	}
	order := e.orders[venueOrderID]
	order.Status = enums.StatusCanceled
	e.orders[venueOrderID] = order
	return nil
}

func (e *terminalFilledLifecycleExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	for _, order := range e.orders {
		if query.VenueOrderID != "" && order.VenueOrderID != query.VenueOrderID {
			continue
		}
		if query.ClientID != "" && order.Request.ClientID != query.ClientID {
			continue
		}
		return &model.OrderStatusReport{AccountID: order.Request.AccountID, Order: order}, nil
	}
	return nil, nil
}

func (e *terminalFilledLifecycleExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	return nil, nil
}

func (e *terminalFilledLifecycleExec) Events() <-chan contract.ExecEnvelope { return e.events }

type placedThenErroredLifecycleExec struct {
	recordingLifecycleExec
	events              chan contract.ExecEnvelope
	placed              model.Order
	unrelated           model.Order
	postSubmitOpenCalls int
	statusCalls         int
	canceled            []string
}

type ambiguousIOCEvidenceExec struct {
	recordingLifecycleExec
	mode         string
	req          model.OrderRequest
	venueOrderID string
	statusCalls  int
	fillCalls    int
	openCalls    int
	canceled     []string
}

type venueLearningFillExec struct {
	recordingLifecycleExec
	statusUsedClientOnly bool
	fillUsedVenueOnly    bool
}

type unknownThenCanceledFillExec struct {
	recordingLifecycleExec
	statusCalls int
}

type unrelatedFillReportExec struct {
	recordingLifecycleExec
}

func (e *unrelatedFillReportExec) GenerateOrderStatusReport(context.Context, model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	return nil, nil
}

func (e *unrelatedFillReportExec) GenerateFillReports(_ context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	return []model.FillReport{{
		AccountID: query.AccountID,
		Fill: model.Fill{
			AccountID: query.AccountID, InstrumentID: query.InstrumentID,
			ClientID: "other-client", VenueOrderID: "other-venue", TradeID: "other-trade",
			Quantity: decimal.RequireFromString("0.01"), Price: decimal.RequireFromString("51000"),
		},
	}}, nil
}

func (e *unknownThenCanceledFillExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	e.statusCalls++
	return &model.OrderStatusReport{
		AccountID: query.AccountID,
		Order: model.Order{
			Request: model.OrderRequest{
				AccountID:    query.AccountID,
				ClientID:     "",
				InstrumentID: query.InstrumentID,
				Quantity:     decimal.RequireFromString("0.01"),
			},
			VenueOrderID: query.VenueOrderID,
			Status:       enums.StatusCanceled,
			FilledQty:    decimal.RequireFromString("0.004"),
		},
	}, nil
}

func (e *unknownThenCanceledFillExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	return nil, nil
}

func (e *venueLearningFillExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	e.statusUsedClientOnly = query.ClientID != "" && query.VenueOrderID == ""
	return &model.OrderStatusReport{
		AccountID: query.AccountID,
		Order: model.Order{
			Request: model.OrderRequest{
				AccountID:    query.AccountID,
				ClientID:     query.ClientID,
				InstrumentID: query.InstrumentID,
				Quantity:     decimal.RequireFromString("0.01"),
			},
			VenueOrderID: "learned-venue-id",
			Status:       enums.StatusNew,
		},
	}, nil
}

func (e *venueLearningFillExec) GenerateFillReports(_ context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	e.fillUsedVenueOnly = query.ClientID == "" && query.VenueOrderID == "learned-venue-id"
	if !e.fillUsedVenueOnly {
		return nil, fmt.Errorf("fill query did not prefer learned venue id: client=%q venue=%q", query.ClientID, query.VenueOrderID)
	}
	return []model.FillReport{{
		AccountID: query.AccountID,
		Fill: model.Fill{
			AccountID:    query.AccountID,
			InstrumentID: query.InstrumentID,
			VenueOrderID: query.VenueOrderID,
			TradeID:      "venue-learning-fill",
			Quantity:     decimal.RequireFromString("0.01"),
		},
	}}, nil
}

func ambiguousIOCTestFixture() (OrderLifecycleSpec, *lifecycleOrderTracker, *trackedLifecycleOrder, model.OrderRequest) {
	spec := terminalFilledLifecycleSpec()
	spec.PollInterval = time.Millisecond
	spec.PollRequestTimeout = 5 * time.Millisecond
	tracker := newLifecycleOrderTracker()
	tracked := tracker.add("fill")
	req := model.OrderRequest{
		AccountID:    spec.AccountID,
		ClientID:     tracked.clientID,
		InstrumentID: spec.InstrumentID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     spec.Quantity,
		Price:        spec.FillPrice,
		PositionSide: spec.PositionSide,
	}
	tracked.request = req
	return spec, tracker, tracked, req
}

func (e *ambiguousIOCEvidenceExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	e.statusCalls++
	if e.req.ClientID == "" {
		e.req = model.OrderRequest{AccountID: query.AccountID, ClientID: query.ClientID, InstrumentID: query.InstrumentID}
		e.venueOrderID = "ambiguous-evidence-order"
	}
	order := model.Order{
		Request:      e.req,
		VenueOrderID: e.venueOrderID,
		Status:       enums.StatusNew,
		FilledQty:    decimal.RequireFromString("0.004"),
	}
	switch e.mode {
	case "cancel_then_absent":
		if len(e.canceled) != 0 {
			return nil, nil
		}
	case "fill_then_terminal":
		if e.statusCalls > 1 {
			order.Status = enums.StatusCanceled
			order.FilledQty = decimal.Zero
		}
	case "unknown_then_terminal":
		order.Status = enums.StatusUnknown
		if len(e.canceled) != 0 {
			order.Status = enums.StatusCanceled
		}
	case "unrelated_fill":
		order.FilledQty = decimal.Zero
	}
	return &model.OrderStatusReport{AccountID: query.AccountID, Order: order}, nil
}

func (e *ambiguousIOCEvidenceExec) GenerateFillReports(_ context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	e.fillCalls++
	if e.mode == "fill_then_terminal" && e.fillCalls > 1 {
		return nil, errors.New("fill history temporarily unavailable")
	}
	clientID := query.ClientID
	venueOrderID := e.venueOrderID
	if e.mode == "unrelated_fill" {
		clientID = e.req.ClientID
		venueOrderID = "other-venue"
	}
	return []model.FillReport{{
		AccountID: query.AccountID,
		Fill: model.Fill{
			AccountID:    query.AccountID,
			InstrumentID: query.InstrumentID,
			ClientID:     clientID,
			VenueOrderID: venueOrderID,
			TradeID:      "ambiguous-fill",
			Quantity:     decimal.RequireFromString("0.004"),
			Price:        decimal.RequireFromString("51000"),
		},
	}}, nil
}

func (e *ambiguousIOCEvidenceExec) OpenOrders(_ context.Context, _ model.InstrumentID) ([]model.Order, error) {
	e.openCalls++
	if e.mode == "fill_then_terminal" && e.openCalls == 1 {
		return []model.Order{{
			Request:      e.req,
			VenueOrderID: e.venueOrderID,
			Status:       enums.StatusNew,
			FilledQty:    decimal.RequireFromString("0.004"),
		}}, nil
	}
	if e.mode == "unknown_then_terminal" && len(e.canceled) == 0 {
		return []model.Order{{
			Request:      e.req,
			VenueOrderID: e.venueOrderID,
			Status:       enums.StatusUnknown,
			FilledQty:    decimal.RequireFromString("0.004"),
		}}, nil
	}
	return nil, nil
}

func (e *ambiguousIOCEvidenceExec) Cancel(_ context.Context, _ model.InstrumentID, venueOrderID string) error {
	e.canceled = append(e.canceled, venueOrderID)
	return nil
}

func newPlacedThenErroredLifecycleExec() *placedThenErroredLifecycleExec {
	return &placedThenErroredLifecycleExec{events: make(chan contract.ExecEnvelope, 8)}
}

func (e *placedThenErroredLifecycleExec) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submits = append(e.submits, req)
	e.placed = model.Order{
		Request:      req,
		VenueOrderID: "ambiguous-current",
		Status:       enums.StatusNew,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	e.unrelated = model.Order{
		Request: model.OrderRequest{
			AccountID:    req.AccountID,
			ClientID:     "btac-unrelated",
			InstrumentID: req.InstrumentID,
		},
		VenueOrderID: "ambiguous-unrelated",
		Status:       enums.StatusNew,
	}
	return nil, errors.New("submit response lost after placement")
}

func (e *placedThenErroredLifecycleExec) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	if e.placed.VenueOrderID == "" {
		return nil, nil
	}
	e.postSubmitOpenCalls++
	if e.postSubmitOpenCalls == 1 {
		return nil, nil
	}
	open := []model.Order{e.unrelated}
	if e.placed.Status != enums.StatusCanceled {
		open = append(open, e.placed)
	}
	return open, nil
}

func (e *placedThenErroredLifecycleExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	e.statusCalls++
	if e.statusCalls == 1 || query.ClientID != e.placed.Request.ClientID {
		return nil, nil
	}
	return &model.OrderStatusReport{AccountID: e.placed.Request.AccountID, Order: e.placed}, nil
}

func (e *placedThenErroredLifecycleExec) Cancel(_ context.Context, _ model.InstrumentID, venueOrderID string) error {
	e.canceled = append(e.canceled, venueOrderID)
	if venueOrderID == e.placed.VenueOrderID {
		e.placed.Status = enums.StatusCanceled
	}
	return nil
}

func (e *placedThenErroredLifecycleExec) Events() <-chan contract.ExecEnvelope { return e.events }

func (e *placedThenErroredLifecycleExec) assertExactCleanup(t *testing.T) {
	t.Helper()
	if len(e.canceled) != 1 || e.canceled[0] != e.placed.VenueOrderID {
		t.Fatalf("canceled=%v, want exact lifecycle venue id %q only", e.canceled, e.placed.VenueOrderID)
	}
	if e.postSubmitOpenCalls < 2 || e.statusCalls < 1 {
		t.Fatalf("cleanup evidence openCalls=%d statusCalls=%d, want bounded eventual-consistency polling", e.postSubmitOpenCalls, e.statusCalls)
	}
}

type stagePlacedThenErroredLifecycleExec struct {
	*terminalFilledLifecycleExec
	stage               string
	placed              model.Order
	unrelated           model.Order
	postSubmitOpenCalls int
	statusCalls         int
	cleanupCanceled     []string
}

func newStagePlacedThenErroredLifecycleExec(stage string) *stagePlacedThenErroredLifecycleExec {
	return &stagePlacedThenErroredLifecycleExec{
		terminalFilledLifecycleExec: newTerminalFilledLifecycleExec(decimal.RequireFromString("0.01"), decimal.RequireFromString("0.01")),
		stage:                       stage,
	}
}

func (e *stagePlacedThenErroredLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	kind := "rest"
	if req.TIF == enums.TifIOC && req.Side == enums.SideBuy {
		kind = "fill"
	} else if req.TIF == enums.TifIOC && req.Side == enums.SideSell {
		kind = "close"
	}
	if kind != e.stage {
		return e.terminalFilledLifecycleExec.Submit(ctx, req)
	}
	e.submits = append(e.submits, req)
	e.placed = model.Order{
		Request:      req,
		VenueOrderID: "ambiguous-" + kind,
		Status:       enums.StatusNew,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	e.unrelated = model.Order{
		Request: model.OrderRequest{
			AccountID:    req.AccountID,
			ClientID:     "btac-unrelated-" + kind,
			InstrumentID: req.InstrumentID,
		},
		VenueOrderID: "unrelated-" + kind,
		Status:       enums.StatusNew,
	}
	return nil, fmt.Errorf("%s submit response lost after placement", kind)
}

func (e *stagePlacedThenErroredLifecycleExec) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	if e.placed.VenueOrderID == "" {
		return e.terminalFilledLifecycleExec.OpenOrders(ctx, id)
	}
	e.postSubmitOpenCalls++
	if e.postSubmitOpenCalls == 1 {
		return nil, nil
	}
	open := []model.Order{e.unrelated}
	if e.placed.Status != enums.StatusCanceled {
		open = append(open, e.placed)
	}
	return open, nil
}

func (e *stagePlacedThenErroredLifecycleExec) GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	if e.placed.VenueOrderID == "" || (query.ClientID != e.placed.Request.ClientID && query.VenueOrderID != e.placed.VenueOrderID) {
		return e.terminalFilledLifecycleExec.GenerateOrderStatusReport(ctx, query)
	}
	e.statusCalls++
	if e.statusCalls == 1 {
		return nil, nil
	}
	return &model.OrderStatusReport{AccountID: e.placed.Request.AccountID, Order: e.placed}, nil
}

func (e *stagePlacedThenErroredLifecycleExec) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	if venueOrderID != e.placed.VenueOrderID {
		return e.terminalFilledLifecycleExec.Cancel(ctx, id, venueOrderID)
	}
	e.cleanupCanceled = append(e.cleanupCanceled, venueOrderID)
	e.placed.Status = enums.StatusCanceled
	return nil
}

func (e *stagePlacedThenErroredLifecycleExec) assertExactCleanup(t *testing.T) {
	t.Helper()
	if len(e.cleanupCanceled) != 1 || e.cleanupCanceled[0] != e.placed.VenueOrderID {
		t.Fatalf("cleanup canceled=%v, want exact %s order %q only", e.cleanupCanceled, e.stage, e.placed.VenueOrderID)
	}
	if e.postSubmitOpenCalls < 2 || e.statusCalls < 1 {
		t.Fatalf("cleanup evidence openCalls=%d statusCalls=%d, want eventual-consistency polling", e.postSubmitOpenCalls, e.statusCalls)
	}
}

type cancelAcknowledgedButOpenLifecycleExec struct {
	*terminalFilledLifecycleExec
	resting             model.Order
	cancelAcknowledged  bool
	postCancelOpenCalls int
}

type unknownRestingLifecycleExec struct {
	*terminalFilledLifecycleExec
	resting     model.Order
	cancelCalls int
}

func newUnknownRestingLifecycleExec() *unknownRestingLifecycleExec {
	return &unknownRestingLifecycleExec{
		terminalFilledLifecycleExec: newTerminalFilledLifecycleExec(decimal.RequireFromString("0.01"), decimal.RequireFromString("0.01")),
	}
}

func (e *unknownRestingLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	order, err := e.terminalFilledLifecycleExec.Submit(ctx, req)
	if err == nil && req.TIF == enums.TifGTX {
		e.resting = *order
	}
	return order, err
}

func (e *unknownRestingLifecycleExec) Cancel(_ context.Context, _ model.InstrumentID, venueOrderID string) error {
	if venueOrderID == e.resting.VenueOrderID {
		e.cancelCalls++
	}
	return nil
}

func (e *unknownRestingLifecycleExec) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	if e.cancelCalls == 0 {
		return nil, nil
	}
	order := e.resting
	order.Status = enums.StatusUnknown
	return []model.Order{order}, nil
}

func (e *unknownRestingLifecycleExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	if query.ClientID == e.resting.Request.ClientID || query.VenueOrderID == e.resting.VenueOrderID {
		order := e.resting
		order.Status = enums.StatusUnknown
		return &model.OrderStatusReport{AccountID: e.resting.Request.AccountID, Order: order}, nil
	}
	return nil, nil
}

func (e *unknownRestingLifecycleExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	return nil, nil
}

type restingFillDuringCancelLifecycleExec struct {
	*terminalFilledLifecycleExec
	mu       sync.Mutex
	resting  model.Order
	exposure decimal.Decimal
}

func newRestingFillDuringCancelLifecycleExec() *restingFillDuringCancelLifecycleExec {
	return &restingFillDuringCancelLifecycleExec{
		terminalFilledLifecycleExec: newTerminalFilledLifecycleExec(decimal.RequireFromString("0.01"), decimal.RequireFromString("0.01")),
	}
}

func (e *restingFillDuringCancelLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.TIF == enums.TifIOC && req.Side == enums.SideSell {
		e.submits = append(e.submits, req)
		order := model.Order{
			Request:      req,
			VenueOrderID: fmt.Sprintf("rest-fill-cleanup-%d", len(e.submits)),
			Status:       enums.StatusFilled,
			FilledQty:    req.Quantity,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		e.orders[order.VenueOrderID] = order
		e.mu.Lock()
		e.exposure = e.exposure.Sub(req.Quantity)
		if e.exposure.IsNegative() {
			e.exposure = decimal.Zero
		}
		e.mu.Unlock()
		return &order, nil
	}
	order, err := e.terminalFilledLifecycleExec.Submit(ctx, req)
	if err == nil && req.TIF == enums.TifGTX {
		e.resting = *order
	}
	return order, err
}

func (e *restingFillDuringCancelLifecycleExec) GenerateFillReports(_ context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	clientMatch := query.ClientID != "" && query.ClientID == e.resting.Request.ClientID
	venueMatch := query.VenueOrderID != "" && query.VenueOrderID == e.resting.VenueOrderID
	if e.resting.VenueOrderID == "" || (!clientMatch && !venueMatch) {
		return nil, nil
	}
	e.mu.Lock()
	e.exposure = decimal.RequireFromString("0.001")
	e.mu.Unlock()
	return []model.FillReport{{
		AccountID: e.resting.Request.AccountID,
		Fill: model.Fill{
			AccountID:    e.resting.Request.AccountID,
			InstrumentID: e.resting.Request.InstrumentID,
			ClientID:     e.resting.Request.ClientID,
			VenueOrderID: e.resting.VenueOrderID,
			TradeID:      "resting-cancel-fill",
			Quantity:     decimal.RequireFromString("0.001"),
			Price:        e.resting.Request.Price,
		},
	}}, nil
}

func (e *restingFillDuringCancelLifecycleExec) GeneratePositionReports(_ context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.exposure.IsPositive() {
		return nil, nil
	}
	return []model.PositionReport{{
		AccountID: query.AccountID,
		Position: model.Position{
			AccountID:    query.AccountID,
			InstrumentID: query.InstrumentID,
			Side:         enums.PosNet,
			Quantity:     e.exposure,
		},
	}}, nil
}

func (e *restingFillDuringCancelLifecycleExec) assertRestingFillCleaned(t *testing.T, wantReduceOnly bool) {
	t.Helper()
	e.mu.Lock()
	exposure := e.exposure
	e.mu.Unlock()
	if !exposure.IsZero() {
		t.Fatalf("remaining exposure=%s, want flat/zero", exposure)
	}
	if len(e.submits) != 2 {
		t.Fatalf("submits=%d, want resting plus one cleanup and no normal opening: %+v", len(e.submits), e.submits)
	}
	cleanup := e.submits[1]
	if cleanup.Side != enums.SideSell || cleanup.TIF != enums.TifIOC || cleanup.ReduceOnly != wantReduceOnly || !cleanup.Quantity.Equal(decimal.RequireFromString("0.001")) {
		t.Fatalf("cleanup=%+v, want exact sell qty=0.001 reduceOnly=%t", cleanup, wantReduceOnly)
	}
}

type lifecycleExposureBalanceReporter struct {
	exec *restingFillDuringCancelLifecycleExec
}

func (r *lifecycleExposureBalanceReporter) AccountState(context.Context) (model.AccountState, error) {
	r.exec.mu.Lock()
	exposure := r.exec.exposure
	r.exec.mu.Unlock()
	return spotBalanceState("TEST:unified", "TEST", "BTC", decimal.NewFromInt(10).Add(exposure).String(), "0"), nil
}

type terminalZeroThenLateFillExec struct {
	*terminalFilledLifecycleExec
	mu                  sync.Mutex
	opening             model.Order
	exposure            decimal.Decimal
	fillReportCalls     int
	positionReportCalls int
}

type unrelatedLateFillExec struct {
	*terminalZeroThenLateFillExec
}

func newUnrelatedLateFillExec() *unrelatedLateFillExec {
	return &unrelatedLateFillExec{terminalZeroThenLateFillExec: newTerminalZeroThenLateFillExec()}
}

func (e *unrelatedLateFillExec) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	fills, err := e.terminalZeroThenLateFillExec.GenerateFillReports(ctx, query)
	for i := range fills {
		fills[i].Fill.VenueOrderID = "other-venue"
		fills[i].Fill.TradeID = "other-trade"
	}
	return fills, err
}

func (e *unrelatedLateFillExec) assertNoCleanupSell(t *testing.T) {
	t.Helper()
	if len(e.submits) != 2 {
		t.Fatalf("submits=%d, want resting/opening only and zero cleanup sells: %+v", len(e.submits), e.submits)
	}
	for _, req := range e.submits {
		if req.Side == enums.SideSell {
			t.Fatalf("unrelated fill authorized cleanup sell: %+v", req)
		}
	}
}

func newTerminalZeroThenLateFillExec() *terminalZeroThenLateFillExec {
	return &terminalZeroThenLateFillExec{
		terminalFilledLifecycleExec: newTerminalFilledLifecycleExec(decimal.Zero, decimal.RequireFromString("0.004")),
	}
}

func (e *terminalZeroThenLateFillExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.TIF == enums.TifIOC && req.Side == enums.SideBuy {
		e.submits = append(e.submits, req)
		order := model.Order{
			Request:      req,
			VenueOrderID: "terminal-zero-opening",
			Status:       enums.StatusCanceled,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		e.orders[order.VenueOrderID] = order
		e.opening = order
		return &order, nil
	}
	if req.TIF == enums.TifIOC && req.Side == enums.SideSell {
		e.submits = append(e.submits, req)
		order := model.Order{
			Request:      req,
			VenueOrderID: "late-fill-cleanup",
			Status:       enums.StatusFilled,
			FilledQty:    req.Quantity,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		e.orders[order.VenueOrderID] = order
		e.mu.Lock()
		e.exposure = e.exposure.Sub(req.Quantity)
		if e.exposure.IsNegative() {
			e.exposure = decimal.Zero
		}
		e.mu.Unlock()
		return &order, nil
	}
	return e.terminalFilledLifecycleExec.Submit(ctx, req)
}

func (e *terminalZeroThenLateFillExec) GenerateFillReports(_ context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	clientMatch := query.ClientID != "" && query.ClientID == e.opening.Request.ClientID
	venueMatch := query.VenueOrderID != "" && query.VenueOrderID == e.opening.VenueOrderID
	if e.opening.VenueOrderID == "" || (!clientMatch && !venueMatch) {
		return nil, nil
	}
	e.mu.Lock()
	e.fillReportCalls++
	fillReportCalls := e.fillReportCalls
	e.mu.Unlock()
	if fillReportCalls < 3 {
		return nil, nil
	}
	qty := decimal.RequireFromString("0.004")
	e.mu.Lock()
	e.exposure = qty
	e.mu.Unlock()
	return []model.FillReport{{
		AccountID: e.opening.Request.AccountID,
		Fill: model.Fill{
			AccountID:    e.opening.Request.AccountID,
			InstrumentID: e.opening.Request.InstrumentID,
			ClientID:     e.opening.Request.ClientID,
			VenueOrderID: e.opening.VenueOrderID,
			TradeID:      "late-opening-fill",
			Quantity:     qty,
			Price:        e.opening.Request.Price,
		},
	}}, nil
}

func (e *terminalZeroThenLateFillExec) GeneratePositionReports(_ context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.exposure.IsPositive() {
		return nil, nil
	}
	e.positionReportCalls++
	if e.positionReportCalls < 3 {
		return nil, nil
	}
	return []model.PositionReport{{
		AccountID: query.AccountID,
		Position: model.Position{
			AccountID:    query.AccountID,
			InstrumentID: query.InstrumentID,
			Side:         enums.PosNet,
			Quantity:     e.exposure,
		},
	}}, nil
}

func (e *terminalZeroThenLateFillExec) assertLateOpeningFillCleaned(t *testing.T) {
	t.Helper()
	e.mu.Lock()
	exposure := e.exposure
	fillReportCalls := e.fillReportCalls
	positionReportCalls := e.positionReportCalls
	e.mu.Unlock()
	if !exposure.IsZero() {
		t.Fatalf("remaining late-fill exposure=%s, want flat", exposure)
	}
	if fillReportCalls < 3 || positionReportCalls < 3 {
		t.Fatalf("fill polls=%d position polls=%d, want third-poll delayed evidence", fillReportCalls, positionReportCalls)
	}
	if len(e.submits) != 3 {
		t.Fatalf("submits=%d, want rest, one opening, one cleanup: %+v", len(e.submits), e.submits)
	}
	cleanup := e.submits[2]
	if cleanup.Side != enums.SideSell || !cleanup.ReduceOnly || !cleanup.Quantity.Equal(decimal.RequireFromString("0.004")) {
		t.Fatalf("cleanup=%+v, want exact reduce-only late-fill cleanup 0.004", cleanup)
	}
}

type ambiguousFilledOpeningLifecycleExec struct {
	*terminalFilledLifecycleExec
	partialQty   decimal.Decimal
	position     decimal.Decimal
	ambiguous    model.Order
	sellAttempts int
}

func newAmbiguousFilledOpeningLifecycleExec(partialQty, closeQty decimal.Decimal) *ambiguousFilledOpeningLifecycleExec {
	return &ambiguousFilledOpeningLifecycleExec{
		terminalFilledLifecycleExec: newTerminalFilledLifecycleExec(partialQty, closeQty),
		partialQty:                  partialQty,
	}
}

func (e *ambiguousFilledOpeningLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.TIF == enums.TifIOC && req.Side == enums.SideBuy {
		e.submits = append(e.submits, req)
		e.ambiguous = model.Order{
			Request:      req,
			VenueOrderID: "ambiguous-filled-opening",
			Status:       enums.StatusCanceled,
			FilledQty:    e.partialQty,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}
		e.orders[e.ambiguous.VenueOrderID] = e.ambiguous
		e.position = e.partialQty
		return nil, errors.New("opening submit response lost after fill")
	}
	order, err := e.terminalFilledLifecycleExec.Submit(ctx, req)
	if err == nil && req.Side == enums.SideSell {
		e.sellAttempts++
		e.position = e.position.Sub(order.FilledQty)
	}
	return order, err
}

func (e *ambiguousFilledOpeningLifecycleExec) GenerateFillReports(_ context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	clientMatch := query.ClientID != "" && query.ClientID == e.ambiguous.Request.ClientID
	venueMatch := query.VenueOrderID != "" && query.VenueOrderID == e.ambiguous.VenueOrderID
	if e.ambiguous.Request.ClientID == "" || (!clientMatch && !venueMatch) {
		return nil, nil
	}
	return []model.FillReport{{
		AccountID: e.ambiguous.Request.AccountID,
		Fill: model.Fill{
			AccountID:    e.ambiguous.Request.AccountID,
			InstrumentID: e.ambiguous.Request.InstrumentID,
			ClientID:     e.ambiguous.Request.ClientID,
			VenueOrderID: e.ambiguous.VenueOrderID,
			TradeID:      "ambiguous-opening-fill",
			Quantity:     e.partialQty,
			Price:        e.ambiguous.Request.Price,
		},
	}}, nil
}

func (e *ambiguousFilledOpeningLifecycleExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	if e.position.IsZero() {
		return nil, nil
	}
	return []model.PositionReport{{
		AccountID: "TEST:unified",
		Position: model.Position{
			AccountID:    "TEST:unified",
			InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
			Side:         enums.PosNet,
			Quantity:     e.position,
		},
	}}, nil
}

func (e *ambiguousFilledOpeningLifecycleExec) assertPerpCleanup(t *testing.T, want decimal.Decimal) {
	t.Helper()
	if !e.position.IsZero() || e.sellAttempts != 1 {
		t.Fatalf("position=%s sellAttempts=%d, want one exact Perp cleanup", e.position, e.sellAttempts)
	}
	last := e.submits[len(e.submits)-1]
	if !last.ReduceOnly || !last.Quantity.Equal(want) {
		t.Fatalf("cleanup request=%+v, want reduce-only quantity %s", last, want)
	}
}

func newCancelAcknowledgedButOpenLifecycleExec() *cancelAcknowledgedButOpenLifecycleExec {
	return &cancelAcknowledgedButOpenLifecycleExec{
		terminalFilledLifecycleExec: newTerminalFilledLifecycleExec(decimal.RequireFromString("0.01"), decimal.RequireFromString("0.01")),
	}
}

func (e *cancelAcknowledgedButOpenLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	order, err := e.terminalFilledLifecycleExec.Submit(ctx, req)
	if err == nil && req.TIF == enums.TifGTX {
		e.resting = *order
	}
	return order, err
}

func (e *cancelAcknowledgedButOpenLifecycleExec) Cancel(_ context.Context, _ model.InstrumentID, venueOrderID string) error {
	if venueOrderID == e.resting.VenueOrderID {
		e.cancelAcknowledged = true
	}
	return nil
}

func (e *cancelAcknowledgedButOpenLifecycleExec) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	if !e.cancelAcknowledged {
		return nil, nil
	}
	e.postCancelOpenCalls++
	if e.postCancelOpenCalls == 1 {
		return nil, nil
	}
	return []model.Order{e.resting}, nil
}

func (e *cancelAcknowledgedButOpenLifecycleExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	if query.ClientID == e.resting.Request.ClientID || query.VenueOrderID == e.resting.VenueOrderID {
		return &model.OrderStatusReport{AccountID: e.resting.Request.AccountID, Order: e.resting}, nil
	}
	return e.terminalFilledLifecycleExec.GenerateOrderStatusReport(context.Background(), query)
}

type sequenceAccountStateSource struct {
	mu     sync.Mutex
	states []model.AccountState
	calls  int
}

func (r *sequenceAccountStateSource) AccountState(context.Context) (model.AccountState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		return model.AccountState{}, errors.New("no account states configured")
	}
	index := r.calls
	if index >= len(r.states) {
		index = len(r.states) - 1
	}
	r.calls++
	return model.CloneAccountState(r.states[index]), nil
}

func spotBalanceState(accountID, venue, currency, total, borrowed string) model.AccountState {
	state := spotAccountState(accountID, venue)
	state.Balances = []model.AccountBalance{{
		AccountID: accountID,
		Currency:  currency,
		Total:     decimal.RequireFromString(total),
		Borrowed:  decimal.RequireFromString(borrowed),
	}}
	return state
}

func spotAccountState(accountID, venue string) model.AccountState {
	now := time.Unix(1, 0).UTC()
	return model.AccountState{
		AccountID: accountID,
		Venue:     venue,
		Type:      model.AccountMargin,
		Reported:  true,
		EventID:   model.AccountStateEventID(venue, accountID, now),
		TsEvent:   now,
		TsInit:    now,
	}
}

func guardedSpotLifecycleSpec(reporter accountStateSource) OrderLifecycleSpec {
	return guardedSpotLifecycleSpecWithRules(reporter, "0.995", "0.001", "0.001", "1", "0.005")
}

func guardedSpotLifecycleSpecWithRules(reporter accountStateSource, closeQty, step, minQty, minNotional, feeReserve string) OrderLifecycleSpec {
	spec := OrderLifecycleSpec{
		Label:          "guarded spot",
		Venue:          "TEST",
		AccountID:      "TEST:unified",
		InstrumentID:   model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindSpot},
		Quantity:       decimal.NewFromInt(1),
		CloseQuantity:  decimal.RequireFromString(closeQty),
		RestingPrice:   decimal.NewFromInt(90),
		FillPrice:      decimal.NewFromInt(101),
		ClosePrice:     decimal.NewFromInt(100),
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		PollInterval:   time.Millisecond,
		CleanupTimeout: 100 * time.Millisecond,
	}
	return ConfigureSpotBalanceGuard(
		spec,
		reporter,
		"BTC",
		decimal.RequireFromString(step),
		decimal.RequireFromString(minQty),
		decimal.RequireFromString(minNotional),
		decimal.RequireFromString(feeReserve),
	)
}

type ambiguousSpotCloseExec struct {
	recordingLifecycleExec
	err error
}

type spotCloseFailureCleanupExec struct {
	recordingLifecycleExec
	mu           sync.Mutex
	mode         string
	orders       map[string]model.Order
	exposure     decimal.Decimal
	sellAttempts int
	events       chan contract.ExecEnvelope
}

func newSpotCloseFailureCleanupExec(mode string) *spotCloseFailureCleanupExec {
	return &spotCloseFailureCleanupExec{
		mode:   mode,
		orders: make(map[string]model.Order),
		events: make(chan contract.ExecEnvelope, 8),
	}
}

func (e *spotCloseFailureCleanupExec) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.submits = append(e.submits, req)
	order := model.Order{
		Request:      req,
		VenueOrderID: fmt.Sprintf("spot-close-recovery-%d", len(e.submits)),
		Status:       enums.StatusNew,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if req.TIF == enums.TifGTX {
		e.orders[order.VenueOrderID] = order
		return &order, nil
	}
	if req.Side == enums.SideBuy {
		order.Status = enums.StatusFilled
		order.FilledQty = req.Quantity
		e.exposure = e.exposure.Add(req.Quantity)
		e.orders[order.VenueOrderID] = order
		e.events <- lifecycleFillEnvelope(order, order.FilledQty)
		return &order, nil
	}
	e.sellAttempts++
	if e.sellAttempts == 1 {
		switch e.mode {
		case "definitive_reject":
			return nil, errors.Join(errors.New("close definitive reject"), contract.ErrVenueRejected)
		case "terminal_zero":
			order.Status = enums.StatusCanceled
			e.orders[order.VenueOrderID] = order
			return &order, nil
		default:
			return nil, fmt.Errorf("unknown close failure mode %q", e.mode)
		}
	}
	order.Status = enums.StatusFilled
	order.FilledQty = req.Quantity
	e.exposure = e.exposure.Sub(req.Quantity)
	if e.exposure.IsNegative() {
		e.exposure = decimal.Zero
	}
	e.orders[order.VenueOrderID] = order
	e.events <- lifecycleFillEnvelope(order, order.FilledQty)
	return &order, nil
}

func (e *spotCloseFailureCleanupExec) Cancel(_ context.Context, _ model.InstrumentID, venueOrderID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cancelVenueOrderID = venueOrderID
	order, ok := e.orders[venueOrderID]
	if ok {
		order.Status = enums.StatusCanceled
		e.orders[venueOrderID] = order
	}
	return nil
}

func (e *spotCloseFailureCleanupExec) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var open []model.Order
	for _, order := range e.orders {
		if !orderstate.IsTerminal(order.Status) {
			open = append(open, order)
		}
	}
	return open, nil
}

func (e *spotCloseFailureCleanupExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, order := range e.orders {
		clientMatch := query.ClientID != "" && query.ClientID == order.Request.ClientID
		venueMatch := query.VenueOrderID != "" && query.VenueOrderID == order.VenueOrderID
		if !clientMatch && !venueMatch {
			continue
		}
		return &model.OrderStatusReport{AccountID: order.Request.AccountID, Order: order}, nil
	}
	return nil, nil
}

func (e *spotCloseFailureCleanupExec) GenerateFillReports(_ context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, order := range e.orders {
		clientMatch := query.ClientID != "" && query.ClientID == order.Request.ClientID
		venueMatch := query.VenueOrderID != "" && query.VenueOrderID == order.VenueOrderID
		if (!clientMatch && !venueMatch) || !order.FilledQty.IsPositive() {
			continue
		}
		return []model.FillReport{{
			AccountID: order.Request.AccountID,
			Fill: model.Fill{
				AccountID:    order.Request.AccountID,
				InstrumentID: order.Request.InstrumentID,
				ClientID:     order.Request.ClientID,
				VenueOrderID: order.VenueOrderID,
				TradeID:      "spot-close-" + order.VenueOrderID,
				Quantity:     order.FilledQty,
				Price:        order.Request.Price,
			},
		}}, nil
	}
	return nil, nil
}

func (e *spotCloseFailureCleanupExec) Events() <-chan contract.ExecEnvelope { return e.events }

func (e *spotCloseFailureCleanupExec) assertSingleSpotCleanup(t *testing.T) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.exposure.Equal(decimal.RequireFromString("0.005")) {
		t.Fatalf("exposure=%s, want accepted fee residual 0.005", e.exposure)
	}
	if len(e.submits) != 4 || e.sellAttempts != 2 {
		t.Fatalf("submits=%d sellAttempts=%d, want rest/open/failed-close/one-cleanup: %+v", len(e.submits), e.sellAttempts, e.submits)
	}
	cleanup := e.submits[3]
	if cleanup.Side != enums.SideSell || cleanup.ReduceOnly || !cleanup.Quantity.Equal(decimal.RequireFromString("0.995")) {
		t.Fatalf("cleanup=%+v, want one non-reduce-only Spot cleanup sell 0.995", cleanup)
	}
}

type spotCloseFailureBalanceReporter struct {
	exec *spotCloseFailureCleanupExec
}

func (r *spotCloseFailureBalanceReporter) AccountState(context.Context) (model.AccountState, error) {
	r.exec.mu.Lock()
	exposure := r.exec.exposure
	r.exec.mu.Unlock()
	return spotBalanceState("TEST:unified", "TEST", "BTC", decimal.NewFromInt(10).Add(exposure).String(), "0"), nil
}

func (e *ambiguousSpotCloseExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.TIF == enums.TifIOC && req.Side == enums.SideSell {
		e.submits = append(e.submits, req)
		return nil, e.err
	}
	return e.recordingLifecycleExec.Submit(ctx, req)
}

func (e *ambiguousSpotCloseExec) GenerateOrderStatusReport(context.Context, model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	return nil, nil
}

func (e *ambiguousSpotCloseExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	return nil, nil
}

type cleanupOpenOrdersExec struct {
	recordingLifecycleExec
	open     []model.Order
	canceled []string
}

func (e *cleanupOpenOrdersExec) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	return append([]model.Order(nil), e.open...), nil
}

func (e *cleanupOpenOrdersExec) Cancel(_ context.Context, _ model.InstrumentID, venueOrderID string) error {
	e.canceled = append(e.canceled, venueOrderID)
	kept := e.open[:0]
	for _, order := range e.open {
		if order.VenueOrderID != venueOrderID {
			kept = append(kept, order)
		}
	}
	e.open = kept
	return nil
}

func (e *cleanupOpenOrdersExec) GenerateOrderStatusReport(context.Context, model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	return nil, nil
}

func (e *cleanupOpenOrdersExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	return nil, nil
}

func (e *recordingLifecycleExec) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: "TEST",
		Reports: contract.ReportCapabilities{
			SingleOrderStatus: true,
			OpenOrders:        true,
			FillHistory:       true,
			PositionReports:   true,
		},
		Streaming: contract.StreamCapabilities{Execution: true},
		Trading:   contract.TradingCapabilities{Submit: true, Cancel: true, CancelAll: true, Modify: true},
	}
}

func (*recordingLifecycleExec) ValidateSubmit(model.OrderRequest) error { return nil }

func (e *recordingLifecycleExec) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submits = append(e.submits, req)
	venueID := fmt.Sprintf("venue-%d", len(e.submits))
	status := enums.StatusNew
	filled := decimal.Zero
	if len(e.submits) > 1 {
		status = enums.StatusFilled
		filled = req.Quantity
	}
	return &model.Order{
		Request:      req,
		VenueOrderID: venueID,
		Status:       status,
		FilledQty:    filled,
		AvgFillPrice: req.Price,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, nil
}

func (e *recordingLifecycleExec) Cancel(_ context.Context, _ model.InstrumentID, venueOrderID string) error {
	e.cancelVenueOrderID = venueOrderID
	return nil
}

func (e *recordingLifecycleExec) CancelAll(context.Context, model.InstrumentID) error { return nil }

func (e *recordingLifecycleExec) Modify(context.Context, model.InstrumentID, string, decimal.Decimal, decimal.Decimal) (*model.Order, error) {
	return nil, nil
}

func (e *recordingLifecycleExec) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	return nil, nil
}

func (e *recordingLifecycleExec) GenerateOrderStatusReports(context.Context, model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	return nil, nil
}

func (e *recordingLifecycleExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	status := enums.StatusFilled
	filledQty := decimal.RequireFromString("0.01")
	if query.VenueOrderID != "" && query.VenueOrderID == e.cancelVenueOrderID {
		status = enums.StatusCanceled
		filledQty = decimal.Zero
	}
	return &model.OrderStatusReport{Order: model.Order{
		Request: model.OrderRequest{
			AccountID:    query.AccountID,
			InstrumentID: query.InstrumentID,
			ClientID:     query.ClientID,
		},
		VenueOrderID: query.VenueOrderID,
		Status:       status,
		FilledQty:    filledQty,
	}}, nil
}

func (e *recordingLifecycleExec) GenerateFillReports(_ context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	if query.VenueOrderID != "" && query.VenueOrderID == e.cancelVenueOrderID {
		return nil, nil
	}
	return []model.FillReport{{Fill: model.Fill{
		AccountID:    query.AccountID,
		InstrumentID: query.InstrumentID,
		VenueOrderID: query.VenueOrderID,
		ClientID:     query.ClientID,
		TradeID:      "recording-" + query.VenueOrderID + query.ClientID,
		Quantity:     decimal.RequireFromString("0.01"),
		Price:        decimal.RequireFromString("51000"),
	}}}, nil
}

func (e *recordingLifecycleExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	return nil, nil
}

func (e *recordingLifecycleExec) GenerateExecutionMassStatus(_ context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	venue := strings.TrimSpace(query.Venue)
	if venue == "" {
		venue = "TEST"
	}
	accountID := strings.TrimSpace(query.AccountID)
	if accountID == "" {
		accountID = "TEST:unified"
	}
	now := time.Now()
	ids := model.NormalizeInstrumentIDs(query.InstrumentIDs)
	if ids == nil {
		ids = []model.InstrumentID{}
	}
	mass := model.NewExecutionMassStatus(venue, accountID, now)
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	mass.OpenOrdersCoverage = model.NewSnapshotCoverage(model.CoverageComplete, accountID, query.ClientID, ids, now)
	mass.FillsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	if query.IncludeFills {
		from := query.Since
		through := query.Until
		if through.IsZero() {
			through = now
		}
		if from.IsZero() && query.Lookback > 0 && !query.Until.IsZero() {
			from = query.Until.Add(-query.Lookback)
		}
		mass.FillsCoverage = model.NewFillCoverage(model.CoverageComplete, accountID, query.ClientID, ids, from, through)
	}
	mass.PositionsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	if query.IncludePositions {
		mass.PositionsCoverage = model.NewSnapshotCoverage(model.CoverageComplete, accountID, query.ClientID, ids, now)
	}
	if err := mass.ValidateFor(query); err != nil {
		return nil, err
	}
	return mass, nil
}

func (e *recordingLifecycleExec) Events() <-chan contract.ExecEnvelope { return nil }
func (e *recordingLifecycleExec) Close() error                         { return nil }

type mismatchedAccountLifecycleExec struct {
	recordingLifecycleExec
}

type wrongSubmitIdentityExec struct {
	recordingLifecycleExec
	mode string
}

type semanticMismatchSubmitExec struct {
	recordingLifecycleExec
	order    model.Order
	canceled []string
}

type malformedCleanupEvidenceExec struct {
	recordingLifecycleExec
	mode     string
	tracked  *trackedLifecycleOrder
	spec     OrderLifecycleSpec
	canceled []string
}

func (e *malformedCleanupEvidenceExec) Cancel(_ context.Context, _ model.InstrumentID, venueOrderID string) error {
	e.canceled = append(e.canceled, venueOrderID)
	return nil
}

func (e *malformedCleanupEvidenceExec) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	return nil, nil
}

func (e *malformedCleanupEvidenceExec) GenerateOrderStatusReport(context.Context, model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	if e.mode != "status_overfill" && e.mode != "status_overfill_discovered_venue" && e.mode != "status_overfill_discovered_venue_omitted_quantity" {
		return nil, nil
	}
	venueOrderID := e.tracked.venueOrderID
	status := enums.StatusFilled
	request := e.tracked.request
	if e.mode == "status_overfill_discovered_venue" || e.mode == "status_overfill_discovered_venue_omitted_quantity" {
		venueOrderID = "malformed-discovered-venue"
		status = enums.StatusNew
	}
	if e.mode == "status_overfill_discovered_venue_omitted_quantity" {
		request.Quantity = decimal.Zero
	}
	return &model.OrderStatusReport{AccountID: e.spec.AccountID, Order: model.Order{
		Request: request, VenueOrderID: venueOrderID,
		Status: status, FilledQty: e.spec.Quantity.Add(decimal.RequireFromString("0.001")),
	}}, nil
}

func (e *malformedCleanupEvidenceExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	if e.mode != "aggregate_fill_overfill" {
		return nil, nil
	}
	makeFill := func(tradeID string) model.FillReport {
		return model.FillReport{AccountID: e.spec.AccountID, Fill: model.Fill{
			AccountID: e.spec.AccountID, InstrumentID: e.spec.InstrumentID,
			ClientID: e.tracked.clientID, VenueOrderID: e.tracked.venueOrderID,
			TradeID: tradeID, Side: e.tracked.request.Side, Quantity: decimal.RequireFromString("0.006"), Price: e.spec.FillPrice,
		}}
	}
	return []model.FillReport{makeFill("malformed-1"), makeFill("malformed-2")}, nil
}

func (e *semanticMismatchSubmitExec) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submits = append(e.submits, req)
	e.order = model.Order{
		Request:      req,
		VenueOrderID: "semantic-venue-1",
		Status:       enums.StatusNew,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	e.order.Request.Quantity = req.Quantity.Add(decimal.RequireFromString("0.001"))
	return &e.order, nil
}

func (e *semanticMismatchSubmitExec) Cancel(_ context.Context, _ model.InstrumentID, venueOrderID string) error {
	e.canceled = append(e.canceled, venueOrderID)
	if venueOrderID == e.order.VenueOrderID {
		e.order.Status = enums.StatusCanceled
		e.order.UpdatedAt = time.Now()
	}
	return nil
}

func (e *semanticMismatchSubmitExec) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	if e.order.VenueOrderID == "" || definitiveLifecycleTerminal(e.order.Status) {
		return nil, nil
	}
	return []model.Order{e.order}, nil
}

func (e *semanticMismatchSubmitExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	if query.VenueOrderID != e.order.VenueOrderID || query.ClientID != e.order.Request.ClientID {
		return nil, nil
	}
	order := e.order
	return &model.OrderStatusReport{AccountID: order.Request.AccountID, Order: order}, nil
}

func (e *semanticMismatchSubmitExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	return nil, nil
}

func (e *wrongSubmitIdentityExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	order, err := e.recordingLifecycleExec.Submit(ctx, req)
	if err != nil {
		return order, err
	}
	switch e.mode {
	case "client", "order_and_error":
		order.Request.ClientID = "other-client"
	case "instrument":
		order.Request.InstrumentID.Symbol = "ETH-USDT"
	}
	if e.mode == "order_and_error" {
		return order, errors.New("submit response lost")
	}
	return order, nil
}

func (e *mismatchedAccountLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	order, err := e.recordingLifecycleExec.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	order.Request.AccountID = "TEST:other"
	return order, nil
}

type runtimeLifecycleExec struct {
	recordingLifecycleExec
	events     chan contract.ExecEnvelope
	venueToReq map[string]model.OrderRequest
}

type unrelatedStatusLifecycleExec struct {
	*runtimeLifecycleExec
}

func newUnrelatedStatusLifecycleExec() *unrelatedStatusLifecycleExec {
	return &unrelatedStatusLifecycleExec{runtimeLifecycleExec: newRuntimeLifecycleExec()}
}

func (e *unrelatedStatusLifecycleExec) GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	if query.VenueOrderID != "" && query.VenueOrderID == e.cancelVenueOrderID {
		req := e.venueToReq[query.VenueOrderID]
		return &model.OrderStatusReport{AccountID: query.AccountID, Order: model.Order{
			Request: req, VenueOrderID: "other-venue", Status: enums.StatusCanceled,
		}}, nil
	}
	return e.runtimeLifecycleExec.GenerateOrderStatusReport(ctx, query)
}

func (e *unrelatedStatusLifecycleExec) assertNoSell(t *testing.T) {
	t.Helper()
	for _, req := range e.submits {
		if req.Side == enums.SideSell {
			t.Fatalf("unrelated status authorized cleanup sell: %+v", req)
		}
	}
}

type preExistingRuntimeLifecycleExec struct {
	*runtimeLifecycleExec
	reports []model.PositionReport
}

func newPreExistingRuntimeLifecycleExec(reports []model.PositionReport) *preExistingRuntimeLifecycleExec {
	return &preExistingRuntimeLifecycleExec{runtimeLifecycleExec: newRuntimeLifecycleExec(), reports: reports}
}

func (e *preExistingRuntimeLifecycleExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	return e.reports, nil
}

func newRuntimeLifecycleExec() *runtimeLifecycleExec {
	return &runtimeLifecycleExec{
		events:     make(chan contract.ExecEnvelope, 16),
		venueToReq: make(map[string]model.OrderRequest),
	}
}

func (e *runtimeLifecycleExec) AccountID() string { return "TEST:unified" }

func (e *runtimeLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	order, err := e.recordingLifecycleExec.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	e.venueToReq[order.VenueOrderID] = order.Request
	if order.FilledQty.IsPositive() {
		e.events <- lifecycleFillEnvelope(*order, order.FilledQty)
	}
	return order, nil
}

func lifecycleFillEnvelope(order model.Order, quantity decimal.Decimal) contract.ExecEnvelope {
	timestamp := order.UpdatedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	return contract.NewExecEnvelope(contract.FillEvent{Fill: model.Fill{
		AccountID:    order.Request.AccountID,
		InstrumentID: order.Request.InstrumentID,
		VenueOrderID: order.VenueOrderID,
		ClientID:     order.Request.ClientID,
		TradeID:      order.VenueOrderID + "-fill",
		Side:         order.Request.Side,
		Price:        order.Request.Price,
		Quantity:     quantity,
		Timestamp:    timestamp,
	}})
}

func (e *runtimeLifecycleExec) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	if err := e.recordingLifecycleExec.Cancel(ctx, id, venueOrderID); err != nil {
		return err
	}
	req := e.venueToReq[venueOrderID]
	e.events <- contract.NewExecEnvelope(contract.OrderEvent{Order: model.Order{
		Request:      req,
		VenueOrderID: venueOrderID,
		Status:       enums.StatusCanceled,
		UpdatedAt:    time.Now(),
	}})
	return nil
}

func (e *runtimeLifecycleExec) Events() <-chan contract.ExecEnvelope { return e.events }
func (e *runtimeLifecycleExec) Close() error                         { close(e.events); return nil }

type lateFillRuntimeLifecycleExec struct {
	runtimeLifecycleExec
	lateFills atomic.Int32
}

type partialCloseRuntimeLifecycleExec struct {
	runtimeLifecycleExec
}

type partialOpeningLifecycleExec struct {
	recordingLifecycleExec
	partialQty decimal.Decimal
	position   decimal.Decimal
}

func (e *partialOpeningLifecycleExec) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submits = append(e.submits, req)
	order := &model.Order{
		Request:      req,
		VenueOrderID: fmt.Sprintf("partial-venue-%d", len(e.submits)),
		Status:       enums.StatusNew,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if req.TIF != enums.TifIOC {
		return order, nil
	}
	if req.Side == enums.SideBuy {
		order.Status = enums.StatusCanceled
		order.FilledQty = e.partialQty
		e.position = e.partialQty
		return order, nil
	}
	order.Status = enums.StatusFilled
	order.FilledQty = req.Quantity
	e.position = e.position.Sub(req.Quantity)
	return order, nil
}

func (e *partialOpeningLifecycleExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	if e.position.IsZero() {
		return nil, nil
	}
	return []model.PositionReport{{Position: model.Position{
		AccountID:    "TEST:unified",
		InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosNet,
		Quantity:     e.position,
	}}}, nil
}

type partialOpeningRuntimeLifecycleExec struct {
	runtimeLifecycleExec
	partialQty decimal.Decimal
	position   decimal.Decimal
}

func newPartialOpeningRuntimeLifecycleExec(partialQty decimal.Decimal) *partialOpeningRuntimeLifecycleExec {
	return &partialOpeningRuntimeLifecycleExec{runtimeLifecycleExec: *newRuntimeLifecycleExec(), partialQty: partialQty}
}

func (e *partialOpeningRuntimeLifecycleExec) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submits = append(e.submits, req)
	order := model.Order{
		Request:      req,
		VenueOrderID: fmt.Sprintf("runtime-partial-venue-%d", len(e.submits)),
		Status:       enums.StatusNew,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	e.venueToReq[order.VenueOrderID] = req
	if req.TIF == enums.TifIOC {
		if req.Side == enums.SideBuy {
			order.Status = enums.StatusCanceled
			order.FilledQty = e.partialQty
			e.position = e.partialQty
		} else {
			order.Status = enums.StatusFilled
			order.FilledQty = req.Quantity
			e.position = e.position.Sub(req.Quantity)
		}
		if order.FilledQty.IsPositive() {
			e.events <- lifecycleFillEnvelope(order, order.FilledQty)
		}
	}
	return &order, nil
}

func (e *partialOpeningRuntimeLifecycleExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	if e.position.IsZero() {
		return nil, nil
	}
	return []model.PositionReport{{Position: model.Position{
		AccountID:    "TEST:unified",
		InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosNet,
		Quantity:     e.position,
	}}}, nil
}

func newPartialCloseRuntimeLifecycleExec() *partialCloseRuntimeLifecycleExec {
	return &partialCloseRuntimeLifecycleExec{runtimeLifecycleExec: *newRuntimeLifecycleExec()}
}

func (e *partialCloseRuntimeLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	order, err := e.recordingLifecycleExec.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	if req.ReduceOnly {
		order.Status = enums.StatusCanceled
		order.FilledQty = req.Quantity.Div(decimal.NewFromInt(2))
	}
	e.venueToReq[order.VenueOrderID] = order.Request
	if order.FilledQty.IsPositive() {
		e.events <- lifecycleFillEnvelope(*order, order.FilledQty)
	}
	return order, nil
}

func newLateFillRuntimeLifecycleExec() *lateFillRuntimeLifecycleExec {
	return &lateFillRuntimeLifecycleExec{runtimeLifecycleExec: *newRuntimeLifecycleExec()}
}

func (e *lateFillRuntimeLifecycleExec) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submits = append(e.submits, req)
	venueID := fmt.Sprintf("late-venue-%d", len(e.submits))
	order := model.Order{
		Request:      req,
		VenueOrderID: venueID,
		Status:       enums.StatusNew,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	e.venueToReq[venueID] = req
	if req.TIF == enums.TifIOC {
		go func(order model.Order) {
			e.events <- contract.NewExecEnvelope(contract.OrderEvent{Order: model.Order{
				Request:      order.Request,
				VenueOrderID: order.VenueOrderID,
				Status:       enums.StatusNew,
				UpdatedAt:    time.Now(),
			}})
			time.Sleep(20 * time.Millisecond)
			e.lateFills.Add(1)
			e.events <- contract.NewExecEnvelope(contract.FillEvent{Fill: model.Fill{
				AccountID:    order.Request.AccountID,
				InstrumentID: order.Request.InstrumentID,
				VenueOrderID: order.VenueOrderID,
				ClientID:     order.Request.ClientID,
				TradeID:      order.VenueOrderID + "-fill",
				Side:         order.Request.Side,
				Price:        order.Request.Price,
				Quantity:     order.Request.Quantity,
				Timestamp:    time.Now(),
			}})
		}(order)
	}
	return &order, nil
}

type cleanupLifecycleExec struct {
	recordingLifecycleExec
	existing decimal.Decimal
}

type positionReportsLifecycleExec struct {
	recordingLifecycleExec
	reports []model.PositionReport
}

func (e *positionReportsLifecycleExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	return e.reports, nil
}

type accountPositionLifecycleExec struct {
	recordingLifecycleExec
	position                     decimal.Decimal
	executionPositionReportCalls atomic.Int32
}

func (e *accountPositionLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	order, err := e.recordingLifecycleExec.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	if req.TIF == enums.TifIOC {
		if req.Side == enums.SideBuy {
			e.position = e.position.Add(req.Quantity)
		} else if req.Side == enums.SideSell {
			e.position = e.position.Sub(req.Quantity)
		}
	}
	return order, nil
}

func (e *accountPositionLifecycleExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	e.executionPositionReportCalls.Add(1)
	return nil, errors.New("execution position reports are intentionally unsupported")
}

type lifecycleAccountPositionReporter struct {
	exec                *accountPositionLifecycleExec
	nonZeroObservations atomic.Int32
}

func (r *lifecycleAccountPositionReporter) Positions(context.Context) ([]model.Position, error) {
	if r.exec.position.IsZero() {
		return nil, nil
	}
	r.nonZeroObservations.Add(1)
	return []model.Position{{
		AccountID:    "TEST:unified",
		InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosNet,
		Quantity:     r.exec.position,
	}}, nil
}

type defaultPositionReporterExec struct {
	recordingLifecycleExec
	calls atomic.Int32
}

func (e *defaultPositionReporterExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	e.calls.Add(1)
	return []model.PositionReport{{Position: model.Position{
		AccountID:    "TEST:unified",
		InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosNet,
		Quantity:     decimal.RequireFromString("0.01"),
	}}}, nil
}

type closeFailureCleanupExec struct {
	recordingLifecycleExec
	existing           decimal.Decimal
	reduceOnlyAttempts int
	cancelAllCalls     int
}

type authoritativeTerminalZeroCloseExec struct {
	*runtimeLifecycleExec
	runtimeMode        bool
	closeReq           model.OrderRequest
	closeVenueOrderID  string
	exposure           decimal.Decimal
	reduceOnlyAttempts int
}

func newAuthoritativeTerminalZeroCloseExec(runtimeMode bool) *authoritativeTerminalZeroCloseExec {
	return &authoritativeTerminalZeroCloseExec{
		runtimeLifecycleExec: newRuntimeLifecycleExec(),
		runtimeMode:          runtimeMode,
	}
}

func (e *authoritativeTerminalZeroCloseExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.TIF == enums.TifIOC && req.Side == enums.SideSell && req.ReduceOnly {
		e.reduceOnlyAttempts++
		if e.reduceOnlyAttempts == 1 {
			e.submits = append(e.submits, req)
			e.closeReq = req
			e.closeVenueOrderID = "authoritative-zero-close"
			return nil, errors.New("close response lost")
		}
		order, err := e.runtimeLifecycleExec.Submit(ctx, req)
		if err == nil {
			e.exposure = decimal.Zero
		}
		return order, err
	}
	order, err := e.runtimeLifecycleExec.Submit(ctx, req)
	if err == nil && req.TIF == enums.TifIOC && req.Side == enums.SideBuy {
		e.exposure = req.Quantity
	}
	return order, err
}

func (e *authoritativeTerminalZeroCloseExec) GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	clientMatch := query.ClientID != "" && query.ClientID == e.closeReq.ClientID
	venueMatch := query.VenueOrderID != "" && query.VenueOrderID == e.closeVenueOrderID
	if e.closeReq.ClientID != "" && (clientMatch || venueMatch) {
		return &model.OrderStatusReport{AccountID: query.AccountID, Order: model.Order{
			Request:      e.closeReq,
			VenueOrderID: e.closeVenueOrderID,
			Status:       enums.StatusRejected,
			FilledQty:    decimal.Zero,
		}}, nil
	}
	return e.runtimeLifecycleExec.GenerateOrderStatusReport(ctx, query)
}

func (e *authoritativeTerminalZeroCloseExec) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	clientMatch := query.ClientID != "" && query.ClientID == e.closeReq.ClientID
	venueMatch := query.VenueOrderID != "" && query.VenueOrderID == e.closeVenueOrderID
	if e.closeReq.ClientID != "" && (clientMatch || venueMatch) {
		return nil, nil
	}
	return e.runtimeLifecycleExec.GenerateFillReports(ctx, query)
}

func (e *authoritativeTerminalZeroCloseExec) GeneratePositionReports(_ context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	if !e.exposure.IsPositive() {
		return nil, nil
	}
	return []model.PositionReport{{AccountID: query.AccountID, Position: model.Position{
		AccountID:    query.AccountID,
		InstrumentID: query.InstrumentID,
		Side:         enums.PosNet,
		Quantity:     e.exposure,
	}}}, nil
}

func (e *authoritativeTerminalZeroCloseExec) assertAuthoritativeZeroRetried(t *testing.T) {
	t.Helper()
	if e.reduceOnlyAttempts != 2 || !e.exposure.IsZero() {
		t.Fatalf("reduce-only attempts=%d exposure=%s, want one rejected close plus one cleanup flatten", e.reduceOnlyAttempts, e.exposure)
	}
	if len(e.submits) != 4 {
		t.Fatalf("submits=%d, want rest/open/rejected-close/cleanup: %+v", len(e.submits), e.submits)
	}
}

type ambiguousFillCleanupExec struct {
	recordingLifecycleExec
	ambiguous          atomic.Bool
	reduceOnlyAttempts atomic.Int32
}

func (e *ambiguousFillCleanupExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.ReduceOnly {
		e.reduceOnlyAttempts.Add(1)
		return nil, errors.New("unexpected reduce-only position cleanup")
	}
	if req.TIF == enums.TifIOC {
		e.ambiguous.Store(true)
		return nil, errors.New("ambiguous fill result")
	}
	return e.recordingLifecycleExec.Submit(ctx, req)
}

func (e *ambiguousFillCleanupExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	if !e.ambiguous.Load() {
		return nil, nil
	}
	return []model.PositionReport{{Position: model.Position{
		AccountID:    "TEST:unified",
		InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosLong,
		Quantity:     decimal.RequireFromString("0.001"),
	}}}, nil
}

func (e *ambiguousFillCleanupExec) GenerateOrderStatusReport(context.Context, model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	return nil, nil
}

func (e *ambiguousFillCleanupExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	return nil, nil
}

type ambiguousFillRuntimeCleanupExec struct {
	runtimeLifecycleExec
	ambiguous          atomic.Bool
	reduceOnlyAttempts atomic.Int32
}

func newAmbiguousFillRuntimeCleanupExec() *ambiguousFillRuntimeCleanupExec {
	return &ambiguousFillRuntimeCleanupExec{runtimeLifecycleExec: *newRuntimeLifecycleExec()}
}

func (e *ambiguousFillRuntimeCleanupExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.ReduceOnly {
		e.reduceOnlyAttempts.Add(1)
		return nil, errors.New("unexpected reduce-only position cleanup")
	}
	if req.TIF == enums.TifIOC {
		e.ambiguous.Store(true)
		order, err := e.recordingLifecycleExec.Submit(ctx, req)
		if err != nil {
			return nil, err
		}
		e.venueToReq[order.VenueOrderID] = order.Request
		order.Status = enums.StatusNew
		order.FilledQty = decimal.Zero
		return order, nil
	}
	return e.runtimeLifecycleExec.Submit(ctx, req)
}

func (e *ambiguousFillRuntimeCleanupExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	if !e.ambiguous.Load() {
		return nil, nil
	}
	return []model.PositionReport{{Position: model.Position{
		AccountID:    "TEST:unified",
		InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosLong,
		Quantity:     decimal.RequireFromString("0.001"),
	}}}, nil
}

func (e *ambiguousFillRuntimeCleanupExec) GenerateOrderStatusReport(context.Context, model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	return nil, nil
}

func (e *ambiguousFillRuntimeCleanupExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	return nil, nil
}

type oversizedCloseFailureCleanupExec struct {
	recordingLifecycleExec
	existing           decimal.Decimal
	exposureMultiplier decimal.Decimal
	reduceOnlyAttempts int
}

func (e *oversizedCloseFailureCleanupExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.ReduceOnly {
		e.reduceOnlyAttempts++
		return nil, errors.Join(errors.New("forced close failure"), contract.ErrVenueRejected)
	}
	order, err := e.recordingLifecycleExec.Submit(ctx, req)
	if err == nil && req.TIF == enums.TifIOC {
		multiplier := e.exposureMultiplier
		if !multiplier.IsPositive() {
			multiplier = decimal.NewFromInt(2)
		}
		e.existing = req.Quantity.Mul(multiplier)
	}
	return order, err
}

func (e *oversizedCloseFailureCleanupExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	if e.existing.IsZero() {
		return nil, nil
	}
	return []model.PositionReport{{Position: model.Position{
		AccountID:    "TEST:unified",
		InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosLong,
		Quantity:     e.existing,
	}}}, nil
}

type hedgedCloseFailureCleanupExec struct {
	recordingLifecycleExec
	opened             bool
	reduceOnlyAttempts int
}

type shortCloseFailureCleanupExec struct {
	recordingLifecycleExec
	opened             bool
	reduceOnlyAttempts int
}

func (e *shortCloseFailureCleanupExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.ReduceOnly {
		e.reduceOnlyAttempts++
		return nil, errors.Join(errors.New("forced close failure"), contract.ErrVenueRejected)
	}
	order, err := e.recordingLifecycleExec.Submit(ctx, req)
	if err == nil && req.TIF == enums.TifIOC {
		e.opened = true
	}
	return order, err
}

func (e *shortCloseFailureCleanupExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	if !e.opened {
		return nil, nil
	}
	return []model.PositionReport{{Position: model.Position{
		AccountID:    "TEST:unified",
		InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosShort,
		Quantity:     decimal.RequireFromString("-0.005"),
	}}}, nil
}

func (e *hedgedCloseFailureCleanupExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.ReduceOnly {
		e.reduceOnlyAttempts++
		return nil, errors.Join(errors.New("forced close failure"), contract.ErrVenueRejected)
	}
	order, err := e.recordingLifecycleExec.Submit(ctx, req)
	if err == nil && req.TIF == enums.TifIOC {
		e.opened = true
	}
	return order, err
}

func (e *hedgedCloseFailureCleanupExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	if !e.opened {
		return nil, nil
	}
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	return []model.PositionReport{
		{Position: model.Position{AccountID: "TEST:unified", InstrumentID: id, Side: enums.PosLong, Quantity: decimal.RequireFromString("0.005")}},
		{Position: model.Position{AccountID: "TEST:unified", InstrumentID: id, Side: enums.PosShort, Quantity: decimal.RequireFromString("-0.005")}},
	}, nil
}

func (e *closeFailureCleanupExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.ReduceOnly {
		e.reduceOnlyAttempts++
		if e.reduceOnlyAttempts == 1 {
			return nil, errors.New("forced close failure")
		}
		e.existing = decimal.Zero
	}
	order, err := e.recordingLifecycleExec.Submit(ctx, req)
	if err == nil && req.TIF == enums.TifIOC && !req.ReduceOnly {
		e.existing = req.Quantity
	}
	return order, err
}

func (e *closeFailureCleanupExec) CancelAll(context.Context, model.InstrumentID) error {
	e.cancelAllCalls++
	return nil
}

func (e *closeFailureCleanupExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	if e.existing.IsZero() {
		return nil, nil
	}
	return []model.PositionReport{{Position: model.Position{
		AccountID:    "TEST:unified",
		InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosLong,
		Quantity:     e.existing,
	}}}, nil
}

func (e *closeFailureCleanupExec) GenerateOrderStatusReport(context.Context, model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	return nil, nil
}

func (e *closeFailureCleanupExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	return nil, nil
}

type slowFillLifecycleExec struct {
	recordingLifecycleExec
	fillReportCalls int
}

func (e *slowFillLifecycleExec) Submit(_ context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submits = append(e.submits, req)
	status := enums.StatusNew
	if req.TIF == enums.TifGTX {
		status = enums.StatusNew
	}
	return &model.Order{
		Request:      req,
		VenueOrderID: fmt.Sprintf("slow-venue-%d", len(e.submits)),
		Status:       status,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, nil
}

func (e *slowFillLifecycleExec) GenerateOrderStatusReport(_ context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	status := enums.StatusNew
	if query.VenueOrderID != "" && query.VenueOrderID == e.cancelVenueOrderID {
		status = enums.StatusCanceled
	}
	return &model.OrderStatusReport{Order: model.Order{
		Request: model.OrderRequest{
			AccountID:    query.AccountID,
			InstrumentID: query.InstrumentID,
			ClientID:     query.ClientID,
		},
		VenueOrderID: query.VenueOrderID,
		Status:       status,
	}}, nil
}

func (e *slowFillLifecycleExec) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	if query.VenueOrderID != "" && query.VenueOrderID == e.cancelVenueOrderID {
		return nil, nil
	}
	e.fillReportCalls++
	if query.VenueOrderID != "" && query.ClientID != "" {
		return nil, errors.New("fill query must prefer venue order id over local client id")
	}
	if e.fillReportCalls == 1 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []model.FillReport{{Fill: model.Fill{
		AccountID:    query.AccountID,
		InstrumentID: query.InstrumentID,
		VenueOrderID: query.VenueOrderID,
		ClientID:     query.ClientID,
		TradeID:      "slow-fill-" + query.VenueOrderID + query.ClientID,
		Quantity:     decimal.RequireFromString("0.01"),
		Price:        decimal.RequireFromString("51000"),
	}}}, nil
}

func (e *cleanupLifecycleExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.ReduceOnly {
		e.existing = decimal.Zero
	}
	e.submits = append(e.submits, req)
	venueID := fmt.Sprintf("venue-%d", len(e.submits))
	status := enums.StatusNew
	filled := decimal.Zero
	if req.TIF == enums.TifIOC || req.ReduceOnly {
		status = enums.StatusFilled
		filled = req.Quantity
	}
	return &model.Order{
		Request:      req,
		VenueOrderID: venueID,
		Status:       status,
		FilledQty:    filled,
		AvgFillPrice: req.Price,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}, nil
}

func (e *cleanupLifecycleExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	if e.existing.IsZero() {
		return nil, nil
	}
	return []model.PositionReport{{Position: model.Position{
		AccountID:    "TEST:unified",
		InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosLong,
		Quantity:     e.existing,
	}}}, nil
}

func TestEnsureTrackedOrderSemanticsVenuePriceImprovementPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		side      enums.OrderSide
		requested string
		venue     string
		allow     bool
		wantErr   bool
	}{
		{name: "strict default rejects changed sell price", side: enums.SideSell, requested: "100", venue: "101", wantErr: true},
		{name: "sell improvement allowed", side: enums.SideSell, requested: "100", venue: "101", allow: true},
		{name: "sell deterioration rejected", side: enums.SideSell, requested: "100", venue: "99", allow: true, wantErr: true},
		{name: "buy improvement allowed", side: enums.SideBuy, requested: "100", venue: "99", allow: true},
		{name: "buy deterioration rejected", side: enums.SideBuy, requested: "100", venue: "101", allow: true, wantErr: true},
		{name: "unchanged price remains valid", side: enums.SideBuy, requested: "100", venue: "100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracked := &trackedLifecycleOrder{request: model.OrderRequest{
				Side:  tc.side,
				Price: decimal.RequireFromString(tc.requested),
			}}
			order := &model.Order{Request: model.OrderRequest{
				Side:  tc.side,
				Price: decimal.RequireFromString(tc.venue),
			}}
			err := ensureTrackedOrderSemantics(OrderLifecycleSpec{AllowVenuePriceImprovement: tc.allow}, "order", tracked, order)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ensureTrackedOrderSemantics error=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
