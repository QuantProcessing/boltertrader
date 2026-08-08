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

func manualState(state string) manual.GatewayState {
	switch strings.ToLower(state) {
	case "filled":
		return manual.GatewayStateFilled
	case "partially_filled":
		return manual.GatewayStatePartiallyFilled
	case "rejected", "failed", "canceled", "cancelled":
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
	if result.State == "filled" || result.State == "partially_filled" {
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
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
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
	if result.State == "filled" || result.State == "partially_filled" {
		fills, fillErr := adapter.fetchFills(ctx, result.GatewayOrderID, intent.AccountID, intent.InstrumentID)
		if fillErr != nil {
			return orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: evidence}, fmt.Errorf("read Gateway fill evidence: %w", fillErr)
		}
		evidence.Fills = fills
	}
	return orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence}, nil
}

func (adapter *gatewayAdapter) fetchFills(ctx context.Context, gatewayOrderID, accountID, instrumentID string) ([]orders.FillEvidence, error) {
	query := url.Values{"gateway_order_id": {gatewayOrderID}, "account_id": {accountID}, "instrument_id": {instrumentID}, "pagination.limit": {"100"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, adapter.baseURL+"/v1/fills?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("construct Gateway fill request")
	}
	request.Header.Set("Authorization", "Bearer "+string(adapter.token))
	adapter.attest(request)
	response, err := adapter.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Gateway fill read failed")
	}
	defer response.Body.Close()
	if err := adapter.verifyAttestation(response); err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return nil, fmt.Errorf("Gateway fill read rejected with status %d", response.StatusCode)
	}
	var result gatewayFillCollection
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("Gateway fill response is invalid")
	}
	fills := make([]orders.FillEvidence, 0, len(result.Items))
	for _, item := range result.Items {
		if item.SettlementAsset == "" {
			return nil, fmt.Errorf("Gateway fill response is missing settlement currency")
		}
		fills = append(fills, orders.FillEvidence{FillID: item.Fill.FillID, AccountID: item.AccountID, InstrumentID: item.InstrumentID, Side: item.Fill.Side, Quantity: item.Fill.Quantity, Price: item.Fill.Price, Fee: item.Fill.Fee, FeeCurrency: item.Fill.FeeAsset, SettlementCurrency: item.SettlementAsset, OccurredAt: item.Fill.Time})
	}
	return fills, nil
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
