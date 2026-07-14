package sdk

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
)

type rawRoundTripFunc func(*http.Request) (*http.Response, error)

func (f rawRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type classifiedBitgetTransportError struct {
	secret string
	cause  error
}

func (e *classifiedBitgetTransportError) Error() string { return e.secret }

func (e *classifiedBitgetTransportError) Unwrap() error { return e.cause }

func TestTransportErrorRedactsArbitraryErrorTreesAndPreservesClassification(t *testing.T) {
	const (
		path   = "/api/v3/private/order"
		secret = "https://example.invalid/private?signature=SENTINEL_BITGET_SIGNATURE"
	)
	sentinel := errors.New("sentinel transport classification")
	leaf := &classifiedBitgetTransportError{secret: secret, cause: sentinel}

	tests := []struct {
		name  string
		cause error
	}{
		{name: "fmt wrapper", cause: fmt.Errorf("wrapped transport: %w", leaf)},
		{name: "errors.Join", cause: errors.Join(errors.New("independent failure"), leaf)},
		{name: "multiple percent-w", cause: fmt.Errorf("two causes: %w / %w", errors.New("independent failure"), leaf)},
		{name: "leaf url.Error", cause: &url.Error{Op: http.MethodPost, URL: secret, Err: leaf}},
		{name: "leaf error text", cause: leaf},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newTransportError(http.MethodPost, path, tt.cause)
			if strings.Contains(got.Error(), secret) {
				t.Fatalf("transport error leaked signed URL: %v", got)
			}
			if got.Error() != "bitget sdk: POST /api/v3/private/order transport failed: transport failure" {
				t.Fatalf("transport error = %q, want fixed safe text", got.Error())
			}
			if !errors.Is(got, sentinel) {
				t.Fatalf("transport error lost errors.Is classification: %v", got)
			}
			var classified *classifiedBitgetTransportError
			if !errors.As(got, &classified) || classified != leaf {
				t.Fatalf("transport error lost errors.As classification: %v", got)
			}
		})
	}
}

func TestTransportErrorRedactsLeafURLErrorAndPreservesType(t *testing.T) {
	const secret = "https://example.invalid/private?signature=SENTINEL_BITGET_LEAF_URL"
	cause := &url.Error{Op: http.MethodPost, URL: secret}
	got := newTransportError(http.MethodPost, "/api/v3/private/order", cause)
	if strings.Contains(got.Error(), secret) {
		t.Fatalf("transport error leaked leaf URL: %v", got)
	}
	var urlErr *url.Error
	if !errors.As(got, &urlErr) || urlErr != cause {
		t.Fatalf("transport error lost leaf *url.Error type: %T %v", got, got)
	}
}

func TestClient_PostPrivateRaw(t *testing.T) {
	var seenPath string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	var out responseEnvelope[map[string]any]
	if err := client.PostPrivateRaw(context.Background(), "/api/v2/spot/trade/batch-orders", map[string]any{"symbol": "BTCUSDT"}, &out); err != nil {
		t.Fatalf("PostPrivateRaw returned error: %v", err)
	}
	if seenPath != "/api/v2/spot/trade/batch-orders" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
}

func TestClient_PostPrivateRawNonSuccessRedactsRequestAndResponse(t *testing.T) {
	const (
		path           = "/api/v3/account/set-passphrase"
		requestSecret  = "SENTINEL_REQUEST_PRIVATE_TOKEN_4f391a"
		responseSecret = "SENTINEL_RESPONSE_ACCESS_TOKEN_82c6de"
	)
	responseBody := `{"code":"40001","msg":"` + responseSecret + `"}`
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Status:     "401 Unauthorized",
				Body:       io.NopCloser(strings.NewReader(responseBody)),
				Header:     make(http.Header),
			}, nil
		})})

	var out responseEnvelope[map[string]any]
	err := client.PostPrivateRaw(context.Background(), path, map[string]string{
		"passphrase": requestSecret,
	}, &out)
	if err == nil {
		t.Fatal("PostPrivateRaw returned nil error for non-success response")
	}
	errText := err.Error()
	for _, secret := range []string{requestSecret, responseSecret} {
		if strings.Contains(errText, secret) {
			t.Fatalf("PostPrivateRaw error leaked secret %q: %s", secret, errText)
		}
	}
	for _, safeContext := range []string{http.MethodPost, path, "401", "response_bytes="} {
		if !strings.Contains(errText, safeContext) {
			t.Fatalf("PostPrivateRaw error %q does not contain safe context %q", errText, safeContext)
		}
	}
}

func TestClient_PrivateRawRateLimitPreservesSentinel(t *testing.T) {
	const responseSecret = "SENTINEL_RATE_LIMIT_RESPONSE_TOKEN_31ac8e"
	tests := []struct {
		name   string
		method string
		path   string
		call   func(*Client) error
	}{
		{
			name:   "GET",
			method: http.MethodGet,
			path:   "/api/v3/account/assets",
			call: func(client *Client) error {
				var out responseEnvelope[map[string]any]
				return client.GetPrivateRaw(context.Background(), "/api/v3/account/assets", nil, &out)
			},
		},
		{
			name:   "POST",
			method: http.MethodPost,
			path:   "/api/v3/trade/place-order",
			call: func(client *Client) error {
				var out responseEnvelope[map[string]any]
				return client.PostPrivateRaw(context.Background(), "/api/v3/trade/place-order", map[string]string{
					"symbol": "BTCUSDT",
				}, &out)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient().
				WithCredentials("key", "secret", "passphrase").
				WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusTooManyRequests,
						Status:     "429 Too Many Requests",
						Body:       io.NopCloser(strings.NewReader(`{"code":"429","msg":"` + responseSecret + `"}`)),
						Header:     make(http.Header),
					}, nil
				})})

			err := tt.call(client)
			if !errors.Is(err, sdkcore.ErrRateLimited) {
				t.Fatalf("%s rate-limit error lost sentinel: %v", tt.method, err)
			}
			if strings.Contains(err.Error(), responseSecret) {
				t.Fatalf("%s rate-limit error leaked response secret: %v", tt.method, err)
			}
			for _, safeContext := range []string{tt.method, tt.path, "429", "response_bytes="} {
				if !strings.Contains(err.Error(), safeContext) {
					t.Fatalf("%s rate-limit error %q does not contain safe context %q", tt.method, err, safeContext)
				}
			}
		})
	}
}

func TestClient_PrivateRawStatusErrorRedactsCallerPathQuery(t *testing.T) {
	const (
		safePath = "/api/v3/private/order"
		secret   = "SENTINEL_BITGET_PATH_QUERY_94fd32"
	)
	rawPath := safePath + "?accessToken=" + secret
	tests := []struct {
		name string
		call func(*Client) error
	}{
		{
			name: "GET",
			call: func(client *Client) error {
				var out responseEnvelope[map[string]any]
				return client.GetPrivateRaw(context.Background(), rawPath, nil, &out)
			},
		},
		{
			name: "POST",
			call: func(client *Client) error {
				var out responseEnvelope[map[string]any]
				return client.PostPrivateRaw(context.Background(), rawPath, map[string]string{"symbol": "BTCUSDT"}, &out)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient().
				WithCredentials("key", "secret", "passphrase").
				WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusUnauthorized,
						Status:     "401 Unauthorized",
						Body:       io.NopCloser(strings.NewReader(`{"code":"40001","msg":"unauthorized"}`)),
						Header:     make(http.Header),
					}, nil
				})})

			err := tt.call(client)
			if err == nil {
				t.Fatal("private raw request returned nil status error")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "accessToken") {
				t.Fatalf("private raw status error leaked caller path query: %v", err)
			}
			if !strings.Contains(err.Error(), safePath) {
				t.Fatalf("private raw status error lost safe path context: %v", err)
			}
		})
	}
}

func TestClient_PostPrivateRawTransportErrorRedactsNestedURLAndPreservesCause(t *testing.T) {
	const (
		path          = "/api/v3/trade/place-order"
		requestSecret = "SENTINEL_PRIVATE_POST_TOKEN_5db021"
	)
	sentinel := errors.New("sentinel transport failure")
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("wrapped transport failure: %w", &url.Error{
				Op:  req.Method,
				URL: req.URL.String() + "?debug=" + requestSecret,
				Err: sentinel,
			})
		})})

	var out responseEnvelope[map[string]any]
	err := client.PostPrivateRaw(context.Background(), path, map[string]string{
		"accessToken": requestSecret,
	}, &out)
	if err == nil {
		t.Fatal("PostPrivateRaw returned nil transport error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("PostPrivateRaw transport error lost cause: %v", err)
	}
	if strings.Contains(err.Error(), requestSecret) {
		t.Fatalf("PostPrivateRaw transport error leaked nested URL secret: %v", err)
	}
	for _, safeContext := range []string{http.MethodPost, path, "transport failed"} {
		if !strings.Contains(err.Error(), safeContext) {
			t.Fatalf("PostPrivateRaw transport error %q does not contain safe context %q", err, safeContext)
		}
	}
}

func TestClient_GetPrivateRaw(t *testing.T) {
	var seenPath string
	var seenQuery string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			seenQuery = req.URL.RawQuery
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	var out responseEnvelope[map[string]any]
	if err := client.GetPrivateRaw(context.Background(), "/api/v2/spot/trade/orderInfo", map[string]string{"orderId": "1"}, &out); err != nil {
		t.Fatalf("GetPrivateRaw returned error: %v", err)
	}
	if seenPath != "/api/v2/spot/trade/orderInfo" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if !strings.Contains(seenQuery, "orderId=1") {
		t.Fatalf("unexpected query: %s", seenQuery)
	}
}

func TestClient_GetPrivateRawNonSuccessRedactsResponse(t *testing.T) {
	const (
		path           = "/api/v3/account/assets"
		responseSecret = "SENTINEL_RESPONSE_REFRESH_TOKEN_74ed13"
	)
	responseBody := `{"code":"40001","msg":"` + responseSecret + `"}`
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Status:     "401 Unauthorized",
				Body:       io.NopCloser(strings.NewReader(responseBody)),
				Header:     make(http.Header),
			}, nil
		})})

	var out responseEnvelope[map[string]any]
	err := client.GetPrivateRaw(context.Background(), path, nil, &out)
	if err == nil {
		t.Fatal("GetPrivateRaw returned nil error for non-success response")
	}
	errText := err.Error()
	if strings.Contains(errText, responseSecret) {
		t.Fatalf("GetPrivateRaw error leaked response secret: %s", errText)
	}
	for _, safeContext := range []string{http.MethodGet, path, "401", "response_bytes="} {
		if !strings.Contains(errText, safeContext) {
			t.Fatalf("GetPrivateRaw error %q does not contain safe context %q", errText, safeContext)
		}
	}
}

func TestClient_GetPrivateRawTransportErrorRedactsQueryAndPreservesCause(t *testing.T) {
	const (
		path        = "/api/v3/account/assets"
		querySecret = "SENTINEL_PRIVATE_QUERY_TOKEN_a7e401"
	)
	sentinel := errors.New("sentinel transport failure")
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("wrapped transport failure: %w", &url.Error{
				Op:  req.Method,
				URL: req.URL.String(),
				Err: sentinel,
			})
		})})

	var out responseEnvelope[map[string]any]
	err := client.GetPrivateRaw(context.Background(), path, map[string]string{
		"accessToken": querySecret,
	}, &out)
	if err == nil {
		t.Fatal("GetPrivateRaw returned nil transport error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("GetPrivateRaw transport error lost cause: %v", err)
	}
	if strings.Contains(err.Error(), querySecret) {
		t.Fatalf("GetPrivateRaw transport error leaked query secret: %v", err)
	}
	for _, safeContext := range []string{http.MethodGet, path, "transport failed"} {
		if !strings.Contains(err.Error(), safeContext) {
			t.Fatalf("GetPrivateRaw transport error %q does not contain safe context %q", err, safeContext)
		}
	}
}
