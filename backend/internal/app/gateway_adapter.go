package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantProcessing/boltertrader/backend/internal/httpapi"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/catalog"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/marketdata"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/accounting"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/manual"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
)

type gatewayAdapter struct {
	baseURL            string
	token              []byte
	client             *http.Client
	mode               orders.Mode
	liveWrites         bool
	requireAttestation bool
}

const (
	gatewayModeHeader             = "X-Bolter-Mode"
	gatewayAuthorityResponseLimit = 64 << 10
)

type gatewayReadEnvelope[T any] struct {
	Data T `json:"data"`
}

type gatewayRuntimeProjection struct {
	AccountID                       string                       `json:"accountId"`
	Active                          *gatewayRuntimeSnapshotView  `json:"active,omitempty"`
	Pending                         *gatewayPendingSnapshotView  `json:"pending,omitempty"`
	Retiring                        []gatewayRuntimeSnapshotView `json:"retiring"`
	DesiredStateRevision            int64                        `json:"desiredStateRevision"`
	RuntimeGeneration               int64                        `json:"runtimeGeneration"`
	CredentialVersion               int64                        `json:"credentialVersion"`
	RuntimeVersion                  int64                        `json:"runtimeVersion"`
	Readiness                       httpapi.ProjectionReadiness  `json:"readiness"`
	WriteReadiness                  httpapi.ProjectionReadiness  `json:"writeReadiness"`
	LastAppliedDesiredStateRevision int64                        `json:"lastAppliedDesiredStateRevision"`
	LastAppliedRuntimeGeneration    int64                        `json:"lastAppliedRuntimeGeneration"`
	BlockedReason                   *string                      `json:"blockedReason"`
	Capabilities                    []gatewayRuntimeCapability   `json:"capabilities"`
	ObservedAt                      time.Time                    `json:"observedAt"`
}

type gatewayRuntimeCapability struct {
	RowID      string                      `json:"rowId"`
	Venue      string                      `json:"venue"`
	Product    string                      `json:"product"`
	Settlement string                      `json:"settlement,omitempty"`
	WriteReady bool                        `json:"writeReady"`
	Operations gatewayCapabilityOperations `json:"operations"`
}

type gatewayDesiredRoute struct {
	RouteID         string `json:"routeId"`
	Product         string `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
	RouteProfileID  string `json:"routeProfileId"`
	RequestedState  string `json:"requestedState"`
}

type gatewayDesiredApplyRequest struct {
	OperationID          string                `json:"operationId"`
	AccountID            string                `json:"accountId"`
	Venue                string                `json:"venue"`
	NativeEnvironment    string                `json:"nativeEnvironment"`
	SecretRef            string                `json:"secretRef"`
	DesiredStateRevision int64                 `json:"desiredStateRevision"`
	RuntimeGeneration    int64                 `json:"runtimeGeneration"`
	CredentialVersion    int64                 `json:"credentialVersion"`
	RequestedState       string                `json:"requestedState"`
	Routes               []gatewayDesiredRoute `json:"routes"`
}

type gatewayRetireRequest struct {
	OperationID          string `json:"operationId"`
	AccountID            string `json:"accountId"`
	DesiredStateRevision int64  `json:"desiredStateRevision"`
	RuntimeGeneration    int64  `json:"runtimeGeneration"`
	CredentialVersion    int64  `json:"credentialVersion"`
}

type gatewayAccountFence struct {
	AccountID            string `json:"accountId"`
	DesiredStateRevision int64  `json:"desiredStateRevision"`
	RuntimeGeneration    int64  `json:"runtimeGeneration"`
	CredentialVersion    int64  `json:"credentialVersion"`
}

type gatewayRotationCleanupRequest struct {
	OperationID           string              `json:"operationId"`
	AccountID             string              `json:"accountId"`
	ExpectedActiveFence   gatewayAccountFence `json:"expectedActiveFence"`
	ExpectedRetiringFence gatewayAccountFence `json:"expectedRetiringFence"`
}

type gatewayRotationCleanupProof struct {
	OperationID          string              `json:"operationId"`
	AccountID            string              `json:"accountId"`
	ActiveFence          gatewayAccountFence `json:"activeFence"`
	RetiringFence        gatewayAccountFence `json:"retiringFence"`
	TombstoneRevision    int64               `json:"tombstoneRevision"`
	RetiringState        string              `json:"retiringState"`
	SecretReferenceState string              `json:"secretReferenceState"`
	CompletedAt          time.Time           `json:"completedAt"`
}

type gatewayRouteRuntimeView struct {
	RouteID         string `json:"routeId"`
	Product         string `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
	RouteProfileID  string `json:"routeProfileId"`
	RequestedState  string `json:"requestedState"`
	Ready           bool   `json:"ready"`
	Reason          string `json:"reason,omitempty"`
}

type gatewayRuntimeSnapshotView struct {
	OperationID          string                    `json:"operationId"`
	DesiredStateRevision int64                     `json:"desiredStateRevision"`
	RuntimeGeneration    int64                     `json:"runtimeGeneration"`
	CredentialVersion    int64                     `json:"credentialVersion"`
	RequestedState       string                    `json:"requestedState"`
	Routes               []gatewayRouteRuntimeView `json:"routes"`
}

type gatewayPendingSnapshotView struct {
	gatewayRuntimeSnapshotView
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type gatewayDesiredRuntimeView struct {
	AccountID      string                       `json:"accountId"`
	RequestedState string                       `json:"requestedState,omitempty"`
	Routes         []gatewayDesiredRoute        `json:"routes,omitempty"`
	Active         *gatewayRuntimeSnapshotView  `json:"active,omitempty"`
	Pending        *gatewayPendingSnapshotView  `json:"pending,omitempty"`
	Retiring       []gatewayRuntimeSnapshotView `json:"retiring,omitempty"`
	Tombstoned     *bool                        `json:"tombstoned"`
}

type gatewayAuthorityError struct {
	Code             string
	Retryable        bool
	MayHaveCommitted bool
	AccountID        string
	RouteID          string
	OperationID      string
}

func (err gatewayAuthorityError) ResultMayHaveCommitted() bool { return err.MayHaveCommitted }

func (err gatewayAuthorityError) Error() string {
	parts := []string{"Gateway authority rejected request", "code=" + err.Code, "account=" + err.AccountID}
	if err.RouteID != "" {
		parts = append(parts, "route="+err.RouteID)
	}
	if err.OperationID != "" {
		parts = append(parts, "operation="+err.OperationID)
	}
	return strings.Join(parts, " ")
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

func (adapter *gatewayAdapter) ApplyDesired(ctx context.Context, apply accounts.DesiredApply) (accounts.GatewayRuntimeTruth, error) {
	operationID, err := secretstore.ParseOperationID(string(apply.OperationID))
	if err != nil || apply.Account.Mode != secretstore.Mode(adapter.mode) || apply.Account.Validate() != nil || !validGatewayRequestedState(string(apply.Account.State)) || len(apply.Account.Routes) > 32 {
		return accounts.GatewayRuntimeTruth{}, fmt.Errorf("Gateway desired request is invalid")
	}
	ref, err := secretstore.ParseOpaqueRef(apply.Account.SecretRef)
	if err != nil {
		return accounts.GatewayRuntimeTruth{}, fmt.Errorf("Gateway desired request is invalid")
	}
	routes := make([]gatewayDesiredRoute, 0, len(apply.Account.Routes))
	for _, route := range apply.Account.Routes {
		routes = append(routes, gatewayDesiredRoute{
			RouteID: route.ID, Product: string(route.Product), SettlementAsset: route.SettlementAsset,
			RouteProfileID: route.RouteProfileID, RequestedState: string(route.RequestedState),
		})
	}
	request := gatewayDesiredApplyRequest{
		OperationID: string(operationID), AccountID: apply.Account.ID,
		Venue: apply.Account.Venue, NativeEnvironment: string(apply.Account.NativeEnvironment),
		SecretRef: ref.RawForTrustedBoundary(), DesiredStateRevision: apply.Account.DesiredStateRevision,
		RuntimeGeneration: apply.Account.RuntimeGeneration, CredentialVersion: apply.Account.CredentialVersion,
		RequestedState: string(apply.Account.State), Routes: routes,
	}
	var response gatewayReadEnvelope[gatewayDesiredRuntimeView]
	routeID := ""
	if len(routes) == 1 {
		routeID = routes[0].RouteID
	}
	path := "/internal/v1/accounts/desired/" + url.PathEscape(apply.Account.ID) + "/operations/" + url.PathEscape(request.OperationID)
	if _, err := adapter.desiredRequest(ctx, http.MethodPut, path, request.OperationID, apply.Account.ID, routeID, request, &response, http.StatusOK, http.StatusAccepted); err != nil {
		return accounts.GatewayRuntimeTruth{}, err
	}
	return gatewayTruth(response.Data, apply.Account.ID)
}

func (adapter *gatewayAdapter) GetRuntimeTruth(ctx context.Context, accountID string) (accounts.GatewayRuntimeTruth, error) {
	if strings.TrimSpace(accountID) == "" || accountID != strings.TrimSpace(accountID) {
		return accounts.GatewayRuntimeTruth{}, fmt.Errorf("Gateway runtime truth request is invalid")
	}
	var response gatewayReadEnvelope[gatewayDesiredRuntimeView]
	path := "/internal/v1/accounts/desired/" + url.PathEscape(accountID)
	if _, err := adapter.desiredRequest(ctx, http.MethodGet, path, uuid.NewString(), accountID, "", nil, &response, http.StatusOK); err != nil {
		return accounts.GatewayRuntimeTruth{}, err
	}
	return gatewayTruth(response.Data, accountID)
}

func (adapter *gatewayAdapter) RetireAccount(ctx context.Context, retire accounts.RetireRequest) (accounts.GatewayRetirement, error) {
	operationID, err := secretstore.ParseOperationID(string(retire.OperationID))
	if err != nil || strings.TrimSpace(retire.AccountID) == "" || retire.AccountID != strings.TrimSpace(retire.AccountID) || retire.DesiredStateRevision < 1 || retire.RuntimeGeneration < 1 || retire.CredentialVersion < 1 {
		return accounts.GatewayRetirement{}, fmt.Errorf("Gateway retirement request is invalid")
	}
	request := gatewayRetireRequest{
		OperationID: string(operationID), AccountID: retire.AccountID,
		DesiredStateRevision: retire.DesiredStateRevision, RuntimeGeneration: retire.RuntimeGeneration,
		CredentialVersion: retire.CredentialVersion,
	}
	var response gatewayReadEnvelope[gatewayDesiredRuntimeView]
	path := "/internal/v1/accounts/desired/" + url.PathEscape(retire.AccountID) + "/retirements/" + url.PathEscape(request.OperationID)
	if _, err := adapter.desiredRequest(ctx, http.MethodPut, path, request.OperationID, retire.AccountID, "", request, &response, http.StatusOK, http.StatusAccepted); err != nil {
		return accounts.GatewayRetirement{}, err
	}
	truth, err := gatewayTruth(response.Data, retire.AccountID)
	if err != nil {
		return accounts.GatewayRetirement{}, err
	}
	return accounts.GatewayRetirement{Tombstoned: truth.Tombstoned && !truth.Active}, nil
}

func (adapter *gatewayAdapter) FinalizeRotation(ctx context.Context, cleanup accounts.RotationCleanupRequest) (accounts.RotationCleanupProof, error) {
	if cleanup.Validate() != nil {
		return accounts.RotationCleanupProof{}, fmt.Errorf("Gateway rotation cleanup request is invalid")
	}
	request := gatewayRotationCleanupRequest{
		OperationID: string(cleanup.OperationID), AccountID: cleanup.AccountID,
		ExpectedActiveFence:   gatewayFence(cleanup.ExpectedActiveFence),
		ExpectedRetiringFence: gatewayFence(cleanup.ExpectedRetiringFence),
	}
	var response gatewayReadEnvelope[gatewayRotationCleanupProof]
	path := "/internal/v1/accounts/desired/" + url.PathEscape(cleanup.AccountID) + "/rotation-finalizations/" + url.PathEscape(request.OperationID)
	if _, err := adapter.desiredRequest(ctx, http.MethodPut, path, request.OperationID, cleanup.AccountID, "", request, &response, http.StatusOK); err != nil {
		return accounts.RotationCleanupProof{}, err
	}
	proof := accounts.RotationCleanupProof{
		OperationID: secretstore.OperationID(response.Data.OperationID), AccountID: response.Data.AccountID,
		ActiveFence:          platformFence(response.Data.ActiveFence),
		RetiringFence:        platformFence(response.Data.RetiringFence),
		TombstoneRevision:    response.Data.TombstoneRevision,
		RetiringState:        response.Data.RetiringState,
		SecretReferenceState: response.Data.SecretReferenceState,
		CompletedAt:          response.Data.CompletedAt.UTC(),
	}
	if proof.ValidateFor(cleanup) != nil {
		return accounts.RotationCleanupProof{}, gatewayAuthorityError{
			Code: "GATEWAY_AUTHORITY_RESPONSE_INVALID", Retryable: true, MayHaveCommitted: true,
			AccountID: cleanup.AccountID, OperationID: string(cleanup.OperationID),
		}
	}
	return proof, nil
}

func gatewayFence(fence accounts.AccountFence) gatewayAccountFence {
	return gatewayAccountFence{
		AccountID: fence.AccountID, DesiredStateRevision: fence.DesiredStateRevision,
		RuntimeGeneration: fence.RuntimeGeneration, CredentialVersion: fence.CredentialVersion,
	}
}

func platformFence(fence gatewayAccountFence) accounts.AccountFence {
	return accounts.AccountFence{
		AccountID: fence.AccountID, DesiredStateRevision: fence.DesiredStateRevision,
		RuntimeGeneration: fence.RuntimeGeneration, CredentialVersion: fence.CredentialVersion,
	}
}

func (adapter *gatewayAdapter) desiredRequest(ctx context.Context, method, path, requestID, accountID, routeID string, payload, target any, statuses ...int) (int, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, fmt.Errorf("encode Gateway authority request")
		}
		defer clear(encoded)
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, adapter.baseURL+path, body)
	if err != nil {
		return 0, fmt.Errorf("construct Gateway authority request")
	}
	request.Header.Set("Authorization", "Bearer "+string(adapter.token))
	request.Header.Set("X-Request-ID", requestID)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	adapter.attest(request)
	response, err := adapter.client.Do(request)
	if err != nil {
		return 0, gatewayAuthorityError{Code: "GATEWAY_AUTHORITY_UNAVAILABLE", Retryable: true, MayHaveCommitted: true, AccountID: accountID, RouteID: routeID, OperationID: requestID}
	}
	defer response.Body.Close()
	if err := adapter.verifyAttestation(response); err != nil {
		acceptedWrite := method != http.MethodGet && containsStatus(statuses, response.StatusCode)
		return 0, gatewayAuthorityError{Code: "GATEWAY_MODE_MISMATCH", MayHaveCommitted: acceptedWrite || response.StatusCode >= 500, AccountID: accountID, RouteID: routeID, OperationID: requestID}
	}
	accepted := containsStatus(statuses, response.StatusCode)
	if !accepted {
		if !isJSONContentType(response.Header.Get("Content-Type")) {
			return response.StatusCode, gatewayRejectedError(accountID, routeID, requestID, response.StatusCode >= 500)
		}
		return response.StatusCode, decodeGatewayAuthorityError(response.Body, accountID, routeID, requestID, response.StatusCode >= 500)
	}
	if !isJSONContentType(response.Header.Get("Content-Type")) {
		return response.StatusCode, gatewayAuthorityError{Code: "GATEWAY_AUTHORITY_RESPONSE_INVALID", Retryable: true, MayHaveCommitted: true, AccountID: accountID, RouteID: routeID, OperationID: requestID}
	}
	if err := decodeGatewayAuthorityJSON(response.Body, target); err != nil {
		return response.StatusCode, gatewayAuthorityError{Code: "GATEWAY_AUTHORITY_RESPONSE_INVALID", Retryable: true, MayHaveCommitted: true, AccountID: accountID, RouteID: routeID, OperationID: requestID}
	}
	return response.StatusCode, nil
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func containsStatus(statuses []int, target int) bool {
	for _, status := range statuses {
		if status == target {
			return true
		}
	}
	return false
}

func decodeGatewayAuthorityError(body io.Reader, accountID, routeID, operationID string, retryable bool) error {
	var envelope struct {
		Error struct {
			Code      string          `json:"code"`
			Message   string          `json:"message"`
			RequestID string          `json:"requestId"`
			Retryable bool            `json:"retryable"`
			Details   json.RawMessage `json:"details,omitempty"`
		} `json:"error"`
	}
	if err := decodeGatewayAuthorityJSON(body, &envelope); err != nil || !validGatewayErrorCode(envelope.Error.Code) {
		return gatewayRejectedError(accountID, routeID, operationID, retryable)
	}
	return gatewayAuthorityError{Code: envelope.Error.Code, Retryable: envelope.Error.Retryable, MayHaveCommitted: retryable, AccountID: accountID, RouteID: routeID, OperationID: operationID}
}

func decodeGatewayAuthorityJSON(body io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(body, gatewayAuthorityResponseLimit+1))
	if err != nil || len(raw) == 0 || len(raw) > gatewayAuthorityResponseLimit || !utf8.Valid(raw) {
		return fmt.Errorf("invalid Gateway authority response")
	}
	if err := rejectDuplicateGatewayAuthorityKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid Gateway authority response")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("Gateway authority response must contain one JSON value")
	}
	return nil
}

func rejectDuplicateGatewayAuthorityKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanGatewayAuthorityJSONValue(decoder); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("Gateway authority response must contain one JSON value")
	}
	return nil
}

func scanGatewayAuthorityJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid Gateway authority response")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("invalid Gateway authority response")
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("invalid Gateway authority response")
			}
			if _, exists := keys[key]; exists {
				return fmt.Errorf("Gateway authority response contains duplicate key")
			}
			keys[key] = struct{}{}
			if err := scanGatewayAuthorityJSONValue(decoder); err != nil {
				return err
			}
		}
		if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
			return fmt.Errorf("invalid Gateway authority response")
		}
	case '[':
		for decoder.More() {
			if err := scanGatewayAuthorityJSONValue(decoder); err != nil {
				return err
			}
		}
		if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
			return fmt.Errorf("invalid Gateway authority response")
		}
	default:
		return fmt.Errorf("invalid Gateway authority response")
	}
	return nil
}

func gatewayRejectedError(accountID, routeID, operationID string, retryable bool) error {
	return gatewayAuthorityError{Code: "GATEWAY_AUTHORITY_REJECTED", Retryable: retryable, MayHaveCommitted: retryable, AccountID: accountID, RouteID: routeID, OperationID: operationID}
}

func validGatewayErrorCode(code string) bool {
	if code == "" || len(code) > 128 {
		return false
	}
	for _, char := range code {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

func validGatewayRequestedState(state string) bool {
	switch state {
	case "creating", "active", "rotating", "draining", "drained", "disabled", "removing", "removed":
		return true
	default:
		return false
	}
}

func gatewayTruth(view gatewayDesiredRuntimeView, accountID string) (accounts.GatewayRuntimeTruth, error) {
	if view.AccountID != accountID || view.Tombstoned == nil {
		return accounts.GatewayRuntimeTruth{}, fmt.Errorf("Gateway runtime truth identity is invalid")
	}
	if *view.Tombstoned && (view.Active != nil || view.Pending != nil || len(view.Retiring) != 0) {
		return accounts.GatewayRuntimeTruth{}, fmt.Errorf("Gateway runtime truth is contradictory")
	}
	truth := accounts.GatewayRuntimeTruth{AccountID: accountID, Tombstoned: *view.Tombstoned}
	if view.Active == nil {
		return truth, nil
	}
	operationID, err := secretstore.ParseOperationID(view.Active.OperationID)
	if err != nil || view.Active.DesiredStateRevision < 1 || view.Active.RuntimeGeneration < 1 || view.Active.CredentialVersion < 1 || !validGatewayRequestedState(view.Active.RequestedState) {
		return accounts.GatewayRuntimeTruth{}, fmt.Errorf("Gateway runtime truth fence is invalid")
	}
	ready := len(view.Active.Routes) > 0
	routes := make([]accounts.GatewayRuntimeRoute, 0, len(view.Active.Routes))
	for _, route := range view.Active.Routes {
		if route.RouteID == "" || route.RouteProfileID == "" || (route.RequestedState == "active" && !route.Ready) {
			ready = false
		}
		routes = append(routes, accounts.GatewayRuntimeRoute{
			RouteID: route.RouteID, Product: accounts.Product(route.Product), SettlementAsset: route.SettlementAsset,
			RouteProfileID: route.RouteProfileID, RequestedState: accounts.RouteState(route.RequestedState), Ready: route.Ready,
		})
	}
	truth.OperationID = operationID
	truth.DesiredStateRevision = view.Active.DesiredStateRevision
	truth.RuntimeGeneration = view.Active.RuntimeGeneration
	truth.CredentialVersion = view.Active.CredentialVersion
	truth.RequestedState = accounts.State(view.Active.RequestedState)
	truth.Routes = routes
	truth.Active = ready
	return truth, nil
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
	active, err := projectRuntimeSnapshot(projection.Active)
	if err != nil {
		return nil, err
	}
	var pending *httpapi.PendingRuntimeSnapshotProjection
	if projection.Pending != nil {
		projected, projectErr := projectRuntimeSnapshot(&projection.Pending.gatewayRuntimeSnapshotView)
		if projectErr != nil || !validPendingRuntimeState(projection.Pending.State) {
			return nil, fmt.Errorf("Gateway pending runtime response is invalid")
		}
		pending = &httpapi.PendingRuntimeSnapshotProjection{RuntimeSnapshotProjection: *projected, State: projection.Pending.State, Reason: projection.Pending.Reason}
	}
	retiring := make([]httpapi.RuntimeSnapshotProjection, 0, len(projection.Retiring))
	for index := range projection.Retiring {
		projected, projectErr := projectRuntimeSnapshot(&projection.Retiring[index])
		if projectErr != nil {
			return nil, projectErr
		}
		retiring = append(retiring, *projected)
	}
	return []httpapi.AccountRuntimeProjection{{
		AccountID: accountID, Active: active, Pending: pending, Retiring: retiring, DesiredStateRevision: projection.DesiredStateRevision, RuntimeGeneration: projection.RuntimeGeneration,
		CredentialVersion: projection.CredentialVersion, RuntimeVersion: projection.RuntimeVersion, Readiness: projection.Readiness, WriteReadiness: projection.WriteReadiness,
		LastAppliedDesiredStateRevision: projection.LastAppliedDesiredStateRevision, LastAppliedRuntimeGeneration: projection.LastAppliedRuntimeGeneration,
		BlockedReason: projection.BlockedReason, Capabilities: projectedCapabilities, ObservedAt: projection.ObservedAt.UTC(),
	}}, nil
}

func projectRuntimeSnapshot(snapshot *gatewayRuntimeSnapshotView) (*httpapi.RuntimeSnapshotProjection, error) {
	if snapshot == nil {
		return nil, nil
	}
	if _, err := secretstore.ParseOperationID(snapshot.OperationID); err != nil || snapshot.DesiredStateRevision < 1 || snapshot.RuntimeGeneration < 1 || snapshot.CredentialVersion < 1 || !validGatewayRequestedState(snapshot.RequestedState) {
		return nil, fmt.Errorf("Gateway runtime snapshot response is invalid")
	}
	routes := make([]httpapi.RouteRuntimeProjection, 0, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		if strings.TrimSpace(route.RouteID) == "" || strings.TrimSpace(route.Product) == "" || strings.TrimSpace(route.RouteProfileID) == "" || (route.RequestedState != "active" && route.RequestedState != "disabled") {
			return nil, fmt.Errorf("Gateway runtime route response is invalid")
		}
		routes = append(routes, httpapi.RouteRuntimeProjection{RouteID: route.RouteID, Product: route.Product, SettlementAsset: route.SettlementAsset, RouteProfileID: route.RouteProfileID, RequestedState: route.RequestedState, Ready: route.Ready, Reason: route.Reason})
	}
	return &httpapi.RuntimeSnapshotProjection{DesiredStateRevision: snapshot.DesiredStateRevision, RuntimeGeneration: snapshot.RuntimeGeneration, CredentialVersion: snapshot.CredentialVersion, RequestedState: snapshot.RequestedState, Routes: routes}, nil
}

func validPendingRuntimeState(state string) bool {
	switch state {
	case "prepared", "candidate_ready", "candidate_failed", "applied":
		return true
	default:
		return false
	}
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
		Revision    string               `json:"revision"`
		ObservedAt  time.Time            `json:"observedAt"`
		Instruments []catalog.Instrument `json:"instruments"`
	}]
	if err := adapter.read(ctx, "/internal/v1/catalog/instruments", nil, &result); err != nil {
		return fmt.Errorf("probe Gateway catalog authority: %w", err)
	}
	readyInstrument := false
	for _, instrument := range result.Data.Instruments {
		if instrument.Ready {
			readyInstrument = true
			break
		}
	}
	if result.Data.Revision == "" || result.Data.ObservedAt.IsZero() || !readyInstrument {
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
