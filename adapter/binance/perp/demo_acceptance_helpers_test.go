package perp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

func demoD(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestDemoAcceptanceNormalizeSymbol(t *testing.T) {
	cases := map[string]string{
		"BTC-USDT": "BTCUSDT",
		"eth_usdt": "ETHUSDT",
		"sol/usdt": "SOLUSDT",
		"bnbusdt":  "BNBUSDT",
	}
	for input, want := range cases {
		if got := normalizeDemoAcceptanceSymbol(input); got != want {
			t.Fatalf("normalizeDemoAcceptanceSymbol(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestDemoAcceptanceSymbolSpecFromExchangeInfo(t *testing.T) {
	info := &sdkperp.ExchangeInfoResponse{Symbols: []sdkperp.SymbolInfo{{
		Symbol:       "ETHUSDT",
		ContractType: "PERPETUAL",
		Filters: []map[string]any{
			{"filterType": "PRICE_FILTER", "tickSize": "0.01"},
			{"filterType": "LOT_SIZE", "stepSize": "0.001", "minQty": "0.001"},
			{"filterType": "MIN_NOTIONAL", "notional": "5"},
		},
	}}}

	spec, err := demoAcceptanceSymbolSpecFromExchangeInfo(info, "eth-usdt")
	if err != nil {
		t.Fatalf("demoAcceptanceSymbolSpecFromExchangeInfo: %v", err)
	}
	if spec.VenueSymbol != "ETHUSDT" {
		t.Fatalf("unexpected venue symbol: %s", spec.VenueSymbol)
	}
	if !spec.PriceTick.Equal(demoD("0.01")) || !spec.SizeStep.Equal(demoD("0.001")) ||
		!spec.MinQty.Equal(demoD("0.001")) || !spec.MinNotional.Equal(demoD("5")) {
		t.Fatalf("unexpected filters: %+v", spec)
	}
}

func TestDemoAcceptanceSelectOrderQuantityChoosesMinTradableStep(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{
		VenueSymbol: "ETHUSDT",
		SizeStep:    demoD("0.001"),
		MinQty:      demoD("0.001"),
		MinNotional: demoD("5"),
	}

	qty, err := selectDemoAcceptanceOrderQuantity(spec, decimal.Zero, demoD("10"), demoD("3000"))
	if err != nil {
		t.Fatalf("selectDemoAcceptanceOrderQuantity: %v", err)
	}
	if !qty.Equal(demoD("0.002")) {
		t.Fatalf("qty=%s, want 0.002", qty)
	}
}

func TestDemoAcceptanceSelectOrderQuantityUsesLowestTestPriceForMinNotional(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{
		VenueSymbol: "ETHUSDT",
		SizeStep:    demoD("0.001"),
		MinQty:      demoD("0.001"),
		MinNotional: demoD("20"),
	}

	qty, err := selectDemoAcceptanceOrderQuantityForPriceBand(spec, decimal.Zero, demoD("100"), demoD("2850"), demoD("3000"))
	if err != nil {
		t.Fatalf("selectDemoAcceptanceOrderQuantityForPriceBand: %v", err)
	}
	if !qty.Equal(demoD("0.008")) {
		t.Fatalf("qty=%s, want 0.008", qty)
	}
	if qty.Mul(demoD("2850")).LessThan(spec.MinNotional) {
		t.Fatalf("qty=%s does not satisfy resting-price min notional", qty)
	}
	if qty.Mul(demoD("3000")).GreaterThan(demoD("100")) {
		t.Fatalf("qty=%s exceeds max notional at mark price", qty)
	}
}

func TestDemoAcceptanceSelectOrderQuantityRejectsOverMaxNotional(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{
		VenueSymbol: "BTCUSDT",
		SizeStep:    demoD("0.001"),
		MinQty:      demoD("0.001"),
		MinNotional: demoD("5"),
	}

	if _, err := selectDemoAcceptanceOrderQuantity(spec, demoD("0.001"), demoD("10"), demoD("65000")); err == nil {
		t.Fatalf("expected over-max notional rejection")
	}
}

func TestDemoAcceptanceSelectOrderQuantityUsesIOCPriceForMaxNotional(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{
		VenueSymbol: "ETHUSDT",
		SizeStep:    demoD("0.001"),
		MinQty:      demoD("0.001"),
		MinNotional: demoD("5"),
	}

	if _, err := selectDemoAcceptanceOrderQuantityForPriceBand(spec, demoD("0.033"), demoD("100"), demoD("2850"), demoD("3000")); err != nil {
		t.Fatalf("configured quantity should fit at the uncrossed price: %v", err)
	}
	if _, err := selectDemoAcceptanceOrderQuantityForPriceBand(spec, demoD("0.033"), demoD("100"), demoD("2850"), demoD("3031")); err == nil {
		t.Fatal("configured quantity must be rejected at the higher IOC lifecycle price")
	}
}

func TestDemoAcceptanceSelectOrderQuantityRejectsNonStepQuantity(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{
		VenueSymbol: "ETHUSDT",
		SizeStep:    demoD("0.001"),
		MinQty:      demoD("0.001"),
		MinNotional: demoD("5"),
	}

	if _, err := selectDemoAcceptanceOrderQuantity(spec, demoD("0.0015"), demoD("10"), demoD("3000")); err == nil {
		t.Fatalf("expected non-step quantity rejection")
	}
}

func TestDemoAcceptanceCleanupMetadataRemediation(t *testing.T) {
	meta := demoAcceptanceCleanupMetadata{
		Symbol:         "ETHUSDT",
		Side:           "BUY",
		Quantity:       demoD("0.002"),
		VenueOrderIDs:  []string{"12345"},
		ClientOrderIDs: []string{"bolter-demo-1"},
		Exposure:       demoD("0.002"),
	}

	got := meta.Remediation()
	for _, want := range []string{"ETHUSDT", "BUY", "0.002", "12345", "bolter-demo-1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("remediation %q missing %q", got, want)
		}
	}
}

func TestDemoClientOrderIDFitsBinanceLimit(t *testing.T) {
	for _, kind := range []string{"rest", "fill", "close", "rtrest", "rtfill"} {
		id := demoClientOrderID(kind)
		if len(id) >= 36 {
			t.Fatalf("client order id %q length=%d, want <36", id, len(id))
		}
		if !strings.Contains(id, kind) {
			t.Fatalf("client order id %q should include kind %q", id, kind)
		}
	}
}

func TestWaitForDemoRuntimePortfolioNetQtyObservesPortfolioFill(t *testing.T) {
	inst := model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	node := btruntime.NewNode(btruntime.Clients{}, clock.NewSimulatedClock(time.Now()), "pf")
	node.Portfolio.OnFill(model.Fill{
		InstrumentID: inst,
		Side:         enums.SideBuy,
		Price:        demoD("1500"),
		Quantity:     demoD("0.01"),
	}, enums.PosNet)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForDemoRuntimePortfolioNetQty(ctx, node, inst, demoD("0.01")); err != nil {
		t.Fatalf("waitForDemoRuntimePortfolioNetQty: %v", err)
	}
}

func TestDemoAcceptanceDefaultMaxNotionalIs100USDT(t *testing.T) {
	t.Setenv("BINANCE_DEMO_MAX_NOTIONAL_USDT", "")

	got := demoDecimalEnvOrDefault(t, "BINANCE_DEMO_MAX_NOTIONAL_USDT", demoDefaultMaxNotionalUSDT)
	if !got.Equal(demoD("100")) {
		t.Fatalf("default Demo max notional=%s, want 100", got)
	}
}

func TestDemoAcceptanceCleanupStateArmsBeforeVenueOrderID(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState("ETHUSDT", demoD("0.002"))

	cleanup.Arm(enums.SideBuy, "bolter-demo-before-submit")

	if !cleanup.Needed() {
		t.Fatalf("cleanup should be needed before a venue order id is known")
	}
	if cleanup.Metadata().Side != "BUY" {
		t.Fatalf("side=%q, want BUY", cleanup.Metadata().Side)
	}
	if got := cleanup.Metadata().ClientOrderIDs; len(got) != 1 || got[0] != "bolter-demo-before-submit" {
		t.Fatalf("client order ids=%v, want pre-submit client id", got)
	}
	if got := cleanup.Metadata().VenueOrderIDs; len(got) != 0 {
		t.Fatalf("venue order ids=%v, want none before submit response", got)
	}
	if got := cleanup.TrackedOpenOrders(); len(got) != 1 || got[0].VenueOrderID != "" || got[0].ClientID != "bolter-demo-before-submit" {
		t.Fatalf("client-only ambiguous submit tracking=%+v", got)
	}

	cleanup.RecordVenueOrderID("12345")
	if got := cleanup.Metadata().VenueOrderIDs; len(got) != 1 || got[0] != "12345" {
		t.Fatalf("venue order ids=%v, want recorded venue id", got)
	}
	tracked := cleanup.TrackedOpenOrders()
	if len(tracked) != 1 || tracked[0].VenueOrderID != "12345" || tracked[0].ClientID != "bolter-demo-before-submit" {
		t.Fatalf("tracked orders=%+v, want venue/client identity pair", tracked)
	}
}

func TestInspectRecordedDemoOrdersRecoversClientOnlyPartialFill(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState("ETHUSDT", demoD("0.020"))
	cleanup.Arm(enums.SideBuy, "ambiguous-client")

	err := inspectRecordedDemoOrdersWithLookup(cleanup.TrackedOpenOrders(), demoD("100"), false, &cleanup, func(tracked demoAcceptanceTrackedOrder) (*sdkperp.OrderResponse, error) {
		if tracked.VenueOrderID != "" || tracked.ClientID != "ambiguous-client" {
			t.Fatalf("lookup tracked=%+v, want client-only identity", tracked)
		}
		return &sdkperp.OrderResponse{
			OrderID:       12345,
			ClientOrderID: tracked.ClientID,
			Status:        "CANCELED",
			ExecutedQty:   "0.010",
			CumQuote:      "30.25",
		}, nil
	})
	if err != nil {
		t.Fatalf("inspect client-only partial fill: %v", err)
	}
	if !cleanup.CloseAuthorized() || !cleanup.CloseLimit().Equal(demoD("0.010")) {
		t.Fatalf("recovered partial fill not authorized for bounded cleanup: authorized=%v limit=%s", cleanup.CloseAuthorized(), cleanup.CloseLimit())
	}
	if got := cleanup.TrackedOpenOrders(); len(got) != 0 {
		t.Fatalf("terminal recovered order remains tracked: %+v", got)
	}
	if got := cleanup.Metadata().VenueOrderIDs; len(got) != 1 || got[0] != "12345" {
		t.Fatalf("recovered venue order ids=%v, want [12345]", got)
	}
}

func TestInspectRecordedDemoOrdersKeepsMalformedTerminalEvidenceVisible(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState("ETHUSDT", demoD("0.020"))
	cleanup.Arm(enums.SideBuy, "ambiguous-client")
	cleanup.RecordVenueOrderID("12345")

	err := inspectRecordedDemoOrdersWithLookup(cleanup.TrackedOpenOrders(), demoD("100"), false, &cleanup, func(demoAcceptanceTrackedOrder) (*sdkperp.OrderResponse, error) {
		return &sdkperp.OrderResponse{OrderID: 12345, ClientOrderID: "ambiguous-client", Status: "CANCELED", ExecutedQty: "malformed"}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "invalid executed quantity") {
		t.Fatalf("malformed terminal evidence err=%v, want explicit parse failure", err)
	}
	if got := cleanup.TrackedOpenOrders(); len(got) != 1 {
		t.Fatalf("malformed terminal evidence was silently removed from tracking: %+v", got)
	}
	if cleanup.CloseAuthorized() {
		t.Fatal("malformed terminal evidence authorized exposure cleanup")
	}
}

func TestResolveUnresolvedDemoOrderDoesNotSwallowMalformedTerminalEvidence(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState("ETHUSDT", demoD("0.020"))
	cleanup.Arm(enums.SideBuy, "ambiguous-client")
	err := resolveUnresolvedDemoOrdersWithRetry(context.Background(), &cleanup, demoD("100"), 1, 0, func(demoAcceptanceTrackedOrder) (*sdkperp.OrderResponse, error) {
		return &sdkperp.OrderResponse{OrderID: 12345, ClientOrderID: "ambiguous-client", Status: "CANCELED", ExecutedQty: "malformed"}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "invalid executed quantity") {
		t.Fatalf("resolve malformed terminal evidence err=%v, want propagated parse failure", err)
	}
}

func TestBinancePerpCleanupDoesNotDiscardInitialInspectionErrors(t *testing.T) {
	source, err := os.ReadFile("demo_acceptance_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "_ = inspectRecordedDemoOrders(") {
		t.Fatal("Binance Perp cleanup discards the first authoritative order-inspection error")
	}
}

func TestInspectRecordedDemoOrdersResolvesClientOnlyOpenOrderForScopedCancel(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState("ETHUSDT", demoD("0.020"))
	cleanup.Arm(enums.SideBuy, "ambiguous-client")

	err := inspectRecordedDemoOrdersWithLookup(cleanup.TrackedOpenOrders(), demoD("100"), false, &cleanup, func(tracked demoAcceptanceTrackedOrder) (*sdkperp.OrderResponse, error) {
		return &sdkperp.OrderResponse{OrderID: 12345, ClientOrderID: tracked.ClientID, Status: "NEW", ExecutedQty: "0"}, nil
	})
	if err != nil {
		t.Fatalf("inspect client-only open order: %v", err)
	}
	if got := cleanup.CancellableVenueOrderIDs(); len(got) != 1 || got[0] != "12345" {
		t.Fatalf("scoped cancellable ids=%v, want [12345]", got)
	}
}

func TestResolveUnresolvedDemoOrderRetriesThenScopesOpenOrderCancel(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState("ETHUSDT", demoD("0.020"))
	cleanup.Arm(enums.SideBuy, "ambiguous-client")
	lookupCalls := 0
	err := resolveUnresolvedDemoOrdersWithRetry(context.Background(), &cleanup, demoD("100"), 2, 0, func(tracked demoAcceptanceTrackedOrder) (*sdkperp.OrderResponse, error) {
		lookupCalls++
		if lookupCalls == 1 {
			return nil, errors.New("temporary query failure")
		}
		return &sdkperp.OrderResponse{OrderID: 12345, ClientOrderID: tracked.ClientID, Status: "NEW", ExecutedQty: "0"}, nil
	})
	if err != nil {
		t.Fatalf("resolve retried open order: %v", err)
	}
	var canceled []string
	if err := cancelRecordedDemoOrders(&cleanup, func(venueOrderID string) error {
		canceled = append(canceled, venueOrderID)
		return nil
	}, func() error { return nil }); err != nil {
		t.Fatalf("cancel resolved order: %v", err)
	}
	if lookupCalls != 2 || len(canceled) != 1 || canceled[0] != "12345" {
		t.Fatalf("lookupCalls=%d canceled=%v, want 2 and [12345]", lookupCalls, canceled)
	}
}

func TestResolveUnresolvedDemoOrderRetriesThenRecoversTerminalPartialFill(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState("ETHUSDT", demoD("0.020"))
	cleanup.Arm(enums.SideBuy, "ambiguous-client")
	lookupCalls := 0
	err := resolveUnresolvedDemoOrdersWithRetry(context.Background(), &cleanup, demoD("100"), 2, 0, func(tracked demoAcceptanceTrackedOrder) (*sdkperp.OrderResponse, error) {
		lookupCalls++
		if lookupCalls == 1 {
			return nil, errors.New("temporary query failure")
		}
		return &sdkperp.OrderResponse{OrderID: 12345, ClientOrderID: tracked.ClientID, Status: "EXPIRED", ExecutedQty: "0.010", CumQuote: "30.25"}, nil
	})
	if err != nil {
		t.Fatalf("resolve retried terminal partial order: %v", err)
	}
	if lookupCalls != 2 || !cleanup.CloseAuthorized() || !cleanup.CloseLimit().Equal(demoD("0.010")) {
		t.Fatalf("lookupCalls=%d authorized=%v limit=%s", lookupCalls, cleanup.CloseAuthorized(), cleanup.CloseLimit())
	}
}

func TestDemoAcceptanceCleanupDoesNotAuthorizeCloseBeforeConfirmedFill(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState("ETHUSDT", demoD("0.002"))
	cleanup.Arm(enums.SideBuy, "bolter-demo-ambiguous")
	cleanup.RecordVenueOrderID("12345")

	if cleanup.CloseAuthorized() {
		t.Fatal("ambiguous submit must not authorize exposure close")
	}
	if got := cleanup.CloseLimit(); !got.IsZero() {
		t.Fatalf("close limit=%s, want zero before authoritative fill", got)
	}
	if got := cleanup.CancellableVenueOrderIDs(); len(got) != 1 || got[0] != "12345" {
		t.Fatalf("cancellable venue ids=%v, want only the recorded acceptance order", got)
	}

	cleanup.ConfirmFill(demoD("0.0015"))
	if !cleanup.CloseAuthorized() || !cleanup.CloseLimit().Equal(demoD("0.0015")) {
		t.Fatalf("confirmed fill did not set bounded close limit: authorized=%v limit=%s", cleanup.CloseAuthorized(), cleanup.CloseLimit())
	}
	cleanup.BeginCloseAttempt()
	if cleanup.CloseAuthorized() {
		t.Fatal("an ambiguous close attempt must never authorize a deferred retry")
	}
	cleanup.MarkOrderTerminal("12345")
	if got := cleanup.CancellableVenueOrderIDs(); len(got) != 0 {
		t.Fatalf("terminal venue ids still cancellable: %v", got)
	}
}

func TestCancelRecordedDemoOrdersKeepsOrdersTrackedUntilNoOpenConfirmed(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState("ETHUSDT", demoD("0.002"))
	cleanup.Arm(enums.SideBuy, "bolter-demo-rest")
	cleanup.RecordVenueOrderID("12345")

	wantErr := errors.New("open order still visible")
	var canceled []string
	err := cancelRecordedDemoOrders(
		&cleanup,
		func(venueOrderID string) error {
			canceled = append(canceled, venueOrderID)
			return nil
		},
		func() error { return wantErr },
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("cancelRecordedDemoOrders error=%v, want %v", err, wantErr)
	}
	if len(canceled) != 1 || canceled[0] != "12345" {
		t.Fatalf("canceled ids=%v, want [12345]", canceled)
	}
	if got := cleanup.CancellableVenueOrderIDs(); len(got) != 1 || got[0] != "12345" {
		t.Fatalf("orders were untracked before authoritative no-open confirmation: %v", got)
	}

	if err := cancelRecordedDemoOrders(&cleanup, func(string) error { return nil }, func() error { return nil }); err != nil {
		t.Fatalf("confirmed cancellation: %v", err)
	}
	if got := cleanup.CancellableVenueOrderIDs(); len(got) != 0 {
		t.Fatalf("terminal orders still tracked after no-open confirmation: %v", got)
	}
}

func TestCancelRecordedDemoOrdersAcceptsCancelErrorAfterAuthoritativeNoOpen(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState("ETHUSDT", demoD("0.002"))
	cleanup.Arm(enums.SideBuy, "bolter-demo-rest")
	cleanup.RecordVenueOrderID("12345")

	if err := cancelRecordedDemoOrders(&cleanup, func(string) error {
		return errors.New("venue reports order already terminal")
	}, func() error { return nil }); err != nil {
		t.Fatalf("authoritative no-open must supersede cancel endpoint error: %v", err)
	}
	if got := cleanup.CancellableVenueOrderIDs(); len(got) != 0 {
		t.Fatalf("terminal order remains tracked after authoritative no-open: %v", got)
	}
}

func TestBinanceDemoRuntimeAcceptanceDoesNotUseExecTester(t *testing.T) {
	source, err := os.ReadFile("demo_runtime_acceptance_test.go")
	if err != nil {
		t.Fatalf("read runtime acceptance source: %v", err)
	}
	for _, forbidden := range []string{"runtime/runtimetest", "NewExecTester", "WithStrategy"} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("runtime acceptance still contains unsafe helper %q", forbidden)
		}
	}
	for _, required := range []string{"node.Exec.Submit", "node.Exec.Cancel", "waitForNoDemoOpenOrders"} {
		if !strings.Contains(string(source), required) {
			t.Fatalf("runtime acceptance source missing explicit lifecycle step %q", required)
		}
	}
}

func TestBinanceDemoAdapterCloseConsumesAuthorizationAtSubmitBoundary(t *testing.T) {
	source, err := os.ReadFile("demo_acceptance_test.go")
	if err != nil {
		t.Fatalf("read adapter acceptance source: %v", err)
	}
	body := string(source)
	start := strings.Index(body, "func closeBinanceDemoExposure")
	if start < 0 {
		t.Fatal("closeBinanceDemoExposure source not found")
	}
	body = body[start:]
	book := strings.Index(body, "adapter.Market.OrderBook")
	arm := strings.Index(body, "state.Arm(enums.SideSell")
	begin := strings.Index(body, "state.BeginCloseAttempt()")
	submit := strings.Index(body, "adapter.Execution.Submit")
	if !(book >= 0 && book < arm && arm < begin && begin < submit) {
		t.Fatalf("close boundary order invalid: book=%d arm=%d begin=%d submit=%d", book, arm, begin, submit)
	}
}

func TestDemoExposureFromPositionsRejectsOffsettingReports(t *testing.T) {
	id := model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	positions := []model.Position{
		{InstrumentID: id, Side: enums.PosLong, Quantity: demoD("0.01")},
		{InstrumentID: id, Side: enums.PosShort, Quantity: demoD("-0.01")},
	}

	if _, err := demoExposureFromPositions(positions, id); err == nil {
		t.Fatal("offsetting non-zero position reports must not be treated as flat")
	}
}

func TestBinanceDemoAdapterAndRuntimePreflightPositionsBeforeFirstSubmit(t *testing.T) {
	assertBefore := func(file, first, second string) {
		t.Helper()
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		body := string(source)
		firstIndex := strings.Index(body, first)
		secondIndex := strings.Index(body, second)
		if firstIndex < 0 || secondIndex < 0 || firstIndex >= secondIndex {
			t.Fatalf("%s must place %q before %q", file, first, second)
		}
	}
	assertBefore("demo_acceptance_test.go", "demoCurrentExposure(", ".Submit(")
	assertBefore("demo_runtime_tester_test.go", "demoCurrentExposure(", "return adapter, spec")
	assertBefore("demo_runtime_acceptance_test.go", "newBinanceDemoRuntimeAcceptanceFixture(", ".Submit(")
}

func TestValidateBinanceDemoFillNotionalRejectsCumQuoteOverCap(t *testing.T) {
	resp := &sdkperp.OrderResponse{ExecutedQty: "0.033", CumQuote: "100.01", AvgPrice: "3030.606"}
	qty, err := validateBinanceDemoFill(resp, demoD("100"))
	if err == nil {
		t.Fatal("fill whose authoritative cumulative quote exceeds the cap must be rejected")
	}
	if !qty.Equal(demoD("0.033")) {
		t.Fatalf("confirmed fill quantity=%s, want 0.033 so cleanup remains safely bounded", qty)
	}
}

func TestBinanceDemoIOCTerminalStatusesIncludePartialFillOutcomes(t *testing.T) {
	for _, status := range []string{"FILLED", "CANCELED", "EXPIRED", "EXPIRED_IN_MATCH"} {
		if !isBinanceDemoTerminalStatus(status) {
			t.Fatalf("status %q must be treated as an authoritative IOC terminal state", status)
		}
	}
	for _, status := range []string{"NEW", "PARTIALLY_FILLED"} {
		if isBinanceDemoTerminalStatus(status) {
			t.Fatalf("status %q must not be treated as terminal", status)
		}
	}
}

func TestValidateBinanceDemoFillAcceptsPositivePartialTerminalFill(t *testing.T) {
	for _, status := range []string{"CANCELED", "EXPIRED", "EXPIRED_IN_MATCH"} {
		resp := &sdkperp.OrderResponse{Status: status, ExecutedQty: "0.010", CumQuote: "30.25"}
		qty, err := validateBinanceDemoFill(resp, demoD("100"))
		if err != nil {
			t.Fatalf("validate partial %s fill: %v", status, err)
		}
		if !qty.Equal(demoD("0.010")) {
			t.Fatalf("partial %s fill qty=%s, want 0.010", status, qty)
		}
	}
}

func TestBinanceDemoFillRequestIsBoundedIOC(t *testing.T) {
	id := model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	req := demoFillOrderRequest(id, "fill-id", demoD("0.01"), demoD("3030"))
	if req.Type != enums.TypeLimit || req.TIF != enums.TifIOC || !req.Price.Equal(demoD("3030")) {
		t.Fatalf("fill request is not bounded IOC: %+v", req)
	}
}

func TestWaitForDemoAccountObservationRequiresMatchingPositionEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events := make(chan contract.AccountEvent, 2)
	events <- contract.BalanceEvent{Balance: model.AccountBalance{Currency: "USDT"}}
	events <- contract.PositionEvent{Position: model.Position{
		InstrumentID: model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindPerp},
		Quantity:     demoD("0.002"),
	}}

	err := waitForDemoAccountObservation(ctx, events, model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindPerp}, demoD("0.002"))
	if err != nil {
		t.Fatalf("waitForDemoAccountObservation: %v", err)
	}
}

func TestWaitForDemoAccountObservationTimesOutWithoutMatchingPosition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	events := make(chan contract.AccountEvent, 1)
	events <- contract.PositionEvent{Position: model.Position{
		InstrumentID: model.InstrumentID{Venue: "BINANCE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Quantity:     demoD("0.002"),
	}}

	err := waitForDemoAccountObservation(ctx, events, model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindPerp}, demoD("0.002"))
	if err == nil || !strings.Contains(err.Error(), "account stream") {
		t.Fatalf("expected account stream timeout, got %v", err)
	}
}
