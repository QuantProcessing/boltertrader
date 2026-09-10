// Package gateway converts Gateway protocol facts into Trading application models.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	gatewayclient "github.com/QuantProcessing/boltertrader/clients/gateway"
	"github.com/QuantProcessing/boltertrader/services/trading/accountpositions"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/shopspring/decimal"
)

type Client struct {
	protocol   *gatewayclient.Client
	liveWrites bool
}
type ClientOption func(*gatewayclient.Config) error

func WithAuthorityPlane(rawURL string, allowPlaintext bool, token []byte) ClientOption {
	return func(config *gatewayclient.Config) error {
		if len(token) < 32 {
			return fmt.Errorf("Gateway authority token is invalid")
		}
		if _, err := gatewayclient.New(gatewayclient.Config{URL: rawURL, Token: string(token), AllowPlaintext: allowPlaintext}); err != nil {
			return err
		}
		config.InternalURL, config.InternalToken = rawURL, string(token)
		// Both origins have been checked with their own explicit plaintext policy.
		config.AllowPlaintext = true
		return nil
	}
}
func NewClient(rawURL string, allowPlaintext bool, token []byte, liveWrites bool, transport *http.Client, options ...ClientOption) (*Client, error) {
	if len(token) < 32 {
		return nil, fmt.Errorf("Gateway token is invalid")
	}
	config := gatewayclient.Config{URL: rawURL, Token: string(token), AllowPlaintext: allowPlaintext, HTTPClient: transport}
	protocol, err := gatewayclient.New(config)
	if err != nil {
		return nil, err
	}
	for _, option := range options {
		if err := option(&config); err != nil {
			return nil, err
		}
	}
	if len(options) > 0 {
		protocol, err = gatewayclient.New(config)
		if err != nil {
			return nil, err
		}
	}
	return &Client{protocol: protocol, liveWrites: liveWrites}, nil
}

type ManualAccounts struct{ client *Client }

func (client *Client) Accounts() ManualAccounts { return ManualAccounts{client: client} }
func (source ManualAccounts) Get(ctx context.Context, principal, accountID string) (manual.Account, error) {
	if source.client == nil {
		return manual.Account{}, fmt.Errorf("Gateway account authority is unavailable")
	}
	if strings.TrimSpace(principal) == "" || strings.TrimSpace(accountID) == "" {
		return manual.Account{}, fmt.Errorf("Gateway account read scope is invalid")
	}
	account, err := source.client.protocol.Account(ctx, accountID)
	if err != nil {
		return manual.Account{}, fmt.Errorf("read Gateway account: %w", err)
	}
	// NativeEnvironment is the venue environment. The Gateway derives both from
	// the same route profile, and the public record carries only this one.
	routes := make([]manual.AccountRoute, 0, len(account.Routes))
	for _, route := range account.Routes {
		routes = append(routes, manual.AccountRoute{
			RouteID: route.RouteID, Product: route.Product,
			SettlementAsset: route.SettlementAsset, RouteProfileID: route.RouteProfileID,
		})
	}
	runtimes, err := source.client.ListAccountRuntimes(ctx, principal, accountID)
	if err != nil {
		return manual.Account{}, fmt.Errorf("read Gateway account runtime: %w", err)
	}
	if len(runtimes) != 1 || !runtimes[0].Connected || strings.TrimSpace(runtimes[0].RuntimeID) == "" {
		return manual.Account{}, fmt.Errorf("Gateway account runtime response is invalid")
	}
	return manual.Account{
		ID: account.ID, RuntimeID: runtimes[0].RuntimeID,
		Venue: account.Venue, VenueEnvironment: account.NativeEnvironment,
		State: account.Status, Routes: routes,
	}, nil
}

type orderIntent struct {
	AccountID    string `json:"accountId,omitempty"`
	InstrumentID string `json:"instrumentId"`
	Side         string `json:"side"`
	Quantity     string `json:"quantity"`
	OrderType    string `json:"orderType"`
	LimitPrice   string `json:"limitPrice,omitempty"`
	LimitPolicy  string `json:"limitPolicy,omitempty"`
	TimeInForce  string `json:"timeInForce,omitempty"`
	ReduceOnly   bool   `json:"reduceOnly,omitempty"`
	Leverage     int    `json:"leverage,omitempty"`
	RouteID      string `json:"routeId,omitempty"`
	RuntimeID    string `json:"runtimeId"`
}

func (client *Client) Execute(ctx context.Context, request manual.GatewayRequest) (manual.GatewayResult, error) {
	if !client.liveWrites {
		return manual.GatewayResult{Handoff: orders.HandoffResult{Outcome: orders.HandoffNotAttempted}}, orders.ErrLiveWriteDisabled
	}
	runtimeID := strings.TrimSpace(request.RuntimeID)

	intent := orderIntent{
		AccountID: request.AccountID, InstrumentID: request.InstrumentID, Side: request.Side, Quantity: request.Quantity,
		OrderType: request.OrderType, LimitPrice: request.LimitPrice, TimeInForce: request.TimeInForce, ReduceOnly: request.ReduceOnly, Leverage: request.Leverage,
		RouteID: request.RouteID, RuntimeID: runtimeID,
	}
	handoff, err := client.place(ctx, request.CommandID, intent)
	result := manual.GatewayResult{CommandID: request.CommandID, AccountID: request.AccountID, State: manualState(handoff.Evidence.State), Handoff: handoff}
	for _, fill := range handoff.Evidence.Fills {
		result.Fills = append(result.Fills, manual.FillFact{InstrumentProduct: fill.InstrumentProduct, BaseAsset: fill.BaseAsset, FillID: fill.FillID, CommandID: request.CommandID, AccountID: fill.AccountID, InstrumentID: fill.InstrumentID, Side: portfolio.Side(fill.Side), Quantity: fill.Quantity, Price: fill.Price, Fee: fill.Fee, FeeCurrency: fill.FeeCurrency, SettlementCurrency: fill.SettlementCurrency, OccurredAt: fill.OccurredAt})
	}
	if result.State == manual.GatewayStateDenied {
		result.DenialCode = "gateway_rejected"
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func (client *Client) Cancel(ctx context.Context, request manual.GatewayCancelRequest) (manual.GatewayResult, error) {
	if !client.liveWrites {
		return manual.GatewayResult{}, orders.ErrLiveWriteDisabled
	}
	if request.CommandID == "" || request.AccountID == "" || request.InstrumentID == "" || request.GatewayOrderID == "" {
		return manual.GatewayResult{}, fmt.Errorf("Gateway cancel identity is incomplete")
	}
	handoff, err := client.cancel(ctx, request)
	result := manual.GatewayResult{
		CommandID: request.CommandID, AccountID: request.AccountID,
		State: manualState(handoff.Evidence.State), Handoff: handoff,
	}
	for _, fill := range handoff.Evidence.Fills {
		result.Fills = append(result.Fills, manual.FillFact{
			InstrumentProduct: fill.InstrumentProduct, BaseAsset: fill.BaseAsset, FillID: fill.FillID, CommandID: request.CommandID, AccountID: fill.AccountID,
			InstrumentID: fill.InstrumentID, Side: portfolio.Side(fill.Side),
			Quantity: fill.Quantity, Price: fill.Price, Fee: fill.Fee, FeeCurrency: fill.FeeCurrency,
			SettlementCurrency: fill.SettlementCurrency, OccurredAt: fill.OccurredAt,
		})
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func manualState(state string) manual.GatewayState {
	switch strings.ToLower(state) {
	case "filled":
		return manual.GatewayStateFilled
	case "partially_filled":
		return manual.GatewayStatePartiallyFilled
	case "canceled", "cancelled":
		return manual.GatewayStateCanceled
	case "rejected", "failed":
		return manual.GatewayStateDenied
	default:
		return manual.GatewayStateSubmitted
	}
}

// Reconcile reads the same durable identity; it never resubmits an order.
func (client *Client) Reconcile(ctx context.Context, command orders.Command) (orders.HandoffResult, error) {
	current := command.ExecutionEvidence
	unknown := func(err error) (orders.HandoffResult, error) {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: current}, err
	}
	var intent struct {
		InstrumentID string `json:"instrumentId"`
	}
	if command.AccountID == "" || json.Unmarshal(command.Payload, &intent) != nil || intent.InstrumentID == "" {
		return unknown(fmt.Errorf("Gateway reconciliation scope is unavailable"))
	}
	var result wire.Order
	var err error
	if current.GatewayOrderID == "" {
		result, err = client.protocol.OrderByKey(ctx, command.CommandID, command.AccountID, intent.InstrumentID)
	} else {
		result, err = client.protocol.Order(ctx, gatewayclient.OrderScope{AccountID: command.AccountID, InstrumentID: intent.InstrumentID, GatewayOrderID: current.GatewayOrderID})
	}
	if err != nil {
		return unknown(err)
	}
	if current.GatewayOrderID != "" && (result.CommandID != current.CommandID || result.ClientOrderID != current.ClientOrderID || result.ResourceVersion < current.ResourceVersion) {
		return unknown(fmt.Errorf("Gateway reconciliation identity regressed"))
	}
	return client.handoffEvidence(ctx, result)
}

func (client *Client) Fetch(ctx context.Context) (catalog.Snapshot, error) {
	venues, err := client.protocol.CatalogVenues(ctx)
	if err != nil {
		return catalog.Snapshot{}, err
	}
	instruments, err := client.protocol.CatalogInstruments(ctx)
	if err != nil {
		return catalog.Snapshot{}, err
	}
	result := catalog.Snapshot{Revision: instruments.Revision, ObservedAt: instruments.ObservedAt, Venues: make([]catalog.Venue, 0, len(venues)), Instruments: make([]catalog.Instrument, 0, len(instruments.Instruments))}
	for _, v := range venues {
		result.Venues = append(result.Venues, catalog.Venue{Venue: v.Venue, Environment: v.Environment, Ready: v.Ready})
	}
	for _, v := range instruments.Instruments {
		result.Instruments = append(result.Instruments, catalog.Instrument{
			InstrumentID: v.InstrumentID, Venue: v.Venue, Environment: v.Environment, Product: v.Product, Settlement: v.Settlement, Symbol: v.Symbol, BaseAsset: v.BaseAsset, QuoteAsset: v.QuoteAsset, MarketFamily: v.MarketFamily, AssetClass: v.AssetClass, ClassificationSource: v.ClassificationSource, RawAssetType: v.RawAssetType, RawCategories: v.RawCategories, Rules: catalog.TradeRules(v.Rules), Ready: v.Ready,
		})
	}
	return result, nil
}

func (client *Client) ListAccountRuntimes(ctx context.Context, principal, accountID string) ([]accountpositions.AccountRuntimeProjection, error) {
	if strings.TrimSpace(principal) == "" {
		return nil, fmt.Errorf("account scope is required")
	}
	item, err := client.protocol.AccountConnection(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return []accountpositions.AccountRuntimeProjection{{AccountID: item.AccountID, RuntimeID: item.RuntimeID, Connected: item.Connected, ObservedAt: item.ObservedAt}}, nil
}

func (client *Client) ListAccountBalances(ctx context.Context, principal, accountID string) ([]accountpositions.AccountBalanceProjection, error) {
	if strings.TrimSpace(principal) == "" {
		return nil, fmt.Errorf("account scope is required")
	}
	if _, err := client.protocol.Account(ctx, accountID); err != nil {
		return nil, err
	}
	balances, err := client.protocol.Balances(ctx, accountID)
	if err != nil {
		return nil, err
	}
	items := make([]accountpositions.AccountBalanceProjection, 0, len(balances))
	for _, item := range balances {
		items = append(items, accountpositions.AccountBalanceProjection{AccountID: item.AccountID, Product: item.Product, SettlementAsset: item.SettlementAsset, Asset: item.Balance.Asset, Available: item.Balance.Available, Locked: item.Balance.Locked, Total: item.Balance.Total, AvailableMargin: item.AvailableMargin})
	}
	return items, nil
}

func (client *Client) ListAccountPositions(ctx context.Context, principal, accountID, instrumentID string) ([]accountpositions.AccountPositionProjection, error) {
	if strings.TrimSpace(instrumentID) == "" {
		return nil, fmt.Errorf("instrument scope is required")
	}
	snapshot, err := client.PositionSnapshot(ctx, principal, accountID)
	if err != nil {
		return nil, err
	}
	if snapshot.Freshness != accountpositions.FreshnessLive {
		return nil, fmt.Errorf("Gateway positions are not current")
	}
	items := make([]accountpositions.AccountPositionProjection, 0)
	for _, item := range snapshot.Items {
		if item.InstrumentID != instrumentID {
			continue
		}
		p := item.Position
		quantity, _ := decimal.NewFromString(p.Quantity)
		if p.Side == "buy" {
			quantity = quantity.Abs()
		} else if p.Side == "sell" {
			quantity = quantity.Abs().Neg()
		}
		items = append(items, accountpositions.AccountPositionProjection{AccountID: item.AccountID, InstrumentID: item.InstrumentID, Side: p.Side, Quantity: quantity.String(), EntryPrice: p.EntryPrice, MarkPrice: p.MarkPrice, UnrealizedPnL: p.UnrealizedPnL})
	}
	return items, nil
}

func handoffError(err error) (orders.HandoffResult, error) {
	outcome := orders.HandoffOutcomeUnknown
	var failure *gatewayclient.Error
	if errors.As(err, &failure) {
		switch failure.Outcome {
		case gatewayclient.NotAttempted:
			outcome = orders.HandoffNotAttempted
		case gatewayclient.Rejected:
			outcome = orders.HandoffRejected
		}
	}
	return orders.HandoffResult{Outcome: outcome}, err
}
func (client *Client) place(ctx context.Context, idempotencyID string, intent orderIntent) (orders.HandoffResult, error) {
	policy := strings.ToLower(intent.LimitPolicy)
	if policy == "" && strings.EqualFold(intent.OrderType, "limit") {
		policy = strings.ToLower(intent.TimeInForce)
		if policy == "gtc" {
			policy = "resting"
		}
	}
	result, err := client.protocol.PlaceOrder(ctx, wire.PlaceOrderRequest{AccountID: intent.AccountID, RouteID: intent.RouteID, InstrumentID: intent.InstrumentID, IdempotencyKey: idempotencyID, Side: intent.Side, Type: strings.ToLower(intent.OrderType), Quantity: intent.Quantity, LimitPrice: intent.LimitPrice, LimitPolicy: policy, ReduceOnly: intent.ReduceOnly, Leverage: intent.Leverage, RuntimeID: intent.RuntimeID})
	if err != nil {
		return handoffError(err)
	}
	return client.handoffEvidence(ctx, result)
}
func (client *Client) cancel(ctx context.Context, request manual.GatewayCancelRequest) (orders.HandoffResult, error) {
	result, err := client.protocol.CancelOrder(ctx, gatewayclient.OrderScope{AccountID: request.AccountID, InstrumentID: request.InstrumentID, GatewayOrderID: request.GatewayOrderID})
	if err != nil {
		return handoffError(err)
	}
	return client.handoffEvidence(ctx, result)
}
func (client *Client) handoffEvidence(ctx context.Context, result wire.Order) (orders.HandoffResult, error) {
	evidence := orders.ExecutionEvidence{CommandID: result.CommandID, GatewayOrderID: result.GatewayOrderID, ClientOrderID: result.ClientOrderID, State: result.State, ResourceVersion: result.ResourceVersion, VenueCode: result.VenueCode, VenueMessage: result.VenueMessage,
		OrderEvents: []orders.OrderEventEvidence{{EventID: result.GatewayOrderID + ":" + strconv.FormatInt(result.ResourceVersion, 10), State: result.State, ResourceVersion: result.ResourceVersion}}}
	switch result.State {
	case "accepted", "open", "partially_filled", "filled", "canceled", "cancelled", "rejected":
	default:
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: evidence}, nil
	}
	if result.State == "filled" || result.State == "partially_filled" || result.State == "canceled" || result.State == "cancelled" {
		fills, err := client.fetchFills(ctx, result.GatewayOrderID, result.AccountID, result.InstrumentID)
		if err != nil {
			return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: evidence}, err
		}
		evidence.Fills = fills
	}
	return orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence}, nil
}
func (client *Client) fetchFills(ctx context.Context, orderID, accountID, instrumentID string) ([]orders.FillEvidence, error) {
	items, err := client.protocol.Fills(ctx, gatewayclient.OrderScope{AccountID: accountID, InstrumentID: instrumentID, GatewayOrderID: orderID})
	if err != nil {
		return nil, err
	}
	fills := make([]orders.FillEvidence, 0, len(items))
	for _, item := range items {
		f := item.Fill
		fills = append(fills, orders.FillEvidence{InstrumentProduct: item.Product, BaseAsset: item.BaseAsset, FillID: f.FillID, AccountID: item.AccountID, InstrumentID: item.InstrumentID, Side: f.Side, Quantity: f.Quantity, Price: f.Price, Fee: f.Fee, FeeCurrency: f.FeeAsset, SettlementCurrency: item.SettlementAsset, OccurredAt: f.Time})
	}
	return fills, nil
}
func (client *Client) CheckMarketDataRoute(ctx context.Context) error {
	venues, err := client.protocol.CatalogVenues(ctx)
	if err != nil {
		return err
	}
	for _, venue := range venues {
		if venue.Ready {
			return nil
		}
	}
	return fmt.Errorf("Gateway market-data route is unavailable")
}

func (client *Client) WatchAccountActivity(ctx context.Context, notify func(string)) error {
	return client.protocol.WatchAccountActivity(ctx, notify)
}
