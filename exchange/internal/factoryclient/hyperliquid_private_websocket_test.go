package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	hyperliquidspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

func TestHyperliquidPrivateOrderEventNormalizesLifecycleAndMarketShape(t *testing.T) {
	meta := hyperliquidMarketMeta{
		instrument: exchange.Instrument{Symbol: "BTC-USDT"},
		nativeCoin: "BTC",
	}
	update := hyperliquid.WsOrderUpdate{
		Order: hyperliquid.WsOrder{
			Coin:      "BTC",
			Side:      "B",
			LimitPx:   "101",
			Sz:        "0",
			OrigSz:    "2",
			Oid:       17,
			Timestamp: 1700000000000,
			OrderType: "Market",
			Tif:       "Ioc",
		},
		Status:          hyperliquid.StatusFilled,
		StatusTimestamp: 1700000000100,
	}

	event, err := hyperliquidPrivateOrderEvent(meta, update)
	if err != nil {
		t.Fatalf("hyperliquidPrivateOrderEvent: %v", err)
	}
	if event.Kind != exchange.EventDelta ||
		event.Order.Instrument != "BTC-USDT" ||
		event.Order.Type != exchange.OrderTypeMarket ||
		event.Order.Status != string(hyperliquid.StatusFilled) ||
		!event.Order.Filled.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("event=%+v", event)
	}
}

func TestHyperliquidPrivateOrderEventKeepsExplicitIOCLimitType(t *testing.T) {
	meta := hyperliquidMarketMeta{
		instrument: exchange.Instrument{Symbol: "BTC-USDT"},
		nativeCoin: "BTC",
	}
	update := hyperliquid.WsOrderUpdate{
		Order: hyperliquid.WsOrder{
			Coin:      "BTC",
			Side:      "A",
			LimitPx:   "101",
			Sz:        "1",
			OrigSz:    "2",
			Oid:       18,
			Timestamp: 1700000000000,
			OrderType: "Limit",
			Tif:       "Ioc",
		},
		Status:          hyperliquid.StatusCanceled,
		StatusTimestamp: 1700000000100,
	}
	event, err := hyperliquidPrivateOrderEvent(meta, update)
	if err != nil {
		t.Fatalf("hyperliquidPrivateOrderEvent: %v", err)
	}
	if event.Order.Type != exchange.OrderTypeLimit ||
		event.Order.LimitPolicy != exchange.LimitPolicyIOC ||
		!event.Order.Filled.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("event=%+v", event)
	}
}

func TestHyperliquidPrivateOrderEventRejectsUnknownNativeOrderType(t *testing.T) {
	meta := hyperliquidMarketMeta{
		instrument: exchange.Instrument{Symbol: "BTC-USDT", Product: exchange.ProductPerp},
		nativeCoin: "BTC",
	}
	update := hyperliquid.WsOrderUpdate{
		Order: hyperliquid.WsOrder{
			Coin:      "BTC",
			Side:      "B",
			LimitPx:   "101",
			Sz:        "1",
			OrigSz:    "1",
			Oid:       17,
			Timestamp: 1700000000000,
			OrderType: "Trigger",
			Tif:       "Gtc",
		},
		Status:          hyperliquid.StatusOpen,
		StatusTimestamp: 1700000000001,
	}
	if _, err := hyperliquidPrivateOrderEvent(meta, update); !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("hyperliquidPrivateOrderEvent err=%v, want ErrMalformedResponse", err)
	}
}

func TestHyperliquidPrivateHubSharesOneNativeSubscriptionAndStopsLastOwner(t *testing.T) {
	hub := newHyperliquidPrivateHub[int]()
	var starts atomic.Int32
	var stops atomic.Int32
	start := func() (func() error, error) {
		starts.Add(1)
		return func() error {
			stops.Add(1)
			return nil
		}, nil
	}
	first := make(chan int, 1)
	second := make(chan int, 1)
	stopFirst, err := hub.add(context.Background(), "BTC-USDT", func(value int) { first <- value }, nil, start)
	if err != nil {
		t.Fatal(err)
	}
	stopSecond, err := hub.add(context.Background(), "ETH-USDT", func(value int) { second <- value }, nil, start)
	if err != nil {
		t.Fatal(err)
	}
	hub.publish(7)
	if <-first != 7 || <-second != 7 || starts.Load() != 1 {
		t.Fatalf("starts=%d, want one shared native subscription", starts.Load())
	}
	if err := stopFirst(); err != nil {
		t.Fatal(err)
	}
	if stops.Load() != 0 {
		t.Fatalf("native stops=%d before last owner", stops.Load())
	}
	if err := stopSecond(); err != nil {
		t.Fatal(err)
	}
	if stops.Load() != 1 {
		t.Fatalf("native stops=%d, want one exact-owned cleanup", stops.Load())
	}
}

func TestHyperliquidPrivatePostResponsesPreserveRestAckSemantics(t *testing.T) {
	spotPlace := hyperliquid.PostResult{Response: hyperliquid.WsPostResponsePayload{
		Type: "action",
		Payload: json.RawMessage(
			`{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":7,"cloid":"0x00000000000000000000000000000001"}}]}}}`,
		),
	}}
	spotAck, err := hyperliquidSpotPostPlaceAck("BTC-USDT", "1", "0x00000000000000000000000000000001", exchange.OrderTypeLimit, spotPlace)
	if err != nil {
		t.Fatalf("spot place ack: %v", err)
	}
	if spotAck.State != exchange.AckResting || spotAck.OrderID != "7" || spotAck.ClientOrderID != "1" {
		t.Fatalf("spot ack=%+v", spotAck)
	}

	perpPlace := hyperliquid.PostResult{Response: hyperliquid.WsPostResponsePayload{
		Type: "action",
		Payload: json.RawMessage(
			`{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"oid":8,"totalSz":"0.5","avgPx":"100"}}]}}}`,
		),
	}}
	perpAck, err := hyperliquidPerpPostPlaceAck("BTC-USDT", "", "", exchange.OrderTypeMarket, perpPlace)
	if err != nil {
		t.Fatalf("perp place ack: %v", err)
	}
	if perpAck.State != exchange.AckImmediatelyFilled ||
		perpAck.OrderType != exchange.OrderTypeMarket ||
		!perpAck.FilledQuantity.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("perp ack=%+v", perpAck)
	}

	cancel := hyperliquid.PostResult{Response: hyperliquid.WsPostResponsePayload{
		Type:    "action",
		Payload: json.RawMessage(`{"status":"ok","response":{"type":"cancel","data":{"statuses":["success"]}}}`),
	}}
	cancelAck, err := hyperliquidPostCancelAck(exchange.ProductPerp, "BTC-USDT", "8", cancel)
	if err != nil {
		t.Fatalf("cancel ack: %v", err)
	}
	if cancelAck.State != exchange.AckAcceptedPending || cancelAck.Operation != exchange.OrderOperationCancel {
		t.Fatalf("cancel ack=%+v", cancelAck)
	}
}

func TestHyperliquidPrivatePostResponsesRejectMalformedAndVenueErrors(t *testing.T) {
	rejection := hyperliquid.PostResult{Response: hyperliquid.WsPostResponsePayload{
		Type: "action",
		Payload: json.RawMessage(
			`{"status":"ok","response":{"type":"order","data":{"statuses":[{"error":"insufficient balance"}]}}}`,
		),
	}}
	ack, err := hyperliquidSpotPostPlaceAck("BTC-USDT", "17", "", exchange.OrderTypeLimit, rejection)
	if err == nil {
		t.Fatal("venue rejection was accepted")
	}
	if ack.State != exchange.AckRejected ||
		ack.Operation != exchange.OrderOperationPlace ||
		ack.ClientOrderID != "17" ||
		ack.VenueMessage == "" {
		t.Fatalf("rejected place ack=%+v", ack)
	}
	malformed := hyperliquid.PostResult{Response: hyperliquid.WsPostResponsePayload{
		Type:    "action",
		Payload: json.RawMessage(`{"status":"ok","response":{"type":"order","data":{"statuses":[]}}}`),
	}}
	if _, err := hyperliquidPerpPostPlaceAck("BTC-USDT", "", "", exchange.OrderTypeLimit, malformed); err == nil {
		t.Fatal("empty place status was accepted")
	}

	cancelRejection := hyperliquid.PostResult{Response: hyperliquid.WsPostResponsePayload{
		Type:    "action",
		Payload: json.RawMessage(`{"status":"ok","response":{"type":"cancel","data":{"statuses":[{"error":"order not found"}]}}}`),
	}}
	cancelAck, err := hyperliquidPostCancelAck(exchange.ProductPerp, "BTC-USDT", "41", cancelRejection)
	if err == nil {
		t.Fatal("cancel venue rejection was accepted")
	}
	if cancelAck.State != exchange.AckRejected ||
		cancelAck.Operation != exchange.OrderOperationCancel ||
		cancelAck.OrderID != "41" ||
		cancelAck.VenueMessage == "" {
		t.Fatalf("rejected cancel ack=%+v", cancelAck)
	}
}

func TestHyperliquidPrivatePostResponseBoundariesCoverEveryProductAndOperation(t *testing.T) {
	const secret = "private-key-should-not-leak"
	rejectedPlacePayload := json.RawMessage(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"error":"` + secret + `"}]}}}`)
	malformedPlacePayload := json.RawMessage(`{"status":"ok","response":{"type":"order","data":{"statuses":[]}}}`)
	rejectedCancelPayload := json.RawMessage(`{"status":"ok","response":{"type":"cancel","data":{"statuses":[{"error":"` + secret + `"}]}}}`)
	malformedCancelPayload := json.RawMessage(`{"status":"ok","response":{"type":"cancel","data":{"statuses":[]}}}`)

	tests := []struct {
		name      string
		product   exchange.Product
		operation exchange.OrderOperation
		result    hyperliquid.PostResult
		decode    func(hyperliquid.PostResult) (exchange.OrderAcknowledgement, error)
		wantErr   error
		wantState exchange.OrderAckState
	}{
		{
			name:      "spot_place_venue_rejected",
			product:   exchange.ProductSpot,
			operation: exchange.OrderOperationPlace,
			result:    hyperliquidPostAction(rejectedPlacePayload),
			decode: func(result hyperliquid.PostResult) (exchange.OrderAcknowledgement, error) {
				return hyperliquidSpotPostPlaceAck("BTC-USDT", "17", hlNativeClientOrderID("17"), exchange.OrderTypeLimit, result)
			},
			wantErr:   exchange.ErrVenueRejected,
			wantState: exchange.AckRejected,
		},
		{
			name:      "perp_place_venue_rejected",
			product:   exchange.ProductPerp,
			operation: exchange.OrderOperationPlace,
			result:    hyperliquidPostAction(rejectedPlacePayload),
			decode: func(result hyperliquid.PostResult) (exchange.OrderAcknowledgement, error) {
				return hyperliquidPerpPostPlaceAck("BTC-USDT", "17", hlNativeClientOrderID("17"), exchange.OrderTypeLimit, result)
			},
			wantErr:   exchange.ErrVenueRejected,
			wantState: exchange.AckRejected,
		},
		{
			name:      "spot_cancel_venue_rejected",
			product:   exchange.ProductSpot,
			operation: exchange.OrderOperationCancel,
			result:    hyperliquidPostAction(rejectedCancelPayload),
			decode: func(result hyperliquid.PostResult) (exchange.OrderAcknowledgement, error) {
				return hyperliquidPostCancelAck(exchange.ProductSpot, "BTC-USDT", "41", result)
			},
			wantErr:   exchange.ErrVenueRejected,
			wantState: exchange.AckRejected,
		},
		{
			name:      "perp_cancel_venue_rejected",
			product:   exchange.ProductPerp,
			operation: exchange.OrderOperationCancel,
			result:    hyperliquidPostAction(rejectedCancelPayload),
			decode: func(result hyperliquid.PostResult) (exchange.OrderAcknowledgement, error) {
				return hyperliquidPostCancelAck(exchange.ProductPerp, "BTC-USDT", "41", result)
			},
			wantErr:   exchange.ErrVenueRejected,
			wantState: exchange.AckRejected,
		},
		{
			name:      "spot_place_malformed",
			product:   exchange.ProductSpot,
			operation: exchange.OrderOperationPlace,
			result:    hyperliquidPostAction(malformedPlacePayload),
			decode: func(result hyperliquid.PostResult) (exchange.OrderAcknowledgement, error) {
				return hyperliquidSpotPostPlaceAck("BTC-USDT", "17", hlNativeClientOrderID("17"), exchange.OrderTypeLimit, result)
			},
			wantErr: exchange.ErrMalformedResponse,
		},
		{
			name:      "perp_place_malformed",
			product:   exchange.ProductPerp,
			operation: exchange.OrderOperationPlace,
			result:    hyperliquidPostAction(malformedPlacePayload),
			decode: func(result hyperliquid.PostResult) (exchange.OrderAcknowledgement, error) {
				return hyperliquidPerpPostPlaceAck("BTC-USDT", "17", hlNativeClientOrderID("17"), exchange.OrderTypeLimit, result)
			},
			wantErr: exchange.ErrMalformedResponse,
		},
		{
			name:      "spot_cancel_malformed",
			product:   exchange.ProductSpot,
			operation: exchange.OrderOperationCancel,
			result:    hyperliquidPostAction(malformedCancelPayload),
			decode: func(result hyperliquid.PostResult) (exchange.OrderAcknowledgement, error) {
				return hyperliquidPostCancelAck(exchange.ProductSpot, "BTC-USDT", "41", result)
			},
			wantErr: exchange.ErrMalformedResponse,
		},
		{
			name:      "perp_cancel_malformed",
			product:   exchange.ProductPerp,
			operation: exchange.OrderOperationCancel,
			result:    hyperliquidPostAction(malformedCancelPayload),
			decode: func(result hyperliquid.PostResult) (exchange.OrderAcknowledgement, error) {
				return hyperliquidPostCancelAck(exchange.ProductPerp, "BTC-USDT", "41", result)
			},
			wantErr: exchange.ErrMalformedResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ack, err := tt.decode(tt.result)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err=%v, want %v", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(ack.VenueMessage, secret) {
				t.Fatalf("unsafe secret leaked in ack=%+v err=%v", ack, err)
			}
			if tt.wantState != "" && ack.State != tt.wantState {
				t.Fatalf("ack state=%s, want %s", ack.State, tt.wantState)
			}
			if tt.wantState != "" &&
				(ack.Product != tt.product || ack.Operation != tt.operation || ack.Instrument != "BTC-USDT") {
				t.Fatalf("ack metadata=%+v", ack)
			}
		})
	}
}

func TestHyperliquidPrivatePostSendAmbiguityMatrixPreservesLocatorsAndRedactsError(t *testing.T) {
	const secret = "post-error-secret"
	tests := []struct {
		name          string
		product       exchange.Product
		operation     exchange.OrderOperation
		orderID       string
		clientOrderID string
	}{
		{name: "spot_place", product: exchange.ProductSpot, operation: exchange.OrderOperationPlace, clientOrderID: "17"},
		{name: "perp_place", product: exchange.ProductPerp, operation: exchange.OrderOperationPlace, clientOrderID: "17"},
		{name: "spot_cancel", product: exchange.ProductSpot, operation: exchange.OrderOperationCancel, orderID: "41"},
		{name: "perp_cancel", product: exchange.ProductPerp, operation: exchange.OrderOperationCancel, orderID: "41"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan hyperliquid.PostResult, 1)
			ch <- hyperliquid.PostResult{Error: errors.New(secret)}
			close(ch)
			_, err := hyperliquidWaitPostResult(context.Background(), ch)
			if !errors.Is(err, exchange.ErrAmbiguousOutcome) {
				t.Fatalf("wait error=%v, want ErrAmbiguousOutcome", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("ambiguous error leaked unsafe cause: %v", err)
			}
			ack := hyperliquidAmbiguousAck(tt.product, tt.operation, "BTC-USDT", tt.orderID, tt.clientOrderID)
			if ack.State != exchange.AckAmbiguous ||
				ack.Product != tt.product ||
				ack.Operation != tt.operation ||
				ack.OrderID != tt.orderID ||
				ack.ClientOrderID != tt.clientOrderID {
				t.Fatalf("ambiguous ack=%+v", ack)
			}
		})
	}
}

func TestHyperliquidPrivateSDKPreSendErrorsReturnNoResultChannel(t *testing.T) {
	const privateKey = "0x0000000000000000000000000000000000000000000000000000000000000001"
	ctx := context.Background()
	base := hyperliquid.NewWebsocketClient(ctx).WithCredentials(privateKey, nil)
	spot := hyperliquidspot.NewWebsocketClient(base)
	perp := hyperliquidperp.NewWebsocketClient(base)

	tests := []struct {
		name string
		call func() (chan hyperliquid.PostResult, error)
	}{
		{
			name: "spot_place_without_connection",
			call: func() (chan hyperliquid.PostResult, error) {
				return spot.PlaceOrder(ctx, hyperliquidspot.PlaceOrderRequest{
					AssetID: 1, IsBuy: true, Price: 100, Size: 1,
					OrderType: hyperliquidspot.OrderType{Limit: &hyperliquidspot.OrderTypeLimit{Tif: hyperliquid.TifGtc}},
				})
			},
		},
		{
			name: "perp_place_without_connection",
			call: func() (chan hyperliquid.PostResult, error) {
				return perp.PlaceOrder(ctx, hyperliquidperp.PlaceOrderRequest{
					AssetID: 1, IsBuy: true, Price: 100, Size: 1,
					OrderType: hyperliquidperp.OrderType{Limit: &hyperliquidperp.OrderTypeLimit{Tif: hyperliquid.TifGtc}},
				})
			},
		},
		{
			name: "spot_cancel_without_connection",
			call: func() (chan hyperliquid.PostResult, error) {
				return spot.CancelOrder(ctx, hyperliquidspot.CancelOrderRequest{AssetID: 1, OrderID: 41})
			},
		},
		{
			name: "perp_cancel_without_connection",
			call: func() (chan hyperliquid.PostResult, error) {
				return perp.CancelOrder(ctx, hyperliquidperp.CancelOrderRequest{AssetID: 1, OrderID: 41})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch, err := tt.call()
			if err == nil {
				t.Fatal("pre-send SDK error was accepted")
			}
			if ch != nil {
				t.Fatalf("pre-send SDK error returned result channel %#v", ch)
			}
		})
	}
}

func TestHyperliquidPrivateWebSocketConnectErrorPreservesCauseWithoutCredentials(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("NO_PROXY", "")

	tests := []struct {
		name      string
		endpoint  string
		proxy     string
		wantCause string
		secrets   []string
	}{
		{
			name:      "endpoint userinfo",
			endpoint:  "ws://endpoint-user:endpoint-secret@example.invalid/ws",
			wantCause: "malformed ws or wss URL",
			secrets:   []string{"endpoint-user", "endpoint-secret"},
		},
		{
			name:      "proxy userinfo",
			endpoint:  "ws://example.invalid/ws",
			proxy:     "http://proxy-user:proxy-secret@127.0.0.1:not-a-port",
			wantCause: "invalid proxy URL",
			secrets:   []string{"proxy-user", "proxy-secret"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PROXY", tt.proxy)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := hyperliquid.NewWebsocketClient(ctx).
				WithURL(tt.endpoint).
				WithCredentials(openAPITestPrivateKey, nil)
			backend := &hyperliquidPrivateWSBackend{
				meta:   clientMeta{venue: exchange.VenueHyperliquid, product: exchange.ProductSpot},
				base:   base,
				user:   base.AccountAddr,
				cancel: cancel,
			}

			err := backend.ensureConnected("WatchOrders")
			if err == nil || !strings.Contains(err.Error(), tt.wantCause) {
				t.Fatalf("connect error = %v, want preserved cause %q", err, tt.wantCause)
			}
			for _, secret := range append(tt.secrets, openAPITestPrivateKey) {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("connect error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestHyperliquidPrivateCommandsRejectMissingOrderIDBeforeMutationForEveryProduct(t *testing.T) {
	for _, product := range []exchange.Product{exchange.ProductSpot, exchange.ProductPerp} {
		t.Run(string(product), func(t *testing.T) {
			var resolves atomic.Int32
			backend := &hyperliquidPrivateWSBackend{
				meta: clientMeta{venue: exchange.VenueHyperliquid, product: product},
				resolve: func(_ context.Context, operation string, instrument string) (hyperliquidMarketMeta, error) {
					resolves.Add(1)
					if operation != "CancelOrder" || instrument != "BTC-USDT" {
						t.Fatalf("resolve operation=%s instrument=%s", operation, instrument)
					}
					return hyperliquidMarketMeta{assetID: 1}, nil
				},
			}
			ack, err := backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{
				Instrument: "BTC-USDT",
			})
			if !errors.Is(err, exchange.ErrInvalidRequest) {
				t.Fatalf("err=%v, want ErrInvalidRequest", err)
			}
			if ack != (exchange.OrderAcknowledgement{}) {
				t.Fatalf("missing-order-id cancel returned ack=%+v", ack)
			}
			if resolves.Load() != 1 {
				t.Fatalf("resolve calls=%d, want 1", resolves.Load())
			}
		})
	}
}

func hyperliquidPostAction(payload json.RawMessage) hyperliquid.PostResult {
	return hyperliquid.PostResult{Response: hyperliquid.WsPostResponsePayload{Type: "action", Payload: payload}}
}

func TestHyperliquidPrivateCommandsRejectInvalidRequestsBeforeSendForEveryProduct(t *testing.T) {
	for _, product := range []exchange.Product{exchange.ProductSpot, exchange.ProductPerp} {
		t.Run(string(product), func(t *testing.T) {
			meta := hyperliquidMarketMeta{
				instrument: exchange.Instrument{
					Symbol:            "BTC-USDT",
					Product:           product,
					QuantityIncrement: decimal.RequireFromString("0.001"),
				},
				nativeCoin:    "BTC",
				assetID:       1,
				sizeDecimals:  3,
				priceDecimals: 2,
			}
			backend := &hyperliquidPrivateWSBackend{
				meta: clientMeta{venue: exchange.VenueHyperliquid, product: product},
				resolve: func(_ context.Context, _ string, instrument string) (hyperliquidMarketMeta, error) {
					if instrument != meta.instrument.Symbol {
						return hyperliquidMarketMeta{}, errors.New("unexpected instrument")
					}
					return meta, nil
				},
			}

			placeAck, err := backend.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
				Instrument:    "BTC-USDT",
				ClientOrderID: "17",
				Side:          exchange.SideBuy,
				Type:          exchange.OrderTypeLimit,
				LimitPrice:    decimal.NewFromInt(100),
				LimitPolicy:   exchange.LimitPolicyResting,
			})
			if !errors.Is(err, exchange.ErrInvalidRequest) {
				t.Fatalf("PlaceOrder err=%v, want ErrInvalidRequest", err)
			}
			if placeAck != (exchange.OrderAcknowledgement{}) {
				t.Fatalf("pre-send PlaceOrder returned ack=%+v", placeAck)
			}

			cancelAck, err := backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{
				Instrument: "BTC-USDT",
				OrderID:    "not-a-native-order-id",
			})
			if !errors.Is(err, exchange.ErrInvalidRequest) {
				t.Fatalf("CancelOrder err=%v, want ErrInvalidRequest", err)
			}
			if cancelAck != (exchange.OrderAcknowledgement{}) {
				t.Fatalf("pre-send CancelOrder returned ack=%+v", cancelAck)
			}
		})
	}
}

func TestHyperliquidPrivatePlaceRequestsCoverPortableParameterBranches(t *testing.T) {
	meta := hyperliquidMarketMeta{
		assetID:       3,
		sizeDecimals:  3,
		priceDecimals: 5,
	}
	base := exchange.PlaceOrderRequest{
		Instrument:    "BTC-USDT",
		ClientOrderID: "17",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.RequireFromString("0.5"),
		LimitPrice:    decimal.RequireFromString("101.23456"),
		LimitPolicy:   exchange.LimitPolicyResting,
	}
	nativeClientID := hlNativeClientOrderID(base.ClientOrderID)

	tests := []struct {
		name   string
		policy exchange.LimitPolicy
		want   hyperliquid.Tif
	}{
		{name: "resting", policy: exchange.LimitPolicyResting, want: hyperliquid.TifGtc},
		{name: "ioc", policy: exchange.LimitPolicyIOC, want: hyperliquid.TifIoc},
		{name: "post_only", policy: exchange.LimitPolicyPostOnly, want: hyperliquid.TifAlo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := base
			request.LimitPolicy = tt.policy
			native, err := hyperliquidSpotPrivatePlaceRequest(meta, request, nativeClientID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if native.AssetID != meta.assetID ||
				!native.IsBuy ||
				native.Price != 101.23 ||
				native.Size != 0.5 ||
				native.OrderType.Limit == nil ||
				native.OrderType.Limit.Tif != tt.want ||
				native.ClientOrderID == nil ||
				*native.ClientOrderID != nativeClientID {
				t.Fatalf("native=%+v", native)
			}
		})
	}

	market := base
	market.Type = exchange.OrderTypeMarket
	market.LimitPrice = decimal.Zero
	market.LimitPolicy = ""
	spotMarket, err := hyperliquidSpotPrivatePlaceRequest(meta, market, nativeClientID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if spotMarket.Price <= 100 ||
		spotMarket.OrderType.Limit == nil ||
		spotMarket.OrderType.Limit.Tif != hyperliquid.TifIoc {
		t.Fatalf("spot market=%+v", spotMarket)
	}

	for _, tt := range tests {
		t.Run("perp_"+tt.name, func(t *testing.T) {
			request := base
			request.Side = exchange.SideSell
			request.LimitPolicy = tt.policy
			request.ReduceOnly = true
			perp, err := hyperliquidPerpPrivatePlaceRequest(meta, request, nativeClientID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if perp.IsBuy ||
				!perp.ReduceOnly ||
				perp.Price != 101.24 ||
				perp.OrderType.Limit == nil ||
				perp.OrderType.Limit.Tif != tt.want {
				t.Fatalf("perp=%+v", perp)
			}
		})
	}
	perpMarket := market
	perpMarket.ReduceOnly = true
	perp, err := hyperliquidPerpPrivatePlaceRequest(meta, perpMarket, nativeClientID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !perp.IsBuy ||
		!perp.ReduceOnly ||
		perp.Price <= 100 ||
		perp.OrderType.Limit == nil ||
		perp.OrderType.Limit.Tif != hyperliquid.TifIoc {
		t.Fatalf("perp market=%+v", perp)
	}
}

func TestHyperliquidMutationErrAllowsWebSocketWithoutHTTPTracker(t *testing.T) {
	ack, err := hlMutationErr(
		exchange.ProductSpot,
		exchange.OrderOperationPlace,
		"BTC-USDT",
		"",
		"17",
		errors.New("transport failed"),
		nil,
	)
	if err == nil || ack.State != exchange.AckAmbiguous || ack.ClientOrderID != "17" {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
}

func TestHyperliquidSpotPrivateWebSocketExercisesEveryExposedMethod(t *testing.T) {
	const privateKey = "0x0000000000000000000000000000000000000000000000000000000000000001"
	nativeClientID := hlNativeClientOrderID("17")
	var unsubscribes atomic.Int32
	var posts atomic.Int32
	serverErrors := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(w, request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var command struct {
				Method       string          `json:"method"`
				ID           int64           `json:"id"`
				Subscription json.RawMessage `json:"subscription"`
			}
			if err := conn.ReadJSON(&command); err != nil {
				return
			}
			switch command.Method {
			case "subscribe":
				var subscription map[string]any
				if err := json.Unmarshal(command.Subscription, &subscription); err != nil {
					serverErrors <- err
					return
				}
				if err := conn.WriteJSON(map[string]any{
					"channel": "subscriptionResponse",
					"data":    json.RawMessage(command.Subscription),
				}); err != nil {
					serverErrors <- err
					return
				}
				switch subscription["type"] {
				case "orderUpdates":
					err = conn.WriteJSON(map[string]any{
						"channel": "orderUpdates",
						"data": []map[string]any{{
							"order": map[string]any{
								"coin": "BTC", "side": "B", "limitPx": "100", "sz": "1",
								"origSz": "1", "oid": int64(7), "timestamp": int64(1_700_000_000_000),
								"orderType": "Limit", "tif": "Gtc",
							},
							"status": "open", "statusTimestamp": int64(1_700_000_000_001),
						}},
					})
				case "userFills":
					err = conn.WriteJSON(map[string]any{
						"channel": "userFills",
						"data": map[string]any{
							"user":       subscription["user"],
							"isSnapshot": true,
							"fills": []map[string]any{{
								"coin": "BTC", "px": "100", "sz": "0.5", "side": "B",
								"time": int64(1_700_000_000_002), "hash": "0xfill",
								"oid": int64(7), "tid": int64(9), "fee": "0.01",
								"feeToken": "USDT", "crossed": true,
							}},
						},
					})
				case "spotState":
					err = conn.WriteJSON(map[string]any{
						"channel": "spotState",
						"data": map[string]any{
							"user": subscription["user"],
							"spotState": map[string]any{
								"balances": []map[string]any{{
									"coin": "USDT", "token": int64(0), "hold": "5", "total": "50",
								}},
							},
						},
					})
				}
				if err != nil {
					serverErrors <- err
					return
				}
			case "unsubscribe":
				unsubscribes.Add(1)
			case "post":
				count := posts.Add(1)
				payload := map[string]any{
					"status": "ok",
					"response": map[string]any{
						"type": "cancel",
						"data": map[string]any{"statuses": []any{"success"}},
					},
				}
				if count == 1 {
					payload = map[string]any{
						"status": "ok",
						"response": map[string]any{
							"type": "order",
							"data": map[string]any{"statuses": []any{map[string]any{
								"resting": map[string]any{"oid": int64(41), "cloid": nativeClientID},
							}}},
						},
					}
				}
				if err := conn.WriteJSON(map[string]any{
					"channel": "post",
					"data": map[string]any{
						"id": command.ID,
						"response": map[string]any{
							"type":    "action",
							"payload": payload,
						},
					},
				}); err != nil {
					serverErrors <- err
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	base := hyperliquid.NewWebsocketClient(ctx).
		WithURL("ws"+strings.TrimPrefix(server.URL, "http")).
		WithCredentials(privateKey, nil)
	base.SubscriptionAckTimeout = time.Second
	meta := hyperliquidMarketMeta{
		instrument: exchange.Instrument{
			Symbol:            "BTC-USDT",
			Product:           exchange.ProductSpot,
			QuantityIncrement: decimal.RequireFromString("0.001"),
		},
		nativeCoin:    "BTC",
		assetID:       1,
		sizeDecimals:  3,
		priceDecimals: 2,
	}
	clientMeta := clientMeta{venue: exchange.VenueHyperliquid, product: exchange.ProductSpot}
	backend := &hyperliquidPrivateWSBackend{
		meta: clientMeta,
		base: base,
		user: base.AccountAddr,
		resolve: func(_ context.Context, _ string, instrument string) (hyperliquidMarketMeta, error) {
			if instrument != meta.instrument.Symbol {
				return hyperliquidMarketMeta{}, errors.New("unknown instrument")
			}
			return meta, nil
		},
		mid:       func(context.Context, string) (float64, error) { return 100, nil },
		spot:      hyperliquidspot.NewWebsocketClient(base),
		lifecycle: newBackendLifecycle(),
		cancel:    cancel,
		orders:    newHyperliquidPrivateHub[hyperliquid.WsOrderUpdate](),
		fills:     newHyperliquidPrivateHub[hyperliquidPrivateFill](),
		state:     newHyperliquidPrivateHub[hyperliquidPrivateState](),
	}
	backend.installReconnectHooks()
	socket := newSpotWebSocket(
		newPublicWebSocket(clientMeta, &fakePublicWSBackend{}),
		backend,
	)
	t.Cleanup(func() { _ = socket.Close() })

	watchCtx, watchCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer watchCancel()
	orders, err := socket.WatchOrders(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	fills, err := socket.WatchFills(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	balances, err := socket.WatchBalances(watchCtx, exchange.WatchAccountRequest{})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-orders.Events():
		if event.Order.OrderID != "7" || event.Order.Instrument != "BTC-USDT" {
			t.Fatalf("order event=%+v", event)
		}
	case err := <-serverErrors:
		t.Fatal(err)
	case <-watchCtx.Done():
		t.Fatal("order event timeout")
	}
	select {
	case event := <-fills.Events():
		if event.Kind != exchange.EventSnapshot ||
			event.Fill.FillID != "9" ||
			event.Fill.Instrument != "BTC-USDT" {
			t.Fatalf("fill event=%+v", event)
		}
	case err := <-serverErrors:
		t.Fatal(err)
	case <-watchCtx.Done():
		t.Fatal("fill event timeout")
	}
	select {
	case event := <-balances.Events():
		if len(event.Balances) != 1 ||
			!event.Balances[0].Available.Equal(decimal.NewFromInt(45)) {
			t.Fatalf("balance event=%+v", event)
		}
	case err := <-serverErrors:
		t.Fatal(err)
	case <-watchCtx.Done():
		t.Fatal("balance event timeout")
	}

	placeAck, err := socket.PlaceOrder(watchCtx, exchange.PlaceOrderRequest{
		Instrument:    "BTC-USDT",
		ClientOrderID: "17",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.NewFromInt(1),
		LimitPrice:    decimal.NewFromInt(100),
		LimitPolicy:   exchange.LimitPolicyResting,
	})
	if err != nil {
		t.Fatal(err)
	}
	if placeAck.State != exchange.AckResting ||
		placeAck.OrderID != "41" ||
		placeAck.ClientOrderID != "17" {
		t.Fatalf("place ack=%+v", placeAck)
	}
	cancelAck, err := socket.CancelOrder(watchCtx, exchange.CancelOrderRequest{
		Instrument: "BTC-USDT",
		OrderID:    "41",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cancelAck.State != exchange.AckAcceptedPending || cancelAck.OrderID != "41" {
		t.Fatalf("cancel ack=%+v", cancelAck)
	}

	for _, subscription := range []interface{ Close() error }{orders, fills, balances} {
		if err := subscription.Close(); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for unsubscribes.Load() != 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if unsubscribes.Load() != 3 || posts.Load() != 2 {
		t.Fatalf("unsubscribes=%d posts=%d", unsubscribes.Load(), posts.Load())
	}
}

func TestHyperliquidPerpPrivateWebSocketExercisesEveryExposedMethod(t *testing.T) {
	const privateKey = "0x0000000000000000000000000000000000000000000000000000000000000001"
	var unsubscribes atomic.Int32
	var stateSubscriptions atomic.Int32
	var posts atomic.Int32
	stateReady := make(chan struct{})
	var stateReadyOnce sync.Once
	sendState := make(chan struct{})
	serverErrors := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(w, request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var command struct {
				Method       string          `json:"method"`
				ID           int64           `json:"id"`
				Subscription json.RawMessage `json:"subscription"`
			}
			if err := conn.ReadJSON(&command); err != nil {
				return
			}
			switch command.Method {
			case "subscribe":
				var subscription map[string]any
				if err := json.Unmarshal(command.Subscription, &subscription); err != nil {
					serverErrors <- err
					return
				}
				if err := conn.WriteJSON(map[string]any{
					"channel": "subscriptionResponse",
					"data":    json.RawMessage(command.Subscription),
				}); err != nil {
					serverErrors <- err
					return
				}
				switch subscription["type"] {
				case "orderUpdates":
					err = conn.WriteJSON(map[string]any{
						"channel": "orderUpdates",
						"data": []map[string]any{{
							"order": map[string]any{
								"coin": "BTC", "side": "A", "limitPx": "100", "sz": "1",
								"origSz": "2", "oid": int64(7), "timestamp": int64(1_700_000_000_000),
								"orderType": "Limit", "tif": "Alo", "reduceOnly": true,
							},
							"status": "open", "statusTimestamp": int64(1_700_000_000_001),
						}},
					})
				case "userFills":
					err = conn.WriteJSON(map[string]any{
						"channel": "userFills",
						"data": map[string]any{
							"user":       subscription["user"],
							"isSnapshot": false,
							"fills": []map[string]any{{
								"coin": "BTC", "px": "100", "sz": "0.5", "side": "A",
								"time": int64(1_700_000_000_002), "hash": "0xfill",
								"oid": int64(7), "tid": int64(9), "fee": "0.01",
								"feeToken": "USDT", "crossed": false,
							}},
						},
					})
				case "clearinghouseState":
					stateSubscriptions.Add(1)
					stateReadyOnce.Do(func() { close(stateReady) })
					<-sendState
					err = conn.WriteJSON(map[string]any{
						"channel": "clearinghouseState",
						"data": map[string]any{
							"user": subscription["user"],
							"dex":  "",
							"clearinghouseState": map[string]any{
								"assetPositions": []map[string]any{{
									"position": map[string]any{
										"coin": "BTC", "entryPx": "100", "leverage": map[string]any{"value": 5},
										"liquidationPx": "50", "marginUsed": "20", "szi": "-2", "unrealizedPnl": "3",
									},
								}},
								"marginSummary": map[string]any{"accountValue": "120", "totalMarginUsed": "20"},
								"withdrawable":  "100",
								"time":          int64(1_700_000_000_003),
							},
						},
					})
				}
				if err != nil {
					serverErrors <- err
					return
				}
			case "unsubscribe":
				unsubscribes.Add(1)
			case "post":
				count := posts.Add(1)
				payload := map[string]any{
					"status": "ok",
					"response": map[string]any{
						"type": "cancel",
						"data": map[string]any{"statuses": []any{"success"}},
					},
				}
				if count == 1 {
					payload = map[string]any{
						"status": "ok",
						"response": map[string]any{
							"type": "order",
							"data": map[string]any{"statuses": []any{map[string]any{
								"filled": map[string]any{"oid": int64(41), "totalSz": "1", "avgPx": "101"},
							}}},
						},
					}
				}
				if err := conn.WriteJSON(map[string]any{
					"channel": "post",
					"data": map[string]any{
						"id": command.ID,
						"response": map[string]any{
							"type":    "action",
							"payload": payload,
						},
					},
				}); err != nil {
					serverErrors <- err
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	base := hyperliquid.NewWebsocketClient(ctx).
		WithURL("ws"+strings.TrimPrefix(server.URL, "http")).
		WithCredentials(privateKey, nil)
	base.SubscriptionAckTimeout = time.Second
	meta := hyperliquidMarketMeta{
		instrument: exchange.Instrument{
			Symbol:            "BTC-USDT",
			Product:           exchange.ProductPerp,
			QuantityIncrement: decimal.RequireFromString("0.001"),
		},
		nativeCoin:    "BTC",
		assetID:       1,
		sizeDecimals:  3,
		priceDecimals: 2,
		markPrice:     decimal.NewFromInt(105),
	}
	clientMeta := clientMeta{venue: exchange.VenueHyperliquid, product: exchange.ProductPerp}
	backend := &hyperliquidPrivateWSBackend{
		meta: clientMeta,
		base: base,
		user: base.AccountAddr,
		resolve: func(_ context.Context, _ string, instrument string) (hyperliquidMarketMeta, error) {
			if instrument != meta.instrument.Symbol {
				return hyperliquidMarketMeta{}, errors.New("unknown instrument")
			}
			return meta, nil
		},
		mid:       func(context.Context, string) (float64, error) { return 100, nil },
		perp:      hyperliquidperp.NewWebsocketClient(base),
		lifecycle: newBackendLifecycle(),
		cancel:    cancel,
		orders:    newHyperliquidPrivateHub[hyperliquid.WsOrderUpdate](),
		fills:     newHyperliquidPrivateHub[hyperliquidPrivateFill](),
		state:     newHyperliquidPrivateHub[hyperliquidPrivateState](),
	}
	backend.installReconnectHooks()
	socket := newPerpWebSocket(clientMeta, &fakePerpWSBackend{}, backend)
	t.Cleanup(func() { _ = socket.Close() })

	watchCtx, watchCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer watchCancel()
	orders, err := socket.WatchOrders(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	fills, err := socket.WatchFills(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	positionsResult := make(chan struct {
		sub exchange.Subscription[exchange.PositionEvent]
		err error
	}, 1)
	go func() {
		sub, err := socket.WatchPositions(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDT"})
		positionsResult <- struct {
			sub exchange.Subscription[exchange.PositionEvent]
			err error
		}{sub: sub, err: err}
	}()
	select {
	case <-stateReady:
	case err := <-serverErrors:
		t.Fatal(err)
	case <-watchCtx.Done():
		t.Fatal("state subscription timeout")
	}
	balances, err := socket.WatchBalances(watchCtx, exchange.WatchAccountRequest{})
	if err != nil {
		t.Fatal(err)
	}
	close(sendState)
	positionResult := <-positionsResult
	if positionResult.err != nil {
		t.Fatal(positionResult.err)
	}
	positions := positionResult.sub

	select {
	case event := <-orders.Events():
		if event.Order.OrderID != "7" ||
			event.Order.LimitPolicy != exchange.LimitPolicyPostOnly ||
			!event.Order.ReduceOnly {
			t.Fatalf("order event=%+v", event)
		}
	case err := <-serverErrors:
		t.Fatal(err)
	case <-watchCtx.Done():
		t.Fatal("order event timeout")
	}
	select {
	case event := <-fills.Events():
		if event.Kind != exchange.EventDelta ||
			event.Fill.FillID != "9" ||
			event.Fill.Instrument != "BTC-USDT" {
			t.Fatalf("fill event=%+v", event)
		}
	case err := <-serverErrors:
		t.Fatal(err)
	case <-watchCtx.Done():
		t.Fatal("fill event timeout")
	}
	select {
	case event := <-balances.Events():
		if len(event.Balances) != 1 ||
			!event.Balances[0].Available.Equal(decimal.NewFromInt(100)) {
			t.Fatalf("balance event=%+v", event)
		}
	case err := <-serverErrors:
		t.Fatal(err)
	case <-watchCtx.Done():
		t.Fatal("balance event timeout")
	}
	select {
	case event := <-positions.Events():
		if len(event.Positions) != 1 ||
			event.Positions[0].Instrument != "BTC-USDT" ||
			event.Positions[0].Side != exchange.SideSell {
			t.Fatalf("position event=%+v", event)
		}
	case err := <-serverErrors:
		t.Fatal(err)
	case <-watchCtx.Done():
		t.Fatal("position event timeout")
	}

	placeAck, err := socket.PlaceOrder(watchCtx, exchange.PlaceOrderRequest{
		Instrument:    "BTC-USDT",
		ClientOrderID: "17",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeMarket,
		Quantity:      decimal.NewFromInt(1),
		ReduceOnly:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if placeAck.State != exchange.AckImmediatelyFilled ||
		placeAck.OrderType != exchange.OrderTypeMarket ||
		placeAck.OrderID != "41" {
		t.Fatalf("place ack=%+v", placeAck)
	}
	cancelAck, err := socket.CancelOrder(watchCtx, exchange.CancelOrderRequest{
		Instrument: "BTC-USDT",
		OrderID:    "41",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cancelAck.State != exchange.AckAcceptedPending || cancelAck.OrderID != "41" {
		t.Fatalf("cancel ack=%+v", cancelAck)
	}

	for _, subscription := range []interface{ Close() error }{orders, fills, balances, positions} {
		if err := subscription.Close(); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for unsubscribes.Load() != 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stateSubscriptions.Load() != 1 || unsubscribes.Load() != 3 || posts.Load() != 2 {
		t.Fatalf("state subscriptions=%d unsubscribes=%d posts=%d", stateSubscriptions.Load(), unsubscribes.Load(), posts.Load())
	}
}

func TestHyperliquidPerpPrivateWebSocketSharesSameSemanticSubscribers(t *testing.T) {
	const privateKey = "0x0000000000000000000000000000000000000000000000000000000000000001"
	var orderSubscriptions atomic.Int32
	var fillSubscriptions atomic.Int32
	var stateSubscriptions atomic.Int32
	var unsubscribes atomic.Int32
	sendEvents := make(chan struct{})
	serverErrors := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(w, request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var sendOnce sync.Once
		for {
			var command struct {
				Method       string          `json:"method"`
				Subscription json.RawMessage `json:"subscription"`
			}
			if err := conn.ReadJSON(&command); err != nil {
				return
			}
			switch command.Method {
			case "subscribe":
				var subscription map[string]any
				if err := json.Unmarshal(command.Subscription, &subscription); err != nil {
					serverErrors <- err
					return
				}
				if err := conn.WriteJSON(map[string]any{
					"channel": "subscriptionResponse",
					"data":    json.RawMessage(command.Subscription),
				}); err != nil {
					serverErrors <- err
					return
				}
				switch subscription["type"] {
				case "orderUpdates":
					orderSubscriptions.Add(1)
				case "userFills":
					fillSubscriptions.Add(1)
				case "clearinghouseState":
					stateSubscriptions.Add(1)
				}
				sendOnce.Do(func() {
					go func() {
						<-sendEvents
						if err := conn.WriteJSON(map[string]any{
							"channel": "orderUpdates",
							"data": []map[string]any{{
								"order": map[string]any{
									"coin": "BTC", "side": "B", "limitPx": "100", "sz": "1",
									"origSz": "1", "oid": int64(7), "timestamp": int64(1_700_000_000_000),
									"orderType": "Limit", "tif": "Gtc",
								},
								"status": "open", "statusTimestamp": int64(1_700_000_000_001),
							}},
						}); err != nil {
							serverErrors <- err
							return
						}
						if err := conn.WriteJSON(map[string]any{
							"channel": "userFills",
							"data": map[string]any{
								"user":       subscription["user"],
								"isSnapshot": false,
								"fills": []map[string]any{{
									"coin": "BTC", "px": "100", "sz": "0.5", "side": "B",
									"time": int64(1_700_000_000_002), "hash": "0xfill",
									"oid": int64(7), "tid": int64(9), "fee": "0.01",
									"feeToken": "USDT", "crossed": true,
								}},
							},
						}); err != nil {
							serverErrors <- err
							return
						}
						if err := conn.WriteJSON(map[string]any{
							"channel": "clearinghouseState",
							"data": map[string]any{
								"user": subscription["user"],
								"dex":  "",
								"clearinghouseState": map[string]any{
									"assetPositions": []map[string]any{{
										"position": map[string]any{
											"coin": "BTC", "entryPx": "100", "leverage": map[string]any{"value": 5},
											"liquidationPx": "50", "marginUsed": "20", "szi": "2", "unrealizedPnl": "3",
										},
									}},
									"marginSummary": map[string]any{"accountValue": "120", "totalMarginUsed": "20"},
									"withdrawable":  "100",
									"time":          int64(1_700_000_000_003),
								},
							},
						}); err != nil {
							serverErrors <- err
						}
					}()
				})
			case "unsubscribe":
				unsubscribes.Add(1)
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	base := hyperliquid.NewWebsocketClient(ctx).
		WithURL("ws"+strings.TrimPrefix(server.URL, "http")).
		WithCredentials(privateKey, nil)
	base.SubscriptionAckTimeout = time.Second
	meta := hyperliquidMarketMeta{
		instrument: exchange.Instrument{
			Symbol:            "BTC-USDT",
			Product:           exchange.ProductPerp,
			QuantityIncrement: decimal.RequireFromString("0.001"),
		},
		nativeCoin:    "BTC",
		assetID:       1,
		sizeDecimals:  3,
		priceDecimals: 2,
		markPrice:     decimal.NewFromInt(105),
	}
	clientMeta := clientMeta{venue: exchange.VenueHyperliquid, product: exchange.ProductPerp}
	backend := &hyperliquidPrivateWSBackend{
		meta: clientMeta,
		base: base,
		user: base.AccountAddr,
		resolve: func(_ context.Context, _ string, instrument string) (hyperliquidMarketMeta, error) {
			if instrument != meta.instrument.Symbol {
				return hyperliquidMarketMeta{}, errors.New("unknown instrument")
			}
			return meta, nil
		},
		mid:       func(context.Context, string) (float64, error) { return 100, nil },
		perp:      hyperliquidperp.NewWebsocketClient(base),
		lifecycle: newBackendLifecycle(),
		cancel:    cancel,
		orders:    newHyperliquidPrivateHub[hyperliquid.WsOrderUpdate](),
		fills:     newHyperliquidPrivateHub[hyperliquidPrivateFill](),
		state:     newHyperliquidPrivateHub[hyperliquidPrivateState](),
	}
	backend.installReconnectHooks()
	socket := newPerpWebSocket(clientMeta, &fakePerpWSBackend{}, backend)
	t.Cleanup(func() { _ = socket.Close() })

	watchCtx, watchCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer watchCancel()
	ordersA, err := socket.WatchOrders(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	ordersB, err := socket.WatchOrders(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	fillsA, err := socket.WatchFills(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	fillsB, err := socket.WatchFills(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	balancesA, err := socket.WatchBalances(watchCtx, exchange.WatchAccountRequest{})
	if err != nil {
		t.Fatal(err)
	}
	balancesB, err := socket.WatchBalances(watchCtx, exchange.WatchAccountRequest{})
	if err != nil {
		t.Fatal(err)
	}
	positionsA, err := socket.WatchPositions(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	positionsB, err := socket.WatchPositions(watchCtx, exchange.WatchRequest{Instrument: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if orderSubscriptions.Load() != 1 || fillSubscriptions.Load() != 1 || stateSubscriptions.Load() != 1 {
		t.Fatalf("native subscriptions orders=%d fills=%d state=%d", orderSubscriptions.Load(), fillSubscriptions.Load(), stateSubscriptions.Load())
	}

	close(sendEvents)
	for _, sub := range []exchange.Subscription[exchange.OrderEvent]{ordersA, ordersB} {
		select {
		case event := <-sub.Events():
			if event.Order.OrderID != "7" || event.Order.Instrument != "BTC-USDT" {
				t.Fatalf("order event=%+v", event)
			}
		case err := <-serverErrors:
			t.Fatal(err)
		case <-watchCtx.Done():
			t.Fatal("order event timeout")
		}
	}
	for _, sub := range []exchange.Subscription[exchange.FillEvent]{fillsA, fillsB} {
		select {
		case event := <-sub.Events():
			if event.Fill.FillID != "9" || event.Fill.Instrument != "BTC-USDT" {
				t.Fatalf("fill event=%+v", event)
			}
		case err := <-serverErrors:
			t.Fatal(err)
		case <-watchCtx.Done():
			t.Fatal("fill event timeout")
		}
	}
	for _, sub := range []exchange.Subscription[exchange.BalanceEvent]{balancesA, balancesB} {
		select {
		case event := <-sub.Events():
			if len(event.Balances) != 1 || !event.Balances[0].Available.Equal(decimal.NewFromInt(100)) {
				t.Fatalf("balance event=%+v", event)
			}
		case err := <-serverErrors:
			t.Fatal(err)
		case <-watchCtx.Done():
			t.Fatal("balance event timeout")
		}
	}
	for _, sub := range []exchange.Subscription[exchange.PositionEvent]{positionsA, positionsB} {
		select {
		case event := <-sub.Events():
			if len(event.Positions) != 1 || event.Positions[0].Instrument != "BTC-USDT" {
				t.Fatalf("position event=%+v", event)
			}
		case err := <-serverErrors:
			t.Fatal(err)
		case <-watchCtx.Done():
			t.Fatal("position event timeout")
		}
	}

	for _, subscription := range []interface{ Close() error }{ordersA, fillsA, balancesA, positionsA} {
		if err := subscription.Close(); err != nil {
			t.Fatal(err)
		}
		if err := subscription.Close(); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if unsubscribes.Load() != 0 {
		t.Fatalf("native unsubscribe before last owner closed: %d", unsubscribes.Load())
	}
	for _, subscription := range []interface{ Close() error }{ordersB, fillsB, balancesB, positionsB} {
		if err := subscription.Close(); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for unsubscribes.Load() != 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if unsubscribes.Load() != 3 {
		t.Fatalf("unsubscribes=%d", unsubscribes.Load())
	}
}

func TestHyperliquidPrivateStateNormalizers(t *testing.T) {
	var state hyperliquidperp.PerpPosition
	if err := json.Unmarshal([]byte(`{
		"assetPositions":[{"position":{"coin":"BTC","entryPx":"100","leverage":{"value":5},"liquidationPx":"50","marginUsed":"20","szi":"2","unrealizedPnl":"3"}}],
		"marginSummary":{"accountValue":"120","totalMarginUsed":"20"},
		"withdrawable":"100",
		"time":1700000000000
	}`), &state); err != nil {
		t.Fatal(err)
	}
	balanceEvent, err := hyperliquidPerpBalanceEvent(state)
	if err != nil {
		t.Fatalf("balance event: %v", err)
	}
	if balanceEvent.Kind != exchange.EventSnapshot ||
		len(balanceEvent.Balances) != 1 ||
		!balanceEvent.Balances[0].Available.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("balance event=%+v", balanceEvent)
	}
	meta := hyperliquidMarketMeta{
		instrument: exchange.Instrument{Symbol: "BTC-USDT"},
		nativeCoin: "BTC",
		markPrice:  decimal.NewFromInt(105),
	}
	positionEvent, err := hyperliquidPerpPositionEvent(meta, state)
	if err != nil {
		t.Fatalf("position event: %v", err)
	}
	if positionEvent.Kind != exchange.EventSnapshot ||
		len(positionEvent.Positions) != 1 ||
		positionEvent.Positions[0].Instrument != "BTC-USDT" {
		t.Fatalf("position event=%+v", positionEvent)
	}
}

func TestHyperliquidPrivateStateNormalizersRejectImpossibleAccountState(t *testing.T) {
	var state hyperliquidperp.PerpPosition
	if err := json.Unmarshal([]byte(`{
		"assetPositions":[],
		"marginSummary":{"accountValue":"10","totalMarginUsed":"0"},
		"withdrawable":"11",
		"time":1700000000000
	}`), &state); err != nil {
		t.Fatal(err)
	}
	if _, err := hyperliquidPerpBalanceEvent(state); err == nil {
		t.Fatal("withdrawable greater than account value was accepted")
	}
	state.Withdrawable = "10"
	state.Time = 0
	if _, err := hyperliquidPerpBalanceEvent(state); err == nil {
		t.Fatal("missing state timestamp was accepted")
	}
}

var (
	_ = hyperliquidspot.PlaceOrderResponse{}
	_ = hyperliquidperp.PlaceOrderResponse{}
)
