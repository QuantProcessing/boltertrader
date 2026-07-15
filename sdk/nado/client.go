package nado

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
)

type Client struct {
	profile    Profile
	client     *http.Client
	now        func() time.Time
	address    string
	subaccount string
	Signer     *Signer

	contractsMu sync.Mutex

	discoveryMu sync.Mutex
}

func NewClient(profile Profile) (*Client, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	return &Client{
		profile: profile,
		client: &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: rejectNadoRedirect,
		},
		now: time.Now,
	}, nil
}

func (c *Client) Profile() Profile { return c.profile }

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	if httpClient != nil {
		clone := *httpClient
		clone.CheckRedirect = rejectNadoRedirect
		c.client = &clone
	}
	return c
}

func (c *Client) WithClock(now func() time.Time) *Client {
	if now != nil {
		c.now = now
	}
	return c
}

func rejectNadoRedirect(_ *http.Request, _ []*http.Request) error {
	return fmt.Errorf("nado client: redirects are disabled")
}

func (c *Client) WithCredentials(privateKey, subaccount string) (*Client, error) {
	if len([]byte(subaccount)) > 12 {
		return nil, fmt.Errorf("nado credentials: subaccount name exceeds 12 bytes")
	}
	signer, err := NewSigner(privateKey, c.profile.ChainID())
	if err != nil {
		return nil, err
	}
	c.subaccount = subaccount
	c.Signer = signer
	c.address = signer.GetAddress().String()
	return c, nil
}

// Sender returns the exact bytes32 wallet/subaccount identity configured on
// this client. Logical runtime account IDs never enter this value.
func (c *Client) Sender() (string, error) {
	if c.Signer == nil {
		return "", ErrCredentialsRequired
	}
	return BuildSender(c.Signer.GetAddress(), c.subaccount), nil
}

func (c *Client) ensureContracts(ctx context.Context) (*ContractV1, error) {
	c.contractsMu.Lock()
	defer c.contractsMu.Unlock()

	discovered, err := c.GetContractsV1(ctx)
	if err != nil {
		return nil, fmt.Errorf("nado contracts discovery: %w", err)
	}
	if err := c.ValidateContractV1(*discovered); err != nil {
		return nil, fmt.Errorf("nado contracts discovery: %w", err)
	}

	return discovered, nil
}

func (c *Client) ResolveProduct(ctx context.Context, productID int64) (DiscoveredProduct, error) {
	c.discoveryMu.Lock()
	defer c.discoveryMu.Unlock()

	status, err := c.GetStatus(ctx)
	if err != nil {
		return DiscoveredProduct{}, fmt.Errorf("nado product discovery: status: %w", err)
	}
	if status != SequencerStatusActive {
		return DiscoveredProduct{}, fmt.Errorf("%w: status %q", ErrNadoSequencerInactive, status)
	}
	products, err := c.GetAllProducts(ctx)
	if err != nil {
		return DiscoveredProduct{}, fmt.Errorf("nado product discovery: all_products: %w", err)
	}
	symbols, err := c.QuerySymbols(ctx, SymbolsRequest{})
	if err != nil {
		return DiscoveredProduct{}, fmt.Errorf("nado product discovery: symbols: %w", err)
	}
	if err := ValidateNadoProductDiscovery(*products, *symbols); err != nil {
		return DiscoveredProduct{}, err
	}

	var resolved DiscoveredProduct
	found := false
	for _, product := range products.SpotProducts {
		if product.ProductID == productID {
			resolved = DiscoveredProduct{
				ProductID: product.ProductID, ProductType: MarketTypeSpot,
				Symbol: symbolByProduct(*symbols, product.ProductID, MarketTypeSpot), BookInfo: product.BookInfo,
			}
			found = true
			break
		}
	}
	if !found {
		for _, product := range products.PerpProducts {
			if product.ProductID == productID {
				resolved = DiscoveredProduct{
					ProductID: product.ProductID, ProductType: MarketTypePerp,
					Symbol: symbolByProduct(*symbols, product.ProductID, MarketTypePerp), BookInfo: product.BookInfo,
				}
				found = true
				break
			}
		}
	}
	if !found {
		return DiscoveredProduct{}, fmt.Errorf("%w: product_id %d", ErrNadoDiscoveryUnknownProduct, productID)
	}
	if !isKnownNadoTradingStatus(resolved.Symbol.TradingStatus) || resolved.Symbol.TradingStatus == TradingStatusNotTradable {
		return DiscoveredProduct{}, fmt.Errorf("%w: product_id %d status %q", ErrNadoDiscoveryInactiveProduct, productID, resolved.Symbol.TradingStatus)
	}
	return resolved, nil
}

func symbolByProduct(symbols SymbolsInfo, productID int64, productType MarketType) Symbol {
	for _, symbol := range symbols.Symbols {
		if int64(symbol.ProductID) == productID && symbol.Type == string(productType) {
			return symbol
		}
	}
	return Symbol{}
}

// Execute sends a POST request (V1) for execution/transaction endpoints.
func (c *Client) Execute(ctx context.Context, reqBody interface{}) ([]byte, error) {
	if c.Signer == nil {
		return nil, ErrCredentialsRequired
	}
	if _, err := c.ensureContracts(ctx); err != nil {
		return nil, err
	}
	return c.execute(ctx, reqBody)
}

func (c *Client) execute(ctx context.Context, reqBody interface{}) ([]byte, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		jsonBytes, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBytes)
	}

	u := c.profile.GatewayV1URL() + "/execute"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perform request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, sdkcore.NewExchangeError("NADO", "429", strings.TrimSpace(string(respBytes)), sdkcore.ErrRateLimited)
		}
		return nil, fmt.Errorf("api error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	var apiResp ApiV1Response
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if apiResp.Status != "success" || apiResp.Error != "" {
		requestType := apiResp.RequestType
		if requestType == "" {
			if request, ok := reqBody.(map[string]interface{}); ok {
				requestType = gatewayRequestType(request)
			}
		}
		return nil, NewGatewayApplicationError(apiResp.ErrorCode, apiResp.Error, requestType)
	}

	return apiResp.Data, nil
}

// QueryV1 v1 endpoints api: support get and post method
func (c *Client) QueryGateWayV1(ctx context.Context, method string, req map[string]interface{}) ([]byte, error) {
	var (
		data []byte
	)
	switch method {
	case http.MethodGet:
		// get request: url.Values
		u, err := url.Parse(c.profile.GatewayV1URL() + "/query")
		if err != nil {
			return nil, err
		}
		q := url.Values{}
		for k, v := range req {
			q.Set(k, fmt.Sprintf("%v", v))
		}
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			if resp.StatusCode == http.StatusTooManyRequests {
				return nil, sdkcore.NewExchangeError("NADO", "429", strings.TrimSpace(string(data)), sdkcore.ErrRateLimited)
			}
			return nil, fmt.Errorf("api v1 error (status %d): %s", resp.StatusCode, string(data))
		}
	case http.MethodPost:
		// post request: json
		jsonBytes, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.profile.GatewayV1URL()+"/query", bytes.NewReader(jsonBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			if resp.StatusCode == http.StatusTooManyRequests {
				return nil, sdkcore.NewExchangeError("NADO", "429", strings.TrimSpace(string(data)), sdkcore.ErrRateLimited)
			}
			return nil, fmt.Errorf("api v1 error (status %d): %s", resp.StatusCode, string(data))
		}
	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}

	var apiResp ApiV1Response
	if err := json.Unmarshal(data, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response v1: %w", err)
	}

	if apiResp.Status != "success" || apiResp.Error != "" {
		return nil, NewGatewayApplicationError(apiResp.ErrorCode, apiResp.Error, apiResp.RequestType)
	}

	return apiResp.Data, nil
}

// QueryGatewayV2 v2 endpoints api: only support get method
func (c *Client) QueryGatewayV2(ctx context.Context, path string, params url.Values, dest interface{}) error {
	u, err := url.Parse(c.profile.GatewayV2URL() + path)
	if err != nil {
		return err
	}
	if params != nil {
		u.RawQuery = params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response v2: %w", err)
	}

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("rate limited: %w", sdkcore.NewExchangeError("NADO", "429", strings.TrimSpace(string(respBytes)), sdkcore.ErrRateLimited))
		}
		return fmt.Errorf("api v2 error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	if err := json.Unmarshal(respBytes, dest); err != nil {
		return fmt.Errorf("unmarshal response v2: %w", err)
	}
	return nil
}

// QueryArchiveV1 v1 endpoints api: only support get method
func (c *Client) QueryArchiveV1(ctx context.Context, params interface{}) (data []byte, err error) {
	jsonBytes, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.profile.ArchiveV1URL(), bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	reader := io.Reader(resp.Body)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Encoding")), "gzip") {
		compressed, gzipErr := gzip.NewReader(resp.Body)
		if gzipErr != nil {
			return nil, fmt.Errorf("open gzip archive response: %w", gzipErr)
		}
		defer compressed.Close()
		reader = compressed
	}
	data, err = io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, sdkcore.NewExchangeError("NADO", "429", strings.TrimSpace(string(data)), sdkcore.ErrRateLimited)
		}
		return nil, fmt.Errorf("api v1 error (status %d): %s", resp.StatusCode, string(data))
	}
	return data, nil
}

// QueryArchiveV2 v2 endpoints api: only support get method
func (c *Client) QueryArchiveV2(ctx context.Context, path string, params url.Values, dest interface{}) error {
	u, err := url.Parse(c.profile.ArchiveV2URL() + path)
	if err != nil {
		return err
	}
	if params != nil {
		u.RawQuery = params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response v2: %w", err)
	}

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("rate limited: %w", sdkcore.NewExchangeError("NADO", "429", strings.TrimSpace(string(respBytes)), sdkcore.ErrRateLimited))
		}
		return fmt.Errorf("api v2 error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	if err := json.Unmarshal(respBytes, dest); err != nil {
		return fmt.Errorf("unmarshal response v2: %w", err)
	}
	return nil
}
