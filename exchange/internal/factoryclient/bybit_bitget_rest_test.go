package factoryclient

import (
	"io"
	"net/http"
	"strconv"
	"strings"
)

type bybitOpenAPIRouter struct{}

func (router bybitOpenAPIRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	query := request.URL.Query()
	var body []byte
	if request.Body != nil {
		body, _ = io.ReadAll(request.Body)
	}
	bodyText := string(body)
	switch request.URL.Path {
	case "/v5/market/instruments-info":
		category := query.Get("category")
		settle := "USDT"
		if category == "spot" {
			settle = ""
		}
		return bybitData(`{"category":"` + category + `","list":[{"symbol":"BTCUSDT","baseCoin":"BTC","quoteCoin":"USDT","settleCoin":"` + settle + `","status":"Trading","priceFilter":{"tickSize":"0.1"},"lotSizeFilter":{"basePrecision":"0.001","qtyStep":"0.001","minOrderQty":"0.001","minOrderAmt":"5","minNotionalValue":"5"}}]}`), nil
	case "/v5/market/orderbook":
		return bybitData(`{"s":"BTCUSDT","b":[["99","1"]],"a":[["101","2"]],"ts":1720000000000,"u":9}`), nil
	case "/v5/market/kline":
		if query.Get("category") == "spot" && query.Get("interval") != "1" {
			return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"retCode":10001,"retMsg":"Invalid period!"}`))}, nil
		}
		return bybitData(`{"category":"` + query.Get("category") + `","symbol":"BTCUSDT","list":[["1720000000000","100","101","99","100.5","3","300"]]}`), nil
	case "/v5/market/recent-trade":
		return bybitData(`{"category":"` + query.Get("category") + `","list":[{"execId":"t1","symbol":"BTCUSDT","price":"100","size":"0.1","side":"Buy","time":"1720000000000"}]}`), nil
	case "/v5/order/create":
		if strings.Contains(bodyText, `"category":"spot"`) &&
			strings.Contains(bodyText, `"orderType":"Market"`) &&
			strings.Contains(bodyText, `"side":"Buy"`) &&
			!strings.Contains(bodyText, `"marketUnit":"quoteCoin"`) {
			return bybitDataError(170140, "Invalid order value"), nil
		}
		return bybitData(`{"orderId":"` + bybitOrderID(bodyText) + `","orderLinkId":"` + bybitOrderLinkID(bodyText) + `"}`), nil
	case "/v5/order/cancel":
		return bybitData(`{"orderId":"` + bybitOrderID(bodyText) + `","orderLinkId":"` + bybitOrderLinkID(bodyText) + `"}`), nil
	case "/v5/order/realtime":
		return bybitData(`{"list":[` + bybitOrderJSON("11", "101", "New", false) + `]}`), nil
	case "/v5/order/history":
		return bybitData(`{"list":[` + bybitOrderJSON("10", "100", "Filled", strings.Contains(query.Get("category"), "linear")) + `]}`), nil
	case "/v5/execution/list":
		return bybitData(`{"list":[{"category":"` + query.Get("category") + `","execType":"Trade","execId":"e1","orderId":"10","orderLinkId":"100","symbol":"BTCUSDT","side":"Buy","execPrice":"99","execQty":"1","execFee":"0.01","feeCurrency":"USDT","isMaker":false,"execTime":"1720000000000"}]}`), nil
	case "/v5/account/wallet-balance":
		return bybitData(`{"list":[{"accountType":"UNIFIED","totalEquity":"100","totalAvailableBalance":"90","totalPerpUPL":"1","totalWalletBalance":"100","coin":[{"coin":"USDT","equity":"100","walletBalance":"100","locked":"10","unrealisedPnl":"1","usdValue":"100"}]}]}`), nil
	case "/v5/position/list":
		return bybitData(`{"list":[{"category":"linear","symbol":"BTCUSDT","side":"","size":"0","avgPrice":"","leverage":"","unrealisedPnl":"","liqPrice":""},{"category":"linear","symbol":"BTCUSDT","side":"Buy","size":"1","avgPrice":"99","leverage":"5","unrealisedPnl":"1","liqPrice":"50"}]}`), nil
	case "/v5/market/tickers":
		return bybitData(`{"category":"linear","list":[{"symbol":"BTCUSDT","markPrice":"100","fundingRate":"0.0001","nextFundingTime":"1720003600000","time":"1720000000000"}]}`), nil
	case "/v5/market/funding/history":
		return bybitData(`{"category":"linear","list":[{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingRateTimestamp":"1720000000000"}]}`), nil
	case "/v5/position/set-leverage":
		return bybitData(`{}`), nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"retCode":10001,"retMsg":"unexpected route"}`))}, nil
}

func bybitData(result string) *http.Response {
	return openAPIJSONResponse(`{"retCode":0,"retMsg":"OK","result":` + result + `,"time":1720000000000}`)
}

func bybitDataError(code int, message string) *http.Response {
	return openAPIJSONResponse(`{"retCode":` + strconv.Itoa(code) + `,"retMsg":"` + message + `","result":{},"time":1720000000000}`)
}

func bybitOrderJSON(orderID, clientID, status string, reduceOnly bool) string {
	return `{"category":"linear","orderId":"` + orderID + `","orderLinkId":"` + clientID + `","symbol":"BTCUSDT","side":"Buy","orderType":"Limit","timeInForce":"GTC","price":"99","qty":"1","cumExecQty":"1","avgPrice":"99","orderStatus":"` + status + `","reduceOnly":` + boolString(reduceOnly) + `,"createdTime":"1720000000000","updatedTime":"1720000001000"}`
}

func bybitOrderID(body string) string {
	if strings.Contains(body, `"orderId":"`) {
		return between(body, `"orderId":"`, `"`)
	}
	return "11"
}

func bybitOrderLinkID(body string) string {
	if strings.Contains(body, `"orderLinkId":"`) {
		return between(body, `"orderLinkId":"`, `"`)
	}
	return "101"
}

type bitgetOpenAPIRouter struct{}

func (router bitgetOpenAPIRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	query := request.URL.Query()
	var body []byte
	if request.Body != nil {
		body, _ = io.ReadAll(request.Body)
	}
	bodyText := string(body)
	switch request.URL.Path {
	case "/api/v3/market/instruments":
		return bitgetData(`[{"symbol":"BTCUSDT","category":"` + query.Get("category") + `","baseCoin":"BTC","quoteCoin":"USDT","minOrderQty":"0.001","minOrderAmount":"5","pricePrecision":"1","quantityPrecision":"3","priceMultiplier":"0.1","quantityMultiplier":"0.001","fundInterval":"8","status":"online"}]`), nil
	case "/api/v3/market/orderbook":
		return bitgetData(`{"b":[["99","1"]],"a":[["101","2"]],"ts":"1720000000000"}`), nil
	case "/api/v3/market/candles":
		return bitgetData(`[["1720000000000","100","101","99","100.5","3","300"]]`), nil
	case "/api/v3/market/fills":
		return bitgetData(`[{"execId":"t1","price":"100","size":"0.1","side":"buy","ts":"1720000000000"}]`), nil
	case "/api/v3/trade/place-order":
		return bitgetData(`{"orderId":"` + bitgetOrderID(bodyText) + `","clientOid":"` + bitgetClientOID(bodyText) + `"}`), nil
	case "/api/v3/trade/cancel-order":
		return bitgetData(`{"orderId":"` + bitgetOrderID(bodyText) + `","clientOid":"` + bitgetClientOID(bodyText) + `"}`), nil
	case "/api/v3/trade/unfilled-orders":
		return bitgetData(`{"list":[` + bitgetOrderJSON("11", "101", "live", false) + `],"cursor":""}`), nil
	case "/api/v3/trade/history-orders":
		return bitgetData(`{"list":[` + bitgetOrderJSON("10", "100", "filled", true) + `],"endId":""}`), nil
	case "/api/v3/trade/fills":
		return bitgetData(`{"list":[{"category":"` + query.Get("category") + `","orderId":"10","clientOid":"100","execId":"e1","symbol":"BTCUSDT","side":"buy","execPrice":"99","execQty":"1","feeDetail":[{"feeCoin":"USDT","fee":"0.01"}],"execTime":"1720000000000"}],"cursor":""}`), nil
	case "/api/v3/account/assets":
		return bitgetData(`{"accountEquity":"100","usdtEquity":"100","available":"90","unrealizedPL":"1","assets":[{"coin":"USDT","available":"90","frozen":"10","equity":"100"}]}`), nil
	case "/api/v3/account/settings":
		return bitgetData(`{"accountMode":"unified","holdMode":"hedge_mode"}`), nil
	case "/api/v3/position/current-position":
		return bitgetData(`{"list":[{"symbol":"BTCUSDT","posSide":"long","qty":"1","averageOpenPrice":"99","markPrice":"100","liquidationPrice":"50","leverage":"5","unrealisedPnl":"1"}]}`), nil
	case "/api/v2/mix/market/current-fund-rate":
		return bitgetData(`[{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingRateInterval":"8","nextUpdate":"1720003600000"}]`), nil
	case "/api/v2/mix/market/history-fund-rate":
		return bitgetData(`[{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingTime":"1720000000000"}]`), nil
	case "/api/v3/account/set-leverage":
		return bitgetData(`{}`), nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"400","msg":"unexpected route"}`))}, nil
}

func bitgetData(data string) *http.Response {
	return openAPIJSONResponse(`{"code":"00000","msg":"success","requestTime":1720000000000,"data":` + data + `}`)
}

func bitgetOrderJSON(orderID, clientID, status string, reduceOnly bool) string {
	return `{"orderId":"` + orderID + `","clientOid":"` + clientID + `","symbol":"BTCUSDT","side":"buy","orderType":"limit","timeInForce":"gtc","price":"99","qty":"1","filledQty":"1","avgPrice":"99","orderStatus":"` + status + `","reduceOnly":"` + boolString(reduceOnly) + `","createdTime":"1720000000000","updatedTime":"1720000001000"}`
}

func bitgetOrderID(body string) string {
	if strings.Contains(body, `"orderId":"`) {
		return between(body, `"orderId":"`, `"`)
	}
	return "11"
}

func bitgetClientOID(body string) string {
	if strings.Contains(body, `"clientOid":"`) {
		return between(body, `"clientOid":"`, `"`)
	}
	return "101"
}

func between(s, prefix, suffix string) string {
	start := strings.Index(s, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.Index(s[start:], suffix)
	if end < 0 {
		return s[start:]
	}
	return s[start : start+end]
}
