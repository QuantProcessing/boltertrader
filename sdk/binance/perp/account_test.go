package perp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestClient_GetAccount(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccount(context.Background())
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.TotalWalletBalance == "" {
		t.Fatalf("unexpected account response: %+v", got)
	}
}

func TestClient_AccountAndModeEndpointPaths(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v2/account", func(w http.ResponseWriter, r *http.Request) {
		assertSignedGet(t, r)
		_, _ = w.Write([]byte(`{"totalWalletBalance":"1","assets":[],"positions":[]}`))
	})
	mux.HandleFunc("/fapi/v2/balance", func(w http.ResponseWriter, r *http.Request) {
		assertSignedGet(t, r)
		_, _ = w.Write([]byte(`[{"asset":"USDT","balance":"1","availableBalance":"1"}]`))
	})
	mux.HandleFunc("/fapi/v1/positionSide/dual", func(w http.ResponseWriter, r *http.Request) {
		assertSignedGet(t, r)
		_, _ = w.Write([]byte(`{"dualSidePosition":false}`))
	})
	mux.HandleFunc("/fapi/v1/multiAssetsMargin", func(w http.ResponseWriter, r *http.Request) {
		assertSignedGet(t, r)
		_, _ = w.Write([]byte(`{"multiAssetsMargin":false}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewClient().
		WithBaseURL(server.URL).
		WithCredentials("key", "secret").
		WithRateLimiter(nil)
	if _, err := client.GetAccount(context.Background()); err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if _, err := client.GetBalance(context.Background()); err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if _, err := client.GetPositionMode(context.Background()); err != nil {
		t.Fatalf("GetPositionMode: %v", err)
	}
	if _, err := client.GetMultiAssetsMode(context.Background()); err != nil {
		t.Fatalf("GetMultiAssetsMode: %v", err)
	}
}

func assertSignedGet(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Method != http.MethodGet {
		t.Fatalf("method=%s, want GET", r.Method)
	}
	if r.URL.Query().Get("timestamp") == "" || r.URL.Query().Get("signature") == "" {
		t.Fatalf("signed request missing timestamp/signature: %s", r.URL.RawQuery)
	}
}

func TestClient_GetBalance(t *testing.T) {
	got, err := newLivePrivateClient(t).GetBalance(context.Background())
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil balance slice")
	}
}

func TestClient_GetPositionRisk(t *testing.T) {
	got, err := newLivePrivateClient(t).GetPositionRisk(context.Background(), envOrDefault("BINANCE_PERP_TEST_SYMBOL", binancePerpTestSymbol))
	if err != nil {
		t.Fatalf("GetPositionRisk: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil position risk slice")
	}
}

func TestClient_ChangeLeverage(t *testing.T) {
	client := requireBinancePerpLiveWrite(t)
	leverage := 1
	if raw := envOrDefault("BINANCE_PERP_TEST_LEVERAGE", "1"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("parse BINANCE_PERP_TEST_LEVERAGE: %v", err)
		}
		leverage = parsed
	}
	got, err := client.ChangeLeverage(context.Background(), envOrDefault("BINANCE_PERP_TEST_SYMBOL", binancePerpTestSymbol), leverage)
	if err != nil {
		t.Fatalf("ChangeLeverage: %v", err)
	}
	if got.Symbol == "" || got.Leverage != leverage {
		t.Fatalf("unexpected leverage response: %+v", got)
	}
}

func TestClient_ChangeMarginType(t *testing.T) {
	err := requireBinancePerpLiveWrite(t).ChangeMarginType(
		context.Background(),
		envOrDefault("BINANCE_PERP_TEST_SYMBOL", binancePerpTestSymbol),
		envOrDefault("BINANCE_PERP_TEST_MARGIN_TYPE", "CROSSED"),
	)
	if err != nil {
		t.Fatalf("ChangeMarginType: %v", err)
	}
}

func TestClient_GetPositionMode(t *testing.T) {
	got, err := newLivePrivateClient(t).GetPositionMode(context.Background())
	if err != nil {
		t.Fatalf("GetPositionMode: %v", err)
	}
	_ = got.DualSidePosition
}

func TestClient_ChangePositionMode(t *testing.T) {
	if err := requireBinancePerpLiveWrite(t).ChangePositionMode(context.Background(), false); err != nil {
		t.Fatalf("ChangePositionMode: %v", err)
	}
}

func TestClient_GetMultiAssetsMode(t *testing.T) {
	got, err := newLivePrivateClient(t).GetMultiAssetsMode(context.Background())
	if err != nil {
		t.Fatalf("GetMultiAssetsMode: %v", err)
	}
	_ = got.MultiAssetsMargin
}

func TestClient_ChangeMultiAssetsMode(t *testing.T) {
	if err := requireBinancePerpLiveWrite(t).ChangeMultiAssetsMode(context.Background(), false); err != nil {
		t.Fatalf("ChangeMultiAssetsMode: %v", err)
	}
}

func TestClient_GetFeeRate(t *testing.T) {
	got, err := newLivePrivateClient(t).GetFeeRate(context.Background(), envOrDefault("BINANCE_PERP_TEST_SYMBOL", binancePerpTestSymbol))
	if err != nil {
		t.Fatalf("GetFeeRate: %v", err)
	}
	if got.Symbol == "" {
		t.Fatalf("unexpected fee rate response: %+v", got)
	}
}
