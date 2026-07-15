package gate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

func TestGateSpotClientsImplementContractsAndCapabilities(t *testing.T) {
	provider := gateSpotTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 8, 3, 20, 0, 0, time.UTC))
	rest := gatesdk.NewClient().WithCredentials("key", "secret")

	var _ contract.MarketDataClient = newMarketDataClient(rest, nil, nil, provider, clk)
	var _ contract.DerivativeReferenceDataClient = newMarketDataClient(rest, nil, nil, provider, clk)
	var _ contract.OpenInterestClient = newMarketDataClient(rest, nil, nil, provider, clk)
	var _ contract.ExecutionClient = newExecutionClient(rest, provider, clk)
	var _ contract.AccountClient = newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindSpot})

	if caps := newAccountClient(rest, provider, clk, nil).Capabilities(); !caps.Reports.AccountBalanceSnapshots {
		t.Fatalf("account capabilities missing balance snapshot support: %+v", caps)
	}
	if caps := newExecutionClient(rest, provider, clk).Capabilities(); !caps.Trading.Submit || caps.Trading.Modify {
		t.Fatalf("execution capabilities must claim submit/cancel but not modify in phase one: %+v", caps)
	}
}

func TestGateRuntimeCapabilitiesFollowSelectedProductScope(t *testing.T) {
	provider := gateFullTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 8, 3, 20, 0, 0, time.UTC))
	rest := gatesdk.NewClient().WithCredentials("key", "secret")

	spotMarketCaps := newMarketDataClient(rest, nil, nil, provider, clk).withScope([]enums.InstrumentKind{enums.KindSpot}).Capabilities()
	if !gateCapabilitiesHasKind(spotMarketCaps, enums.KindSpot) || gateCapabilitiesHasKind(spotMarketCaps, enums.KindPerp) {
		t.Fatalf("spot-scoped market capabilities overclaimed products: %+v", spotMarketCaps)
	}
	if spotMarketCaps.ReferenceData.CurrentFunding || spotMarketCaps.ReferenceData.CurrentOpenInterest {
		t.Fatalf("spot-scoped market capabilities must not claim derivative reference data: %+v", spotMarketCaps.ReferenceData)
	}
	perpMarketCaps := newMarketDataClient(rest, nil, nil, provider, clk).withScope([]enums.InstrumentKind{enums.KindPerp}).Capabilities()
	if ref := perpMarketCaps.ReferenceData; !ref.CurrentFunding || !ref.CurrentMarkPrice || !ref.CurrentIndexPrice || !ref.CurrentOpenInterest || !ref.ReferencePolling {
		t.Fatalf("perp-scoped reference capabilities incomplete: %+v", ref)
	}
	perpExecCaps := newExecutionClient(rest, provider, clk).withScope([]enums.InstrumentKind{enums.KindPerp}).Capabilities()
	if !gateCapabilitiesHasKind(perpExecCaps, enums.KindPerp) || gateCapabilitiesHasKind(perpExecCaps, enums.KindSpot) {
		t.Fatalf("perp-scoped execution capabilities overclaimed products: %+v", perpExecCaps)
	}
	if !perpExecCaps.Reports.PositionReports {
		t.Fatalf("perp-scoped execution capabilities must retain position reports: %+v", perpExecCaps)
	}
	spotExecCaps := newExecutionClient(rest, provider, clk).withScope([]enums.InstrumentKind{enums.KindSpot}).Capabilities()
	if spotExecCaps.Reports.PositionReports {
		t.Fatalf("spot-scoped execution capabilities must not claim position reports: %+v", spotExecCaps)
	}
}

func TestGateSpotAccountStateAndRuntimeResync(t *testing.T) {
	server := newGateSpotServer(t)
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 8, 3, 21, 0, 0, time.UTC))
	provider := gateSpotTestProvider()
	rest := gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client())
	acct := newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindSpot})
	exec := newExecutionClient(rest, provider, clk)

	state, err := acct.AccountState(context.Background())
	if err != nil {
		t.Fatalf("AccountState: %v", err)
	}
	if state.AccountID != AccountIDUnified || state.Venue != VenueName || state.Type != model.AccountCash {
		t.Fatalf("unexpected account identity/type: %+v", state)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("state invalid: %v", err)
	}
	if len(state.Balances) != 2 || !state.Balances[0].CashInvariantOK() {
		t.Fatalf("unexpected balances: %+v", state.Balances)
	}
	if state.TsEvent != clk.Now() || state.TsInit != clk.Now() {
		t.Fatalf("account state timestamps=%s/%s, want clock now", state.TsEvent, state.TsInit)
	}

	node := btruntime.NewNode(btruntime.Clients{Execution: exec, Account: acct}, clk, AccountIDUnified, btruntime.WithAccountID(AccountIDUnified))
	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if report.AccountStatesApplied != 1 {
		t.Fatalf("account states applied=%d, want 1: %+v", report.AccountStatesApplied, report)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDUnified, model.AccountCash, enums.KindSpot)

	inst := provider.All()[0]
	if err := acct.SetLeverage(context.Background(), inst.ID, d("2")); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetLeverage err=%v, want ErrNotSupported", err)
	}
	if err := acct.SetMarginMode(context.Background(), inst.ID, "cross"); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetMarginMode err=%v, want ErrNotSupported", err)
	}
}

func TestGateSpotExecutionReportsAndMassStatus(t *testing.T) {
	server := newGateSpotServer(t)
	provider := gateSpotTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 8, 3, 22, 0, 0, time.UTC))
	rest := gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client())
	exec := newExecutionClient(rest, provider, clk)
	inst := provider.All()[0]

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst.ID,
		ClientID:     "spot-client-1",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("0.01"),
		Price:        d("1000"),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if order.Request.AccountID != AccountIDUnified || order.Request.ClientID != "spot-client-1" || order.VenueOrderID != "123" {
		t.Fatalf("unexpected submitted order: %+v", order)
	}

	reports, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{AccountID: AccountIDUnified, InstrumentID: inst.ID})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReports: %v", err)
	}
	if len(reports) != 1 || reports[0].Order.Request.ClientID != "spot-client-1" || reports[0].Order.Status != enums.StatusNew {
		t.Fatalf("unexpected order reports: %+v", reports)
	}
	clientOnlyReport, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{AccountID: AccountIDUnified, InstrumentID: inst.ID, ClientID: "spot-client-1"})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReport client-id-only: %v", err)
	}
	if clientOnlyReport == nil || clientOnlyReport.Order.Request.ClientID != "spot-client-1" || clientOnlyReport.Order.VenueOrderID != "123" {
		t.Fatalf("unexpected client-id-only spot order report: %+v", clientOnlyReport)
	}
	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{AccountID: AccountIDUnified, InstrumentID: inst.ID, VenueOrderID: "123"})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReport: %v", err)
	}
	if report == nil || report.Order.Status != enums.StatusFilled || !report.Order.FilledQty.Equal(d("0.01")) {
		t.Fatalf("unexpected single order report: %+v", report)
	}
	fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: AccountIDUnified, InstrumentID: inst.ID, VenueOrderID: "123"})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(fills) != 1 || fills[0].Fill.AccountID != AccountIDUnified || fills[0].Fill.Liquidity != enums.LiqTaker {
		t.Fatalf("unexpected fill reports: %+v", fills)
	}
	if _, err := exec.withScope([]enums.InstrumentKind{enums.KindSpot}).GeneratePositionReports(context.Background(), model.PositionReportQuery{AccountID: AccountIDUnified, InstrumentID: inst.ID}); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("spot-scoped GeneratePositionReports err=%v, want ErrNotSupported", err)
	}
	if err := exec.Cancel(context.Background(), inst.ID, "123"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case env := <-exec.Events():
		orderEvent, ok := env.Payload.(contract.OrderEvent)
		if !ok || orderEvent.Order.Status != enums.StatusCanceled || orderEvent.Order.Request.ClientID != "spot-client-1" {
			t.Fatalf("unexpected cancel event: %+v", env.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cancel order event")
	}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.OpenOrdersCoverage.State != model.CoverageComplete || len(mass.OrderReports) != 1 {
		t.Fatalf("unexpected mass status: %+v", mass)
	}
}

func TestGateSpotMarketSnapshotsAndPayloads(t *testing.T) {
	server := newGateSpotServer(t)
	provider := gateSpotTestProvider()
	rest := gatesdk.NewClient().WithBaseURL(server.URL).WithHTTPClient(server.Client())
	market := newMarketDataClient(rest, nil, nil, provider, clock.NewRealClock())
	inst := provider.All()[0]

	book, err := market.OrderBook(context.Background(), inst.ID, 5)
	if err != nil {
		t.Fatalf("OrderBook: %v", err)
	}
	if book.Sequence != 7 || len(book.Bids) != 1 || !book.Bids[0].Price.Equal(d("999")) {
		t.Fatalf("unexpected book: %+v", book)
	}
	bars, err := market.Bars(context.Background(), inst.ID, "1m", 1)
	if err != nil {
		t.Fatalf("Bars: %v", err)
	}
	if len(bars) != 1 || !bars[0].Open.Equal(d("1")) || !bars[0].Close.Equal(d("2")) {
		t.Fatalf("unexpected bars: %+v", bars)
	}

	quote, ok := quoteFromTickerPayload(inst.ID, []byte(`{"time":1700000000,"channel":"spot.tickers","event":"update","result":{"currency_pair":"ETH_USDT","highest_bid":"999","lowest_ask":"1001"}}`), time.Time{})
	if !ok || !quote.BidPrice.Equal(d("999")) || !quote.AskPrice.Equal(d("1001")) {
		t.Fatalf("unexpected quote=%+v ok=%v", quote, ok)
	}
	trades := tradesFromPayload(inst.ID, []byte(`{"time":1700000000,"channel":"spot.trades","event":"update","result":[{"id":"t1","currency_pair":"ETH_USDT","side":"sell","amount":"0.5","price":"1000","create_time_ms":"1700000000123"}]}`), time.Time{})
	if len(trades) != 1 || trades[0].AggressorSide != enums.SideSell || trades[0].Timestamp.IsZero() {
		t.Fatalf("unexpected trades: %+v", trades)
	}
}

func TestGateSpotPrivateStreamEventConversions(t *testing.T) {
	provider := gateSpotTestProvider()
	resolve := provider.resolveVenueSymbol
	now := time.Date(2026, 7, 8, 3, 23, 0, 0, time.UTC)

	orderMsg, err := gatesdk.DecodeSpotOrderMessage([]byte(`{"channel":"spot.orders","event":"update","result":[{"id":"123","text":"t-spot-client-1","currency_pair":"ETH_USDT","type":"limit","side":"buy","amount":"0.01","price":"1000","status":"open","filled_amount":"0.005","avg_deal_price":"1000"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	orderEvents := execEventsFromSpotOrderMessage(orderMsg, resolve, AccountIDUnified)
	if len(orderEvents) != 1 {
		t.Fatalf("order events len=%d", len(orderEvents))
	}
	orderEvent := orderEvents[0].(contract.OrderEvent)
	if !orderEvent.Order.FilledQty.IsZero() || orderEvent.Order.Request.ClientID != "spot-client-1" {
		t.Fatalf("unexpected order event: %+v", orderEvent)
	}

	tradeMsg, err := gatesdk.DecodeSpotUserTradeMessage([]byte(`{"channel":"spot.usertrades","event":"update","result":[{"id":"fill-1","text":"t-spot-client-1","currency_pair":"ETH_USDT","order_id":"123","side":"buy","role":"maker","amount":"0.005","price":"1000","fee":"-0.01","fee_currency":"USDT","create_time_ms":"1700000000123"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	fillEvents := execEventsFromSpotUserTradeMessage(tradeMsg, resolve, AccountIDUnified)
	if len(fillEvents) != 1 {
		t.Fatalf("fill events len=%d", len(fillEvents))
	}
	fillEvent := fillEvents[0].(contract.FillEvent)
	if fillEvent.Fill.Liquidity != enums.LiqMaker || !fillEvent.Fill.Fee.Equal(d("0.01")) {
		t.Fatalf("unexpected fill event: %+v", fillEvent)
	}

	balanceMsg, err := gatesdk.DecodeSpotBalanceMessage([]byte(`{"channel":"spot.balances","event":"update","result":[{"currency":"USDT","available":"9","freeze":"1","total":"10","timestamp_ms":"1700000000123"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	accountEvents := accountEventsFromSpotBalanceMessage(balanceMsg, AccountIDUnified, now)
	if len(accountEvents) != 1 {
		t.Fatalf("account events len=%d", len(accountEvents))
	}
	balanceEvent := accountEvents[0].(contract.BalanceEvent)
	if balanceEvent.Balance.AccountID != AccountIDUnified || !balanceEvent.Balance.CashInvariantOK() {
		t.Fatalf("unexpected balance event: %+v", balanceEvent)
	}
}

func TestGateUSDTPerpAccountStateAndRuntimeResync(t *testing.T) {
	server := newGateSpotServer(t)
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 8, 3, 30, 0, 0, time.UTC))
	provider := gateFullTestProvider()
	rest := gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client())
	acct := newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
	exec := newExecutionClient(rest, provider, clk)

	state, err := acct.AccountState(context.Background())
	if err != nil {
		t.Fatalf("AccountState: %v", err)
	}
	if state.AccountID != AccountIDUnified || state.Venue != VenueName || state.Type != model.AccountMargin || state.BaseCurrency != "USDT" {
		t.Fatalf("unexpected futures account identity/type: %+v", state)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("state invalid: %v", err)
	}
	if len(state.Balances) < 2 || len(state.Margins) == 0 {
		t.Fatalf("expected spot+futures balances and margins: %+v", state)
	}
	positions, err := acct.Positions(context.Background())
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(positions) != 1 || positions[0].InstrumentID.Kind != enums.KindPerp || !positions[0].Quantity.Equal(d("2")) {
		t.Fatalf("unexpected positions: %+v", positions)
	}

	node := btruntime.NewNode(btruntime.Clients{Execution: exec, Account: acct}, clk, AccountIDUnified, btruntime.WithAccountID(AccountIDUnified))
	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if report.AccountStatesApplied != 1 {
		t.Fatalf("account states applied=%d, want 1: %+v", report.AccountStatesApplied, report)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDUnified, model.AccountMargin, enums.KindPerp)
}

func TestGateUSDTPerpExecutionReports(t *testing.T) {
	server := newGateSpotServer(t)
	provider := gateFullTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 8, 3, 31, 0, 0, time.UTC))
	rest := gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client())
	exec := newExecutionClient(rest, provider, clk)
	perpID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}

	order, err := exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: perpID,
		ClientID:     "perp-client-1",
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("2"),
		Price:        d("50000"),
		ReduceOnly:   true,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if order.Request.AccountID != AccountIDUnified || order.VenueOrderID != "456" || order.Request.Side != enums.SideSell {
		t.Fatalf("unexpected submitted perp order: %+v", order)
	}
	open, err := exec.OpenOrders(context.Background(), perpID)
	if err != nil {
		t.Fatalf("OpenOrders: %v", err)
	}
	if len(open) != 1 || open[0].Request.PositionSide != enums.PosNet {
		t.Fatalf("unexpected open perp orders: %+v", open)
	}
	mass, err := exec.withScope([]enums.InstrumentKind{enums.KindPerp}).GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.OrderReports) != 1 {
		t.Fatalf("unexpected futures mass status: %+v", mass)
	}
	clientOnlyReport, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{AccountID: AccountIDUnified, InstrumentID: perpID, ClientID: "perp-client-1"})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReport client-id-only: %v", err)
	}
	if clientOnlyReport == nil || clientOnlyReport.Order.Request.ClientID != "perp-client-1" || clientOnlyReport.Order.VenueOrderID != "456" {
		t.Fatalf("unexpected client-id-only perp order report: %+v", clientOnlyReport)
	}
	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{AccountID: AccountIDUnified, InstrumentID: perpID, VenueOrderID: "456"})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReport: %v", err)
	}
	if report == nil || report.Order.Status != enums.StatusFilled || !report.Order.FilledQty.Equal(d("2")) {
		t.Fatalf("unexpected perp order report: %+v", report)
	}
	fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: AccountIDUnified, InstrumentID: perpID, VenueOrderID: "456"})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(fills) != 1 || fills[0].Fill.Side != enums.SideSell || fills[0].Fill.FeeCurrency != "USDT" {
		t.Fatalf("unexpected perp fill reports: %+v", fills)
	}
	if err := exec.Cancel(context.Background(), perpID, "456"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case env := <-exec.Events():
		orderEvent, ok := env.Payload.(contract.OrderEvent)
		if !ok || orderEvent.Order.Status != enums.StatusCanceled || orderEvent.Order.Request.ClientID != "perp-client-1" {
			t.Fatalf("unexpected futures cancel event: %+v", env.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for futures cancel order event")
	}
	positions, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{AccountID: AccountIDUnified, InstrumentID: perpID})
	if err != nil {
		t.Fatalf("GeneratePositionReports: %v", err)
	}
	if len(positions) != 1 || positions[0].Position.Side != enums.PosNet {
		t.Fatalf("unexpected position reports: %+v", positions)
	}
}

func TestGateUSDTPerpMarketSnapshotsAndPrivateEvents(t *testing.T) {
	server := newGateSpotServer(t)
	provider := gateFullTestProvider()
	rest := gatesdk.NewClient().WithBaseURL(server.URL).WithHTTPClient(server.Client())
	market := newMarketDataClient(rest, nil, nil, provider, clock.NewRealClock())
	perpID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}

	book, err := market.OrderBook(context.Background(), perpID, 5)
	if err != nil {
		t.Fatalf("OrderBook: %v", err)
	}
	if book.Sequence != 8 || len(book.Bids) != 1 || !book.Bids[0].Quantity.Equal(d("2")) {
		t.Fatalf("unexpected futures book: %+v", book)
	}
	if got, want := book.Timestamp, time.Unix(1783484986, 705_000_000); !got.Equal(want) {
		t.Fatalf("futures book timestamp=%s, want %s", got, want)
	}
	bars, err := market.Bars(context.Background(), perpID, "1m", 1)
	if err != nil {
		t.Fatalf("Bars: %v", err)
	}
	if len(bars) != 1 || !bars[0].Close.Equal(d("50001")) {
		t.Fatalf("unexpected futures bars: %+v", bars)
	}
	ref, err := market.ReferenceSnapshot(context.Background(), perpID)
	if err != nil {
		t.Fatalf("ReferenceSnapshot: %v", err)
	}
	if !ref.Fields.Has(model.ReferenceHasFundingRate) || !ref.Fields.Has(model.ReferenceHasMarkPrice) || !ref.Fields.Has(model.ReferenceHasIndexPrice) || !ref.Fields.Has(model.ReferenceHasFundingInterval) || !ref.Fields.Has(model.ReferenceHasNextFundingTime) {
		t.Fatalf("reference fields incomplete: %+v", ref)
	}
	if !ref.FundingRate.Equal(d("0.0001")) || !ref.MarkPrice.Equal(d("50001.5")) || !ref.IndexPrice.Equal(d("50000.5")) || ref.FundingInterval != 8*time.Hour || ref.NextFundingTime.Unix() != 1700006400 {
		t.Fatalf("unexpected futures reference: %+v", ref)
	}
	oi, err := market.OpenInterest(context.Background(), perpID)
	if err != nil {
		t.Fatalf("OpenInterest: %v", err)
	}
	if !oi.OpenInterest.Equal(d("42")) || oi.Unit != "contracts" {
		t.Fatalf("unexpected futures OI: %+v", oi)
	}
	trades := tradesFromPayload(perpID, []byte(`{"time":1700000000,"channel":"futures.trades","event":"update","result":[{"id":99,"create_time":1700000000,"contract":"BTC_USDT","size":-2,"price":"50000"}]}`), time.Time{})
	if len(trades) != 1 || trades[0].AggressorSide != enums.SideSell || !trades[0].Quantity.Equal(d("2")) {
		t.Fatalf("unexpected futures trades: %+v", trades)
	}

	resolve := provider.resolveVenueSymbol
	orderMsg, err := gatesdk.DecodeFuturesOrderMessage([]byte(`{"channel":"futures.orders","event":"update","result":[{"id":456,"text":"t-perp-client-1","contract":"BTC_USDT","size":-2,"left":2,"price":"50000","tif":"gtc","status":"open","reduce_only":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	orderEvents := execEventsFromFuturesOrderMessage(orderMsg, resolve, AccountIDUnified)
	if len(orderEvents) != 1 || orderEvents[0].(contract.OrderEvent).Order.Request.PositionSide != enums.PosShort {
		t.Fatalf("unexpected futures order events: %+v", orderEvents)
	}
	tradeMsg, err := gatesdk.DecodeFuturesUserTradeMessage([]byte(`{"channel":"futures.usertrades","event":"update","result":[{"id":99,"create_time":1700000000,"contract":"BTC_USDT","order_id":456,"size":-2,"price":"50000","role":"taker","text":"t-perp-client-1","fee":"-0.02"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	fillEvents := execEventsFromFuturesUserTradeMessage(tradeMsg, resolve, AccountIDUnified)
	if len(fillEvents) != 1 || fillEvents[0].(contract.FillEvent).Fill.Side != enums.SideSell {
		t.Fatalf("unexpected futures fill events: %+v", fillEvents)
	}
	positionMsg, err := gatesdk.DecodeFuturesPositionMessage([]byte(`{"channel":"futures.positions","event":"update","result":[{"contract":"BTC_USDT","size":2,"mode":"single","entry_price":"50000","mark_price":"50001","unrealised_pnl":"2","leverage":"3","update_time":1700000000}]}`))
	if err != nil {
		t.Fatal(err)
	}
	accountEvents := accountEventsFromFuturesPositionMessage(positionMsg, resolve, AccountIDUnified, time.Now())
	if len(accountEvents) != 1 || accountEvents[0].(contract.PositionEvent).Position.Side != enums.PosNet {
		t.Fatalf("unexpected futures position events: %+v", accountEvents)
	}
}

func TestGateReferenceSnapshotKeepsTickerZeroFundingRate(t *testing.T) {
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}
	receivedAt := time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)

	ref := referenceFromGateFutures(id, &gatesdk.FuturesTicker{
		Contract:    "BTC_USDT",
		FundingRate: "0",
	}, &gatesdk.Contract{
		Name:        "BTC_USDT",
		FundingRate: "0.0007",
	}, receivedAt)

	if !ref.Fields.Has(model.ReferenceHasFundingRate) {
		t.Fatalf("expected funding rate field presence: %+v", ref)
	}
	if !ref.FundingRate.Equal(decimal.Zero) {
		t.Fatalf("ticker zero funding rate was overwritten by contract fallback: %+v", ref)
	}
}

func TestGateReportsRejectMismatchedAccountIDBeforeVenueRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("unexpected venue request for mismatched account id: %s", r.URL.String())
	}))
	defer server.Close()

	exec := newExecutionClient(
		gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		gateSpotTestProvider(),
		clock.NewSimulatedClock(time.Date(2026, 7, 8, 3, 24, 0, 0, time.UTC)),
	)
	spotID := model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT", Kind: enums.KindSpot}
	if orders, err := exec.GenerateOrderStatusReports(context.Background(), model.OrderStatusReportQuery{AccountID: "GATE-OTHER", InstrumentID: spotID}); err != nil || len(orders) != 0 {
		t.Fatalf("mismatched account order reports=%+v err=%v, want empty nil", orders, err)
	}
	if fills, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: "GATE-OTHER", InstrumentID: spotID}); err != nil || len(fills) != 0 {
		t.Fatalf("mismatched account fill reports=%+v err=%v, want empty nil", fills, err)
	}
	if called {
		t.Fatal("mismatched account report crossed HTTP boundary")
	}
}

func gateSpotTestProvider() *instrumentProvider {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromGateSpot(gatesdk.CurrencyPair{ID: "ETH_USDT", Base: "ETH", Quote: "USDT", TradeStatus: "tradable", AmountPrecision: 4, Precision: 2, MinBaseAmount: "0.001", MinQuoteAmount: "5"}),
	})
	return provider
}

func gateFullTestProvider() *instrumentProvider {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromGateSpot(gatesdk.CurrencyPair{ID: "ETH_USDT", Base: "ETH", Quote: "USDT", TradeStatus: "tradable", AmountPrecision: 4, Precision: 2, MinBaseAmount: "0.001", MinQuoteAmount: "5"}),
		instrumentFromGateContract(gatesdk.SettleUSDT, gatesdk.Contract{Name: "BTC_USDT", Status: "trading", QuantoMultiplier: "0.0001", OrderPriceRound: "0.1", OrderSizeMin: 1}),
	})
	return provider
}

func newGateSpotServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/spot/currency_pairs":
			writeJSON(t, w, []any{map[string]any{"id": "ETH_USDT", "base": "ETH", "quote": "USDT", "amount_precision": 4, "precision": 2, "min_base_amount": "0.001", "min_quote_amount": "5", "trade_status": "tradable"}})
		case "/spot/accounts":
			writeJSON(t, w, []any{
				map[string]any{"currency": "USDT", "available": "100.5", "locked": "2.25", "update_id": 1},
				map[string]any{"currency": "ETH", "available": "1", "locked": "0", "update_id": 2},
			})
		case "/spot/order_book":
			if got := r.URL.Query().Get("currency_pair"); got != "ETH_USDT" {
				t.Fatalf("currency_pair=%q, want ETH_USDT", got)
			}
			writeJSON(t, w, map[string]any{"id": 7, "current": 1700000000000, "update": 1700000000001, "bids": [][]string{{"999", "0.5"}}, "asks": [][]string{{"1001", "0.4"}}})
		case "/spot/candlesticks":
			writeJSON(t, w, [][]string{{"1700000000", "12", "2", "3", "0.5", "1"}})
		case "/spot/orders":
			switch r.Method {
			case http.MethodPost:
				writeJSON(t, w, map[string]any{"id": "123", "text": "t-spot-client-1", "currency_pair": "ETH_USDT", "type": "limit", "side": "buy", "amount": "0.01", "price": "1000", "status": "open"})
			case http.MethodGet:
				if got := r.URL.Query().Get("currency_pair"); got != "ETH_USDT" {
					t.Fatalf("spot open orders currency_pair=%q, want ETH_USDT", got)
				}
				writeJSON(t, w, []any{map[string]any{"id": "123", "text": "t-spot-client-1", "currency_pair": "ETH_USDT", "type": "limit", "side": "buy", "amount": "0.01", "price": "1000", "time_in_force": "gtc", "status": "open"}})
			default:
				t.Fatalf("unexpected /spot/orders method %s", r.Method)
			}
		case "/spot/orders/123":
			switch r.Method {
			case http.MethodGet:
				writeJSON(t, w, map[string]any{"id": "123", "text": "t-spot-client-1", "currency_pair": "ETH_USDT", "type": "limit", "side": "buy", "amount": "0.01", "price": "1000", "time_in_force": "gtc", "status": "closed", "finish_as": "filled", "filled_amount": "0.01", "avg_deal_price": "1000"})
			case http.MethodDelete:
				writeJSON(t, w, map[string]any{"id": "123", "text": "t-spot-client-1", "currency_pair": "ETH_USDT", "type": "limit", "side": "buy", "amount": "0.01", "price": "1000", "status": "closed", "finish_as": "cancelled"})
			default:
				t.Fatalf("unexpected /spot/orders/123 method %s", r.Method)
			}
		case "/spot/open_orders":
			if got := r.URL.Query().Get("account"); got != "spot" {
				t.Fatalf("spot open_orders account=%q, want spot", got)
			}
			writeJSON(t, w, []any{map[string]any{"currency_pair": "ETH_USDT", "total": 1, "orders": []any{map[string]any{"id": "123", "text": "t-spot-client-1", "currency_pair": "ETH_USDT", "type": "limit", "side": "buy", "amount": "0.01", "price": "1000", "time_in_force": "gtc", "status": "open"}}}})
		case "/spot/my_trades":
			writeJSON(t, w, []any{map[string]any{"id": "fill-1", "text": "t-spot-client-1", "currency_pair": "ETH_USDT", "order_id": "123", "side": "buy", "role": "taker", "amount": "0.01", "price": "1000", "fee": "-0.01", "fee_currency": "USDT", "create_time_ms": "1700000000123"}})
		case "/futures/usdt/contracts":
			writeJSON(t, w, []any{map[string]any{"name": "BTC_USDT", "status": "trading", "quanto_multiplier": "0.0001", "order_price_round": "0.1", "order_size_min": 1}})
		case "/futures/usdt/contracts/BTC_USDT":
			writeJSON(t, w, map[string]any{"name": "BTC_USDT", "status": "trading", "quanto_multiplier": "0.0001", "order_price_round": "0.1", "order_size_min": 1, "funding_rate": "0.0001", "funding_interval": 28800, "funding_next_apply": 1700006400})
		case "/futures/usdt/tickers":
			if got := r.URL.Query().Get("contract"); got != "BTC_USDT" {
				t.Fatalf("futures ticker contract=%q, want BTC_USDT", got)
			}
			writeJSON(t, w, []any{map[string]any{"contract": "BTC_USDT", "total_size": "42", "mark_price": "50001.5", "index_price": "50000.5", "funding_rate": "0.0001"}})
		case "/futures/usdt/accounts":
			writeJSON(t, w, map[string]any{"user": 42, "total": "1000", "available": "900", "currency": "USDT", "position_mode": "single", "position_initial_margin": "10", "maintenance_margin": "2", "position_margin": "10", "margin_mode": "cross"})
		case "/futures/usdt/positions":
			writeJSON(t, w, []any{map[string]any{"contract": "BTC_USDT", "size": 2, "mode": "single", "entry_price": "50000", "mark_price": "50001", "unrealised_pnl": "2", "leverage": "3", "update_time": 1700000000}})
		case "/futures/usdt/order_book":
			writeJSON(t, w, map[string]any{"id": 8, "current": 1783484986.964, "update": 1783484986.705, "bids": []any{map[string]any{"p": "49999", "s": 2}}, "asks": []any{map[string]any{"p": "50001", "s": 1}}})
		case "/futures/usdt/candlesticks":
			writeJSON(t, w, [][]string{{"1700000000", "3", "50001", "50002", "49999", "50000"}})
		case "/futures/usdt/orders":
			switch r.Method {
			case http.MethodPost:
				writeJSON(t, w, map[string]any{"id": 456, "text": "t-perp-client-1", "contract": "BTC_USDT", "size": -2, "left": 2, "price": "50000", "tif": "gtc", "status": "open", "reduce_only": true, "create_time": 1783486598.248})
			case http.MethodGet:
				writeJSON(t, w, []any{map[string]any{"id": 456, "text": "t-perp-client-1", "contract": "BTC_USDT", "size": -2, "left": 2, "price": "50000", "tif": "gtc", "status": "open", "reduce_only": true, "create_time": 1783486598.248}})
			default:
				t.Fatalf("unexpected /futures/usdt/orders method %s", r.Method)
			}
		case "/futures/usdt/orders/456":
			switch r.Method {
			case http.MethodGet:
				writeJSON(t, w, map[string]any{"id": 456, "text": "t-perp-client-1", "contract": "BTC_USDT", "size": -2, "left": 0, "price": "50000", "fill_price": "50000", "tif": "gtc", "status": "finished", "finish_as": "filled", "reduce_only": true, "create_time": 1783486598.248, "update_time": 1783486599.248})
			case http.MethodDelete:
				writeJSON(t, w, map[string]any{"id": 456, "text": "t-perp-client-1", "contract": "BTC_USDT", "size": -2, "left": 2, "price": "50000", "tif": "gtc", "status": "finished", "finish_as": "cancelled", "create_time": 1783486598.248, "update_time": 1783486599.248})
			default:
				t.Fatalf("unexpected /futures/usdt/orders/456 method %s", r.Method)
			}
		case "/futures/usdt/my_trades":
			writeJSON(t, w, []any{map[string]any{"id": 99, "create_time": 1783486598.248, "contract": "BTC_USDT", "order_id": 456, "size": -2, "price": "50000", "role": "taker", "text": "t-perp-client-1", "fee": "-0.02"}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write json: %v", err)
	}
}

func d(s string) decimal.Decimal {
	return decimal.RequireFromString(s)
}

func gateCapabilitiesHasKind(caps contract.Capabilities, kind enums.InstrumentKind) bool {
	for _, product := range caps.Products {
		if product.Kind == kind {
			return true
		}
	}
	return false
}
