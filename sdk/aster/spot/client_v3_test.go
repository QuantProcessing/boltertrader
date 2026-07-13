package spot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	exchanges "github.com/QuantProcessing/boltertrader/internal/errs"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestClientSignsV3RequestsWithoutLegacyHeader(t *testing.T) {
	profile, _ := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	security, err := astercommon.NewSecurityContext(astercommon.CredentialConfig{
		User:       "0x1111111111111111111111111111111111111111",
		PrivateKey: fmt.Sprintf("%064x", 1),
	}, astercommon.WithClock(astercommon.ClockFunc(func() time.Time {
		return time.UnixMicro(1_748_310_859_508_867)
	})))
	if err != nil {
		t.Fatal(err)
	}

	var captured *http.Request
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request.Clone(request.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    request,
		}, nil
	})}
	client, err := NewClient(profile, security)
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(httpClient)
	if err := client.Post(context.Background(), "/api/v3/order", map[string]any{
		"symbol": "ASTERUSDT", "side": "BUY", "type": "LIMIT",
	}, true, nil); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if captured == nil {
		t.Fatal("request was not sent")
	}
	if captured.URL.Scheme+"://"+captured.URL.Host != profile.RESTURL() || captured.URL.Path != "/api/v3/order" {
		t.Fatalf("request target = %s://%s%s", captured.URL.Scheme, captured.URL.Host, captured.URL.Path)
	}
	if got := captured.Header.Get("X-MBX-APIKEY"); got != "" {
		t.Fatalf("legacy X-MBX-APIKEY header = %q", got)
	}
	assertV3AuthQuery(t, captured.URL.Query(), security)
}

func TestClientFailsBeforeTransportWithoutCredentials(t *testing.T) {
	profile, _ := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	var calls atomic.Int64
	client, err := NewClient(profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("unexpected transport call")
	})})
	if err := client.Get(context.Background(), "/api/v3/account", nil, true, nil); err == nil {
		t.Fatal("signed request without credentials succeeded")
	}
	if calls.Load() != 0 {
		t.Fatalf("transport calls = %d, want 0", calls.Load())
	}
}

func TestClientRejectsNormalizedTestSymbolBeforeTransport(t *testing.T) {
	profile, _ := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	var calls atomic.Int64
	client, err := NewClient(profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("unexpected transport call")
	})})
	_, err = client.Depth(context.Background(), "  testasset  ", 10)
	var unsafe *astercommon.UnsafeSymbolError
	if !errors.As(err, &unsafe) {
		t.Fatalf("error = %T %v", err, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("transport calls = %d, want 0", calls.Load())
	}
}

func TestClientRejectsPerpProfile(t *testing.T) {
	profile, _ := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductPerp)
	if _, err := NewClient(profile, nil); err == nil {
		t.Fatal("spot client accepted perp profile")
	}
}

func TestClientRedactsMalformedVenueErrorBody(t *testing.T) {
	profile, _ := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	security, err := astercommon.NewSecurityContext(astercommon.CredentialConfig{
		User:       "0x1111111111111111111111111111111111111111",
		PrivateKey: fmt.Sprintf("%064x", 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(profile, security)
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("signature=0xabc&nonce=1&symbol=ASTERUSDT")),
			Request:    request,
		}, nil
	})})
	err = client.Post(context.Background(), "/api/v3/order", map[string]any{"symbol": "ASTERUSDT"}, true, nil)
	if err == nil {
		t.Fatal("malformed venue error succeeded")
	}
	for _, forbidden := range []string{"0xabc", "nonce=", "ASTERUSDT"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("venue error leaked %q: %q", forbidden, err.Error())
		}
	}
}

func TestClientRedactsRateLimitVenueMessage(t *testing.T) {
	profile, _ := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	client, err := NewClient(profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":-1003,"msg":"too many requests; signature=0xabc&nonce=1&symbol=ASTERUSDT"}`)),
			Request:    request,
		}, nil
	})})

	err = client.Get(context.Background(), "/api/v3/ping", nil, false, nil)
	if !errors.Is(err, exchanges.ErrRateLimited) {
		t.Fatalf("error = %v, want rate-limited classification", err)
	}
	for _, forbidden := range []string{"0xabc", "nonce=", "ASTERUSDT"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("rate-limit error leaked %q: %q", forbidden, err.Error())
		}
	}
}

func assertV3AuthQuery(t *testing.T, query url.Values, security *astercommon.SecurityContext) {
	t.Helper()
	for _, key := range []string{"user", "signer", "nonce", "timestamp", "signature"} {
		if query.Get(key) == "" {
			t.Errorf("signed query missing %s", key)
		}
	}
	if query.Get("user") != security.User() || query.Get("signer") != security.Signer() {
		t.Error("signed query identity mismatch")
	}
}

func newClientForServer(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	profile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		clone.Host = target.Host
		return transport.RoundTrip(clone)
	})})
	return client
}

func newTestWSMarketClient(t *testing.T, ctx context.Context) *WsMarketClient {
	t.Helper()
	profile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewWsMarketClient(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
