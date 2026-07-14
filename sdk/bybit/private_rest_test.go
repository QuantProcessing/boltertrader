package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestClient_GetWalletBalance(t *testing.T) {
	got, err := newLivePrivateClient(t).GetWalletBalance(context.Background(), "UNIFIED", "")
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Bybit wallet balance endpoint")
		t.Fatalf("GetWalletBalance: %v", err)
	}
	if len(got.List) == 0 {
		t.Fatal("expected at least one wallet balance record")
	}
}

func TestClient_GetAccountInfo(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccountInfo(context.Background())
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Bybit account info endpoint")
		t.Fatalf("GetAccountInfo: %v", err)
	}
	if got.UnifiedMarginStatus == 0 {
		t.Fatal("expected unified margin status")
	}
}

func TestClient_GetAPIKeyInfoBuildsUserQueryAPIRequest(t *testing.T) {
	t.Parallel()

	var seenPath string
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`{
					"retCode": 0,
					"retMsg": "OK",
					"result": {
						"readOnly": 0,
						"uta": 1,
						"permissions": {
							"ContractTrade": ["Order", "Position"],
							"Spot": ["SpotTrade"]
						}
					}
				}`)),
				Header: make(http.Header),
			}, nil
		})})

	got, err := client.GetAPIKeyInfo(context.Background())
	if err != nil {
		t.Fatalf("GetAPIKeyInfo returned error: %v", err)
	}
	if seenPath != "/v5/user/query-api" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if got == nil || got.ReadOnly != 0 || got.UTA != 1 {
		t.Fatalf("unexpected api key info: %+v", got)
	}
	if !got.Permissions.HasSpotTrade() {
		t.Fatalf("expected spot trade permission: %+v", got.Permissions)
	}
	if !got.Permissions.HasContractOrder() || !got.Permissions.HasContractPosition() {
		t.Fatalf("expected contract order and position permissions: %+v", got.Permissions)
	}
}

func TestClient_GetAPIKeyInfoReturnsRetCodeError(t *testing.T) {
	t.Parallel()

	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":10003,"retMsg":"API key is invalid.","result":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	_, err := client.GetAPIKeyInfo(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "query api key failed: 10003 API key is invalid") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_GetFeeRates(t *testing.T) {
	got, err := newLivePrivateClient(t).GetFeeRates(context.Background(), "linear", bybitLinearSymbol)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Bybit fee rates endpoint")
		t.Fatalf("GetFeeRates: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected fee rate records")
	}
}

func TestClient_GetPositions(t *testing.T) {
	got, err := newLivePrivateClient(t).GetPositions(context.Background(), "linear", "", "USDT")
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Bybit positions endpoint")
		t.Fatalf("GetPositions: %v", err)
	}
	if got == nil {
		t.Fatal("expected positions slice")
	}
}

func TestClient_GetPositionsConsumesEveryPage(t *testing.T) {
	t.Parallel()

	var seenCursors []string
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenCursors = append(seenCursors, req.URL.Query().Get("cursor"))
			if req.URL.Path != "/v5/position/list" {
				return nil, errors.New("unexpected request path")
			}
			if req.URL.Query().Get("category") != "linear" || req.URL.Query().Get("settleCoin") != "USDT" {
				return nil, errors.New("position filters were not preserved")
			}

			var body string
			switch req.URL.Query().Get("cursor") {
			case "":
				body = `{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"page-2","list":[{"symbol":"BTCUSDT","side":"Buy","size":"1"}]}}`
			case "page-2":
				body = `{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"page-3","list":[{"symbol":"ETHUSDT","side":"Sell","size":"2"}]}}`
			case "page-3":
				body = `{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"","list":[{"symbol":"SOLUSDT","side":"Buy","size":"3"}]}}`
			default:
				return nil, errors.New("unexpected cursor")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		})})

	got, err := client.GetPositions(context.Background(), "linear", "", "USDT")
	if err != nil {
		t.Fatalf("GetPositions returned error: %v", err)
	}
	if strings.Join(seenCursors, ",") != ",page-2,page-3" {
		t.Fatalf("unexpected cursor sequence: %v", seenCursors)
	}
	if len(got) != 3 || got[0].Symbol != "BTCUSDT" || got[1].Symbol != "ETHUSDT" || got[2].Symbol != "SOLUSDT" {
		t.Fatalf("unexpected positions: %+v", got)
	}
}

func TestClient_GetPositionsRejectsCursorCycles(t *testing.T) {
	t.Parallel()

	requests := 0
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"same-cursor","list":[{"symbol":"BTCUSDT"}]}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	got, err := client.GetPositions(context.Background(), "linear", "", "USDT")
	if err == nil || !strings.Contains(err.Error(), "cursor cycle") {
		t.Fatalf("expected cursor-cycle error, got positions=%+v err=%v", got, err)
	}
	if got != nil {
		t.Fatalf("expected no partial positions on cursor cycle, got %+v", got)
	}
	if requests != 2 {
		t.Fatalf("expected cycle detection after two requests, got %d", requests)
	}
}

func TestClient_GetPositionsDoesNotReturnPartialResultsAfterPageError(t *testing.T) {
	t.Parallel()

	requests := 0
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			if requests == 2 {
				return nil, errors.New("second page unavailable")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"page-2","list":[{"symbol":"BTCUSDT"}]}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	got, err := client.GetPositions(context.Background(), "linear", "", "USDT")
	if err == nil || !strings.Contains(err.Error(), "second page unavailable") {
		t.Fatalf("expected second-page error, got positions=%+v err=%v", got, err)
	}
	if got != nil {
		t.Fatalf("expected no partial positions after page error, got %+v", got)
	}
}

func TestClient_GetPositionsHasFinitePageLimitAcrossEmptyPages(t *testing.T) {
	t.Parallel()

	const expectedMaxPages = 1000
	calls := 0
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if calls > expectedMaxPages {
				return nil, errors.New("pagination exceeded test transport bound")
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

	positions, err := client.GetPositions(context.Background(), "linear", "", "USDT")
	if err == nil || !strings.Contains(err.Error(), "page limit") {
		t.Fatalf("error=%v, want page limit error", err)
	}
	if positions != nil {
		t.Fatalf("positions=%+v, want nil rather than accumulated partial pages", positions)
	}
	if calls != expectedMaxPages {
		t.Fatalf("calls=%d, want %d", calls, expectedMaxPages)
	}
}

func TestClient_SetLeverage(t *testing.T) {
	client := requireBybitLiveWrite(t)
	symbol := bybitEnvOrDefault("BYBIT_TEST_SYMBOL", bybitLinearSymbol)
	leverage := bybitEnvOrDefault("BYBIT_TEST_LEVERAGE", "2")

	err := client.SetLeverage(context.Background(), SetLeverageRequest{
		Category:     "linear",
		Symbol:       symbol,
		BuyLeverage:  leverage,
		SellLeverage: leverage,
	})
	if err != nil {
		t.Fatalf("SetLeverage: %v", err)
	}
}

func TestClient_SwitchPositionMode(t *testing.T) {
	client := requireBybitLiveWrite(t)
	symbol := bybitEnvOrDefault("BYBIT_TEST_SYMBOL", bybitLinearSymbol)
	mode := bybitEnvOrDefaultInt(t, "BYBIT_TEST_POSITION_MODE", 0)

	err := client.SwitchPositionMode(context.Background(), SwitchPositionModeRequest{
		Category: "linear",
		Symbol:   symbol,
		Mode:     mode,
	})
	if err != nil {
		t.Fatalf("SwitchPositionMode: %v", err)
	}
}

func TestClient_SetLeverageBuildsPositionRequest(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":0,"retMsg":"OK","result":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	err := client.SetLeverage(context.Background(), SetLeverageRequest{
		Category:     "linear",
		Symbol:       "BTCUSDT",
		BuyLeverage:  "5",
		SellLeverage: "5",
	})
	if err != nil {
		t.Fatalf("SetLeverage returned error: %v", err)
	}
	if seenPath != "/v5/position/set-leverage" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	for _, want := range []string{
		`"category":"linear"`,
		`"symbol":"BTCUSDT"`,
		`"buyLeverage":"5"`,
		`"sellLeverage":"5"`,
	} {
		if !strings.Contains(seenBody, want) {
			t.Fatalf("expected body to contain %s, got %s", want, seenBody)
		}
	}
}

func TestClient_SwitchPositionModeIgnoresAlreadySet(t *testing.T) {
	t.Parallel()

	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":110025,"retMsg":"Position mode has not been modified","result":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	err := client.SwitchPositionMode(context.Background(), SwitchPositionModeRequest{
		Category: "linear",
		Symbol:   "BTCUSDT",
		Mode:     3,
	})
	if err != nil {
		t.Fatalf("SwitchPositionMode returned error for 110025: %v", err)
	}
}

func TestClient_SwitchPositionModeBuildsPositionRequest(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":0,"retMsg":"OK","result":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	err := client.SwitchPositionMode(context.Background(), SwitchPositionModeRequest{
		Category: "linear",
		Symbol:   "BTCUSDT",
		Mode:     3,
	})
	if err != nil {
		t.Fatalf("SwitchPositionMode returned error: %v", err)
	}
	if seenPath != "/v5/position/switch-mode" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	for _, want := range []string{
		`"category":"linear"`,
		`"symbol":"BTCUSDT"`,
		`"mode":3`,
	} {
		if !strings.Contains(seenBody, want) {
			t.Fatalf("expected body to contain %s, got %s", want, seenBody)
		}
	}
}

func TestClient_BorrowSpotBuildsAccountBorrowRequest(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":0,"retMsg":"OK","result":{"coin":"USDT","amount":"100"}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	result, err := client.BorrowSpot(context.Background(), BorrowSpotRequest{
		Coin:   "USDT",
		Amount: "100",
	})
	if err != nil {
		t.Fatalf("BorrowSpot returned error: %v", err)
	}
	if seenPath != "/v5/account/borrow" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if result == nil || result.Coin != "USDT" || result.Amount != "100" {
		t.Fatalf("unexpected result: %+v", result)
	}
	for _, want := range []string{
		`"coin":"USDT"`,
		`"amount":"100"`,
	} {
		if !strings.Contains(seenBody, want) {
			t.Fatalf("expected body to contain %s, got %s", want, seenBody)
		}
	}
}

func TestClient_RepaySpotBorrowBuildsNoConvertRepayRequest(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":0,"retMsg":"OK","result":{"resultStatus":"SU"}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	result, err := client.RepaySpotBorrow(context.Background(), RepaySpotBorrowRequest{
		Coin:   "USDT",
		Amount: "50",
	})
	if err != nil {
		t.Fatalf("RepaySpotBorrow returned error: %v", err)
	}
	if seenPath != "/v5/account/no-convert-repay" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if result == nil || result.ResultStatus != "SU" {
		t.Fatalf("unexpected result: %+v", result)
	}
	for _, want := range []string{
		`"coin":"USDT"`,
		`"amount":"50"`,
	} {
		if !strings.Contains(seenBody, want) {
			t.Fatalf("expected body to contain %s, got %s", want, seenBody)
		}
	}
}

func TestClient_GetSpotBorrowAmountReadsWalletSpotBorrow(t *testing.T) {
	t.Parallel()

	var seenQuery string
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenQuery = req.URL.RawQuery
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":0,"retMsg":"OK","result":{"list":[{"accountType":"UNIFIED","coin":[{"coin":"USDT","borrowAmount":"12","spotBorrow":"7"}]}]}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	amount, err := client.GetSpotBorrowAmount(context.Background(), "USDT")
	if err != nil {
		t.Fatalf("GetSpotBorrowAmount returned error: %v", err)
	}
	if amount != "7" {
		t.Fatalf("unexpected amount: %s", amount)
	}
	if !strings.Contains(seenQuery, "accountType=UNIFIED") || !strings.Contains(seenQuery, "coin=USDT") {
		t.Fatalf("unexpected query: %s", seenQuery)
	}
}

func TestClient_SetMarginModeBuildsAccountRequest(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":0,"retMsg":"OK","result":{"reasons":[]}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	result, err := client.SetMarginMode(context.Background(), SetMarginModeRequest{
		SetMarginMode: "PORTFOLIO_MARGIN",
	})
	if err != nil {
		t.Fatalf("SetMarginMode returned error: %v", err)
	}
	if seenPath != "/v5/account/set-margin-mode" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if result == nil || len(result.Reasons) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !strings.Contains(seenBody, `"setMarginMode":"PORTFOLIO_MARGIN"`) {
		t.Fatalf("unexpected body: %s", seenBody)
	}
}

func TestClient_SetMarginModeIgnoresAlreadySet(t *testing.T) {
	t.Parallel()

	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":10001,"retMsg":"margin mode has not been modified","result":{"reasons":[]}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	_, err := client.SetMarginMode(context.Background(), SetMarginModeRequest{
		SetMarginMode: "REGULAR_MARGIN",
	})
	if err != nil {
		t.Fatalf("SetMarginMode returned error for already-set response: %v", err)
	}
}
