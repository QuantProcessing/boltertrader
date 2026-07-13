package spot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

func TestSpotPrivateStreamFixturesDecodeAndRoute(t *testing.T) {
	profile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewWsAccountClient(context.Background(), profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	execution := make(chan *ExecutionReportEvent, 1)
	account := make(chan *AccountPositionEvent, 1)
	client.SubscribeExecutionReport(func(event *ExecutionReportEvent) { execution <- event })
	client.SubscribeAccountPosition(func(event *AccountPositionEvent) { account <- event })

	client.handleMessage(readSpotFixture(t, "execution_report.json"))
	client.handleMessage(readSpotFixture(t, "account_update.json"))

	select {
	case event := <-execution:
		if event.OrderID != 10001 || event.AveragePrice != "1.2500" || event.OriginalOrderType != "LIMIT" {
			t.Fatalf("execution event = %+v", event)
		}
		if event.CommissionAsset == nil || *event.CommissionAsset != "ASTER" {
			t.Fatalf("commission asset = %v", event.CommissionAsset)
		}
	default:
		t.Fatal("execution report was not routed")
	}
	select {
	case event := <-account:
		if event.LastAccountUpdate != 1783641600190 || event.Reason != "TRADE" || len(event.Balances) != 2 {
			t.Fatalf("account event = %+v", event)
		}
	default:
		t.Fatal("account update was not routed")
	}
}

func TestSpotExecutionReportLifecycleFixtures(t *testing.T) {
	profile, _ := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	client, err := NewWsAccountClient(context.Background(), profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	events := make(chan *ExecutionReportEvent, 3)
	client.SubscribeExecutionReport(func(event *ExecutionReportEvent) { events <- event })
	for _, fixture := range []string{"execution_report_new.json", "execution_report.json", "execution_report_canceled.json"} {
		client.handleMessage(readSpotFixture(t, fixture))
	}

	want := []struct {
		execution string
		status    string
		tradeID   int64
	}{
		{execution: "NEW", status: "NEW", tradeID: -1},
		{execution: "TRADE", status: "PARTIALLY_FILLED", tradeID: 20001},
		{execution: "CANCELED", status: "CANCELED", tradeID: -1},
	}
	for index, expected := range want {
		select {
		case event := <-events:
			if event.ExecutionType != expected.execution || event.OrderStatus != expected.status || event.TradeID != expected.tradeID {
				t.Fatalf("event %d = %+v", index, event)
			}
			if expected.execution != "TRADE" && event.CommissionAsset != nil {
				t.Fatalf("event %d commission asset = %v, want absent", index, event.CommissionAsset)
			}
		default:
			t.Fatalf("event %d was not routed", index)
		}
	}
}

func TestUserStreamManagerRenewsAfterKeepAliveFailureAndStopsOnce(t *testing.T) {
	var mu sync.Mutex
	postCount := 0
	putCount := 0
	deleteKeys := make([]string, 0, 1)
	client := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		switch request.Method {
		case http.MethodPost:
			postCount++
			key := "fixture-listen-key-1"
			if postCount > 1 {
				key = "fixture-listen-key-2"
			}
			return fixtureHTTPResponse(request, http.StatusOK, []byte(`{"listenKey":"`+key+`"}`)), nil
		case http.MethodPut:
			putCount++
			if putCount == 1 {
				return fixtureHTTPResponse(request, http.StatusBadRequest, []byte(`{"code":-1125,"msg":"Invalid listen key."}`)), nil
			}
			return fixtureHTTPResponse(request, http.StatusOK, []byte(`{}`)), nil
		case http.MethodDelete:
			deleteKeys = append(deleteKeys, request.URL.Query().Get("listenKey"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Request:    request,
			}, nil
		default:
			t.Fatalf("unexpected request method %s", request.Method)
			return nil, nil
		}
	})

	manager := NewUserStreamManager(client)
	manager.KeepAliveInt = 10 * time.Millisecond
	renewed := make(chan string, 1)
	manager.SetRenewHandler(func(key string) { renewed <- key })

	key, err := manager.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if key != "fixture-listen-key-1" {
		t.Fatalf("initial listen key = %q", key)
	}
	select {
	case key = <-renewed:
		if key != "fixture-listen-key-2" || manager.ListenKey() != key {
			t.Fatalf("renewed key = %q manager=%q", key, manager.ListenKey())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listen key was not renewed")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if postCount != 2 || putCount < 1 {
		t.Fatalf("POST=%d PUT=%d", postCount, putCount)
	}
	if len(deleteKeys) != 1 || deleteKeys[0] != "fixture-listen-key-2" {
		t.Fatalf("closed listen keys = %v", deleteKeys)
	}
}

func TestWsAccountClientReconnectsWithRenewedListenKey(t *testing.T) {
	var wsConnections atomic.Int64
	paths := make(chan string, 2)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	wsServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer conn.Close()
		connection := wsConnections.Add(1)
		paths <- request.URL.Path
		var payload map[string]any
		if err := json.Unmarshal(readSpotFixture(t, "account_update.json"), &payload); err != nil {
			t.Errorf("decode account fixture: %v", err)
			return
		}
		payload["E"] = float64(1783641600200 + connection)
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Errorf("encode account fixture: %v", err)
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, encoded); err != nil {
			t.Errorf("write account fixture: %v", err)
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer wsServer.Close()

	var restMu sync.Mutex
	postCount := 0
	putCount := 0
	profile, _ := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	security := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		restMu.Lock()
		defer restMu.Unlock()
		switch request.Method {
		case http.MethodPost:
			postCount++
			return fixtureHTTPResponse(request, http.StatusOK, []byte(`{"listenKey":"key-`+fmt.Sprint(postCount)+`"}`)), nil
		case http.MethodPut:
			putCount++
			if putCount == 1 {
				return fixtureHTTPResponse(request, http.StatusBadRequest, []byte(`{"code":-1125,"msg":"Invalid listen key."}`)), nil
			}
			return fixtureHTTPResponse(request, http.StatusOK, []byte(`{}`)), nil
		case http.MethodDelete:
			return fixtureHTTPResponse(request, http.StatusOK, []byte(`{}`)), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", request.Method)
		}
	})
	client, err := NewWsAccountClient(context.Background(), profile, security.security)
	if err != nil {
		t.Fatal(err)
	}
	client.restClient.WithHTTPClient(security.HTTPClient)
	client.StreamMgr.Client = client.restClient
	client.StreamMgr.KeepAliveInt = 25 * time.Millisecond
	client.ReconnectWait = 10 * time.Millisecond
	client.userStreamURL = func(key string) string {
		return websocketURL(wsServer.URL) + "/" + key
	}

	received := make(chan int64, 2)
	client.SubscribeAccountPosition(func(event *AccountPositionEvent) { received <- event.EventTime })
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	for index, expected := range []int64{1783641600201, 1783641600202} {
		select {
		case got := <-received:
			if got != expected {
				t.Fatalf("event %d time = %d, want %d", index, got, expected)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for private event %d", index)
		}
	}
	first := <-paths
	second := <-paths
	if first != "/ws/key-1" || second != "/ws/key-2" {
		t.Fatalf("user stream paths = %q, %q", first, second)
	}
}
