package perp

import (
	"context"
	"net/http"
	"strings"
)

// GetAccount gets basic account info
func (c *Client) GetAccount(ctx context.Context) (*Account, error) {
	var res Account
	params := map[string]interface{}{
		"accountId": c.AccountID,
	}
	err := c.call(ctx, http.MethodGet, "/api/v1/private/account/getAccountById", params, true, &res)
	return &res, err
}

// GetAccountAsset gets account asset info including positions
func (c *Client) GetAccountAsset(ctx context.Context) (*AccountAsset, error) {
	var res AccountAsset
	params := map[string]interface{}{
		"accountId": c.AccountID,
	}
	err := c.call(ctx, http.MethodGet, "/api/v1/private/account/getAccountAsset", params, true, &res)
	return &res, err
}

// GetPositionTransactions gets position transactions by IDs
func (c *Client) GetPositionTransactions(ctx context.Context, ids []string) ([]PositionTransaction, error) {
	var res PositionTransactionResponse
	params := map[string]interface{}{
		"accountId":                 c.AccountID,
		"positionTransactionIdList": strings.Join(ids, ","),
	}
	err := c.call(ctx, http.MethodGet, "/api/v1/private/account/getPositionTransactionById", params, true, &res)
	if err != nil {
		return nil, err
	}
	return res.DataList, nil
}

func (c *Client) UpdateLeverageSetting(ctx context.Context, contractId string, leverage int) error {
	var res UpdateLeverageSettingResponse
	params := map[string]interface{}{
		"accountId":  c.AccountID,
		"contractId": contractId,
		"leverage":   leverage,
	}
	err := c.call(ctx, http.MethodPost, "/api/v1/private/account/updateLeverageSetting", params, true, &res)
	return err
}

func (c *Client) GetOpenOrders(ctx context.Context, contractId *string) ([]Order, error) {
	var res ActiveOrders
	fos := []string{OrderStatusPending.String(), OrderStatusOpen.String()}
	foStr := strings.Join(fos, ",")
	params := map[string]interface{}{
		"accountId":        c.AccountID,
		"filterStatusList": foStr,
	}
	if contractId != nil {
		params["filterContractIdList"] = *contractId
	}
	err := c.call(ctx, http.MethodGet, "/api/v1/private/order/getActiveOrderPage", params, true, &res)
	return res.DataList, err
}
