package sdk

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestClientGetFuturesOrderByIDUsesStringPathIdentity(t *testing.T) {
	fixed := time.Unix(1700000000, 0)
	client := NewClient().
		WithBaseURL("https://example.test/api/v4").
		WithCredentials("key", "secret").
		WithClock(func() time.Time { return fixed })
	client.WithHTTPClient(testHTTPClient(t, func(req *http.Request) (int, string) {
		if req.Method != http.MethodGet || req.URL.Path != "/api/v4/futures/usdt/orders/t-12345" {
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
		}
		if req.URL.RawQuery != "" {
			t.Fatalf("unexpected query %q", req.URL.RawQuery)
		}
		wantPayload := buildSigningPayload(http.MethodGet, req.URL.Path, req.URL.RawQuery, "", "1700000000")
		if got, want := req.Header.Get("KEY"), "key"; got != want {
			t.Fatalf("KEY=%q want %q", got, want)
		}
		if got, want := req.Header.Get("SIGN"), sign("secret", wantPayload); got != want {
			t.Fatalf("SIGN=%q want %q", got, want)
		}
		return http.StatusOK, `{"id":123,"contract":"ETH_USDT","text":"t-12345","size":1,"left":1,"price":"1000","status":"open"}`
	}))

	order, err := client.GetFuturesOrderByID(context.Background(), SettleUSDT, "t-12345")
	if err != nil {
		t.Fatal(err)
	}
	if order.ID != 123 || order.Text != "t-12345" || order.Contract != "ETH_USDT" {
		t.Fatalf("unexpected order: %+v", order)
	}
}
