package spot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/aster/spot"
	"github.com/shopspring/decimal"
)

var (
	_ contract.MarketDataClient  = (*marketDataClient)(nil)
	_ contract.ExecutionClient   = (*executionClient)(nil)
	_ contract.AccountClient     = (*accountClient)(nil)
	_ contract.AccountIDProvider = (*executionClient)(nil)
	_ contract.AccountIDProvider = (*accountClient)(nil)
	_ model.InstrumentProvider   = (*instrumentProvider)(nil)
)

func TestDefaultAndCustomAccountIDPropagation(t *testing.T) {
	provider := newInstrumentProvider()
	inst := mustSpotInstrument(t)
	provider.LoadSnapshot([]*model.Instrument{inst})

	exec := newExecutionClient(nil, provider, clock.NewRealClock(), "")
	if exec.AccountID() != AccountIDDefault {
		t.Fatalf("default account id=%q, want %q", exec.AccountID(), AccountIDDefault)
	}
	if AccountIDDefault != "ASTER-001" {
		t.Fatalf("AccountIDDefault=%q, want %q", AccountIDDefault, "ASTER-001")
	}
	order := orderFromResponse(&sdkspot.OrderResponse{
		Symbol: "BTCUSDT", OrderID: 42, ClientOrderID: "c1", Status: "NEW", Type: "LIMIT", Side: "BUY", TimeInForce: "GTC", OrigQty: "0.25", Price: "60000",
	}, model.OrderRequest{InstrumentID: inst.ID}, AccountIDDefault)
	if order.Request.AccountID != AccountIDDefault {
		t.Fatalf("order account id=%q, want default", order.Request.AccountID)
	}

	acct := newAccountClient(nil, clock.NewRealClock(), "ASTER-CUSTOM")
	state := accountStateFromResponse(&sdkspot.AccountResponse{
		UpdateTime: 1700000000000,
		Balances:   []sdkspot.Balance{{Asset: "USDT", Free: "12.3", Locked: "0.7"}},
	}, acct.AccountID(), clock.NewRealClock().Now())
	if state.AccountID != "ASTER-CUSTOM" || state.Balances[0].AccountID != "ASTER-CUSTOM" {
		t.Fatalf("custom account id not propagated through state: %#v", state)
	}
}

func TestValidateSubmitRejectsInvalidSpotRequestsBeforeREST(t *testing.T) {
	inst := mustSpotInstrument(t)
	exec := newExecutionClient(spotClientNoNetwork(t), testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	valid := model.OrderRequest{
		AccountID:    AccountIDDefault,
		InstrumentID: inst.ID,
		ClientID:     "c-valid",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1.23"),
		Price:        d("10.0000"),
		PositionSide: enums.PosNet,
	}
	if err := exec.ValidateSubmit(valid); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	cases := map[string]model.OrderRequest{
		"account mismatch":       withSpot(valid, func(r *model.OrderRequest) { r.AccountID = "OTHER" }),
		"zero quantity":          withSpot(valid, func(r *model.OrderRequest) { r.Quantity = decimal.Zero }),
		"negative quantity":      withSpot(valid, func(r *model.OrderRequest) { r.Quantity = d("-1") }),
		"limit missing price":    withSpot(valid, func(r *model.OrderRequest) { r.Price = decimal.Zero }),
		"limit non tick price":   withSpot(valid, func(r *model.OrderRequest) { r.Price = d("10.00001") }),
		"non step quantity":      withSpot(valid, func(r *model.OrderRequest) { r.Quantity = d("1.235") }),
		"below minimum quantity": withSpot(valid, func(r *model.OrderRequest) { r.Quantity = d("0.001") }),
		"below minimum notional": withSpot(valid, func(r *model.OrderRequest) { r.Quantity = d("0.01") }),
		"wrong instrument kind":  withSpot(valid, func(r *model.OrderRequest) { r.InstrumentID.Kind = enums.KindPerp }),
		"market post only":       withSpot(valid, func(r *model.OrderRequest) { r.Type = enums.TypeMarket; r.TIF = enums.TifGTX; r.Price = decimal.Zero }),
		"reduce only":            withSpot(valid, func(r *model.OrderRequest) { r.ReduceOnly = true }),
		"explicit position side": withSpot(valid, func(r *model.OrderRequest) { r.PositionSide = enums.PosLong }),
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if err := exec.ValidateSubmit(req); err == nil {
				t.Fatalf("ValidateSubmit accepted invalid request")
			}
			if _, err := exec.Submit(context.Background(), req); err == nil {
				t.Fatalf("Submit accepted invalid request")
			}
		})
	}
}

func TestSubmitRejectsMalformedRequiredOrderResponseDecimal(t *testing.T) {
	inst := mustSpotInstrument(t)
	exec := newExecutionClient(spotClientResponse(t, `{"symbol":"ASTERUSDT","orderId":42,"clientOrderId":"c-bad","status":"NEW","type":"LIMIT","side":"BUY","timeInForce":"GTC","origQty":"not-decimal","price":"1.2345","executedQty":"0","cummulativeQuoteQty":"0"}`), testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	_, err := exec.Submit(context.Background(), model.OrderRequest{
		AccountID:    AccountIDDefault,
		InstrumentID: inst.ID,
		ClientID:     "c-bad",
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1.23"),
		Price:        d("10.0000"),
		PositionSide: enums.PosNet,
	})
	if err == nil {
		t.Fatalf("Submit accepted malformed required response decimal")
	}
}

func TestSpotSubmitRejectsMalformedSuccessfulResponseIdentity(t *testing.T) {
	inst := mustSpotInstrument(t)
	req := model.OrderRequest{
		AccountID: AccountIDDefault, InstrumentID: inst.ID, ClientID: "submit-identity",
		Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC,
		Quantity: d("1.23"), Price: d("10.0000"), PositionSide: enums.PosNet,
	}
	for name, identity := range map[string]struct {
		symbol   string
		clientID string
		orderID  int64
	}{
		"empty symbol":        {clientID: req.ClientID, orderID: 42},
		"mismatched symbol":   {symbol: "OTHERUSDT", clientID: req.ClientID, orderID: 42},
		"empty client id":     {symbol: inst.VenueSymbol, orderID: 42},
		"mismatched client":   {symbol: inst.VenueSymbol, clientID: "other-client", orderID: 42},
		"missing venue order": {symbol: inst.VenueSymbol, clientID: req.ClientID},
	} {
		t.Run(name, func(t *testing.T) {
			body := fmt.Sprintf(`{"symbol":%q,"orderId":%d,"clientOrderId":%q,"status":"NEW","type":"LIMIT","side":"BUY","timeInForce":"GTC","origQty":"1.23","price":"10","executedQty":"0","cumQty":"0","cumQuote":"0"}`, identity.symbol, identity.orderID, identity.clientID)
			exec := newExecutionClient(spotClientResponse(t, body), testProvider(inst), clock.NewRealClock(), AccountIDDefault)
			order, err := exec.Submit(context.Background(), req)
			if order != nil || err == nil {
				t.Fatalf("order=%+v err=%v, want ambiguous malformed-success error", order, err)
			}
			if errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("err=%v, malformed success must not claim venue rejection", err)
			}
		})
	}
}

func TestSpotGenerateOrderStatusReportRecoversClientOnlyTerminalOrders(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		executed   string
		cumQuote   string
		wantStatus enums.OrderStatus
	}{
		{name: "fully filled", status: "FILLED", executed: "1", cumQuote: "10", wantStatus: enums.StatusFilled},
		{name: "partially filled IOC expired", status: "EXPIRED", executed: "0.4", cumQuote: "4", wantStatus: enums.StatusExpired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			client := spotClientNoNetwork(t)
			client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				called = true
				if request.Method != http.MethodGet || request.URL.Path != "/api/v3/order" {
					t.Fatalf("unexpected REST call: %s %s", request.Method, request.URL.String())
				}
				query := request.URL.Query()
				if got := query.Get("symbol"); got != "ASTERUSDT" {
					t.Fatalf("symbol=%q, want ASTERUSDT", got)
				}
				if got := query.Get("origClientOrderId"); got != "ambiguous-ioc" {
					t.Fatalf("origClientOrderId=%q, want ambiguous-ioc", got)
				}
				if got := query.Get("orderId"); got != "" {
					t.Fatalf("orderId=%q, want omitted for client-only query", got)
				}
				body := fmt.Sprintf(`{"symbol":"ASTERUSDT","orderId":42,"clientOrderId":"ambiguous-ioc","status":%q,"type":"LIMIT","side":"BUY","timeInForce":"IOC","origQty":"1","price":"10","executedQty":%q,"cumQty":%q,"cumQuote":%q,"avgPrice":"10","updateTime":1700000000000}`, tc.status, tc.executed, tc.executed, tc.cumQuote)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: request}, nil
			})})

			inst := mustSpotInstrument(t)
			exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
			report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
				InstrumentID: inst.ID,
				AccountID:    AccountIDDefault,
				ClientID:     "ambiguous-ioc",
			})
			if err != nil {
				t.Fatalf("GenerateOrderStatusReport: %v", err)
			}
			if !called {
				t.Fatal("GenerateOrderStatusReport did not query the exact order")
			}
			if report == nil {
				t.Fatal("GenerateOrderStatusReport returned nil for terminal order")
			}
			if report.Order.Status != tc.wantStatus || !report.Order.FilledQty.Equal(d(tc.executed)) {
				t.Fatalf("order status/filled=%s/%s, want %s/%s", report.Order.Status, report.Order.FilledQty, tc.wantStatus, tc.executed)
			}
			if report.Order.Request.ClientID != "ambiguous-ioc" || report.Order.VenueOrderID != "42" {
				t.Fatalf("order identity=%q/%q, want ambiguous-ioc/42", report.Order.Request.ClientID, report.Order.VenueOrderID)
			}
		})
	}
}

func TestSpotGenerateOrderStatusReportPrefersVenueOrderID(t *testing.T) {
	client := spotClientNoNetwork(t)
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v3/order" {
			t.Fatalf("unexpected REST call: %s %s", request.Method, request.URL.String())
		}
		query := request.URL.Query()
		if got := query.Get("orderId"); got != "42" {
			t.Fatalf("orderId=%q, want 42", got)
		}
		if got := query.Get("origClientOrderId"); got != "" {
			t.Fatalf("origClientOrderId=%q, want omitted when venue order id is available", got)
		}
		body := `{"symbol":"ASTERUSDT","orderId":42,"clientOrderId":"caller-client","status":"FILLED","type":"LIMIT","side":"BUY","timeInForce":"IOC","origQty":"1","price":"10","executedQty":"1","cumQty":"1","cumQuote":"10","avgPrice":"10","updateTime":1700000000000}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: request}, nil
	})})

	inst := mustSpotInstrument(t)
	exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{
		InstrumentID: inst.ID,
		AccountID:    AccountIDDefault,
		ClientID:     "caller-client",
		VenueOrderID: "42",
	})
	if err != nil {
		t.Fatalf("GenerateOrderStatusReport: %v", err)
	}
	if report == nil || report.Order.VenueOrderID != "42" || report.Order.Request.ClientID != "caller-client" {
		t.Fatalf("report=%+v, want exact venue/client identity", report)
	}
}

func TestSpotGenerateOrderStatusReportRejectsMismatchedResponseIdentity(t *testing.T) {
	tests := []struct {
		name  string
		query model.SingleOrderStatusQuery
		body  string
	}{
		{name: "symbol", query: model.SingleOrderStatusQuery{ClientID: "caller-client"}, body: `{"symbol":"OTHERUSDT","orderId":42,"clientOrderId":"caller-client","status":"NEW","type":"LIMIT","side":"BUY","timeInForce":"GTC","origQty":"1","price":"10","executedQty":"0"}`},
		{name: "client id", query: model.SingleOrderStatusQuery{ClientID: "caller-client"}, body: `{"symbol":"ASTERUSDT","orderId":42,"clientOrderId":"unrelated-client","status":"NEW","type":"LIMIT","side":"BUY","timeInForce":"GTC","origQty":"1","price":"10","executedQty":"0"}`},
		{name: "venue order id", query: model.SingleOrderStatusQuery{VenueOrderID: "42"}, body: `{"symbol":"ASTERUSDT","orderId":99,"clientOrderId":"caller-client","status":"NEW","type":"LIMIT","side":"BUY","timeInForce":"GTC","origQty":"1","price":"10","executedQty":"0"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := spotClientResponse(t, tc.body)
			inst := mustSpotInstrument(t)
			tc.query.InstrumentID = inst.ID
			tc.query.AccountID = AccountIDDefault
			exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
			if report, err := exec.GenerateOrderStatusReport(context.Background(), tc.query); err == nil {
				t.Fatalf("GenerateOrderStatusReport returned report=%+v for mismatched %s", report, tc.name)
			}
		})
	}
}

func TestSpotGenerateOrderStatusReportHandlesNotFoundAndAccountMismatch(t *testing.T) {
	called := false
	client := spotClientNoNetwork(t)
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"code":-2013,"msg":"Order does not exist"}`)), Header: make(http.Header), Request: request}, nil
	})})
	inst := mustSpotInstrument(t)
	exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	report, err := exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{InstrumentID: inst.ID, AccountID: AccountIDDefault, ClientID: "missing"})
	if err != nil || report != nil {
		t.Fatalf("not-found report/error=%+v/%v, want nil/nil", report, err)
	}
	if !called {
		t.Fatal("not-found path did not query the exact order")
	}

	called = false
	report, err = exec.GenerateOrderStatusReport(context.Background(), model.SingleOrderStatusQuery{InstrumentID: inst.ID, AccountID: "OTHER", ClientID: "missing"})
	if err != nil || report != nil {
		t.Fatalf("account-mismatch report/error=%+v/%v, want nil/nil", report, err)
	}
	if called {
		t.Fatal("account mismatch reached REST")
	}
}

func TestSpotCommandErrorMapsOnlyStructuredBusinessRejections(t *testing.T) {
	business := astercommon.NewVenueError(http.StatusBadRequest, http.MethodPost, "/api/v3/order", -2010, "New order rejected")
	if err := mapAsterCommandError(business); !errors.Is(err, contract.ErrVenueRejected) {
		t.Fatalf("business err=%v, want ErrVenueRejected", err)
	}
	for _, err := range []error{
		astercommon.NewVenueError(http.StatusInternalServerError, http.MethodPost, "/api/v3/order", -2010, "internal error"),
		astercommon.NewVenueError(http.StatusBadRequest, http.MethodPost, "/api/v3/order", 0, "malformed"),
		astercommon.NewTransportError(http.MethodPost, "/api/v3/order", context.DeadlineExceeded),
	} {
		if mapped := mapAsterCommandError(err); errors.Is(mapped, contract.ErrVenueRejected) {
			t.Fatalf("ambiguous err=%v mapped as venue rejection: %v", err, mapped)
		}
	}
}

func TestSpotAsterErrorMappingsPreserveOriginalVenueError(t *testing.T) {
	tests := []error{
		astercommon.NewVenueError(http.StatusUnauthorized, http.MethodPost, "/api/v3/order", -2015, "invalid api key"),
		astercommon.NewVenueError(http.StatusTooManyRequests, http.MethodPost, "/api/v3/order", -1003, "too many requests"),
		astercommon.NewVenueError(http.StatusBadRequest, http.MethodPost, "/api/v3/order", -1121, "invalid symbol"),
		astercommon.NewVenueError(http.StatusBadRequest, http.MethodDelete, "/api/v3/order", -2011, "unknown order"),
		astercommon.NewVenueError(http.StatusBadRequest, http.MethodPost, "/api/v3/order", -1013, "invalid precision"),
	}
	for _, original := range tests {
		var want *astercommon.VenueError
		if !errors.As(original, &want) {
			t.Fatalf("fixture %T is not VenueError", original)
		}
		for name, mapError := range map[string]func(error) error{
			"query":   mapAsterError,
			"command": mapAsterCommandError,
		} {
			t.Run(fmt.Sprintf("%s/%d", name, want.Code()), func(t *testing.T) {
				mapped := mapError(original)
				var got *astercommon.VenueError
				if !errors.As(mapped, &got) || got != want {
					t.Fatalf("mapped=%v VenueError=%p, want original %p", mapped, got, want)
				}
			})
		}
	}
}

func TestSpotCancelRequiresExactAuthoritativeSuccessResponse(t *testing.T) {
	inst := mustSpotInstrument(t)
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "exact canceled order", body: `{"symbol":"ASTERUSDT","orderId":42,"clientOrderId":"client-42","status":"CANCELED"}`},
		{name: "empty object", body: `{}`, wantErr: true},
		{name: "zero order id", body: `{"symbol":"ASTERUSDT","orderId":0,"status":"CANCELED"}`, wantErr: true},
		{name: "mismatched order id", body: `{"symbol":"ASTERUSDT","orderId":43,"status":"CANCELED"}`, wantErr: true},
		{name: "mismatched symbol", body: `{"symbol":"OTHERUSDT","orderId":42,"status":"CANCELED"}`, wantErr: true},
		{name: "mismatched status", body: `{"symbol":"ASTERUSDT","orderId":42,"status":"NEW"}`, wantErr: true},
		{name: "lowercase status", body: `{"symbol":"ASTERUSDT","orderId":42,"status":"canceled"}`, wantErr: true},
		{name: "padded status", body: `{"symbol":"ASTERUSDT","orderId":42,"status":" CANCELED "}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := spotClientNoNetwork(t)
			client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header), Request: request}, nil
			})})
			exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
			err := exec.Cancel(context.Background(), inst.ID, "42")
			if (err != nil) != test.wantErr {
				t.Fatalf("Cancel err=%v, wantErr=%v", err, test.wantErr)
			}
			if errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("HTTP-200 response validation err=%v must remain ambiguous", err)
			}
		})
	}
}

func TestSpotCommandOutcomeMatrixUsesVenueRejectedOnlyForDefinitiveBusinessErrors(t *testing.T) {
	inst := mustSpotInstrument(t)
	req := model.OrderRequest{
		AccountID: AccountIDDefault, InstrumentID: inst.ID, ClientID: "command-spot",
		Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC,
		Quantity: d("1.23"), Price: d("10.0000"), PositionSide: enums.PosNet,
	}
	tests := []struct {
		name       string
		statusCode int
		body       string
		transport  error
		definitive bool
	}{
		{name: "business 4xx", statusCode: http.StatusBadRequest, body: `{"code":-2010,"msg":"New order rejected"}`, definitive: true},
		{name: "transport", transport: context.DeadlineExceeded},
		{name: "http 5xx", statusCode: http.StatusInternalServerError, body: `{"code":-2010,"msg":"internal"}`},
		{name: "malformed success", statusCode: http.StatusOK, body: `{not-json`},
	}
	for _, operation := range []string{"submit", "cancel"} {
		for _, test := range tests {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				body := test.body
				if operation == "cancel" && test.definitive {
					body = `{"code":-2011,"msg":"Unknown order"}`
				}
				client := spotClientNoNetwork(t)
				client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					if test.transport != nil {
						return nil, test.transport
					}
					return &http.Response{StatusCode: test.statusCode, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: request}, nil
				})})
				exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
				var err error
				if operation == "submit" {
					_, err = exec.Submit(context.Background(), req)
				} else {
					err = exec.Cancel(context.Background(), inst.ID, "42")
				}
				if err == nil {
					t.Fatal("command unexpectedly succeeded")
				}
				if got := errors.Is(err, contract.ErrVenueRejected); got != test.definitive {
					t.Fatalf("err=%v venueRejected=%v, want %v", err, got, test.definitive)
				}
				if test.transport != nil && !errors.Is(err, test.transport) {
					t.Fatalf("err=%v, want preserved transport cause %v", err, test.transport)
				}
			})
		}
	}
	exec := newExecutionClient(nil, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	if order, err := exec.Modify(context.Background(), inst.ID, "42", d("10.1"), d("1.23")); order != nil || !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("Modify order=%+v err=%v, want ErrNotSupported", order, err)
	}
}

func TestInstrumentConversionUsesExactDecimalIncrements(t *testing.T) {
	inst := mustSpotInstrument(t)
	if inst.ID != (model.InstrumentID{Venue: VenueName, Symbol: "ASTER-USDT", Kind: enums.KindSpot}) {
		t.Fatalf("id=%+v", inst.ID)
	}
	assertDec(t, inst.PriceTick, "0.0001")
	assertDec(t, inst.SizeStep, "0.01")
	assertDec(t, inst.MinQty, "0.01")
	assertDec(t, inst.MinNotional, "5")
	if inst.PositionMode != model.NetOnly || inst.Settle != "USDT" || inst.VenueSymbol != "ASTERUSDT" {
		t.Fatalf("unexpected instrument: %#v", inst)
	}
}

func TestOrderStatusSideTIFAndFeeConversion(t *testing.T) {
	if got, err := sideToAster(enums.SideBuy); err != nil || got != "BUY" {
		t.Fatalf("sideToAster buy=(%q,%v)", got, err)
	}
	if got := sideFromAster("SELL"); got != enums.SideSell {
		t.Fatalf("sideFromAster SELL=%s", got)
	}
	if got, err := orderTypeToAster(enums.TypeLimit, enums.TifGTX); err != nil || got != "LIMIT" {
		t.Fatalf("post-only type=(%q,%v)", got, err)
	}
	if got, err := tifToAster(enums.TifGTX); err != nil || got != "GTX" {
		t.Fatalf("post-only tif=(%q,%v)", got, err)
	}
	if got := tifFromAster("GTX"); got != enums.TifGTX {
		t.Fatalf("post-only tif from Aster=%s", got)
	}
	if got, err := tifToAster(enums.TifIOC); err != nil || got != "IOC" {
		t.Fatalf("tif=(%q,%v)", got, err)
	}
	if got := statusFromAster("PARTIALLY_FILLED"); got != enums.StatusPartiallyFilled {
		t.Fatalf("status=%s", got)
	}
	fill := fillFromTrade(sdkspot.Trade{
		Symbol: "BTCUSDT", ID: 99, OrderID: 42, Side: "BUY", Price: "60000", Qty: "0.001", Commission: "0.02", CommissionAsset: "USDT", Time: 1700000000000, Maker: true,
	}, testSpotID(), AccountIDDefault, "client-a")
	if fill.AccountID != AccountIDDefault || fill.ClientID != "client-a" || fill.VenueOrderID != "42" || fill.TradeID != "99" || fill.Liquidity != enums.LiqMaker {
		t.Fatalf("fill ids/liquidity not converted: %#v", fill)
	}
	assertDec(t, fill.Fee, "0.02")
}

func TestSpotRejectsReduceOnlyAndPositionSide(t *testing.T) {
	inst := mustSpotInstrument(t)
	_, err := orderRequestToAster(model.OrderRequest{InstrumentID: inst.ID, Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC, Quantity: d("1"), Price: d("1"), ReduceOnly: true}, inst)
	if !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("reduce-only err=%v, want ErrNotSupported", err)
	}
	_, err = orderRequestToAster(model.OrderRequest{InstrumentID: inst.ID, Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC, Quantity: d("1"), Price: d("1"), PositionSide: enums.PosLong}, inst)
	if !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("position side err=%v, want ErrNotSupported", err)
	}
}

func TestSpotDiscoveryFailsClosedForMalformedInScopeRows(t *testing.T) {
	profile := mustProfile(t)
	valid := *mustSpotSymbolInfo(t, "ASTERUSDT")

	malformedTick := valid
	malformedTick.Filters = replaceSpotFilterValue(malformedTick.Filters, "PRICE_FILTER", "tickSize", "not-a-decimal")
	if _, err := instrumentFromSymbolInfo(&malformedTick, profile); err == nil {
		t.Fatalf("malformed tick accepted")
	}

	zeroStep := valid
	zeroStep.Filters = replaceSpotFilterValue(zeroStep.Filters, "LOT_SIZE", "stepSize", "0")
	if _, err := instrumentFromSymbolInfo(&zeroStep, profile); err == nil {
		t.Fatalf("zero step accepted")
	}

	testSymbol := valid
	testSymbol.Symbol = "TESTASTERUSDT"
	if inst, err := instrumentFromSymbolInfo(&testSymbol, profile); err != nil || inst != nil {
		t.Fatalf("TEST symbol should be filtered without error, got inst=%#v err=%v", inst, err)
	}

	provider := newInstrumentProvider()
	if err := provider.loadExchangeInfo(&sdkspot.ExchangeInfoResponse{Symbols: []sdkspot.SymbolInfo{malformedTick}}, profile); err == nil {
		t.Fatalf("provider accepted malformed in-scope row")
	}
	if err := provider.loadExchangeInfo(&sdkspot.ExchangeInfoResponse{Symbols: []sdkspot.SymbolInfo{testSymbol}}, profile); err == nil {
		t.Fatalf("provider accepted discovery with no supported instruments")
	}
}

func TestSpotDiscoveryAcceptsMissingTradingAllowedButRejectsExplicitFalse(t *testing.T) {
	profile := mustProfile(t)
	valid := *mustSpotSymbolInfo(t, "ASTERUSDT")

	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, "isSpotTradingAllowed")
	missingRaw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var missing sdkspot.SymbolInfo
	if err := json.Unmarshal(missingRaw, &missing); err != nil {
		t.Fatal(err)
	}
	inst, err := instrumentFromSymbolInfo(&missing, profile)
	if err != nil || inst == nil {
		t.Fatalf("missing isSpotTradingAllowed should retain a TRADING spot symbol, got inst=%#v err=%v", inst, err)
	}

	payload["isSpotTradingAllowed"] = false
	explicitFalseRaw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var explicitFalse sdkspot.SymbolInfo
	if err := json.Unmarshal(explicitFalseRaw, &explicitFalse); err != nil {
		t.Fatal(err)
	}
	inst, err = instrumentFromSymbolInfo(&explicitFalse, profile)
	if err != nil || inst != nil {
		t.Fatalf("explicit false isSpotTradingAllowed should filter the symbol, got inst=%#v err=%v", inst, err)
	}
}

func TestSpotNewValidatesProfileWhenClientInjected(t *testing.T) {
	perpProfile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductPerp)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(context.Background(), Config{Profile: perpProfile, Client: spotClientNoNetwork(t)})
	if err == nil {
		t.Fatalf("New accepted wrong product profile with injected client")
	}
}

func TestSpotNewRejectsInjectedClientFromDifferentEnvironment(t *testing.T) {
	production, err := astercommon.NewProfile(astercommon.EnvironmentProduction, astercommon.ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sdkspot.NewClient(production, testSecurity(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(context.Background(), Config{Profile: mustProfile(t), Client: client})
	if err == nil {
		t.Fatal("New accepted production client under Testnet profile")
	}
}

func TestCapabilitiesAndUnsupportedBehaviorAreTruthful(t *testing.T) {
	market := newMarketDataClient(nil, nil, newInstrumentProvider(), clock.NewRealClock())
	exec := newExecutionClient(nil, newInstrumentProvider(), clock.NewRealClock(), AccountIDDefault)
	acct := newAccountClient(nil, clock.NewRealClock(), AccountIDDefault)
	if len(market.Capabilities().Products) != 1 || !market.Capabilities().Products[0].Market || market.Capabilities().Products[0].Trading || market.Capabilities().Products[0].Account || market.Capabilities().Reports != (contract.ReportCapabilities{}) || market.Capabilities().Streaming.Market {
		t.Fatalf("market capabilities=%#v", market.Capabilities())
	}
	if !exec.Capabilities().Trading.Submit || !exec.Capabilities().Reports.OpenOrders || !exec.Capabilities().Reports.SingleOrderStatus || exec.Capabilities().Streaming.Execution {
		t.Fatalf("exec capabilities=%#v", exec.Capabilities())
	}
	if !acct.Capabilities().Reports.AccountBalanceSnapshots || acct.Capabilities().Reports.PositionReports {
		t.Fatalf("acct capabilities=%#v", acct.Capabilities())
	}
	if err := acct.SetLeverage(context.Background(), testSpotID(), d("2")); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("SetLeverage err=%v, want ErrNotSupported", err)
	}
	if _, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{}); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("GenerateFillReports err=%v, want ErrNotSupported", err)
	}
}

func TestSpotGenerateFillReportsUsesUserTradesAndPreservesAccount(t *testing.T) {
	inst := mustSpotInstrument(t)
	exec := newExecutionClient(spotClientResponse(t, `[{"symbol":"ASTERUSDT","id":99,"orderId":42,"side":"BUY","price":"60000","qty":"0.001","quoteQty":"60","commission":"0.02","commissionAsset":"USDT","time":1700000000000,"maker":true}]`), testProvider(inst), clock.NewRealClock(), "ASTER-CUSTOM")

	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{InstrumentID: inst.ID, AccountID: "ASTER-CUSTOM", ClientID: "caller-client", VenueOrderID: "42"})
	if err != nil {
		t.Fatalf("GenerateFillReports returned error: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports len=%d, want 1", len(reports))
	}
	fill := reports[0].Fill
	if reports[0].Venue != VenueName || reports[0].AccountID != "ASTER-CUSTOM" || fill.AccountID != "ASTER-CUSTOM" || fill.ClientID != "caller-client" || fill.VenueOrderID != "42" || fill.TradeID != "99" {
		t.Fatalf("fill report ids not preserved: %#v", reports[0])
	}
	assertDec(t, fill.Quantity, "0.001")
	assertDec(t, fill.Fee, "0.02")
}

func TestSpotGenerateFillReportsDoesNotFabricateClientID(t *testing.T) {
	inst := mustSpotInstrument(t)
	exec := newExecutionClient(spotClientResponse(t, `[{"symbol":"ASTERUSDT","id":99,"orderId":42,"side":"BUY","price":"60000","qty":"0.001","quoteQty":"60","commission":"0.02","commissionAsset":"USDT","time":1700000000000,"maker":true}]`), testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	reports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{InstrumentID: inst.ID, ClientID: "caller-client"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 0 {
		t.Fatalf("client-id-only query matched venue trades without client evidence: %#v", reports)
	}
}

func TestSpotExecutionMassStatusResyncsOpenOrdersAndFills(t *testing.T) {
	inst := mustSpotInstrument(t)
	client := spotClientSequence(t, map[string]string{
		"/api/v3/openOrders": `[{"symbol":"ASTERUSDT","orderId":42,"clientOrderId":"c-open","status":"NEW","type":"LIMIT","side":"BUY","timeInForce":"GTC","origQty":"1","price":"10","executedQty":"0","cumQty":"0","cumQuote":"0","updateTime":1700000000000}]`,
		"/api/v3/userTrades": `[{"symbol":"ASTERUSDT","id":99,"orderId":42,"side":"BUY","price":"10","qty":"0.5","quoteQty":"5","commission":"0.01","commissionAsset":"USDT","time":1700000001000,"maker":true}]`,
	})
	exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	query := model.MassStatusQuery{AccountID: AccountIDDefault, IncludeFills: true}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus returned error: %v", err)
	}
	if len(mass.OrderReports) != 1 || mass.OrderReports["42"].Order.VenueOrderID != "42" {
		t.Fatalf("order reports=%#v", mass.OrderReports)
	}
	if len(mass.FillReports["42"]) != 1 || mass.FillReports["42"][0].Fill.TradeID != "99" {
		t.Fatalf("fill reports=%#v", mass.FillReports)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed coverage: %v", err)
	}
	if mass.OpenOrdersCoverage.State != model.CoverageComplete || mass.FillsCoverage.State != model.CoverageComplete || mass.PositionsCoverage.State != model.CoverageNotRequested {
		t.Fatalf("coverage=%+v/%+v/%+v", mass.OpenOrdersCoverage, mass.FillsCoverage, mass.PositionsCoverage)
	}
	if open := mass.OpenOrdersCoverage.Scope; open.AccountID != AccountIDDefault || open.ClientID != "" || len(open.InstrumentIDs) != 1 || open.InstrumentIDs[0] != inst.ID || open.Through.IsZero() || !open.From.IsZero() {
		t.Fatalf("open-order coverage scope=%+v, want exact account/instrument snapshot scope", open)
	}
	if fills := mass.FillsCoverage.Scope; fills.AccountID != AccountIDDefault || fills.ClientID != "" || len(fills.InstrumentIDs) != 1 || fills.InstrumentIDs[0] != inst.ID || !fills.From.IsZero() || fills.Through.IsZero() {
		t.Fatalf("fill coverage scope=%+v, want exact account/instrument history scope", fills)
	}
	if !mass.PositionsCoverage.Scope.IsZero() {
		t.Fatalf("not-requested position coverage scope=%+v, want zero", mass.PositionsCoverage.Scope)
	}
}

func TestSpotExecutionMassStatusBoundsFillHistoryAndWarnsOnSaturation(t *testing.T) {
	const fillLimit = 1000
	since := time.UnixMilli(1_700_000_000_000)
	until := since.Add(10 * time.Minute)
	trades := make([]sdkspot.Trade, fillLimit)
	for i := range trades {
		trades[i] = sdkspot.Trade{
			Symbol:          "ASTERUSDT",
			ID:              int64(i + 1),
			OrderID:         42,
			Side:            "BUY",
			Price:           "10",
			Qty:             "0.1",
			QuoteQty:        "1",
			Commission:      "0.001",
			CommissionAsset: "USDT",
			Time:            since.Add(time.Duration(i) * time.Millisecond).UnixMilli(),
		}
	}
	tradeBody, err := json.Marshal(trades)
	if err != nil {
		t.Fatal(err)
	}
	client := spotClientNoNetwork(t)
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := "[]"
		switch request.URL.Path {
		case "/api/v3/openOrders":
		case "/api/v3/userTrades":
			query := request.URL.Query()
			if got := query.Get("startTime"); got != fmt.Sprint(since.UnixMilli()) {
				t.Errorf("userTrades startTime=%q, want %d", got, since.UnixMilli())
			}
			if got := query.Get("endTime"); got != fmt.Sprint(until.UnixMilli()) {
				t.Errorf("userTrades endTime=%q, want %d", got, until.UnixMilli())
			}
			if got := query.Get("limit"); got != fmt.Sprint(fillLimit) {
				t.Errorf("userTrades limit=%q, want %d", got, fillLimit)
			}
			body = string(tradeBody)
		default:
			t.Fatalf("unexpected REST call: %s %s", request.Method, request.URL.String())
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: request}, nil
	})})

	inst := mustSpotInstrument(t)
	exec := newExecutionClient(client, testProvider(inst), clock.NewRealClock(), AccountIDDefault)
	query := model.MassStatusQuery{
		AccountID:    AccountIDDefault,
		Since:        since,
		Until:        until,
		IncludeFills: true,
	}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	totalFills := 0
	for _, reports := range mass.FillReports {
		totalFills += len(reports)
	}
	if totalFills != fillLimit {
		t.Fatalf("fill reports=%d, want bounded page of %d", totalFills, fillLimit)
	}
	if mass.OpenOrdersCoverage.State != model.CoverageComplete || mass.FillsCoverage.State != model.CoveragePartial || mass.PositionsCoverage.State != model.CoverageNotRequested {
		t.Fatalf("fills coverage=%+v, want typed Partial for saturated history", mass.FillsCoverage)
	}
	if fills := mass.FillsCoverage.Scope; fills.AccountID != AccountIDDefault || fills.ClientID != "" || len(fills.InstrumentIDs) != 1 || fills.InstrumentIDs[0] != inst.ID || !fills.From.Equal(since) || !fills.Through.Equal(until) {
		t.Fatalf("fills coverage scope=%+v, want exact [%s,%s] history scope for %s", fills, since, until, inst.ID)
	}
	if !mass.PositionsCoverage.Scope.IsZero() {
		t.Fatalf("not-requested position coverage scope=%+v, want zero", mass.PositionsCoverage.Scope)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
	for _, warning := range mass.Warnings {
		if warning.Code == "FILL_REPORTS_LIMIT_REACHED" {
			return
		}
	}
	t.Fatalf("warnings=%+v, want FILL_REPORTS_LIMIT_REACHED", mass.Warnings)
}

func mustSpotInstrument(t *testing.T) *model.Instrument {
	t.Helper()
	inst, err := instrumentFromSymbolInfo(mustSpotSymbolInfo(t, "ASTERUSDT"), mustProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	if inst == nil {
		t.Fatal("instrumentFromSymbolInfo returned nil")
	}
	return inst
}

func mustSpotSymbolInfo(t *testing.T, symbol string) *sdkspot.SymbolInfo {
	t.Helper()
	var info sdkspot.ExchangeInfoResponse
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "sdk", "aster", "spot", "testdata", "v3", "exchange_info.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	for i := range info.Symbols {
		if info.Symbols[i].Symbol == symbol {
			return &info.Symbols[i]
		}
	}
	t.Fatalf("%s fixture not found", symbol)
	return nil
}

func mustProfile(t *testing.T) astercommon.Profile {
	t.Helper()
	profile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func testSpotID() model.InstrumentID {
	return model.InstrumentID{Venue: VenueName, Symbol: "ASTER-USDT", Kind: enums.KindSpot}
}

func testProvider(insts ...*model.Instrument) *instrumentProvider {
	provider := newInstrumentProvider()
	provider.LoadSnapshot(insts)
	return provider
}

func spotClientNoNetwork(t *testing.T) *sdkspot.Client {
	t.Helper()
	client, err := sdkspot.NewClient(mustProfile(t), testSecurity(t))
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected REST call: %s %s", r.Method, r.URL.String())
		return &http.Response{StatusCode: http.StatusTeapot, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})})
	return client
}

func spotClientResponse(t *testing.T, body string) *sdkspot.Client {
	t.Helper()
	client, err := sdkspot.NewClient(mustProfile(t), testSecurity(t))
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})})
	return client
}

func spotClientSequence(t *testing.T, byPath map[string]string) *sdkspot.Client {
	t.Helper()
	client, err := sdkspot.NewClient(mustProfile(t), testSecurity(t))
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, ok := byPath[r.URL.Path]
		if !ok {
			t.Fatalf("unexpected REST call: %s %s", r.Method, r.URL.String())
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})})
	return client
}

func testSecurity(t *testing.T) *astercommon.SecurityContext {
	t.Helper()
	security, err := astercommon.NewSecurityContext(astercommon.CredentialConfig{
		User:       "0x1111111111111111111111111111111111111111",
		PrivateKey: fmt.Sprintf("%064x", 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	return security
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func withSpot(req model.OrderRequest, mutate func(*model.OrderRequest)) model.OrderRequest {
	mutate(&req)
	return req
}

func replaceSpotFilterValue(filters []sdkspot.SymbolFilter, filterType, field, value string) []sdkspot.SymbolFilter {
	out := append([]sdkspot.SymbolFilter(nil), filters...)
	for i := range out {
		if out[i].FilterType != filterType {
			continue
		}
		switch field {
		case "tickSize":
			out[i].TickSize = value
		case "stepSize":
			out[i].StepSize = value
		case "minQty":
			out[i].MinQty = value
		case "minNotional":
			out[i].MinNotional = value
		}
	}
	return out
}

func d(v string) decimal.Decimal { return decimal.RequireFromString(v) }

func assertDec(t *testing.T, got decimal.Decimal, want string) {
	t.Helper()
	if !got.Equal(d(want)) || got.String() != d(want).String() {
		t.Fatalf("decimal=%s, want %s", got, want)
	}
}
