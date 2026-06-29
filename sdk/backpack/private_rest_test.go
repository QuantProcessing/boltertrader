package sdk

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientExecuteOrderDelegatesToPlaceOrder(t *testing.T) {
	t.Parallel()

	assertSinglePrivateWrapperCall(t, "private_rest.go", "ExecuteOrder", "PlaceOrder", "ctx", "req")
}

func TestClientPlaceOrderDelegatesToExistingOrderExecutionPath(t *testing.T) {
	t.Parallel()

	var gotMethod string
	var gotPath string
	var gotInstruction string
	client := NewClient().WithCredentials("api-key", testSeedBase64())
	client.baseURL = "https://example.test"
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotInstruction = r.Header.Get("X-API-Key")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"id":"123","symbol":"BTC_USDC","side":"Bid","orderType":"Limit","quantity":"1","price":"100","status":"New","clientId":7}`,
				)),
				Header: make(http.Header),
			}, nil
		}),
	}

	order, err := client.PlaceOrder(context.Background(), CreateOrderRequest{
		Symbol:    "BTC_USDC",
		Side:      "Bid",
		OrderType: "Limit",
		Quantity:  "1",
		Price:     "100",
		ClientID:  7,
	})
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/api/v1/order", gotPath)
	require.Equal(t, "api-key", gotInstruction)
	require.Equal(t, "123", order.ID)
}

func TestClientGetFillHistoryUsesSignedHistoryEndpoint(t *testing.T) {
	t.Parallel()

	var gotMethod string
	var gotPath string
	var gotQuery string
	client := NewClient().WithCredentials("api-key", testSeedBase64())
	client.baseURL = "https://example.test"
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotQuery = r.URL.RawQuery
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`[{"clientId":"42","fee":"0.01","feeSymbol":"USDC","isMaker":true,"orderId":"order-1","price":"100","quantity":"0.5","side":"Bid","symbol":"BTC_USDC_PERP","timestamp":"1710000000000","tradeId":9001}]`,
				)),
				Header: make(http.Header),
			}, nil
		}),
	}

	fills, err := client.GetFillHistory(context.Background(), FillHistoryRequest{
		MarketType:    "PERP",
		Symbol:        "BTC_USDC_PERP",
		OrderID:       "order-1",
		FillType:      "User",
		Limit:         50,
		SortDirection: "Asc",
	})
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, gotMethod)
	require.Equal(t, "/wapi/v1/history/fills", gotPath)
	require.Contains(t, gotQuery, "marketType=PERP")
	require.Contains(t, gotQuery, "symbol=BTC_USDC_PERP")
	require.Contains(t, gotQuery, "orderId=order-1")
	require.Contains(t, gotQuery, "fillType=User")
	require.Contains(t, gotQuery, "limit=50")
	require.Contains(t, gotQuery, "sortDirection=Asc")
	require.Len(t, fills, 1)
	require.Equal(t, "order-1", fills[0].OrderID)
	require.Equal(t, "9001", fills[0].TradeID.String())
	require.Equal(t, "42", fills[0].ClientID.String())
}

func TestClientPlaceOrdersUsesBatchEndpoint(t *testing.T) {
	t.Parallel()

	var gotMethod string
	var gotPath string
	var gotBody []CreateOrderRequest
	client := NewClient().WithCredentials("api-key", testSeedBase64())
	client.baseURL = "https://example.test"
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`[{"id":"batch-1","symbol":"BTC_USDC_PERP","side":"Bid","orderType":"Limit","quantity":"1","price":"100","status":"New","clientId":7},{"id":"batch-2","symbol":"BTC_USDC_PERP","side":"Ask","orderType":"Limit","quantity":"2","price":"101","status":"New","clientId":8}]`,
				)),
				Header: make(http.Header),
			}, nil
		}),
	}

	orders, err := client.PlaceOrders(context.Background(), []CreateOrderRequest{
		{Symbol: "BTC_USDC_PERP", Side: "Bid", OrderType: "Limit", Quantity: "1", Price: "100", ClientID: 7},
		{Symbol: "BTC_USDC_PERP", Side: "Ask", OrderType: "Limit", Quantity: "2", Price: "101", ClientID: 8},
	})
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "/api/v1/orders", gotPath)
	require.Len(t, gotBody, 2)
	require.Equal(t, uint32(7), gotBody[0].ClientID)
	require.Len(t, orders, 2)
	require.Equal(t, "batch-2", orders[1].ID)
}

func testSeedBase64() string {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(seed)
}

func assertSinglePrivateWrapperCall(t *testing.T, filename, wrapperName, targetName string, argNames ...string) {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, filepath.Join(filepath.Dir(currentFile), filename), nil, 0)
	require.NoError(t, err)

	var method *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != wrapperName {
			continue
		}
		method = fn
		break
	}
	require.NotNil(t, method, "wrapper %s not found", wrapperName)
	require.Len(t, method.Body.List, 1, "wrapper %s should stay a single statement", wrapperName)

	ret, ok := method.Body.List[0].(*ast.ReturnStmt)
	require.True(t, ok, "wrapper %s should consist of a return statement", wrapperName)
	require.Len(t, ret.Results, 1, "wrapper %s should return a single result expression", wrapperName)

	call, ok := ret.Results[0].(*ast.CallExpr)
	require.True(t, ok, "wrapper %s should return a method call", wrapperName)

	selector, ok := call.Fun.(*ast.SelectorExpr)
	require.True(t, ok, "wrapper %s should call a selector", wrapperName)
	receiver, ok := selector.X.(*ast.Ident)
	require.True(t, ok, "wrapper %s should call through the receiver", wrapperName)
	require.Equal(t, "c", receiver.Name)
	require.Equal(t, targetName, selector.Sel.Name)
	require.Len(t, call.Args, len(argNames), "wrapper %s should preserve its argument list", wrapperName)
	for i, argName := range argNames {
		arg, ok := call.Args[i].(*ast.Ident)
		require.True(t, ok, "wrapper %s arg %d should stay an identifier", wrapperName, i)
		require.Equal(t, argName, arg.Name)
	}
}
