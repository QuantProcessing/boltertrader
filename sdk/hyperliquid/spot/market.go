package spot

import (
	"context"
	"encoding/json"
)

type SpotMeta struct {
	Tokens []struct {
		Name        string `json:"name"`
		SzDecimals  int    `json:"szDecimals"`
		WeiDecimals int    `json:"weiDecimals"`
		Index       int    `json:"index"`
		TokenId     string `json:"tokenId"`
		IsCanonical bool   `json:"isCanonical"`
		FullName    string `json:"fullName,omitempty"`
	}
	Universe []struct {
		Name        string `json:"name"`
		Index       int    `json:"index"`
		Tokens      []int  `json:"tokens"`
		IsCanonical bool   `json:"isCanonical"`
	}
}

func (c *Client) GetSpotMeta(ctx context.Context) (*SpotMeta, error) {
	data, err := c.Post(ctx, "/info", map[string]string{
		"type": "spotMeta",
	})
	if err != nil {
		return nil, err
	}
	var res SpotMeta
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *Client) GetOutcomeMeta(ctx context.Context) (*OutcomeMeta, error) {
	data, err := c.Post(ctx, "/info", map[string]string{
		"type": "outcomeMeta",
	})
	if err != nil {
		return nil, err
	}
	var res OutcomeMeta
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
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
