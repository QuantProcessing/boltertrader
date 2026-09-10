package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/order"
)

func TestOrderLookupAuthenticatesAndReturnsOriginalOrderScope(t *testing.T) {
	fixture := newHTTPFixture(t)
	fixture.orders.getResult = order.Result{CommandID: "018f0000-0000-7000-8000-000000000001", GatewayOrderID: "018f0000-0000-7000-8000-000000000002",
		ClientOrderID: "1", AccountID: "acct-a", InstrumentID: "binance:live:spot:BTC-USDT", State: domain.StateOpen, ResourceVersion: 2}
	path := "/v1/orders/by-idempotency-key/command_original"
	unauthorized := httptest.NewRecorder()
	fixture.handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path, nil))
	if unauthorized.Code != http.StatusUnauthorized || fixture.orders.getCalls != 0 {
		t.Fatalf("unauthorized status=%d calls=%d", unauthorized.Code, fixture.orders.getCalls)
	}
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	var response orderLookupResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != 200 || response.Order.GatewayOrderID != fixture.orders.getResult.GatewayOrderID || response.Order.CommandID != fixture.orders.getResult.CommandID || response.Order.ResourceVersion != 2 || response.AccountID != "acct-a" || response.InstrumentID != fixture.orders.getResult.InstrumentID || response.IdempotencyKey != "command_original" {
		t.Fatalf("response=%s", recorder.Body.String())
	}
	if fixture.orders.getPrincipal == "" || fixture.orders.getIdempotencyKey != "command_original" || fixture.orders.placeCalls != 0 {
		t.Fatalf("lookup calls=%+v", fixture.orders)
	}
	fixture.orders.getResult = order.Result{}
	recorder = httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != 404 {
		t.Fatalf("not found status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
