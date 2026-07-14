package spot

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

func TestOKXSpotDemoEndpointsCustomProfile(t *testing.T) {
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

func TestSelectOKXSpotDemoQuantityUsesCrossedIOCPrice(t *testing.T) {
	spec := demoSpotSpec{
		VenueSymbol: "BTC-USDT",
		PriceTick:   spotDemoDecimal("0.1"),
		SizeStep:    spotDemoDecimal("1"),
		MinQty:      spotDemoDecimal("1"),
	}
	ask := spotDemoDecimal("100")
	fillPrice := ceilDecimalToStep(ask.Mul(spotDemoDecimal("1.01")), spec.PriceTick)

	if _, err := selectDemoSpotQuantity(spec, spotDemoDecimal("100.5"), ask); err != nil {
		t.Fatalf("ask-price sizing should fit the cap and expose the regression setup: %v", err)
	}
	if _, err := selectDemoSpotQuantity(spec, spotDemoDecimal("100.5"), fillPrice); err == nil {
		t.Fatal("crossed IOC price must reject a minimum quantity whose fill notional exceeds the cap")
	}
}

func TestValidateOKXSpotDemoFillEnforcesActualNotionalCap(t *testing.T) {
	overCap := &okx.Order{AccFillSz: "1", AvgPx: "100.01"}
	overCapQty, err := validateOKXSpotDemoFill(overCap, spotDemoDecimal("100"))
	if err == nil {
		t.Fatal("actual filled notional above the configured cap must be rejected")
	}
	if !overCapQty.Equal(spotDemoDecimal("1")) {
		t.Fatalf("over-cap fill qty=%s, want authoritative quantity retained for bounded cleanup", overCapQty)
	}

	withinCap := &okx.Order{AccFillSz: "0.5", AvgPx: "100"}
	qty, err := validateOKXSpotDemoFill(withinCap, spotDemoDecimal("100"))
	if err != nil {
		t.Fatalf("validate bounded fill: %v", err)
	}
	if !qty.Equal(spotDemoDecimal("0.5")) {
		t.Fatalf("filled qty=%s, want 0.5", qty)
	}
}

func TestOKXSpotDemoCleanupRequiresConfirmedFillAndSingleClose(t *testing.T) {
	spec := demoSpotSpec{VenueSymbol: "BTC-USDT", BaseCurrency: "BTC", QuoteCurrency: "USDT"}
	cleanup := newDemoSpotCleanupState(spec, spotDemoDecimal("1"))
	cleanup.TrackOrder(demoOrderRoleOpening, "acceptance-order")
	cleanup.RecordVenueOrderID("acceptance-order", "venue-order")

	if cleanup.CloseAuthorized() {
		t.Fatal("ambiguous submit must not authorize inventory close")
	}
	if got := cleanup.PendingOrders(); len(got) != 1 || got[0].VenueOrderID != "venue-order" {
		t.Fatalf("pending orders=%v, want only the recorded acceptance order", got)
	}

	if err := cleanup.ObserveOrder("acceptance-order", &okx.Order{ClOrdId: "acceptance-order", OrdId: "venue-order", State: okx.OrderStatusFilled, AccFillSz: "0.8"}); err != nil {
		t.Fatal(err)
	}
	if cleanup.CloseAuthorized() {
		t.Fatal("fill confirmation without an observed inventory delta must not authorize close")
	}
	cleanup.SetBaseDelta(spotDemoDecimal("0.79"))
	if !cleanup.CloseAuthorized() || !cleanup.CloseLimit().Equal(spotDemoDecimal("0.8")) {
		t.Fatalf("close authorization=%v limit=%s, want true/0.8", cleanup.CloseAuthorized(), cleanup.CloseLimit())
	}
	cleanup.MarkCloseAttempted()
	if cleanup.CloseAuthorized() {
		t.Fatal("a close Submit attempt must consume authorization so ambiguous outcomes are not retried")
	}
}

func TestOKXSpotDemoCloseQuantityNeverExceedsConfirmedFill(t *testing.T) {
	spec := demoSpotSpec{SizeStep: spotDemoDecimal("0.1"), MinQty: spotDemoDecimal("0.1")}
	if _, err := demoSpotCloseQuantity(spotDemoDecimal("1.1"), spotDemoDecimal("1"), spec); err == nil {
		t.Fatal("inventory above the authoritative fill must fail closed")
	}
	if _, err := demoSpotCloseQuantity(spotDemoDecimal("-0.1"), spotDemoDecimal("1"), spec); err == nil {
		t.Fatal("negative inventory delta must fail closed")
	}
	qty, err := demoSpotCloseQuantity(spotDemoDecimal("0.96"), spotDemoDecimal("1"), spec)
	if err != nil {
		t.Fatalf("bounded close quantity: %v", err)
	}
	if !qty.Equal(spotDemoDecimal("0.9")) {
		t.Fatalf("close qty=%s, want 0.9", qty)
	}
}

func TestOKXSpotDemoCleanupTracksOnlyAcceptanceOrders(t *testing.T) {
	spec := demoSpotSpec{VenueSymbol: "BTC-USDT"}
	cleanup := newDemoSpotCleanupState(spec, decimal.NewFromInt(1))
	cleanup.TrackOrder(demoOrderRoleOpening, "own-2")
	cleanup.TrackOrder(demoOrderRoleOpening, "own-1")
	cleanup.RecordVenueOrderID("own-2", "venue-2")
	cleanup.RecordVenueOrderID("own-1", "venue-1")
	if err := cleanup.ObserveOrder("own-2", &okx.Order{ClOrdId: "own-2", OrdId: "venue-2", State: okx.OrderStatusCanceled}); err != nil {
		t.Fatal(err)
	}

	got := cleanup.PendingOrders()
	if len(got) != 1 || got[0].ClientOrderID != "own-1" {
		t.Fatalf("pending orders=%v, want only own-1", got)
	}
	if cleanup.needed != true || cleanup.spec.VenueSymbol != "BTC-USDT" {
		t.Fatalf("unexpected cleanup state: %+v", cleanup)
	}
}

func TestOKXSpotDemoWritePathsStayFailClosed(t *testing.T) {
	for _, name := range []string{"demo_acceptance_test.go", "demo_runtime_acceptance_test.go"} {
		source := readOKXSpotDemoSource(t, name)
		for _, forbidden := range []string{"SkipIfTransientLiveNetworkError", ".Skip(", ".Skipf("} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s contains forbidden live-write skip path %q", name, forbidden)
			}
		}
		if !strings.Contains(source, "selectDemoSpotQuantity(spec, cfg.MaxNotionalUSDT, fillPrice)") {
			t.Fatalf("%s must size opening orders at the crossed IOC fill price", name)
		}
	}
	support := readOKXSpotDemoSource(t, "demo_acceptance_support_test.go")
	if strings.Contains(support, "CancelAll(") {
		t.Fatal("OKX Spot Demo cleanup must never cancel unrelated account orders")
	}
	runtimeSource := readOKXSpotDemoSource(t, "demo_runtime_acceptance_test.go")
	if !strings.Contains(runtimeSource, "AttachAccountRequiredRiskWithMaxNotional") {
		t.Fatal("OKX Spot Demo runtime acceptance must install the configured max-notional cap")
	}
}

func TestOKXSpotDemoOrderObservationDeduplicatesOpeningFillAndExcludesClose(t *testing.T) {
	cleanup := newDemoSpotCleanupState(demoSpotSpec{}, spotDemoDecimal("1"))
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
	if got := cleanup.CloseLimit(); !got.Equal(spotDemoDecimal("0.5")) {
		t.Fatalf("close limit=%s, want deduplicated resting+opening fill 0.5", got)
	}
	if got := cleanup.RestingFillQuantity(); !got.Equal(spotDemoDecimal("0.2")) {
		t.Fatalf("resting fill=%s, want 0.2", got)
	}
	if cleanup.OpeningAllowed() {
		t.Fatal("resting partial fill must block the IOC opening order")
	}
}

func TestOKXSpotDemoCancelConfirmsTerminalPartialFill(t *testing.T) {
	cleanup := newDemoSpotCleanupState(demoSpotSpec{}, spotDemoDecimal("1"))
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
	if cancelCalls != 1 || !cleanup.RestingFillQuantity().Equal(spotDemoDecimal("0.2")) {
		t.Fatalf("cancelCalls=%d restingFill=%s", cancelCalls, cleanup.RestingFillQuantity())
	}
	if cleanup.OpeningAllowed() {
		t.Fatal("cancel returning nil must not hide a terminal partial fill")
	}
}

func TestOKXSpotDemoAmbiguousSubmitRecoversByClientID(t *testing.T) {
	cleanup := newDemoSpotCleanupState(demoSpotSpec{}, spotDemoDecimal("1"))
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
	if got := cleanup.CloseLimit(); !got.Equal(spotDemoDecimal("0.4")) {
		t.Fatalf("recovered opening fill=%s, want 0.4", got)
	}
}

func TestOKXSpotDemoCloseAuthorizationConsumesOnlyAtSubmitBoundary(t *testing.T) {
	cleanup := newDemoSpotCleanupState(demoSpotSpec{}, spotDemoDecimal("1"))
	cleanup.TrackOrder(demoOrderRoleOpening, "open")
	if err := cleanup.ObserveOrder("open", &okx.Order{ClOrdId: "open", OrdId: "1", State: okx.OrderStatusFilled, AccFillSz: "0.5"}); err != nil {
		t.Fatal(err)
	}
	cleanup.SetBaseDelta(spotDemoDecimal("0.49"))
	if !cleanup.CloseAuthorized() || !cleanup.CloseLimit().Equal(spotDemoDecimal("0.5")) {
		t.Fatal("close must be authorized after authoritative fill and inventory observation")
	}
	if !cleanup.CloseAuthorized() || !cleanup.CloseLimit().Equal(spotDemoDecimal("0.5")) {
		t.Fatal("pre-submit reads/errors must not consume close authorization")
	}
	cleanup.MarkCloseAttempted()
	if cleanup.CloseAuthorized() {
		t.Fatal("once Submit is invoked, close authorization must be permanently consumed")
	}
}

func TestOKXSpotDemoStableReadsRejectZeroThenNonZero(t *testing.T) {
	stable := newDemoStableReads(2)
	if stable.Observe(true) {
		t.Fatal("one zero/open-free read must not pass")
	}
	if stable.Observe(false) {
		t.Fatal("zero followed by non-zero must reset stability")
	}
	if stable.Observe(true) || !stable.Observe(true) {
		t.Fatal("only two consecutive stable reads may pass")
	}
}

func readOKXSpotDemoSource(t *testing.T, name string) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve OKX Spot Demo test source path")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(current), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func spotDemoDecimal(value string) decimal.Decimal {
	return decimal.RequireFromString(value)
}
