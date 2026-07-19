package lighter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (c *Client) getJSON(ctx context.Context, path string, params url.Values, auth bool, out any) error {
	if params != nil {
		if encoded := params.Encode(); encoded != "" {
			path = fmt.Sprintf("%s?%s", path, encoded)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	if auth {
		token, err := c.CreateAuthToken(time.Now().Add(10 * time.Minute))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("http error %d", resp.StatusCode)
	}
	if len(data) == 0 {
		return fmt.Errorf("%w: empty response body", ErrMalformedResponse)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: invalid JSON response", ErrMalformedResponse)
	}
	return nil
}

// GetAssetDetails fetches asset details
func (c *Client) GetAssetDetails(ctx context.Context, assetIndex *int16) (*AssetDetailsResponse, error) {
	path := "/api/v1/assetDetails"
	if assetIndex != nil {
		path = fmt.Sprintf("%s?asset_index=%d", path, *assetIndex)
	}
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

	var res AssetDetailsResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get asset details: %s", res.Msg)
	}
	return &res, nil
}

// GetOrderBookDetails fetches order book details
func (c *Client) GetOrderBookDetails(ctx context.Context, marketId *int, filter *string) (*OrderBookDetailsResponse, error) {
	params := url.Values{}
	if marketId != nil {
		params.Add("market_id", fmt.Sprintf("%d", *marketId))
	}
	if filter != nil {
		params.Add("filter", *filter)
	}

	path := "/api/v1/orderBookDetails"
	if queryString := params.Encode(); queryString != "" {
		path = fmt.Sprintf("%s?%s", path, queryString)
	}
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

	var res OrderBookDetailsResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("%w: invalid order-book-details response", ErrMalformedResponse)
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("%w: invalid order-book-details response code", ErrMalformedResponse)
	}
	return &res, nil
}

// GetOrderBooks fetches order books
func (c *Client) GetOrderBooks(ctx context.Context, marketId *int) (*OrderBooksResponse, error) {
	path := "/api/v1/orderBooks"
	if marketId != nil {
		path = fmt.Sprintf("%s?market_id=%d", path, *marketId)
	}
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

	var res OrderBooksResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get order books: %s", res.Msg)
	}
	return &res, nil
}

// GetRecentTrades fetches recent trades
func (c *Client) GetRecentTrades(ctx context.Context, marketId int, limit int64) (*RecentTradesResponse, error) {
	path := fmt.Sprintf("/api/v1/recentTrades?market_id=%d&limit=%d", marketId, limit)
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

	var res RecentTradesResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get recent trades: %s", res.Msg)
	}
	return &res, nil
}

// GetOrderBookOrders fetches detailed order book orders
func (c *Client) GetOrderBookOrders(ctx context.Context, marketId int, limit int64) (*OrderBookOrdersResponse, error) {
	path := fmt.Sprintf("/api/v1/orderBookOrders?market_id=%d&limit=%d", marketId, limit)
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

	var res OrderBookOrdersResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("%w: invalid order-book-orders response", ErrMalformedResponse)
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("%w: invalid order-book-orders response code", ErrMalformedResponse)
	}
	return &res, nil
}

// GetFundingRates fetches current funding rates
func (c *Client) GetFundingRates(ctx context.Context) (*FundingRatesResponse, error) {
	path := "/api/v1/funding-rates"
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

	var res FundingRatesResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get funding rates: %s", res.Msg)
	}
	return &res, nil
}

// GetFundingRate retrieves the raw funding-rate row for a specific market id.
func (c *Client) GetFundingRate(ctx context.Context, marketId int) (*FundingRate, error) {
	rates, err := c.GetFundingRates(ctx)
	if err != nil {
		return nil, err
	}
	if rates.Code != 200 {
		return nil, fmt.Errorf("failed to get funding rate: %s", rates.Msg)
	}

	// Find the rate for the requested market id
	for _, rate := range rates.FundingRate {
		if rate.MarketId == marketId && strings.EqualFold(rate.Exchange, "lighter") {
			return rate, nil
		}
	}

	return nil, fmt.Errorf("funding rate not found for market id: %d", marketId)
}

// GetAllFundingRates retrieves raw funding-rate rows for all Lighter markets.
func (c *Client) GetAllFundingRates(ctx context.Context) ([]*FundingRate, error) {
	rates, err := c.GetFundingRates(ctx)
	if err != nil {
		return nil, err
	}
	if rates.Code != 200 {
		return nil, fmt.Errorf("failed to get funding rates: %s", rates.Msg)
	}

	var result []*FundingRate
	for _, rate := range rates.FundingRate {
		if !strings.EqualFold(rate.Exchange, "lighter") {
			continue
		}
		result = append(result, rate)
	}

	return result, nil
}

// GetExchangeStats fetches exchange statistics
func (c *Client) GetExchangeStats(ctx context.Context) (*ExchangeStatsResponse, error) {
	path := "/api/v1/exchangeStats"
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

	var res ExchangeStatsResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// GetCandlesticks fetches candles.
func (c *Client) GetCandlesticks(ctx context.Context, marketId int, resolution string, startTimestamp, endTimestamp, countBack int64) (*CandlesticksResponse, error) {
	params := url.Values{}
	params.Set("market_id", fmt.Sprintf("%d", marketId))
	params.Set("resolution", resolution)
	params.Set("start_timestamp", fmt.Sprintf("%d", startTimestamp))
	params.Set("end_timestamp", fmt.Sprintf("%d", endTimestamp))
	params.Set("count_back", fmt.Sprintf("%d", countBack))

	var res CandlesticksResponse
	if err := c.getJSON(ctx, "/api/v1/candles", params, false, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("%w: invalid candlesticks response code", ErrMalformedResponse)
	}
	return &res, nil
}

// GetFundingHistory fetches historical funding rates.
func (c *Client) GetFundingHistory(ctx context.Context, marketId int, resolution string, startTimestamp, endTimestamp, countBack int64) (*FundingHistoryResponse, error) {
	params := url.Values{}
	params.Set("market_id", fmt.Sprintf("%d", marketId))
	params.Set("resolution", resolution)
	params.Set("start_timestamp", fmt.Sprintf("%d", startTimestamp))
	params.Set("end_timestamp", fmt.Sprintf("%d", endTimestamp))
	params.Set("count_back", fmt.Sprintf("%d", countBack))
	var res FundingHistoryResponse
	if err := c.getJSON(ctx, "/api/v1/fundings", params, false, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get funding history: %s", res.Msg)
	}
	return &res, nil
}

// GetTransferFeeInfo fetches authenticated transfer fee info.
func (c *Client) GetTransferFeeInfo(ctx context.Context, toAccountIndex *int64) (*TransferFeeInfoResponse, error) {
	params := url.Values{}
	params.Set("account_index", fmt.Sprintf("%d", c.AccountIndex))
	if toAccountIndex != nil {
		params.Set("to_account_index", fmt.Sprintf("%d", *toAccountIndex))
	}
	var res TransferFeeInfoResponse
	if err := c.getJSON(ctx, "/api/v1/transferFeeInfo", params, true, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get transfer fee info: %s", res.Msg)
	}
	return &res, nil
}

// GetWithdrawalDelay fetches withdrawal delay.
func (c *Client) GetWithdrawalDelay(ctx context.Context) (*WithdrawalDelayResponse, error) {
	var res WithdrawalDelayResponse
	if err := c.getJSON(ctx, "/api/v1/withdrawalDelay", nil, false, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// GetAnnouncements fetches announcements
func (c *Client) GetAnnouncements(ctx context.Context) (*AnnouncementResponse, error) {
	path := "/api/v1/announcement"
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

	var res AnnouncementResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get announcements: %s", res.Msg)
	}
	return &res, nil
}

// GetL1Metadata fetches authenticated L1 metadata for an address.
func (c *Client) GetL1Metadata(ctx context.Context, l1Address string) (*L1MetadataResponse, error) {
	params := url.Values{}
	params.Set("l1_address", l1Address)
	var res L1MetadataResponse
	if err := c.getJSON(ctx, "/api/v1/l1Metadata", params, true, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// GetPublicPoolsMetadata fetches public pools metadata.
func (c *Client) GetPublicPoolsMetadata(ctx context.Context, filter string, index, limit int64, accountIndex *int64) (*PublicPoolsMetadataResponse, error) {
	params := url.Values{}
	if filter != "" {
		params.Set("filter", filter)
	}
	params.Set("index", fmt.Sprintf("%d", index))
	params.Set("limit", fmt.Sprintf("%d", limit))
	auth := false
	if accountIndex != nil {
		params.Set("account_index", fmt.Sprintf("%d", *accountIndex))
		auth = true
	}
	var res PublicPoolsMetadataResponse
	if err := c.getJSON(ctx, "/api/v1/publicPoolsMetadata", params, auth, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("failed to get public pools metadata: %s", res.Msg)
	}
	return &res, nil
}
