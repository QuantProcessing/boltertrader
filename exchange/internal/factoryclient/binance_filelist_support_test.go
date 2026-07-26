//go:build binance_filetest

package factoryclient

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type okxSpotClient struct{ ws exchange.SpotWebSocket }
type okxPerpClient struct{ ws exchange.PerpWebSocket }
type binanceSpotClient struct{ ws exchange.SpotWebSocket }
type binancePerpClient struct{ ws exchange.PerpWebSocket }
type hyperliquidSpotClient struct{ ws exchange.SpotWebSocket }
type hyperliquidPerpClient struct{ ws exchange.PerpWebSocket }

type openAPIRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn openAPIRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type noSendTransport struct {
	calls atomic.Int64
}

func (transport *noSendTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"error":"unexpected network call"}`)),
	}, nil
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func errorFromValue[T any](_ T, err error) error {
	return err
}

func errorFromSlice[T any](_ []T, err error) error {
	return err
}

func assertExchangeErrorKind(t *testing.T, err error, want exchange.ErrorKind) {
	t.Helper()
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) || exchangeErr.Kind() != want {
		t.Fatalf("error = %v, want exchange kind %s", err, want)
	}
}

func openAPIJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
