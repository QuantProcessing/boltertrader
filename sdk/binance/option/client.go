package option

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/QuantProcessing/boltertrader/internal/mbx"
)

// Client is the REST client for Binance European Options (eapi.binance.com).
//
// Only the methods needed by the Tier 2 reference adapter are implemented
// here: chain/mark/positions/account + place/cancel/query orders.
type Client struct {
	BaseURL    string
	APIKey     string
	SecretKey  string
	HTTPClient *http.Client
	Logger     *zap.SugaredLogger
}

func NewClient() *Client {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	logger := zap.NewNop().Sugar().Named("binance-option")

	if proxyURL := os.Getenv("PROXY"); proxyURL != "" {
		if parsed, err := url.Parse(proxyURL); err == nil {
			httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(parsed)}
			logger.Debugw("Using configured proxy")
		}
	}
	return &Client{
		BaseURL:    BaseURL,
		HTTPClient: httpClient,
		Logger:     logger,
	}
}

func (c *Client) WithCredentials(apiKey, secretKey string) *Client {
	c.APIKey = apiKey
	c.SecretKey = secretKey
	return c
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	c.HTTPClient = httpClient
	return c
}

// call dispatches an HTTP request with optional signing. Params are URL-encoded;
// signing appends timestamp + HMAC-SHA256 signature.
func (c *Client) call(ctx context.Context, method, endpoint string, params map[string]any, signed bool, out any) error {
	u, err := url.Parse(c.BaseURL + endpoint)
	if err != nil {
		return err
	}

	values := url.Values{}
	for k, v := range params {
		values.Set(k, fmt.Sprintf("%v", v))
	}

	if signed {
		values.Set("timestamp", strconv.FormatInt(Timestamp(), 10))
		query := values.Encode()
		signature := GenerateSignature(c.SecretKey, query)
		u.RawQuery = query + "&signature=" + signature
	} else {
		u.RawQuery = values.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return err
	}
	if c.APIKey != "" {
		req.Header.Set("X-MBX-APIKEY", c.APIKey)
	}

	c.Logger.Debugw("eapi request", "method", method, "path", u.Path)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("binance option %s %s transport failed: %w", method, u.Path, mbx.TransportCause(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 400 {
		var apiErr ErrorResponse
		if jerr := json.Unmarshal(body, &apiErr); jerr == nil && apiErr.Code != 0 {
			return fmt.Errorf("eapi error %d: %s", apiErr.Code, apiErr.Msg)
		}
		return fmt.Errorf("eapi HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("eapi decode: %w (body=%s)", err, strings.TrimSpace(string(body)))
	}
	return nil
}

// =============================================================================
// Public market data
// =============================================================================

func (c *Client) GetExchangeInfo(ctx context.Context) (*ExchangeInfoResponse, error) {
	out := &ExchangeInfoResponse{}
	if err := c.call(ctx, http.MethodGet, EndpointExchangeInfo, nil, false, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetMark returns mark info. If symbol is empty, all option symbols are returned.
func (c *Client) GetMark(ctx context.Context, symbol string) (MarkResponse, error) {
	params := map[string]any{}
	if symbol != "" {
		params["symbol"] = symbol
	}
	var out MarkResponse
	if err := c.call(ctx, http.MethodGet, EndpointMark, params, false, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// =============================================================================
// Account / positions
// =============================================================================

func (c *Client) GetAccount(ctx context.Context) (*AccountResponse, error) {
	out := &AccountResponse{}
	if err := c.call(ctx, http.MethodGet, EndpointAccount, nil, true, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetPositions returns open option positions. symbol filter is optional.
func (c *Client) GetPositions(ctx context.Context, symbol string) (PositionResponse, error) {
	params := map[string]any{}
	if symbol != "" {
		params["symbol"] = symbol
	}
	var out PositionResponse
	if err := c.call(ctx, http.MethodGet, EndpointPosition, params, true, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// =============================================================================
// Orders
// =============================================================================

func (c *Client) PlaceOrder(ctx context.Context, req *OrderRequest) (*OrderResponse, error) {
	params := map[string]any{
		"symbol":   req.Symbol,
		"side":     req.Side,
		"type":     req.Type,
		"quantity": req.Quantity,
	}
	if req.Price != "" {
		params["price"] = req.Price
	}
	if req.TimeInForce != "" {
		params["timeInForce"] = req.TimeInForce
	}
	if req.ReduceOnly {
		params["reduceOnly"] = "true"
	}
	if req.PostOnly {
		params["postOnly"] = "true"
	}
	if req.ClientOrderID != "" {
		params["clientOrderId"] = req.ClientOrderID
	}
	out := &OrderResponse{}
	if err := c.call(ctx, http.MethodPost, EndpointOrder, params, true, out); err != nil {
		return nil, err
	}
	return out, nil
}

// CancelOrder cancels either by orderID or clientOrderID (one is required).
func (c *Client) CancelOrder(ctx context.Context, symbol, orderID, clientOrderID string) (*OrderResponse, error) {
	params := map[string]any{"symbol": symbol}
	if orderID != "" {
		params["orderId"] = orderID
	}
	if clientOrderID != "" {
		params["clientOrderId"] = clientOrderID
	}
	out := &OrderResponse{}
	if err := c.call(ctx, http.MethodDelete, EndpointOrder, params, true, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetOrder(ctx context.Context, symbol, orderID, clientOrderID string) (*OrderResponse, error) {
	params := map[string]any{"symbol": symbol}
	if orderID != "" {
		params["orderId"] = orderID
	}
	if clientOrderID != "" {
		params["clientOrderId"] = clientOrderID
	}
	out := &OrderResponse{}
	if err := c.call(ctx, http.MethodGet, EndpointOrder, params, true, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetOpenOrders(ctx context.Context, symbol string) ([]OrderResponse, error) {
	params := map[string]any{}
	if symbol != "" {
		params["symbol"] = symbol
	}
	var out []OrderResponse
	if err := c.call(ctx, http.MethodGet, EndpointOpenOrders, params, true, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// =============================================================================
// User data stream (listenKey)
// =============================================================================

type ListenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

func (c *Client) CreateListenKey(ctx context.Context) (string, error) {
	out := &ListenKeyResponse{}
	if err := c.call(ctx, http.MethodPost, EndpointListenKey, nil, true, out); err != nil {
		return "", err
	}
	return out.ListenKey, nil
}

func (c *Client) KeepAliveListenKey(ctx context.Context, key string) error {
	params := map[string]any{"listenKey": key}
	return c.call(ctx, http.MethodPut, EndpointListenKey, params, true, nil)
}

func (c *Client) CloseListenKey(ctx context.Context, key string) error {
	params := map[string]any{"listenKey": key}
	return c.call(ctx, http.MethodDelete, EndpointListenKey, params, true, nil)
}
