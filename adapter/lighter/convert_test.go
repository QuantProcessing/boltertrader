package lighter

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

func TestBuildRegistryInstrumentsIncludesPerpAndSpot(t *testing.T) {
	details := &sdk.OrderBookDetailsResponse{
		Code: 200,
		OrderBookDetails: []*sdk.OrderBookDetail{{
			Symbol:                 "ETH",
			MarketId:               0,
			MarketType:             string(sdk.MarketTypePerp),
			MinBaseAmount:          "0.0050",
			MinQuoteAmount:         "10.000000",
			SupportedSizeDecimals:  4,
			SupportedPriceDecimals: 2,
			SizeDecimals:           4,
			PriceDecimals:          2,
		}},
		SpotOrderBookDetails: []*sdk.OrderBookDetail{{
			Symbol:                 "ETH/USDC",
			MarketId:               2048,
			MarketType:             string(sdk.MarketTypeSpot),
			MinBaseAmount:          "0.0050",
			MinQuoteAmount:         "10.000000",
			SupportedSizeDecimals:  4,
			SupportedPriceDecimals: 2,
			SizeDecimals:           4,
			PriceDecimals:          2,
		}},
	}

	registry, err := newRegistryFromOrderBookDetails(details)
	if err != nil {
		t.Fatalf("newRegistryFromOrderBookDetails: %v", err)
	}

	perpID := model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindPerp}
	spotID := model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindSpot}
	perp, ok := registry.Instrument(perpID)
	if !ok {
		t.Fatalf("missing perp instrument %s", perpID)
	}
	spot, ok := registry.Instrument(spotID)
	if !ok {
		t.Fatalf("missing spot instrument %s", spotID)
	}
	if perp.AssetIndex == nil || *perp.AssetIndex != 0 || perp.VenueSymbol != "ETH" {
		t.Fatalf("unexpected perp identity: %+v", perp)
	}
	if spot.AssetIndex == nil || *spot.AssetIndex != 2048 || spot.VenueSymbol != "ETH/USDC" {
		t.Fatalf("unexpected spot identity: %+v", spot)
	}
	if !perp.PriceTick.Equal(decimal.RequireFromString("0.01")) || !perp.SizeStep.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("unexpected perp precision: %+v", perp)
	}
}

func TestAccountStateFromLighterAccountIsUnifiedMargin(t *testing.T) {
	now := time.Unix(100, 0)
	acct := &sdk.Account{
		AccountIndex:              66,
		Status:                    1,
		AvailableBalance:          "9990.000000",
		Collateral:                "10000.000000",
		TotalAssetValue:           "10000",
		CrossAssetValue:           "10000",
		CrossInitialMarginReq:     "10.000000",
		CrossMaintenanceMarginReq: "2.000000",
		AccountTradingMode:        1,
		Assets: []*sdk.SpotAsset{
			{Symbol: "ETH", Balance: "3.00000000", LockedBalance: "0.50000000", MarginMode: "disabled", MarginBalance: "0.00000000"},
			{Symbol: "USDC", Balance: "0.000000", LockedBalance: "0.000000", MarginMode: "enabled", MarginBalance: "10000.000000"},
		},
	}

	state := accountStateFromLighterAccount(acct, model.AccountIDLighterDefault, now)

	if state.AccountID != model.AccountIDLighterDefault || state.Type != model.AccountMargin || state.BaseCurrency != "USDC" {
		t.Fatalf("unexpected state identity: %+v", state)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("state should validate: %v", err)
	}
	if !state.Reported || state.EventID == "" || state.TsEvent.IsZero() || state.TsInit.IsZero() {
		t.Fatalf("account state envelope incomplete: %+v", state)
	}
	if got := state.Balances[1].Total; !got.Equal(decimal.RequireFromString("10000")) {
		t.Fatalf("USDC total=%s, want 10000", got)
	}
	if got := state.Margins[0].Initial; !got.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("initial margin=%s, want 10", got)
	}
}

func TestPlaceOrderRequestQuantizesTicksAndAccountID(t *testing.T) {
	inst := &model.Instrument{
		ID:           model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindPerp},
		VenueSymbol:  "ETH",
		AssetIndex:   intPtr(0),
		PriceTick:    decimal.RequireFromString("0.01"),
		SizeStep:     decimal.RequireFromString("0.0001"),
		MinQty:       decimal.RequireFromString("0.0050"),
		MinNotional:  decimal.RequireFromString("10"),
		PositionMode: model.NetOnly,
	}
	provider := newRegistry([]*model.Instrument{inst})
	now := time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC)
	exec := newExecutionClient(nil, provider, clock.NewSimulatedClock(now), 66)

	req, index, err := exec.placeOrderRequest(model.OrderRequest{
		AccountID:    model.AccountIDLighterDefault,
		InstrumentID: inst.ID,
		ClientID:     "lighter-test-order",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     decimal.RequireFromString("0.0063"),
		Price:        decimal.RequireFromString("1683.31"),
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("placeOrderRequest: %v", err)
	}
	if req.MarketId != 0 || req.Price != 168331 || req.BaseAmount != 63 || req.IsAsk != 0 ||
		req.OrderType != sdk.OrderTypeLimit || req.TimeInForce != sdk.OrderTimeInForcePostOnly {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.OrderExpiry != now.Add(28*24*time.Hour).UnixMilli() {
		t.Fatalf("order expiry=%d, want now+28d", req.OrderExpiry)
	}
	if index == 0 {
		t.Fatalf("client order index should be non-zero")
	}

	_, _, err = exec.placeOrderRequest(model.OrderRequest{
		AccountID:    "LIGHTER-OTHER",
		InstrumentID: inst.ID,
		ClientID:     "wrong-account",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.RequireFromString("0.0063"),
		Price:        decimal.RequireFromString("1683.31"),
		PositionSide: enums.PosNet,
	})
	if err == nil {
		t.Fatalf("expected account id mismatch")
	}
}

func TestReportsRejectMismatchedAccountIDBeforeREST(t *testing.T) {
	inst := &model.Instrument{
		ID:         model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindPerp},
		AssetIndex: intPtr(0),
		PriceTick:  decimal.RequireFromString("0.01"),
		SizeStep:   decimal.RequireFromString("0.0001"),
	}
	exec := newExecutionClient(nil, newRegistry([]*model.Instrument{inst}), clock.NewRealClock(), 66)

	orders, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{AccountID: "LIGHTER-OTHER", InstrumentID: inst.ID})
	if err != nil || len(orders) != 0 {
		t.Fatalf("mismatched account order reports=%+v err=%v, want empty nil", orders, err)
	}
	order, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{AccountID: "LIGHTER-OTHER", InstrumentID: inst.ID, ClientID: "client"})
	if err != nil || order != nil {
		t.Fatalf("mismatched account single order=%+v err=%v, want nil nil", order, err)
	}
	fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: "LIGHTER-OTHER", InstrumentID: inst.ID})
	if err != nil || len(fills) != 0 {
		t.Fatalf("mismatched account fill reports=%+v err=%v, want empty nil", fills, err)
	}
	positions, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{AccountID: "LIGHTER-OTHER", InstrumentID: inst.ID})
	if err != nil || len(positions) != 0 {
		t.Fatalf("mismatched account position reports=%+v err=%v, want empty nil", positions, err)
	}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: "LIGHTER-OTHER", IncludeFills: true, IncludePositions: true})
	if err != nil || mass == nil || mass.AccountID != "LIGHTER-OTHER" || len(mass.OrderReports) != 0 || len(mass.FillReports) != 0 || len(mass.PositionReports) != 0 {
		t.Fatalf("mismatched account mass=%+v err=%v, want empty LIGHTER-OTHER mass", mass, err)
	}
}

func TestClientOrderIndexFitsLighterVenueRange(t *testing.T) {
	for n := 0; n < 512; n++ {
		got := clientOrderIndex("ORDER-" + decimal.NewFromInt(int64(n)).String())
		if got <= 0 || got > 0x7fff_ffff {
			t.Fatalf("client order index=%d outside Lighter 31-bit venue range", got)
		}
	}
}

func TestOrderFromLighterPrefersMappedClientOrderID(t *testing.T) {
	inst := &model.Instrument{
		ID:         model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindSpot},
		AssetIndex: intPtr(2048),
		PriceTick:  decimal.RequireFromString("0.01"),
		SizeStep:   decimal.RequireFromString("0.0001"),
	}
	exec := newExecutionClient(nil, newRegistry([]*model.Instrument{inst}), nil, 66)
	exec.rememberClientIndex(42, "runtime-client-id")

	order := exec.orderFromLighter(&sdk.Order{
		MarketIndex:       2048,
		OrderIndex:        1001,
		ClientOrderIndex:  42,
		ClientOrderId:     "42",
		InitialBaseAmount: "0.0100",
		Price:             "100.00",
		Status:            sdk.OrderStatusOpen,
	})

	if order.Request.ClientID != "runtime-client-id" {
		t.Fatalf("client id=%q, want mapped runtime id", order.Request.ClientID)
	}
}
