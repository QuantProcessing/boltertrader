package venuehttp

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

// HTTPPolicy describes native request cost and command identity. Gateway owns
// the shared local budget; an SDK owns the meaning of its wire request.
type HTTPPolicy struct {
	Venue    string
	Endpoint string
	Cost     int
	Action   string
}

func PolicyForRequest(request *http.Request) HTTPPolicy {
	policy := HTTPPolicy{Venue: RequestVenue(request), Cost: 1}
	if request == nil || request.URL == nil {
		return policy
	}
	path := request.URL.Path
	endpoint := path
	// Gate puts the order identity in the path. It must not create a new bucket.
	if at := strings.Index(endpoint, "/orders/"); at >= 0 {
		endpoint = endpoint[:at] + "/orders/:id"
	}
	policy.Endpoint = request.Method + " " + endpoint
	if request.Method == http.MethodGet {
		policy.Action = "read"
	}
	body := requestBodyFields(request)
	switch policy.Venue {
	case "binance", "aster":
		if request.Method == http.MethodPost && (strings.HasSuffix(path, "/order") || strings.HasSuffix(path, "/batchOrders")) {
			policy.Action = "place"
		}
		if request.Method == http.MethodDelete && strings.Contains(strings.ToLower(path), "order") {
			policy.Action = "cancel"
		}
		if policy.Venue == "binance" {
			policy.Cost = binanceRequestCost(request)
		}
	case "bybit":
		if strings.HasPrefix(path, "/v5/order/create") {
			policy.Action = "place"
		}
		if strings.HasPrefix(path, "/v5/order/cancel") {
			policy.Action = "cancel"
		}
		if batch := body["request"]; len(batch) > 0 {
			var items []json.RawMessage
			if json.Unmarshal(batch, &items) == nil && len(items) > 1 {
				policy.Cost = len(items)
			}
		}
	case "bitget":
		if strings.HasSuffix(path, "/place-order") || strings.HasSuffix(path, "/batch-place-order") {
			policy.Action = "place"
		}
		if strings.Contains(path, "/cancel-") || strings.Contains(path, "/batch-cancel-") {
			policy.Action = "cancel"
		}
	case "okx":
		if request.Method == http.MethodPost && (path == "/api/v5/trade/order" || path == "/api/v5/trade/batch-orders") {
			policy.Action = "place"
		}
		if strings.HasPrefix(path, "/api/v5/trade/cancel-") {
			policy.Action = "cancel"
		}
	case "gate":
		if request.Method == http.MethodPost && strings.HasSuffix(path, "/orders") {
			policy.Action = "place"
		}
		if request.Method == http.MethodDelete && strings.Contains(path, "/orders") {
			policy.Action = "cancel"
		}
	case "hyperliquid":
		var kind string
		_ = json.Unmarshal(body["type"], &kind)
		if path == "/info" {
			policy.Action = "read"
			policy.Cost = 20
			switch kind {
			case "l2Book", "allMids", "clearinghouseState", "spotClearinghouseState", "orderStatus", "exchangeStatus":
				policy.Cost = 2
			case "historicalOrders", "userFills", "userFillsByTime", "userTwapSliceFills":
				// Reserve for the documented maximum of 2000 returned rows.
				policy.Cost = 120
			case "userRole":
				policy.Cost = 60
			case "candleSnapshot":
				policy.Cost = 105
			}
			policy.Endpoint += "/" + kind
		}
		if path == "/exchange" {
			var action struct {
				Type    string            `json:"type"`
				Orders  []json.RawMessage `json:"orders"`
				Cancels []json.RawMessage `json:"cancels"`
			}
			if json.Unmarshal(body["action"], &action) == nil {
				switch action.Type {
				case "order":
					policy.Action = "place"
				case "cancel", "cancelByCloid":
					policy.Action = "cancel"
				}
				policy.Cost = 1 + (len(action.Orders)+len(action.Cancels))/40
				policy.Endpoint += "/" + action.Type
			}
		}
	case "lighter":
		policy.Action = lighterAction(request, policy.Action)
	case "nado":
		if strings.HasSuffix(path, "/execute") {
			if _, ok := body["place_order"]; ok {
				policy.Action = "place"
			}
			if _, ok := body["cancel_orders"]; ok {
				policy.Action = "cancel"
			}
			if _, ok := body["cancel_product_orders"]; ok {
				policy.Action = "cancel"
			}
		} else {
			policy.Action = "read"
		}
	}
	return policy
}

func requestBodyFields(request *http.Request) map[string]json.RawMessage {
	if request.GetBody == nil {
		return nil
	}
	body, err := request.GetBody()
	if err != nil {
		return nil
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 64<<10))
	if err != nil {
		return nil
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	return fields
}

// Weights for the platform's active Binance REST families. Unknown endpoints
// use a conservative 20 units; this is not a claim that all Binance APIs cost 20.
// Sources: Binance Spot REST market/account docs and USD-M market-data docs.
func binanceRequestCost(request *http.Request) int {
	path, q := request.URL.Path, request.URL.Query()
	if request.Method != http.MethodGet {
		return 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if strings.HasPrefix(path, "/api/v3/") {
		switch strings.TrimPrefix(path, "/api/v3/") {
		case "ping", "time":
			return 1
		case "exchangeInfo", "account", "myTrades":
			return 20
		case "order":
			return 4
		case "openOrders":
			if q.Get("symbol") == "" {
				return 80
			}
			return 6
		case "depth":
			if limit <= 0 {
				limit = 100
			}
			switch {
			case limit <= 100:
				return 5
			case limit <= 500:
				return 25
			case limit <= 1000:
				return 50
			default:
				return 250
			}
		case "klines", "uiKlines", "avgPrice":
			return 2
		case "ticker/price", "ticker/bookTicker":
			if q.Get("symbol") == "" {
				return 4
			}
			return 2
		case "ticker/24hr":
			if q.Get("symbol") != "" {
				return 2
			}
			var symbols []string
			if json.Unmarshal([]byte(q.Get("symbols")), &symbols) == nil && len(symbols) > 0 {
				if len(symbols) <= 20 {
					return 2
				}
				if len(symbols) <= 100 {
					return 40
				}
			}
			return 80
		}
	}
	if strings.HasPrefix(path, "/fapi/") {
		part := path[strings.LastIndex(path, "/")+1:]
		switch part {
		case "ping", "time", "exchangeInfo", "openInterest", "order":
			return 1
		case "depth":
			if limit <= 0 {
				limit = 500
			}
			switch {
			case limit <= 50:
				return 2
			case limit <= 100:
				return 5
			case limit <= 500:
				return 10
			default:
				return 20
			}
		case "ticker", "24hr":
			if q.Get("symbol") == "" {
				return 40
			}
			return 1
		case "bookTicker":
			if q.Get("symbol") == "" {
				return 5
			}
			return 2
		case "price":
			if q.Get("symbol") == "" {
				return 2
			}
			return 1
		case "premiumIndex":
			if q.Get("symbol") == "" {
				return 10
			}
			return 1
		case "klines", "markPriceKlines", "indexPriceKlines":
			if limit <= 0 {
				limit = 500
			}
			switch {
			case limit < 100:
				return 1
			case limit < 500:
				return 2
			case limit <= 1000:
				return 5
			default:
				return 10
			}
		case "account", "balance", "positionRisk", "userTrades", "allOrders":
			return 5
		case "openOrders":
			if q.Get("symbol") == "" {
				return 40
			}
			return 1
		case "fundingRate", "fundingInfo":
			return 1 // local accounting includes weight-zero calls
		}
	}
	return 20
}

func lighterAction(request *http.Request, fallback string) string {
	if request.Method != http.MethodPost || request.URL.Path != "/api/v1/sendTx" || request.GetBody == nil {
		return fallback
	}
	mediaType, params, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return fallback
	}
	body, err := request.GetBody()
	if err != nil {
		return fallback
	}
	defer body.Close()
	reader := multipart.NewReader(io.LimitReader(body, 64<<10), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err != nil {
			return fallback
		}
		if part.FormName() != "tx_type" {
			part.Close()
			continue
		}
		value, err := io.ReadAll(io.LimitReader(part, 16))
		part.Close()
		if err != nil {
			return fallback
		}
		// Lighter protocol transaction IDs; other actions never claim placement proof.
		switch string(value) {
		case "14":
			return "place"
		case "15", "16":
			return "cancel"
		default:
			return fallback
		}
	}
}
