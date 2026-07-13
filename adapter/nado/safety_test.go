package nado

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func TestNadoSubmitFailsClosedUntilPreparedValidationExists(t *testing.T) {
	provider := nadoTestProvider()
	exec := newExecutionClient(nil, provider, clock.NewRealClock(), enums.KindSpot, AccountIDUnified)
	spotID := model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT0", Kind: enums.KindSpot}
	req := model.OrderRequest{
		AccountID:    AccountIDUnified,
		InstrumentID: spotID,
		ClientID:     "not-prepared",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.RequireFromString("0.001"),
		Price:        decimal.RequireFromString("1000"),
		PositionSide: enums.PosNet,
	}
	if _, err := exec.Submit(context.Background(), req); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("unprepared Submit err=%v, want ErrNotSupported", err)
	}
	if exec.Capabilities().Trading.Submit {
		t.Fatal("Submit capability must remain false until Story 6 prepared validation is implemented")
	}
}

func TestNadoLocalValidationRejectsScopeAccountAndOffStep(t *testing.T) {
	provider := nadoTestProvider()
	exec := newExecutionClient(nil, provider, clock.NewRealClock(), enums.KindSpot, AccountIDUnified)
	spotID := model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT0", Kind: enums.KindSpot}
	base := model.OrderRequest{
		AccountID:    AccountIDUnified,
		InstrumentID: spotID,
		ClientID:     "local-check",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.RequireFromString("0.001"),
		Price:        decimal.RequireFromString("1000"),
		PositionSide: enums.PosNet,
	}

	wrongAccount := base
	wrongAccount.AccountID = "NADO-OTHER"
	if err := exec.validateOrderRequest(wrongAccount); !errors.Is(err, ErrAccountMismatch) {
		t.Fatalf("account mismatch err=%v", err)
	}

	wrongKind := base
	wrongKind.InstrumentID = model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
	if err := exec.validateOrderRequest(wrongKind); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("product scope err=%v", err)
	}

	offStep := base
	offStep.Quantity = decimal.RequireFromString("0.00101")
	if err := exec.validateOrderRequest(offStep); err == nil {
		t.Fatal("off-step quantity must fail instead of rounding")
	}
}

func TestNadoExecutionRejectsOutOfScopeOperationsBeforeTransport(t *testing.T) {
	provider := nadoTestProvider()
	exec := newExecutionClient(nil, provider, clock.NewRealClock(), enums.KindSpot, AccountIDUnified)
	perpID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}

	assertOutOfScope := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, contract.ErrNotSupported) || !strings.Contains(err.Error(), "outside adapter scope") {
			t.Fatalf("err=%v, want product-scope rejection", err)
		}
	}
	if _, _, err := exec.instrument(perpID); err == nil {
		t.Fatal("instrument lookup accepted an out-of-scope Perp instrument")
	} else {
		assertOutOfScope(t, err)
	}

	tests := map[string]func() error{
		"cancel": func() error {
			return exec.Cancel(context.Background(), perpID, "0xdigest")
		},
		"cancel all": func() error {
			return exec.CancelAll(context.Background(), perpID)
		},
		"open orders": func() error {
			_, err := exec.OpenOrders(context.Background(), perpID)
			return err
		},
		"order reports": func() error {
			_, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{InstrumentID: perpID})
			return err
		},
		"single order report": func() error {
			_, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{InstrumentID: perpID})
			return err
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) { assertOutOfScope(t, run()) })
	}
}

func TestNadoMassStatusSkipsInstrumentsOutsideConfiguredProductScope(t *testing.T) {
	provider, err := newInstrumentProviderFromDiscovery(nadoTestProducts(), nadoTestSymbols(), []enums.InstrumentKind{enums.KindPerp})
	if err != nil {
		t.Fatal(err)
	}
	exec := newExecutionClient(nil, provider, clock.NewRealClock(), enums.KindSpot, AccountIDUnified)
	if _, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{}); err != nil {
		t.Fatalf("Spot mass status queried an out-of-scope Perp instrument: %v", err)
	}
}

func TestNadoAccountConversionRejectsMalformedRequiredEvidence(t *testing.T) {
	provider := nadoTestProvider()
	now := time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)
	account := sdk.AccountInfo{Exists: true, Healths: []sdk.Health{
		{Assets: "1000000000000000000000", Liabilities: "0", Health: "bad"},
		{Assets: "1000000000000000000000", Liabilities: "0", Health: "850000000000000000000"},
		{Assets: "1000000000000000000000", Liabilities: "0", Health: "900000000000000000000"},
	}}
	if _, err := accountStateFromNado(&sdk.AccountSnapshot{Account: account, ReceivedAt: now}, provider, AccountIDUnified, now); err == nil {
		t.Fatal("malformed initial health must fail account readiness")
	}

	account.Healths[0].Health = "800000000000000000000"
	account.SpotBalances = []sdk.Balance{{ProductID: 999}}
	account.SpotBalances[0].Balance.Amount = "1"
	if _, err := accountStateFromNado(&sdk.AccountSnapshot{Account: account, ReceivedAt: now}, provider, AccountIDUnified, now); err == nil {
		t.Fatal("unknown spot product must fail account readiness")
	}
}

func TestNadoAccountConversionClampsNegativeAvailableCollateral(t *testing.T) {
	provider := nadoTestProvider()
	now := time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)
	account := sdk.AccountInfo{Exists: true, Healths: []sdk.Health{
		{Assets: "0", Liabilities: "1000000000000000000", Health: "-1000000000000000000"},
		{Assets: "1000000000000000000", Liabilities: "0", Health: "1000000000000000000"},
		{Assets: "1000000000000000000", Liabilities: "0", Health: "-2000000000000000000"},
	}}
	state, err := accountStateFromNado(&sdk.AccountSnapshot{Account: account, ReceivedAt: now}, provider, AccountIDUnified, now)
	if err != nil {
		t.Fatalf("negative initial health should clamp, not fail: %v", err)
	}
	if state.Summary == nil || !state.Summary.AvailableCollateral.IsZero() || !state.Summary.Equity.Equal(decimal.RequireFromString("-2")) {
		t.Fatalf("health clamp/equity semantics mismatch: %+v", state.Summary)
	}
}

func TestNadoAccountConversionRejectsNegativeHealthAssetsAndLiabilities(t *testing.T) {
	provider := nadoTestProvider()
	now := time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)
	account := sdk.AccountInfo{Exists: true, Healths: []sdk.Health{
		{Assets: "1000000000000000000", Liabilities: "-1000000000000000000", Health: "2000000000000000000"},
		{Assets: "1000000000000000000", Liabilities: "0", Health: "1000000000000000000"},
		{Assets: "1000000000000000000", Liabilities: "0", Health: "1000000000000000000"},
	}}
	if _, err := accountStateFromNado(&sdk.AccountSnapshot{Account: account, ReceivedAt: now}, provider, AccountIDUnified, now); err == nil {
		t.Fatal("negative health liability must fail account readiness")
	}

	account.Healths[0].Liabilities = "0"
	account.Healths[0].Assets = "-1000000000000000000"
	if _, err := accountStateFromNado(&sdk.AccountSnapshot{Account: account, ReceivedAt: now}, provider, AccountIDUnified, now); err == nil {
		t.Fatal("negative health assets must fail account readiness")
	}
}

func TestNadoAppendixReduceOnlyAndPartialOrderStatus(t *testing.T) {
	record := sdk.Order{
		Amount:         "1000000000000000000",
		UnfilledAmount: "500000000000000000",
		PriceX18:       "1000000000000000000",
		Appendix:       "2049",
		Digest:         "0xabc",
		PlacedAt:       1700000000000,
		OrderType:      string(sdk.OrderTypeLimit),
	}
	order, err := orderFromNadoRecord(record, model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}, AccountIDUnified)
	if err != nil {
		t.Fatalf("order conversion: %v", err)
	}
	if !order.Request.ReduceOnly || order.Status != enums.StatusPartiallyFilled {
		t.Fatalf("reduce-only/partial status lost: %+v", order)
	}
}

func TestNadoRejectsInjectedClientFromDifferentEnvironment(t *testing.T) {
	mainnet, err := sdk.NewProfile(sdk.EnvironmentMainnet)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sdk.NewClient(mainnet)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(context.Background(), Config{Environment: sdk.EnvironmentTestnet, Client: client, ProductKind: enums.KindSpot})
	if err == nil {
		t.Fatal("New accepted mainnet client under Testnet configuration")
	}
}
