package perp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
)

func TestOrderAPIErrorPreservesHTTPStatusForDefinitiveRejection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		code       int
		message    string
		definitive bool
		wantAPI    bool
		wantAuth   bool
		wantRate   bool
	}{
		{name: "invalid order message", status: http.StatusBadRequest, code: -1013, message: "filter failure", definitive: true, wantAPI: true},
		{name: "bad symbol", status: http.StatusBadRequest, code: -1121, message: "invalid symbol", definitive: true, wantAPI: true},
		{name: "business rejection", status: http.StatusBadRequest, code: -2010, message: "order rejected", definitive: true, wantAPI: true},
		{name: "cancel rejection", status: http.StatusBadRequest, code: -2011, message: "unknown order", definitive: true, wantAPI: true},
		{name: "missing order", status: http.StatusBadRequest, code: -2013, message: "order does not exist", definitive: true, wantAPI: true},
		{name: "would immediately trigger", status: http.StatusBadRequest, code: -2021, message: "would immediately trigger", definitive: true, wantAPI: true},
		{name: "reduce-only rejection", status: http.StatusBadRequest, code: -2022, message: "reduce only rejected", definitive: true, wantAPI: true},
		{name: "negative quantity", status: http.StatusBadRequest, code: -4003, message: "quantity less than zero", definitive: true, wantAPI: true},
		{name: "unknown request-range code", status: http.StatusBadRequest, code: -1192, message: "future request error", wantAPI: true},
		{name: "unknown order-range code", status: http.StatusBadRequest, code: -2500, message: "future order error", wantAPI: true},
		{name: "unknown filter-range code", status: http.StatusBadRequest, code: -4500, message: "future filter error", wantAPI: true},
		{name: "unknown temporary client error", status: http.StatusBadRequest, code: -1000, message: "unknown error", wantAPI: true},
		{name: "request timeout", status: http.StatusRequestTimeout, code: -1007, message: "timeout", wantAPI: true},
		{name: "unknown conflict", status: http.StatusConflict, code: -9999, message: "unknown conflict", wantAPI: true},
		{name: "unauthorized", status: http.StatusUnauthorized, code: -2015, message: "invalid api key", wantAuth: true},
		{name: "forbidden", status: http.StatusForbidden, code: -2015, message: "permission denied", wantAuth: true},
		{name: "ip ban", status: http.StatusTeapot, code: -1003, message: "banned until later", wantRate: true},
		{name: "rate limited", status: http.StatusTooManyRequests, code: -1003, message: "too many requests", wantRate: true},
		{name: "server failure", status: http.StatusInternalServerError, code: -1000, message: "internal error", wantAPI: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient().WithRateLimiter(nil)
			client.HTTPClient = &http.Client{Transport: rejectionRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"code":%d,"msg":%q}`, tc.code, tc.message))),
					Header:     make(http.Header),
				}, nil
			})}

			_, err := client.PlaceOrder(context.Background(), PlaceOrderParams{Symbol: "BTCUSDT", Side: "BUY", Type: "MARKET", Quantity: "1"})
			if got := errors.Is(err, sdkcore.ErrAuthFailed); got != tc.wantAuth {
				t.Fatalf("ErrAuthFailed=%t, want %t: %v", got, tc.wantAuth, err)
			}
			if got := errors.Is(err, sdkcore.ErrRateLimited); got != tc.wantRate {
				t.Fatalf("ErrRateLimited=%t, want %t: %v", got, tc.wantRate, err)
			}
			var apiErr *APIError
			if got := errors.As(err, &apiErr); got != tc.wantAPI {
				t.Fatalf("errors.As(*APIError)=%t, want %t: %v", got, tc.wantAPI, err)
			}
			if tc.wantAPI && apiErr.HTTPStatus != tc.status {
				t.Fatalf("HTTPStatus=%d, want %d", apiErr.HTTPStatus, tc.status)
			}
			if got := IsDefinitiveOrderRejection(err); got != tc.definitive {
				t.Fatalf("IsDefinitiveOrderRejection=%t, want %t", got, tc.definitive)
			}
		})
	}
}

type rejectionRoundTripFunc func(*http.Request) (*http.Response, error)

func (f rejectionRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
