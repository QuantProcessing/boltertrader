package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
)

func TestRecoveryWithoutResponseOnlyReadsOriginalPlacementKey(t *testing.T) {
	for _, state := range []orders.State{orders.StateAccepted, orders.StateSubmitted, orders.StateUncertain} {
		for _, scenario := range []string{"found", "not_found", "wrong_key", "wrong_account", "wrong_instrument", "dispatching", "future_state"} {
			t.Run(string(state)+"/"+scenario, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.Method != http.MethodGet || r.URL.Path != "/v1/orders/by-idempotency-key/local-command" {
						t.Errorf("recovery attempted %s %s", r.Method, r.URL.Path)
					}
					if scenario == "not_found" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					key, account, instrument, venueState := "local-command", "account-a", "BTC-USDT", "open"
					switch scenario {
					case "wrong_key":
						key = "different"
					case "wrong_account":
						account = "different"
					case "wrong_instrument":
						instrument = "different"
					case "dispatching", "future_state":
						venueState = scenario
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"idempotency_key": key, "account_id": account, "instrument_id": instrument,
						"order": wire.Order{AccountID: account, InstrumentID: instrument, CommandID: "gc", GatewayOrderID: "go", ClientOrderID: "client", State: venueState, ResourceVersion: 2}})
				}))
				defer server.Close()
				// Read-only recovery works even when new writes are disabled.
				client, err := NewClient(server.URL, false, []byte(strings.Repeat("x", 32)), false, server.Client())
				if err != nil {
					t.Fatal(err)
				}
				command := orders.Command{CommandID: "local-command", IdempotencyKey: "browser-key", AccountID: "account-a", State: state,
					Payload: []byte(`{"instrumentId":"BTC-USDT","leg":"long","counterAccountId":"b"}`)}
				result, err := client.Reconcile(context.Background(), command)
				if calls != 1 {
					t.Fatalf("requests=%d", calls)
				}
				if scenario == "found" {
					if err != nil || result.Outcome != orders.HandoffAccepted || result.Evidence.GatewayOrderID != "go" {
						t.Fatalf("result=%+v err=%v", result, err)
					}
				} else if result.Outcome != orders.HandoffOutcomeUnknown {
					t.Fatalf("ambiguous lookup=%+v err=%v", result, err)
				}
			})
		}
	}
}
