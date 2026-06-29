package perp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

var (
	ErrInvalidParam = errors.New("invalid param")
)

const edgeXFundingRatePageSizeMax = 100

// Public API

func (c *Client) GetExchangeInfo(ctx context.Context) (*ExchangeInfo, error) {
	var res ExchangeInfo
	err := c.call(ctx, http.MethodGet, c.publicAPIPath("meta", "getMetaData"), nil, false, &res)
	return &res, err
}

func (c *Client) GetTicker(ctx context.Context, contractId string) (*Ticker, error) {
	var res []Ticker
	err := c.call(ctx, http.MethodGet, c.publicAPIPath("quote", "getTicker"), map[string]interface{}{"contractId": contractId}, false, &res)
	if len(res) == 0 {
		return nil, ErrInvalidParam
	}
	return &res[0], err
}

func (c *Client) GetOrderBook(ctx context.Context, contractId string, level int) (*OrderBook, error) {
	var res []OrderBook
	err := c.call(ctx, http.MethodGet, c.publicAPIPath("quote", "getDepth"), map[string]interface{}{"contractId": contractId, "level": level}, false, &res)
	if len(res) == 0 {
		return nil, ErrInvalidParam
	}
	return &res[0], err
}

func (c *Client) GetKline(ctx context.Context,
	contractId, priceType, klineType string, size int,
	offsetData, filterBeginKlineTimeInclusive, filterEndKlineTimeExclusive string) (*GetKlineResponse, error) {
	// check params
	if size <= 0 || size > 1000 {
		return nil, ErrInvalidParam
	}
	var res GetKlineResponse
	err := c.call(ctx, http.MethodGet, c.publicAPIPath("quote", "getKline"), map[string]interface{}{
		"contractId":                    contractId,
		"priceType":                     priceType,
		"klineType":                     klineType,
		"size":                          size,                          // Number to retrieve. Must be greater than 0 and less than or equal to 1000
		"offsetData":                    offsetData,                    // Pagination offset. If empty, get the first page
		"filterBeginKlineTimeInclusive": filterBeginKlineTimeInclusive, // Query start time (if 0, means from current time). Returns in descending order by time
		"filterEndKlineTimeExclusive":   filterEndKlineTimeExclusive,   // Query end time
	}, false, &res)
	return &res, err
}

func (c *Client) GetExchangeLongShortRatio(ctx context.Context, rang, filterContractIdList, filterExchangeList *string) (*ExchangeLongShortRatio, error) {
	var params = make(map[string]interface{})
	if rang != nil {
		params["rang"] = *rang
	}
	if filterContractIdList != nil {
		params["filterContractIdList"] = *filterContractIdList
	}
	if filterExchangeList != nil {
		params["filterExchangeList"] = *filterExchangeList
	}
	var res ExchangeLongShortRatio
	err := c.call(ctx, http.MethodGet, c.publicAPIPath("quote", "getExchangeLongShortRatio"), params, false, &res)
	return &res, err
}

// GetFundingRate retrieves the latest funding rate for a specific contract.
func (c *Client) GetFundingRate(ctx context.Context, contractId string) (*FundingRateData, error) {
	params := make(map[string]interface{})
	if contractId != "" {
		params["contractId"] = contractId
	}

	var data []FundingRateData
	err := c.call(ctx, http.MethodGet, c.publicAPIPath("funding", "getLatestFundingRate"), params, false, &data)
	if err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return nil, ErrInvalidParam
	}

	return &data[0], nil
}

// GetFundingRatePage retrieves one versioned funding-rate history page.
func (c *Client) GetFundingRatePage(ctx context.Context,
	contractId string,
	size int,
	offsetData string,
	filterSettlementFundingRate bool,
	filterBeginTimeInclusive int64,
	filterEndTimeExclusive int64,
) (*FundingRatePage, error) {
	if size <= 0 || size > edgeXFundingRatePageSizeMax {
		return nil, ErrInvalidParam
	}
	params := map[string]interface{}{
		"size":                        size,
		"filterSettlementFundingRate": filterSettlementFundingRate,
	}
	if contractId != "" {
		params["contractId"] = contractId
	}
	if offsetData != "" {
		params["offsetData"] = offsetData
	}
	if filterBeginTimeInclusive > 0 {
		params["filterBeginTimeInclusive"] = filterBeginTimeInclusive
	}
	if filterEndTimeExclusive > 0 {
		params["filterEndTimeExclusive"] = filterEndTimeExclusive
	}

	var page FundingRatePage
	err := c.call(ctx, http.MethodGet, c.publicAPIPath("funding", "getFundingRatePage"), params, false, &page)
	if err != nil {
		return nil, err
	}
	return &page, nil
}

// GetFundingRateHistory retrieves settlement funding-rate history for a contract.
func (c *Client) GetFundingRateHistory(ctx context.Context, contractId string, startTime int64, endTime int64, limit int) ([]FundingRateData, error) {
	var rows []FundingRateData
	offset := ""
	for {
		size := edgeXFundingRatePageSizeMax
		if limit > 0 {
			remaining := limit - len(rows)
			if remaining <= 0 {
				break
			}
			if remaining < size {
				size = remaining
			}
		}
		page, err := c.GetFundingRatePage(ctx, contractId, size, offset, true, startTime, endTime)
		if err != nil {
			return nil, err
		}
		rows = append(rows, page.DataList...)
		if page.NextPageOffsetData == "" || (limit > 0 && len(rows) >= limit) {
			break
		}
		if page.NextPageOffsetData == offset {
			break
		}
		offset = page.NextPageOffsetData
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// GetAllFundingRates retrieves the latest funding rates for all contracts.
func (c *Client) GetAllFundingRates(ctx context.Context) ([]FundingRateData, error) {
	// First, get all contract information to retrieve contract IDs
	exchangeInfo, err := c.GetExchangeInfo(ctx)
	if err != nil {
		return nil, err
	}

	// Collect all contract IDs
	var contractIds []string
	for _, contract := range exchangeInfo.ContractList {
		contractIds = append(contractIds, contract.ContractId)
	}

	if len(contractIds) == 0 {
		return []FundingRateData{}, nil
	}

	// Build query parameters with multiple contractId fields
	params := url.Values{}
	for _, contractId := range contractIds {
		params.Add("contractId", contractId)
	}

	var data []FundingRateData
	endpoint := c.publicAPIPath("funding", "getLatestFundingRate") + "?" + params.Encode()
	err = c.call(ctx, http.MethodGet, endpoint, nil, false, &data)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (c *Client) publicAPIPath(section string, name string) string {
	version := strings.ToLower(strings.TrimSpace(c.PublicAPIVersion))
	if version == "" {
		version = "v2"
	}
	if strings.Contains(c.BaseURL, "edgex-prod-v2") {
		version = "v2"
	}
	if version != "v2" {
		version = "v1"
	}
	return "/api/" + version + "/public/" + section + "/" + name
}
