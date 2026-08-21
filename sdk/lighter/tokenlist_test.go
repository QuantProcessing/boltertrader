package lighter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientGetListedTokensUsesPublicTokenlist(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/tokenlist" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "" {
			t.Fatal("public tokenlist request carried authorization")
		}
		_, _ = w.Write([]byte(`{"code":200,"tokens":[{"symbol":"TSLA","name":"Tesla","logo":"tsla","logo_extension":"svg","description_key":"tsla","gecko_id":"","paprika_id":"","market":"PERPS","asset_type":"RWA","categories":["STOCK"],"is_allowed_mainnet":true,"is_asset_allowed_mainnet":true}]}`))
	}))
	defer server.Close()

	client := NewClient().WithBaseURL(server.URL)
	result, err := client.GetListedTokens(context.Background())
	if err != nil {
		t.Fatalf("GetListedTokens: %v", err)
	}
	if len(result.Tokens) != 1 || result.Tokens[0].Symbol != "TSLA" || result.Tokens[0].AssetType != "RWA" || len(result.Tokens[0].Categories) != 1 {
		t.Fatalf("result = %+v", result)
	}
}
