package spot

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

func demoD(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestSpotDemoAcceptanceNormalizeSymbol(t *testing.T) {
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

func TestSpotDemoAcceptanceSymbolSpecFromExchangeInfo(t *testing.T) {
	info := &sdkspot.ExchangeInfoResponse{Symbols: []sdkspot.SymbolInfo{{
		Symbol:     "ETHUSDT",
		Status:     "TRADING",
		BaseAsset:  "ETH",
		QuoteAsset: "USDT",
		Filters: []map[string]any{
			{"filterType": "PRICE_FILTER", "tickSize": "0.01"},
			{"filterType": "LOT_SIZE", "stepSize": "0.0001", "minQty": "0.0001"},
			{"filterType": "MIN_NOTIONAL", "minNotional": "5"},
		},
	}}}

	spec, err := demoAcceptanceSymbolSpecFromExchangeInfo(info, "eth-usdt")
	if err != nil {
		t.Fatalf("demoAcceptanceSymbolSpecFromExchangeInfo: %v", err)
	}
	if spec.VenueSymbol != "ETHUSDT" || spec.BaseCurrency != "ETH" || spec.QuoteCurrency != "USDT" {
		t.Fatalf("unexpected symbol spec identity: %+v", spec)
	}
	if !spec.PriceTick.Equal(demoD("0.01")) || !spec.SizeStep.Equal(demoD("0.0001")) ||
		!spec.MinQty.Equal(demoD("0.0001")) || !spec.MinNotional.Equal(demoD("5")) {
		t.Fatalf("unexpected filters: %+v", spec)
	}
}

func TestSpotDemoAcceptanceSelectOrderQuantityChoosesMinTradableStep(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{
		VenueSymbol: "ETHUSDT",
		SizeStep:    demoD("0.0001"),
		MinQty:      demoD("0.0001"),
		MinNotional: demoD("5"),
	}

	qty, err := selectDemoAcceptanceOrderQuantity(spec, decimal.Zero, demoD("100"), demoD("3000"))
	if err != nil {
		t.Fatalf("selectDemoAcceptanceOrderQuantity: %v", err)
	}
	if !qty.Equal(demoD("0.0017")) {
		t.Fatalf("qty=%s, want 0.0017", qty)
	}
}

func TestSpotDemoAcceptanceSelectOrderQuantityRejectsOverMaxNotional(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{
		VenueSymbol: "BTCUSDT",
		SizeStep:    demoD("0.00001"),
		MinQty:      demoD("0.00001"),
		MinNotional: demoD("5"),
	}

	if _, err := selectDemoAcceptanceOrderQuantity(spec, demoD("0.01"), demoD("100"), demoD("65000")); err == nil {
		t.Fatalf("expected over-max notional rejection")
	}
}

func TestSpotDemoAcceptanceSelectOrderQuantityUsesIOCPriceForMaxNotional(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{
		VenueSymbol: "ETHUSDT",
		SizeStep:    demoD("0.001"),
		MinQty:      demoD("0.001"),
		MinNotional: demoD("5"),
	}
	if _, err := selectDemoAcceptanceOrderQuantityForPriceBand(spec, demoD("0.033"), demoD("100"), demoD("2850"), demoD("3000")); err != nil {
		t.Fatalf("configured quantity should fit at uncrossed price: %v", err)
	}
	if _, err := selectDemoAcceptanceOrderQuantityForPriceBand(spec, demoD("0.033"), demoD("100"), demoD("2850"), demoD("3031")); err == nil {
		t.Fatal("configured quantity must be rejected at the higher IOC lifecycle price")
	}
}

func TestSpotDemoAcceptanceDefaultMaxNotionalIs100USDT(t *testing.T) {
	t.Setenv("BINANCE_DEMO_MAX_NOTIONAL_USDT", "")

	got := demoDecimalEnvOrDefault(t, "BINANCE_DEMO_MAX_NOTIONAL_USDT", demoDefaultMaxNotionalUSDT)
	if !got.Equal(demoD("100")) {
		t.Fatalf("default Demo max notional=%s, want 100", got)
	}
}

func TestSpotDemoAcceptanceCleanupMetadataRemediation(t *testing.T) {
	meta := demoAcceptanceCleanupMetadata{
		Symbol:         "ETHUSDT",
		Side:           "BUY",
		Quantity:       demoD("0.002"),
		VenueOrderIDs:  []string{"12345"},
		ClientOrderIDs: []string{"btds-demo-1"},
		BaseCurrency:   "ETH",
		QuoteCurrency:  "USDT",
		BaseDelta:      demoD("0.002"),
	}

	got := meta.Remediation()
	for _, want := range []string{"ETHUSDT", "BUY", "0.002", "12345", "btds-demo-1", "ETH", "USDT"} {
		if !strings.Contains(got, want) {
			t.Fatalf("remediation %q missing %q", got, want)
		}
	}
}

func TestSpotDemoCleanupRequiresConfirmedFillAndCapsClose(t *testing.T) {
	spec := demoAcceptanceSymbolSpec{VenueSymbol: "ETHUSDT", BaseCurrency: "ETH", QuoteCurrency: "USDT"}
	cleanup := newDemoAcceptanceCleanupState(spec, demoD("0.002"))
	cleanup.Arm(enums.SideBuy, "ambiguous")
	cleanup.RecordVenueOrderID("12345")
	if cleanup.CloseAuthorized() || !cleanup.CloseLimit().IsZero() {
		t.Fatalf("ambiguous submit authorized close: authorized=%v limit=%s", cleanup.CloseAuthorized(), cleanup.CloseLimit())
	}
	if got := cleanup.CancellableVenueOrderIDs(); len(got) != 1 || got[0] != "12345" {
		t.Fatalf("cancellable ids=%v, want recorded acceptance order", got)
	}
	tracked := cleanup.TrackedOpenOrders()
	if len(tracked) != 1 || tracked[0].VenueOrderID != "12345" || tracked[0].ClientID != "ambiguous" {
		t.Fatalf("tracked orders=%+v, want venue/client identity pair", tracked)
	}
	cleanup.ConfirmFill(demoD("0.0015"))
	if !cleanup.CloseAuthorized() || !cleanup.CloseLimit().Equal(demoD("0.0015")) {
		t.Fatalf("confirmed fill not reflected in close cap: authorized=%v limit=%s", cleanup.CloseAuthorized(), cleanup.CloseLimit())
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

func TestInspectRecordedSpotDemoOrdersRecoversClientOnlyPartialFill(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState(demoAcceptanceSymbolSpec{VenueSymbol: "ETHUSDT"}, demoD("0.020"))
	cleanup.Arm(enums.SideBuy, "ambiguous-client")

	err := inspectRecordedSpotDemoOrdersWithLookup(cleanup.TrackedOpenOrders(), demoD("100"), false, &cleanup, func(tracked demoAcceptanceTrackedOrder) (*sdkspot.OrderResponse, error) {
		if tracked.VenueOrderID != "" || tracked.ClientID != "ambiguous-client" {
			t.Fatalf("lookup tracked=%+v, want client-only identity", tracked)
		}
		return &sdkspot.OrderResponse{
			OrderID:             12345,
			ClientOrderID:       tracked.ClientID,
			Status:              "EXPIRED",
			ExecutedQty:         "0.010",
			CummulativeQuoteQty: "30.25",
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

func TestInspectRecordedSpotDemoOrdersKeepsMalformedTerminalEvidenceVisible(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState(demoAcceptanceSymbolSpec{VenueSymbol: "ETHUSDT"}, demoD("0.020"))
	cleanup.Arm(enums.SideBuy, "ambiguous-client")
	cleanup.RecordVenueOrderID("12345")

	err := inspectRecordedSpotDemoOrdersWithLookup(cleanup.TrackedOpenOrders(), demoD("100"), false, &cleanup, func(demoAcceptanceTrackedOrder) (*sdkspot.OrderResponse, error) {
		return &sdkspot.OrderResponse{OrderID: 12345, ClientOrderID: "ambiguous-client", Status: "CANCELED", ExecutedQty: "malformed"}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "invalid executed quantity") {
		t.Fatalf("malformed terminal evidence err=%v, want explicit parse failure", err)
	}
	if got := cleanup.TrackedOpenOrders(); len(got) != 1 {
		t.Fatalf("malformed terminal evidence was silently removed from tracking: %+v", got)
	}
	if cleanup.CloseAuthorized() {
		t.Fatal("malformed terminal evidence authorized inventory cleanup")
	}
}

func TestResolveUnresolvedSpotDemoOrderDoesNotSwallowMalformedTerminalEvidence(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState(demoAcceptanceSymbolSpec{VenueSymbol: "ETHUSDT"}, demoD("0.020"))
	cleanup.Arm(enums.SideBuy, "ambiguous-client")
	err := resolveUnresolvedSpotDemoOrdersWithRetry(context.Background(), &cleanup, demoD("100"), 1, 0, func(demoAcceptanceTrackedOrder) (*sdkspot.OrderResponse, error) {
		return &sdkspot.OrderResponse{OrderID: 12345, ClientOrderID: "ambiguous-client", Status: "CANCELED", ExecutedQty: "malformed"}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "invalid executed quantity") {
		t.Fatalf("resolve malformed terminal evidence err=%v, want propagated parse failure", err)
	}
}

func TestBinanceSpotCleanupDoesNotDiscardInitialInspectionErrors(t *testing.T) {
	source, err := os.ReadFile("demo_acceptance_support_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "_ = inspectRecordedSpotDemoOrders(") {
		t.Fatal("Binance Spot cleanup discards the first authoritative order-inspection error")
	}
}

func TestInspectRecordedSpotDemoOrdersResolvesClientOnlyOpenOrderForScopedCancel(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState(demoAcceptanceSymbolSpec{VenueSymbol: "ETHUSDT"}, demoD("0.020"))
	cleanup.Arm(enums.SideBuy, "ambiguous-client")

	err := inspectRecordedSpotDemoOrdersWithLookup(cleanup.TrackedOpenOrders(), demoD("100"), false, &cleanup, func(tracked demoAcceptanceTrackedOrder) (*sdkspot.OrderResponse, error) {
		return &sdkspot.OrderResponse{OrderID: 12345, ClientOrderID: tracked.ClientID, Status: "NEW", ExecutedQty: "0"}, nil
	})
	if err != nil {
		t.Fatalf("inspect client-only open order: %v", err)
	}
	if got := cleanup.CancellableVenueOrderIDs(); len(got) != 1 || got[0] != "12345" {
		t.Fatalf("scoped cancellable ids=%v, want [12345]", got)
	}
}

func TestResolveUnresolvedSpotDemoOrderRetriesThenScopesOpenOrderCancel(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState(demoAcceptanceSymbolSpec{VenueSymbol: "ETHUSDT"}, demoD("0.020"))
	cleanup.Arm(enums.SideBuy, "ambiguous-client")
	lookupCalls := 0
	err := resolveUnresolvedSpotDemoOrdersWithRetry(context.Background(), &cleanup, demoD("100"), 2, 0, func(tracked demoAcceptanceTrackedOrder) (*sdkspot.OrderResponse, error) {
		lookupCalls++
		if lookupCalls == 1 {
			return nil, errors.New("temporary query failure")
		}
		return &sdkspot.OrderResponse{OrderID: 12345, ClientOrderID: tracked.ClientID, Status: "NEW", ExecutedQty: "0"}, nil
	})
	if err != nil {
		t.Fatalf("resolve retried open order: %v", err)
	}
	var canceled []string
	if err := cancelRecordedSpotDemoOrders(&cleanup, func(venueOrderID string) error {
		canceled = append(canceled, venueOrderID)
		return nil
	}, func() error { return nil }); err != nil {
		t.Fatalf("cancel resolved order: %v", err)
	}
	if lookupCalls != 2 || len(canceled) != 1 || canceled[0] != "12345" {
		t.Fatalf("lookupCalls=%d canceled=%v, want 2 and [12345]", lookupCalls, canceled)
	}
}

func TestResolveUnresolvedSpotDemoOrderRetriesThenRecoversTerminalPartialFill(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState(demoAcceptanceSymbolSpec{VenueSymbol: "ETHUSDT"}, demoD("0.020"))
	cleanup.Arm(enums.SideBuy, "ambiguous-client")
	lookupCalls := 0
	err := resolveUnresolvedSpotDemoOrdersWithRetry(context.Background(), &cleanup, demoD("100"), 2, 0, func(tracked demoAcceptanceTrackedOrder) (*sdkspot.OrderResponse, error) {
		lookupCalls++
		if lookupCalls == 1 {
			return nil, errors.New("temporary query failure")
		}
		return &sdkspot.OrderResponse{OrderID: 12345, ClientOrderID: tracked.ClientID, Status: "EXPIRED_IN_MATCH", ExecutedQty: "0.010", CummulativeQuoteQty: "30.25"}, nil
	})
	if err != nil {
		t.Fatalf("resolve retried terminal partial order: %v", err)
	}
	if lookupCalls != 2 || !cleanup.CloseAuthorized() || !cleanup.CloseLimit().Equal(demoD("0.010")) {
		t.Fatalf("lookupCalls=%d authorized=%v limit=%s", lookupCalls, cleanup.CloseAuthorized(), cleanup.CloseLimit())
	}
}

func TestCancelRecordedSpotDemoOrdersKeepsOrdersTrackedUntilNoOpenConfirmed(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState(demoAcceptanceSymbolSpec{VenueSymbol: "ETHUSDT"}, demoD("0.002"))
	cleanup.Arm(enums.SideBuy, "bolter-demo-rest")
	cleanup.RecordVenueOrderID("12345")

	wantErr := errors.New("open order still visible")
	var canceled []string
	err := cancelRecordedSpotDemoOrders(
		&cleanup,
		func(venueOrderID string) error {
			canceled = append(canceled, venueOrderID)
			return nil
		},
		func() error { return wantErr },
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("cancelRecordedSpotDemoOrders error=%v, want %v", err, wantErr)
	}
	if len(canceled) != 1 || canceled[0] != "12345" {
		t.Fatalf("canceled ids=%v, want [12345]", canceled)
	}
	if got := cleanup.CancellableVenueOrderIDs(); len(got) != 1 || got[0] != "12345" {
		t.Fatalf("orders were untracked before authoritative no-open confirmation: %v", got)
	}

	if err := cancelRecordedSpotDemoOrders(&cleanup, func(string) error { return nil }, func() error { return nil }); err != nil {
		t.Fatalf("confirmed cancellation: %v", err)
	}
	if got := cleanup.CancellableVenueOrderIDs(); len(got) != 0 {
		t.Fatalf("terminal orders still tracked after no-open confirmation: %v", got)
	}
}

func TestCancelRecordedSpotDemoOrdersAcceptsCancelErrorAfterAuthoritativeNoOpen(t *testing.T) {
	cleanup := newDemoAcceptanceCleanupState(demoAcceptanceSymbolSpec{VenueSymbol: "ETHUSDT"}, demoD("0.002"))
	cleanup.Arm(enums.SideBuy, "bolter-demo-rest")
	cleanup.RecordVenueOrderID("12345")

	if err := cancelRecordedSpotDemoOrders(&cleanup, func(string) error {
		return errors.New("venue reports order already terminal")
	}, func() error { return nil }); err != nil {
		t.Fatalf("authoritative no-open must supersede cancel endpoint error: %v", err)
	}
	if got := cleanup.CancellableVenueOrderIDs(); len(got) != 0 {
		t.Fatalf("terminal order remains tracked after authoritative no-open: %v", got)
	}
}

func TestValidateBinanceSpotDemoFillRejectsCumQuoteOverCap(t *testing.T) {
	resp := &sdkspot.OrderResponse{ExecutedQty: "0.033", CummulativeQuoteQty: "100.01"}
	qty, err := validateBinanceSpotDemoFill(resp, demoD("100"))
	if err == nil {
		t.Fatal("fill whose authoritative cumulative quote exceeds the cap must be rejected")
	}
	if !qty.Equal(demoD("0.033")) {
		t.Fatalf("confirmed fill quantity=%s, want 0.033 so cleanup remains safely bounded", qty)
	}
}

func TestBinanceSpotDemoIOCTerminalStatusesIncludePartialFillOutcomes(t *testing.T) {
	for _, status := range []string{"FILLED", "CANCELED", "EXPIRED", "EXPIRED_IN_MATCH"} {
		if !isBinanceSpotDemoTerminalStatus(status) {
			t.Fatalf("status %q must be treated as an authoritative IOC terminal state", status)
		}
	}
	for _, status := range []string{"NEW", "PARTIALLY_FILLED"} {
		if isBinanceSpotDemoTerminalStatus(status) {
			t.Fatalf("status %q must not be treated as terminal", status)
		}
	}
}

func TestValidateBinanceSpotDemoFillAcceptsPositivePartialTerminalFill(t *testing.T) {
	for _, status := range []string{"CANCELED", "EXPIRED", "EXPIRED_IN_MATCH"} {
		resp := &sdkspot.OrderResponse{Status: status, ExecutedQty: "0.010", CummulativeQuoteQty: "30.25"}
		qty, err := validateBinanceSpotDemoFill(resp, demoD("100"))
		if err != nil {
			t.Fatalf("validate partial %s fill: %v", status, err)
		}
		if !qty.Equal(demoD("0.010")) {
			t.Fatalf("partial %s fill qty=%s, want 0.010", status, qty)
		}
	}
}

func TestBinanceSpotDemoFillObservationThresholdAllowsBaseAssetFee(t *testing.T) {
	got := demoSpotFillObservationThreshold(demoD("0.001"), demoD("0.001"))
	if !got.Equal(demoD("0.0005")) {
		t.Fatalf("observation threshold=%s, want 0.0005", got)
	}
	got = demoSpotFillObservationThreshold(demoD("0.0002"), demoD("0.001"))
	if !got.Equal(demoD("0.0001")) {
		t.Fatalf("small partial-fill threshold=%s, want 0.0001", got)
	}
}

func TestBinanceSpotDemoCloseConsumesAuthorizationAtSubmitBoundary(t *testing.T) {
	supportSource, err := os.ReadFile("demo_acceptance_support_test.go")
	if err != nil {
		t.Fatalf("read adapter acceptance support source: %v", err)
	}
	adapterBody := string(supportSource)
	start := strings.Index(adapterBody, "func closeBinanceSpotDemoBaseDelta")
	if start < 0 {
		t.Fatal("closeBinanceSpotDemoBaseDelta source not found")
	}
	adapterBody = adapterBody[start:]
	balances := strings.Index(adapterBody, "demoSpotBalances")
	ticker := strings.Index(adapterBody, "adapter.rest.BookTicker")
	arm := strings.Index(adapterBody, "state.Arm(enums.SideSell")
	begin := strings.Index(adapterBody, "state.BeginCloseAttempt()")
	submit := strings.Index(adapterBody, "adapter.Execution.Submit")
	if !(balances >= 0 && balances < ticker && ticker < arm && arm < begin && begin < submit) {
		t.Fatalf("adapter close boundary order invalid: balances=%d ticker=%d arm=%d begin=%d submit=%d", balances, ticker, arm, begin, submit)
	}

	runtimeSource, err := os.ReadFile("demo_runtime_acceptance_test.go")
	if err != nil {
		t.Fatalf("read runtime acceptance source: %v", err)
	}
	runtimeBody := string(runtimeSource)
	closeBlock := strings.Index(runtimeBody, "closeTicker, err :=")
	if closeBlock < 0 {
		t.Fatal("runtime close ticker preflight source not found")
	}
	runtimeBody = runtimeBody[closeBlock:]
	ticker = strings.Index(runtimeBody, "closeTicker, err :=")
	arm = strings.Index(runtimeBody, "cleanup.Arm(enums.SideSell")
	begin = strings.Index(runtimeBody, "cleanup.BeginCloseAttempt()")
	submit = strings.Index(runtimeBody, "node.Exec.Submit")
	if !(ticker >= 0 && ticker < arm && arm < begin && begin < submit) {
		t.Fatalf("runtime close boundary order invalid: ticker=%d arm=%d begin=%d submit=%d", ticker, arm, begin, submit)
	}
}

func TestBinanceSpotDemoFillRequestIsBoundedIOC(t *testing.T) {
	id := model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT"}
	req := demoFillOrderRequest(id, "fill-id", demoD("0.01"), demoD("3030"))
	if req.Type != enums.TypeLimit || req.TIF != enums.TifIOC || !req.Price.Equal(demoD("3030")) {
		t.Fatalf("fill request is not bounded IOC: %+v", req)
	}
}

func TestSpotDemoClientOrderIDFitsBinanceLimit(t *testing.T) {
	for _, kind := range []string{"rest", "fill", "close"} {
		id := demoClientOrderID(kind)
		if len(id) >= 36 {
			t.Fatalf("client order id %q length=%d, want <36", id, len(id))
		}
		if !strings.Contains(id, kind) {
			t.Fatalf("client order id %q should include kind %q", id, kind)
		}
	}
}

func TestSpotDemoHTTPClientRejectsInvalidProxy(t *testing.T) {
	t.Setenv("PROXY", ":// bad proxy")

	if _, err := demoHTTPClient(time.Second); err == nil {
		t.Fatalf("expected invalid proxy error")
	}
}

func TestSpotDemoHTTPClientIgnoresInheritedProxyEnv(t *testing.T) {
	t.Setenv("PROXY", "")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:65535")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:65535")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:65535")

	client, err := demoHTTPClient(time.Second)
	if err != nil {
		t.Fatalf("demoHTTPClient: %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport=%T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatalf("demo HTTP client must ignore inherited proxy env unless PROXY is set")
	}
}

func TestWaitForDemoSpotBalanceObservationRequiresCurrencyDelta(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events := make(chan contract.AccountEvent, 2)
	events <- contract.BalanceEvent{Balance: model.AccountBalance{Currency: "USDT", Total: demoD("10")}}
	events <- contract.BalanceEvent{Balance: model.AccountBalance{Currency: "ETH", Total: demoD("1.2")}}

	if err := waitForDemoSpotBalanceObservation(ctx, events, "ETH", demoD("1"), demoD("0.1")); err != nil {
		t.Fatalf("waitForDemoSpotBalanceObservation: %v", err)
	}
}

func TestWaitForDemoSpotBalanceObservationTimesOutWithoutDelta(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	events := make(chan contract.AccountEvent, 1)
	events <- contract.BalanceEvent{Balance: model.AccountBalance{Currency: "ETH", Total: demoD("1.01")}}

	err := waitForDemoSpotBalanceObservation(ctx, events, "ETH", demoD("1"), demoD("0.1"))
	if err == nil || !strings.Contains(err.Error(), "balance stream") {
		t.Fatalf("expected balance stream timeout, got %v", err)
	}
}

func TestDemoSpotPortfolioDeltaWithinUsesPreFillBaselineAndUpperBound(t *testing.T) {
	baseline := demoD("5")
	want := demoD("0.9")
	tolerance := demoD("0.000001")
	for _, tc := range []struct {
		name    string
		current string
		wantOK  bool
	}{
		{name: "observed net delta", current: "5.9", wantOK: true},
		{name: "historical baseline cannot pass", current: "5", wantOK: false},
		{name: "gross quantity cannot pass", current: "6", wantOK: false},
		{name: "small rounding tolerance", current: "5.9000005", wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := demoSpotPortfolioDeltaWithin(demoD(tc.current), baseline, want, tolerance); got != tc.wantOK {
				t.Fatalf("current=%s baseline=%s want=%s tolerance=%s got=%v want=%v", tc.current, baseline, want, tolerance, got, tc.wantOK)
			}
		})
	}
}
