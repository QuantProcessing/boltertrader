package sdk

import (
	"context"
	"fmt"
	"strconv"
)

type TransactionLogRequest struct {
	AccountType string
	Category    string
	Currency    string
	BaseCoin    string
	Type        string
	StartMillis int64
	EndMillis   int64
	Limit       int
	Cursor      string
}

func (c *Client) GetTransactionLog(ctx context.Context, req TransactionLogRequest) (TransactionLogResult, error) {
	query := map[string]string{
		"accountType": req.AccountType,
		"category":    req.Category,
		"currency":    req.Currency,
		"baseCoin":    req.BaseCoin,
		"type":        req.Type,
		"cursor":      req.Cursor,
	}
	if req.StartMillis > 0 {
		query["startTime"] = strconv.FormatInt(req.StartMillis, 10)
	}
	if req.EndMillis > 0 {
		query["endTime"] = strconv.FormatInt(req.EndMillis, 10)
	}
	if req.Limit > 0 {
		query["limit"] = strconv.Itoa(req.Limit)
	}

	var resp responseEnvelope[TransactionLogResult]
	err := c.getPrivate(ctx, "/v5/account/transaction-log", query, &resp)
	if err != nil {
		return TransactionLogResult{}, err
	}
	if resp.RetCode != 0 {
		return TransactionLogResult{}, fmt.Errorf("bybit sdk: get transaction log failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return resp.Result, nil
}
