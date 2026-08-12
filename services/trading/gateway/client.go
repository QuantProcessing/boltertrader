// Package gateway is the trading service's client for the Gateway's HTTP
// surface: the catalog it reads instruments from, the market data it quotes
// against, the account authority it fences orders on, and the order plane it
// hands executions to.
//
// It lives on trading's side of the wire rather than the Gateway's because
// every type here is a decoding of what the Gateway said, judged against what
// trading needs — a Gateway response that satisfies the Gateway's own contract
// can still be unusable here, and this package is where that is decided.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	tradinghttp "github.com/QuantProcessing/boltertrader/services/trading/httpapi"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

// Client is trading's handle on the Gateway. One instance serves every question
// trading asks — catalog, market data, accounts, orders, fills — because they
// all travel the same authenticated origin under the same mode, and splitting
// them would mean four places that can disagree about which Gateway is real.
type Client struct {
	baseURL            string
	token              []byte
	httpClient         *http.Client
	mode               orders.Mode
	liveWrites         bool
	requireAttestation bool
}

const (
	// ModeHeader carries the mode attestation both ways: the client states the
	// mode it believes it is in, and refuses a response that disagrees.
	ModeHeader             = "X-Bolter-Mode"
	authorityResponseLimit = 64 << 10
)

type readEnvelope[T any] struct {
	Data T `json:"data"`
}

type runtimeProjectionView struct {
	AccountID                       string                           `json:"accountId"`
	Active                          *runtimeSnapshotView             `json:"active,omitempty"`
	Pending                         *pendingSnapshotView             `json:"pending,omitempty"`
	Retiring                        []runtimeSnapshotView            `json:"retiring"`
	Tombstoned                      *bool                            `json:"tombstoned"`
	DesiredStateRevision            *int64                           `json:"desiredStateRevision"`
	RuntimeGeneration               *int64                           `json:"runtimeGeneration"`
	CredentialVersion               *int64                           `json:"credentialVersion"`
	RuntimeVersion                  *int64                           `json:"runtimeVersion"`
	Readiness                       *tradinghttp.ProjectionReadiness `json:"readiness"`
	WriteReadiness                  *tradinghttp.ProjectionReadiness `json:"writeReadiness"`
	LastAppliedDesiredStateRevision *int64                           `json:"lastAppliedDesiredStateRevision"`
	LastAppliedRuntimeGeneration    *int64                           `json:"lastAppliedRuntimeGeneration"`
	BlockedReason                   *string                          `json:"blockedReason"`
	Capabilities                    []runtimeCapabilityView          `json:"capabilities"`
	ObservedAt                      *time.Time                       `json:"observedAt"`
}

type runtimeCapabilityView struct {
	RowID      string                   `json:"rowId"`
	Venue      string                   `json:"venue"`
	Product    string                   `json:"product"`
	Settlement string                   `json:"settlement,omitempty"`
	WriteReady bool                     `json:"writeReady"`
	Operations capabilityOperationsView `json:"operations"`
}

type routeRuntimeView struct {
	RouteID         string `json:"routeId"`
	Product         string `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
	RouteProfileID  string `json:"routeProfileId"`
	RequestedState  string `json:"requestedState"`
	Ready           bool   `json:"ready"`
	Reason          string `json:"reason,omitempty"`
}

type runtimeSnapshotView struct {
	OperationID          string             `json:"operationId"`
	DesiredStateRevision int64              `json:"desiredStateRevision"`
	RuntimeGeneration    int64              `json:"runtimeGeneration"`
	CredentialVersion    int64              `json:"credentialVersion"`
	RequestedState       string             `json:"requestedState"`
	Routes               []routeRuntimeView `json:"routes"`
}

type pendingSnapshotView struct {
	runtimeSnapshotView
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type capabilityOperationsView struct {
	Place                  bool `json:"place"`
	Amend                  bool `json:"amend"`
	Cancel                 bool `json:"cancel"`
	OpenOrders             bool `json:"open_orders"`
	OrderHistory           bool `json:"order_history"`
	Fills                  bool `json:"fills"`
	Balances               bool `json:"balances"`
	Account                bool `json:"account"`
	Positions              bool `json:"positions"`
	Funding                bool `json:"funding"`
	AccountFundingPayments bool `json:"account_funding_payments"`
	Leverage               bool `json:"leverage"`
	PrivateStream          bool `json:"private_stream"`
	MarginMode             bool `json:"margin_mode"`
	PositionMode           bool `json:"position_mode"`
	FundsTransfer          bool `json:"funds_transfer"`
}

// MarketDataSource adapts the client to marketdata.Source. It is a type of its
// own rather than a method set on Client because Client already answers the
// catalog's Fetch, and one type cannot hold two.
type MarketDataSource struct{ client *Client }

// MarketData views the client as a market-data source.
func (client *Client) MarketData() MarketDataSource { return MarketDataSource{client: client} }

// ManualAccounts answers the manual product's account question from the
// Gateway's internal account plane. The public /v1/accounts/{id} view cannot
// serve here: it reports runtime readiness, not the account's lifecycle state,
// environment, or the three fences a preview is sealed against.
type ManualAccounts struct{ client *Client }

// Accounts views the client as the manual product's account authority.
func (client *Client) Accounts() ManualAccounts { return ManualAccounts{client: client} }

// accountRecordView mirrors lifecycle.PublicAccount. Every field the Gateway
// emits must be named because the read decoder refuses unknown fields, so a
// field added there fails loudly here rather than being silently dropped.
type accountRecordView struct {
	ID                   string          `json:"accountId"`
	Venue                string          `json:"venue"`
	Mode                 string          `json:"mode"`
	NativeEnvironment    string          `json:"nativeEnvironment"`
	DisplayName          string          `json:"displayName"`
	ProductPolicy        []string        `json:"productPolicy"`
	Routes               json.RawMessage `json:"routes,omitempty"`
	Status               string          `json:"status"`
	DesiredStateRevision int64           `json:"desiredStateRevision"`
	RuntimeGeneration    int64           `json:"runtimeGeneration"`
	CredentialVersion    int64           `json:"credentialVersion"`
	LastValidatedAt      *time.Time      `json:"lastValidatedAt"`
	UpdatedAt            time.Time       `json:"updatedAt"`
}

func (source ManualAccounts) Get(ctx context.Context, principal, accountID string) (tradinghttp.ManualAccount, error) {
	if source.client == nil {
		return tradinghttp.ManualAccount{}, fmt.Errorf("Gateway account authority is unavailable")
	}
	if strings.TrimSpace(principal) == "" || strings.TrimSpace(accountID) == "" {
		return tradinghttp.ManualAccount{}, fmt.Errorf("Gateway account read scope is invalid")
	}
	var envelope readEnvelope[struct {
		Account accountRecordView `json:"account"`
	}]
	path := "/internal/v1/accounts/" + url.PathEscape(accountID)
	if err := source.client.readLimit(ctx, principal, path, nil, &envelope, authorityResponseLimit); err != nil {
		return tradinghttp.ManualAccount{}, fmt.Errorf("read Gateway account: %w", err)
	}
	account := envelope.Data.Account
	if account.ID != accountID {
		return tradinghttp.ManualAccount{}, fmt.Errorf("Gateway account response is invalid")
	}
	// NativeEnvironment is the venue environment. The Gateway derives both from
	// the same route profile, and the public record carries only this one.
	return tradinghttp.ManualAccount{
		ID:                   account.ID,
		Venue:                account.Venue,
		VenueEnvironment:     account.NativeEnvironment,
		State:                account.Status,
		ProductIDs:           account.ProductPolicy,
		DesiredStateRevision: account.DesiredStateRevision,
		RuntimeGeneration:    account.RuntimeGeneration,
		CredentialVersion:    account.CredentialVersion,
	}, nil
}

func (source MarketDataSource) Fetch(ctx context.Context, query marketdata.Query) (marketdata.Fact, error) {
	if source.client == nil {
		return marketdata.Fact{}, fmt.Errorf("Gateway market-data source is unavailable")
	}
	return source.client.fetchMarketData(ctx, query)
}

type orderIntent struct {
	AccountID            string `json:"accountId,omitempty"`
	InstrumentID         string `json:"instrumentId"`
	Side                 string `json:"side"`
	Quantity             string `json:"quantity"`
	OrderType            string `json:"orderType"`
	LimitPrice           string `json:"limitPrice,omitempty"`
	LimitPolicy          string `json:"limitPolicy,omitempty"`
	TimeInForce          string `json:"timeInForce,omitempty"`
	ReduceOnly           bool   `json:"reduceOnly,omitempty"`
	DesiredStateRevision uint64 `json:"desiredStateRevision"`
	RuntimeGeneration    uint64 `json:"runtimeGeneration"`
	CredentialVersion    uint64 `json:"credentialVersion"`
}

type placeRequest struct {
	AccountID            string `json:"account_id"`
	InstrumentID         string `json:"instrument_id"`
	IdempotencyKey       string `json:"idempotency_key"`
	Side                 string `json:"side"`
	Type                 string `json:"type"`
	Quantity             string `json:"quantity"`
	LimitPrice           string `json:"limit_price,omitempty"`
	LimitPolicy          string `json:"limit_policy,omitempty"`
	ReduceOnly           bool   `json:"reduce_only,omitempty"`
	DesiredStateRevision uint64 `json:"desired_state_revision"`
	RuntimeGeneration    uint64 `json:"runtime_generation"`
	CredentialVersion    uint64 `json:"credential_version"`
}

type placeResponse struct {
	CommandID       string `json:"command_id"`
	GatewayOrderID  string `json:"gateway_order_id"`
	ClientOrderID   string `json:"client_order_id"`
	State           string `json:"state"`
	ResourceVersion int64  `json:"resource_version"`
}

type fillCollection struct {
	Items []struct {
		AccountID       string `json:"account_id"`
		InstrumentID    string `json:"instrument_id"`
		GatewayOrderID  string `json:"gateway_order_id"`
		SettlementAsset string `json:"settlement_asset"`
		Fill            struct {
			FillID   string    `json:"fill_id"`
			Side     string    `json:"side"`
			Price    string    `json:"price"`
			Quantity string    `json:"quantity"`
			Fee      string    `json:"fee"`
			FeeAsset string    `json:"fee_asset"`
			Time     time.Time `json:"time"`
		} `json:"fill"`
	} `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// newClient builds a test-mode client that does not require the Gateway to
// attest its mode back. Only tests use it: a hand-rolled httptest server has no
// reason to know about the attestation header, and demanding it there would
// test the fixture rather than the client.
func newClient(rawURL string, token []byte, httpClient *http.Client) (*Client, error) {
	client, err := NewClientForMode(rawURL, token, orders.ModeTest, false, httpClient)
	if client != nil {
		client.requireAttestation = false
	}
	return client, err
}

// NewClientForMode builds the client trading talks to the Gateway through. It
// refuses at construction rather than at first call: a token too short to be a
// service token, a mode that is neither test nor live, live writes claimed
// under a test mode, or a plaintext origin that is not loopback are all
// configuration mistakes, and the useful moment to say so is startup.
func NewClientForMode(rawURL string, token []byte, mode orders.Mode, liveWrites bool, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || len(token) < 32 || (mode != orders.ModeTest && mode != orders.ModeLive) || (liveWrites && mode != orders.ModeLive) {
		return nil, fmt.Errorf("Gateway client configuration is invalid")
	}
	if parsed.Scheme == "http" {
		address := net.ParseIP(parsed.Hostname())
		if address == nil || !address.IsLoopback() {
			return nil, fmt.Errorf("Gateway client plaintext origin is invalid")
		}
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(parsed.String(), "/"), token: bytes.Clone(token), httpClient: httpClient, mode: mode, liveWrites: liveWrites, requireAttestation: true}, nil
}

func validRequestedState(state string) bool {
	switch state {
	case "creating", "active", "rotating", "draining", "drained", "disabled", "removing", "removed":
		return true
	default:
		return false
	}
}

func (client *Client) Execute(ctx context.Context, request manual.GatewayRequest) (manual.GatewayResult, error) {
	if request.Mode != client.mode || (request.Mode == orders.ModeLive && !client.liveWrites) {
		return manual.GatewayResult{}, orders.ErrLiveWriteDisabled
	}
	intent := orderIntent{
		AccountID: request.AccountID, InstrumentID: request.InstrumentID, Side: request.Side, Quantity: request.Quantity,
		OrderType: request.OrderType, LimitPrice: request.LimitPrice, TimeInForce: request.TimeInForce, ReduceOnly: request.ReduceOnly,
		DesiredStateRevision: uint64(request.DesiredStateRevision), RuntimeGeneration: uint64(request.RuntimeGeneration), CredentialVersion: uint64(request.CredentialVersion),
	}
	handoff, err := client.place(ctx, request.CommandID, intent)
	result := manual.GatewayResult{CommandID: request.CommandID, AccountID: request.AccountID, Mode: request.Mode, State: manualState(handoff.Evidence.State), Handoff: handoff}
	for _, fill := range handoff.Evidence.Fills {
		result.Fills = append(result.Fills, manual.FillFact{FillID: fill.FillID, CommandID: request.CommandID, AccountID: fill.AccountID, InstrumentID: fill.InstrumentID, Mode: request.Mode, Side: portfolio.Side(fill.Side), Quantity: fill.Quantity, Price: fill.Price, Fee: fill.Fee, FeeCurrency: fill.FeeCurrency, SettlementCurrency: fill.SettlementCurrency, OccurredAt: fill.OccurredAt})
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
	if request.Mode != client.mode || (request.Mode == orders.ModeLive && !client.liveWrites) {
		return manual.GatewayResult{}, orders.ErrLiveWriteDisabled
	}
	if request.CommandID == "" || request.AccountID == "" || request.InstrumentID == "" || request.GatewayOrderID == "" {
		return manual.GatewayResult{}, fmt.Errorf("Gateway cancel identity is incomplete")
	}
	handoff, err := client.cancel(ctx, request)
	result := manual.GatewayResult{
		CommandID: request.CommandID, AccountID: request.AccountID, Mode: request.Mode,
		State: manualState(handoff.Evidence.State), Handoff: handoff,
	}
	for _, fill := range handoff.Evidence.Fills {
		result.Fills = append(result.Fills, manual.FillFact{
			FillID: fill.FillID, CommandID: request.CommandID, AccountID: fill.AccountID,
			InstrumentID: fill.InstrumentID, Mode: request.Mode, Side: portfolio.Side(fill.Side),
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

func (client *Client) Handoff(ctx context.Context, command orders.Command) (orders.HandoffResult, error) {
	if command.Mode != client.mode || (command.Mode == orders.ModeLive && !client.liveWrites) {
		return orders.HandoffResult{Outcome: orders.HandoffNotAttempted}, orders.ErrLiveWriteDisabled
	}
	var intent orderIntent
	decoder := json.NewDecoder(bytes.NewReader(command.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffNotAttempted}, fmt.Errorf("Gateway command intent is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return orders.HandoffResult{Outcome: orders.HandoffNotAttempted}, fmt.Errorf("Gateway command intent is invalid")
	}
	if intent.AccountID == "" {
		intent.AccountID = command.AccountID
	}
	return client.place(ctx, command.CommandID, intent)
}

func (client *Client) Reconcile(ctx context.Context, command orders.Command) (orders.HandoffResult, error) {
	current := command.ExecutionEvidence
	if current.GatewayOrderID == "" {
		if command.State == orders.StateUncertain {
			return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: current}, fmt.Errorf("Gateway reconciliation identity is unavailable")
		}
		return client.Handoff(ctx, command)
	}
	var result placeResponse
	if err := client.read(ctx, "/v1/orders/"+url.PathEscape(current.GatewayOrderID), nil, &result); err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: current}, err
	}
	if result.CommandID != current.CommandID || result.GatewayOrderID != current.GatewayOrderID || result.ClientOrderID != current.ClientOrderID || result.ResourceVersion < current.ResourceVersion || result.State == "" {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: current}, fmt.Errorf("Gateway reconciliation identity is invalid")
	}
	evidence := orders.ExecutionEvidence{CommandID: result.CommandID, GatewayOrderID: result.GatewayOrderID, ClientOrderID: result.ClientOrderID, State: result.State, ResourceVersion: result.ResourceVersion,
		OrderEvents: []orders.OrderEventEvidence{{EventID: result.GatewayOrderID + ":" + strconv.FormatInt(result.ResourceVersion, 10), State: result.State, ResourceVersion: result.ResourceVersion}}}
	if result.State == "filled" || result.State == "partially_filled" || result.State == "canceled" || result.State == "cancelled" {
		fills, err := client.fetchFills(ctx, result.GatewayOrderID, command.AccountID, "")
		if err != nil {
			return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: current}, err
		}
		evidence.Fills = fills
	}
	return orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence}, nil
}

func (client *Client) Fetch(ctx context.Context) (catalog.Snapshot, error) {
	var venues readEnvelope[[]catalog.Venue]
	if err := client.read(ctx, "/internal/v1/catalog/venues", nil, &venues); err != nil {
		return catalog.Snapshot{}, fmt.Errorf("read Gateway venues: %w", err)
	}
	var instruments readEnvelope[struct {
		Revision    string               `json:"revision"`
		ObservedAt  time.Time            `json:"observedAt"`
		Instruments []catalog.Instrument `json:"instruments"`
	}]
	if err := client.read(ctx, "/internal/v1/catalog/instruments", nil, &instruments); err != nil {
		return catalog.Snapshot{}, fmt.Errorf("read Gateway instruments: %w", err)
	}
	if strings.TrimSpace(instruments.Data.Revision) == "" || instruments.Data.ObservedAt.IsZero() {
		return catalog.Snapshot{}, fmt.Errorf("Gateway catalog authority is invalid")
	}
	return catalog.Snapshot{
		Revision: instruments.Data.Revision, ObservedAt: instruments.Data.ObservedAt.UTC(),
		Venues: venues.Data, Instruments: instruments.Data.Instruments,
	}, nil
}

func (client *Client) ListAccountRuntimes(ctx context.Context, principal, mode, accountID string) ([]tradinghttp.AccountRuntimeProjection, error) {
	if strings.TrimSpace(principal) == "" || strings.TrimSpace(accountID) == "" || mode != string(client.mode) {
		return nil, fmt.Errorf("Gateway runtime projection scope is invalid")
	}
	var runtime readEnvelope[runtimeProjectionView]
	if err := client.read(ctx, "/internal/v1/accounts/runtime/"+url.PathEscape(accountID), nil, &runtime); err != nil {
		return nil, fmt.Errorf("read Gateway runtime account: %w", err)
	}
	projection := runtime.Data
	if projection.AccountID != accountID || projection.Tombstoned == nil || projection.Readiness == nil || projection.WriteReadiness == nil || projection.Retiring == nil || projection.Capabilities == nil {
		return nil, fmt.Errorf("Gateway runtime account response is invalid")
	}
	if *projection.Tombstoned && (projection.Active != nil || projection.Pending != nil) {
		return nil, fmt.Errorf("Gateway runtime account response is invalid")
	}
	if projection.Active != nil {
		if *projection.Tombstoned || projection.DesiredStateRevision == nil || *projection.DesiredStateRevision < 1 || projection.RuntimeGeneration == nil || *projection.RuntimeGeneration < 1 || projection.CredentialVersion == nil || *projection.CredentialVersion < 1 || projection.RuntimeVersion == nil || *projection.RuntimeVersion < 1 || projection.LastAppliedDesiredStateRevision == nil || *projection.LastAppliedDesiredStateRevision < 1 || projection.LastAppliedRuntimeGeneration == nil || *projection.LastAppliedRuntimeGeneration < 1 || *projection.LastAppliedDesiredStateRevision > *projection.DesiredStateRevision || *projection.LastAppliedRuntimeGeneration > *projection.RuntimeGeneration || projection.ObservedAt == nil || projection.ObservedAt.IsZero() || len(projection.Capabilities) == 0 {
			return nil, fmt.Errorf("Gateway runtime account response is invalid")
		}
	} else if !*projection.Tombstoned && projection.Pending == nil {
		return nil, fmt.Errorf("Gateway runtime account response is invalid")
	}
	projectedCapabilities := make([]tradinghttp.AccountCapabilityProjection, 0, len(projection.Capabilities))
	for _, row := range projection.Capabilities {
		if row.RowID == "" || row.Venue == "" || row.Product == "" {
			return nil, fmt.Errorf("Gateway account capability response is invalid")
		}
		operations := row.Operations
		projectedCapabilities = append(projectedCapabilities, tradinghttp.AccountCapabilityProjection{
			RowID: row.RowID, Venue: row.Venue, Product: row.Product, Settlement: row.Settlement, WriteReady: row.WriteReady,
			Operations: tradinghttp.CapabilityOperations{Place: operations.Place, Amend: operations.Amend, Cancel: operations.Cancel, OpenOrders: operations.OpenOrders, OrderHistory: operations.OrderHistory, Fills: operations.Fills, Balances: operations.Balances, Account: operations.Account, Positions: operations.Positions, Leverage: operations.Leverage, PrivateStream: operations.PrivateStream, MarginMode: operations.MarginMode, PositionMode: operations.PositionMode, FundsTransfer: operations.FundsTransfer},
		})
	}
	active, err := projectRuntimeSnapshot(projection.Active)
	if err != nil {
		return nil, err
	}
	var pending *tradinghttp.PendingRuntimeSnapshotProjection
	if projection.Pending != nil {
		projected, projectErr := projectRuntimeSnapshot(&projection.Pending.runtimeSnapshotView)
		if projectErr != nil || !validPendingRuntimeState(projection.Pending.State) {
			return nil, fmt.Errorf("Gateway pending runtime response is invalid")
		}
		pending = &tradinghttp.PendingRuntimeSnapshotProjection{RuntimeSnapshotProjection: *projected, State: projection.Pending.State, Reason: projection.Pending.Reason}
	}
	retiring := make([]tradinghttp.RuntimeSnapshotProjection, 0, len(projection.Retiring))
	for index := range projection.Retiring {
		projected, projectErr := projectRuntimeSnapshot(&projection.Retiring[index])
		if projectErr != nil {
			return nil, projectErr
		}
		retiring = append(retiring, *projected)
	}
	return []tradinghttp.AccountRuntimeProjection{{
		AccountID: accountID, Active: active, Pending: pending, Retiring: retiring, DesiredStateRevision: int64Value(projection.DesiredStateRevision), RuntimeGeneration: int64Value(projection.RuntimeGeneration),
		CredentialVersion: int64Value(projection.CredentialVersion), RuntimeVersion: int64Value(projection.RuntimeVersion), Readiness: *projection.Readiness, WriteReadiness: *projection.WriteReadiness,
		LastAppliedDesiredStateRevision: int64Value(projection.LastAppliedDesiredStateRevision), LastAppliedRuntimeGeneration: int64Value(projection.LastAppliedRuntimeGeneration),
		BlockedReason: projection.BlockedReason, Capabilities: projectedCapabilities, ObservedAt: timeValue(projection.ObservedAt),
	}}, nil
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func projectRuntimeSnapshot(snapshot *runtimeSnapshotView) (*tradinghttp.RuntimeSnapshotProjection, error) {
	if snapshot == nil {
		return nil, nil
	}
	if _, err := secretstore.ParseOperationID(snapshot.OperationID); err != nil || snapshot.DesiredStateRevision < 1 || snapshot.RuntimeGeneration < 1 || snapshot.CredentialVersion < 1 || !validRequestedState(snapshot.RequestedState) {
		return nil, fmt.Errorf("Gateway runtime snapshot response is invalid")
	}
	routes := make([]tradinghttp.RouteRuntimeProjection, 0, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		if strings.TrimSpace(route.RouteID) == "" || strings.TrimSpace(route.Product) == "" || strings.TrimSpace(route.RouteProfileID) == "" || (route.RequestedState != "active" && route.RequestedState != "disabled") {
			return nil, fmt.Errorf("Gateway runtime route response is invalid")
		}
		routes = append(routes, tradinghttp.RouteRuntimeProjection{RouteID: route.RouteID, Product: route.Product, SettlementAsset: route.SettlementAsset, RouteProfileID: route.RouteProfileID, RequestedState: route.RequestedState, Ready: route.Ready, Reason: route.Reason})
	}
	return &tradinghttp.RuntimeSnapshotProjection{DesiredStateRevision: snapshot.DesiredStateRevision, RuntimeGeneration: snapshot.RuntimeGeneration, CredentialVersion: snapshot.CredentialVersion, RequestedState: snapshot.RequestedState, Routes: routes}, nil
}

func validPendingRuntimeState(state string) bool {
	switch state {
	case "prepared", "candidate_ready", "candidate_failed", "applied":
		return true
	default:
		return false
	}
}

func (client *Client) fetchMarketData(ctx context.Context, query marketdata.Query) (marketdata.Fact, error) {
	values := url.Values{"venue": {query.Venue}, "environment": {query.Environment}, "product": {query.Product}, "instrumentId": {query.InstrumentID}}
	var result readEnvelope[marketdata.Fact]
	if err := client.read(ctx, "/internal/v1/market-data", values, &result); err != nil {
		return marketdata.Fact{}, fmt.Errorf("read Gateway market data: %w", err)
	}
	if result.Data.Venue != query.Venue || result.Data.Environment != query.Environment || result.Data.Product != query.Product || result.Data.InstrumentID != query.InstrumentID || result.Data.ObservedAt.IsZero() {
		return marketdata.Fact{}, fmt.Errorf("Gateway market-data response is invalid")
	}
	result.Data.ObservedAt = result.Data.ObservedAt.UTC()
	return result.Data, nil
}

func (client *Client) CheckMarketDataRoute(ctx context.Context) error {
	var result readEnvelope[struct {
		Revision    string               `json:"revision"`
		ObservedAt  time.Time            `json:"observedAt"`
		Instruments []catalog.Instrument `json:"instruments"`
	}]
	if err := client.read(ctx, "/internal/v1/catalog/instruments", nil, &result); err != nil {
		return fmt.Errorf("probe Gateway catalog authority: %w", err)
	}
	readyInstrument := false
	for _, instrument := range result.Data.Instruments {
		if instrument.Ready {
			readyInstrument = true
			break
		}
	}
	if strings.TrimSpace(result.Data.Revision) == "" || result.Data.ObservedAt.IsZero() || result.Data.Instruments == nil || (len(result.Data.Instruments) > 0 && !readyInstrument) {
		return fmt.Errorf("Gateway market-data route is unavailable")
	}
	return nil
}

func (client *Client) read(ctx context.Context, path string, query url.Values, target any) error {
	return client.readLimit(ctx, "", path, query, target, 64<<10)
}

// readLimit carries an optional principal because part of the Gateway's private
// plane refuses a read that cannot say who is asking: the service token proves
// which service called, not which user it called for. An empty principal sends
// no header, which is what the routes that answer for the service itself want.
func (client *Client) readLimit(ctx context.Context, principal, path string, query url.Values, target any, limit int64) error {
	requestURL := client.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return fmt.Errorf("construct Gateway request")
	}
	request.Header.Set("Authorization", "Bearer "+string(client.token))
	if principal != "" {
		request.Header.Set("X-Bolter-Principal", principal)
	}
	client.attest(request)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("Gateway read failed")
	}
	defer response.Body.Close()
	if err := client.verifyAttestation(response); err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("Gateway read rejected with status %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("Gateway read response is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("Gateway read response is invalid")
	}
	return nil
}

func (client *Client) place(ctx context.Context, idempotencyID string, intent orderIntent) (orders.HandoffResult, error) {
	orderType := strings.ToLower(intent.OrderType)
	policy := strings.ToLower(intent.LimitPolicy)
	if policy == "" && orderType == "limit" {
		policy = strings.ToLower(intent.TimeInForce)
		if policy == "gtc" {
			policy = "resting"
		}
	}
	if idempotencyID == "" || intent.AccountID == "" || intent.InstrumentID == "" || intent.Side == "" || intent.Quantity == "" || (orderType != "market" && orderType != "limit") {
		return orders.HandoffResult{Outcome: orders.HandoffNotAttempted}, fmt.Errorf("Gateway order intent is incomplete")
	}
	if intent.DesiredStateRevision == 0 || intent.RuntimeGeneration == 0 || intent.CredentialVersion == 0 {
		return orders.HandoffResult{Outcome: orders.HandoffNotAttempted}, fmt.Errorf("Gateway order fence is incomplete")
	}
	payload, err := json.Marshal(placeRequest{
		AccountID: intent.AccountID, InstrumentID: intent.InstrumentID, IdempotencyKey: idempotencyID,
		Side: intent.Side, Type: orderType, Quantity: intent.Quantity, LimitPrice: intent.LimitPrice, LimitPolicy: policy, ReduceOnly: intent.ReduceOnly,
		DesiredStateRevision: intent.DesiredStateRevision, RuntimeGeneration: intent.RuntimeGeneration, CredentialVersion: intent.CredentialVersion,
	})
	if err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffNotAttempted}, fmt.Errorf("encode Gateway order")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/v1/orders", bytes.NewReader(payload))
	if err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffNotAttempted}, fmt.Errorf("construct Gateway request")
	}
	request.Header.Set("Authorization", "Bearer "+string(client.token))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyID)
	client.attest(request)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}, fmt.Errorf("Gateway order handoff failed")
	}
	defer response.Body.Close()
	if err := client.verifyAttestation(response); err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}, err
	}
	if response.StatusCode != http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		outcome := orders.HandoffOutcomeUnknown
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusRequestTimeout {
			outcome = orders.HandoffRejected
		}
		return orders.HandoffResult{Outcome: outcome}, fmt.Errorf("Gateway order handoff rejected with status %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	decoder.DisallowUnknownFields()
	var result placeResponse
	if err := decoder.Decode(&result); err != nil || result.CommandID == "" || result.GatewayOrderID == "" || result.ClientOrderID == "" || result.State == "" || result.ResourceVersion < 1 {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}, fmt.Errorf("Gateway order response is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}, fmt.Errorf("Gateway order response is invalid")
	}
	evidence := orders.ExecutionEvidence{
		CommandID: result.CommandID, GatewayOrderID: result.GatewayOrderID, ClientOrderID: result.ClientOrderID,
		State: result.State, ResourceVersion: result.ResourceVersion,
		OrderEvents: []orders.OrderEventEvidence{{EventID: result.GatewayOrderID + ":" + strconv.FormatInt(result.ResourceVersion, 10), State: result.State, ResourceVersion: result.ResourceVersion}},
	}
	if result.State == "filled" || result.State == "partially_filled" || result.State == "canceled" || result.State == "cancelled" {
		fills, fillErr := client.fetchFills(ctx, result.GatewayOrderID, intent.AccountID, intent.InstrumentID)
		if fillErr != nil {
			return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: evidence}, fmt.Errorf("read Gateway fill evidence: %w", fillErr)
		}
		evidence.Fills = fills
	}
	return orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence}, nil
}

func (client *Client) cancel(ctx context.Context, request manual.GatewayCancelRequest) (orders.HandoffResult, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodDelete, client.baseURL+"/v1/orders/"+url.PathEscape(request.GatewayOrderID), nil)
	if err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffNotAttempted}, fmt.Errorf("construct Gateway cancel request")
	}
	httpRequest.Header.Set("Authorization", "Bearer "+string(client.token))
	client.attest(httpRequest)
	response, err := client.httpClient.Do(httpRequest)
	if err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}, fmt.Errorf("Gateway cancel handoff failed")
	}
	defer response.Body.Close()
	if err := client.verifyAttestation(response); err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}, err
	}
	if response.StatusCode != http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		outcome := orders.HandoffOutcomeUnknown
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusRequestTimeout {
			outcome = orders.HandoffRejected
		}
		return orders.HandoffResult{Outcome: outcome}, fmt.Errorf("Gateway cancel handoff rejected with status %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	decoder.DisallowUnknownFields()
	var result placeResponse
	if err := decoder.Decode(&result); err != nil || result.CommandID != request.CommandID || result.GatewayOrderID != request.GatewayOrderID || result.ClientOrderID == "" || result.State == "" || result.ResourceVersion < 1 {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}, fmt.Errorf("Gateway cancel response is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}, fmt.Errorf("Gateway cancel response is invalid")
	}
	evidence := orders.ExecutionEvidence{
		CommandID: result.CommandID, GatewayOrderID: result.GatewayOrderID, ClientOrderID: result.ClientOrderID,
		State: result.State, ResourceVersion: result.ResourceVersion,
		OrderEvents: []orders.OrderEventEvidence{{EventID: result.GatewayOrderID + ":" + strconv.FormatInt(result.ResourceVersion, 10), State: result.State, ResourceVersion: result.ResourceVersion}},
	}
	if result.State == "filled" || result.State == "partially_filled" || result.State == "canceled" || result.State == "cancelled" {
		fills, fillErr := client.fetchFills(ctx, result.GatewayOrderID, request.AccountID, request.InstrumentID)
		if fillErr != nil {
			return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: evidence}, fmt.Errorf("read Gateway fill evidence: %w", fillErr)
		}
		evidence.Fills = fills
	}
	return orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence}, nil
}

const maxFillPages = 100

func (client *Client) fetchFills(ctx context.Context, gatewayOrderID, accountID, instrumentID string) ([]orders.FillEvidence, error) {
	query := url.Values{"gateway_order_id": {gatewayOrderID}, "account_id": {accountID}, "instrument_id": {instrumentID}, "pagination.limit": {"100"}}
	fills := make([]orders.FillEvidence, 0)
	seenCursors := map[string]struct{}{"": {}}
	for page := 0; page < maxFillPages; page++ {
		var result fillCollection
		if err := client.read(ctx, "/v1/fills", query, &result); err != nil {
			return nil, fmt.Errorf("read Gateway fills: %w", err)
		}
		for _, item := range result.Items {
			if item.SettlementAsset == "" {
				return nil, fmt.Errorf("Gateway fill response is missing settlement currency")
			}
			fills = append(fills, orders.FillEvidence{FillID: item.Fill.FillID, AccountID: item.AccountID, InstrumentID: item.InstrumentID, Side: item.Fill.Side, Quantity: item.Fill.Quantity, Price: item.Fill.Price, Fee: item.Fill.Fee, FeeCurrency: item.Fill.FeeAsset, SettlementCurrency: item.SettlementAsset, OccurredAt: item.Fill.Time})
		}
		if result.NextCursor == "" {
			return fills, nil
		}
		if _, seen := seenCursors[result.NextCursor]; seen {
			return nil, fmt.Errorf("Gateway fill pagination cursor cycled")
		}
		seenCursors[result.NextCursor] = struct{}{}
		query.Set("pagination.cursor", result.NextCursor)
	}
	return nil, fmt.Errorf("Gateway fill pagination exceeded %d pages", maxFillPages)
}

func (client *Client) attest(request *http.Request) {
	if client.requireAttestation {
		request.Header.Set(ModeHeader, string(client.mode))
	}
}

func (client *Client) verifyAttestation(response *http.Response) error {
	if client.requireAttestation && response.Header.Get(ModeHeader) != string(client.mode) {
		return fmt.Errorf("Gateway mode attestation mismatch")
	}
	return nil
}
