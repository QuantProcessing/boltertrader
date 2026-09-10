package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/shopspring/decimal"
)

type Account = wire.Account
type AccountConnection = wire.AccountConnection
type BalanceResource = wire.BalanceResource
type PositionSnapshot = wire.PositionSnapshot
type PlaceOrderRequest = wire.PlaceOrderRequest
type Order = wire.Order
type FillResource = wire.FillResource

type OrderScope struct{ AccountID, InstrumentID, GatewayOrderID string }

func (s OrderScope) valid() bool {
	return validID(s.AccountID) && validID(s.InstrumentID) && validID(s.GatewayOrderID)
}

// Identities are opaque. In particular, catalog IDs can contain %3A for a
// namespaced venue symbol; PathEscape/Values.Encode handle transport escaping.
func validID(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 512 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func number(value string) bool {
	if len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	n, err := decimal.NewFromString(value)
	return err == nil && n.Exponent() >= -100 && n.Exponent() <= 100
}
func positive(value string) bool {
	if !number(value) {
		return false
	}
	n, _ := decimal.NewFromString(value)
	return n.IsPositive()
}
func nonnegative(value string) bool {
	if !number(value) {
		return false
	}
	n, _ := decimal.NewFromString(value)
	return !n.IsNegative()
}
func product(value string) bool { return value == "spot" || value == "perp" }
func side(value string) bool    { return value == "buy" || value == "sell" }

func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	var result wire.Envelope[struct {
		Accounts []Account `json:"accounts"`
	}]
	if err := c.request(ctx, http.MethodGet, "/internal/v1/accounts", nil, nil, "", &result, 200); err != nil {
		return nil, err
	}
	if result.Data.Accounts == nil {
		return nil, protocol("account list")
	}
	seen := map[string]bool{}
	for _, account := range result.Data.Accounts {
		if !validAccount(account) || seen[account.ID] {
			return nil, protocol("account list")
		}
		seen[account.ID] = true
	}
	return result.Data.Accounts, nil
}
func (c *Client) Account(ctx context.Context, id string) (Account, error) {
	if !validID(id) {
		return Account{}, invalid("account identity")
	}
	var result wire.Envelope[struct {
		Account Account `json:"account"`
	}]
	if err := c.request(ctx, http.MethodGet, "/internal/v1/accounts/"+url.PathEscape(id), nil, nil, "", &result, 200); err != nil {
		return Account{}, err
	}
	if result.Data.Account.ID != id || !validAccount(result.Data.Account) {
		return Account{}, protocol("account")
	}
	return result.Data.Account, nil
}
func validAccount(account Account) bool {
	if !validID(account.ID) || !validID(account.Venue) || account.NativeEnvironment != "live" || account.Status != "active" {
		return false
	}
	seen := map[string]bool{}
	for _, route := range account.Routes {
		if !validID(route.RouteID) || !validID(route.RouteProfileID) || !product(route.Product) || seen[route.RouteID] {
			return false
		}
		seen[route.RouteID] = true
	}
	return len(account.Routes) > 0
}
func (c *Client) AccountConnection(ctx context.Context, id string) (AccountConnection, error) {
	if !validID(id) {
		return AccountConnection{}, invalid("account identity")
	}
	var result wire.Envelope[AccountConnection]
	if err := c.request(ctx, http.MethodGet, "/internal/v1/accounts/runtime/"+url.PathEscape(id), nil, nil, "", &result, 200); err != nil {
		return AccountConnection{}, err
	}
	if result.Data.AccountID != id || !validID(result.Data.RuntimeID) || result.Data.ObservedAt.IsZero() {
		return AccountConnection{}, protocol("account connection")
	}
	return result.Data, nil
}

// Balances returns a complete account read; a missing completeness marker is an error.
func (c *Client) Balances(ctx context.Context, id string) ([]BalanceResource, error) {
	if !validID(id) {
		return nil, invalid("account identity")
	}
	var page wire.BalanceSnapshot
	if err := c.request(ctx, http.MethodGet, "/v1/balances", url.Values{"account_id": {id}}, nil, "", &page, 200); err != nil {
		return nil, err
	}
	if page.Items == nil || !page.Complete || len(page.Items) > wire.MaxBalanceItems {
		return nil, protocol("incomplete balances")
	}
	for _, item := range page.Items {
		if item.AccountID != id || !product(item.Product) || !validID(item.Balance.Asset) || !number(item.Balance.Available) || !number(item.Balance.Locked) || !number(item.Balance.Total) || (item.AvailableMargin != "" && !number(item.AvailableMargin)) {
			return nil, protocol("balance")
		}
	}
	return page.Items, nil
}
func (c *Client) PositionSnapshot(ctx context.Context, id string) (PositionSnapshot, error) {
	if !validID(id) {
		return PositionSnapshot{}, invalid("account identity")
	}
	var result wire.Envelope[PositionSnapshot]
	if err := c.request(ctx, http.MethodGet, "/v1/position-snapshot", url.Values{"account_id": {id}}, nil, "", &result, 200); err != nil {
		return PositionSnapshot{}, err
	}
	snapshot := result.Data
	if snapshot.AccountID != id || snapshot.Items == nil {
		return PositionSnapshot{}, protocol("position snapshot")
	}
	switch snapshot.Freshness {
	case "live", "delayed", "stale", "syncing", "unavailable":
	default:
		return PositionSnapshot{}, protocol("position freshness")
	}
	if snapshot.Freshness == "live" && snapshot.VerifiedAt.IsZero() {
		return PositionSnapshot{}, protocol("position verification time")
	}
	seen := map[string]bool{}
	for _, item := range snapshot.Items {
		p := item.Position
		key := item.RouteID + "\x00" + item.InstrumentID + "\x00" + item.PositionSlot
		if item.AccountID != id || !validID(item.RouteID) || !validID(item.InstrumentID) || seen[key] || (item.PositionSlot != "net" && item.PositionSlot != "long" && item.PositionSlot != "short") || p.PositionSlot != item.PositionSlot || !side(p.Side) || !number(p.Quantity) || !number(p.EntryPrice) || !number(p.MarkPrice) || !number(p.UnrealizedPnL) || item.ResourceVersion == 0 || item.ObservedAt.IsZero() || item.VerifiedAt.IsZero() {
			return PositionSnapshot{}, protocol("position")
		}
		for _, optional := range []wire.OptionalDecimal{p.LiquidationPrice, p.Leverage, p.MarginUsed} {
			if optional.Valid && !number(optional.Value) {
				return PositionSnapshot{}, protocol("optional position amount")
			}
		}
		seen[key] = true
	}
	return snapshot, nil
}

func validatePlace(r PlaceOrderRequest) error {
	if !validID(r.AccountID) || !validID(r.RouteID) || !validID(r.InstrumentID) || !validID(r.IdempotencyKey) || !validID(r.RuntimeID) || !side(r.Side) || !positive(r.Quantity) || r.Leverage < 0 {
		return invalid("order")
	}
	switch r.Type {
	case "market":
		if r.LimitPrice != "" || r.LimitPolicy != "" {
			return invalid("market order limit fields")
		}
	case "limit":
		if !positive(r.LimitPrice) || (r.LimitPolicy != "resting" && r.LimitPolicy != "ioc" && r.LimitPolicy != "post_only") {
			return invalid("limit order")
		}
	default:
		return invalid("order type")
	}
	return nil
}
func validOrder(r Order) bool {
	if !validID(r.CommandID) || !validID(r.GatewayOrderID) || !validID(r.ClientOrderID) || !validID(r.AccountID) || !validID(r.InstrumentID) || r.ResourceVersion < 1 {
		return false
	}
	switch r.State {
	case "prepared", "dispatching", "accepted", "open", "partially_filled", "filled", "canceled", "rejected", "uncertain":
		return true
	default:
		return false
	}
}
func (c *Client) PlaceOrder(ctx context.Context, r PlaceOrderRequest) (Order, error) {
	if err := validatePlace(r); err != nil {
		return Order{}, err
	}
	var result Order
	if err := c.request(ctx, http.MethodPost, "/v1/orders", nil, r, r.IdempotencyKey, &result, 202); err != nil {
		return Order{}, err
	}
	if !validOrder(result) || result.AccountID != r.AccountID || result.InstrumentID != r.InstrumentID {
		return Order{}, protocol("order scope")
	}
	return result, nil
}
func (c *Client) Order(ctx context.Context, scope OrderScope) (Order, error) {
	return c.order(ctx, http.MethodGet, scope, 200)
}
func (c *Client) CancelOrder(ctx context.Context, scope OrderScope) (Order, error) {
	// Order identities are immutable. Verify the selected account/instrument
	// before a cancellation can reach the Gateway's write endpoint.
	if _, err := c.Order(ctx, scope); err != nil {
		if failure, ok := err.(*Error); ok {
			failure.Outcome = NotAttempted
		}
		return Order{}, err
	}
	return c.order(ctx, http.MethodDelete, scope, 202)
}
func (c *Client) order(ctx context.Context, method string, scope OrderScope, status int) (Order, error) {
	if !scope.valid() {
		return Order{}, invalid("order scope")
	}
	var result Order
	if err := c.request(ctx, method, "/v1/orders/"+url.PathEscape(scope.GatewayOrderID), nil, nil, "", &result, status); err != nil {
		return Order{}, err
	}
	if !validOrder(result) || result.GatewayOrderID != scope.GatewayOrderID || result.AccountID != scope.AccountID || result.InstrumentID != scope.InstrumentID {
		return Order{}, protocol("order scope")
	}
	return result, nil
}

// OrderByKey is read-only recovery after a lost placement response. A 404 is
// returned as an error; it is never proof that no request can still be running.
func (c *Client) OrderByKey(ctx context.Context, key, accountID, instrumentID string) (Order, error) {
	if !validID(key) || !validID(accountID) || !validID(instrumentID) {
		return Order{}, invalid("order lookup scope")
	}
	var result wire.OrderLookup
	if err := c.request(ctx, http.MethodGet, "/v1/orders/by-idempotency-key/"+url.PathEscape(key), nil, nil, "", &result, 200); err != nil {
		return Order{}, err
	}
	if result.IdempotencyKey != key || result.AccountID != accountID || result.InstrumentID != instrumentID || !validOrder(result.Order) || result.Order.AccountID != accountID || result.Order.InstrumentID != instrumentID {
		return Order{}, protocol("order lookup scope")
	}
	return result.Order, nil
}

type OrdersQuery struct {
	AccountID, InstrumentID, Cursor string
	Limit                           int
	OpenOnly                        bool
}

func (c *Client) Orders(ctx context.Context, q OrdersQuery) (wire.Page[Order], error) {
	var page wire.Page[Order]
	if !validID(q.AccountID) || (q.InstrumentID != "" && !validID(q.InstrumentID)) {
		return page, invalid("order list scope")
	}
	values, err := pagination(q.Limit, q.Cursor)
	if err != nil {
		return page, err
	}
	values.Set("account_id", q.AccountID)
	if q.InstrumentID != "" {
		values.Set("instrument_id", q.InstrumentID)
	}
	path := "/v1/orders"
	if q.OpenOnly {
		path = "/v1/open-orders"
	}
	if err = c.request(ctx, http.MethodGet, path, values, nil, "", &page, 200); err != nil {
		return wire.Page[Order]{}, err
	}
	limit := q.Limit
	if limit == 0 {
		limit = 100
	}
	if page.Items == nil || len(page.Items) > limit {
		return wire.Page[Order]{}, protocol("order list")
	}
	seen := make(map[string]bool, len(page.Items))
	for _, item := range page.Items {
		if seen[item.GatewayOrderID] || !validOrder(item) || item.AccountID != q.AccountID || (q.InstrumentID != "" && item.InstrumentID != q.InstrumentID) {
			return wire.Page[Order]{}, protocol("order list scope")
		}
		seen[item.GatewayOrderID] = true
	}
	if page.NextCursor != "" && (!validID(page.NextCursor) || page.NextCursor == q.Cursor) {
		return wire.Page[Order]{}, protocol("order cursor")
	}
	return page, nil
}
func pagination(limit int, cursor string) (url.Values, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 || len(cursor) > 8192 {
		return nil, invalid("pagination")
	}
	values := url.Values{"pagination.limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		values.Set("pagination.cursor", cursor)
	}
	return values, nil
}

// Fills reads every page for exactly one order. It discards partial results on
// error rather than letting callers book an incomplete set as complete evidence.
func (c *Client) Fills(ctx context.Context, scope OrderScope) ([]FillResource, error) {
	if !scope.valid() {
		return nil, invalid("fill scope")
	}
	values, _ := pagination(100, "")
	values.Set("account_id", scope.AccountID)
	values.Set("instrument_id", scope.InstrumentID)
	values.Set("gateway_order_id", scope.GatewayOrderID)
	result := make([]FillResource, 0)
	seen := map[string]bool{"": true}
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		var page wire.Page[FillResource]
		if err := c.request(ctx, http.MethodGet, "/v1/fills", values, nil, "", &page, 200); err != nil {
			return nil, err
		}
		if page.Items == nil || len(page.Items) > 100 {
			return nil, protocol("fill page")
		}
		for _, item := range page.Items {
			fill := item.Fill
			if item.AccountID != scope.AccountID || item.InstrumentID != scope.InstrumentID || item.GatewayOrderID != scope.GatewayOrderID || !validID(item.SettlementAsset) || !product(item.Product) || !validID(item.BaseAsset) || !validID(fill.FillID) || !side(fill.Side) || !positive(fill.Price) || !positive(fill.Quantity) || !number(fill.Fee) || (!decimal.RequireFromString(fill.Fee).IsZero() && !validID(fill.FeeAsset)) || fill.Time.IsZero() {
				return nil, protocol("fill scope or amount")
			}
			result = append(result, item)
		}
		if page.NextCursor == "" {
			return result, nil
		}
		if len(page.NextCursor) > 8192 || seen[page.NextCursor] {
			return nil, protocol("fill cursor cycle")
		}
		seen[page.NextCursor] = true
		values.Set("pagination.cursor", page.NextCursor)
	}
	return nil, protocol("fill pagination limit")
}
