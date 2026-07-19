package okx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestClient_GetAccountBalance(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccountBalance(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil balance slice")
	}
}

func TestClient_GetPositions(t *testing.T) {
	instType := "SWAP"
	got, err := newLivePrivateClient(t).GetPositions(context.Background(), &instType, nil)
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil positions slice")
	}
}

func TestClient_GetAccountConfig(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccountConfig(context.Background())
	if err != nil {
		t.Fatalf("GetAccountConfig: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected account config")
	}
}

func TestClient_AccountEndpointPaths(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v5/account/balance", func(w http.ResponseWriter, r *http.Request) {
		assertSignedOKXGet(t, r)
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"details":[{"ccy":"USDT","eq":"1"}]}]}`))
	})
	mux.HandleFunc("/api/v5/account/positions", func(w http.ResponseWriter, r *http.Request) {
		assertSignedOKXGet(t, r)
		if got := r.URL.Query().Get("instType"); got != "SWAP" {
			t.Fatalf("instType=%q, want SWAP", got)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
	})
	mux.HandleFunc("/api/v5/account/config", func(w http.ResponseWriter, r *http.Request) {
		assertSignedOKXGet(t, r)
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"acctLv":"1","posMode":"net_mode"}]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithBaseURL(server.URL)
	if _, err := client.GetAccountBalance(context.Background(), nil); err != nil {
		t.Fatalf("GetAccountBalance: %v", err)
	}
	instType := "SWAP"
	if _, err := client.GetPositions(context.Background(), &instType, nil); err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if _, err := client.GetAccountConfig(context.Background()); err != nil {
		t.Fatalf("GetAccountConfig: %v", err)
	}
}

func assertSignedOKXGet(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Method != http.MethodGet {
		t.Fatalf("method=%s, want GET", r.Method)
	}
	for _, header := range []string{"OK-ACCESS-KEY", "OK-ACCESS-SIGN", "OK-ACCESS-TIMESTAMP", "OK-ACCESS-PASSPHRASE"} {
		if r.Header.Get(header) == "" {
			t.Fatalf("missing %s header", header)
		}
	}
}

func TestClient_SetPositionMode(t *testing.T) {
	got, err := requireOKXLiveWrite(t).SetPositionMode(context.Background(), okxEnvOrDefault("OKX_TEST_POSITION_MODE", "net_mode"))
	if err != nil {
		t.Fatalf("SetPositionMode: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil position mode response")
	}
}

func TestClient_SetPositionModeSendsPosModePayload(t *testing.T) {
	t.Parallel()

	const wantPosMode = "long_short_mode"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/account/set-position-mode" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["posMode"] != wantPosMode {
			t.Fatalf("unexpected position mode payload: %+v", req)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"posMode":"long_short_mode"}]}`))
	}))
	defer srv.Close()

	client := NewClient().WithCredentials("key", "secret", "pass")
	client.BaseURL = srv.URL
	got, err := client.SetPositionMode(context.Background(), wantPosMode)
	if err != nil {
		t.Fatalf("SetPositionMode: %v", err)
	}
	if len(got) != 1 || got[0].PosMode != wantPosMode {
		t.Fatalf("unexpected position mode response: %+v", got)
	}
}

func TestClient_SetLeverage(t *testing.T) {
	leverage, err := strconv.Atoi(okxEnvOrDefault("OKX_TEST_LEVERAGE", "1"))
	if err != nil {
		t.Fatalf("parse OKX_TEST_LEVERAGE: %v", err)
	}
	got, err := requireOKXLiveWrite(t).SetLeverage(context.Background(), SetLeverage{
		InstId:  okxEnvOrDefault("OKX_TEST_LEVERAGE_INST_ID", okxSwapInstID),
		Lever:   leverage,
		MgnMode: okxEnvOrDefault("OKX_TEST_MARGIN_MODE", "cross"),
	})
	if err != nil {
		t.Fatalf("SetLeverage: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil leverage response")
	}
}

func TestClientSetLeverageAcceptsStringLeverResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v5/account/set-leverage" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["lever"] != float64(1) {
			t.Fatalf("request lever = %#v, want numeric 1", payload["lever"])
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"ETH-USDT-SWAP","lever":"1","mgnMode":"cross","posSide":""}]}`))
	}))
	defer server.Close()

	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithBaseURL(server.URL)
	rows, err := client.SetLeverage(context.Background(), SetLeverage{
		InstId:  "ETH-USDT-SWAP",
		Lever:   1,
		MgnMode: "cross",
	})
	if err != nil {
		t.Fatalf("SetLeverage string lever response: %v", err)
	}
	if len(rows) != 1 || rows[0].Lever != 1 {
		t.Fatalf("SetLeverage response = %+v, want effective leverage 1", rows)
	}
}

func TestClient_GetTradeFee(t *testing.T) {
	got, err := newLivePrivateClient(t).GetTradeFee(context.Background(), "SPOT", nil)
	if err != nil {
		t.Fatalf("GetTradeFee: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil fee response")
	}
}
