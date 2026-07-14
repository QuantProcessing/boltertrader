package grvt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestLoginErrorRedactsResponseAuthenticationMaterial(t *testing.T) {
	const apiKey = "grvt-login-api-key-secret"
	const responseToken = "grvt-login-response-token-secret"

	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/auth/api_key/login" {
					t.Fatalf("unexpected login path: %s", r.URL.Path)
				}
				w.WriteHeader(status)
				_, _ = fmt.Fprintf(w, `{"api_key":%q,"token":%q}`, apiKey, responseToken)
			}))
			defer server.Close()

			client := NewClient().WithCredentials(apiKey, "subaccount", "private-key")
			client.EdgeURL = server.URL
			client.HttpClient = server.Client()

			err := client.Login(context.Background())
			if err == nil {
				t.Fatal("Login unexpectedly succeeded")
			}
			assertGRVTSecretsAbsent(t, err.Error(), apiKey, responseToken, `"api_key"`, `"token"`)
			if !strings.Contains(err.Error(), http.StatusText(status)) {
				t.Fatalf("Login error lost HTTP status: %v", err)
			}

			if status == http.StatusTooManyRequests {
				var exchangeErr *sdkcore.ExchangeError
				if !errors.As(err, &exchangeErr) {
					t.Fatalf("Login rate limit lost ExchangeError type: %T %v", err, err)
				}
				if exchangeErr.Code != "429" {
					t.Fatalf("Login rate-limit code = %q, want 429", exchangeErr.Code)
				}
				if !errors.Is(err, sdkcore.ErrRateLimited) {
					t.Fatalf("Login rate limit lost sentinel: %v", err)
				}
			}
		})
	}
}

func TestLoginErrorRedactsUntrustedHTTPStatusText(t *testing.T) {
	const statusSecret = "grvt-untrusted-status-secret"

	client := NewClient().WithCredentials("api-key", "subaccount", "private-key")
	client.HttpClient = &http.Client{Transport: grvtRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized " + statusSecret,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})}

	err := client.Login(context.Background())
	if err == nil {
		t.Fatal("Login unexpectedly succeeded")
	}
	assertGRVTSecretsAbsent(t, err.Error(), statusSecret)
	if !strings.Contains(err.Error(), http.StatusText(http.StatusUnauthorized)) {
		t.Fatalf("Login error lost fixed HTTP status: %v", err)
	}
}

func TestLoginRequestConstructionErrorRedactsPrivateURLAndPreservesClassification(t *testing.T) {
	const urlSecret = "grvt-invalid-login-url-secret"

	for _, edgeURL := range []string{
		"://example.invalid?auth_token=" + urlSecret,
		"https://example.invalid/%zz?auth_token=" + urlSecret,
	} {
		t.Run(edgeURL[:strings.Index(edgeURL, "?")], func(t *testing.T) {
			client := NewClient().WithCredentials("api-key", "subaccount", "private-key")
			client.EdgeURL = edgeURL

			err := client.Login(context.Background())
			if err == nil {
				t.Fatal("Login with invalid URL unexpectedly succeeded")
			}
			assertGRVTSecretsAbsent(t, err.Error(), urlSecret, "auth_token")
			var urlErr *url.Error
			if !errors.As(err, &urlErr) {
				t.Fatalf("login request construction error lost url.Error classification: %T %v", err, err)
			}
		})
	}
}

func TestSignedPostErrorsRedactPrivateResponsesAndPreserveClassification(t *testing.T) {
	const requestSecret = "grvt-signed-request-secret"
	const responseSecret = "grvt-signed-response-token-secret"

	tests := []struct {
		name           string
		status         int
		body           string
		wantCode       string
		wantRateLimit  bool
		wantGRVTCode   int
		wantGRVTStatus int
	}{
		{
			name:   "generic bad request",
			status: http.StatusBadRequest,
			body:   fmt.Sprintf(`{"token":%q}`, responseSecret),
		},
		{
			name:          "generic rate limit",
			status:        http.StatusTooManyRequests,
			body:          fmt.Sprintf(`{"token":%q}`, responseSecret),
			wantCode:      "429",
			wantRateLimit: true,
		},
		{
			name:           "structured rejection",
			status:         http.StatusBadRequest,
			body:           fmt.Sprintf(`{"code":3210,"message":%q,"status":400}`, responseSecret),
			wantGRVTCode:   3210,
			wantGRVTStatus: 400,
		},
		{
			name:          "structured rate limit",
			status:        http.StatusBadRequest,
			body:          fmt.Sprintf(`{"code":1006,"message":%q,"status":400}`, responseSecret),
			wantCode:      "1006",
			wantRateLimit: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			client := NewClient()
			client.HttpClient = server.Client()
			client.cookie = &http.Cookie{Name: "gravity", Value: "session-cookie"}

			_, err := client.Post(context.Background(), server.URL, map[string]string{
				"signature": requestSecret,
			}, true)
			if err == nil {
				t.Fatal("signed Post unexpectedly succeeded")
			}
			assertGRVTSecretsAbsent(t, err.Error(), requestSecret, responseSecret, `"token"`)

			if test.wantRateLimit {
				var exchangeErr *sdkcore.ExchangeError
				if !errors.As(err, &exchangeErr) {
					t.Fatalf("rate limit lost ExchangeError type: %T %v", err, err)
				}
				if exchangeErr.Code != test.wantCode {
					t.Fatalf("rate-limit code = %q, want %q", exchangeErr.Code, test.wantCode)
				}
				if !errors.Is(err, sdkcore.ErrRateLimited) {
					t.Fatalf("rate limit lost sentinel: %v", err)
				}
			}

			if test.wantGRVTCode != 0 {
				var grvtErr *GrvtError
				if !errors.As(err, &grvtErr) {
					t.Fatalf("structured rejection lost GrvtError type: %T %v", err, err)
				}
				if grvtErr.Code != test.wantGRVTCode || grvtErr.Status != test.wantGRVTStatus {
					t.Fatalf("GrvtError = code %d status %d, want code %d status %d", grvtErr.Code, grvtErr.Status, test.wantGRVTCode, test.wantGRVTStatus)
				}
			}
		})
	}
}

func TestSignedPostTransportErrorRedactsPrivateURLAndPreservesCause(t *testing.T) {
	const urlSecret = "grvt-private-url-token-secret"
	sentinel := errors.New("transport unavailable")

	client := NewClient()
	client.cookie = &http.Cookie{Name: "gravity", Value: "session-cookie"}
	client.HttpClient = &http.Client{Transport: grvtRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("wrapped transport failure: %w", &url.Error{
			Op:  req.Method,
			URL: req.URL.String(),
			Err: sentinel,
		})
	})}

	_, err := client.Post(
		context.Background(),
		"https://example.invalid/private?auth_token="+urlSecret,
		map[string]string{"order": "payload"},
		true,
	)
	if err == nil {
		t.Fatal("signed Post unexpectedly succeeded")
	}
	assertGRVTSecretsAbsent(t, err.Error(), urlSecret, "auth_token")
	if !errors.Is(err, sentinel) {
		t.Fatalf("signed Post transport error lost cause: %v", err)
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("signed Post transport error lost url.Error classification: %T %v", err, err)
	}
}

func TestSignedPostRequestConstructionErrorRedactsPrivateURL(t *testing.T) {
	const urlSecret = "grvt-invalid-private-url-token-secret"

	for _, rawURL := range []string{
		"://example.invalid/private?auth_token=" + urlSecret,
		"https://example.invalid/%zz?auth_token=" + urlSecret,
	} {
		t.Run(rawURL[:strings.Index(rawURL, "?")], func(t *testing.T) {
			client := NewClient()
			client.cookie = &http.Cookie{Name: "gravity", Value: "session-cookie"}
			_, err := client.Post(
				context.Background(),
				rawURL,
				map[string]string{"order": "payload"},
				true,
			)
			if err == nil {
				t.Fatal("signed Post with invalid URL unexpectedly succeeded")
			}
			assertGRVTSecretsAbsent(t, err.Error(), urlSecret, "auth_token")
			var urlErr *url.Error
			if !errors.As(err, &urlErr) {
				t.Fatalf("request construction error lost url.Error classification: %T %v", err, err)
			}
		})
	}
}

func TestSignedPostTransportErrorRedactsLeafURLError(t *testing.T) {
	const urlSecret = "grvt-leaf-url-error-secret"

	client := NewClient()
	client.cookie = &http.Cookie{Name: "gravity", Value: "session-cookie"}
	client.HttpClient = &http.Client{Transport: grvtRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{
			Op:  http.MethodPost,
			URL: "https://example.invalid/private?auth_token=" + urlSecret,
		}
	})}

	_, err := client.Post(
		context.Background(),
		"https://example.invalid/private",
		map[string]string{"order": "payload"},
		true,
	)
	if err == nil {
		t.Fatal("signed Post unexpectedly succeeded")
	}
	assertGRVTSecretsAbsent(t, err.Error(), urlSecret, "auth_token")
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("leaf url.Error classification was lost: %T %v", err, err)
	}
}

func TestWebsocketHandshakeFailureRedactsHeadersBodyAndCredentials(t *testing.T) {
	const apiKey = "grvt-handshake-api-key-secret"
	const cookieSecret = "grvt-handshake-cookie-secret"
	const responseToken = "grvt-handshake-response-token-secret"
	const urlTokenSecret = "grvt-handshake-url-token-secret"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/api_key/login":
			http.SetCookie(w, &http.Cookie{Name: "gravity", Value: cookieSecret})
			w.Header().Set("X-Grvt-Account-Id", "account-id")
			w.WriteHeader(http.StatusOK)
		case "/ws":
			w.Header().Add("Set-Cookie", "gravity="+cookieSecret)
			w.Header().Set("X-Auth-Token", responseToken)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"api_key":%q,"token":%q}`, apiKey, responseToken)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	core, observed := observer.New(zap.DebugLevel)
	client := NewClient().WithCredentials(apiKey, "subaccount", "private-key")
	client.EdgeURL = server.URL
	client.HttpClient = server.Client()
	wsClient := NewAccountWebsocketClient(context.Background(), client)
	wsClient.URL = "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?auth_token=" + urlTokenSecret
	wsClient.Logger = zap.New(core).Sugar()

	err := wsClient.Connect()
	if err == nil {
		t.Fatal("Connect unexpectedly succeeded")
	}
	if !errors.Is(err, websocket.ErrBadHandshake) {
		t.Fatalf("Connect lost websocket handshake cause: %v", err)
	}
	if !strings.Contains(err.Error(), http.StatusText(http.StatusUnauthorized)) {
		t.Fatalf("Connect error lost HTTP status: %v", err)
	}

	logged := grvtObservedText(observed.All())
	assertGRVTSecretsAbsent(t, err.Error()+"\n"+logged,
		apiKey,
		cookieSecret,
		responseToken,
		urlTokenSecret,
		`"api_key"`,
		`"token"`,
		"Set-Cookie",
		"X-Auth-Token",
	)
	for _, metadata := range []string{"status", "response_bytes"} {
		if !strings.Contains(logged, metadata) {
			t.Fatalf("handshake log omitted safe metadata %q: %s", metadata, logged)
		}
	}
}

func TestWebsocketOutboundLogsRedactPrivateSelectorsAndRPCParams(t *testing.T) {
	const selectorSecret = "grvt-private-account-selector-secret"
	const signatureSecret = "grvt-replayable-order-signature-secret"
	const rpcAccountSecret = "grvt-private-rpc-account-secret"

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for requestNumber := 0; requestNumber < 2; requestNumber++ {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var request struct {
				Method string `json:"m"`
				ID     uint32 `json:"i"`
			}
			if err := json.Unmarshal(payload, &request); err != nil {
				return
			}
			if request.Method == "v1/create_order" {
				_ = conn.WriteJSON(WsRpcResponse{JsonRpc: "2.0", Id: request.ID, Result: json.RawMessage(`{}`)})
			}
		}
	}))
	defer server.Close()

	core, observed := observer.New(zap.DebugLevel)
	wsClient := NewMarketWebsocketClient(context.Background(), NewClient())
	wsClient.URL = "ws" + strings.TrimPrefix(server.URL, "http")
	wsClient.Logger = zap.New(core).Sugar()
	if err := wsClient.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer wsClient.Close()

	if err := wsClient.Subscribe("v1.private", selectorSecret, func([]byte) error { return nil }); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := wsClient.SendRPC("v1/create_order", map[string]any{
		"signature": signatureSecret,
		"account":   rpcAccountSecret,
	}); err != nil {
		t.Fatalf("SendRPC: %v", err)
	}

	logged := grvtObservedText(observed.All())
	assertGRVTSecretsAbsent(t, logged, selectorSecret, signatureSecret, rpcAccountSecret, `"signature"`, `"account"`)
	for _, metadata := range []string{"method", "id"} {
		if !strings.Contains(logged, metadata) {
			t.Fatalf("outbound WS log omitted safe metadata %q: %s", metadata, logged)
		}
	}
}

func TestWebsocketInboundLogsAndRPCErrorsRedactPrivatePayloads(t *testing.T) {
	const selectorSecret = "grvt-inbound-account-selector-secret"
	const feedSecret = "grvt-private-feed-token-secret"
	const rpcMessageSecret = "grvt-private-rpc-message-secret"
	const rpcDataSecret = "grvt-private-rpc-data-secret"
	const callbackErrorSecret = "grvt-private-callback-error-secret"

	core, observed := observer.New(zap.DebugLevel)
	wsClient := NewAccountRpcWebsocketClient(context.Background(), NewClient())
	wsClient.Logger = zap.New(core).Sugar()
	wsClient.subs["v1.fill.callback-selector"] = func([]byte) error {
		return errors.New(callbackErrorSecret)
	}

	wsClient.handleMessage([]byte(fmt.Sprintf(
		`{"s":"v1.order","s1":%q,"f":{"token":%q}}`, selectorSecret, feedSecret,
	)))
	wsClient.handleRPCResponse([]byte(fmt.Sprintf(
		`{"j":"2.0","i":71,"r":{"token":%q},"e":{"c":4001,"m":%q,"d":%q}}`,
		feedSecret, rpcMessageSecret, rpcDataSecret,
	)))
	wsClient.handleMessage([]byte(fmt.Sprintf(
		`{"j":"2.0","m":"subscribe","i":72,"r":{"token":%q}}`, feedSecret,
	)))
	wsClient.handleMessage([]byte(`{"s":"v1.fill","s1":"callback-selector","f":{}}`))

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(grvtObservedText(observed.All()), "callback failed") {
			break
		}
		time.Sleep(time.Millisecond)
	}

	logged := grvtObservedText(observed.All())
	assertGRVTSecretsAbsent(t, logged,
		selectorSecret,
		feedSecret,
		rpcMessageSecret,
		rpcDataSecret,
		callbackErrorSecret,
		`"token"`,
	)
	for _, metadata := range []string{"bytes", "id"} {
		if !strings.Contains(logged, metadata) {
			t.Fatalf("inbound WS log omitted safe metadata %q: %s", metadata, logged)
		}
	}

}

func TestWebsocketReturnedRPCErrorRedactsServerPayloadButPreservesCode(t *testing.T) {
	const requestSecret = "grvt-private-order-id-secret"
	const rpcMessageSecret = "grvt-returned-rpc-message-secret"
	const rpcDataSecret = "grvt-returned-rpc-data-secret"
	const retryableMessage = "signature does not match payload"

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var request struct {
			ID uint32 `json:"i"`
		}
		if err := json.Unmarshal(payload, &request); err != nil {
			return
		}
		_ = conn.WriteJSON(WsRpcResponse{
			JsonRpc: "2.0",
			Id:      request.ID,
			Error: &WsRpcError{
				Code:    4001,
				Message: retryableMessage + ": " + rpcMessageSecret,
				Data:    rpcDataSecret,
			},
		})
	}))
	defer server.Close()

	core, observed := observer.New(zap.DebugLevel)
	wsClient := NewMarketWebsocketClient(context.Background(), NewClient())
	wsClient.URL = "ws" + strings.TrimPrefix(server.URL, "http")
	wsClient.Logger = zap.New(core).Sugar()
	if err := wsClient.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer wsClient.Close()
	wsClient.auth = true

	err := func() error {
		_, err := wsClient.CancelOrder(context.Background(), &CancelOrderRequest{OrderID: stringPtr(requestSecret)})
		return err
	}()
	if err == nil {
		t.Fatal("CancelOrder unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "4001") {
		t.Fatalf("returned RPC error lost code: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), retryableMessage) {
		t.Fatalf("returned RPC error lost retryable classification: %v", err)
	}
	assertGRVTSecretsAbsent(t, err.Error(), rpcMessageSecret, rpcDataSecret)
	assertGRVTSecretsAbsent(t, grvtObservedText(observed.All()), requestSecret, rpcMessageSecret, rpcDataSecret)
}

func TestWebsocketReadErrorLogRedactsCloseReason(t *testing.T) {
	const closeReasonSecret = "grvt-private-close-reason-secret"

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, closeReasonSecret),
			time.Now().Add(time.Second),
		)
	}))
	defer server.Close()

	core, observed := observer.New(zap.DebugLevel)
	wsClient := NewMarketWebsocketClient(context.Background(), NewClient())
	wsClient.URL = "ws" + strings.TrimPrefix(server.URL, "http")
	wsClient.Logger = zap.New(core).Sugar()
	if err := wsClient.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer wsClient.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(grvtObservedText(observed.All()), "websocket unexpected close error") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	logged := grvtObservedText(observed.All())
	if !strings.Contains(logged, "websocket unexpected close error") {
		t.Fatalf("timed out waiting for websocket close log: %s", logged)
	}
	assertGRVTSecretsAbsent(t, logged, closeReasonSecret)
	if !strings.Contains(logged, "close_code") {
		t.Fatalf("websocket close log omitted safe close code: %s", logged)
	}
}

func assertGRVTSecretsAbsent(t *testing.T, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Fatalf("authentication/private material %q leaked: %s", secret, text)
		}
	}
}

func grvtObservedText(entries []observer.LoggedEntry) string {
	var logged strings.Builder
	for _, entry := range entries {
		contextJSON, err := json.Marshal(entry.ContextMap())
		if err != nil {
			_, _ = fmt.Fprintf(&logged, "%s %v\n", entry.Message, entry.ContextMap())
			continue
		}
		_, _ = fmt.Fprintf(&logged, "%s %s\n", entry.Message, contextJSON)
	}
	return logged.String()
}

func stringPtr(value string) *string {
	return &value
}

type grvtRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn grvtRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
