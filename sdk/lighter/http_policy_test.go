package lighter

import (
	"context"
	"github.com/QuantProcessing/boltertrader/internal/venuehttp"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type policyTestTransport func(*http.Request) (*http.Response, error)

func (fn policyTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestSDKTransactionTypeIdentifiesExactBlockedCommand(t *testing.T) {
	for txType, action := range map[int]string{TxTypeCreateOrder: "place", TxTypeCancelOrder: "cancel", TxTypeCancelAllOrders: "cancel", 99: ""} {
		c := NewClient()
		c.HTTPClient = &http.Client{Transport: policyTestTransport(func(r *http.Request) (*http.Response, error) {
			if p := venuehttp.PolicyForRequest(r); p.Action != action {
				t.Fatalf("type=%d policy=%+v", txType, p)
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "SIGNED_TX") {
				t.Fatal("policy consumed signed body")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		})}
		if _, err := c.PostForm(context.Background(), "/api/v1/sendTx", map[string]string{"tx_type": strconv.Itoa(txType), "tx_info": "SIGNED_TX"}, false); err != nil {
			t.Fatal(err)
		}
	}
}
