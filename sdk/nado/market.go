package nado

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// GetAssets returns the list of assets from V2 Assets endpoint.
func (c *Client) GetAssets(ctx context.Context) ([]AssetV2, error) {
	var assets []AssetV2
	if err := c.QueryGatewayV2(ctx, "/assets", nil, &assets); err != nil {
		return nil, err
	}
	return assets, nil
}

// GetPairs returns the list of products from V2 Pairs endpoint.
func (c *Client) GetPairs(ctx context.Context, market *string) ([]PairV2, error) {
	q := url.Values{}
	if market != nil {
		q.Set("market", *market)
	}

	var pairs []PairV2
	if err := c.QueryGatewayV2(ctx, "/pairs", q, &pairs); err != nil {
		return nil, err
	}
	return pairs, nil
}

// GetApr returns the APRs from V2 APR endpoint.
func (c *Client) GetApr(ctx context.Context) ([]AprV2, error) {
	var aps []AprV2
	if err := c.QueryGatewayV2(ctx, "/apr", nil, &aps); err != nil {
		return nil, err
	}
	return aps, nil
}

// GetOrderBook returns the orderbook for a ticker using V2.
func (c *Client) GetOrderBook(ctx context.Context, tickerID string, depth int) (*OrderBookV2, error) {
	q := url.Values{}
	q.Set("ticker_id", tickerID)
	q.Set("depth", strconv.Itoa(depth))

	var ob OrderBookV2
	if err := c.QueryGatewayV2(ctx, "/orderbook", q, &ob); err != nil {
		return nil, err
	}
	return &ob, nil
}

// GetTickers
func (c *Client) GetTickers(ctx context.Context, market MarketType, edge *bool) (TickerV2Map, error) {
	q := url.Values{}
	q.Set("market", string(market))

	if edge != nil {
		q.Set("edge", strconv.FormatBool(*edge))
	}
	var tickers TickerV2Map
	if err := c.QueryArchiveV2(ctx, "/tickers", q, &tickers); err != nil {
		return nil, err
	}
	return tickers, nil
}

// GetContracts returns the list of contracts from V2 Contracts endpoint.
func (c *Client) GetContracts(ctx context.Context, edge *bool) (ContractV2Map, error) {
	q := url.Values{}
	if edge != nil {
		q.Set("edge", strconv.FormatBool(*edge))
	}
	var contracts ContractV2Map
	if err := c.QueryArchiveV2(ctx, "/contracts", q, &contracts); err != nil {
		return nil, err
	}
	return contracts, nil
}

func (c *Client) GetTrades(ctx context.Context, tickerID string, limit *int, maxTradeID *int64) ([]TradeV2, error) {
	q := url.Values{}
	q.Set("ticker_id", tickerID)
	if limit != nil {
		q.Set("limit", strconv.Itoa(*limit))
	}
	if maxTradeID != nil {
		q.Set("max_trade_id", strconv.FormatInt(*maxTradeID, 10))
	}
	var trades []TradeV2
	if err := c.QueryArchiveV2(ctx, "/trades", q, &trades); err != nil {
		return nil, err
	}
	return trades, nil
}

// v1 endpoints api

func (c *Client) GetMarketPrice(ctx context.Context, productID int64) (*MarketPrice, error) {
	req := map[string]interface{}{
		"type":       "market_price",
		"product_id": productID,
	}
	data, err := c.QueryGateWayV1(ctx, "POST", req)
	if err != nil {
		return nil, err
	}
	var resp MarketPrice
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetMarketPrices(ctx context.Context, productID []int) ([]MarketPrice, error) {
	req := map[string]interface{}{
		"type":        "market_prices",
		"product_ids": productID,
	}
	data, err := c.QueryGateWayV1(ctx, "POST", req)
	if err != nil {
		return nil, err
	}
	var resp MarketPricesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return resp.MarketPrices, nil
}

func (c *Client) GetNonces(ctx context.Context) (*Nonce, error) {
	if c.Signer == nil {
		return nil, ErrCredentialsRequired
	}
	req := map[string]interface{}{
		"type":    "nonces",
		"address": c.address,
	}
	data, err := c.QueryGateWayV1(ctx, "GET", req)
	if err != nil {
		return nil, err
	}
	var resp Nonce
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetMarketLiquidity(ctx context.Context, productID int64, depth int) (*MarketLiquidity, error) {
	if depth > 100 || depth <= 0 {
		return nil, errors.New("depth must be between 1 and 100")
	}
	req := map[string]interface{}{
		"type":       "market_liquidity",
		"product_id": productID,
		"depth":      depth,
	}
	data, err := c.QueryGateWayV1(ctx, "GET", req)
	if err != nil {
		return nil, err
	}
	var resp MarketLiquidity
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetSymbols(ctx context.Context, productType *string) (*SymbolsInfo, error) {
	q := map[string]interface{}{
		"type": "symbols",
	}
	if productType != nil {
		q["product_type"] = *productType
	}
	data, err := c.QueryGateWayV1(ctx, "GET", q)
	if err != nil {
		return nil, err
	}
	var resp SymbolsInfo
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetAllProducts(ctx context.Context) (*AllProductsResponse, error) {
	req := map[string]interface{}{
		"type": "all_products",
	}
	data, err := c.QueryGateWayV1(ctx, http.MethodGet, req)
	if err != nil {
		return nil, err
	}
	var resp AllProductsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetStatus(ctx context.Context) (SequencerStatus, error) {
	data, err := c.QueryGateWayV1(ctx, http.MethodGet, map[string]interface{}{"type": "status"})
	if err != nil {
		return "", err
	}
	var status SequencerStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return "", fmt.Errorf("decode sequencer status: %w", err)
	}
	return status, nil
}

func (c *Client) QuerySymbols(ctx context.Context, req SymbolsRequest) (*SymbolsInfo, error) {
	payload := map[string]interface{}{
		"type": "symbols",
	}
	if req.ProductType != "" {
		payload["product_type"] = string(req.ProductType)
	}
	method := http.MethodGet
	if len(req.ProductIDs) > 0 {
		payload["product_ids"] = req.ProductIDs
		method = http.MethodPost
	}
	data, err := c.QueryGateWayV1(ctx, method, payload)
	if err != nil {
		return nil, err
	}
	var resp SymbolsInfo
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetContractsV1(ctx context.Context) (*ContractV1, error) {
	req := map[string]interface{}{
		"type": "contracts",
	}
	data, err := c.QueryGateWayV1(ctx, "GET", req)
	if err != nil {
		return nil, err
	}
	var resp ContractV1
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetMaxOrderSize(ctx context.Context, req MaxOrderSizeRequest) (*MaxOrderSizeResponse, error) {
	if req.Direction != OrderDirectionLong && req.Direction != OrderDirectionShort {
		return nil, fmt.Errorf("nado max order size: invalid direction %q", req.Direction)
	}
	payload := map[string]interface{}{
		"type":       "max_order_size",
		"product_id": req.ProductID,
		"sender":     req.Sender,
		"price_x18":  req.PriceX18,
		"direction":  string(req.Direction),
	}
	if req.AvgPriceX18 != "" {
		payload["avg_price_x18"] = req.AvgPriceX18
	}
	if req.SpotLeverage != nil {
		payload["spot_leverage"] = strconv.FormatBool(*req.SpotLeverage)
	}
	if req.ReduceOnly != nil {
		payload["reduce_only"] = strconv.FormatBool(*req.ReduceOnly)
	}
	if req.Isolated != nil {
		payload["isolated"] = strconv.FormatBool(*req.Isolated)
	}
	if req.BorrowMargin != nil {
		payload["borrow_margin"] = strconv.FormatBool(*req.BorrowMargin)
	}

	data, err := c.QueryGateWayV1(ctx, http.MethodGet, payload)
	if err != nil {
		return nil, err
	}
	var resp MaxOrderSizeResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetCandlesticks queries historical candlesticks from the archive indexer.
func (c *Client) GetCandlesticks(ctx context.Context, req CandlestickRequest) ([]ArchiveCandlestick, error) {
	data, err := c.QueryArchiveV1(ctx, req)
	if err != nil {
		return nil, err
	}
	var resp CandlestickResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return resp.Candlesticks, nil
}

// GetFundingRate retrieves the raw funding rate response for a specific product.
func (c *Client) GetFundingRate(ctx context.Context, productID int64) (*FundingRateResponse, error) {
	req := map[string]interface{}{
		"funding_rate": map[string]interface{}{
			"product_id": productID,
		},
	}
	data, err := c.QueryArchiveV1(ctx, req)
	if err != nil {
		return nil, err
	}

	var fundingResp FundingRateResponse
	if err := json.Unmarshal(data, &fundingResp); err != nil {
		return nil, err
	}
	if fundingResp.ProductID == 0 {
		fundingResp.ProductID = productID
	}
	return &fundingResp, nil
}

// GetPerpPrice returns the archive indexer's exact mark and index prices for one
// perpetual product. The SDK deliberately preserves the native x18 strings.
func (c *Client) GetPerpPrice(ctx context.Context, productID int64) (*PerpPriceResponse, error) {
	req := map[string]interface{}{
		"price": map[string]interface{}{
			"product_id": productID,
		},
	}
	data, err := c.QueryArchiveV1(ctx, req)
	if err != nil {
		return nil, err
	}

	var price PerpPriceResponse
	if err := json.Unmarshal(data, &price); err != nil {
		return nil, err
	}
	return &price, nil
}

// GetOraclePrices returns exact oracle prices and source update times for the
// requested products.
func (c *Client) GetOraclePrices(ctx context.Context, productIDs []int64) ([]OraclePriceResponse, error) {
	req := map[string]interface{}{
		"oracle_price": map[string]interface{}{
			"product_ids": productIDs,
		},
	}
	data, err := c.QueryArchiveV1(ctx, req)
	if err != nil {
		return nil, err
	}

	var response oraclePricesResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return response.Prices, nil
}

// GetAllFundingRates retrieves raw funding rates for all perp products.
func (c *Client) GetAllFundingRates(ctx context.Context) (map[string]FundingRateResponse, error) {
	// First, get all perp symbols to find product IDs
	perpType := "perp"
	symbols, err := c.GetSymbols(ctx, &perpType)
	if err != nil {
		return nil, err
	}

	// Collect all product IDs
	var productIDs []int64
	for _, sym := range symbols.Symbols {
		if sym.Type == "perp" {
			productIDs = append(productIDs, int64(sym.ProductID))
		}
	}

	if len(productIDs) == 0 {
		return map[string]FundingRateResponse{}, nil
	}

	// Query all funding rates
	req := map[string]interface{}{
		"funding_rates": map[string]interface{}{
			"product_ids": productIDs,
		},
	}
	data, err := c.QueryArchiveV1(ctx, req)
	if err != nil {
		return nil, err
	}

	// Response is a map: product_id -> FundingRateResponse
	var fundingMap map[string]FundingRateResponse
	if err := json.Unmarshal(data, &fundingMap); err != nil {
		return nil, err
	}

	for productIDStr, fundingResp := range fundingMap {
		productID, err := strconv.ParseInt(productIDStr, 10, 64)
		if err != nil {
			continue
		}
		if fundingResp.ProductID == 0 {
			fundingResp.ProductID = productID
		}
		fundingMap[productIDStr] = fundingResp
	}

	return fundingMap, nil
}
