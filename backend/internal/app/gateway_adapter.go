package app

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

	"github.com/QuantProcessing/boltertrader/backend/internal/httpapi"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/catalog"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/marketdata"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/accounting"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/manual"
)

type gatewayAdapter struct {
	baseURL            string
	token              []byte
	client             *http.Client
	mode               orders.Mode
	liveWrites         bool
	requireAttestation bool
}

const gatewayModeHeader = "X-Bolter-Mode"

type gatewayReadEnvelope[T any] struct {
	Data T `json:"data"`
}

type gatewayRuntimeProjection struct {
	AccountID                       string                      `json:"accountId"`
	DesiredStateRevision            int64                       `json:"desiredStateRevision"`
	RuntimeGeneration               int64                       `json:"runtimeGeneration"`
	CredentialVersion               int64                       `json:"credentialVersion"`
	RuntimeVersion                  int64                       `json:"runtimeVersion"`
	Readiness                       httpapi.ProjectionReadiness `json:"readiness"`
	WriteReadiness                  httpapi.ProjectionReadiness `json:"writeReadiness"`
	LastAppliedDesiredStateRevision int64                       `json:"lastAppliedDesiredStateRevision"`
	LastAppliedRuntimeGeneration    int64                       `json:"lastAppliedRuntimeGeneration"`
	BlockedReason                   *string                     `json:"blockedReason"`
	Capabilities                    []gatewayRuntimeCapability  `json:"capabilities"`
	ObservedAt                      time.Time                   `json:"observedAt"`
}

type gatewayRuntimeCapability struct {
	RowID      string                      `json:"rowId"`
	Venue      string                      `json:"venue"`
	Product    string                      `json:"product"`
	Settlement string                      `json:"settlement,omitempty"`
	WriteReady bool                        `json:"writeReady"`
	Operations gatewayCapabilityOperations `json:"operations"`
}

type gatewayCapabilityOperations struct {
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

type gatewayMarketDataSource struct{ adapter *gatewayAdapter }

type marketDataRouteProbe interface {
	CheckMarketDataRoute(context.Context) error
}

func (source gatewayMarketDataSource) Fetch(ctx context.Context, query marketdata.Query) (marketdata.Fact, error) {
	if source.adapter == nil {
		return marketdata.Fact{}, fmt.Errorf("Gateway market-data source is unavailable")
	}
	return source.adapter.fetchMarketData(ctx, query)
}

type gatewayOrderIntent struct {
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

type gatewayPlaceRequest struct {
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

type gatewayPlaceResponse struct {
	CommandID       string `json:"command_id"`
	GatewayOrderID  string `json:"gateway_order_id"`
	ClientOrderID   string `json:"client_order_id"`
	State           string `json:"state"`
	ResourceVersion int64  `json:"resource_version"`
}

type gatewayFillCollection struct {
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

type gatewayMarkPrice struct {
	AccountID    string    `json:"account_id"`
	InstrumentID string    `json:"instrument_id"`
	Price        string    `json:"price"`
	ObservedAt   time.Time `json:"observed_at"`
}

type gatewayFundingPaymentPage struct {
	Items      []gatewayFundingPayment `json:"items"`
	NextCursor string                  `json:"next_cursor,omitempty"`
	Scope      struct {
		AccountID       string    `json:"account_id"`
		InstrumentID    string    `json:"instrument_id,omitempty"`
		AllInstruments  bool      `json:"all_instruments"`
		SettlementAsset string    `json:"settlement_asset,omitempty"`
		Start           time.Time `json:"start"`
		End             time.Time `json:"end"`
	} `json:"scope"`
	AccountID       string    `json:"account_id"`
	InstrumentID    string    `json:"instrument_id,omitempty"`
	SettlementAsset string    `json:"settlement_asset,omitempty"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	Watermark       struct {
		Revision         int64     `json:"revision"`
		ObservedAt       time.Time `json:"observed_at"`
		CompleteThrough  time.Time `json:"complete_through"`
		Verified         bool      `json:"verified"`
		Cursor           string    `json:"cursor,omitempty"`
		EffectiveThrough time.Time `json:"effective_through,omitempty"`
	} `json:"watermark"`
	Status              string          `json:"status"`
	AsOf                time.Time       `json:"as_of"`
	CompleteThrough     time.Time       `json:"complete_through,omitempty"`
	Revision            int64           `json:"revision"`
	Page                json.RawMessage `json:"page"`
	PaginationTraversal json.RawMessage `json:"pagination_traversal"`
}

type gatewayFundingPayment struct {
	FactID           string    `json:"fact_id"`
	SupersedesFactID string    `json:"supersedes_fact_id,omitempty"`
	AccountID        string    `json:"account_id"`
	InstrumentID     string    `json:"instrument_id"`
	SettlementAsset  string    `json:"settlement_asset"`
	Amount           string    `json:"amount"`
	EffectiveAt      time.Time `json:"effective_at"`
	ObservedAt       time.Time `json:"observed_at,omitempty"`
	Revision         int64     `json:"revision,omitempty"`
	PaymentID        string    `json:"payment_id,omitempty"`
	Venue            string    `json:"venue"`
	VenueEnvironment string    `json:"venue_environment"`
	Product          string    `json:"product"`
	Version          string    `json:"version,omitempty"`
	SourceRef        string    `json:"source_ref,omitempty"`
	FormulaVersion   string    `json:"formula_version,omitempty"`
}

func newGatewayAdapter(rawURL string, token []byte, client *http.Client) (*gatewayAdapter, error) {
	adapter, err := newGatewayAdapterForMode(rawURL, token, orders.ModeTest, false, client)
	if adapter != nil {
		adapter.requireAttestation = false
	}
	return adapter, err
}

func newGatewayAdapterForMode(rawURL string, token []byte, mode orders.Mode, liveWrites bool, client *http.Client) (*gatewayAdapter, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || len(token) < 32 || (mode != orders.ModeTest && mode != orders.ModeLive) || (liveWrites && mode != orders.ModeLive) {
		return nil, fmt.Errorf("Gateway adapter configuration is invalid")
	}
	if parsed.Scheme == "http" {
		address := net.ParseIP(parsed.Hostname())
		if address == nil || !address.IsLoopback() {
			return nil, fmt.Errorf("Gateway adapter plaintext origin is invalid")
		}
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &gatewayAdapter{baseURL: strings.TrimRight(parsed.String(), "/"), token: bytes.Clone(token), client: client, mode: mode, liveWrites: liveWrites, requireAttestation: true}, nil
}

func (adapter *gatewayAdapter) Execute(ctx context.Context, request manual.GatewayRequest) (manual.GatewayResult, error) {
	if request.Mode != adapter.mode || (request.Mode == orders.ModeLive && !adapter.liveWrites) {
		return manual.GatewayResult{}, orders.ErrLiveWriteDisabled
	}
	intent := gatewayOrderIntent{
		AccountID: request.AccountID, InstrumentID: request.InstrumentID, Side: request.Side, Quantity: request.Quantity,
		OrderType: request.OrderType, LimitPrice: request.LimitPrice, TimeInForce: request.TimeInForce, ReduceOnly: request.ReduceOnly,
		DesiredStateRevision: uint64(request.DesiredStateRevision), RuntimeGeneration: uint64(request.RuntimeGeneration), CredentialVersion: uint64(request.CredentialVersion),
	}
	handoff, err := adapter.place(ctx, request.CommandID, intent)
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

func (adapter *gatewayAdapter) Cancel(ctx context.Context, request manual.GatewayCancelRequest) (manual.GatewayResult, error) {
	if request.Mode != adapter.mode || (request.Mode == orders.ModeLive && !adapter.liveWrites) {
		return manual.GatewayResult{}, orders.ErrLiveWriteDisabled
	}
	if request.CommandID == "" || request.AccountID == "" || request.InstrumentID == "" || request.GatewayOrderID == "" {
		return manual.GatewayResult{}, fmt.Errorf("Gateway cancel identity is incomplete")
	}
	handoff, err := adapter.cancel(ctx, request)
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

func (adapter *gatewayAdapter) Handoff(ctx context.Context, command orders.Command) (orders.HandoffResult, error) {
	if command.Mode != adapter.mode || (command.Mode == orders.ModeLive && !adapter.liveWrites) {
		return orders.HandoffResult{Outcome: orders.HandoffNotAttempted}, orders.ErrLiveWriteDisabled
	}
	var intent gatewayOrderIntent
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
	return adapter.place(ctx, command.CommandID, intent)
}

func (adapter *gatewayAdapter) Reconcile(ctx context.Context, command orders.Command) (orders.HandoffResult, error) {
	current := command.ExecutionEvidence
	if current.GatewayOrderID == "" {
		if command.State == orders.StateUncertain {
			return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: current}, fmt.Errorf("Gateway reconciliation identity is unavailable")
		}
		return adapter.Handoff(ctx, command)
	}
	var result gatewayPlaceResponse
	if err := adapter.read(ctx, "/v1/orders/"+url.PathEscape(current.GatewayOrderID), nil, &result); err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: current}, err
	}
	if result.CommandID != current.CommandID || result.GatewayOrderID != current.GatewayOrderID || result.ClientOrderID != current.ClientOrderID || result.ResourceVersion < current.ResourceVersion || result.State == "" {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: current}, fmt.Errorf("Gateway reconciliation identity is invalid")
	}
	evidence := orders.ExecutionEvidence{CommandID: result.CommandID, GatewayOrderID: result.GatewayOrderID, ClientOrderID: result.ClientOrderID, State: result.State, ResourceVersion: result.ResourceVersion,
		OrderEvents: []orders.OrderEventEvidence{{EventID: result.GatewayOrderID + ":" + strconv.FormatInt(result.ResourceVersion, 10), State: result.State, ResourceVersion: result.ResourceVersion}}}
	if result.State == "filled" || result.State == "partially_filled" || result.State == "canceled" || result.State == "cancelled" {
		fills, err := adapter.fetchFills(ctx, result.GatewayOrderID, command.AccountID, "")
		if err != nil {
			return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: current}, err
		}
		evidence.Fills = fills
	}
	return orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence}, nil
}

func (adapter *gatewayAdapter) Fetch(ctx context.Context) (catalog.Snapshot, error) {
	var venues gatewayReadEnvelope[[]catalog.Venue]
	if err := adapter.read(ctx, "/internal/v1/catalog/venues", nil, &venues); err != nil {
		return catalog.Snapshot{}, fmt.Errorf("read Gateway venues: %w", err)
	}
	var instruments gatewayReadEnvelope[struct {
		Revision    string               `json:"revision"`
		ObservedAt  time.Time            `json:"observedAt"`
		Instruments []catalog.Instrument `json:"instruments"`
	}]
	if err := adapter.read(ctx, "/internal/v1/catalog/instruments", nil, &instruments); err != nil {
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

func (adapter *gatewayAdapter) ListAccountRuntimes(ctx context.Context, principal, mode, accountID string) ([]httpapi.AccountRuntimeProjection, error) {
	if strings.TrimSpace(principal) == "" || strings.TrimSpace(accountID) == "" || mode != string(adapter.mode) {
		return nil, fmt.Errorf("Gateway runtime projection scope is invalid")
	}
	var runtime gatewayReadEnvelope[gatewayRuntimeProjection]
	if err := adapter.read(ctx, "/internal/v1/accounts/runtime/"+url.PathEscape(accountID), nil, &runtime); err != nil {
		return nil, fmt.Errorf("read Gateway runtime account: %w", err)
	}
	projection := runtime.Data
	if projection.AccountID != accountID || projection.DesiredStateRevision < 1 || projection.RuntimeGeneration < 1 || projection.CredentialVersion < 1 || projection.RuntimeVersion < 1 || projection.LastAppliedDesiredStateRevision < 1 || projection.LastAppliedRuntimeGeneration < 1 || projection.LastAppliedDesiredStateRevision > projection.DesiredStateRevision || projection.LastAppliedRuntimeGeneration > projection.RuntimeGeneration || projection.ObservedAt.IsZero() || len(projection.Capabilities) == 0 {
		return nil, fmt.Errorf("Gateway runtime account response is invalid")
	}
	projectedCapabilities := make([]httpapi.AccountCapabilityProjection, 0, len(projection.Capabilities))
	for _, row := range projection.Capabilities {
		if row.RowID == "" || row.Venue == "" || row.Product == "" {
			return nil, fmt.Errorf("Gateway account capability response is invalid")
		}
		operations := row.Operations
		projectedCapabilities = append(projectedCapabilities, httpapi.AccountCapabilityProjection{
			RowID: row.RowID, Venue: row.Venue, Product: row.Product, Settlement: row.Settlement, WriteReady: row.WriteReady,
			Operations: httpapi.CapabilityOperations{Place: operations.Place, Amend: operations.Amend, Cancel: operations.Cancel, OpenOrders: operations.OpenOrders, OrderHistory: operations.OrderHistory, Fills: operations.Fills, Balances: operations.Balances, Account: operations.Account, Positions: operations.Positions, Funding: operations.Funding, AccountFundingPayments: operations.AccountFundingPayments, Leverage: operations.Leverage, PrivateStream: operations.PrivateStream, MarginMode: operations.MarginMode, PositionMode: operations.PositionMode, FundsTransfer: operations.FundsTransfer},
		})
	}
	return []httpapi.AccountRuntimeProjection{{
		AccountID: accountID, DesiredStateRevision: projection.DesiredStateRevision, RuntimeGeneration: projection.RuntimeGeneration,
		CredentialVersion: projection.CredentialVersion, RuntimeVersion: projection.RuntimeVersion, Readiness: projection.Readiness, WriteReadiness: projection.WriteReadiness,
		LastAppliedDesiredStateRevision: projection.LastAppliedDesiredStateRevision, LastAppliedRuntimeGeneration: projection.LastAppliedRuntimeGeneration,
		BlockedReason: projection.BlockedReason, Capabilities: projectedCapabilities, ObservedAt: projection.ObservedAt.UTC(),
	}}, nil
}

func (adapter *gatewayAdapter) fetchMarketData(ctx context.Context, query marketdata.Query) (marketdata.Fact, error) {
	values := url.Values{"venue": {query.Venue}, "environment": {query.Environment}, "product": {query.Product}, "instrumentId": {query.InstrumentID}}
	var result gatewayReadEnvelope[marketdata.Fact]
	if err := adapter.read(ctx, "/internal/v1/market-data", values, &result); err != nil {
		return marketdata.Fact{}, fmt.Errorf("read Gateway market data: %w", err)
	}
	if result.Data.Venue != query.Venue || result.Data.Environment != query.Environment || result.Data.Product != query.Product || result.Data.InstrumentID != query.InstrumentID || result.Data.ObservedAt.IsZero() {
		return marketdata.Fact{}, fmt.Errorf("Gateway market-data response is invalid")
	}
	result.Data.ObservedAt = result.Data.ObservedAt.UTC()
	return result.Data, nil
}

func (adapter *gatewayAdapter) MarkPrice(ctx context.Context, accountID, instrumentID string) (accounting.MarkPrice, error) {
	if accountID == "" || instrumentID == "" {
		return accounting.MarkPrice{}, fmt.Errorf("Gateway mark-price scope is invalid")
	}
	var result gatewayMarkPrice
	if err := adapter.read(ctx, "/v1/mark-price", url.Values{"account_id": {accountID}, "instrument_id": {instrumentID}}, &result); err != nil {
		return accounting.MarkPrice{}, fmt.Errorf("read Gateway mark price: %w", err)
	}
	if result.AccountID != accountID || result.InstrumentID != instrumentID || result.Price == "" || result.ObservedAt.IsZero() {
		return accounting.MarkPrice{}, fmt.Errorf("Gateway mark-price response is invalid")
	}
	return accounting.MarkPrice{AccountID: result.AccountID, InstrumentID: result.InstrumentID, Price: result.Price, ObservedAt: result.ObservedAt.UTC()}, nil
}

func (adapter *gatewayAdapter) FundingPayments(ctx context.Context, query accounting.FundingPaymentQuery) (accounting.FundingPaymentPage, error) {
	if query.AccountID == "" || query.InstrumentID == "" || query.SettlementAsset == "" || query.Start.IsZero() || query.End.IsZero() || !query.Start.Before(query.End) || query.Limit < 1 || query.Limit > 500 {
		return accounting.FundingPaymentPage{}, fmt.Errorf("Gateway funding-payment scope is invalid")
	}
	values := url.Values{
		"account_id": {query.AccountID}, "instrument_id": {query.InstrumentID}, "settlement_asset": {query.SettlementAsset},
		"start": {query.Start.UTC().Format(time.RFC3339Nano)}, "end": {query.End.UTC().Format(time.RFC3339Nano)}, "pagination.limit": {strconv.Itoa(query.Limit)},
	}
	if query.Cursor != "" {
		values.Set("pagination.cursor", query.Cursor)
	}
	var result gatewayFundingPaymentPage
	if err := adapter.readLimit(ctx, "/v1/funding-payments", values, &result, 1<<20); err != nil {
		return accounting.FundingPaymentPage{}, fmt.Errorf("read Gateway funding payments: %w", err)
	}
	if result.AccountID != query.AccountID || result.InstrumentID != query.InstrumentID || result.SettlementAsset != query.SettlementAsset || result.Scope.AccountID != query.AccountID || result.Scope.InstrumentID != query.InstrumentID || result.Scope.SettlementAsset != query.SettlementAsset || !result.Start.Equal(query.Start) || !result.End.Equal(query.End) || !result.Scope.Start.Equal(query.Start) || !result.Scope.End.Equal(query.End) || result.AsOf.IsZero() {
		return accounting.FundingPaymentPage{}, fmt.Errorf("Gateway funding-payment response is invalid")
	}
	page := accounting.FundingPaymentPage{
		AccountID: result.AccountID, InstrumentID: result.InstrumentID, NextCursor: result.NextCursor,
		Start: result.Start.UTC(), End: result.End.UTC(), CompleteThrough: result.CompleteThrough.UTC(), ObservedAt: result.Watermark.ObservedAt.UTC(),
		Revision: result.Revision, Status: accounting.Completeness(result.Status), Verified: result.Watermark.Verified,
		Items: make([]accounting.FundingPayment, 0, len(result.Items)),
	}
	for _, item := range result.Items {
		page.Items = append(page.Items, accounting.FundingPayment{
			FactID: item.FactID, SupersedesFactID: item.SupersedesFactID, AccountID: item.AccountID, InstrumentID: item.InstrumentID,
			SettlementAsset: item.SettlementAsset, Amount: item.Amount, EffectiveAt: item.EffectiveAt.UTC(), ObservedAt: item.ObservedAt.UTC(), Revision: item.Revision,
		})
	}
	return page, nil
}

func (adapter *gatewayAdapter) CheckMarketDataRoute(ctx context.Context) error {
	var result gatewayReadEnvelope[struct {
		Ready bool `json:"ready"`
	}]
	if err := adapter.read(ctx, "/internal/v1/market-data/readiness", nil, &result); err != nil {
		return fmt.Errorf("probe Gateway market-data route: %w", err)
	}
	if !result.Data.Ready {
		return fmt.Errorf("Gateway market-data route is unavailable")
	}
	return nil
}

func (adapter *gatewayAdapter) read(ctx context.Context, path string, query url.Values, target any) error {
	return adapter.readLimit(ctx, path, query, target, 64<<10)
}

func (adapter *gatewayAdapter) readLimit(ctx context.Context, path string, query url.Values, target any, limit int64) error {
	requestURL := adapter.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return fmt.Errorf("construct Gateway request")
	}
	request.Header.Set("Authorization", "Bearer "+string(adapter.token))
	adapter.attest(request)
	response, err := adapter.client.Do(request)
	if err != nil {
		return fmt.Errorf("Gateway read failed")
	}
	defer response.Body.Close()
	if err := adapter.verifyAttestation(response); err != nil {
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

func (adapter *gatewayAdapter) place(ctx context.Context, idempotencyID string, intent gatewayOrderIntent) (orders.HandoffResult, error) {
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
	payload, err := json.Marshal(gatewayPlaceRequest{
		AccountID: intent.AccountID, InstrumentID: intent.InstrumentID, IdempotencyKey: idempotencyID,
		Side: intent.Side, Type: orderType, Quantity: intent.Quantity, LimitPrice: intent.LimitPrice, LimitPolicy: policy, ReduceOnly: intent.ReduceOnly,
		DesiredStateRevision: intent.DesiredStateRevision, RuntimeGeneration: intent.RuntimeGeneration, CredentialVersion: intent.CredentialVersion,
	})
	if err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffNotAttempted}, fmt.Errorf("encode Gateway order")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, adapter.baseURL+"/v1/orders", bytes.NewReader(payload))
	if err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffNotAttempted}, fmt.Errorf("construct Gateway request")
	}
	request.Header.Set("Authorization", "Bearer "+string(adapter.token))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyID)
	adapter.attest(request)
	response, err := adapter.client.Do(request)
	if err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}, fmt.Errorf("Gateway order handoff failed")
	}
	defer response.Body.Close()
	if err := adapter.verifyAttestation(response); err != nil {
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
	var result gatewayPlaceResponse
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
		fills, fillErr := adapter.fetchFills(ctx, result.GatewayOrderID, intent.AccountID, intent.InstrumentID)
		if fillErr != nil {
			return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: evidence}, fmt.Errorf("read Gateway fill evidence: %w", fillErr)
		}
		evidence.Fills = fills
	}
	return orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence}, nil
}

func (adapter *gatewayAdapter) cancel(ctx context.Context, request manual.GatewayCancelRequest) (orders.HandoffResult, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodDelete, adapter.baseURL+"/v1/orders/"+url.PathEscape(request.GatewayOrderID), nil)
	if err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffNotAttempted}, fmt.Errorf("construct Gateway cancel request")
	}
	httpRequest.Header.Set("Authorization", "Bearer "+string(adapter.token))
	adapter.attest(httpRequest)
	response, err := adapter.client.Do(httpRequest)
	if err != nil {
		return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}, fmt.Errorf("Gateway cancel handoff failed")
	}
	defer response.Body.Close()
	if err := adapter.verifyAttestation(response); err != nil {
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
	var result gatewayPlaceResponse
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
		fills, fillErr := adapter.fetchFills(ctx, result.GatewayOrderID, request.AccountID, request.InstrumentID)
		if fillErr != nil {
			return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: evidence}, fmt.Errorf("read Gateway fill evidence: %w", fillErr)
		}
		evidence.Fills = fills
	}
	return orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence}, nil
}

const maxGatewayFillPages = 100

func (adapter *gatewayAdapter) fetchFills(ctx context.Context, gatewayOrderID, accountID, instrumentID string) ([]orders.FillEvidence, error) {
	query := url.Values{"gateway_order_id": {gatewayOrderID}, "account_id": {accountID}, "instrument_id": {instrumentID}, "pagination.limit": {"100"}}
	fills := make([]orders.FillEvidence, 0)
	seenCursors := map[string]struct{}{"": {}}
	for page := 0; page < maxGatewayFillPages; page++ {
		var result gatewayFillCollection
		if err := adapter.read(ctx, "/v1/fills", query, &result); err != nil {
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
	return nil, fmt.Errorf("Gateway fill pagination exceeded %d pages", maxGatewayFillPages)
}

func (adapter *gatewayAdapter) attest(request *http.Request) {
	if adapter.requireAttestation {
		request.Header.Set(gatewayModeHeader, string(adapter.mode))
	}
}

func (adapter *gatewayAdapter) verifyAttestation(response *http.Response) error {
	if adapter.requireAttestation && response.Header.Get(gatewayModeHeader) != string(adapter.mode) {
		return fmt.Errorf("Gateway mode attestation mismatch")
	}
	return nil
}
