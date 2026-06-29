package subaccount

import (
	"context"
	"fmt"
)

func (c *Client) GetAssetsV4(ctx context.Context, email string) (*AssetsResponse, error) {
	if email == "" {
		return nil, fmt.Errorf("email is required")
	}
	var out AssetsResponse
	if err := c.get(ctx, "/sapi/v4/sub-account/assets", map[string]string{"email": email}, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetSpotAssetsSummary(ctx context.Context, email string, page, size int) (*SpotAssetsSummary, error) {
	params := map[string]string{}
	if email != "" {
		params["email"] = email
	}
	if page > 0 {
		params["page"] = fmt.Sprintf("%d", page)
	}
	if size > 0 {
		params["size"] = fmt.Sprintf("%d", size)
	}

	var out SpotAssetsSummary
	if err := c.get(ctx, "/sapi/v1/sub-account/spotSummary", params, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) FuturesTransfer(ctx context.Context, email, asset, amount string, transferType int) (*FuturesTransferResponse, error) {
	params := map[string]string{
		"email":  email,
		"asset":  asset,
		"amount": amount,
		"type":   fmt.Sprintf("%d", transferType),
	}
	var out FuturesTransferResponse
	if err := c.post(ctx, "/sapi/v1/sub-account/futures/transfer", params, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UniversalTransfer(ctx context.Context, req UniversalTransferRequest) (*UniversalTransferResponse, error) {
	params := map[string]string{
		"fromEmail":       req.FromEmail,
		"toEmail":         req.ToEmail,
		"fromAccountType": req.FromAccountType,
		"toAccountType":   req.ToAccountType,
		"clientTranId":    req.ClientTranID,
		"symbol":          req.Symbol,
		"asset":           req.Asset,
		"amount":          req.Amount,
	}
	var out UniversalTransferResponse
	if err := c.post(ctx, "/sapi/v1/sub-account/universalTransfer", params, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
