package perp

import (
	"context"
	"encoding/json"
	"fmt"
)

// MetaAndAssetCtxsFull is the parsed form of the metaAndAssetCtxs response:
// element [0] is meta (universe list) and element [1] is the array of AssetContexts.
type MetaAndAssetCtxsFull struct {
	Meta struct {
		Universe []struct {
			Name string `json:"name"`
		} `json:"universe"`
	}
	AssetCtxs MetaAndAssetCtxsResponse
}

// GetMetaAndAssetCtxs fetches raw metaAndAssetCtxs from the /info endpoint and
// returns the parsed meta+asset-context pair. Both GetFundingRate and
// FetchOpenInterest use this shared helper to avoid duplicate POSTs.
func (c *Client) GetMetaAndAssetCtxs(ctx context.Context) (*MetaAndAssetCtxsFull, error) {
	return c.GetMetaAndAssetCtxsForDex(ctx, "")
}

func (c *Client) GetMetaAndAssetCtxsForDex(ctx context.Context, dex string) (*MetaAndAssetCtxsFull, error) {
	req := map[string]any{"type": "metaAndAssetCtxs"}
	if dex != "" {
		req["dex"] = dex
	}
	data, err := c.Post(ctx, "/info", req)
	if err != nil {
		return nil, err
	}

	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if len(raw) < 2 {
		return nil, fmt.Errorf("unexpected response format: expected 2 elements, got %d", len(raw))
	}

	var result MetaAndAssetCtxsFull
	if err := json.Unmarshal(raw[0], &result.Meta); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw[1], &result.AssetCtxs); err != nil {
		return nil, err
	}
	return &result, nil
}

// L2Book

type L2BookResponse struct {
	Coin   string      `json:"coin"`
	Levels [][]L2Level `json:"levels"`
	Time   int64       `json:"time"`
}

type L2Level struct {
	Px string `json:"px"`
	Sz string `json:"sz"`
	N  int    `json:"n"`
}

func (c *Client) L2Book(ctx context.Context, coin string) (*L2BookResponse, error) {
	req := map[string]string{
		"type": "l2Book",
		"coin": coin,
	}
	data, err := c.Post(ctx, "/info", req)
	if err != nil {
		return nil, err
	}
	var res L2BookResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}

	return &res, nil
}

// AllMids

func (c *Client) AllMids(ctx context.Context) (map[string]string, error) {
	req := map[string]string{
		"type": "allMids",
	}
	data, err := c.Post(ctx, "/info", req)
	if err != nil {
		return nil, err
	}
	var res map[string]string
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// CandleSnapshot

type Candle struct {
	T      int64  `json:"t"` // Open time
	TClose int64  `json:"T"` // Close time
	S      string `json:"s"` // Symbol
	I      string `json:"i"` // Interval
	O      string `json:"o"` // Open
	C      string `json:"c"` // Close
	H      string `json:"h"` // High
	L      string `json:"l"` // Low
	V      string `json:"v"` // Volume
	N      int64  `json:"n"` // Number of trades
}

type CandleSnapshotRequest struct {
	Coin      string `json:"coin"`
	Interval  string `json:"interval"`
	StartTime int64  `json:"startTime"`
	EndTime   int64  `json:"endTime"`
}

func (c *Client) CandleSnapshot(ctx context.Context, coin string, interval string, startTime, endTime int64) ([]Candle, error) {
	data, err := c.Post(ctx, "/info", map[string]any{
		"type": "candleSnapshot",
		"req": CandleSnapshotRequest{
			Coin:      coin,
			Interval:  interval,
			StartTime: startTime,
			EndTime:   endTime,
		},
	})
	if err != nil {
		return nil, err
	}
	var res []Candle
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// GetPrepMeta (Metadata)

func (c *Client) GetPrepMeta(ctx context.Context) (*PrepMeta, error) {
	return c.GetPrepMetaForDex(ctx, "")
}

func (c *Client) GetPrepMetaForDex(ctx context.Context, dex string) (*PrepMeta, error) {
	req := map[string]any{"type": "meta"}
	if dex != "" {
		req["dex"] = dex
	}
	data, err := c.Post(ctx, "/info", req)
	if err != nil {
		return nil, err
	}
	var res PrepMeta
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) GetAllPerpMetas(ctx context.Context) ([]PrepMeta, error) {
	data, err := c.Post(ctx, "/info", map[string]string{"type": "allPerpMetas"})
	if err != nil {
		return nil, err
	}
	var res []PrepMeta
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func (c *Client) GetPerpDexs(ctx context.Context) ([]PerpDex, error) {
	data, err := c.Post(ctx, "/info", map[string]string{"type": "perpDexs"})
	if err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	dexs := make([]PerpDex, 0, len(raw))
	for idx, item := range raw {
		if string(item) == "null" {
			continue
		}
		var dex PerpDex
		if err := json.Unmarshal(item, &dex); err != nil {
			return nil, err
		}
		if dex.Name == "" {
			continue
		}
		dex.Index = idx
		dexs = append(dexs, dex)
	}
	return dexs, nil
}

// GetFundingRate retrieves the current funding rate for a specific coin.
// It uses the metaAndAssetCtxs endpoint which provides real-time asset contexts
// including mark price, funding rate, open interest, etc.
func (c *Client) GetFundingRate(ctx context.Context, coin string) (*FundingRate, error) {
	meta, err := c.GetMetaAndAssetCtxs(ctx)
	if err != nil {
		return nil, err
	}

	// Match coin by index
	for i, uni := range meta.Meta.Universe {
		if uni.Name == coin {
			if i >= len(meta.AssetCtxs) {
				return nil, fmt.Errorf("asset context not found for coin: %s", coin)
			}

			return &FundingRate{
				Coin:     coin,
				Funding:  meta.AssetCtxs[i].Funding,
				MarkPx:   meta.AssetCtxs[i].MarkPx,
				OraclePx: meta.AssetCtxs[i].OraclePx,
				Premium:  meta.AssetCtxs[i].Premium,
			}, nil
		}
	}

	return nil, fmt.Errorf("funding rate not found for coin: %s", coin)
}

// GetAllFundingRates retrieves funding rates for all available coins.
// Returns a map where keys are coin names (e.g., "BTC", "ETH") and values are funding rates.
func (c *Client) GetAllFundingRates(ctx context.Context) (map[string]string, error) {
	meta, err := c.GetMetaAndAssetCtxs(ctx)
	if err != nil {
		return nil, err
	}

	// Build the map of coin name to funding rate
	result := make(map[string]string)
	for i, uni := range meta.Meta.Universe {
		if i < len(meta.AssetCtxs) {
			result[uni.Name] = meta.AssetCtxs[i].Funding
		}
	}

	return result, nil
}

// FundingRateHistoryEntry matches one element of the /info fundingHistory response.
type FundingRateHistoryEntry struct {
	Coin        string `json:"coin"`
	FundingRate string `json:"fundingRate"`
	Premium     string `json:"premium"`
	Time        int64  `json:"time"`
}

// GetFundingRateHistory retrieves historical funding rates via the /info endpoint.
// startTimeMs is required by the Hyperliquid API (post this time, inclusive).
// endTimeMs is optional (0 = now). Returns ascending or descending per HL docs;
// the caller can re-sort if needed.
// Docs: https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/info-endpoint
func (c *Client) GetFundingRateHistory(ctx context.Context, coin string, startTimeMs, endTimeMs int64) ([]FundingRateHistoryEntry, error) {
	return c.GetFundingRateHistoryForDex(ctx, "", coin, startTimeMs, endTimeMs)
}

func (c *Client) GetFundingRateHistoryForDex(ctx context.Context, dex string, coin string, startTimeMs, endTimeMs int64) ([]FundingRateHistoryEntry, error) {
	body := map[string]interface{}{
		"type":      "fundingHistory",
		"coin":      coin,
		"startTime": startTimeMs,
	}
	if dex != "" {
		body["dex"] = dex
	}
	if endTimeMs > 0 {
		body["endTime"] = endTimeMs
	}
	data, err := c.Post(ctx, "/info", body)
	if err != nil {
		return nil, err
	}
	var res []FundingRateHistoryEntry
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res, nil
}
