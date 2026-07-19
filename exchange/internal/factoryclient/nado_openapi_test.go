package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	nadosdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func TestNadoCandleMaxTimeOmitsZeroEnd(t *testing.T) {
	if got := nadoCandleMaxTime(time.Time{}); got != 0 {
		t.Fatalf("zero end max time = %d, want omitted zero", got)
	}
	end := time.Unix(1720000000, 0)
	if got := nadoCandleMaxTime(end); got != end.Unix() {
		t.Fatalf("non-zero end max time = %d, want %d", got, end.Unix())
	}
}

func TestNadoFillAllowsArchiveRowsWithoutTimestamp(t *testing.T) {
	fill, err := nadoMatchFill(nadosdk.Match{
		Digest:        "0x1111111111111111111111111111111111111111111111111111111111111111",
		BaseFilled:    "1000000000000000000",
		Fee:           "1000000000000000",
		SubmissionIdx: "1",
		Order: nadosdk.MatchOrder{
			PriceX18: "1000000000000000000",
			Amount:   "1000000000000000000",
		},
	}, nadoProduct{
		canonical:  "USDC-USDT0",
		quote:      "USDT0",
		marketType: nadosdk.MarketTypeSpot,
	})
	if err != nil {
		t.Fatalf("nadoMatchFill: %v", err)
	}
	if !fill.Time.IsZero() {
		t.Fatalf("missing archive timestamp fabricated as %s", fill.Time)
	}
}

type nadoRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn nadoRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestNadoOpenAPIFixtureFileCoversRESTMatrix(t *testing.T) {
	TestOpenAPINadoRESTExecutionMatrix(t)
}

func TestNadoMinSizeIsQuoteDenominatedMinNotional(t *testing.T) {
	client := NewNadoSpot(testAsterPrivateKey, "default", Settings{
		Environment: "testnet",
		Endpoint:    "https://nado-fixture.invalid/v1",
		HTTPClient:  &http.Client{Transport: newNadoOpenAPIRouter(t)},
	})
	instruments, err := client.Instruments(context.Background())
	if err != nil {
		t.Fatalf("Instruments: %v", err)
	}
	for _, instrument := range instruments {
		if instrument.Symbol != "ETH-USDT0" {
			continue
		}
		if !instrument.MinQuantity.Equal(instrument.QuantityIncrement) {
			t.Fatalf("MinQuantity = %s, want quantity increment %s", instrument.MinQuantity, instrument.QuantityIncrement)
		}
		if !instrument.MinNotional.Valid || !instrument.MinNotional.Value.Equal(decimal.NewFromInt(5)) {
			t.Fatalf("MinNotional = %+v, want 5 USDT0", instrument.MinNotional)
		}
		return
	}
	t.Fatal("ETH-USDT0 instrument not found")
}

func TestNadoMarketOrderLimitPriceStaysOnVenueTick(t *testing.T) {
	tick := decimal.RequireFromString("0.0001")
	best := decimal.RequireFromString("1.0006")

	buy, err := nadoMarketOrderLimitPrice(best, tick, exchange.SideBuy)
	if err != nil {
		t.Fatalf("buy price: %v", err)
	}
	if want := decimal.RequireFromString("1.0507"); !buy.Equal(want) {
		t.Fatalf("buy price = %s, want %s", buy, want)
	}

	sell, err := nadoMarketOrderLimitPrice(best, tick, exchange.SideSell)
	if err != nil {
		t.Fatalf("sell price: %v", err)
	}
	if want := decimal.RequireFromString("0.9505"); !sell.Equal(want) {
		t.Fatalf("sell price = %s, want %s", sell, want)
	}
}

func TestNadoTerminalOrderUpdateAllowsOfficialZeroRemainingAmount(t *testing.T) {
	event, err := nadoOrderUpdateEvent(&nadosdk.OrderUpdate{
		Type:      "order_update",
		Timestamp: "1695081920633151000",
		ProductId: 5,
		Digest:    "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Amount:    "0",
		Reason:    nadosdk.OrderReasonFilled,
		Id:        "100",
	}, nadoProduct{
		canonical:  "USDC-USDT0",
		productID:  5,
		marketType: nadosdk.MarketTypeSpot,
	}, exchange.ProductSpot, "WatchOrders")
	if err != nil {
		t.Fatalf("terminal order update: %v", err)
	}
	if event.Order.Status != "filled" || !event.Order.Quantity.IsZero() || event.Order.Side != "" {
		t.Fatalf("terminal order event = %+v", event)
	}
	if want := time.Unix(0, 1695081920633151000).UTC(); !event.Order.UpdatedAt.Equal(want) {
		t.Fatalf("terminal order event time = %s, want %s", event.Order.UpdatedAt, want)
	}
}

func TestNadoPositionChangeAllowsOfficialFlatPosition(t *testing.T) {
	event, err := nadoPositionEvent(&nadosdk.PositionChange{
		Type:         "position_change",
		Timestamp:    "1783641600",
		ProductId:    2,
		Amount:       "0",
		VQuoteAmount: "0",
		Reason:       nadosdk.PositionReasonMatchOrders,
	}, nadoProduct{
		canonical:  "ETH-PERP-USDT0",
		productID:  2,
		marketType: nadosdk.MarketTypePerp,
	}, "WatchPositions")
	if err != nil {
		t.Fatalf("flat position update: %v", err)
	}
	if len(event.Positions) != 1 {
		t.Fatalf("flat position event = %+v, want one position", event)
	}
	position := event.Positions[0]
	if position.Instrument != "ETH-PERP-USDT0" || !position.Quantity.IsZero() || position.Side != "" || !position.EntryPrice.IsZero() {
		t.Fatalf("flat position = %+v, want zero quantity with no directional side", position)
	}
}

func TestNadoStreamTimestampAcceptsDocumentedUnits(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  time.Time
	}{
		{name: "seconds", value: "1783641600", want: time.Unix(1783641600, 0).UTC()},
		{name: "milliseconds", value: "1783641600300", want: time.UnixMilli(1783641600300).UTC()},
		{name: "nanoseconds", value: "1695081920633151000", want: time.Unix(0, 1695081920633151000).UTC()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nadoUnixTime(tc.value, exchange.ProductSpot, "WatchOrders", "order timestamp")
			if err != nil {
				t.Fatalf("nadoUnixTime: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("time = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNadoAmbiguousGatewayFailurePreservesSafeVenueCode(t *testing.T) {
	base := &nadoBase{meta: clientMeta{venue: exchange.VenueNado, product: exchange.ProductSpot}}
	ack := baseAck(base.meta, exchange.OrderOperationPlace, "ETH-USDT0", "", "101")
	err := base.mutationError(
		"PlaceOrder",
		nadosdk.NewGatewayApplicationError(2500, "sensitive upstream detail", "place_order"),
		&ack,
	)
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) {
		t.Fatalf("error=%T %v, want *exchange.Error", err, err)
	}
	if exchangeErr.Kind() != exchange.KindAmbiguousOutcome || exchangeErr.Details().Code != "2500" {
		t.Fatalf("error=%+v, want ambiguous outcome with code 2500", exchangeErr)
	}
	if ack.VenueCode != "2500" || strings.Contains(err.Error(), "sensitive upstream detail") {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
}

func TestNadoSubscriptionCloseTreatsDisconnectedSocketAsAlreadyClosed(t *testing.T) {
	calls := 0
	stop := nadoIdempotentUnsubscribe(func() error {
		calls++
		return errors.New("not connected")
	})
	if err := stop(); err != nil {
		t.Fatalf("idempotent unsubscribe: %v", err)
	}
	if calls != 1 {
		t.Fatalf("unsubscribe calls = %d, want 1", calls)
	}

	want := errors.New("unsubscribe failed")
	if err := nadoIdempotentUnsubscribe(func() error { return want })(); !errors.Is(err, want) {
		t.Fatalf("non-disconnect error = %v, want %v", err, want)
	}
}

func TestNadoPerpAccountDoesNotFabricateAvailable(t *testing.T) {
	client := NewNadoUSDT0Perp(testAsterPrivateKey, "default", Settings{Environment: "testnet", Endpoint: "https://nado-fixture.invalid/v1", HTTPClient: &http.Client{Transport: newNadoOpenAPIRouter(t)}})
	account, err := client.PerpAccount(context.Background())
	if err != nil {
		t.Fatalf("PerpAccount: %v", err)
	}
	if account.Available.Valid {
		t.Fatalf("Nado Available was fabricated: %+v", account.Available)
	}
}

func TestNadoMutationRejectsMalformedResponseDigest(t *testing.T) {
	router := newNadoOpenAPIRouter(t)
	client := NewNadoSpot(testAsterPrivateKey, "default", Settings{
		Environment: "testnet",
		Endpoint:    "https://nado-fixture.invalid/v1",
		HTTPClient: &http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			response, err := router.RoundTrip(request)
			if err != nil || request.URL.Path != "/v1/execute" {
				return response, err
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				return nil, err
			}
			_ = response.Body.Close()
			body = []byte(strings.ReplaceAll(string(body), "0x1111111111111111111111111111111111111111111111111111111111111111", "0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"))
			response.Body = io.NopCloser(strings.NewReader(string(body)))
			response.ContentLength = int64(len(body))
			return response, nil
		})},
	})
	_, err := client.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{Instrument: "ETH-USDT0", ClientOrderID: "101", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.NewFromInt(1), LimitPrice: decimal.NewFromInt(99), LimitPolicy: exchange.LimitPolicyResting})
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("PlaceOrder malformed digest error = %v, want ErrMalformedResponse", err)
	}
}

func TestNadoCancelAcceptsNativeLowercaseDigest(t *testing.T) {
	client := NewNadoSpot(testAsterPrivateKey, "default", Settings{Environment: "testnet", Endpoint: "https://nado-fixture.invalid/v1", HTTPClient: &http.Client{Transport: newNadoOpenAPIRouter(t)}})
	ack, err := client.CancelOrder(context.Background(), exchange.CancelOrderRequest{Instrument: "ETH-USDT0", OrderID: "0x1111111111111111111111111111111111111111111111111111111111111111"})
	if err != nil {
		t.Fatalf("CancelOrder native digest: %v", err)
	}
	if ack.OrderID != "0x1111111111111111111111111111111111111111111111111111111111111111" {
		t.Fatalf("CancelOrder ack digest = %q", ack.OrderID)
	}
}
