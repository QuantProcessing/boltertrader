package spot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
)

func TestBinanceSpotSupportedCommandsMapOnlyDefinitiveAPIRejections(t *testing.T) {
	inst := testSpotInstrument()
	for _, command := range []string{"submit", "cancel"} {
		for _, outcome := range []struct {
			name       string
			status     int
			body       string
			transport  error
			definitive bool
		}{
			{name: "business rejection", status: http.StatusBadRequest, body: `{"code":-2010,"msg":"order rejected"}`, definitive: true},
			{name: "unknown request-range code", status: http.StatusBadRequest, body: `{"code":-1192,"msg":"future request error"}`},
			{name: "unknown order-range code", status: http.StatusBadRequest, body: `{"code":-2500,"msg":"future order error"}`},
			{name: "unknown filter-range code", status: http.StatusBadRequest, body: `{"code":-4500,"msg":"future filter error"}`},
			{name: "unknown temporary client error", status: http.StatusBadRequest, body: `{"code":-1000,"msg":"unknown error"}`},
			{name: "authentication failure", status: http.StatusUnauthorized, body: `{"code":-2015,"msg":"invalid api key"}`},
			{name: "ip rate limit", status: http.StatusTeapot, body: `{"code":-1003,"msg":"banned until later"}`},
			{name: "server failure", status: http.StatusInternalServerError, body: `{"code":-1000,"msg":"internal error"}`},
			{name: "malformed rejection", status: http.StatusBadRequest, body: `not-json`},
			{name: "transport failure", transport: io.ErrUnexpectedEOF},
			{name: "deadline", transport: context.DeadlineExceeded},
		} {
			t.Run(command+"/"+outcome.name, func(t *testing.T) {
				rest := sdkspot.NewClient().WithRateLimiter(nil)
				rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					if outcome.transport != nil {
						return nil, outcome.transport
					}
					return &http.Response{StatusCode: outcome.status, Body: io.NopCloser(strings.NewReader(outcome.body)), Header: make(http.Header)}, nil
				})}
				exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock())

				var err error
				switch command {
				case "submit":
					_, err = exec.Submit(context.Background(), model.OrderRequest{
						InstrumentID: inst.ID, ClientID: "reject", Side: enums.SideBuy,
						Type: enums.TypeMarket, Quantity: d("0.01"), PositionSide: enums.PosNet,
					})
				case "cancel":
					err = exec.Cancel(context.Background(), inst.ID, "123")
				}
				if got := errors.Is(err, contract.ErrVenueRejected); got != outcome.definitive {
					t.Fatalf("err=%v ErrVenueRejected=%t, want %t", err, got, outcome.definitive)
				}
				if outcome.definitive {
					var apiErr *sdkspot.APIError
					if !errors.As(err, &apiErr) || apiErr.Code != -2010 {
						t.Fatalf("err=%v does not preserve original Binance APIError", err)
					}
				}
			})
		}
	}
}

func TestBinanceSpotSubmitTreatsIncompleteOrMismatchedSuccessAsAmbiguous(t *testing.T) {
	inst := testSpotInstrument()
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty envelope", body: `{}`},
		{name: "missing status", body: `{"symbol":"ETHUSDT","orderId":123,"clientOrderId":"client-1"}`},
		{name: "symbol mismatch", body: `{"symbol":"BTCUSDT","orderId":123,"clientOrderId":"client-1","status":"NEW"}`},
		{name: "client identity mismatch", body: `{"symbol":"ETHUSDT","orderId":123,"clientOrderId":"other","status":"NEW"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rest := sdkspot.NewClient().WithRateLimiter(nil)
			rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock())
			order, err := exec.Submit(context.Background(), model.OrderRequest{
				InstrumentID: inst.ID, ClientID: "client-1", Side: enums.SideBuy,
				Type: enums.TypeMarket, Quantity: d("0.01"), PositionSide: enums.PosNet,
			})
			if order != nil || err == nil || errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("order=%+v err=%v, want ambiguous success-envelope error", order, err)
			}
		})
	}
}

func TestBinanceSpotCancelValidatesSuccessfulResponseEnvelope(t *testing.T) {
	inst := testSpotInstrument()
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "valid cancellation", body: `{"symbol":"ETHUSDT","orderId":123,"status":"CANCELED"}`},
		{name: "malformed envelope", body: `{`, wantErr: true},
		{name: "empty envelope", body: `{}`, wantErr: true},
		{name: "symbol mismatch", body: `{"symbol":"BTCUSDT","orderId":123,"status":"CANCELED"}`, wantErr: true},
		{name: "order identity mismatch", body: `{"symbol":"ETHUSDT","orderId":999,"status":"CANCELED"}`, wantErr: true},
		{name: "missing status", body: `{"symbol":"ETHUSDT","orderId":123}`, wantErr: true},
		{name: "not canceled", body: `{"symbol":"ETHUSDT","orderId":123,"status":"NEW"}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rest := sdkspot.NewClient().WithRateLimiter(nil)
			rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock())

			err := exec.Cancel(context.Background(), inst.ID, "123")
			if tc.wantErr {
				if err == nil || errors.Is(err, contract.ErrVenueRejected) {
					t.Fatalf("err=%v, want ordinary ambiguous cancel error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Cancel: %v", err)
			}
		})
	}
}
