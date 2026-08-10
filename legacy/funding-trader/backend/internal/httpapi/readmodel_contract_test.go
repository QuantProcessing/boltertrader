package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/auth"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
)

type contractReadModel struct{}

func (contractReadModel) ListOpportunities(context.Context, readmodel.Scope, readmodel.OpportunityQuery) (readmodel.Page, error) {
	return readmodel.Page{
		Items: []json.RawMessage{json.RawMessage(`{"opportunityId":"opp-1","symbol":"BTC-USDT","netAprPct":12.5}`)},
		AsOf:  time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC),
	}, nil
}

func (contractReadModel) Opportunity(context.Context, readmodel.Scope, string) (json.RawMessage, error) {
	return json.RawMessage(`{"opportunityId":"opp-1","symbol":"BTC-USDT","netAprPct":12.5}`), nil
}

func (contractReadModel) ListAlerts(context.Context, readmodel.Scope, readmodel.AlertQuery) (readmodel.Page, error) {
	return readmodel.Page{
		Items:  []json.RawMessage{json.RawMessage(`{"alertId":"alert-1","severity":"warning","relatedOpportunityId":"opp-1"}`)},
		AsOf:   time.Date(2026, 7, 29, 16, 0, 1, 0, time.UTC),
		Counts: map[string]int{"all": 1, "critical": 0, "warning": 1, "watch": 0},
	}, nil
}

func (contractReadModel) ListPositions(context.Context, readmodel.Scope, readmodel.PositionQuery) (readmodel.Page, error) {
	return readmodel.Page{
		Items: []json.RawMessage{json.RawMessage(`{"positionId":"position-1","symbol":"BTC-USDT","state":"open"}`)},
		AsOf:  time.Date(2026, 7, 29, 16, 0, 3, 0, time.UTC),
	}, nil
}

func (contractReadModel) ListActivities(context.Context, readmodel.Scope, readmodel.ActivityQuery) (readmodel.Page, error) {
	return readmodel.Page{
		Items: []json.RawMessage{json.RawMessage(`{"activityId":"activity-1","kind":"position.opened","state":"open"}`)},
		AsOf:  time.Date(2026, 7, 29, 16, 0, 4, 0, time.UTC),
	}, nil
}

func (contractReadModel) ListAuditEvents(context.Context, readmodel.Scope, readmodel.AuditQuery) (readmodel.Page, error) {
	return readmodel.Page{
		Items: []json.RawMessage{json.RawMessage(`{"auditId":"1","kind":"auth.login","outcome":"success","requestId":"req-login"}`)},
		AsOf:  time.Date(2026, 7, 29, 16, 0, 2, 0, time.UTC),
	}, nil
}

func (contractReadModel) Snapshot(context.Context, readmodel.Scope, []readmodel.EventKind) (readmodel.Snapshot, error) {
	return readmodel.Snapshot{}, nil
}

func TestReadModelResponsesExposeContractFieldsAtTopLevel(t *testing.T) {
	handler, cookie, _ := newReadModelContractServer(t)

	for _, path := range []string{
		"/api/v1/opportunities",
		"/api/v1/funding-rate-alerts",
		"/api/v1/positions",
		"/api/v1/activities",
		"/api/v1/audit-events",
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Data) != 1 {
				t.Fatalf("data=%+v", body.Data)
			}
			if _, wrapped := body.Data[0]["payload"]; wrapped {
				t.Fatalf("contract item is wrapped in payload: %+v", body.Data[0])
			}
		})
	}
}

func TestReadModelCanonicalDetailAndStreamRoutesRequireAuthentication(t *testing.T) {
	handler, _, _ := newReadModelContractServer(t)
	for _, path := range []string{"/api/v1/opportunities/opp-1", "/api/v1/stream"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestOpportunityDetailUsesCanonicalTopLevelPayload(t *testing.T) {
	handler, cookie, _ := newReadModelContractServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/opportunities/opp-1", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data["opportunityId"] != "opp-1" {
		t.Fatalf("body=%s", response.Body.String())
	}
}

func TestOpenPositionDetailFallsBackToVisibleReadModelList(t *testing.T) {
	handler, cookie, _ := newReadModelContractServer(t)

	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/positions", nil)
	listRequest.AddCookie(cookie)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), `"positionId":"position-1"`) {
		t.Fatalf("list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}

	detailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/positions/position-1", nil)
	detailRequest.AddCookie(cookie)
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailResponse.Code, detailResponse.Body.String())
	}
	if !strings.Contains(detailResponse.Body.String(), `"positionId":"position-1"`) ||
		strings.Contains(detailResponse.Body.String(), `"payload"`) {
		t.Fatalf("detail body=%s", detailResponse.Body.String())
	}
}

func TestReadModelAndStreamFiltersFailClosed(t *testing.T) {
	handler, cookie, _ := newReadModelContractServer(t)
	for _, path := range []string{
		"/api/v1/opportunities?sort=unknown",
		"/api/v1/opportunities?limit=101",
		"/api/v1/funding-rate-alerts?severity=emergency",
		"/api/v1/activities?from=not-a-time",
		"/api/v1/stream?kind=resync_required",
		"/api/v1/opportunities?environment=demo",
		"/api/v1/opportunities?mode=live",
		"/api/v1/funding-rate-alerts?environment=testnet",
		"/api/v1/positions?mode=test",
		"/api/v1/activities?environment=demo",
		"/api/v1/audit-events?mode=live",
		"/api/v1/stream?environment=demo",
		"/api/v1/stream?mode=live",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestInvalidStreamCursorEmitsBrowserResnapshotRequired(t *testing.T) {
	handler, cookie, _ := newReadModelContractServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/stream?cursor=tampered", nil)
	request.AddCookie(cookie)
	response := newStreamResponseRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body()
	if !strings.Contains(body, "event: resnapshot.required") ||
		!strings.Contains(body, `"scope":["all"]`) ||
		!strings.Contains(body, `"cursor"`) ||
		strings.Contains(body, "resync_required") {
		t.Fatalf("resnapshot body=%s", body)
	}
}

func TestStreamEventEnvelopeExposesContractVersionFromStreamEvent(t *testing.T) {
	payload, err := streamEventData(uistream.StreamEvent{
		Name:            uistream.EventData,
		Kind:            string(readmodel.OpportunityUpsert),
		Cursor:          "cursor-1",
		PrincipalID:     auth.DefaultPrincipalID,
		Mode:            "test",
		ContractVersion: readmodel.ContractVersion,
		Data:            json.RawMessage(`{"opportunityId":"opp-1","venueEnvironment":"demo"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNoBareEnvironmentField(t, payload)
	if !strings.Contains(string(payload), `"contractVersion":"`+readmodel.ContractVersion+`"`) ||
		!strings.Contains(string(payload), `"principalId":"`+auth.DefaultPrincipalID+`"`) ||
		!strings.Contains(string(payload), `"mode":"test"`) ||
		!strings.Contains(string(payload), `"venueEnvironment":"demo"`) {
		t.Fatalf("bad stream payload: %s", payload)
	}
}

func TestAuthenticatedStreamEmitsSnapshotAndCanonicalDomainEvent(t *testing.T) {
	handler, cookie, stream := newReadModelContractServer(t)
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/stream?kind=opportunity.upsert", nil).WithContext(requestContext)
	request.AddCookie(cookie)
	recorder := newStreamResponseRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, request)
		close(done)
	}()

	recorder.waitForFlush(t)
	if recorder.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type=%q", recorder.Header().Get("Content-Type"))
	}
	if body := recorder.Body(); !strings.Contains(body, "event: snapshot") ||
		!strings.Contains(body, `"opportunities"`) ||
		!strings.Contains(body, `"cursor"`) {
		t.Fatalf("snapshot body=%s", body)
	}
	if _, err := stream.Publish(context.Background(), uistream.EventInput{
		Scope: "all",
		Kind:  "opportunity.upsert",
		Data:  json.RawMessage(`{"opportunityId":"opp-stream","symbol":"ETH-USDT"}`),
	}); err != nil {
		t.Fatal(err)
	}
	recorder.waitForFlush(t)
	if body := recorder.Body(); !strings.Contains(body, "event: opportunity.upsert") ||
		!strings.Contains(body, `"opportunityId":"opp-stream"`) {
		t.Fatalf("event body=%s", body)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not close after request cancellation")
	}
	if count := stream.SubscriberCount(); count != 0 {
		t.Fatalf("subscriber leak=%d", count)
	}
}

func TestSSESinkRevalidatesSessionBeforeEveryDelivery(t *testing.T) {
	clock := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	store := auth.NewMemoryStore()
	service := auth.NewService(store, "test", time.Minute, false)
	service.SetClock(func() time.Time { return clock })
	if err := service.Bootstrap(context.Background(), "owner@example.com", "first secure password"); err != nil {
		t.Fatal(err)
	}
	login, err := service.Login(context.Background(), "owner@example.com", "first secure password", "req-stream-login")
	if err != nil {
		t.Fatal(err)
	}
	recorder := newStreamResponseRecorder()
	sink := &sseSink{writer: recorder, auth: service, token: login.Session.Token}
	event := uistream.StreamEvent{
		Name: "snapshot", Scope: "all", Cursor: "cursor-1",
		Data: json.RawMessage(`{"opportunities":[]}`), OccurredAt: clock,
	}
	if err := sink.Send(context.Background(), event); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	clock = clock.Add(2 * time.Minute)
	if err := sink.Send(context.Background(), event); !errors.Is(err, auth.ErrInvalidSession) {
		t.Fatalf("expired delivery error=%v", err)
	}
}

type contractSnapshotSource struct{}

func (contractSnapshotSource) Snapshot(context.Context, uistream.Request) (uistream.Snapshot, error) {
	return uistream.Snapshot{
		Scope: "all",
		Data:  json.RawMessage(`{"opportunities":[{"opportunityId":"opp-1","symbol":"BTC-USDT"}],"alerts":[],"positions":[],"activities":[],"auditEvents":[]}`),
	}, nil
}

type streamResponseRecorder struct {
	mu      sync.Mutex
	header  http.Header
	body    bytes.Buffer
	status  int
	flushed chan struct{}
}

func newStreamResponseRecorder() *streamResponseRecorder {
	return &streamResponseRecorder{header: make(http.Header), flushed: make(chan struct{}, 8)}
}

func (recorder *streamResponseRecorder) Header() http.Header {
	return recorder.header
}

func (recorder *streamResponseRecorder) WriteHeader(status int) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.status == 0 {
		recorder.status = status
	}
}

func (recorder *streamResponseRecorder) Write(payload []byte) (int, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	return recorder.body.Write(payload)
}

func (recorder *streamResponseRecorder) Flush() {
	recorder.flushed <- struct{}{}
}

func (recorder *streamResponseRecorder) Body() string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.body.String()
}

func (recorder *streamResponseRecorder) waitForFlush(t *testing.T) {
	t.Helper()
	select {
	case <-recorder.flushed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SSE flush")
	}
}

func newReadModelContractServer(t *testing.T) (http.Handler, *http.Cookie, *uistream.Service) {
	t.Helper()
	store := auth.NewMemoryStore()
	service := auth.NewService(store, "test", time.Hour, false)
	if err := service.Bootstrap(context.Background(), "owner@example.com", "first secure password"); err != nil {
		t.Fatal(err)
	}
	stream, err := uistream.NewService(uistream.ServiceConfig{
		BootID:            "http-contract-boot",
		PrincipalID:       auth.DefaultPrincipalID,
		Mode:              "test",
		SigningKey:        []byte("0123456789abcdef0123456789abcdef"),
		Store:             uistream.NewMemoryEventStore(),
		SnapshotSource:    contractSnapshotSource{},
		HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(ServerConfig{
		Mode:      "test",
		Version:   "test",
		Build:     "test",
		Auth:      service,
		ReadModel: contractReadModel{},
		Stream:    stream,
		Readiness: readiness.StaticChecks(map[string]readiness.Component{
			"process":    {Ready: true},
			"database":   {Ready: true},
			"gateway":    {Ready: true},
			"publicData": {Ready: true},
			"accounts":   {Ready: true},
		}),
	})
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"owner@example.com","password":"first secure password"}`))
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies=%+v", cookies)
	}
	return handler, cookies[0], stream
}
