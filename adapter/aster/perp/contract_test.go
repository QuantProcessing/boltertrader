package perp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
	"github.com/shopspring/decimal"
)

var (
	_ contract.MarketDataClient              = (*marketDataClient)(nil)
	_ contract.DerivativeReferenceDataClient = (*marketDataClient)(nil)
	_ contract.OpenInterestClient            = (*marketDataClient)(nil)
	_ contract.ExecutionClient               = (*executionClient)(nil)
	_ contract.AccountClient                 = (*accountClient)(nil)
	_ contract.AccountStateReporter          = (*accountClient)(nil)
	_ contract.AccountIDProvider             = (*executionClient)(nil)
	_ contract.AccountIDProvider             = (*accountClient)(nil)
	_ model.InstrumentProvider               = (*instrumentProvider)(nil)
)

func TestDefaultAndCustomAccountIDPropagation(t *testing.T) {
	provider := newInstrumentProvider()
	inst := mustPerpInstrument(t)
	provider.LoadSnapshot([]*model.Instrument{inst})

	exec := newExecutionClient(nil, provider, clock.NewRealClock(), "")
	if exec.AccountID() != AccountIDDefault {
		t.Fatalf("default account id=%q, want %q", exec.AccountID(), AccountIDDefault)
	}
	if AccountIDDefault != model.AccountIDAsterDefault {
		t.Fatalf("AccountIDDefault=%q, want model.AccountIDAsterDefault %q", AccountIDDefault, model.AccountIDAsterDefault)
	}
	order := orderFromResponse(&sdkperp.OrderResponse{
		Symbol: "BTCUSDT", OrderID: 42, ClientOrderID: "c1", Status: "NEW", Type: "LIMIT", Side: "SELL", TimeInForce: "GTC", OrigQty: "0.25", Price: "60000", ReduceOnly: true,
	}, model.OrderRequest{InstrumentID: inst.ID}, "ASTER-CUSTOM")
	if order.Request.AccountID != "ASTER-CUSTOM" || !order.Request.ReduceOnly {
		t.Fatalf("custom account/reduce-only not propagated: %#v", order)
	}

	state := accountStateFromResponse(&sdkperp.AccountResponse{
		UpdateTime:         1700000000000,
		AvailableBalance:   "88",
		TotalWalletBalance: "100",
		Assets:             []sdkperp.AccountAsset{{Asset: "USDT", WalletBalance: "100", AvailableBalance: "88", InitialMargin: "10", MaintMargin: "2", UpdateTime: 1700000000000}},
	}, "ASTER-CUSTOM", clock.NewRealClock().Now())
	if state.AccountID != "ASTER-CUSTOM" || state.Type != model.AccountMargin || state.BaseCurrency != "USDT" || state.Balances[0].AccountID != "ASTER-CUSTOM" {
		t.Fatalf("custom account id not propagated through state: %#v", state)
	}
}

func TestValidateSubmitRejectsInvalidPerpRequestsBeforeREST(t *testing.T) {
	inst := mustPerpInstrument(t)
	exec := newExecutionClient(perpClientNoNetwork(t), testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	valid := model.OrderRequest{
		AccountID:    AccountIDDefault,
		InstrumentID: inst.ID,
		ClientID:     "c-valid",
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1.23"),
		Price:        d("10.0000"),
		PositionSide: enums.PosNet,
		ReduceOnly:   true,
	}
	if err := exec.ValidateSubmit(valid); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	cases := map[string]model.OrderRequest{
		"account mismatch":       withPerp(valid, func(r *model.OrderRequest) { r.AccountID = "OTHER" }),
		"zero quantity":          withPerp(valid, func(r *model.OrderRequest) { r.Quantity = decimal.Zero }),
		"negative quantity":      withPerp(valid, func(r *model.OrderRequest) { r.Quantity = d("-1") }),
		"limit missing price":    withPerp(valid, func(r *model.OrderRequest) { r.Price = decimal.Zero }),
		"limit non tick price":   withPerp(valid, func(r *model.OrderRequest) { r.Price = d("10.00001") }),
		"non step quantity":      withPerp(valid, func(r *model.OrderRequest) { r.Quantity = d("1.235") }),
		"below minimum quantity": withPerp(valid, func(r *model.OrderRequest) { r.Quantity = d("0.001") }),
		"below minimum notional": withPerp(valid, func(r *model.OrderRequest) { r.Quantity = d("0.01") }),
		"wrong instrument kind":  withPerp(valid, func(r *model.OrderRequest) { r.InstrumentID.Kind = enums.KindSpot }),
		"market post only":       withPerp(valid, func(r *model.OrderRequest) { r.Type = enums.TypeMarket; r.TIF = enums.TifGTX; r.Price = decimal.Zero }),
		"venue options":          withPerp(valid, func(r *model.OrderRequest) { r.Venue = &model.VenueOrderOpts{Native: struct{}{}} }),
		"hedge position side":    withPerp(valid, func(r *model.OrderRequest) { r.PositionSide = enums.PosLong }),
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if err := exec.ValidateSubmit(req); err == nil {
				t.Fatalf("ValidateSubmit accepted invalid request")
			}
			if _, err := exec.Submit(context.Background(), req); err == nil {
				t.Fatalf("Submit accepted invalid request")
			}
		})
	}
}

func TestSubmitRejectsMalformedRequiredOrderResponseDecimal(t *testing.T) {
	inst := mustPerpInstrument(t)
	exec := newExecutionClient(perpClientResponse(t, `{"symbol":"ASTERUSDT","orderId":42,"clientOrderId":"c-bad","status":"NEW","type":"LIMIT","side":"SELL","positionSide":"BOTH","timeInForce":"GTC","origQty":"not-decimal","price":"1.2345","executedQty":"0","cumQuote":"0","reduceOnly":true}`), testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	_, err := exec.Submit(context.Background(), model.OrderRequest{
		AccountID:    AccountIDDefault,
		InstrumentID: inst.ID,
		ClientID:     "c-bad",
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1.23"),
		Price:        d("10.0000"),
		PositionSide: enums.PosNet,
		ReduceOnly:   true,
	})
	if err == nil {
		t.Fatalf("Submit accepted malformed required response decimal")
	}
}

func TestInstrumentConversionUsesExactDecimalIncrements(t *testing.T) {
	inst := mustPerpInstrument(t)
	if inst.ID != (model.InstrumentID{Venue: VenueName, Symbol: "ASTER-USDT", Kind: enums.KindPerp}) {
		t.Fatalf("id=%+v", inst.ID)
	}
	assertDec(t, inst.PriceTick, "0.0001")
	assertDec(t, inst.SizeStep, "0.01")
	assertDec(t, inst.MinQty, "0.01")
	assertDec(t, inst.MinNotional, "5")
	if inst.PositionMode != model.NetOnly || inst.Settle != "USDT" || inst.VenueSymbol != "ASTERUSDT" {
		t.Fatalf("unexpected instrument: %#v", inst)
	}
}

func TestOrderStatusSideTIFReduceOnlyAndFeeConversion(t *testing.T) {
	if got, err := sideToAster(enums.SideSell); err != nil || got != "SELL" {
		t.Fatalf("sideToAster sell=(%q,%v)", got, err)
	}
	if got := sideFromAster("BUY"); got != enums.SideBuy {
		t.Fatalf("sideFromAster BUY=%s", got)
	}
	if got, err := orderTypeToAster(enums.TypeMarket, enums.TifUnknown); err != nil || got != sdkperp.OrderType_MARKET {
		t.Fatalf("market type=(%q,%v)", got, err)
	}
	if got, err := tifToAster(enums.TifGTX); err != nil || got != sdkperp.TimeInForce_GTX {
		t.Fatalf("post-only tif=(%q,%v)", got, err)
	}
	if got := statusFromAster("PARTIALLY_FILLED"); got != enums.StatusPartiallyFilled {
		t.Fatalf("status=%s", got)
	}
	fill := fillFromTrade(sdkperp.Trade{
		Symbol: "BTCUSDT", ID: 99, OrderID: 42, Side: "SELL", Price: "60000", Qty: "0.001", Commission: "0.02", CommissionAsset: "USDT", Time: 1700000000000, Maker: false,
	}, testPerpID(), AccountIDDefault, "client-a")
	if fill.AccountID != AccountIDDefault || fill.ClientID != "client-a" || fill.VenueOrderID != "42" || fill.TradeID != "99" || fill.Liquidity != enums.LiqTaker {
		t.Fatalf("fill ids/liquidity not converted: %#v", fill)
	}
	assertDec(t, fill.Fee, "0.02")
}

func TestCapabilitiesAndUnsupportedBehaviorAreTruthful(t *testing.T) {
	market := newMarketDataClient(nil, nil, newInstrumentProvider(), clock.NewRealClock())
	streamingMarket := newMarketDataClient(nil, &fakePerpMarketWS{}, newInstrumentProvider(), clock.NewRealClock())
	exec := newExecutionClient(nil, newInstrumentProvider(), clock.NewRealClock(), AccountIDDefault)
	acct := newAccountClient(nil, newInstrumentProvider(), clock.NewRealClock(), AccountIDDefault)
	wantReference := contract.ReferenceDataCapabilities{
		CurrentFunding:      true,
		CurrentMarkPrice:    true,
		CurrentIndexPrice:   true,
		ReferencePolling:    true,
		FundingHistory:      true,
		CurrentOpenInterest: true,
	}
	if len(market.Capabilities().Products) != 1 || !market.Capabilities().Products[0].Market || market.Capabilities().Products[0].Trading || market.Capabilities().Products[0].Account || market.Capabilities().Reports != (contract.ReportCapabilities{}) || market.Capabilities().ReferenceData != wantReference || market.Capabilities().Streaming.Market {
		t.Fatalf("market capabilities=%#v", market.Capabilities())
	}
	wantReference.ReferenceStream = true
	if streamingMarket.Capabilities().ReferenceData != wantReference || !streamingMarket.Capabilities().Streaming.Market {
		t.Fatalf("streaming market capabilities=%#v", streamingMarket.Capabilities())
	}
	if !exec.Capabilities().Trading.CancelAll || exec.Capabilities().Trading.Modify || !exec.Capabilities().Reports.OpenOrders || !exec.Capabilities().Reports.SingleOrderStatus || exec.Capabilities().Streaming.Execution {
		t.Fatalf("exec capabilities=%#v", exec.Capabilities())
	}
	if !acct.Capabilities().Reports.PositionReports || !acct.Capabilities().Reports.AccountStateSnapshots {
		t.Fatalf("acct capabilities=%#v", acct.Capabilities())
	}
	if _, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{}); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("GenerateFillReports err=%v, want ErrNotSupported", err)
	}
	if _, err := exec.Modify(context.Background(), testPerpID(), "123", d("1"), d("1")); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("Modify err=%v, want ErrNotSupported", err)
	}
	if err := acct.SetLeverage(context.Background(), testPerpID(), d("2")); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetLeverage err=%v, want ErrNotSupported", err)
	}
	if err := acct.SetMarginMode(context.Background(), testPerpID(), "cross"); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetMarginMode err=%v, want ErrNotSupported", err)
	}
}

func TestPerpOneWayPositionSideMapsBothToPosNetAndPreservesSignedQuantity(t *testing.T) {
	pos := positionFromRisk(sdkperp.PositionRiskResponse{
		Symbol: "ASTERUSDT", PositionSide: "BOTH", PositionAmt: "-2.5", EntryPrice: "1.2", MarkPrice: "1.1", UpdateTime: 1700000000000,
	}, testPerpID(), AccountIDDefault, clock.NewRealClock().Now())
	if pos.Side != enums.PosNet {
		t.Fatalf("side=%s, want PosNet", pos.Side)
	}
	assertDec(t, pos.Quantity, "-2.5")
}

func TestPerpAccountStateIncludesSummaryAndRejectsNegativeMarginValues(t *testing.T) {
	fallback := clock.NewRealClock().Now()
	account := &sdkperp.AccountResponse{
		UpdateTime:            1700000000000,
		AvailableBalance:      "88",
		TotalWalletBalance:    "100",
		TotalMarginBalance:    "105",
		TotalInitialMargin:    "10",
		TotalMaintMargin:      "2",
		TotalUnrealizedProfit: "5",
		Assets: []sdkperp.AccountAsset{{
			Asset:            "USDT",
			WalletBalance:    "100",
			AvailableBalance: "88",
			MarginBalance:    "105",
			InitialMargin:    "10",
			MaintMargin:      "2",
			UpdateTime:       1700000000000,
		}},
	}
	if err := validateAccountResponseDecimals(account); err != nil {
		t.Fatalf("valid account rejected: %v", err)
	}
	state := accountStateFromResponse(account, AccountIDDefault, fallback)
	if state.Summary == nil {
		t.Fatalf("margin account state missing summary: %#v", state)
	}
	if state.Summary.SettlementCurrency != "USDT" {
		t.Fatalf("summary settlement=%q, want USDT", state.Summary.SettlementCurrency)
	}
	assertDec(t, state.Summary.Equity, "105")
	assertDec(t, state.Summary.AvailableCollateral, "88")
	if err := state.Validate(); err != nil {
		t.Fatalf("state validation failed: %v", err)
	}

	negative := *account
	negative.TotalMaintMargin = "-1"
	if err := validateAccountResponseDecimals(&negative); err == nil {
		t.Fatalf("negative account margin accepted")
	}
	negative = *account
	negative.Assets = append([]sdkperp.AccountAsset(nil), account.Assets...)
	negative.Assets[0].AvailableBalance = "-0.01"
	if err := validateAccountResponseDecimals(&negative); err == nil {
		t.Fatalf("negative asset available balance accepted")
	}
}

func TestPerpGenerateFillAndPositionReportsUseAuthoritativeREST(t *testing.T) {
	inst := mustPerpInstrument(t)
	client := perpClientSequence(t, map[string]string{
		"/fapi/v3/userTrades":   `[{"symbol":"ASTERUSDT","id":99,"orderId":42,"side":"SELL","price":"60000","qty":"0.001","quoteQty":"60","commission":"0.02","commissionAsset":"USDT","time":1700000000000,"maker":false,"positionSide":"BOTH"}]`,
		"/fapi/v3/positionRisk": `[{"symbol":"ASTERUSDT","positionSide":"BOTH","positionAmt":"-2.5","entryPrice":"1.2","markPrice":"1.1","unRealizedProfit":"-0.25","leverage":"3","updateTime":1700000000000}]`,
	})
	exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), "ASTER-CUSTOM")

	fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{InstrumentID: inst.ID, AccountID: "ASTER-CUSTOM", ClientID: "caller-client", VenueOrderID: "42"})
	if err != nil {
		t.Fatalf("GenerateFillReports returned error: %v", err)
	}
	if len(fills) != 1 {
		t.Fatalf("fill reports len=%d, want 1", len(fills))
	}
	if fills[0].Venue != VenueName || fills[0].AccountID != "ASTER-CUSTOM" || fills[0].Fill.AccountID != "ASTER-CUSTOM" || fills[0].Fill.ClientID != "caller-client" || fills[0].Fill.VenueOrderID != "42" {
		t.Fatalf("fill report ids not preserved: %#v", fills[0])
	}

	positions, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{InstrumentID: inst.ID, AccountID: "ASTER-CUSTOM"})
	if err != nil {
		t.Fatalf("GeneratePositionReports returned error: %v", err)
	}
	if len(positions) != 1 {
		t.Fatalf("position reports len=%d, want 1", len(positions))
	}
	pos := positions[0].Position
	if positions[0].Venue != VenueName || positions[0].AccountID != "ASTER-CUSTOM" || pos.AccountID != "ASTER-CUSTOM" || pos.InstrumentID != inst.ID || pos.Side != enums.PosNet {
		t.Fatalf("position report ids not preserved: %#v", positions[0])
	}
	assertDec(t, pos.Quantity, "-2.5")
}

func TestPerpGenerateFillReportsDoesNotFabricateClientID(t *testing.T) {
	inst := mustPerpInstrument(t)
	exec := newExecutionClient(perpClientResponse(t, `[{"symbol":"ASTERUSDT","id":99,"orderId":42,"side":"SELL","price":"60000","qty":"0.001","quoteQty":"60","commission":"0.02","commissionAsset":"USDT","time":1700000000000,"maker":false,"positionSide":"BOTH"}]`), testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{InstrumentID: inst.ID, ClientID: "caller-client"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 0 {
		t.Fatalf("client-id-only query matched venue trades without client evidence: %#v", reports)
	}
}

func TestPerpPositionReportsFailClosedOnUnresolvedVenueSymbol(t *testing.T) {
	inst := mustPerpInstrument(t)
	client := perpClientResponse(t, `[{"symbol":"OTHERUSDT","positionSide":"BOTH","positionAmt":"1","entryPrice":"1.2","markPrice":"1.1","unRealizedProfit":"0","leverage":"3","updateTime":1700000000000}]`)
	acct := newAccountClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	if _, err := acct.Positions(context.Background()); err == nil {
		t.Fatalf("account positions accepted unresolved nonzero venue symbol")
	}
	exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	if _, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{}); err == nil {
		t.Fatalf("position reports accepted unresolved nonzero venue symbol")
	}
}

func TestPerpPositionReportsIgnoreUnresolvedZeroVenueSymbol(t *testing.T) {
	inst := mustPerpInstrument(t)
	client := perpClientResponse(t, `[{"symbol":"OTHERUSDT","positionSide":"BOTH","positionAmt":"0","entryPrice":"0","markPrice":"1.1","unRealizedProfit":"0","leverage":"3","updateTime":1700000000000}]`)
	acct := newAccountClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	positions, err := acct.Positions(context.Background())
	if err != nil || len(positions) != 0 {
		t.Fatalf("account zero positions=%+v err=%v, want empty without error", positions, err)
	}
	exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	reports, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{})
	if err != nil || len(reports) != 0 {
		t.Fatalf("zero position reports=%+v err=%v, want empty without error", reports, err)
	}
}

func TestPerpExecutionMassStatusResyncsOpenOrdersFillsAndPositions(t *testing.T) {
	inst := mustPerpInstrument(t)
	client := perpClientSequence(t, map[string]string{
		"/fapi/v3/openOrders":   `[{"symbol":"ASTERUSDT","orderId":42,"clientOrderId":"c-open","status":"NEW","type":"LIMIT","side":"SELL","positionSide":"BOTH","timeInForce":"GTC","origQty":"1","price":"10","executedQty":"0","cumQty":"0","cumQuote":"0","avgPrice":"0","updateTime":1700000000000}]`,
		"/fapi/v3/userTrades":   `[{"symbol":"ASTERUSDT","id":99,"orderId":42,"side":"SELL","price":"10","qty":"0.5","quoteQty":"5","commission":"0.01","commissionAsset":"USDT","time":1700000001000,"maker":false,"positionSide":"BOTH"}]`,
		"/fapi/v3/positionRisk": `[{"symbol":"ASTERUSDT","positionSide":"BOTH","positionAmt":"-2.5","entryPrice":"1.2","markPrice":"1.1","unRealizedProfit":"-0.25","leverage":"3","updateTime":1700000000000}]`,
	})
	exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDDefault, IncludeFills: true, IncludePositions: true})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus returned error: %v", err)
	}
	if mass.Partial {
		t.Fatalf("complete open-order enumeration must not be partial: warnings=%#v", mass.Warnings)
	}
	if len(mass.OrderReports) != 1 || mass.OrderReports["42"].Order.VenueOrderID != "42" {
		t.Fatalf("order reports=%#v", mass.OrderReports)
	}
	if len(mass.FillReports["42"]) != 1 || mass.FillReports["42"][0].Fill.TradeID != "99" {
		t.Fatalf("fill reports=%#v", mass.FillReports)
	}
	if len(mass.PositionReports) != 1 {
		t.Fatalf("position reports=%#v", mass.PositionReports)
	}
}

func TestPerpDiscoveryFailsClosedForUnsupportedSettlementAndMalformedRows(t *testing.T) {
	profile := mustProfile(t)
	valid := *mustPerpSymbolInfo(t, "ASTERUSDT")

	missingSettle := valid
	missingSettle.MarginAsset = ""
	if _, err := instrumentFromSymbolInfo(&missingSettle, profile); err == nil {
		t.Fatalf("missing settlement accepted")
	}
	nonUSDT := valid
	nonUSDT.MarginAsset = "USDC"
	if _, err := instrumentFromSymbolInfo(&nonUSDT, profile); err == nil {
		t.Fatalf("non-USDT settlement accepted")
	}
	malformedTick := valid
	malformedTick.Filters = replacePerpFilterValue(malformedTick.Filters, "PRICE_FILTER", "tickSize", "not-a-decimal")
	if _, err := instrumentFromSymbolInfo(&malformedTick, profile); err == nil {
		t.Fatalf("malformed tick accepted")
	}
	zeroStep := valid
	zeroStep.Filters = replacePerpFilterValue(zeroStep.Filters, "LOT_SIZE", "stepSize", "0")
	if _, err := instrumentFromSymbolInfo(&zeroStep, profile); err == nil {
		t.Fatalf("zero step accepted")
	}

	testSymbol := valid
	testSymbol.Symbol = "TESTASTERUSDT"
	if inst, err := instrumentFromSymbolInfo(&testSymbol, profile); err != nil || inst != nil {
		t.Fatalf("TEST symbol should be filtered without error, got inst=%#v err=%v", inst, err)
	}

	provider := newInstrumentProvider()
	if err := provider.loadExchangeInfo(&sdkperp.ExchangeInfoResponse{Symbols: []sdkperp.SymbolInfo{malformedTick}}, profile); err == nil {
		t.Fatalf("provider accepted malformed in-scope row")
	}
	if err := provider.loadExchangeInfo(&sdkperp.ExchangeInfoResponse{Symbols: []sdkperp.SymbolInfo{testSymbol}}, profile); err == nil {
		t.Fatalf("provider accepted discovery with no supported instruments")
	}
}

func TestPerpNewValidatesProfileWhenClientInjected(t *testing.T) {
	spotProfile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(context.Background(), Config{Profile: spotProfile, Client: perpClientNoNetwork(t)})
	if err == nil {
		t.Fatalf("New accepted wrong product profile with injected client")
	}
}

func TestPerpNewRejectsInjectedClientFromDifferentEnvironment(t *testing.T) {
	production, err := astercommon.NewProfile(astercommon.EnvironmentProduction, astercommon.ProductPerp)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sdkperp.NewClient(production, testSecurity(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(context.Background(), Config{Profile: mustProfile(t), Client: client})
	if err == nil {
		t.Fatal("New accepted production client under Testnet profile")
	}
}

func mustPerpInstrument(t *testing.T) *model.Instrument {
	t.Helper()
	inst, err := instrumentFromSymbolInfo(mustPerpSymbolInfo(t, "ASTERUSDT"), mustProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	if inst == nil {
		t.Fatal("instrumentFromSymbolInfo returned nil")
	}
	return inst
}

func mustPerpSymbolInfo(t *testing.T, symbol string) *sdkperp.SymbolInfo {
	t.Helper()
	var info sdkperp.ExchangeInfoResponse
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "sdk", "aster", "perp", "testdata", "v3", "exchange_info.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	for i := range info.Symbols {
		if info.Symbols[i].Symbol == symbol {
			return &info.Symbols[i]
		}
	}
	t.Fatalf("%s fixture not found", symbol)
	return nil
}

func mustProfile(t *testing.T) astercommon.Profile {
	t.Helper()
	profile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductPerp)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func testPerpID() model.InstrumentID {
	return model.InstrumentID{Venue: VenueName, Symbol: "ASTER-USDT", Kind: enums.KindPerp}
}

func testProvider(insts ...*model.Instrument) *instrumentProvider {
	provider := newInstrumentProvider()
	provider.LoadSnapshot(insts)
	return provider
}

func perpClientNoNetwork(t *testing.T) *sdkperp.Client {
	t.Helper()
	client, err := sdkperp.NewClient(mustProfile(t), testSecurity(t))
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected REST call: %s %s", r.Method, r.URL.String())
		return &http.Response{StatusCode: http.StatusTeapot, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})})
	return client
}

func perpClientResponse(t *testing.T, body string) *sdkperp.Client {
	t.Helper()
	client, err := sdkperp.NewClient(mustProfile(t), testSecurity(t))
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})})
	return client
}

func perpClientSequence(t *testing.T, byPath map[string]string) *sdkperp.Client {
	t.Helper()
	client, err := sdkperp.NewClient(mustProfile(t), testSecurity(t))
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, ok := byPath[r.URL.Path]
		if !ok {
			t.Fatalf("unexpected REST call: %s %s", r.Method, r.URL.String())
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})})
	return client
}

func testSecurity(t *testing.T) *astercommon.SecurityContext {
	t.Helper()
	security, err := astercommon.NewSecurityContext(astercommon.CredentialConfig{
		User:       "0x1111111111111111111111111111111111111111",
		PrivateKey: fmt.Sprintf("%064x", 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	return security
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func withPerp(req model.OrderRequest, mutate func(*model.OrderRequest)) model.OrderRequest {
	mutate(&req)
	return req
}

func replacePerpFilterValue(filters []sdkperp.SymbolFilter, filterType, field, value string) []sdkperp.SymbolFilter {
	out := append([]sdkperp.SymbolFilter(nil), filters...)
	for i := range out {
		if out[i].FilterType != filterType {
			continue
		}
		switch field {
		case "tickSize":
			out[i].TickSize = value
		case "stepSize":
			out[i].StepSize = value
		case "minQty":
			out[i].MinQty = value
		case "notional":
			out[i].Notional = value
		}
	}
	return out
}

func d(v string) decimal.Decimal { return decimal.RequireFromString(v) }

func assertDec(t *testing.T, got decimal.Decimal, want string) {
	t.Helper()
	if !got.Equal(d(want)) || got.String() != d(want).String() {
		t.Fatalf("decimal=%s, want %s", got, want)
	}
}
