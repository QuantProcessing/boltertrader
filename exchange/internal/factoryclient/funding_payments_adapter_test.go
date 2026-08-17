package factoryclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestBinanceFundingPaymentsMapsSignedIncomeAndContinuation(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/fapi/v1/income" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("incomeType") != "FUNDING_FEE" || query.Get("symbol") != "BTCUSDT" ||
			query.Get("startTime") != strconv.FormatInt(start.UnixMilli(), 10) ||
			query.Get("endTime") != strconv.FormatInt(end.UnixMilli()-1, 10) ||
			query.Get("limit") != "1" || query.Get("signature") == "" || query.Get("timestamp") == "" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		switch query.Get("page") {
		case "1":
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","incomeType":"FUNDING_FEE","income":"-0.125","asset":"USDT","time":1782864001000,"tranId":123,"tradeId":"456","info":"funding fee"}]`))
		case "2":
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Fatalf("page=%s", query.Get("page"))
		}
	}))
	defer server.Close()

	client := NewBinanceUSDPerp("key-one", "secret-one", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	instrument := binanceFundingPaymentInstrument()
	page, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 1, SettlementAsset: "USDT",
	})
	if err != nil {
		t.Fatalf("FundingPayments first page: %v", err)
	}
	if page.Status != exchange.FundingPaymentsIncomplete || page.Page.Cursor == "" || !page.Page.HasMore || len(page.Items) != 1 {
		t.Fatalf("first page=%+v", page)
	}
	item := page.Items[0]
	if item.Amount.String() != "-0.125" || item.SourceRef != "binance:fapi:v1:income:FUNDING_FEE:123" ||
		item.NativeSymbol != "BTCUSDT" || item.SettlementAsset != "USDT" || item.FactID == "" {
		t.Fatalf("item=%+v", item)
	}
	terminal, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 1, SettlementAsset: "USDT", Cursor: page.Page.Cursor,
	})
	if err != nil {
		t.Fatalf("FundingPayments terminal page: %v", err)
	}
	if terminal.Status != exchange.FundingPaymentsComplete || terminal.Page.Cursor != "" || terminal.Page.HasMore || len(terminal.Items) != 0 {
		t.Fatalf("terminal page=%+v", terminal)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestBinanceFundingPaymentsAllInstrumentEmptyAndCursorRejection(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") != "" {
			t.Fatalf("all-instrument request unexpectedly sent symbol: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := NewBinanceUSDPerp("key-one", "secret-one", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	page, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Start: start, End: end, Limit: 100, SettlementAsset: "USDT",
	})
	if err != nil {
		t.Fatalf("FundingPayments: %v", err)
	}
	if page.Status != exchange.FundingPaymentsCompleteEmpty || len(page.Items) != 0 {
		t.Fatalf("empty page=%+v", page)
	}
	_, err = client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Start: start, End: end, Limit: 100, SettlementAsset: "USDT", Cursor: "2",
	})
	if !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("raw cursor err=%v", err)
	}
}

func TestBinanceFundingPaymentsFutureEndIsIncompleteUnverified(t *testing.T) {
	start := time.Now().UTC().Add(-time.Hour)
	end := time.Now().UTC().Add(24 * time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := NewBinanceUSDPerp("key-one", "secret-one", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	page, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Start: start, End: end, Limit: 100, SettlementAsset: "USDT",
	})
	if err != nil {
		t.Fatalf("FundingPayments: %v", err)
	}
	if page.Status != exchange.FundingPaymentsIncomplete || page.Watermark.Verified ||
		!page.CompleteThrough.IsZero() || !page.Watermark.CompleteThrough.IsZero() || !page.Watermark.EffectiveThrough.IsZero() {
		t.Fatalf("future-ended page claimed completeness: %+v", page)
	}
}

func TestBinanceFundingPaymentsRejectsSettlementMismatch(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","incomeType":"FUNDING_FEE","income":"0.1","asset":"USDC","time":1782864001000,"tranId":123}]`))
	}))
	defer server.Close()

	client := NewBinanceUSDPerp("key-one", "secret-one", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	instrument := binanceFundingPaymentInstrument()
	_, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 100, SettlementAsset: "USDT",
	})
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("settlement mismatch err=%v", err)
	}
}

func TestLighterFundingPaymentsMapsQueryAndContinuation(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/orderBookDetails":
			_, _ = w.Write([]byte(`{"code":200,"message":"","order_book_details":[{"symbol":"BTC","market_id":2,"market_type":"perp","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":1,"supported_quote_decimals":6}]}`))
		case "/api/v1/positionFunding":
			requests++
			query := r.URL.Query()
			if query.Get("account_index") != "7" || query.Get("limit") != "1" || query.Get("side") != "all" ||
				query.Get("market_ids") != "2" || query.Get("start_timestamp") != strconv.FormatInt(start.Unix(), 10) ||
				query.Get("end_timestamp") != strconv.FormatInt(end.Unix(), 10) {
				t.Fatalf("query=%s", r.URL.RawQuery)
			}
			if requests == 1 {
				_, _ = w.Write([]byte(`{"code":200,"message":"","next_cursor":"native-next","position_fundings":[{"timestamp":1782864001,"market_id":2,"funding_id":99,"change":"0.25","rate":"0.001","position_size":"1","position_side":"long","discount":"0"}]}`))
				return
			}
			if query.Get("cursor") != "native-next" {
				t.Fatalf("cursor=%s", query.Get("cursor"))
			}
			_, _ = w.Write([]byte(`{"code":200,"message":"","position_fundings":[]}`))
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewLighterPerp(openAPILighterPrivateKey, 7, 2, Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	instrument := lighterFundingPaymentInstrument()
	page, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 1, SettlementAsset: "USDC",
	})
	if err != nil {
		t.Fatalf("FundingPayments first page: %v", err)
	}
	if page.Status != exchange.FundingPaymentsIncomplete || page.Page.Cursor == "" || len(page.Items) != 1 {
		t.Fatalf("first page=%+v", page)
	}
	if item := page.Items[0]; item.Amount.String() != "0.25" || item.SourceRef != "lighter:positionFunding:99" ||
		item.NativeID != "2" || item.SettlementAsset != "USDC" {
		t.Fatalf("item=%+v", item)
	}
	terminal, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 1, SettlementAsset: "USDC", Cursor: page.Page.Cursor,
	})
	if err != nil {
		t.Fatalf("FundingPayments terminal page: %v", err)
	}
	if terminal.Status != exchange.FundingPaymentsComplete || terminal.Page.Cursor != "" {
		t.Fatalf("terminal page=%+v", terminal)
	}
}

func TestLighterFundingPaymentsAllInstrumentEmptyAndCursorRejection(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/orderBookDetails":
			_, _ = w.Write([]byte(`{"code":200,"message":"","order_book_details":[{"symbol":"BTC","market_id":2,"market_type":"perp","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":1,"supported_quote_decimals":6}]}`))
		case "/api/v1/positionFunding":
			if r.URL.Query().Get("market_ids") != "" {
				t.Fatalf("all-instrument request unexpectedly sent market_ids: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":200,"message":"","position_fundings":[]}`))
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewLighterPerp(openAPILighterPrivateKey, 7, 2, Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	page, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Start: start, End: end, Limit: 100, SettlementAsset: "USDC",
	})
	if err != nil {
		t.Fatalf("FundingPayments: %v", err)
	}
	if page.Status != exchange.FundingPaymentsCompleteEmpty {
		t.Fatalf("empty page=%+v", page)
	}
	_, err = client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Start: start, End: end, Limit: 100, SettlementAsset: "USDC", Cursor: "2",
	})
	if !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("raw cursor err=%v", err)
	}
}

func TestLighterFundingPaymentsFutureEndIsIncompleteUnverified(t *testing.T) {
	start := time.Now().UTC().Add(-time.Hour)
	end := time.Now().UTC().Add(24 * time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/orderBookDetails":
			_, _ = w.Write([]byte(`{"code":200,"message":"","order_book_details":[{"symbol":"BTC","market_id":2,"market_type":"perp","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":1,"supported_quote_decimals":6}]}`))
		case "/api/v1/positionFunding":
			_, _ = w.Write([]byte(`{"code":200,"message":"","position_fundings":[]}`))
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewLighterPerp(openAPILighterPrivateKey, 7, 2, Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	page, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Start: start, End: end, Limit: 100, SettlementAsset: "USDC",
	})
	if err != nil {
		t.Fatalf("FundingPayments: %v", err)
	}
	if page.Status != exchange.FundingPaymentsIncomplete || page.Watermark.Verified ||
		!page.CompleteThrough.IsZero() || !page.Watermark.CompleteThrough.IsZero() || !page.Watermark.EffectiveThrough.IsZero() {
		t.Fatalf("future-ended page claimed completeness: %+v", page)
	}
}

func TestLighterFundingPaymentsRejectsSettlementMismatch(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/orderBookDetails":
			_, _ = w.Write([]byte(`{"code":200,"message":"","order_book_details":[{"symbol":"BTC","market_id":2,"market_type":"perp","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":1,"supported_quote_decimals":6}]}`))
		case "/api/v1/positionFunding":
			_, _ = w.Write([]byte(`{"code":200,"message":"","position_fundings":[{"timestamp":1782864001,"market_id":2,"funding_id":99,"change":"0.25"}]}`))
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewLighterPerp(openAPILighterPrivateKey, 7, 2, Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	instrument := lighterFundingPaymentInstrument()
	_, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 100, SettlementAsset: "USDT",
	})
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("settlement mismatch err=%v", err)
	}
}

func TestGateFundingPaymentsMapsAccountBookFundRowsAndContinuation(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/futures/usdt/account_book" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("contract") != "BTC_USDT" || query.Get("type") != "fund" ||
			query.Get("from") != strconv.FormatInt(start.Unix(), 10) ||
			query.Get("to") != strconv.FormatInt(end.Unix(), 10) || query.Get("limit") != "1" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		switch query.Get("offset") {
		case "":
			_, _ = w.Write([]byte(`[{"time":1782864001.123,"change":"-0.125","balance":"999.875","text":"funding fee","type":"fund","contract":"BTC_USDT","trade_id":"456","id":"789"}]`))
		case "1":
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Fatalf("offset=%s", query.Get("offset"))
		}
	}))
	defer server.Close()

	client, ok := NewGateUSDTPerp("key-one", "secret-one", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	if !ok {
		t.Fatal("Gate USDT perp does not expose PerpFundingPaymentREST")
	}
	instrument := gateFundingPaymentInstrument()
	page, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 1, SettlementAsset: "USDT",
	})
	if err != nil {
		t.Fatalf("FundingPayments first page: %v", err)
	}
	if page.Status != exchange.FundingPaymentsIncomplete || page.Page.Cursor == "" || !page.Page.HasMore || len(page.Items) != 1 {
		t.Fatalf("first page=%+v", page)
	}
	item := page.Items[0]
	if item.Amount.String() != "-0.125" || item.PaymentID != "789" || item.SourceRef != "gate:futures:account_book:fund:789" ||
		item.NativeSymbol != "BTC_USDT" || item.NativeID != "BTC_USDT" || item.SettlementAsset != "USDT" ||
		!item.EffectiveAt.Equal(time.Unix(1782864001, 123000000).UTC()) || item.FactID == "" {
		t.Fatalf("item=%+v", item)
	}
	terminal, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 1, SettlementAsset: "USDT", Cursor: page.Page.Cursor,
	})
	if err != nil {
		t.Fatalf("FundingPayments terminal page: %v", err)
	}
	if terminal.Status != exchange.FundingPaymentsIncomplete || terminal.Watermark.Verified ||
		!terminal.CompleteThrough.IsZero() || terminal.Page.Cursor != "" || terminal.Page.HasMore || len(terminal.Items) != 0 {
		t.Fatalf("terminal page=%+v", terminal)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestGateFundingPaymentsRejectsMalformedRows(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	tests := []struct {
		name string
		body string
	}{
		{"non fund type", `[{"time":1782864001,"change":"-0.125","type":"dnw","contract":"BTC_USDT","id":"1"}]`},
		{"instrument mismatch", `[{"time":1782864001,"change":"-0.125","type":"fund","contract":"ETH_USDT","id":"1"}]`},
		{"bad amount", `[{"time":1782864001,"change":"bad","type":"fund","contract":"BTC_USDT","id":"1"}]`},
		{"missing id", `[{"time":1782864001,"change":"-0.125","type":"fund","contract":"BTC_USDT"}]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/futures/usdt/account_book" {
					t.Fatalf("path=%s", r.URL.Path)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client := NewGateUSDTPerp("key-one", "secret-one", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
			instrument := gateFundingPaymentInstrument()
			_, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
				Instrument: &instrument, Start: start, End: end, Limit: 100, SettlementAsset: "USDT",
			})
			if !errors.Is(err, exchange.ErrMalformedResponse) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestGateFundingPaymentsAllInstrumentEmptyAndCursorRejection(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("contract") != "" {
			t.Fatalf("all-instrument request unexpectedly sent contract: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := NewGateUSDTPerp("key-one", "secret-one", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	page, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Start: start, End: end, Limit: 100, SettlementAsset: "USDT",
	})
	if err != nil {
		t.Fatalf("FundingPayments: %v", err)
	}
	if page.Status != exchange.FundingPaymentsIncomplete || page.Watermark.Verified ||
		!page.CompleteThrough.IsZero() || len(page.Items) != 0 {
		t.Fatalf("empty page=%+v", page)
	}
	_, err = client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Start: start, End: end, Limit: 100, SettlementAsset: "USDT", Cursor: "1",
	})
	if !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("raw cursor err=%v", err)
	}
}

func TestGateFundingPaymentsRejectsNonUSDTSettlement(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	client := NewGateUSDTPerp("key-one", "secret-one", Settings{Endpoint: "https://example.test"}).(exchange.PerpFundingPaymentREST)
	instrument := gateFundingPaymentInstrument()
	_, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 100, SettlementAsset: "USDC",
	})
	if !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("settlement err=%v", err)
	}
}

func TestBybitFundingPaymentsMapsSettlementFundingAndContinuation(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v5/account/transaction-log" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("accountType") != "UNIFIED" || query.Get("category") != "linear" ||
			query.Get("currency") != "USDT" || query.Get("baseCoin") != "BTC" ||
			query.Get("type") != "SETTLEMENT" ||
			query.Get("startTime") != strconv.FormatInt(start.UnixMilli(), 10) ||
			query.Get("endTime") != strconv.FormatInt(end.UnixMilli(), 10) ||
			query.Get("limit") != "1" || r.Header.Get("X-BAPI-SIGN") == "" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		switch requests {
		case 1:
			if query.Get("cursor") != "" {
				t.Fatalf("first page cursor=%q", query.Get("cursor"))
			}
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"native-next","list":[{"id":"pay-1","symbol":"BTCUSDT","category":"linear","side":"Buy","transactionTime":"1782864001000","type":"SETTLEMENT","currency":"USDT","funding":"-0.125","feeRate":"0.0001","tradeId":"trade-1","orderId":"order-1"}]},"time":1782864002000}`))
		case 2:
			if query.Get("cursor") != "native-next" {
				t.Fatalf("second page cursor=%q", query.Get("cursor"))
			}
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"","list":[]},"time":1782864003000}`))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()

	client := NewBybitLinearPerp("key-one", "secret-one", "USDT", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	instrument := bybitFundingPaymentInstrument()
	page, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 1, SettlementAsset: "USDT",
	})
	if err != nil {
		t.Fatalf("FundingPayments first page: %v", err)
	}
	if page.Status != exchange.FundingPaymentsIncomplete || page.Page.Cursor == "" || !page.Page.HasMore || len(page.Items) != 1 {
		t.Fatalf("first page=%+v", page)
	}
	if page.Page.Cursor == "native-next" || page.Items[0].Amount.String() != "-0.125" || page.Items[0].PaymentID != "pay-1" ||
		page.Items[0].NativeSymbol != "BTCUSDT" || page.Items[0].NativeID != "native-BTCUSDT" || page.Items[0].SettlementAsset != "USDT" ||
		page.Items[0].SourceRef != "bybit:v5:account:transaction-log:SETTLEMENT:pay-1" || page.Items[0].FormulaVersion != "actual-funding-payment-v1" ||
		page.Items[0].FactID == "" || !page.CompleteThrough.IsZero() {
		t.Fatalf("item/page=%+v", page)
	}

	terminal, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 1, SettlementAsset: "USDT", Cursor: page.Page.Cursor,
	})
	if err != nil {
		t.Fatalf("FundingPayments terminal page: %v", err)
	}
	if terminal.Status != exchange.FundingPaymentsComplete || terminal.Page.Cursor != "" || terminal.Page.HasMore || !terminal.Watermark.Verified || !terminal.CompleteThrough.Equal(end) {
		t.Fatalf("terminal page=%+v", terminal)
	}
}

func TestBybitFundingPaymentsSplitsThirtyDayWindowIntoNativeSlices(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * 24 * time.Hour)
	requests := 0
	seenPayments := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v5/account/transaction-log" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("cursor") != "" {
			t.Fatalf("unexpected native cursor while traversing slices: %s", r.URL.RawQuery)
		}
		startMillis, err := strconv.ParseInt(query.Get("startTime"), 10, 64)
		if err != nil {
			t.Fatalf("startTime=%q", query.Get("startTime"))
		}
		endMillis, err := strconv.ParseInt(query.Get("endTime"), 10, 64)
		if err != nil {
			t.Fatalf("endTime=%q", query.Get("endTime"))
		}
		if endMillis-startMillis > int64((7*24*time.Hour)/time.Millisecond) {
			t.Fatalf("native bybit window exceeded seven days: %s", r.URL.RawQuery)
		}
		paymentID := "pay-" + strconv.Itoa(requests)
		seenPayments[paymentID] = true
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"","list":[{"id":"` + paymentID + `","symbol":"BTCUSDT","category":"linear","transactionTime":"` + strconv.FormatInt(startMillis+1000, 10) + `","type":"SETTLEMENT","currency":"USDT","funding":"0.01"}]},"time":1782864002000}`))
	}))
	defer server.Close()

	client := NewBybitLinearPerp("key-one", "secret-one", "USDT", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	instrument := bybitFundingPaymentInstrument()
	page, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 50, SettlementAsset: "USDT",
	})
	if err != nil {
		t.Fatalf("FundingPayments: %v", err)
	}
	if page.Status != exchange.FundingPaymentsComplete || page.Page.Cursor != "" || page.Page.HasMore || !page.Watermark.Verified || !page.CompleteThrough.Equal(end) {
		t.Fatalf("page did not complete original thirty-day window: %+v", page)
	}
	if requests != 5 || len(page.Items) != 5 {
		t.Fatalf("requests/items=%d/%d, want five native slices and five payments", requests, len(page.Items))
	}
	for _, item := range page.Items {
		if !seenPayments[item.PaymentID] {
			t.Fatalf("unexpected or duplicated payment item: %+v", item)
		}
		delete(seenPayments, item.PaymentID)
	}
	if len(seenPayments) != 0 {
		t.Fatalf("missing payments from page: %+v", seenPayments)
	}
}

func TestBybitFundingPaymentsCursorCarriesNativeCursorAndSliceProgress(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * 24 * time.Hour)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		query := r.URL.Query()
		startMillis := query.Get("startTime")
		firstSliceStart := strconv.FormatInt(start.UnixMilli(), 10)
		secondSliceStart := strconv.FormatInt(start.Add(7*24*time.Hour).UnixMilli(), 10)
		switch requests {
		case 1:
			if startMillis != firstSliceStart || query.Get("cursor") != "" {
				t.Fatalf("first request did not start first slice: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"native-next","list":[{"id":"pay-1","symbol":"BTCUSDT","category":"linear","transactionTime":"1782864001000","type":"SETTLEMENT","currency":"USDT","funding":"0.01"}]},"time":1782864002000}`))
		case 2:
			if startMillis != firstSliceStart || query.Get("cursor") != "native-next" {
				t.Fatalf("second request did not resume native cursor in first slice: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"","list":[{"id":"pay-2","symbol":"BTCUSDT","category":"linear","transactionTime":"1782864002000","type":"SETTLEMENT","currency":"USDT","funding":"0.02"}]},"time":1782864003000}`))
		case 3:
			if startMillis != secondSliceStart || query.Get("cursor") != "" {
				t.Fatalf("third request did not advance to second slice: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"","list":[{"id":"pay-3","symbol":"BTCUSDT","category":"linear","transactionTime":"1783468801000","type":"SETTLEMENT","currency":"USDT","funding":"0.03"}]},"time":1783468802000}`))
		default:
			t.Fatalf("unexpected request %d: %s", requests, r.URL.RawQuery)
		}
	}))
	defer server.Close()

	client := NewBybitLinearPerp("key-one", "secret-one", "USDT", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	instrument := bybitFundingPaymentInstrument()
	first, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 1, SettlementAsset: "USDT",
	})
	if err != nil {
		t.Fatalf("FundingPayments first page: %v", err)
	}
	if len(first.Items) != 1 || first.Page.Cursor == "" || !first.Page.HasMore || first.Status != exchange.FundingPaymentsIncomplete {
		t.Fatalf("first page=%+v", first)
	}

	second, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 1, SettlementAsset: "USDT", Cursor: first.Page.Cursor,
	})
	if err != nil {
		t.Fatalf("FundingPayments second page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].PaymentID != "pay-2" || second.Page.Cursor == "" || !second.Page.HasMore {
		t.Fatalf("second page=%+v", second)
	}

	third, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 1, SettlementAsset: "USDT", Cursor: second.Page.Cursor,
	})
	if err != nil {
		t.Fatalf("FundingPayments third page: %v", err)
	}
	if len(third.Items) != 1 || third.Items[0].PaymentID != "pay-3" || third.Page.Cursor == "" || !third.Page.HasMore {
		t.Fatalf("third page=%+v", third)
	}
}

func TestBybitFundingPaymentsKeepsPortableLimitWhileClampingNativeLimit(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		query := r.URL.Query()
		if query.Get("limit") != "50" {
			t.Fatalf("native bybit limit=%q, want 50; query=%s", query.Get("limit"), r.URL.RawQuery)
		}
		switch requests {
		case 1:
			if query.Get("cursor") != "" {
				t.Fatalf("first request cursor=%q", query.Get("cursor"))
			}
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"native-next","list":[{"id":"pay-1","symbol":"BTCUSDT","category":"linear","transactionTime":"1782864001000","type":"SETTLEMENT","currency":"USDT","funding":"0.01"}]},"time":1782864002000}`))
		case 2:
			if query.Get("cursor") != "native-next" {
				t.Fatalf("second request cursor=%q", query.Get("cursor"))
			}
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"","list":[]},"time":1782864003000}`))
		default:
			t.Fatalf("unexpected request %d: %s", requests, r.URL.RawQuery)
		}
	}))
	defer server.Close()

	client := NewBybitLinearPerp("key-one", "secret-one", "USDT", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	instrument := bybitFundingPaymentInstrument()
	first, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 500, SettlementAsset: "USDT",
	})
	if err != nil {
		t.Fatalf("FundingPayments first page: %v", err)
	}
	if first.Page.Limit != 500 || first.Page.Cursor == "" || !first.Page.HasMore || first.Status != exchange.FundingPaymentsIncomplete {
		t.Fatalf("first page leaked native limit or lost continuation: %+v", first)
	}

	second, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 500, SettlementAsset: "USDT", Cursor: first.Page.Cursor,
	})
	if err != nil {
		t.Fatalf("FundingPayments second page: %v", err)
	}
	if second.Page.Limit != 500 || second.Page.Cursor != "" || second.Page.HasMore {
		t.Fatalf("second page leaked native limit or retained continuation: %+v", second)
	}
}

func TestBybitFundingPaymentsRejectsInvalidRequestBounds(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	instrument := bybitFundingPaymentInstrument()
	client := NewBybitLinearPerp("key-one", "secret-one", "USDT", Settings{Endpoint: "https://example.test"}).(exchange.PerpFundingPaymentREST)
	tests := []struct {
		name string
		req  exchange.FundingPaymentHistoryRequest
	}{
		{name: "limit over portable max", req: exchange.FundingPaymentHistoryRequest{Instrument: &instrument, Start: start, End: start.Add(time.Hour), Limit: 501, SettlementAsset: "USDT"}},
		{name: "non usdt settlement", req: exchange.FundingPaymentHistoryRequest{Instrument: &instrument, Start: start, End: start.Add(time.Hour), Limit: 50, SettlementAsset: "USDC"}},
		{name: "raw cursor", req: exchange.FundingPaymentHistoryRequest{Instrument: &instrument, Start: start, End: start.Add(time.Hour), Limit: 50, SettlementAsset: "USDT", Cursor: "native-next"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.FundingPayments(context.Background(), test.req)
			if !errors.Is(err, exchange.ErrInvalidRequest) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestBybitFundingPaymentsDoesNotTreatFeeRateAsPayment(t *testing.T) {
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"","list":[{"id":"pay-1","symbol":"BTCUSDT","category":"linear","transactionTime":"1782864001000","type":"SETTLEMENT","currency":"USDT","funding":"","feeRate":"0.0001"}]},"time":1782864002000}`))
	}))
	defer server.Close()

	client := NewBybitLinearPerp("key-one", "secret-one", "USDT", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	instrument := bybitFundingPaymentInstrument()
	_, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 50, SettlementAsset: "USDT",
	})
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("err=%v", err)
	}
}

func TestBybitFundingPaymentsPreservesEmptyNativeID(t *testing.T) {
	start := time.Date(2026, 8, 3, 15, 59, 0, 0, time.UTC)
	end := start.Add(2 * time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"","list":[{"id":"pay-1","symbol":"BTCUSDT","category":"linear","transactionTime":"1785772800000","type":"SETTLEMENT","currency":"USDT","funding":"0.01"}]},"time":1785772801000}`))
	}))
	defer server.Close()

	client := NewBybitLinearPerp("key-one", "secret-one", "USDT", Settings{Endpoint: server.URL}).(exchange.PerpFundingPaymentREST)
	instrument := bybitFundingPaymentInstrument()
	instrument.Identity.NativeID = ""
	page, err := client.FundingPayments(context.Background(), exchange.FundingPaymentHistoryRequest{
		Instrument: &instrument, Start: start, End: end, Limit: 50, SettlementAsset: "USDT",
	})
	if err != nil {
		t.Fatalf("FundingPayments: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items=%d, want one", len(page.Items))
	}
	if got := page.Items[0].NativeID; got != "" {
		t.Fatalf("native id=%q, want empty catalog-native id preserved", got)
	}
}

func binanceFundingPaymentInstrument() exchange.Instrument {
	return newInstrument(
		exchange.VenueBinance,
		exchange.ProductPerp,
		"BTC-USDT",
		"BTC",
		"USDT",
		"USDT",
		"BTCUSDT",
		"native-BTCUSDT",
		decimal.New(1, -1),
		decimal.New(1, -3),
		decimal.New(1, -3),
		exchange.OptionalDecimal{Value: decimal.NewFromInt(5), Valid: true},
		oneContract(),
	)
}

func lighterFundingPaymentInstrument() exchange.Instrument {
	return newInstrument(
		exchange.VenueLighter,
		exchange.ProductPerp,
		"BTC-USDC",
		"BTC",
		"USDC",
		"USDC",
		"BTC",
		"2",
		decimal.New(1, -1),
		decimal.New(1, -3),
		decimal.New(1, -3),
		exchange.OptionalDecimal{Value: decimal.NewFromInt(5), Valid: true},
		oneContract(),
	)
}

func gateFundingPaymentInstrument() exchange.Instrument {
	return newInstrument(
		exchange.VenueGate,
		exchange.ProductPerp,
		"BTC-USDT",
		"BTC",
		"USDT",
		"USDT",
		"BTC_USDT",
		"BTC_USDT",
		decimal.New(1, -1),
		decimal.New(1, -3),
		decimal.New(1, -3),
		exchange.OptionalDecimal{Value: decimal.NewFromInt(5), Valid: true},
		oneContract(),
	)
}

func bybitFundingPaymentInstrument() exchange.Instrument {
	return newInstrument(
		exchange.VenueBybit,
		exchange.ProductPerp,
		"BTC-USDT",
		"BTC",
		"USDT",
		"USDT",
		"BTCUSDT",
		"native-BTCUSDT",
		decimal.New(1, -1),
		decimal.New(1, -3),
		decimal.New(1, -3),
		exchange.OptionalDecimal{Value: decimal.NewFromInt(5), Valid: true},
		oneContract(),
	)
}
