package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
)

func fundingJSONBody(t *testing.T, value any) *bytes.Reader {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(encoded)
}

type fundingProjectionFixture struct {
	preview                  FundingExecutionPreview
	close                    FundingExecutionPreview
	closeRequirements        []FundingReservationRequirement
	closeRequirementsFailure error
}

func (fixture fundingProjectionFixture) ListOpportunities(context.Context, string, string) ([]FundingOpportunity, error) {
	return []FundingOpportunity{{OpportunityID: "opp-a", CanonicalMarketID: "BTC-USDT-PERP"}}, nil
}
func (fixture fundingProjectionFixture) GetOpportunity(context.Context, string, string, string) (FundingOpportunity, error) {
	return FundingOpportunity{OpportunityID: "opp-a", CanonicalMarketID: "BTC-USDT-PERP"}, nil
}
func (fixture fundingProjectionFixture) CreatePreview(context.Context, string, FundingPreviewRequest) (FundingExecutionPreview, error) {
	return fixture.preview, nil
}
func (fixture fundingProjectionFixture) GetPreview(context.Context, string, string) (FundingExecutionPreview, error) {
	return fixture.preview, nil
}
func (fixture fundingProjectionFixture) CreateCloseAuthority(context.Context, string, string, string, string) (FundingExecutionPreview, error) {
	if len(fixture.close.Facts) != 0 {
		return fixture.close, nil
	}
	return fixture.preview, nil
}
func (fixture fundingProjectionFixture) GetCloseReservationRequirements(context.Context, string, string, string) ([]FundingReservationRequirement, error) {
	if fixture.closeRequirementsFailure != nil {
		return nil, fixture.closeRequirementsFailure
	}
	if fixture.closeRequirements != nil {
		return fixture.closeRequirements, nil
	}
	return []FundingReservationRequirement{
		{AccountID: "long", ProductID: "funding", ReservationKind: "close", NettingScope: reservations.NettingScope{AccountID: "long", Venue: "venue-a", InstrumentID: "btc-a", VenueEnvironment: "testnet"}, Quantity: "0.5", Notional: "50", Currency: "USDT", Mode: "test"},
		{AccountID: "short", ProductID: "funding", ReservationKind: "close", NettingScope: reservations.NettingScope{AccountID: "short", Venue: "venue-b", InstrumentID: "btc-b", VenueEnvironment: "testnet"}, Quantity: "0.5", Notional: "50", Currency: "USDT", Mode: "test"},
	}, nil
}

func TestFundingProductRoutesEnqueueDurableOpenWork(t *testing.T) {
	now := time.Date(2026, 8, 6, 2, 30, 0, 0, time.UTC)
	workStore := work.NewMemoryStore()
	workService := work.NewWithStore("contract-v1", time.Minute, workStore)
	workService.SetClock(func() time.Time { return now })
	executions := work.NewMemoryFundingExecutionStore()
	if _, _, err := executions.ReserveFundingExecution(context.Background(), work.FundingExecution{ExecutionID: "execution-browser-a", PrincipalID: "browser-a", Mode: orders.ModeTest, LineageID: "lineage-a", ParentCommandID: "parent-a", PlanKey: "open:preview-a", PreviewHash: "preview-hash-a", IntentKind: "funding.open", State: work.FundingExecutionHedged, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	projection := fundingProjectionFixture{preview: FundingExecutionPreview{
		PreviewID: "preview-a", OpportunityID: "opp-a", CanSubmit: true, Mode: "test",
		Facts:         []work.Fact{{FactID: "fact-preview-a", Cursor: "cursor-preview-a", ProductID: "funding", Mode: "test", Kind: "funding.execution-input", Payload: json.RawMessage(`{"previewId":"preview-a"}`), EffectiveAt: now}},
		AccountFences: json.RawMessage(`[{"accountId":"long","desiredStateRevision":4,"runtimeGeneration":2,"credentialVersion":3},{"accountId":"short","desiredStateRevision":5,"runtimeGeneration":3,"credentialVersion":4}]`),
		WorkPayload:   json.RawMessage(`{"opportunityId":"opp-a","previewId":"preview-a","longAccountId":"long","shortAccountId":"short","canonicalMarketId":"BTC-USDT-PERP","notionalPerLeg":"50","maxSlippageBps":"10","holdThroughSettlement":true}`),
	}, close: FundingExecutionPreview{PreviewID: "preview-a", CanSubmit: true, Mode: "test",
		Facts:         []work.Fact{{FactID: "fact-close-long", Cursor: "cursor-close-a", AccountID: "long", ProductID: "funding", Mode: "test", Kind: "funding.unwind-reservation", Payload: json.RawMessage(`{"reservationId":"close-long"}`), EffectiveAt: now}, {FactID: "fact-close-short", Cursor: "cursor-close-a", AccountID: "short", ProductID: "funding", Mode: "test", Kind: "funding.unwind-reservation", Payload: json.RawMessage(`{"reservationId":"close-short"}`), EffectiveAt: now}},
		AccountFences: json.RawMessage(`[{"accountId":"long","desiredStateRevision":4,"runtimeGeneration":2,"credentialVersion":3},{"accountId":"short","desiredStateRevision":5,"runtimeGeneration":3,"credentialVersion":4}]`),
		WorkPayload:   json.RawMessage(`{"lineageId":"lineage-a","reason":"browser_request","requestedAt":"2026-08-06T02:30:00Z"}`),
	}}
	mux := http.NewServeMux()
	err := RegisterFundingRoutes(mux, FundingRoutesOptions{
		Projection: projection, Work: workService, Executions: executions,
		Principal:         func(*http.Request) (string, string, error) { return "browser-a", "test", nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	previewRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/funding/execution-previews", fundingJSONBody(t, map[string]any{"opportunityId": "opp-a", "longAccountId": "long", "shortAccountId": "short", "canonicalMarketId": "BTC-USDT-PERP", "notionalPerLeg": "50", "maxSlippageBps": 10, "holdThroughSettlement": true, "mode": "test"}))
	previewResponse := httptest.NewRecorder()
	mux.ServeHTTP(previewResponse, previewRequest)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}

	commandRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/funding/commands", fundingJSONBody(t, map[string]any{"previewId": "preview-a", "action": "open", "lineageId": "lineage-a", "idempotencyKey": "open-a"}))
	commandResponse := httptest.NewRecorder()
	mux.ServeHTTP(commandResponse, commandRequest)
	if commandResponse.Code != http.StatusAccepted {
		t.Fatalf("command status=%d body=%s", commandResponse.Code, commandResponse.Body.String())
	}
	var commandEnvelope struct {
		Data work.WorkItem `json:"data"`
	}
	if err := json.Unmarshal(commandResponse.Body.Bytes(), &commandEnvelope); err != nil {
		t.Fatal(err)
	}
	if commandEnvelope.Data.Status != "pending" {
		t.Fatalf("public command status=%q, want pending", commandEnvelope.Data.Status)
	}
	items, err := workService.ListOwnedWork(context.Background(), "browser-a", "test", "lineage-a")
	if err != nil || len(items) != 1 || items[0].WorkKind != "open" || string(items[0].Payload) != string(projection.preview.WorkPayload) {
		t.Fatalf("durable work=%+v err=%v", items, err)
	}
	closeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/funding/commands", fundingJSONBody(t, map[string]any{"previewId": "preview-a", "action": "close", "lineageId": "lineage-a", "idempotencyKey": "close-a"}))
	closeResponse := httptest.NewRecorder()
	mux.ServeHTTP(closeResponse, closeRequest)
	if closeResponse.Code != http.StatusAccepted {
		t.Fatalf("close status=%d body=%s", closeResponse.Code, closeResponse.Body.String())
	}
	items, err = workService.ListOwnedWork(context.Background(), "browser-a", "test", "lineage-a")
	var closeItem work.WorkItem
	for _, item := range items {
		if item.WorkKind == "close" {
			closeItem = item
		}
	}
	if err != nil || len(items) != 2 || closeItem.WorkItemID == "" || string(closeItem.Payload) != string(projection.close.WorkPayload) || len(projection.close.Facts) != 2 {
		t.Fatalf("close durable work=%+v err=%v", items, err)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/v1/products/funding/commands/"+items[0].WorkItemID, nil)
	getResponse := httptest.NewRecorder()
	mux.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
	var getEnvelope struct {
		Data work.WorkItem `json:"data"`
	}
	if err := json.Unmarshal(getResponse.Body.Bytes(), &getEnvelope); err != nil {
		t.Fatal(err)
	}
	if getEnvelope.Data.Status != "pending" {
		t.Fatalf("public get status=%q, want pending", getEnvelope.Data.Status)
	}
	if bytes.Contains(getResponse.Body.Bytes(), []byte(`"state":"open"`)) || !bytes.Contains(getResponse.Body.Bytes(), []byte(`"state":"pending"`)) {
		t.Fatalf("browser command status was not mapped to pending: %s", getResponse.Body.String())
	}
	manualItem, _, err := workStore.EnqueueWork(context.Background(), work.EnqueueRequest{PrincipalID: "browser-a", IdempotencyKey: "manual-a", WorkKind: "open", LineageID: "lineage-a", ProductID: "manual", Mode: "test", FactsCursor: "manual-cursor", Facts: []work.Fact{{FactID: "manual-fact", Cursor: "manual-cursor", ProductID: "manual", Mode: "test", Kind: "manual", Payload: json.RawMessage(`{}`), EffectiveAt: now}}, AccountFences: projection.preview.AccountFences, Payload: json.RawMessage(`{"request":"manual"}`)}, now)
	if err != nil {
		t.Fatal(err)
	}
	manualGet := httptest.NewRequest(http.MethodGet, "/api/v1/products/funding/commands/"+manualItem.WorkItemID, nil)
	manualGetResponse := httptest.NewRecorder()
	mux.ServeHTTP(manualGetResponse, manualGet)
	if manualGetResponse.Code != http.StatusNotFound {
		t.Fatalf("non-Funding get status=%d body=%s", manualGetResponse.Code, manualGetResponse.Body.String())
	}
	lineage := httptest.NewRequest(http.MethodGet, "/api/v1/products/funding/lineage/lineage-a", nil)
	lineageResponse := httptest.NewRecorder()
	mux.ServeHTTP(lineageResponse, lineage)
	if lineageResponse.Code != http.StatusOK {
		t.Fatalf("lineage status=%d body=%s", lineageResponse.Code, lineageResponse.Body.String())
	}
	var lineageEnvelope struct {
		Data struct {
			WorkItems                    []work.WorkItem                 `json:"workItems"`
			CloseReservationRequirements []FundingReservationRequirement `json:"closeReservationRequirements"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lineageResponse.Body.Bytes(), &lineageEnvelope); err != nil {
		t.Fatal(err)
	}
	if len(lineageEnvelope.Data.WorkItems) != 2 || len(lineageEnvelope.Data.CloseReservationRequirements) != 2 {
		t.Fatalf("lineage work items=%+v", lineageEnvelope.Data.WorkItems)
	}
	for _, requirement := range lineageEnvelope.Data.CloseReservationRequirements {
		if requirement.AccountID == "" || requirement.AccountID != requirement.NettingScope.AccountID || requirement.ProductID != "funding" || requirement.ReservationKind != "close" || requirement.NettingScope.Venue == "" || requirement.NettingScope.InstrumentID == "" || requirement.NettingScope.VenueEnvironment == "" || requirement.Quantity != "0.5" || requirement.Notional != "50" || requirement.Currency != "USDT" || requirement.Mode != "test" {
			t.Fatalf("close reservation requirement is not a complete reservation body: %+v", requirement)
		}
	}
	for _, item := range lineageEnvelope.Data.WorkItems {
		if item.Status != "pending" {
			t.Fatalf("public lineage status=%q, want pending: %+v", item.Status, item)
		}
	}
	if bytes.Contains(lineageResponse.Body.Bytes(), []byte(`"productId":"manual"`)) {
		t.Fatalf("lineage leaked non-Funding work: %s", lineageResponse.Body.String())
	}
	if !bytes.Contains(lineageResponse.Body.Bytes(), []byte(`"principalId":"browser-a"`)) {
		t.Fatalf("lineage omitted principal-owned execution: %s", lineageResponse.Body.String())
	}
	if bytes.Contains(lineageResponse.Body.Bytes(), []byte(`"state":"open"`)) || !bytes.Contains(lineageResponse.Body.Bytes(), []byte(`"state":"pending"`)) {
		t.Fatalf("browser lineage status was not mapped to pending: %s", lineageResponse.Body.String())
	}
}

func TestFundingOpenPreviewReturnsDirectlyCreatableReservationsBeforeAdmission(t *testing.T) {
	now := time.Date(2026, 8, 8, 13, 0, 0, 0, time.UTC)
	fixture := newFundingOpenHTTPFixture(t, now)
	opportunity := fixture.opportunity(t)
	if bytes.Contains(opportunity.body, []byte(`"quantity"`)) || bytes.Contains(opportunity.body, []byte(`"notional"`)) || bytes.Contains(opportunity.body, []byte(`"currency"`)) {
		t.Fatalf("opportunity advertised non-authoritative reservation amounts: %s", opportunity.body)
	}

	preview := fixture.preview(t, opportunity.item)
	if len(preview.ReservationRequirements) != 2 {
		t.Fatalf("preview requirements=%+v", preview.ReservationRequirements)
	}
	for index, requirement := range preview.ReservationRequirements {
		if requirement.AccountID == "" || requirement.AccountID != requirement.NettingScope.AccountID || requirement.ProductID != "funding" || requirement.ReservationKind != reservations.KindOpen || requirement.NettingScope.Venue == "" || requirement.NettingScope.InstrumentID == "" || requirement.NettingScope.VenueEnvironment == "" || requirement.Quantity == "" || requirement.Notional == "" || requirement.Currency == "" || requirement.Mode != "test" {
			t.Fatalf("requirement[%d] is not a complete reservation body: %+v", index, requirement)
		}
		fixture.reserve(t, requirement, "open-reservation-"+requirement.AccountID)
	}

	response := fixture.command("preview-open", preview.PreviewID)
	if response.Code != http.StatusAccepted {
		t.Fatalf("command status=%d body=%s", response.Code, response.Body.String())
	}
	items, err := fixture.work.ListOwnedWork(context.Background(), "browser-a", "test", "lineage-open")
	if err != nil || len(items) != 1 || items[0].WorkKind != "open" {
		t.Fatalf("durable open work=%+v err=%v", items, err)
	}
}

func TestFundingOpenCommandRejectsInsufficientOrWrongScopeReservations(t *testing.T) {
	tests := map[string]func(*FundingReservationRequirement){
		"insufficient amount": func(requirement *FundingReservationRequirement) {
			requirement.Quantity = "0.001"
			requirement.Notional = "1"
		},
		"wrong scope": func(requirement *FundingReservationRequirement) {
			requirement.NettingScope.InstrumentID += "-wrong"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 8, 8, 13, 0, 0, 0, time.UTC)
			fixture := newFundingOpenHTTPFixture(t, now)
			opportunity := fixture.opportunity(t).item
			preview := fixture.preview(t, opportunity)
			for index, requirement := range preview.ReservationRequirements {
				if index == 0 {
					mutate(&requirement)
				}
				fixture.reserve(t, requirement, fmt.Sprintf("invalid-open-%d", index))
			}
			response := fixture.command("invalid-open-command", preview.PreviewID)
			if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"FUNDING_PREVIEW_STALE"`)) {
				t.Fatalf("command status=%d body=%s", response.Code, response.Body.String())
			}
			items, err := fixture.work.ListOwnedWork(context.Background(), "browser-a", "test", "lineage-open")
			if err != nil || len(items) != 0 {
				t.Fatalf("rejected command persisted work=%+v err=%v", items, err)
			}
		})
	}
}

type fundingOpenHTTPFixture struct {
	mux  *http.ServeMux
	work *work.Service
}

type fundingOpportunityResponse struct {
	item FundingOpportunity
	body []byte
}

func newFundingOpenHTTPFixture(t *testing.T, now time.Time) fundingOpenHTTPFixture {
	t.Helper()
	reservationStore := reservations.NewMemoryStore()
	reservationService := reservations.NewService(reservationStore, func() time.Time { return now })
	accountSource := accountFixture{
		"account-long":  {ID: "account-long", Venue: "venue-a", VenueEnvironment: "testnet", ProductIDs: []string{"funding"}, DesiredStateRevision: 11, RuntimeGeneration: 4, CredentialVersion: 3, State: "active"},
		"account-short": {ID: "account-short", Venue: "venue-b", VenueEnvironment: "testnet", ProductIDs: []string{"funding"}, DesiredStateRevision: 13, RuntimeGeneration: 7, CredentialVersion: 5, State: "active"},
	}
	projection, err := NewSharedFundingProjection(SharedFundingProjectionOptions{Catalog: &catalogFixture{snapshot: fundingCatalog(now)}, MarketData: fundingMarkets(now), Accounts: accountSource, Reservations: reservationStore, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	workService := work.NewWithStore("contract-v1", time.Minute, work.NewMemoryStore())
	workService.SetClock(func() time.Time { return now })
	principal := func(*http.Request) (string, string, error) { return "browser-a", "test", nil }
	authorize := func(*http.Request) error { return nil }
	mux := http.NewServeMux()
	if err := RegisterTradingRoutes(mux, TradingOptions{Reservations: reservationService, Commands: orders.NewService(orders.NewMemoryStore(), nil, func() time.Time { return now }), Portfolio: portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), func() time.Time { return now }), Principal: principal, AuthorizeMutation: authorize}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterFundingRoutes(mux, FundingRoutesOptions{Projection: projection, Work: workService, Principal: principal, AuthorizeMutation: authorize}); err != nil {
		t.Fatal(err)
	}
	return fundingOpenHTTPFixture{mux: mux, work: workService}
}

func (fixture fundingOpenHTTPFixture) opportunity(t *testing.T) fundingOpportunityResponse {
	t.Helper()
	response := httptest.NewRecorder()
	fixture.mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/products/funding/opportunities", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("opportunities status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			Items []FundingOpportunity `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || len(envelope.Data.Items) != 1 {
		t.Fatalf("opportunities body=%s err=%v", response.Body.String(), err)
	}
	return fundingOpportunityResponse{item: envelope.Data.Items[0], body: append([]byte(nil), response.Body.Bytes()...)}
}

func (fixture fundingOpenHTTPFixture) preview(t *testing.T, opportunity FundingOpportunity) FundingExecutionPreview {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/products/funding/execution-previews", fundingJSONBody(t, map[string]any{"opportunityId": opportunity.OpportunityID, "longAccountId": opportunity.LongAccountID, "shortAccountId": opportunity.ShortAccountID, "canonicalMarketId": opportunity.CanonicalMarketID, "notionalPerLeg": "50", "maxSlippageBps": 10, "holdThroughSettlement": true, "mode": "test"}))
	response := httptest.NewRecorder()
	fixture.mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data FundingExecutionPreview `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("preview body=%s err=%v", response.Body.String(), err)
	}
	return envelope.Data
}

func (fixture fundingOpenHTTPFixture) reserve(t *testing.T, requirement FundingReservationRequirement, key string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/reservations", fundingJSONBody(t, requirement))
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	fixture.mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("reservation status=%d body=%s requirement=%+v", response.Code, response.Body.String(), requirement)
	}
}

func (fixture fundingOpenHTTPFixture) command(key, previewID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/products/funding/commands", fundingJSONBodyNoTest(map[string]any{"previewId": previewID, "action": "open", "lineageId": "lineage-open", "idempotencyKey": key}))
	response := httptest.NewRecorder()
	fixture.mux.ServeHTTP(response, request)
	return response
}

func fundingJSONBodyNoTest(value any) *bytes.Reader {
	encoded, _ := json.Marshal(value)
	return bytes.NewReader(encoded)
}

func TestFundingLineageReturnsConflictWithBlockerWhenCloseRequirementsAreUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	service := work.NewWithStore("contract-v1", time.Minute, work.NewMemoryStore())
	if _, _, err := service.Enqueue(context.Background(), work.EnqueueRequest{PrincipalID: "browser-a", IdempotencyKey: "open-a", WorkKind: "open", LineageID: "lineage-a", ProductID: "funding", Mode: "test", FactsCursor: "cursor-a", Facts: []work.Fact{{FactID: "fact-a", Cursor: "cursor-a", ProductID: "funding", Mode: "test", Kind: "funding.execution-input", Payload: json.RawMessage(`{}`), EffectiveAt: now}}, AccountFences: json.RawMessage(`[{"accountId":"long","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"short","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`), Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterFundingRoutes(mux, FundingRoutesOptions{Projection: fundingProjectionFixture{closeRequirementsFailure: context.Canceled}, Work: service, Principal: func(*http.Request) (string, string, error) { return "browser-a", "test", nil }, AuthorizeMutation: func(*http.Request) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/products/funding/lineage/lineage-a", nil))
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"FUNDING_CLOSE_NOT_READY"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"blockers":["CURRENT_CLOSE_AUTHORITY_UNAVAILABLE"]`)) || bytes.Contains(response.Body.Bytes(), []byte(`"closeReservationRequirements":[]`)) {
		t.Fatalf("lineage close conflict=%d body=%s", response.Code, response.Body.String())
	}
}

func TestFundingProductMutationsRequireAuthorizationAndTestMode(t *testing.T) {
	mux := http.NewServeMux()
	service := work.NewWithStore("contract-v1", time.Minute, work.NewMemoryStore())
	err := RegisterFundingRoutes(mux, FundingRoutesOptions{Projection: fundingProjectionFixture{}, Work: service, Principal: func(*http.Request) (string, string, error) { return "browser-a", "live", nil }, AuthorizeMutation: func(*http.Request) error { return context.Canceled }})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/products/funding/commands", fundingJSONBody(t, map[string]any{"previewId": "preview-a", "action": "open", "lineageId": "lineage-a", "idempotencyKey": "open-a"}))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestFundingProductCommandRequiresAllFields(t *testing.T) {
	mux := http.NewServeMux()
	service := work.NewWithStore("contract-v1", time.Minute, work.NewMemoryStore())
	err := RegisterFundingRoutes(mux, FundingRoutesOptions{Projection: fundingProjectionFixture{}, Work: service, Principal: func(*http.Request) (string, string, error) { return "browser-a", "test", nil }, AuthorizeMutation: func(*http.Request) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/products/funding/commands", fundingJSONBody(t, map[string]any{"previewId": "preview-a", "action": "open", "lineageId": "lineage-a"}))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
