package perp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

func TestOKXPerpDemoExposureRejectsMultipleNonZeroLegs(t *testing.T) {
	id := model.InstrumentID{Venue: venueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}
	positions := []model.Position{
		{InstrumentID: id, Side: enums.PosLong, Quantity: perpDemoDecimal("1")},
		{InstrumentID: id, Side: enums.PosShort, Quantity: perpDemoDecimal("-1")},
	}
	if _, err := demoExposureFromPositions(positions, id); err == nil || !strings.Contains(err.Error(), "multiple non-zero") {
		t.Fatalf("offsetting OKX legs err=%v, want fail-closed", err)
	}
	positions[1].Quantity = decimal.Zero
	got, err := demoExposureFromPositions(positions, id)
	if err != nil || !got.Equal(perpDemoDecimal("1")) {
		t.Fatalf("single OKX leg exposure=%s err=%v, want 1", got, err)
	}
}

func TestOKXPerpDemoCloseChecksAllPositionLegsBeforeSubmit(t *testing.T) {
	source := readOKXPerpDemoSource(t, "demo_acceptance_support_test.go")
	start := strings.Index(source, "func closeOKXPerpDemoExposure(")
	if start < 0 {
		t.Fatal("closeOKXPerpDemoExposure missing")
	}
	body := source[start:]
	exposureCheck := strings.Index(body, "demoCurrentExposure(")
	submit := strings.Index(body, "adapter.Execution.Submit(")
	if exposureCheck < 0 || submit < 0 || exposureCheck >= submit {
		t.Fatal("OKX Perp close must reject multi-leg exposure before order-book or submit side effects")
	}
}

func TestOKXPerpDemoEndpointsCustomProfile(t *testing.T) {
	endpoints := okxDemoEndpoints(t, testenv.OKXDemoConfig{
		HostProfile: testenv.OKXDemoHostProfileCustom,
		RESTBaseURL: "https://okx-rest.example.test",
		WSBaseURL:   "wss://okx-ws.example.test/",
	})

	if endpoints.REST != "https://okx-rest.example.test" {
		t.Fatalf("REST=%q", endpoints.REST)
	}
	if endpoints.WSPublic != "wss://okx-ws.example.test/ws/v5/public" {
		t.Fatalf("WSPublic=%q", endpoints.WSPublic)
	}
	if endpoints.WSPrivate != "wss://okx-ws.example.test/ws/v5/private" {
		t.Fatalf("WSPrivate=%q", endpoints.WSPrivate)
	}
	if endpoints.WSBusiness != "wss://okx-ws.example.test/ws/v5/business" {
		t.Fatalf("WSBusiness=%q", endpoints.WSBusiness)
	}
}

func TestSelectOKXPerpDemoQuantityUsesCrossedIOCPriceAndCtVal(t *testing.T) {
	spec := demoPerpSpec{
		VenueSymbol:    "BTC-USDT-SWAP",
		QuoteCurrency:  "USDT",
		SettleCurrency: "USDT",
		PriceTick:      perpDemoDecimal("0.1"),
		SizeStep:       perpDemoDecimal("1"),
		MinQty:         perpDemoDecimal("1"),
		CtVal:          perpDemoDecimal("0.01"),
		CtValCcy:       "BTC",
	}
	ask := perpDemoDecimal("100")
	fillPrice := ceilDecimalToStep(ask.Mul(perpDemoDecimal("1.01")), spec.PriceTick)

	if _, err := selectDemoPerpQuantity(spec, perpDemoDecimal("1.005"), ask); err != nil {
		t.Fatalf("ask-price sizing should fit and expose the crossed-price regression: %v", err)
	}
	if _, err := selectDemoPerpQuantity(spec, perpDemoDecimal("1.005"), fillPrice); err == nil {
		t.Fatal("crossed IOC price with ctVal multiplier must reject notional above the cap")
	}
}

func TestValidateOKXPerpDemoFillEnforcesActualCtValNotionalCap(t *testing.T) {
	spec := demoPerpSpec{
		QuoteCurrency:  "USDT",
		SettleCurrency: "USDT",
		CtVal:          perpDemoDecimal("0.01"),
		CtValCcy:       "BTC",
	}
	overCap := &okx.Order{AccFillSz: "2", AvgPx: "5001"}
	overCapQty, err := validateOKXPerpDemoFill(overCap, spec, perpDemoDecimal("100"))
	if err == nil {
		t.Fatal("actual fill notional including contract value must be capped")
	}
	if !overCapQty.Equal(perpDemoDecimal("2")) {
		t.Fatalf("over-cap contracts=%s, want authoritative quantity retained for bounded cleanup", overCapQty)
	}

	withinCap := &okx.Order{AccFillSz: "2", AvgPx: "5000"}
	qty, err := validateOKXPerpDemoFill(withinCap, spec, perpDemoDecimal("100"))
	if err != nil {
		t.Fatalf("validate bounded perp fill: %v", err)
	}
	if !qty.Equal(perpDemoDecimal("2")) {
		t.Fatalf("filled contracts=%s, want 2", qty)
	}
}

func TestOKXPerpDemoCleanupRequiresConfirmedFillAndSingleClose(t *testing.T) {
	cleanup := newDemoPerpCleanupState(demoPerpSpec{VenueSymbol: "BTC-USDT-SWAP"}, perpDemoDecimal("2"))
	cleanup.TrackOrder(demoOrderRoleOpening, "own-order")
	cleanup.RecordVenueOrderID("own-order", "venue-order")

	if cleanup.CloseAuthorized() {
		t.Fatal("ambiguous submit must not authorize exposure close")
	}
	if err := cleanup.ObserveOrder("own-order", &okx.Order{ClOrdId: "own-order", OrdId: "venue-order", State: okx.OrderStatusFilled, AccFillSz: "1.5"}); err != nil {
		t.Fatal(err)
	}
	if cleanup.CloseAuthorized() {
		t.Fatal("fill confirmation without observed account exposure must not authorize close")
	}
	cleanup.SetExposure(perpDemoDecimal("1.5"))
	if !cleanup.CloseAuthorized() || !cleanup.CloseLimit().Equal(perpDemoDecimal("1.5")) {
		t.Fatalf("close authorization=%v limit=%s, want true/1.5", cleanup.CloseAuthorized(), cleanup.CloseLimit())
	}
	cleanup.MarkCloseAttempted()
	if cleanup.CloseAuthorized() {
		t.Fatal("ambiguous close outcomes must not be retried")
	}
}

func TestOKXPerpDemoCloseQuantityNeverExceedsConfirmedFill(t *testing.T) {
	if _, err := demoPerpCloseQuantity(perpDemoDecimal("-1"), perpDemoDecimal("1")); err == nil {
		t.Fatal("unexpected short exposure after a buy lifecycle must fail closed")
	}
	if _, err := demoPerpCloseQuantity(perpDemoDecimal("1.1"), perpDemoDecimal("1")); err == nil {
		t.Fatal("exposure above the authoritative fill must fail closed")
	}
	qty, err := demoPerpCloseQuantity(perpDemoDecimal("0.8"), perpDemoDecimal("1"))
	if err != nil {
		t.Fatalf("bounded close quantity: %v", err)
	}
	if !qty.Equal(perpDemoDecimal("0.8")) {
		t.Fatalf("close qty=%s, want 0.8", qty)
	}
}

func TestDemoPerpNotionalPerContractHandlesQuoteCtVal(t *testing.T) {
	spec := demoPerpSpec{
		QuoteCurrency:  "USDT",
		SettleCurrency: "USDT",
		CtVal:          perpDemoDecimal("10"),
		CtValCcy:       "USDT",
	}
	if got := demoPerpNotionalPerContract(spec, perpDemoDecimal("5000")); !got.Equal(perpDemoDecimal("10")) {
		t.Fatalf("per-contract notional=%s, want quote ctVal 10", got)
	}
}

func TestOKXPerpDemoWritePathsStayFailClosed(t *testing.T) {
	for _, name := range []string{"demo_acceptance_test.go", "demo_runtime_acceptance_test.go"} {
		source := readOKXPerpDemoSource(t, name)
		for _, forbidden := range []string{"SkipIfTransientLiveNetworkError", ".Skip(", ".Skipf("} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s contains forbidden live-write skip path %q", name, forbidden)
			}
		}
		if !strings.Contains(source, "selectDemoPerpQuantity(spec, cfg.MaxNotionalUSDT, fillPrice)") {
			t.Fatalf("%s must size opening orders at the crossed IOC fill price", name)
		}
	}
	support := readOKXPerpDemoSource(t, "demo_acceptance_support_test.go")
	if strings.Contains(support, "CancelAll(") {
		t.Fatal("OKX Perp Demo cleanup must never cancel unrelated account orders")
	}
	runtimeSource := readOKXPerpDemoSource(t, "demo_runtime_acceptance_test.go")
	if !strings.Contains(runtimeSource, "AttachAccountRequiredRiskWithMaxNotional") {
		t.Fatal("OKX Perp Demo runtime acceptance must install the configured max-notional cap")
	}
}

func TestOKXPerpDemoOrderObservationDeduplicatesOpeningFillAndExcludesClose(t *testing.T) {
	cleanup := newDemoPerpCleanupState(demoPerpSpec{}, perpDemoDecimal("1"))
	cleanup.TrackOrder(demoOrderRoleResting, "rest")
	cleanup.TrackOrder(demoOrderRoleOpening, "open")
	cleanup.TrackOrder(demoOrderRoleClose, "close")

	for _, observation := range []struct {
		clientID string
		order    *okx.Order
	}{
		{"rest", &okx.Order{ClOrdId: "rest", OrdId: "1", State: okx.OrderStatusCanceled, AccFillSz: "0.2"}},
		{"rest", &okx.Order{ClOrdId: "rest", OrdId: "1", State: okx.OrderStatusCanceled, AccFillSz: "0.2"}},
		{"open", &okx.Order{ClOrdId: "open", OrdId: "2", State: okx.OrderStatusFilled, AccFillSz: "0.3"}},
		{"open", &okx.Order{ClOrdId: "open", OrdId: "2", State: okx.OrderStatusFilled, AccFillSz: "0.3"}},
		{"close", &okx.Order{ClOrdId: "close", OrdId: "3", State: okx.OrderStatusFilled, AccFillSz: "0.1"}},
	} {
		if err := cleanup.ObserveOrder(observation.clientID, observation.order); err != nil {
			t.Fatalf("observe %s: %v", observation.clientID, err)
		}
	}
	if got := cleanup.CloseLimit(); !got.Equal(perpDemoDecimal("0.5")) {
		t.Fatalf("close limit=%s, want deduplicated resting+opening fill 0.5", got)
	}
	if got := cleanup.RestingFillQuantity(); !got.Equal(perpDemoDecimal("0.2")) {
		t.Fatalf("resting fill=%s, want 0.2", got)
	}
	if cleanup.OpeningAllowed() {
		t.Fatal("resting partial fill must block the IOC opening order")
	}
}

func TestOKXPerpDemoCancelConfirmsTerminalPartialFill(t *testing.T) {
	cleanup := newDemoPerpCleanupState(demoPerpSpec{}, perpDemoDecimal("1"))
	cleanup.TrackOrder(demoOrderRoleResting, "rest")
	cleanup.RecordVenueOrderID("rest", "venue-rest")
	cancelCalls := 0
	err := cancelAndConfirmTrackedDemoOrder(
		context.Background(),
		&cleanup,
		"rest",
		func(context.Context, string) error {
			cancelCalls++
			return nil
		},
		func(context.Context, string, string) (*okx.Order, error) {
			return &okx.Order{ClOrdId: "rest", OrdId: "venue-rest", State: okx.OrderStatusCanceled, AccFillSz: "0.2"}, nil
		},
	)
	if err != nil {
		t.Fatalf("cancel and confirm: %v", err)
	}
	if cancelCalls != 1 || !cleanup.RestingFillQuantity().Equal(perpDemoDecimal("0.2")) {
		t.Fatalf("cancelCalls=%d restingFill=%s", cancelCalls, cleanup.RestingFillQuantity())
	}
	if cleanup.OpeningAllowed() {
		t.Fatal("cancel returning nil must not hide a terminal partial fill")
	}
}

func TestOKXPerpDemoAmbiguousSubmitRecoversByClientID(t *testing.T) {
	cleanup := newDemoPerpCleanupState(demoPerpSpec{}, perpDemoDecimal("1"))
	cleanup.TrackOrder(demoOrderRoleOpening, "ambiguous")
	var gotVenueID, gotClientID string
	err := recoverAmbiguousTrackedDemoOrder(
		context.Background(),
		&cleanup,
		"ambiguous",
		func(_ context.Context, venueID, clientID string) (*okx.Order, error) {
			gotVenueID, gotClientID = venueID, clientID
			return &okx.Order{ClOrdId: clientID, OrdId: "venue-open", State: okx.OrderStatusCanceled, AccFillSz: "0.4"}, nil
		},
	)
	if err != nil {
		t.Fatalf("recover ambiguous submit: %v", err)
	}
	if gotVenueID != "" || gotClientID != "ambiguous" {
		t.Fatalf("lookup used venue=%q client=%q, want client-only lookup", gotVenueID, gotClientID)
	}
	if got := cleanup.CloseLimit(); !got.Equal(perpDemoDecimal("0.4")) {
		t.Fatalf("recovered opening fill=%s, want 0.4", got)
	}
}

func TestOKXPerpDemoCloseAuthorizationConsumesOnlyAtSubmitBoundary(t *testing.T) {
	cleanup := newDemoPerpCleanupState(demoPerpSpec{}, perpDemoDecimal("1"))
	cleanup.TrackOrder(demoOrderRoleOpening, "open")
	if err := cleanup.ObserveOrder("open", &okx.Order{ClOrdId: "open", OrdId: "1", State: okx.OrderStatusFilled, AccFillSz: "0.5"}); err != nil {
		t.Fatal(err)
	}
	cleanup.SetExposure(perpDemoDecimal("0.5"))
	if !cleanup.CloseAuthorized() || !cleanup.CloseLimit().Equal(perpDemoDecimal("0.5")) {
		t.Fatal("close must be authorized after authoritative fill and exposure observation")
	}
	if !cleanup.CloseAuthorized() || !cleanup.CloseLimit().Equal(perpDemoDecimal("0.5")) {
		t.Fatal("pre-submit reads/errors must not consume close authorization")
	}
	cleanup.MarkCloseAttempted()
	if cleanup.CloseAuthorized() {
		t.Fatal("once Submit is invoked, close authorization must be permanently consumed")
	}
}

func TestOKXPerpDemoStableReadsRejectZeroThenNonZero(t *testing.T) {
	stable := newDemoStableReads(2)
	if stable.Observe(true) {
		t.Fatal("one flat/open-free read must not pass")
	}
	if stable.Observe(false) {
		t.Fatal("flat followed by non-flat must reset stability")
	}
	if stable.Observe(true) || !stable.Observe(true) {
		t.Fatal("only two consecutive stable reads may pass")
	}
}

func readOKXPerpDemoSource(t *testing.T, name string) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve OKX Perp Demo test source path")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(current), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func perpDemoDecimal(value string) decimal.Decimal {
	return decimal.RequireFromString(value)
}
