package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const maxArchiveOrderDigests = 500

// GetAccount returns the account summary (balances, positions).
func (c *Client) GetAccount(ctx context.Context) (*AccountInfo, error) {
	if c.Signer == nil {
		return nil, ErrCredentialsRequired
	}
	sender := BuildSender(c.Signer.GetAddress(), c.subaccount)
	req := map[string]interface{}{
		"type":       "subaccount_info",
		"subaccount": sender,
	}

	data, err := c.QueryGateWayV1(ctx, "POST", req)
	if err != nil {
		return nil, err
	}
	var resp AccountInfo
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetAccountSnapshot(ctx context.Context) (*AccountSnapshot, error) {
	account, err := c.GetAccount(ctx)
	if err != nil {
		return nil, err
	}
	return c.accountSnapshot(*account), nil
}

func (c *Client) GetSubaccountInfo(ctx context.Context, req SubaccountInfoRequest) (*AccountInfo, error) {
	payload := map[string]interface{}{
		"type":       "subaccount_info",
		"subaccount": req.Subaccount,
	}
	if len(req.Txns) > 0 {
		txns, err := json.Marshal(req.Txns)
		if err != nil {
			return nil, fmt.Errorf("marshal subaccount simulation: %w", err)
		}
		payload["txns"] = string(txns)
	}
	if req.PreState != nil {
		payload["pre_state"] = fmt.Sprintf("%t", *req.PreState)
	}
	data, err := c.QueryGateWayV1(ctx, "POST", payload)
	if err != nil {
		return nil, err
	}
	var resp AccountInfo
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetSubaccountInfoSnapshot(ctx context.Context, req SubaccountInfoRequest) (*AccountSnapshot, error) {
	account, err := c.GetSubaccountInfo(ctx, req)
	if err != nil {
		return nil, err
	}
	return c.accountSnapshot(*account), nil
}

func (c *Client) accountSnapshot(account AccountInfo) *AccountSnapshot {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	return &AccountSnapshot{Account: account, ReceivedAt: now().UTC()}
}

// GetAccountProductOrders returns open orders for a specific product and sender.
func (c *Client) GetAccountProductOrders(ctx context.Context, productID int64, sender string) (*AccountProductOrders, error) {
	req := map[string]interface{}{
		"type":       "subaccount_orders",
		"product_id": productID,
		"sender":     sender,
	}
	data, err := c.QueryGateWayV1(ctx, "POST", req)
	if err != nil {
		return nil, err
	}
	var resp AccountProductOrders
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetAccountMultiProductsOrders(ctx context.Context, productIDs []int64, sender string) (*GetAccountMultiProductsOrders, error) {
	req := map[string]interface{}{
		"type":        "orders",
		"product_ids": productIDs,
		"sender":      sender,
	}
	data, err := c.QueryGateWayV1(ctx, "POST", req)
	if err != nil {
		return nil, err
	}
	var resp GetAccountMultiProductsOrders
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetOrder returns a specific order.
func (c *Client) GetOrder(ctx context.Context, productID int64, digest string) (*Order, error) {
	req := map[string]interface{}{
		"type":       "order",
		"product_id": productID,
		"digest":     digest,
	}
	data, err := c.QueryGateWayV1(ctx, "POST", req)
	if err != nil {
		return nil, err
	}
	var resp Order
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetFeeRates returns the fee rates for the sender.
func (c *Client) GetFeeRates(ctx context.Context) (*FeeRates, error) {
	if c.Signer == nil {
		return nil, fmt.Errorf("credentials required for fee rate lookup")
	}
	sender := BuildSender(c.Signer.GetAddress(), c.subaccount)
	req := map[string]interface{}{
		"type":   "fee_rates",
		"sender": sender,
	}
	data, err := c.QueryGateWayV1(ctx, "GET", req)
	if err != nil {
		return nil, err
	}
	var resp FeeRates
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetSubaccountSnapshots queries the archive for account state at specific timestamps.
func (c *Client) GetSubaccountSnapshots(ctx context.Context, subaccounts []string, timestamps []int64) (*ArchiveSnapshotResponse, error) {
	req := ArchiveSnapshotRequest{
		AccountSnapshots: AccountSnapshotsQuery{
			Subaccounts: subaccounts,
			Timestamps:  timestamps,
		},
	}
	data, err := c.QueryArchiveV1(ctx, req)
	if err != nil {
		return nil, err
	}
	var resp ArchiveSnapshotResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetMatches queries historical matches for a subaccount.
func (c *Client) GetMatches(ctx context.Context, subaccount string, productIDs []int64, limit int) (*ArchiveMatchesResponse, error) {
	req := ArchiveMatchesRequest{
		Matches: MatchesQuery{
			Subaccounts: []string{subaccount},
			ProductIds:  productIDs,
			Limit:       limit,
		},
	}

	data, err := c.QueryArchiveV1(ctx, req)
	if err != nil {
		return nil, err
	}

	var resp ArchiveMatchesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetOrdersByDigests queries matched historical orders from the archive
// indexer. Gateway GetOrder only reads the live orderbook and is not a
// terminal-order source.
func (c *Client) GetOrdersByDigests(ctx context.Context, digests []string) (*ArchiveOrdersResponse, error) {
	if len(digests) == 0 || len(digests) > maxArchiveOrderDigests {
		return nil, fmt.Errorf("nado archive orders: digest count must be between 1 and %d", maxArchiveOrderDigests)
	}
	normalized := make([]string, len(digests))
	for i, digest := range digests {
		normalized[i] = strings.TrimSpace(digest)
		if normalized[i] == "" {
			return nil, fmt.Errorf("nado archive orders: digest at index %d is empty", i)
		}
	}
	data, err := c.QueryArchiveV1(ctx, ArchiveOrdersRequest{Orders: OrdersByDigestsQuery{
		Digests: normalized,
		Limit:   len(normalized),
	}})
	if err != nil {
		return nil, err
	}
	var resp ArchiveOrdersResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
