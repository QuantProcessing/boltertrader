package spot

import (
	"errors"
	"net/http"
)

var ErrWSOutcomeUnknown = errors.New("binance spot ws-api outcome unknown after request write")

// APIError represents a Binance API error
type APIError struct {
	Code       int    `json:"code"`
	Message    string `json:"msg"`
	HTTPStatus int    `json:"-"`
}

func (e *APIError) Error() string {
	return e.Message
}

// IsDefinitiveOrderRejection reports whether err is a parsed Binance order or
// request-business error carried by a 400 response. Transport failures,
// authentication failures, temporary/unknown 10xx errors, rate limits,
// malformed envelopes, other 4xx responses, and 5xx responses remain outside
// the definitive venue-rejection taxonomy.
func IsDefinitiveOrderRejection(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil || apiErr.Code == 0 || apiErr.Message == "" {
		return false
	}
	return apiErr.HTTPStatus == http.StatusBadRequest && isDefinitiveOrderErrorCode(apiErr.Code)
}

func isDefinitiveOrderErrorCode(code int) bool {
	switch code {
	case -1013, -1014, -1015, -1020,
		-1100, -1101, -1102, -1103, -1104, -1105, -1106, -1108,
		-1111, -1112, -1114, -1115, -1116, -1117, -1118, -1119,
		-1121, -1128, -1130, -1134, -1135, -1145,
		-2010, -2011, -2013, -2016, -2038, -2039:
		return true
	default:
		return false
	}
}

func isAuthenticationError(status, code int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden ||
		code == -1002 || code == -1022 || code == -2014 || code == -2015
}

// Common response types

type ServerTimeResponse struct {
	ServerTime int64 `json:"serverTime"`
}

type ExchangeInfoResponse struct {
	Timezone   string       `json:"timezone"`
	ServerTime int64        `json:"serverTime"`
	Symbols    []SymbolInfo `json:"symbols"`
}

type SymbolInfo struct {
	Symbol             string                   `json:"symbol"`
	Status             string                   `json:"status"`
	BaseAsset          string                   `json:"baseAsset"`
	BaseAssetPrecision int                      `json:"baseAssetPrecision"`
	QuoteAsset         string                   `json:"quoteAsset"`
	QuotePrecision     int                      `json:"quotePrecision"`
	Filters            []map[string]interface{} `json:"filters"`
}

type ExecutionReport struct {
	EventType        string `json:"e"`
	EventTime        int64  `json:"E"`
	Symbol           string `json:"s"`
	ClientOrderID    string `json:"c"`
	Side             string `json:"S"`
	OrderType        string `json:"o"`
	TimeInForce      string `json:"f"`
	Quantity         string `json:"q"`
	Price            string `json:"p"`
	StopPrice        string `json:"P"`
	IcebergQuantity  string `json:"F"`
	OrderListID      int64  `json:"g"` // -1
	OriginalClientID string `json:"C"` // ""
	ExecutionType    string `json:"x"` // NEW, CANCELED, replaced, REJECTED, TRADE, EXPIRED
	OrderStatus      string `json:"X"` // NEW, PARTIALLY_FILLED, FILLED, CANCELED, REJECTED, EXPIRED
	RejectReason     string `json:"r"`
	OrderID          int64  `json:"i"`
	LastExecutedQty  string `json:"l"`
	CumulativeQty    string `json:"z"`
	LastExecPrice    string `json:"L"`
	Commission       string `json:"n"`
	CommissionAsset  string `json:"N"`
	TransactTime     int64  `json:"T"`
	TradeID          int64  `json:"t"`
	Ignore1          int64  `json:"I"` // Ignore
	IsOrderWorking   bool   `json:"w"`
	IsMaker          bool   `json:"m"`
	Ignore2          bool   `json:"M"` // Ignore
	CreationTime     int64  `json:"O"`
	CumQuoteQty      string `json:"Z"`
	LastQuoteQty     string `json:"Y"`
	QuoteOrderQty    string `json:"Q"`
}
