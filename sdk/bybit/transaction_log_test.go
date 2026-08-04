package sdk

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestClient_GetTransactionLogBuildsPrivateQuery(t *testing.T) {
	startMillis := int64(1782864000000)
	endMillis := int64(1782867600000)
	var seenSignature string
	client := NewClient().
		WithCredentials("key", "secret").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/v5/account/transaction-log" {
				t.Fatalf("path=%s", req.URL.Path)
			}
			query := req.URL.Query()
			if query.Get("accountType") != "UNIFIED" ||
				query.Get("category") != "linear" ||
				query.Get("currency") != "USDT" ||
				query.Get("baseCoin") != "BTC" ||
				query.Get("type") != "SETTLEMENT" ||
				query.Get("startTime") != strconv.FormatInt(startMillis, 10) ||
				query.Get("endTime") != strconv.FormatInt(endMillis, 10) ||
				query.Get("limit") != "50" ||
				query.Get("cursor") != "native-cursor" {
				t.Fatalf("query=%s", req.URL.RawQuery)
			}
			if req.Header.Get("X-BAPI-API-KEY") != "key" {
				t.Fatalf("missing api-key header")
			}
			seenSignature = req.Header.Get("X-BAPI-SIGN")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(`{
					"retCode":0,
					"retMsg":"OK",
					"result":{
						"nextPageCursor":"next-native-cursor",
						"list":[{"id":"pay-1","symbol":"BTCUSDT","category":"linear","side":"Buy","transactionTime":"1782864001000","type":"SETTLEMENT","currency":"USDT","funding":"-0.125","feeRate":"0.0001","tradeId":"trade-1","orderId":"order-1"}]
					},
					"time":1782864002000
				}`)),
				Header: make(http.Header),
			}, nil
		})})

	got, err := client.GetTransactionLog(context.Background(), TransactionLogRequest{
		AccountType: "UNIFIED",
		Category:    "linear",
		Currency:    "USDT",
		BaseCoin:    "BTC",
		Type:        "SETTLEMENT",
		StartMillis: startMillis,
		EndMillis:   endMillis,
		Limit:       50,
		Cursor:      "native-cursor",
	})
	if err != nil {
		t.Fatalf("GetTransactionLog returned error: %v", err)
	}
	if seenSignature == "" {
		t.Fatalf("missing signature header")
	}
	if got.NextPageCursor != "next-native-cursor" || len(got.List) != 1 || got.List[0].Funding != "-0.125" || got.List[0].FeeRate != "0.0001" {
		t.Fatalf("result=%+v", got)
	}
}
