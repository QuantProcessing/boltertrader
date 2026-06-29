package lighter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	p2 "github.com/elliottech/poseidon_crypto/hash/poseidon2_goldilocks"
	ethCommon "github.com/ethereum/go-ethereum/common"
)

// GetAccountActiveOrders fetches active orders for the account
func (c *Client) GetAccountActiveOrders(ctx context.Context, marketId int) (*AccountActiveOrdersResponse, error) {
	path := fmt.Sprintf("/api/v1/accountActiveOrders?account_index=%d&market_id=%d", c.AccountIndex, marketId)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	// Add Auth
	token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10))
	if err != nil {
		return nil, fmt.Errorf("failed to create auth token: %w", err)
	}
	req.Header.Set("Authorization", token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res AccountActiveOrdersResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get active orders: %s", res.Msg)
	}
	return &res, nil
}

// GetNextNonce fetches the next nonce for the account, using a local cache to handle high concurrency
func (c *Client) GetNextNonce(ctx context.Context) (int64, error) {
	c.nonceMu.Lock()
	defer c.nonceMu.Unlock()

	if c.nonceInit {
		nonce := c.nonce
		c.nonce++
		return nonce, nil
	}

	path := fmt.Sprintf("/api/v1/nextNonce?account_index=%d&api_key_index=%d", c.AccountIndex, c.KeyIndex)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return 0, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var res struct {
		Code  int    `json:"code"`
		Msg   string `json:"message"`
		Nonce int64  `json:"nonce"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return 0, err
	}
	if res.Code != 200 {
		return 0, fmt.Errorf("failed to get nonce: %s", res.Msg)
	}

	c.nonce = res.Nonce + 1
	c.nonceInit = true
	return res.Nonce, nil
}

// GetAccount fetches account information
func (c *Client) GetAccount(ctx context.Context) (*AccountResponse, error) {
	path := fmt.Sprintf("/api/v1/account?by=index&value=%d", c.AccountIndex)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	// Add Auth
	// token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10).Unix())
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create auth token: %w", err)
	// }
	// req.Header.Set("Authorization", token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res AccountResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get account: %s", res.Msg)
	}
	return &res, nil
}

// GetInactiveOrders fetches inactive orders
func (c *Client) GetInactiveOrders(ctx context.Context, marketId *int, limit int64) (*AccountInactiveOrdersResponse, error) {
	path := fmt.Sprintf("/api/v1/accountInactiveOrders?account_index=%d", c.AccountIndex)
	if marketId != nil {
		path = fmt.Sprintf("%s&market_id=%d", path, *marketId)
	}
	if limit > 0 {
		path = fmt.Sprintf("%s&limit=%d", path, limit)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	// Add Auth
	token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10))
	if err != nil {
		return nil, fmt.Errorf("failed to create auth token: %w", err)
	}
	req.Header.Set("Authorization", token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res AccountInactiveOrdersResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get inactive orders: %s", res.Msg)
	}
	return &res, nil
}

// GetTrades fetches account trades.
func (c *Client) GetTrades(ctx context.Context, req TradesRequest) (*TradesResponse, error) {
	accountIndex := c.AccountIndex
	if req.AccountIndex != nil {
		accountIndex = *req.AccountIndex
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	sortBy := req.SortBy
	if sortBy == "" {
		sortBy = "trade_id"
	}
	sortDir := req.SortDir
	if sortDir == "" {
		sortDir = "desc"
	}
	tradeType := req.Type
	if tradeType == "" {
		tradeType = "all"
	}
	marketType := req.MarketType
	if marketType == "" {
		marketType = "all"
	}

	params := url.Values{}
	params.Set("account_index", strconv.FormatInt(accountIndex, 10))
	params.Set("sort_by", sortBy)
	params.Set("sort_dir", sortDir)
	params.Set("type", tradeType)
	params.Set("limit", strconv.FormatInt(limit, 10))
	params.Set("market_type", marketType)
	if req.MarketID != nil {
		params.Set("market_id", strconv.Itoa(*req.MarketID))
	}
	if req.OrderIndex != nil {
		params.Set("order_index", strconv.FormatInt(*req.OrderIndex, 10))
	}
	if req.Cursor != "" {
		params.Set("cursor", req.Cursor)
	}
	if req.From != 0 {
		params.Set("from", strconv.FormatInt(req.From, 10))
	}
	if req.AskFilter != nil {
		params.Set("ask_filter", strconv.FormatInt(int64(*req.AskFilter), 10))
	}
	if req.Role != "" {
		params.Set("role", req.Role)
	}
	if req.Aggregate {
		params.Set("aggregate", "true")
	}
	if req.SkipAskOrderID != "" {
		params.Set("skip_ask_order_id", req.SkipAskOrderID)
	}
	if req.SkipBidOrderID != "" {
		params.Set("skip_bid_order_id", req.SkipBidOrderID)
	}

	var res TradesResponse
	if err := c.getJSON(ctx, "/api/v1/trades", params, true, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get trades: %s", res.Msg)
	}
	return &res, nil
}

// GetAccountTxs fetches account transactions
func (c *Client) GetAccountTxs(ctx context.Context, limit int64) (*AccountTxsResponse, error) {
	path := fmt.Sprintf("/api/v1/accountTxs?by=account_index&value=%d&limit=%d", c.AccountIndex, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	// Add Auth
	token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10))
	if err != nil {
		return nil, fmt.Errorf("failed to create auth token: %w", err)
	}
	req.Header.Set("Authorization", token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res AccountTxsResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get account txs: %s", res.Msg)
	}
	return &res, nil
}

// GetPnL fetches account PnL
func (c *Client) GetPnL(ctx context.Context, startTimestamp, endTimestamp int64) (*PnlResponse, error) {
	path := fmt.Sprintf("/api/v1/pnl?by=index&value=%d&resolution=1d&start_timestamp=%d&end_timestamp=%d", c.AccountIndex, startTimestamp, endTimestamp)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	// Add Auth
	token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10))
	if err != nil {
		return nil, fmt.Errorf("failed to create auth token: %w", err)
	}
	req.Header.Set("Authorization", token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res PnlResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get pnl: %s", res.Msg)
	}
	return &res, nil
}

// GetAccountLimits fetches account limits
func (c *Client) GetAccountLimits(ctx context.Context) (*AccountLimitsResponse, error) {
	path := fmt.Sprintf("/api/v1/accountLimits?account_index=%d", c.AccountIndex)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	// Add Auth
	token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10))
	if err != nil {
		return nil, fmt.Errorf("failed to create auth token: %w", err)
	}
	req.Header.Set("Authorization", token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res AccountLimitsResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get account limits: %s", res.Msg)
	}
	return &res, nil
}

// GetAccountMetadata fetches account metadata
func (c *Client) GetAccountMetadata(ctx context.Context) (*AccountMetadataResponse, error) {
	path := fmt.Sprintf("/api/v1/accountMetadata?by=index&value=%d", c.AccountIndex)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	// Add Auth
	token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10))
	if err != nil {
		return nil, fmt.Errorf("failed to create auth token: %w", err)
	}
	req.Header.Set("Authorization", token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res AccountMetadataResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ChangeAccountTier changes account tier
func (c *Client) ChangeAccountTier(ctx context.Context, newTier string) (*ChangeAccountTierResponse, error) {
	params := map[string]string{
		"account_index": fmt.Sprintf("%d", c.AccountIndex),
		"new_tier":      newTier,
	}

	data, err := c.PostForm(ctx, "/api/v1/changeAccountTier", params, true)
	if err != nil {
		return nil, err
	}

	var res ChangeAccountTierResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to change account tier: %s", res.Msg)
	}
	return &res, nil
}

// GetPositionFunding fetches position funding history
func (c *Client) GetPositionFunding(ctx context.Context, marketId *int, limit int64, side *string) (*PositionFundingResponse, error) {
	path := fmt.Sprintf("/api/v1/positionFunding?account_index=%d&limit=%d", c.AccountIndex, limit)
	if marketId != nil {
		path = fmt.Sprintf("%s&market_id=%d", path, *marketId)
	}
	if side != nil {
		path = fmt.Sprintf("%s&side=%s", path, *side)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	// Add Auth
	token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10))
	if err != nil {
		return nil, fmt.Errorf("failed to create auth token: %w", err)
	}
	req.Header.Set("Authorization", token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res PositionFundingResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get position funding: %s", res.Msg)
	}
	return &res, nil
}

// GetApiKeys fetches API keys
func (c *Client) GetApiKeys(ctx context.Context) (*ApiKeysResponse, error) {
	path := fmt.Sprintf("/api/v1/apikeys?account_index=%d", c.AccountIndex)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	// Add Auth
	token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10))
	if err != nil {
		return nil, fmt.Errorf("failed to create auth token: %w", err)
	}
	req.Header.Set("Authorization", token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res ApiKeysResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get api keys: %s", res.Msg)
	}
	return &res, nil
}

// GetReferralPoints fetches referral points
func (c *Client) GetReferralPoints(ctx context.Context) (*ReferralPointsResponse, error) {
	path := fmt.Sprintf("/api/v1/referral_points?account_index=%d", c.AccountIndex)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	// Add Auth
	token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10))
	if err != nil {
		return nil, fmt.Errorf("failed to create auth token: %w", err)
	}
	req.Header.Set("Authorization", token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res ReferralPointsResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get referral points: %s", res.Msg)
	}
	return &res, nil
}

// GetAccountsByL1Address fetches accounts by L1 address
func (c *Client) GetAccountsByL1Address(ctx context.Context, l1Address string) (*AccountsByL1AddressResponse, error) {
	path := fmt.Sprintf("/api/v1/accountsByL1Address?l1_address=%s", l1Address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res AccountsByL1AddressResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get accounts by L1 address: %s", res.Msg)
	}
	return &res, nil
}

// UpdateLeverage updates leverage
func (c *Client) UpdateLeverage(ctx context.Context, marketId int, leverage uint16, marginMode uint8) (*UpdateLeverageResponse, error) {
	nonce, err := c.GetNextNonce(ctx)
	if err != nil {
		return nil, err
	}

	imf := uint16(10000 / leverage)
	info := &UpdateLeverageInfo{
		AccountIndex:          c.AccountIndex,
		ApiKeyIndex:           uint32(c.KeyIndex),
		MarketIndex:           uint32(marketId),
		InitialMarginFraction: imf,
		MarginMode:            marginMode,
		Nonce:                 nonce,
		ExpiredAt:             time.Now().Add(time.Hour * 24 * 7).UnixMilli(), // Default expiry
	}

	hash, err := HashUpdateLeverage(c.ChainId, info)

	signature, err := c.KeyManager.Sign(hash, p2.NewPoseidon2())
	if err != nil {
		return nil, fmt.Errorf("failed to sign update leverage: %w", err)
	}

	payloadInfo := &UpdateLeveragePayload{
		UpdateLeverageInfo: info,
		Sig:                signature,
		SignedHash:         ethCommon.Bytes2Hex(hash),
	}

	jsonBytes, err := json.Marshal(payloadInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal update leverage info: %w", err)
	}

	params := map[string]string{
		"tx_type": fmt.Sprintf("%d", TxTypeUpdateLeverage),
		"tx_info": string(jsonBytes),
	}

	respData, err := c.PostForm(ctx, "/api/v1/sendTx", params, true)
	if err != nil {
		return nil, err
	}

	var res UpdateLeverageResponse
	if err := json.Unmarshal(respData, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to update leverage: %s", res.Msg)
	}
	return &res, nil
}
