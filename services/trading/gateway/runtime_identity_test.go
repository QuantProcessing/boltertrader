package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gatewayhttp "github.com/QuantProcessing/boltertrader/gateway/httpapi"
)

func TestRuntimeProjectionNeverInventsIdentityFromLegacyCounters(t *testing.T) {
	for _, missing := range []string{"accountId", "runtimeId", "observedAt"} {
		t.Run(missing, func(t *testing.T) {
			authority := new(adapterContractAuthority)
			response, err := authority.Execute(context.Background(), gatewayhttp.AuthorityRequest{Operation: gatewayhttp.AuthorityGetRuntimeAccount, AccountID: "account-a"})
			if err != nil {
				t.Fatal(err)
			}
			body := response.Body.(map[string]any)
			delete(body, missing)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewEncoder(w).Encode(map[string]any{"data": body}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			client, err := NewClient(server.URL, false, []byte("gateway-adapter-token-with-at-least-32-bytes"), false, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if items, err := client.ListAccountRuntimes(context.Background(), "principal-a", "account-a"); err == nil || len(items) != 0 {
				t.Fatalf("incomplete runtime accepted: %+v err=%v", items, err)
			}
		})
	}
}
