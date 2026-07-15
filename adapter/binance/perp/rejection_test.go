package perp

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
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

func TestBinancePerpSupportedCommandsMapOnlyDefinitiveAPIRejections(t *testing.T) {
	inst := rejectionTestInstrument()
	for _, command := range []string{"submit", "cancel", "modify"} {
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
				rest := sdkperp.NewClient().WithRateLimiter(nil)
				rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if command == "modify" && req.Method == http.MethodGet {
						return binancePerpTestResponse(http.StatusOK, `{"orderId":123,"symbol":"BTCUSDT","side":"BUY","origQty":"1","price":"100","status":"NEW"}`), nil
					}
					if outcome.transport != nil {
						return nil, outcome.transport
					}
					return binancePerpTestResponse(outcome.status, outcome.body), nil
				})}
				exec := newExecutionClient(rest, rejectionTestProvider(inst), clock.NewRealClock())

				var err error
				switch command {
				case "submit":
					_, err = exec.Submit(context.Background(), model.OrderRequest{
						InstrumentID: inst.ID, ClientID: "reject", Side: enums.SideBuy,
						Type: enums.TypeMarket, Quantity: decimal.NewFromInt(1), PositionSide: enums.PosNet,
					})
				case "cancel":
					err = exec.Cancel(context.Background(), inst.ID, "123")
				case "modify":
					_, err = exec.Modify(context.Background(), inst.ID, "123", decimal.NewFromInt(101), decimal.NewFromInt(1))
				}
				if got := errors.Is(err, contract.ErrVenueRejected); got != outcome.definitive {
					t.Fatalf("err=%v ErrVenueRejected=%t, want %t", err, got, outcome.definitive)
				}
				if outcome.definitive {
					var apiErr *sdkperp.APIError
					if !errors.As(err, &apiErr) || apiErr.Code != -2010 {
						t.Fatalf("err=%v does not preserve original Binance APIError", err)
					}
				}
			})
		}
	}
}

func TestBinancePerpWritesTreatIncompleteOrMismatchedSuccessAsAmbiguous(t *testing.T) {
	inst := rejectionTestInstrument()
	tests := []struct {
		name   string
		invoke func(*executionClient) (*model.Order, error)
		body   string
	}{
		{
			name: "regular submit missing status",
			body: `{"symbol":"BTCUSDT","orderId":123,"clientOrderId":"client-1"}`,
			invoke: func(exec *executionClient) (*model.Order, error) {
				return exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst.ID, ClientID: "client-1", Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: decimal.NewFromInt(1), PositionSide: enums.PosNet})
			},
		},
		{
			name: "regular submit client mismatch",
			body: `{"symbol":"BTCUSDT","orderId":123,"clientOrderId":"other","status":"NEW"}`,
			invoke: func(exec *executionClient) (*model.Order, error) {
				return exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst.ID, ClientID: "client-1", Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: decimal.NewFromInt(1), PositionSide: enums.PosNet})
			},
		},
		{
			name: "algo submit missing status",
			body: `{"symbol":"BTCUSDT","algoId":456,"clientAlgoId":"client-1"}`,
			invoke: func(exec *executionClient) (*model.Order, error) {
				return exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst.ID, ClientID: "client-1", Side: enums.SideBuy, Type: enums.TypeStopMarket, Quantity: decimal.NewFromInt(1), TriggerPrice: decimal.NewFromInt(90), PositionSide: enums.PosNet})
			},
		},
		{
			name: "algo submit symbol mismatch",
			body: `{"symbol":"ETHUSDT","algoId":456,"clientAlgoId":"client-1","algoStatus":"NEW"}`,
			invoke: func(exec *executionClient) (*model.Order, error) {
				return exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst.ID, ClientID: "client-1", Side: enums.SideBuy, Type: enums.TypeStopMarket, Quantity: decimal.NewFromInt(1), TriggerPrice: decimal.NewFromInt(90), PositionSide: enums.PosNet})
			},
		},
		{
			name: "modify order identity mismatch",
			body: `{"symbol":"BTCUSDT","orderId":999,"clientOrderId":"existing","status":"NEW"}`,
			invoke: func(exec *executionClient) (*model.Order, error) {
				return exec.Modify(context.Background(), inst.ID, "123", decimal.NewFromInt(101), decimal.NewFromInt(1))
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rest := sdkperp.NewClient().WithRateLimiter(nil)
			rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if strings.Contains(tc.name, "modify") && req.Method == http.MethodGet {
					return binancePerpTestResponse(http.StatusOK, `{"orderId":123,"clientOrderId":"existing","symbol":"BTCUSDT","side":"BUY","origQty":"1","price":"100","status":"NEW"}`), nil
				}
				return binancePerpTestResponse(http.StatusOK, tc.body), nil
			})}
			exec := newExecutionClient(rest, rejectionTestProvider(inst), clock.NewRealClock())
			order, err := tc.invoke(exec)
			if order != nil || err == nil || errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("order=%+v err=%v, want ambiguous success-envelope error", order, err)
			}
		})
	}
}

func TestBinancePerpCancelValidatesSuccessfulResponseEnvelope(t *testing.T) {
	inst := rejectionTestInstrument()
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "valid cancellation", body: `{"symbol":"BTCUSDT","orderId":123,"status":"CANCELED"}`},
		{name: "malformed envelope", body: `{`, wantErr: true},
		{name: "empty envelope", body: `{}`, wantErr: true},
		{name: "symbol mismatch", body: `{"symbol":"ETHUSDT","orderId":123,"status":"CANCELED"}`, wantErr: true},
		{name: "order identity mismatch", body: `{"symbol":"BTCUSDT","orderId":999,"status":"CANCELED"}`, wantErr: true},
		{name: "missing status", body: `{"symbol":"BTCUSDT","orderId":123}`, wantErr: true},
		{name: "not canceled", body: `{"symbol":"BTCUSDT","orderId":123,"status":"NEW"}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rest := sdkperp.NewClient().WithRateLimiter(nil)
			rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return binancePerpTestResponse(http.StatusOK, tc.body), nil
			})}
			exec := newExecutionClient(rest, rejectionTestProvider(inst), clock.NewRealClock())

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

func TestBinancePerpAlgoCancelValidatesSuccessfulResponseEnvelope(t *testing.T) {
	inst := rejectionTestInstrument()
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "valid cancellation", body: `{"algoId":123,"clientAlgoId":"client-1","code":"200","msg":"success"}`},
		{name: "malformed envelope", body: `{`, wantErr: true},
		{name: "empty envelope", body: `{}`, wantErr: true},
		{name: "algo identity mismatch", body: `{"algoId":999,"code":"200","msg":"success"}`, wantErr: true},
		{name: "missing result code", body: `{"algoId":123,"msg":"success"}`, wantErr: true},
		{name: "non-success result code", body: `{"algoId":123,"code":"500","msg":"unknown"}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rest := sdkperp.NewClient().WithRateLimiter(nil)
			rest.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return binancePerpTestResponse(http.StatusOK, tc.body), nil
			})}
			exec := newExecutionClient(rest, rejectionTestProvider(inst), clock.NewRealClock())
			exec.rememberAlgo("123")

			err := exec.Cancel(context.Background(), inst.ID, "123")
			if tc.wantErr {
				if err == nil || errors.Is(err, contract.ErrVenueRejected) {
					t.Fatalf("err=%v, want ordinary ambiguous algo-cancel error", err)
				}
				if !exec.isKnownAlgo("123") {
					t.Fatal("ambiguous algo cancellation forgot routing identity")
				}
				return
			}
			if err != nil {
				t.Fatalf("Cancel: %v", err)
			}
			if exec.isKnownAlgo("123") {
				t.Fatal("successful algo cancellation retained routing identity")
			}
		})
	}
}

func rejectionTestInstrument() *model.Instrument {
	return instrumentFromSymbolInfo(&sdkperp.SymbolInfo{
		Symbol: "BTCUSDT", ContractType: "PERPETUAL", BaseAsset: "BTC", QuoteAsset: "USDT", MarginAsset: "USDT",
		Filters: []map[string]any{{"filterType": "PRICE_FILTER", "tickSize": "0.1"}, {"filterType": "LOT_SIZE", "stepSize": "0.001"}},
	})
}

func rejectionTestProvider(inst *model.Instrument) *instrumentProvider {
	provider := newInstrumentProvider()
	provider.byID[inst.ID.String()] = inst
	provider.bySymbol[inst.VenueSymbol] = inst.ID
	provider.all = []*model.Instrument{inst}
	return provider
}

func binancePerpTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
