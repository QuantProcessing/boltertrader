package perp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSAccountCompanion_NewWsAccountClient(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	if client.Client == nil || client.WsClient == nil || client.BaseURL != WSPrivateBaseURL {
		t.Fatalf("unexpected account client: %+v", client)
	}
}

func TestWsAccountClientUnexpectedDisconnectRenewsListenKeyBeforeRecovery(t *testing.T) {
	var listenKeys atomic.Int64
	restServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fapi/v1/listenKey" {
			t.Errorf("unexpected REST request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		key := listenKeys.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"listenKey":"key-%d"}`, key)
	}))
	defer restServer.Close()

	var connections atomic.Int64
	paths := make(chan string, 3)
	releaseFirst := make(chan struct{})
	wsURL := newPerpWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		defer conn.Close()
		connection := connections.Add(1)
		paths <- r.URL.Path
		if connection == 1 {
			<-releaseFirst
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	profile := EndpointProfile{
		RESTBaseURL:      restServer.URL,
		EndpointPrefix:   "/fapi",
		AccountVersion:   "v2",
		WSPrivateBaseURL: wsURL + "/ws",
	}
	client := NewWsAccountClientWithEndpointProfile(ctx, "api-key", "secret", profile)
	client.Client.WithRateLimiter(nil)
	client.KeepAliveInt = time.Hour
	client.WsClient.ReconnectWait = 10 * time.Millisecond
	reconnectPhases := make(chan string, 2)
	client.SetReconnectHooks(func(error) {
		reconnectPhases <- "started"
	}, func() {
		reconnectPhases <- "recovered"
	})
	defer client.Close()

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	select {
	case path := <-paths:
		if path != "/ws/key-1" {
			t.Fatalf("initial private stream path = %q", path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial private stream connection")
	}
	select {
	case phase := <-reconnectPhases:
		t.Fatalf("initial connect emitted reconnect phase %q", phase)
	default:
	}

	close(releaseFirst)
	select {
	case phase := <-reconnectPhases:
		if phase != "started" {
			t.Fatalf("first reconnect phase = %q, want started", phase)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect start")
	}

	waitPerpAccountFreshPath(t, paths, "/ws/key-2", 2*time.Second)
	select {
	case phase := <-reconnectPhases:
		if phase != "recovered" {
			t.Fatalf("second reconnect phase = %q, want recovered", phase)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect recovery")
	}
	if got := listenKeys.Load(); got != 2 {
		t.Fatalf("listen-key creates = %d, want 2", got)
	}
}

func TestWsAccountClientRecoveryRetriesFreshListenKeyUntilSocketIsLive(t *testing.T) {
	var listenKeyRequests atomic.Int64
	restServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fapi/v1/listenKey" {
			t.Errorf("unexpected REST request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		request := listenKeyRequests.Add(1)
		if request == 2 {
			http.Error(w, "temporary listen-key failure", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"listenKey":"key-%d"}`, request)
	}))
	defer restServer.Close()

	var connections atomic.Int64
	paths := make(chan string, 3)
	releaseFirst := make(chan struct{})
	wsURL := newPerpWSServer(t, func(conn *websocket.Conn, r *http.Request) {
		defer conn.Close()
		connection := connections.Add(1)
		paths <- r.URL.Path
		if connection == 1 {
			<-releaseFirst
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	profile := EndpointProfile{
		RESTBaseURL:      restServer.URL,
		EndpointPrefix:   "/fapi",
		AccountVersion:   "v2",
		WSPrivateBaseURL: wsURL + "/ws",
	}
	client := NewWsAccountClientWithEndpointProfile(ctx, "api-key", "secret", profile)
	client.Client.WithRateLimiter(nil)
	client.KeepAliveInt = time.Hour
	client.WsClient.ReconnectWait = 10 * time.Millisecond
	reconnectPhases := make(chan string, 2)
	client.SetReconnectHooks(func(error) {
		reconnectPhases <- "started"
	}, func() {
		reconnectPhases <- "recovered"
	})
	defer client.Close()

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitPerpAccountPath(t, paths, "/ws/key-1", time.Second)
	close(releaseFirst)
	waitPerpAccountPhase(t, reconnectPhases, "started", time.Second)

	// Account recovery starts immediately and keeps trying fresh listen keys
	// after the first REST failure; a successful stale-key reconnect is not a
	// prerequisite for reaching key-3.
	waitPerpAccountFreshPath(t, paths, "/ws/key-3", 3*time.Second)
	waitPerpAccountPhase(t, reconnectPhases, "recovered", time.Second)
	if got := listenKeyRequests.Load(); got != 3 {
		t.Fatalf("listen-key creates = %d, want 3", got)
	}
}

func waitPerpAccountPath(t *testing.T, paths <-chan string, want string, timeout time.Duration) {
	t.Helper()
	select {
	case got := <-paths:
		if got != want {
			t.Fatalf("private stream path = %q, want %q", got, want)
		}
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for private stream path %q", want)
	}
}

func waitPerpAccountFreshPath(t *testing.T, paths <-chan string, want string, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case got := <-paths:
			if got == want {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for fresh private stream path %q", want)
		}
	}
}

func waitPerpAccountPhase(t *testing.T, phases <-chan string, want string, timeout time.Duration) {
	t.Helper()
	select {
	case got := <-phases:
		if got != want {
			t.Fatalf("reconnect phase = %q, want %q", got, want)
		}
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for reconnect phase %q", want)
	}
}

func TestWsAccountClientRecoveryStopsWhenAccountLifecycleEnds(t *testing.T) {
	tests := []struct {
		name   string
		cancel func(context.CancelFunc, *WsAccountClient)
	}{
		{
			name: "Close",
			cancel: func(_ context.CancelFunc, client *WsAccountClient) {
				client.Close()
			},
		},
		{
			name: "parent context",
			cancel: func(cancel context.CancelFunc, _ *WsAccountClient) {
				cancel()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var listenKeyRequests atomic.Int64
			recoveryRequestStarted := make(chan struct{}, 1)
			recoveryRequestCanceled := make(chan struct{}, 1)
			releaseBlockedRequest := make(chan struct{})
			defer close(releaseBlockedRequest)

			httpClient := &http.Client{Transport: wsAccountRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				request := listenKeyRequests.Add(1)
				if request == 1 {
					return wsAccountJSONResponse(r, http.StatusOK, `{"listenKey":"key-1"}`), nil
				}
				select {
				case recoveryRequestStarted <- struct{}{}:
				default:
				}
				select {
				case <-r.Context().Done():
					select {
					case recoveryRequestCanceled <- struct{}{}:
					default:
					}
					return nil, r.Context().Err()
				case <-releaseBlockedRequest:
					return wsAccountJSONResponse(r, http.StatusServiceUnavailable, `{"error":"released"}`), nil
				}
			})}

			var connections atomic.Int64
			paths := make(chan string, 2)
			releaseFirst := make(chan struct{})
			wsURL := newPerpWSServer(t, func(conn *websocket.Conn, r *http.Request) {
				defer conn.Close()
				connection := connections.Add(1)
				paths <- r.URL.Path
				if connection == 1 {
					<-releaseFirst
					return
				}
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			})

			parentCtx, parentCancel := context.WithCancel(context.Background())
			defer parentCancel()
			profile := EndpointProfile{
				RESTBaseURL:      "https://rest.invalid",
				EndpointPrefix:   "/fapi",
				AccountVersion:   "v2",
				WSPrivateBaseURL: wsURL + "/ws",
			}
			client := NewWsAccountClientWithEndpointProfile(parentCtx, "api-key", "secret", profile)
			client.Client.WithHTTPClient(httpClient).WithRateLimiter(nil)
			client.KeepAliveInt = time.Hour
			client.WsClient.ReconnectWait = 10 * time.Millisecond
			var recovered atomic.Int64
			started := make(chan struct{}, 1)
			client.SetReconnectHooks(func(error) {
				started <- struct{}{}
			}, func() {
				recovered.Add(1)
			})
			defer client.Close()

			if err := client.Connect(); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			waitPerpAccountPath(t, paths, "/ws/key-1", time.Second)
			close(releaseFirst)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for reconnect start")
			}
			select {
			case <-recoveryRequestStarted:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for blocked fresh listen-key request")
			}

			test.cancel(parentCancel, client)
			select {
			case <-recoveryRequestCanceled:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("account lifecycle end did not cancel the in-flight recovery request")
			}
			time.Sleep(30 * time.Millisecond)
			if got := listenKeyRequests.Load(); got != 2 {
				t.Fatalf("listen-key creates after lifecycle end = %d, want 2", got)
			}
			if got := recovered.Load(); got != 0 {
				t.Fatalf("recovered callbacks after lifecycle end = %d, want 0", got)
			}
		})
	}
}

func TestWsAccountClientCloseSerializesRecoveryClientReplacement(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	lifecycle := client.currentLifecycle()
	original := client.WsClient
	replaceStarted := make(chan struct{})
	allowReplace := make(chan struct{})
	client.beforeRecoveryClientReplace = func() {
		close(replaceStarted)
		<-allowReplace
	}

	recoveryDone := make(chan struct{})
	go func() {
		client.resubscribe()
		close(recoveryDone)
	}()
	select {
	case <-replaceStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovery client replacement")
	}

	closeDone := make(chan struct{})
	go func() {
		client.Close()
		close(closeDone)
	}()
	select {
	case <-lifecycle.ctx.Done():
	case <-time.After(time.Second):
		close(allowReplace)
		t.Fatal("Close did not cancel the account lifecycle before waiting")
	}

	returnedBeforeReplacementFinished := false
	select {
	case <-closeDone:
		returnedBeforeReplacementFinished = true
	case <-time.After(50 * time.Millisecond):
	}
	close(allowReplace)
	select {
	case <-recoveryDone:
	case <-time.After(time.Second):
		t.Fatal("recovery did not stop after Close")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after recovery stopped")
	}

	if returnedBeforeReplacementFinished {
		t.Error("Close returned while recovery could still replace the account WebSocket client")
	}
	if client.WsClient != original {
		t.Error("recovery published a replacement WebSocket client after Close")
	}
	original.Mu.RLock()
	originalClosed := original.isClosed
	original.Mu.RUnlock()
	if !originalClosed {
		t.Error("Close did not close the exact account WebSocket client")
	}
}

type wsAccountRoundTripFunc func(*http.Request) (*http.Response, error)

func (f wsAccountRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func wsAccountJSONResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func TestWSAccountCompanion_NewDemoWsAccountClientUsesDemoRESTAndWS(t *testing.T) {
	client := NewDemoWsAccountClient(context.Background(), "demo-key", "demo-secret")
	if client.Client == nil || client.WsClient == nil {
		t.Fatalf("unexpected nil client: %+v", client)
	}
	if client.Client.BaseURL != DemoBaseURL {
		t.Fatalf("expected Demo REST base URL %s, got %s", DemoBaseURL, client.Client.BaseURL)
	}
	if client.Client.APIKey != "demo-key" || client.Client.SecretKey != "demo-secret" {
		t.Fatalf("unexpected Demo credentials: key=%q secret=%q", client.Client.APIKey, client.Client.SecretKey)
	}
	if client.BaseURL != DemoWSPrivateBaseURL || client.WsClient.URL != DemoWSPrivateBaseURL {
		t.Fatalf("expected Demo private stream URL %s, got base=%s ws=%s", DemoWSPrivateBaseURL, client.BaseURL, client.WsClient.URL)
	}
}

func TestWSAccountCompanion_WithEndpointProfileUsesRESTAndPrivateWS(t *testing.T) {
	profile := EndpointProfile{
		RESTBaseURL:      "https://profile.test/rest",
		EndpointPrefix:   "/fapi",
		AccountVersion:   "v2",
		WSPrivateBaseURL: "wss://profile.test/private",
	}
	client := NewWsAccountClientWithEndpointProfile(context.Background(), "profile-key", "profile-secret", profile)
	if client.Client == nil || client.WsClient == nil {
		t.Fatalf("unexpected nil client: %+v", client)
	}
	if client.Client.BaseURL != profile.RESTBaseURL {
		t.Fatalf("expected profile REST base URL %s, got %s", profile.RESTBaseURL, client.Client.BaseURL)
	}
	if client.Client.APIKey != "profile-key" || client.Client.SecretKey != "profile-secret" {
		t.Fatalf("unexpected profile credentials: key=%q secret=%q", client.Client.APIKey, client.Client.SecretKey)
	}
	if client.BaseURL != profile.WSPrivateBaseURL || client.WsClient.URL != profile.WSPrivateBaseURL {
		t.Fatalf("expected profile private stream URL %s, got base=%s ws=%s", profile.WSPrivateBaseURL, client.BaseURL, client.WsClient.URL)
	}
}

func TestWSAccountCompanion_NewCoinMWsAccountClientUsesDstreamAndDAPI(t *testing.T) {
	client := NewCoinMWsAccountClient(context.Background(), "api-key", "secret")
	if client.Client == nil || client.WsClient == nil {
		t.Fatalf("unexpected nil client: %+v", client)
	}
	if client.Client.BaseURL != CoinMBaseURL {
		t.Fatalf("expected COIN-M REST base URL %s, got %s", CoinMBaseURL, client.Client.BaseURL)
	}
	if client.Client.EndpointPrefix != "/dapi" {
		t.Fatalf("expected COIN-M endpoint prefix /dapi, got %s", client.Client.EndpointPrefix)
	}
	if client.Client.AccountVersion != "v1" {
		t.Fatalf("expected COIN-M account version v1, got %s", client.Client.AccountVersion)
	}
	if client.BaseURL != CoinMWSPrivateBaseURL || client.WsClient.URL != CoinMWSPrivateBaseURL {
		t.Fatalf("expected COIN-M private stream base URL %s, got base=%s ws=%s", CoinMWSPrivateBaseURL, client.BaseURL, client.WsClient.URL)
	}
}

func TestWSAccountCompanion_WithURLSetsBaseURL(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	client.WithURL("wss://example.test/private")
	if client.BaseURL != "wss://example.test/private" {
		t.Fatalf("unexpected base url: %s", client.BaseURL)
	}
}

func TestWSAccountCompanion_SetOnResubscribe(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	called := false
	client.SetOnResubscribe(func() {
		called = true
	})
	client.onResubscribe()
	if !called {
		t.Fatal("expected on resubscribe hook to be stored")
	}
}

func TestWsAccountClient_SubscribeAlgoUpdate(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	var got *AlgoUpdateEvent
	client.SubscribeAlgoUpdate(func(event *AlgoUpdateEvent) {
		got = event
	})

	payload := []byte(`{
		"e":"ALGO_UPDATE",
		"E":1700000000001,
		"T":1700000000002,
		"o":{
			"caid":"algo-client",
			"aid":9001,
			"at":"CONDITIONAL",
			"o":"STOP_MARKET",
			"s":"BTCUSDT",
			"S":"SELL",
			"ps":"SHORT",
			"f":"GTC",
			"q":"0.2",
			"X":"TRIGGERED",
			"tp":"190",
			"p":"0",
			"wt":"CONTRACT_PRICE",
			"pm":"NONE",
			"cp":false,
			"pP":true,
			"R":true,
			"tt":1700000000003,
			"gtd":1700003600000,
			"ai":"77",
			"ap":"191",
			"aq":"0.2",
			"act":"MARKET",
			"cr":"1.2",
			"V":"NONE"
		}
	}`)
	if err := client.handleAlgoUpdate(payload); err != nil {
		t.Fatalf("handleAlgoUpdate: %v", err)
	}
	if got == nil {
		t.Fatal("expected algo update callback")
	}
	if got.EventType != "ALGO_UPDATE" || got.Order.ClientAlgoID != "algo-client" || got.Order.AlgoID != 9001 {
		t.Fatalf("unexpected algo update: %+v", got)
	}
	if got.Order.ActualOrderID != "77" || got.Order.AlgoStatus != "TRIGGERED" || got.Order.PositionSide != "SHORT" {
		t.Fatalf("unexpected algo order payload: %+v", got.Order)
	}
}

func TestWSAccountCompanion_ResetWSClientInstallsReconnectRecovery(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "secret")
	if client.WsClient.postReconnect == nil {
		t.Fatal("expected account websocket to install reconnect recovery hook")
	}

	client.resetWSClient()
	if client.WsClient.postReconnect == nil {
		t.Fatal("expected reset websocket to keep reconnect recovery hook")
	}
}
