package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
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
	preview FundingExecutionPreview
	close   FundingExecutionPreview
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
func (fixture fundingProjectionFixture) CreateCloseAuthority(context.Context, string, string, string) (FundingExecutionPreview, error) {
	if len(fixture.close.Facts) != 0 {
		return fixture.close, nil
	}
	return fixture.preview, nil
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
			WorkItems []work.WorkItem `json:"workItems"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lineageResponse.Body.Bytes(), &lineageEnvelope); err != nil {
		t.Fatal(err)
	}
	if len(lineageEnvelope.Data.WorkItems) != 2 {
		t.Fatalf("lineage work items=%+v", lineageEnvelope.Data.WorkItems)
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
