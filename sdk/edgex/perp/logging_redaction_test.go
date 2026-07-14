package perp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRESTDebugLogRedactsQueryBodyAndPrivateResponse(t *testing.T) {
	const querySecret = "edgex-private-account-query-secret"
	const requestSignature = "edgex-l2-replayable-signature-secret"
	const responseToken = "edgex-private-response-token-secret"

	core, observed := observer.New(zap.DebugLevel)
	client := NewClient()
	client.BaseURL = "https://example.invalid"
	client.Logger = zap.New(core).Sugar()
	client.HTTPClient = &http.Client{Transport: edgexRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"code":"0","data":{"token":%q}}`, responseToken))),
		}, nil
	})}

	if err := client.call(context.Background(), http.MethodGet, "/api/v1/private/account", map[string]interface{}{
		"accountId": querySecret,
	}, false, nil); err != nil {
		t.Fatalf("GET call: %v", err)
	}
	if err := client.call(context.Background(), http.MethodPost, "/api/v1/private/order", map[string]interface{}{
		"l2Signature": requestSignature,
	}, false, nil); err != nil {
		t.Fatalf("POST call: %v", err)
	}

	logged := edgexObservedText(observed.All())
	for _, secret := range []string{querySecret, requestSignature, responseToken, "l2Signature", `"token"`} {
		if strings.Contains(logged, secret) {
			t.Fatalf("debug log leaked %q: %s", secret, logged)
		}
	}
	for _, metadata := range []string{"method", "path", "request_bytes", "status", "response_bytes"} {
		if !strings.Contains(logged, metadata) {
			t.Fatalf("debug log omitted safe metadata %q: %s", metadata, logged)
		}
	}
}

func TestPrivateWSLogsRedactAccountPayloads(t *testing.T) {
	const tradeSecret = "edgex-private-trade-payload-secret"
	const errorSecret = "edgex-private-error-payload-secret"

	core, observed := observer.New(zap.DebugLevel)
	client := NewWsAccountClient(context.Background(), "private-key", "account-id")
	client.Logger = zap.New(core).Sugar()

	client.handleMessage([]byte(fmt.Sprintf(`{"type":"trade-event","content":{"event":"ACCOUNT_UPDATE","version":7,"data":{"account":[{"id":"account-id","ethAddress":%q}]},"time":123}}`, tradeSecret)))
	client.handleMessage([]byte(fmt.Sprintf(`{"type":"error","content":{"token":%q}}`, errorSecret)))

	logged := edgexObservedText(observed.All())
	for _, secret := range []string{tradeSecret, errorSecret, `"ethAddress"`, `"token"`} {
		if strings.Contains(logged, secret) {
			t.Fatalf("private WS log leaked %q: %s", secret, logged)
		}
	}
	for _, metadata := range []string{"bytes", "type", "event", "version"} {
		if !strings.Contains(logged, metadata) {
			t.Fatalf("private WS log omitted safe metadata %q: %s", metadata, logged)
		}
	}
}

func TestRESTTransportErrorsDoNotExposePrivateResponseBodies(t *testing.T) {
	const responseSecret = "edgex-private-error-response-secret"

	for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := NewClient()
			client.BaseURL = "https://example.invalid"
			client.HTTPClient = &http.Client{Transport: edgexRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: status,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"token":%q}`, responseSecret))),
				}, nil
			})}

			err := client.call(context.Background(), http.MethodPost, "/api/v1/private/order", map[string]interface{}{
				"l2Signature": "request-signature",
			}, false, nil)
			if err == nil {
				t.Fatal("private REST error unexpectedly succeeded")
			}
			if strings.Contains(err.Error(), responseSecret) || strings.Contains(err.Error(), `"token"`) {
				t.Fatalf("private response leaked through returned error: %v", err)
			}
			if status == http.StatusTooManyRequests && !errors.Is(err, sdkcore.ErrRateLimited) {
				t.Fatalf("rate-limit classification was lost: %v", err)
			}
		})
	}
}

func TestRESTTransportErrorDoesNotExposePrivateQuery(t *testing.T) {
	const querySecret = "edgex-private-transport-query-secret"
	sentinel := errors.New("transport unavailable")
	client := NewClient()
	client.BaseURL = "https://example.invalid"
	client.HTTPClient = &http.Client{Transport: edgexRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("wrapped transport failure: %w", &url.Error{
			Op:  req.Method,
			URL: req.URL.String(),
			Err: sentinel,
		})
	})}

	err := client.call(context.Background(), http.MethodGet, "/api/v1/private/account", map[string]interface{}{
		"accountId": querySecret,
	}, false, nil)
	if err == nil {
		t.Fatal("private REST transport error unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), querySecret) || strings.Contains(err.Error(), "accountId") {
		t.Fatalf("private query leaked through transport error: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("transport cause was not preserved: %v", err)
	}
}

type classifiedEdgeXTransportError struct {
	secret string
	cause  error
}

func (e *classifiedEdgeXTransportError) Error() string { return e.secret }

func (e *classifiedEdgeXTransportError) Unwrap() error { return e.cause }

func TestRESTTransportErrorRedactsArbitraryErrorTreesAndPreservesClassification(t *testing.T) {
	const secret = "https://example.invalid/private?signature=SENTINEL_EDGEX_SIGNATURE"
	sentinel := errors.New("sentinel transport classification")
	leaf := &classifiedEdgeXTransportError{secret: secret, cause: sentinel}

	tests := []struct {
		name  string
		cause error
	}{
		{name: "fmt wrapper", cause: fmt.Errorf("wrapped transport: %w", leaf)},
		{name: "errors.Join", cause: errors.Join(errors.New("independent failure"), leaf)},
		{name: "multiple percent-w", cause: fmt.Errorf("two causes: %w / %w", errors.New("independent failure"), leaf)},
		{name: "leaf url.Error", cause: &url.Error{Op: http.MethodGet, URL: secret, Err: leaf}},
		{name: "leaf error text", cause: leaf},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fmt.Errorf("edgex GET /api/v1/private/account transport failed: %w", edgexTransportCause(tt.cause))
			if strings.Contains(got.Error(), secret) {
				t.Fatalf("transport error leaked signed URL: %v", got)
			}
			if got.Error() != "edgex GET /api/v1/private/account transport failed: transport failure" {
				t.Fatalf("transport error = %q, want fixed safe text", got.Error())
			}
			if !errors.Is(got, sentinel) {
				t.Fatalf("transport error lost errors.Is classification: %v", got)
			}
			var classified *classifiedEdgeXTransportError
			if !errors.As(got, &classified) || classified != leaf {
				t.Fatalf("transport error lost errors.As classification: %v", got)
			}
		})
	}
}

func TestRESTTransportErrorRedactsLeafURLErrorAndPreservesType(t *testing.T) {
	const secret = "https://example.invalid/private?signature=SENTINEL_EDGEX_LEAF_URL"
	cause := &url.Error{Op: http.MethodGet, URL: secret}
	got := fmt.Errorf("edgex GET /api/v1/private/account transport failed: %w", edgexTransportCause(cause))
	if strings.Contains(got.Error(), secret) {
		t.Fatalf("transport error leaked leaf URL: %v", got)
	}
	var urlErr *url.Error
	if !errors.As(got, &urlErr) || urlErr != cause {
		t.Fatalf("transport error lost leaf *url.Error type: %T %v", got, got)
	}
}

func TestRESTSuccessStatusAPIErrorEnvelopeRedactsMessageAndPreservesSafeClassification(t *testing.T) {
	tests := []struct {
		name          string
		code          string
		message       string
		wantRateLimit bool
	}{
		{
			name:    "ordinary API error",
			code:    "AUTH_FAILED",
			message: "SENTINEL_EDGEX_AUTH_BODY_SECRET",
		},
		{
			name:          "rate limit code",
			code:          "RATE_LIMIT_EXCEEDED",
			message:       "SENTINEL_EDGEX_RATE_BODY_SECRET",
			wantRateLimit: true,
		},
		{
			name:          "rate limit message",
			code:          "E_THROTTLED",
			message:       "too many requests SENTINEL_EDGEX_THROTTLE_BODY_SECRET",
			wantRateLimit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient().WithCredentials("01", "test-account")
			client.BaseURL = "https://example.invalid"
			client.HTTPClient = &http.Client{Transport: edgexRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("X-edgeX-Api-Signature") == "" {
					t.Fatal("test did not enter signed private-request path")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"code":%q,"msg":%q,"data":{}}`, tt.code, tt.message))),
				}, nil
			})}

			var result map[string]any
			err := client.call(context.Background(), http.MethodGet, "/api/v1/private/account/getAccountById", map[string]interface{}{
				"accountId": "test-account",
			}, true, &result)
			if err == nil {
				t.Fatal("API error envelope unexpectedly succeeded")
			}
			if strings.Contains(err.Error(), tt.message) || strings.Contains(err.Error(), "SENTINEL_EDGEX") {
				t.Fatalf("API error leaked response message: %v", err)
			}
			if !strings.Contains(err.Error(), tt.code) {
				t.Fatalf("API error omitted safe code %q: %v", tt.code, err)
			}

			if tt.wantRateLimit {
				if !errors.Is(err, sdkcore.ErrRateLimited) {
					t.Fatalf("rate-limit classification was lost: %v", err)
				}
				var exchangeErr *sdkcore.ExchangeError
				if !errors.As(err, &exchangeErr) {
					t.Fatalf("rate-limit ExchangeError type was lost: %T %v", err, err)
				}
				if exchangeErr.Code != tt.code {
					t.Fatalf("ExchangeError.Code = %q, want %q", exchangeErr.Code, tt.code)
				}
				if strings.Contains(exchangeErr.Message, tt.message) || strings.Contains(exchangeErr.Message, "SENTINEL_EDGEX") {
					t.Fatalf("ExchangeError.Message leaked response message: %q", exchangeErr.Message)
				}
			} else if errors.Is(err, sdkcore.ErrRateLimited) {
				t.Fatalf("ordinary API error was misclassified as rate limited: %v", err)
			}
		})
	}
}

type edgexRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn edgexRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func edgexObservedText(entries []observer.LoggedEntry) string {
	var logged strings.Builder
	for _, entry := range entries {
		_, _ = fmt.Fprintf(&logged, "%s %v\n", entry.Message, entry.ContextMap())
	}
	return logged.String()
}
