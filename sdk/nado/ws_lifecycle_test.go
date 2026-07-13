package nado

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const wsTestPrivateKey = "1111111111111111111111111111111111111111111111111111111111111111"

func wsURLFromHTTP(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	u.Scheme = strings.Replace(u.Scheme, "http", "ws", 1)
	return u.String()
}

func newWsTestnetRESTClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return newNadoClientForServer(t, server)
}

func TestWsMarketClientUsesTestnetProfileURL(t *testing.T) {
	t.Parallel()

	profile, err := NewProfile(EnvironmentTestnet)
	require.NoError(t, err)
	client, err := NewWsMarketClient(context.Background(), profile)
	require.NoError(t, err)

	require.Equal(t, profile.SubscriptionsWSURL(), client.url)
	require.Contains(t, client.url, ".test.")
	require.NotContains(t, client.url, ".prod.")
}

func TestWsConstructorsUseSelectedProfileEndpoints(t *testing.T) {
	for _, environment := range []Environment{EnvironmentMainnet, EnvironmentTestnet} {
		t.Run(string(environment), func(t *testing.T) {
			profile, err := NewProfile(environment)
			require.NoError(t, err)
			restClient, err := NewClient(profile)
			require.NoError(t, err)
			restClient, err = restClient.WithCredentials(wsTestPrivateKey, "default")
			require.NoError(t, err)

			market, err := NewWsMarketClient(context.Background(), profile)
			require.NoError(t, err)
			require.Equal(t, profile.SubscriptionsWSURL(), market.url)

			account, err := NewWsAccountClient(context.Background(), restClient)
			require.NoError(t, err)
			require.Equal(t, profile.SubscriptionsWSURL(), account.url)

			gateway, err := NewWsApiClient(context.Background(), restClient)
			require.NoError(t, err)
			require.Equal(t, profile.GatewayWSURL(), gateway.baseWsClient.url)
		})
	}
}

func TestWsAPIReconnectRetainsProfileURL(t *testing.T) {
	t.Parallel()

	var upgrade websocket.Upgrader
	connections := make(chan struct{}, 2)
	var connectionCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrade.Upgrade(w, r, nil)
		require.NoError(t, err)
		connections <- struct{}{}
		if connectionCount.Add(1) == 1 {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			_ = conn.Close()
			return
		}
		defer conn.Close()
		<-r.Context().Done()
	}))
	defer server.Close()

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsApiClient(context.Background(), restClient)
	require.NoError(t, err)
	client.baseWsClient.url = wsURLFromHTTP(server.URL)
	defer client.Close()
	require.NoError(t, client.Connect())

	for range 2 {
		select {
		case <-connections:
		case <-time.After(4 * time.Second):
			t.Fatal("timed out waiting for gateway WS reconnect")
		}
	}
	require.Equal(t, wsURLFromHTTP(server.URL), client.baseWsClient.url)
}

func TestSubscriptionConnectIsIdempotent(t *testing.T) {
	var upgrade websocket.Upgrader
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrade.Upgrade(w, r, nil)
		require.NoError(t, err)
		connections.Add(1)
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	profile, err := NewProfile(EnvironmentTestnet)
	require.NoError(t, err)

	market, err := NewWsMarketClient(context.Background(), profile)
	require.NoError(t, err)
	market.url = wsURLFromHTTP(server.URL)
	require.NoError(t, market.Connect())
	require.NoError(t, market.Connect())
	market.Close()

	restClient, err := NewClient(profile)
	require.NoError(t, err)
	restClient, err = restClient.WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	account, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	account.url = wsURLFromHTTP(server.URL)
	require.NoError(t, account.Connect())
	require.NoError(t, account.Connect())
	account.Close()

	require.Equal(t, int32(2), connections.Load())
}

func TestWsMarketReconnectRetainsURLAndResubscribesOnce(t *testing.T) {
	t.Parallel()

	var upgrade websocket.Upgrader
	subscriptions := make(chan map[string]any, 2)
	connections := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotContains(t, r.Host, "prod.nado.xyz")
		conn, err := upgrade.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		n := connections.Add(1)
		var req map[string]any
		require.NoError(t, conn.ReadJSON(&req))
		subscriptions <- req
		if n == 1 {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			return
		}
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	profile, err := NewProfile(EnvironmentTestnet)
	require.NoError(t, err)
	client, err := NewWsMarketClient(context.Background(), profile)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	defer client.Close()

	require.NoError(t, client.Connect())
	require.NoError(t, client.SubscribeTrades(2, nil))

	first := <-subscriptions
	require.Equal(t, "subscribe", first["method"])
	require.Equal(t, client.url, wsURLFromHTTP(server.URL))

	select {
	case second := <-subscriptions:
		require.Equal(t, "subscribe", second["method"])
		require.Equal(t, float64(2), second["stream"].(map[string]any)["product_id"])
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for reconnect resubscribe")
	}

	select {
	case extra := <-subscriptions:
		t.Fatalf("unexpected duplicate resubscribe: %#v", extra)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestWsAccountAuthDiscoversTestnetEndpointAndSubscribes(t *testing.T) {
	t.Parallel()

	contractsSeen := atomic.Int32{}
	restClient := newWsTestnetRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/query", r.URL.Path)
		require.NotContains(t, r.Host, "prod.nado.xyz")
		require.Equal(t, "contracts", r.URL.Query().Get("type"))
		contractsSeen.Add(1)
		_, _ = io.WriteString(w, `{"status":"success","data":{"chain_id":"763373","endpoint_addr":"0x4444444444444444444444444444444444444444"}}`)
	}))
	restClient, err := restClient.WithCredentials(wsTestPrivateKey, "arb")
	require.NoError(t, err)

	var upgrade websocket.Upgrader
	received := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotContains(t, r.Host, "prod.nado.xyz")
		conn, err := upgrade.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		for i := 0; i < 2; i++ {
			var req map[string]any
			require.NoError(t, conn.ReadJSON(&req))
			received <- req
			if req["method"] == "authenticate" {
				require.NoError(t, conn.WriteJSON(map[string]any{"id": float64(AuthRequestID)}))
			} else if req["method"] == "subscribe" {
				require.NoError(t, conn.WriteJSON(map[string]any{"id": req["id"], "status": "success"}))
			}
		}
	}))
	defer server.Close()

	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	defer client.Close()

	productID := int64(2)
	require.NoError(t, client.SubscribeOrders(&productID, nil))
	require.NoError(t, client.Connect())

	auth := <-received
	require.Equal(t, "authenticate", auth["method"])
	subscribe := <-received
	require.Equal(t, "subscribe", subscribe["method"])
	require.Equal(t, int32(1), contractsSeen.Load())
}

func TestWsAccountConnectFailsWhenSubscriptionIsRejected(t *testing.T) {
	t.Parallel()

	restClient := newWsTestnetRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "contracts", r.URL.Query().Get("type"))
		_, _ = io.WriteString(w, nadoFixtureBody(t, "contracts.json"))
	}))
	restClient, err := restClient.WithCredentials(wsTestPrivateKey, "arb")
	require.NoError(t, err)

	var upgrade websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrade.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		for range 2 {
			var request map[string]any
			require.NoError(t, conn.ReadJSON(&request))
			switch request["method"] {
			case "authenticate":
				require.NoError(t, conn.WriteJSON(map[string]any{"id": float64(AuthRequestID)}))
			case "subscribe":
				require.NoError(t, conn.WriteJSON(map[string]any{"id": request["id"], "error": "invalid wildcard stream"}))
			}
		}
	}))
	defer server.Close()

	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	defer client.Close()
	require.NoError(t, client.SubscribeOrders(nil, nil))
	err = client.Connect()
	require.ErrorContains(t, err, "invalid wildcard stream")
}

func TestWsAccountReconnectReauthenticatesAndResubscribesOnce(t *testing.T) {
	t.Parallel()

	restClient := newWsTestnetRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "contracts", r.URL.Query().Get("type"))
		_, _ = io.WriteString(w, nadoFixtureBody(t, "contracts.json"))
	}))
	restClient, err := restClient.WithCredentials(wsTestPrivateKey, "arb")
	require.NoError(t, err)

	var upgrade websocket.Upgrader
	var connections atomic.Int32
	messages := make(chan map[string]any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrade.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		connection := connections.Add(1)
		for range 2 {
			var request map[string]any
			require.NoError(t, conn.ReadJSON(&request))
			messages <- request
			if request["method"] == "authenticate" {
				require.NoError(t, conn.WriteJSON(map[string]any{"id": float64(AuthRequestID)}))
			} else if request["method"] == "subscribe" {
				require.NoError(t, conn.WriteJSON(map[string]any{"id": request["id"], "status": "success"}))
			}
		}
		if connection == 1 {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			return
		}
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	client.url = wsURLFromHTTP(server.URL)
	defer client.Close()
	productID := int64(2)
	require.NoError(t, client.SubscribeOrders(&productID, nil))
	require.NoError(t, client.Connect())

	for connection := 1; connection <= 2; connection++ {
		for _, method := range []string{"authenticate", "subscribe"} {
			select {
			case message := <-messages:
				require.Equal(t, method, message["method"], "connection %d", connection)
			case <-time.After(4 * time.Second):
				t.Fatalf("timed out waiting for %s on connection %d", method, connection)
			}
		}
	}
	require.Equal(t, int32(2), connections.Load())
	require.Equal(t, wsURLFromHTTP(server.URL), client.url)
}

func TestWsFixtureRoutingPublicAndPrivateStreams(t *testing.T) {
	t.Parallel()

	market := &WsMarketClient{
		subscriptions: make(map[string]*marketSubscription),
		Logger:        zap.NewNop().Sugar().Named("test-market"),
	}

	trades := make(chan *Trade, 1)
	require.NoError(t, market.SubscribeTrades(2, func(trade *Trade) {
		trades <- trade
	}))
	market.handleMessage([]byte(nadoFixtureBody(t, "trade_stream.json")))
	require.Equal(t, int64(2), (<-trades).ProductId)

	books := make(chan *OrderBook, 1)
	require.NoError(t, market.SubscribeOrderBook(2, func(book *OrderBook) {
		books <- book
	}))
	market.handleMessage([]byte(`{"type":"book_depth","product_id":2,"bids":[["2500","1"]],"asks":[["2501","2"]]}`))
	require.Len(t, (<-books).Bids, 1)

	oneMinute := make(chan *Candlestick, 1)
	fiveMinute := make(chan *Candlestick, 1)
	require.NoError(t, market.SubscribeLatestCandlestick(2, 60, func(candle *Candlestick) { oneMinute <- candle }))
	require.NoError(t, market.SubscribeLatestCandlestick(2, 300, func(candle *Candlestick) { fiveMinute <- candle }))
	market.handleMessage([]byte(`{"type":"latest_candlestick","timestamp":"1783641600000","product_id":2,"granularity":60,"open_x18":"1","high_x18":"2","low_x18":"1","close_x18":"2","volume":"10"}`))
	market.handleMessage([]byte(`{"type":"latest_candlestick","timestamp":"1783641600000","product_id":2,"granularity":300,"open_x18":"2","high_x18":"3","low_x18":"2","close_x18":"3","volume":"20"}`))
	require.Equal(t, int32(60), (<-oneMinute).Granularity)
	require.Equal(t, int32(300), (<-fiveMinute).Granularity)

	funding := make(chan *FundingRate, 1)
	require.NoError(t, market.SubscribeFundingRate(nil, func(rate *FundingRate) {
		funding <- rate
	}))
	market.handleMessage([]byte(nadoFixtureBody(t, "funding_rate_stream.json")))
	require.Equal(t, "50000000000000000", (<-funding).FundingRateX18)

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "arb")
	require.NoError(t, err)
	account, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)

	orders := make(chan *OrderUpdate, 1)
	require.NoError(t, account.SubscribeOrders(nil, func(order *OrderUpdate) {
		orders <- order
	}))
	account.handleMessage([]byte(nadoFixtureBody(t, "order_update_stream.json")))
	require.Equal(t, OrderReasonPlaced, (<-orders).Reason)

	fills := make(chan *Fill, 1)
	require.NoError(t, account.SubscribeFills(nil, func(fill *Fill) {
		fills <- fill
	}))
	account.handleMessage([]byte(nadoFixtureBody(t, "fill_stream.json")))
	require.Equal(t, "100000000000000000", (<-fills).FilledQty)

	positions := make(chan *PositionChange, 1)
	require.NoError(t, account.SubscribePositions(nil, func(position *PositionChange) {
		positions <- position
	}))
	account.handleMessage([]byte(nadoFixtureBody(t, "position_change_stream.json")))
	require.Equal(t, PositionReasonMatchOrders, (<-positions).Reason)
}

func TestPreparedOrderCarriesSpotLeverageFalseAndExecutesExactRequest(t *testing.T) {
	t.Parallel()

	restClient := newWsTestnetRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/query", r.URL.Path)
		require.NotContains(t, r.Host, "prod.nado.xyz")
		switch r.URL.Query().Get("type") {
		case "status":
			_, _ = io.WriteString(w, nadoFixtureBody(t, "status.json"))
		case "all_products":
			_, _ = io.WriteString(w, nadoFixtureBody(t, "all_products.json"))
		case "symbols":
			_, _ = io.WriteString(w, nadoFixtureBody(t, "symbols.json"))
		case "contracts":
			_, _ = io.WriteString(w, nadoFixtureBody(t, "contracts.json"))
		default:
			var req map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			switch req["type"] {
			case "symbols":
				_, _ = io.WriteString(w, nadoFixtureBody(t, "symbols.json"))
			case "contracts":
				_, _ = io.WriteString(w, nadoFixtureBody(t, "contracts.json"))
			default:
				t.Fatalf("unexpected REST query for prepared order: method=%s query=%s body=%#v", r.Method, r.URL.RawQuery, req)
			}
		}
	}))
	restClient, err := restClient.WithCredentials(wsTestPrivateKey, "arb")
	require.NoError(t, err)
	client, err := NewWsApiClient(context.Background(), restClient)
	require.NoError(t, err)

	var upgrade websocket.Upgrader
	received := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotContains(t, r.Host, "prod.nado.xyz")
		conn, err := upgrade.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		var req map[string]any
		require.NoError(t, conn.ReadJSON(&req))
		received <- req
		id := req["place_order"].(map[string]any)["id"]
		require.NoError(t, conn.WriteJSON(map[string]any{
			"id":     id,
			"status": "success",
			"data":   json.RawMessage(`{"digest":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		}))
	}))
	defer server.Close()
	client.baseWsClient.url = wsURLFromHTTP(server.URL)
	require.NoError(t, client.Connect())
	defer client.Close()

	borrowMargin := false
	prepared, err := client.PrepareOrder(context.Background(), ClientOrderInput{
		ProductId:    1,
		Price:        "2500",
		Amount:       "0.1",
		Side:         OrderSideBuy,
		OrderType:    OrderTypeLimit,
		BorrowMargin: &borrowMargin,
	})
	require.NoError(t, err)
	wantEncoded, err := EncodeSignedOrder(prepared.Tx, prepared.Signature)
	require.NoError(t, err)
	require.Equal(t, wantEncoded, prepared.EncodedOrder)
	prepared.Request["sentinel"] = "mutation"
	_, err = client.ExecutePreparedOrder(context.Background(), prepared)
	require.ErrorContains(t, err, "payload mismatch")
	delete(prepared.Request, "sentinel")

	resp, err := client.ExecutePreparedOrder(context.Background(), prepared)
	require.NoError(t, err)
	require.Equal(t, "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", resp.Digest)

	req := <-received
	require.NotContains(t, req, "sentinel")
	placeOrder := req["place_order"].(map[string]any)
	require.Equal(t, false, placeOrder["spot_leverage"])
	require.Equal(t, false, placeOrder["borrow_margin"])
}

func TestWsApiExecuteCorrelatesWriteResponsesByNestedRequestID(t *testing.T) {
	var upgrade websocket.Upgrader
	received := make(chan map[string]any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrade.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()

		for i := 0; i < 4; i++ {
			var req map[string]any
			require.NoError(t, conn.ReadJSON(&req))
			received <- req
			key, body := singleGatewayRequestBody(t, req)
			id, ok := body["id"]
			require.True(t, ok, "%s request missing generated nested id: %#v", key, body)
			resp := map[string]any{"id": id, "status": "success", "data": json.RawMessage(`{"digest":"0xid"}`)}
			if i%2 == 0 {
				resp["signature"] = "different-response-signature"
			}
			require.NoError(t, conn.WriteJSON(resp))
		}
	}))
	defer server.Close()

	client := &WsApiClient{ctx: context.Background(), Logger: zap.NewNop().Sugar().Named("test-gateway")}
	client.baseWsClient = newBaseWsClient(context.Background(), wsURLFromHTTP(server.URL), client.handleMessage)
	require.NoError(t, client.Connect())
	defer client.Close()

	requests := []struct {
		name string
		req  map[string]any
	}{
		{name: "place", req: map[string]any{"place_order": map[string]any{"product_id": float64(2), "signature": "place-sig", "id": int64(901)}}},
		{name: "cancel", req: map[string]any{"cancel_orders": map[string]any{"signature": "cancel-sig", "id": int64(902)}}},
		{name: "cancel_and_place", req: map[string]any{"cancel_and_place": map[string]any{"id": int64(903), "cancel_signature": "cancel-sig", "place_order": map[string]any{"signature": "place-sig"}}}},
		{name: "prepared_execute", req: map[string]any{"place_order": map[string]any{"product_id": float64(2), "signature": "prepared-sig", "id": float64(909)}}},
	}
	for _, tc := range requests {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		resp, err := client.Execute(ctx, tc.req, strPtrForTest(tc.name+"-sig"))
		cancel()
		require.NoError(t, err, tc.name)
		require.Equal(t, "success", resp.Status, tc.name)
	}
	for range requests {
		<-received
	}
}

func TestWsApiWriteMethodsCorrelateByIDWithoutResponseSignature(t *testing.T) {
	restClient := newWsTestnetRESTClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/query", r.URL.Path)
		require.NotContains(t, r.Host, "prod.nado.xyz")
		switch r.URL.Query().Get("type") {
		case "status":
			_, _ = io.WriteString(w, nadoFixtureBody(t, "status.json"))
		case "contracts":
			_, _ = io.WriteString(w, nadoFixtureBody(t, "contracts.json"))
		case "all_products":
			_, _ = io.WriteString(w, nadoFixtureBody(t, "all_products.json"))
		case "symbols":
			_, _ = io.WriteString(w, nadoFixtureBody(t, "symbols.json"))
		default:
			var req map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			switch req["type"] {
			case "contracts":
				_, _ = io.WriteString(w, nadoFixtureBody(t, "contracts.json"))
			case "all_products":
				_, _ = io.WriteString(w, nadoFixtureBody(t, "all_products.json"))
			case "symbols":
				_, _ = io.WriteString(w, nadoFixtureBody(t, "symbols.json"))
			default:
				t.Fatalf("unexpected REST query: method=%s query=%s body=%#v", r.Method, r.URL.RawQuery, req)
			}
		}
	}))
	restClient, err := restClient.WithCredentials(wsTestPrivateKey, "arb")
	require.NoError(t, err)
	client, err := NewWsApiClient(context.Background(), restClient)
	require.NoError(t, err)

	var upgrade websocket.Upgrader
	requests := make(chan map[string]any, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrade.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		for i := 0; i < 5; i++ {
			var req map[string]any
			require.NoError(t, conn.ReadJSON(&req))
			requests <- req
			id := gatewayIDFromRequest(t, req)
			require.NoError(t, conn.WriteJSON(map[string]any{
				"id":     id,
				"status": "success",
				"data":   json.RawMessage(`{"digest":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","cancelled_orders":[]}`),
			}))
		}
	}))
	defer server.Close()
	client.baseWsClient.url = wsURLFromHTTP(server.URL)
	require.NoError(t, client.Connect())
	defer client.Close()

	borrowMargin := false
	spotLeverage := false
	_, err = client.PlaceOrder(context.Background(), ClientOrderInput{ProductId: 1, Price: "2500", Amount: "0.1", Side: OrderSideBuy, OrderType: OrderTypeLimit, BorrowMargin: &borrowMargin, SpotLeverage: &spotLeverage})
	require.NoError(t, err)
	prepared, err := client.PrepareOrder(context.Background(), ClientOrderInput{ProductId: 1, Price: "2500", Amount: "0.1", Side: OrderSideBuy, OrderType: OrderTypeLimit, BorrowMargin: &borrowMargin, SpotLeverage: &spotLeverage})
	require.NoError(t, err)
	_, err = client.ExecutePreparedOrder(context.Background(), prepared)
	require.NoError(t, err)
	digest := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err = client.CancelOrders(context.Background(), CancelOrdersInput{ProductIds: []int64{1}, Digests: []string{digest}})
	require.NoError(t, err)
	_, err = client.CancelProductOrders(context.Background(), []int64{1})
	require.NoError(t, err)
	_, err = client.CancelAndPlace(context.Background(), CancelOrdersInput{ProductIds: []int64{1}, Digests: []string{digest}}, ClientOrderInput{ProductId: 1, Price: "2500", Amount: "0.1", Side: OrderSideBuy, OrderType: OrderTypeLimit, BorrowMargin: &borrowMargin, SpotLeverage: &spotLeverage})
	require.NoError(t, err)

	seen := map[int64]string{}
	for range 5 {
		req := <-requests
		key, _ := singleGatewayRequestBody(t, req)
		id := gatewayIDFromRequest(t, req)
		if prior, exists := seen[id]; exists {
			t.Fatalf("duplicate request id %d for %s and %s", id, prior, key)
		}
		seen[id] = key
	}
	require.Len(t, seen, 5)
}

func TestWsApiExecuteConcurrentRequestIDsDoNotOverwritePending(t *testing.T) {
	const n = 32
	var upgrade websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrade.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		for i := 0; i < n; i++ {
			var req map[string]any
			require.NoError(t, conn.ReadJSON(&req))
			id := gatewayIDFromRequest(t, req)
			require.NoError(t, conn.WriteJSON(map[string]any{"id": id, "status": "success", "data": json.RawMessage(`{"digest":"0xid"}`)}))
		}
	}))
	defer server.Close()

	client := &WsApiClient{ctx: context.Background(), Logger: zap.NewNop().Sugar().Named("test-gateway")}
	client.baseWsClient = newBaseWsClient(context.Background(), wsURLFromHTTP(server.URL), client.handleMessage)
	require.NoError(t, client.Connect())
	defer client.Close()

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			resp, err := client.Execute(ctx, map[string]any{"place_order": map[string]any{"product_id": float64(2), "signature": "sig", "id": nextGatewayRequestID()}}, strPtrForTest("sig"))
			if err != nil {
				errs <- err
				return
			}
			if resp == nil || resp.Status != "success" {
				errs <- io.ErrUnexpectedEOF
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func gatewayIDFromRequest(t *testing.T, req map[string]any) int64 {
	t.Helper()
	key, body := singleGatewayRequestBody(t, req)
	id, ok := body["id"]
	require.True(t, ok, "%s request missing id: %#v", key, body)
	switch v := id.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, err := v.Int64()
		require.NoError(t, err)
		return n
	default:
		t.Fatalf("%s request id has unexpected type %T", key, id)
		return 0
	}
}

func singleGatewayRequestBody(t *testing.T, req map[string]any) (string, map[string]any) {
	t.Helper()
	require.Len(t, req, 1)
	for key, value := range req {
		body, ok := value.(map[string]any)
		require.True(t, ok, "%s body: %#v", key, value)
		return key, body
	}
	t.Fatal("empty request")
	return "", nil
}

func strPtrForTest(value string) *string { return &value }
