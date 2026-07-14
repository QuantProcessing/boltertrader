package sdk

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClient_GetInstruments(t *testing.T) {
	got, err := newLiveClient(t).GetInstruments(context.Background(), "linear")
	if err != nil {
		t.Fatalf("GetInstruments: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one instrument")
	}
}

func TestClient_GetInstrumentsForBase(t *testing.T) {
	got, err := newLiveClient(t).GetInstrumentsForBase(context.Background(), "linear", "BTC")
	if err != nil {
		t.Fatalf("GetInstrumentsForBase: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected BTC linear instruments")
	}
}

func TestClient_GetInstrumentsForBaseConsumesEveryPage(t *testing.T) {
	t.Parallel()

	var seenCursors []string
	client := NewClient().
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/v5/market/instruments-info" {
				return nil, fmt.Errorf("unexpected request path %q", req.URL.Path)
			}
			if got := req.URL.Query().Get("baseCoin"); got != "BTC" {
				return nil, fmt.Errorf("baseCoin=%q, want BTC", got)
			}
			cursor := req.URL.Query().Get("cursor")
			seenCursors = append(seenCursors, cursor)

			var body string
			switch cursor {
			case "":
				body = `{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"page-2","list":[{"symbol":"BTCUSDT"}]}}`
			case "page-2":
				body = `{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"page-3","list":[{"symbol":"BTCPERP"}]}}`
			case "page-3":
				body = `{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"","list":[{"symbol":"BTCUSD"}]}}`
			default:
				return nil, fmt.Errorf("unexpected cursor %q", cursor)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		})})

	instruments, err := client.GetInstrumentsForBase(context.Background(), "linear", "BTC")
	if err != nil {
		t.Fatalf("GetInstrumentsForBase returned error: %v", err)
	}
	if got := strings.Join(seenCursors, ","); got != ",page-2,page-3" {
		t.Fatalf("cursor sequence=%q, want %q", got, ",page-2,page-3")
	}
	if len(instruments) != 3 || instruments[0].Symbol != "BTCUSDT" || instruments[1].Symbol != "BTCPERP" || instruments[2].Symbol != "BTCUSD" {
		t.Fatalf("instruments=%+v, want all three pages", instruments)
	}
}

func TestClient_GetInstrumentsForBaseRejectsRepeatedCursor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cursors []string
	}{
		{name: "adjacent", cursors: []string{"same", "same"}},
		{name: "cycle", cursors: []string{"cursor-a", "cursor-b", "cursor-a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := NewClient().
				WithBaseURL("https://example.test").
				WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					if calls > len(tt.cursors) {
						return nil, fmt.Errorf("pagination exceeded cursor-case transport bound")
					}
					body := fmt.Sprintf(
						`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":%q,"list":[{"symbol":%q}]}}`,
						tt.cursors[calls-1],
						fmt.Sprintf("instrument-%d", calls),
					)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				})})

			instruments, err := client.GetInstrumentsForBase(context.Background(), "linear", "BTC")
			if err == nil || !strings.Contains(err.Error(), "repeated cursor") {
				t.Fatalf("error=%v, want repeated cursor error", err)
			}
			if instruments != nil {
				t.Fatalf("instruments=%+v, want nil rather than accumulated partial pages", instruments)
			}
			if calls != len(tt.cursors) {
				t.Fatalf("calls=%d, want %d", calls, len(tt.cursors))
			}
		})
	}
}

func TestClient_GetInstrumentsForBaseHasFinitePageLimitAcrossEmptyPages(t *testing.T) {
	t.Parallel()

	const expectedMaxPages = 1000
	calls := 0
	client := NewClient().
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if calls > expectedMaxPages {
				return nil, fmt.Errorf("pagination exceeded test transport bound")
			}
			list := "[]"
			if calls == 1 {
				list = `[{"symbol":"BTCUSDT"}]`
			}
			body := fmt.Sprintf(
				`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"cursor-%d","list":%s}}`,
				calls,
				list,
			)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		})})

	instruments, err := client.GetInstrumentsForBase(context.Background(), "linear", "BTC")
	if err == nil || !strings.Contains(err.Error(), "page limit") {
		t.Fatalf("error=%v, want page limit error", err)
	}
	if instruments != nil {
		t.Fatalf("instruments=%+v, want nil rather than accumulated partial pages", instruments)
	}
	if calls != expectedMaxPages {
		t.Fatalf("calls=%d, want %d", calls, expectedMaxPages)
	}
}

func TestClient_GetTicker(t *testing.T) {
	got, err := newLiveClient(t).GetTicker(context.Background(), "spot", bybitSpotSymbol)
	if err != nil {
		t.Fatalf("GetTicker: %v", err)
	}
	if got.Symbol != bybitSpotSymbol {
		t.Fatalf("unexpected ticker symbol: %s", got.Symbol)
	}
}

func TestClient_GetTickersBuildsAllTickersRequest(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenQuery string
	client := NewClient().
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			seenQuery = req.URL.RawQuery
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"retCode":0,"retMsg":"OK","time":1710000000123,"result":{"category":"linear","list":[{"symbol":"BTCUSDT","fundingRate":"0.0001","markPrice":"65000","indexPrice":"64990","nextFundingTime":"1710003600000","fundingIntervalHour":"8"}]}}`,
				)),
				Header: make(http.Header),
			}, nil
		})})

	got, err := client.GetTickers(context.Background(), "linear")
	if err != nil {
		t.Fatalf("GetTickers returned error: %v", err)
	}
	if seenPath != "/v5/market/tickers" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if strings.Contains(seenQuery, "symbol=") || !strings.Contains(seenQuery, "category=linear") {
		t.Fatalf("unexpected query: %s", seenQuery)
	}
	if len(got) != 1 || got[0].Symbol != "BTCUSDT" || got[0].FundingRate != "0.0001" || got[0].Time != "1710000000123" {
		t.Fatalf("unexpected ticker rows: %+v", got)
	}
}

func TestClient_GetOrderBook(t *testing.T) {
	got, err := newLiveClient(t).GetOrderBook(context.Background(), "linear", bybitLinearSymbol, 5)
	if err != nil {
		t.Fatalf("GetOrderBook: %v", err)
	}
	if len(got.Asks) == 0 || len(got.Bids) == 0 {
		t.Fatalf("expected non-empty order book: %+v", got)
	}
}

func TestClient_GetRecentTrades(t *testing.T) {
	got, err := newLiveClient(t).GetRecentTrades(context.Background(), "spot", bybitSpotSymbol, 10)
	if err != nil {
		t.Fatalf("GetRecentTrades: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected recent trades")
	}
}

func TestClient_GetKlines(t *testing.T) {
	got, err := newLiveClient(t).GetKlines(context.Background(), "linear", bybitLinearSymbol, "60", 0, 0, 10)
	if err != nil {
		t.Fatalf("GetKlines: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected klines")
	}
}

func TestClient_GetOpenInterest(t *testing.T) {
	got, err := newLiveClient(t).GetOpenInterest(context.Background(), "linear", bybitLinearSymbol, "5min", 0, 0, 50, "")
	if err != nil {
		t.Fatalf("GetOpenInterest: %v", err)
	}
	if len(got.List) == 0 {
		t.Fatal("expected open interest history")
	}
}

func TestClient_GetFundingHistory(t *testing.T) {
	got, err := newLiveClient(t).GetFundingHistory(context.Background(), "linear", bybitLinearSymbol, 0, 0, 2)
	if err != nil {
		t.Fatalf("GetFundingHistory: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected funding history")
	}
}
