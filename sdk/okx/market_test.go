package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_GetTickers(t *testing.T) {
	got, err := newLiveClient(t).GetTickers(context.Background(), "SPOT", nil)
	if err != nil {
		t.Fatalf("GetTickers: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected tickers")
	}
}

func TestClient_GetTicker(t *testing.T) {
	got, err := newLiveClient(t).GetTicker(context.Background(), okxSpotInstID)
	if err != nil {
		t.Fatalf("GetTicker: %v", err)
	}
	if len(got) == 0 || got[0].InstId != okxSpotInstID {
		t.Fatalf("unexpected ticker response: %+v", got)
	}
}

func TestClient_GetOrderBook(t *testing.T) {
	size := 5
	got, err := newLiveClient(t).GetOrderBook(context.Background(), okxSpotInstID, &size)
	if err != nil {
		t.Fatalf("GetOrderBook: %v", err)
	}
	if len(got) == 0 || len(got[0].Asks) == 0 || len(got[0].Bids) == 0 {
		t.Fatalf("unexpected order book response: %+v", got)
	}
}

func TestClient_GetInstruments(t *testing.T) {
	got, err := newLiveClient(t).GetInstruments(context.Background(), "SPOT")
	if err != nil {
		t.Fatalf("GetInstruments: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected instruments")
	}
}

func TestClient_GetInstrumentsByFamily(t *testing.T) {
	got, err := newLiveClient(t).GetInstrumentsByFamily(context.Background(), "SWAP", "BTC-USDT")
	if err != nil {
		t.Fatalf("GetInstrumentsByFamily: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected instruments by family")
	}
}

func TestClient_GetSpreadsBuildsPublicQuery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v5/sprd/spreads" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"sprdId":"BTC-USDT_BTC-USDT-SWAP","baseCcy":"BTC","quoteCcy":"USDT","tickSz":"0.1","lotSz":"1","minSz":"1","state":"live","legs":[{"instId":"BTC-USDT","side":"buy","sz":"1"},{"instId":"BTC-USDT-SWAP","side":"sell","sz":"1"}]}]}`))
	}))
	defer srv.Close()

	client := NewClient()
	client.BaseURL = srv.URL
	got, err := client.GetSpreads(context.Background())
	if err != nil {
		t.Fatalf("GetSpreads: %v", err)
	}
	if len(got) != 1 || got[0].SprdId != "BTC-USDT_BTC-USDT-SWAP" || len(got[0].Legs) != 2 {
		t.Fatalf("unexpected spreads response: %+v", got)
	}
}

func TestClient_GetCandles(t *testing.T) {
	bar := "1m"
	limit := 1
	got, err := newLiveClient(t).GetCandles(context.Background(), okxSpotInstID, &bar, nil, nil, &limit)
	if err != nil {
		t.Fatalf("GetCandles: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected candles")
	}
}

func TestClient_GetTrades(t *testing.T) {
	limit := 1
	got, err := newLiveClient(t).GetTrades(context.Background(), okxSpotInstID, &limit)
	if err != nil {
		t.Fatalf("GetTrades: %v", err)
	}
	if len(got) == 0 || got[0].InstId != okxSpotInstID {
		t.Fatalf("unexpected trades response: %+v", got)
	}
}

func TestClient_GetFundingRate(t *testing.T) {
	got, err := newLiveClient(t).GetFundingRate(context.Background(), okxSwapInstID)
	if err != nil {
		t.Fatalf("GetFundingRate: %v", err)
	}
	if got.InstrumentID != okxSwapInstID || got.FundingRate == "" {
		t.Fatalf("unexpected funding rate response: %+v", got)
	}
}

func TestClient_GetFundingRatePreservesRawResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/public/funding-rate" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("instId") != okxSwapInstID {
			t.Fatalf("unexpected instId: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","fundingRate":"0.00040000","nextFundingRate":"0.00050000","fundingTime":"1000","nextFundingTime":"14401000","premium":"0.0001","settFundingRate":"0.0003","settState":"settled","ts":"900"}]}`))
	}))
	defer srv.Close()

	client := NewClient()
	client.BaseURL = srv.URL
	got, err := client.GetFundingRate(context.Background(), okxSwapInstID)
	if err != nil {
		t.Fatalf("GetFundingRate: %v", err)
	}
	if got.InstrumentID != okxSwapInstID || got.Premium != "0.0001" || got.SettFundingRate != "0.0003" || got.Ts != "900" {
		t.Fatalf("expected raw OKX funding payload, got %+v", got)
	}
}

func TestClient_GetMarkPriceBuildsPublicQuery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/public/mark-price" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("instType") != "SWAP" || r.URL.Query().Get("instId") != okxSwapInstID {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","markPx":"43125.5","ts":"1000"}]}`))
	}))
	defer srv.Close()

	client := NewClient()
	client.BaseURL = srv.URL
	got, err := client.GetMarkPrice(context.Background(), "SWAP", okxSwapInstID)
	if err != nil {
		t.Fatalf("GetMarkPrice: %v", err)
	}
	if got.InstId != okxSwapInstID || got.MarkPx != "43125.5" || got.Ts != "1000" {
		t.Fatalf("unexpected mark price response: %+v", got)
	}
}

func TestClient_GetIndexTickerBuildsPublicQuery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/market/index-tickers" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("instId") != "BTC-USDT" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT","idxPx":"43120.25","ts":"1000"}]}`))
	}))
	defer srv.Close()

	client := NewClient()
	client.BaseURL = srv.URL
	got, err := client.GetIndexTicker(context.Background(), "BTC-USDT")
	if err != nil {
		t.Fatalf("GetIndexTicker: %v", err)
	}
	if got.InstId != "BTC-USDT" || got.IdxPx != "43120.25" || got.Ts != "1000" {
		t.Fatalf("unexpected index ticker response: %+v", got)
	}
}

func TestClient_GetOptionSummaryBuildsPublicQuery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/public/opt-summary" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("instFamily") != "BTC-USD" || r.URL.Query().Get("expTime") != "240628" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"OPTION","instId":"BTC-USD-240628-50000-C","instFamily":"BTC-USD","deltaBS":"0.52","gammaBS":"0.01","vegaBS":"10.5","thetaBS":"-1.25","deltaPA":"0.53","gammaPA":"0.011","vegaPA":"10.6","thetaPA":"-1.26","markVol":"0.63","bidVol":"0.61","askVol":"0.65","fwdPx":"43150.5","oi":"125","ts":"1000"}]}`))
	}))
	defer srv.Close()

	client := NewClient()
	client.BaseURL = srv.URL
	got, err := client.GetOptionSummary(context.Background(), "BTC-USD", "240628")
	if err != nil {
		t.Fatalf("GetOptionSummary: %v", err)
	}
	if len(got) != 1 || got[0].InstId != "BTC-USD-240628-50000-C" || got[0].DeltaBS != "0.52" || got[0].FwdPx != "43150.5" {
		t.Fatalf("unexpected option summary response: %+v", got)
	}
}

func TestClient_GetEstimatedPriceBuildsPublicQuery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/public/estimated-price" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("instId") != "BTC-USD-240628" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"FUTURES","instId":"BTC-USD-240628","settlePx":"43150.5","ts":"1000"}]}`))
	}))
	defer srv.Close()

	client := NewClient()
	client.BaseURL = srv.URL
	got, err := client.GetEstimatedPrice(context.Background(), "BTC-USD-240628")
	if err != nil {
		t.Fatalf("GetEstimatedPrice: %v", err)
	}
	if got.InstId != "BTC-USD-240628" || got.SettlePx != "43150.5" || got.Ts != "1000" {
		t.Fatalf("unexpected estimated price response: %+v", got)
	}
}

func TestClient_GetAllFundingRates(t *testing.T) {
	got, err := newLiveClient(t).GetAllFundingRates(context.Background())
	if err != nil {
		t.Fatalf("GetAllFundingRates: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil all funding rates")
	}
}

func TestClient_GetOpenInterest(t *testing.T) {
	got, err := newLiveClient(t).GetOpenInterest(context.Background(), okxSwapInstID)
	if err != nil {
		t.Fatalf("GetOpenInterest: %v", err)
	}
	if got.InstId != okxSwapInstID || got.OI == "" {
		t.Fatalf("unexpected open interest response: %+v", got)
	}
}

func TestClient_GetFundingRateHistory(t *testing.T) {
	got, err := newLiveClient(t).GetFundingRateHistory(context.Background(), okxSwapInstID, 0, 0, 1)
	if err != nil {
		t.Fatalf("GetFundingRateHistory: %v", err)
	}
	if len(got) == 0 || got[0].InstId != okxSwapInstID {
		t.Fatalf("unexpected funding history response: %+v", got)
	}
}

func TestClient_GetHistoryTrades(t *testing.T) {
	got, err := newLiveClient(t).GetHistoryTrades(context.Background(), okxSpotInstID, 1, "", "", 1)
	if err != nil {
		t.Fatalf("GetHistoryTrades: %v", err)
	}
	if len(got) == 0 || got[0].InstId != okxSpotInstID {
		t.Fatalf("unexpected history trades response: %+v", got)
	}
}
