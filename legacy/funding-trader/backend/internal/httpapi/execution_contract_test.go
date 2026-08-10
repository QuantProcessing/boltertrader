package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/auth"
	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	"github.com/QuantProcessing/funding-trader/backend/internal/maintenance"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
	"github.com/shopspring/decimal"
)

func TestExecutionPreviewRequiresAuthenticationAndCSRF(t *testing.T) {
	service := &fakeHTTPExecution{}
	handler := newExecutionHTTPHandler(service)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/execution-previews",
		bytes.NewBufferString(`{"opportunityId":"opp-1","capital":"5000","leverage":2}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = authenticatedExecutionRequest(http.MethodPost, "/api/v1/execution-previews",
		`{"opportunityId":"opp-1","capital":"5000","leverage":2}`, "")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.previewCalls != 0 {
		t.Fatalf("execution service was called before csrf validation: %d", service.previewCalls)
	}
}

func TestExecutionMutatingRoutesRespectMaintenanceMode(t *testing.T) {
	service := &fakeHTTPExecution{}
	gate := maintenance.NewGate()
	gate.Enter("product_mode_cutover")
	handler := New(ServerConfig{
		Mode: "test", Version: "test", Build: "test",
		MaintenanceGate: gate,
		Auth:            &executionAuthStub{},
		Execution:       service,
	})

	req := authenticatedExecutionRequest(http.MethodPost, "/api/v1/execution-previews",
		`{"opportunityId":"opp-1","capital":"5000","leverage":2}`, "csrf")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("maintenance preview status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.previewScope != (executionservice.Scope{}) {
		t.Fatalf("execution preview called during maintenance: %+v", service.previewScope)
	}
	if service.submitScope != (executionservice.Scope{}) || service.closeScope != (executionservice.Scope{}) {
		t.Fatalf("execution write called during maintenance: submit=%+v close=%+v", service.submitScope, service.closeScope)
	}

	req = authenticatedExecutionRequest(http.MethodPost, "/api/v1/orders",
		`{"opportunityId":"opp-1","previewId":"preview-1","capital":"5000","leverage":2,"operationKind":"open"}`, "csrf")
	req.Header.Set("Idempotency-Key", "idem-maint")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("maintenance submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.submitScope != (executionservice.Scope{}) || service.closeScope != (executionservice.Scope{}) {
		t.Fatalf("execution writes called during maintenance: submit=%+v close=%+v", service.submitScope, service.closeScope)
	}
}

func TestExecutionPreviewBuildsServerOwnedFreshPreviewRequest(t *testing.T) {
	service := &fakeHTTPExecution{
		preview: execution.PreviewResult{
			PreviewID: "preview-server", SnapshotID: "snapshot-1",
			Operation: execution.OperationOpen, OpportunityID: "opp-1",
			CapitalUSDT: decimal.NewFromInt(5000), Leverage: 2,
			CanSubmit: false, Blockers: []execution.Finding{{
				Code: execution.ReasonRiskUnverifiable, Message: "risk limits are not verifiable",
			}},
		},
	}
	handler := newExecutionHTTPHandler(service)
	req := authenticatedExecutionRequest(http.MethodPost, "/api/v1/execution-previews",
		`{"opportunityId":"opp-1","capital":"5000","leverage":2}`, "csrf")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.previewCalls != 1 ||
		service.previewScope.PrincipalID != auth.DefaultPrincipalID ||
		service.previewScope.Mode != "test" ||
		service.previewRequest.PreviewID == "" ||
		service.previewRequest.Operation != execution.OperationOpen ||
		service.previewRequest.OpportunityID != "opp-1" ||
		!service.previewRequest.CapitalUSDT.Equal(decimal.NewFromInt(5000)) ||
		service.previewRequest.Leverage != 2 {
		t.Fatalf("preview request was not normalized server-side: scope=%+v request=%+v",
			service.previewScope, service.previewRequest)
	}
	var response struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, response.Data,
		"previewId",
		"opportunityId",
		"snapshotId",
		"canSubmit",
		"blockers",
		"warnings",
		"plannedLegs",
		"estimatedFeesUsd",
		"estimatedSlippageUsd",
		"expectedNetAprPct",
		"expectedNetPnL7dUsd",
	)
	if string(response.Data["previewId"]) != `"preview-server"` ||
		string(response.Data["canSubmit"]) != "false" ||
		string(response.Data["blockers"]) == "[]" {
		t.Fatalf("preview response=%s", rec.Body.String())
	}
}

func TestScopesUseConfiguredEnvironmentInsteadOfStaleSessionEnvironment(t *testing.T) {
	server := &Server{cfg: ServerConfig{Mode: "live"}}
	ctx := context.WithValue(context.Background(), principalKey{}, auth.Principal{
		ID:   auth.DefaultPrincipalID,
		Mode: "test",
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/opportunities", nil).WithContext(ctx)

	executionScope := server.executionScope(req)
	if executionScope.PrincipalID != auth.DefaultPrincipalID || executionScope.Mode != "live" {
		t.Fatalf("execution scope=%+v, want configured live mode", executionScope)
	}
	readScope := server.readScope(req)
	if readScope.PrincipalID != auth.DefaultPrincipalID || readScope.Mode != "live" {
		t.Fatalf("read scope=%+v, want configured live mode", readScope)
	}
}

func TestExecutionPreviewUsesFrozenBrowserDTOAndDecimalConventions(t *testing.T) {
	service := &fakeHTTPExecution{
		preview: execution.PreviewResult{
			PreviewID:         "preview-1",
			SnapshotID:        "snapshot-1",
			OpportunityID:     "opp-1",
			CanSubmit:         true,
			FeesUSDT:          decimal.RequireFromString("1.25"),
			SlippageUSDT:      decimal.RequireFromString("2.50"),
			ExpectedNetAprPct: decimal.RequireFromString("12.345"),
			NetProfit7d:       decimal.RequireFromString("8.75"),
			PlannedLegs: []execution.PlannedLeg{{
				LegIdentity: execution.LegIdentity{
					AccountID: "account-1", Venue: "venue-a",
					InstrumentID: "instrument-1", VenueEnvironment: "demo",
				},
				Role:                execution.LegRoleLong,
				Side:                execution.SideBuy,
				Symbol:              "BTCUSDT",
				GatewayInstrumentID: "gateway-instrument-must-not-leak",
				MarkPrice:           decimal.RequireFromString("100.50"),
				Notional:            decimal.RequireFromString("5000.00"),
				Quantity:            decimal.RequireFromString("49.75"),
			}},
		},
	}
	handler := newExecutionHTTPHandler(service)
	req := authenticatedExecutionRequest(http.MethodPost, "/api/v1/execution-previews",
		`{"opportunityId":"opp-1","capital":"5000","leverage":2}`, "csrf")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", rec.Code, rec.Body.String())
	}

	var response struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if string(response.Data["estimatedFeesUsd"]) != `"1.25"` ||
		string(response.Data["estimatedSlippageUsd"]) != `"2.5"` ||
		string(response.Data["expectedNetAprPct"]) != "12.345" ||
		string(response.Data["expectedNetPnL7dUsd"]) != `"8.75"` {
		t.Fatalf("preview decimal contract mismatch: %s", rec.Body.String())
	}
	var plannedLegs []map[string]json.RawMessage
	if err := json.Unmarshal(response.Data["plannedLegs"], &plannedLegs); err != nil {
		t.Fatal(err)
	}
	if len(plannedLegs) != 1 {
		t.Fatalf("planned legs=%s", response.Data["plannedLegs"])
	}
	assertExactJSONKeys(t, plannedLegs[0],
		"role",
		"accountId",
		"venue",
		"venueEnvironment",
		"symbol",
		"instrumentId",
		"side",
		"quantity",
		"notional",
		"markPrice",
		"reduceOnly",
	)
	if string(plannedLegs[0]["venueEnvironment"]) != `"demo"` {
		t.Fatalf("planned leg lost venue environment: %s", response.Data["plannedLegs"])
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("gateway-instrument-must-not-leak")) {
		t.Fatalf("preview leaked gateway-only instrument identity: %s", rec.Body.String())
	}
}

func TestExecutionPreviewRejectsCredentialFieldsAndNonStringCapital(t *testing.T) {
	handler := newExecutionHTTPHandler(&fakeHTTPExecution{})
	for _, body := range []string{
		`{"opportunityId":"opp-1","capital":"5000","leverage":2,"apiKey":"must-not-exist"}`,
		`{"opportunityId":"opp-1","capital":5000,"leverage":2}`,
	} {
		req := authenticatedExecutionRequest(http.MethodPost, "/api/v1/execution-previews", body, "csrf")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestSubmitAndCloseRequireIdempotencyAndReturnAcceptedExecution(t *testing.T) {
	service := &fakeHTTPExecution{
		submitResult: executionservice.Result{Execution: execution.Execution{
			ID: "execution-open", Operation: execution.OperationOpen, State: execution.StateHedged,
		}},
		closeResult: executionservice.Result{Execution: execution.Execution{
			ID: "execution-close", Operation: execution.OperationClose,
			PositionID: "position/1", State: execution.StateClosed,
		}},
	}
	handler := newExecutionHTTPHandler(service)
	openBody := `{"opportunityId":"opp-1","previewId":"preview-1","capital":"5000","leverage":2,"operationKind":"open"}`

	req := authenticatedExecutionRequest(http.MethodPost, "/api/v1/orders", openBody, "csrf")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = authenticatedExecutionRequest(http.MethodPost, "/api/v1/orders", openBody, "csrf")
	req.Header.Set("Idempotency-Key", "idem-open")
	req.Header.Set("X-Request-Id", "request-open")
	requestStartedAt := time.Now().UTC()
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	requestFinishedAt := time.Now().UTC()
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.submitKey != "idem-open" ||
		service.submitRequestID != "request-open" ||
		service.submitRequest.Operation != execution.OperationOpen ||
		service.submitRequest.PreviewID != "preview-1" {
		t.Fatalf("submit request=%+v key=%q requestId=%q",
			service.submitRequest, service.submitKey, service.submitRequestID)
	}
	if service.submitRequest.ReceivedAt.Before(requestStartedAt) ||
		service.submitRequest.ReceivedAt.After(requestFinishedAt) {
		t.Fatalf("submit receivedAt = %s, want within [%s, %s]",
			service.submitRequest.ReceivedAt, requestStartedAt, requestFinishedAt)
	}

	req = authenticatedExecutionRequest(http.MethodPost, "/api/v1/positions/position%2F1/close", "", "csrf")
	req.Header.Set("Idempotency-Key", "idem-close")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("close status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.closeKey != "idem-close" ||
		service.closeRequest.Operation != execution.OperationClose ||
		service.closeRequest.PositionID != "position/1" ||
		service.closeRequest.PreviewID != "" {
		t.Fatalf("close request=%+v key=%q", service.closeRequest, service.closeKey)
	}
}

func TestGetExecutionMapsScopedNotFoundAndReturnsExactSnapshot(t *testing.T) {
	latestEventAt := time.Date(2026, time.July, 29, 16, 30, 0, 123000000, time.UTC)
	service := &fakeHTTPExecution{getExecution: execution.Execution{
		ID: "execution-1", Operation: execution.OperationOpen,
		OpportunityID: "opp-1",
		State:         execution.StateManualInterventionRequired,
		UpdatedAt:     latestEventAt,
		GatewayIDs:    []string{"client-command-must-not-leak"},
		Compensation: &execution.CompensationState{
			Attempted:      true,
			OrderID:        "gateway-order-2",
			TargetRole:     execution.LegRoleLong,
			Quantity:       decimal.RequireFromString("0.25"),
			Status:         execution.LegStatusUncertain,
			Error:          execution.ErrorInfo{Code: "WRITE_UNKNOWN", SafeMessage: "write status unknown", Retryable: true},
			ManualRequired: true,
		},
		ReconciliationState:      "blocked",
		BlockingReason:           "manual review required",
		ManualIntervention:       true,
		ManualInterventionReason: "internal duplicate must not become a second field",
		Legs: []execution.Leg{{
			LegIdentity: execution.LegIdentity{
				AccountID: "account-1", Venue: "venue-a",
				InstrumentID: "instrument-1", VenueEnvironment: "demo",
			},
			LegID:             "long",
			Role:              execution.LegRoleLong,
			Side:              execution.SideBuy,
			RequestedQuantity: decimal.RequireFromString("1.5"),
			Filled:            decimal.RequireFromString("1.25"),
			State:             execution.LegStatusPartial,
			GatewayOrderID:    "gateway-order-1",
			GatewayID:         "client-command-must-not-leak",
			LastError:         execution.ErrorInfo{Code: "PARTIAL", SafeMessage: "partially filled", Retryable: false},
			UpdatedAt:         latestEventAt,
		}},
		Fills: []execution.Fill{{
			LegIdentity: execution.LegIdentity{
				AccountID: "account-1", Venue: "venue-a",
				InstrumentID: "instrument-1", VenueEnvironment: "demo",
			},
			FillID:        "fill-1",
			LegID:         "long",
			GatewayFillID: "gateway-fill-1",
			GatewayID:     "client-command-must-not-leak",
			Quantity:      decimal.RequireFromString("1.25"),
			Price:         decimal.RequireFromString("101.50"),
			Fee:           execution.Money{Amount: decimal.RequireFromString("0.20"), Currency: "USDT"},
			OccurredAt:    latestEventAt,
		}},
	}}
	handler := newExecutionHTTPHandler(service)
	req := authenticatedExecutionRequest(http.MethodGet, "/api/v1/executions/execution-1", "", "")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
	}
	if service.getID != "execution-1" ||
		service.getScope.PrincipalID != auth.DefaultPrincipalID {
		t.Fatalf("get scope=%+v id=%q", service.getScope, service.getID)
	}
	var response struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, response.Data,
		"executionId",
		"operationKind",
		"opportunityId",
		"state",
		"latestEventAt",
		"gatewayOrderIds",
		"compensationState",
		"reconciliationState",
		"blockingReason",
		"manualInterventionRequired",
		"legs",
		"fills",
	)
	if string(response.Data["executionId"]) != `"execution-1"` ||
		string(response.Data["state"]) != `"manualInterventionRequired"` ||
		string(response.Data["latestEventAt"]) != `"2026-07-29T16:30:00.123Z"` ||
		string(response.Data["gatewayOrderIds"]) != `["gateway-order-1"]` {
		t.Fatalf("execution response contract mismatch: %s", rec.Body.String())
	}
	var legs []map[string]json.RawMessage
	if err := json.Unmarshal(response.Data["legs"], &legs); err != nil {
		t.Fatal(err)
	}
	if len(legs) != 1 {
		t.Fatalf("legs=%s", response.Data["legs"])
	}
	assertExactJSONKeys(t, legs[0],
		"legId",
		"role",
		"accountId",
		"venue",
		"instrumentId",
		"venueEnvironment",
		"side",
		"requestedQuantity",
		"filledQuantity",
		"state",
		"gatewayOrderId",
		"lastError",
		"updatedAt",
	)
	if string(legs[0]["venueEnvironment"]) != `"demo"` ||
		string(legs[0]["requestedQuantity"]) != `"1.5"` ||
		string(legs[0]["filledQuantity"]) != `"1.25"` ||
		string(legs[0]["state"]) != `"partiallyFilled"` ||
		!bytes.Contains(legs[0]["lastError"], []byte(`"message":"partially filled"`)) {
		t.Fatalf("leg response contract mismatch: %s", response.Data["legs"])
	}
	var fills []map[string]json.RawMessage
	if err := json.Unmarshal(response.Data["fills"], &fills); err != nil {
		t.Fatal(err)
	}
	if len(fills) != 1 {
		t.Fatalf("fills=%s", response.Data["fills"])
	}
	assertExactJSONKeys(t, fills[0],
		"fillId",
		"legId",
		"accountId",
		"venue",
		"instrumentId",
		"venueEnvironment",
		"gatewayFillId",
		"quantity",
		"price",
		"fee",
		"occurredAt",
	)
	if string(fills[0]["accountId"]) != `"account-1"` ||
		string(fills[0]["venue"]) != `"venue-a"` ||
		string(fills[0]["instrumentId"]) != `"instrument-1"` ||
		string(fills[0]["venueEnvironment"]) != `"demo"` ||
		string(fills[0]["quantity"]) != `"1.25"` ||
		string(fills[0]["price"]) != `"101.5"` ||
		!bytes.Contains(fills[0]["fee"], []byte(`"amount":"0.2"`)) {
		t.Fatalf("fill response contract mismatch: %s", response.Data["fills"])
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("client-command-must-not-leak")) ||
		bytes.Contains(rec.Body.Bytes(), []byte(`"principalId"`)) ||
		bytes.Contains(rec.Body.Bytes(), []byte(`"manualInterventionReason"`)) {
		t.Fatalf("execution response leaked internal fields: %s", rec.Body.String())
	}

	service.getErr = executionservice.ErrNotFound
	req = authenticatedExecutionRequest(http.MethodGet, "/api/v1/executions/missing", "", "")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestExecutionPublicStateMappingIsFrozen(t *testing.T) {
	tests := []struct {
		state execution.State
		fills []execution.Fill
		want  string
	}{
		{state: execution.StatePreviewed, want: "open"},
		{state: execution.StateSubmitting, want: "open"},
		{state: execution.StateLeg1Working, want: "open"},
		{state: execution.StateLeg1Working, fills: []execution.Fill{{FillID: "fill-1"}}, want: "partiallyFilled"},
		{state: execution.StateLeg2Working, want: "open"},
		{state: execution.StateHedged, want: "open"},
		{state: execution.StatePartiallyHedged, want: "partiallyHedged"},
		{state: execution.StateCompensating, want: "compensating"},
		{state: execution.StateClosed, want: "completed"},
		{state: execution.StateFailed, want: "rejected"},
		{state: execution.StateUncertain, want: "uncertain"},
		{state: execution.StateManualInterventionRequired, want: "manualInterventionRequired"},
	}
	for _, test := range tests {
		t.Run(string(test.state)+test.want, func(t *testing.T) {
			if got := executionStateDTO(execution.Execution{State: test.state, Fills: test.fills}); got != test.want {
				t.Fatalf("executionStateDTO(%s)=%q want %q", test.state, got, test.want)
			}
		})
	}
}

func TestExecutionStreamEventsAndSnapshotsUseFrozenBrowserDTO(t *testing.T) {
	latestEventAt := time.Date(2026, time.July, 29, 18, 0, 0, 0, time.UTC)
	internal := execution.Execution{
		ID:              "execution-stream",
		PrincipalID:     "principal-must-not-leak",
		Mode:            "test",
		Operation:       execution.OperationOpen,
		OpportunityID:   "opp-1",
		State:           execution.StateHedged,
		UpdatedAt:       latestEventAt,
		GatewayIDs:      []string{"client-command-must-not-leak"},
		AppliedEventIDs: []string{"internal-event-must-not-leak"},
		Legs: []execution.Leg{{
			LegIdentity: execution.LegIdentity{
				AccountID: "account-1", Venue: "venue-a",
				InstrumentID: "instrument-1", VenueEnvironment: "demo",
			},
			LegID:             "long",
			Role:              execution.LegRoleLong,
			Side:              execution.SideBuy,
			RequestedQuantity: decimal.RequireFromString("1.5"),
			Filled:            decimal.RequireFromString("1.5"),
			State:             execution.LegStatusFilled,
			GatewayOrderID:    "gateway-order-1",
			GatewayID:         "client-command-must-not-leak",
			UpdatedAt:         latestEventAt,
		}},
	}
	internalPayload, err := json.Marshal(internal)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("execution upsert", func(t *testing.T) {
		payload, streamErr := streamEventData(uistream.StreamEvent{
			Name:       uistream.EventData,
			Kind:       string(readmodel.ExecutionUpsert),
			Cursor:     "cursor-1",
			Data:       internalPayload,
			OccurredAt: latestEventAt,
		})
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		assertStreamExecutionPublicDTO(t, payload, false)
	})

	t.Run("snapshot executions", func(t *testing.T) {
		snapshotPayload, marshalErr := json.Marshal(map[string]any{
			"opportunities": []any{},
			"executions":    []json.RawMessage{internalPayload},
			"asOf":          latestEventAt,
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		payload, streamErr := streamEventData(uistream.StreamEvent{
			Name:       uistream.EventSnapshot,
			Cursor:     "cursor-2",
			Data:       snapshotPayload,
			OccurredAt: latestEventAt,
		})
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		assertStreamExecutionPublicDTO(t, payload, true)
	})
}

func assertStreamExecutionPublicDTO(t *testing.T, payload []byte, snapshot bool) {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	executionPayload := envelope.Data
	if snapshot {
		var data struct {
			Executions []json.RawMessage `json:"executions"`
		}
		if err := json.Unmarshal(envelope.Data, &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Executions) != 1 {
			t.Fatalf("executions=%s", envelope.Data)
		}
		executionPayload = data.Executions[0]
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(executionPayload, &fields); err != nil {
		t.Fatal(err)
	}
	assertExactJSONKeys(t, fields,
		"executionId",
		"operationKind",
		"opportunityId",
		"state",
		"latestEventAt",
		"gatewayOrderIds",
		"compensationState",
		"reconciliationState",
		"blockingReason",
		"manualInterventionRequired",
		"legs",
		"fills",
	)
	if string(fields["executionId"]) != `"execution-stream"` ||
		string(fields["state"]) != `"open"` ||
		bytes.Contains(executionPayload, []byte("principal-must-not-leak")) ||
		bytes.Contains(executionPayload, []byte("client-command-must-not-leak")) ||
		bytes.Contains(executionPayload, []byte("internal-event-must-not-leak")) {
		t.Fatalf("stream execution contract mismatch: %s", executionPayload)
	}
}

func TestExecutionResultUsesPersistedFirstSnapshotForIdempotentReplay(t *testing.T) {
	first := execution.Execution{
		ID: "execution-first", Operation: execution.OperationOpen,
		OpportunityID: "opp-1", State: execution.StateHedged,
		UpdatedAt: time.Date(2026, time.July, 29, 17, 0, 0, 0, time.UTC),
	}
	body, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeHTTPExecution{submitResult: executionservice.Result{
		Execution: execution.Execution{
			ID: "execution-current", Operation: execution.OperationOpen,
			OpportunityID: "opp-1", State: execution.StateUncertain,
		},
		ResponseCode: http.StatusAccepted,
		ResponseBody: body,
		Replayed:     true,
	}}
	handler := newExecutionHTTPHandler(service)
	req := authenticatedExecutionRequest(http.MethodPost, "/api/v1/orders",
		`{"opportunityId":"opp-1","previewId":"preview-1","capital":"5000","leverage":2,"operationKind":"open"}`, "csrf")
	req.Header.Set("Idempotency-Key", "idem-first")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data map[string]json.RawMessage `json:"data"`
		Meta map[string]json.RawMessage `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if string(response.Data["executionId"]) != `"execution-first"` ||
		string(response.Data["state"]) != `"open"` ||
		string(response.Meta["replayed"]) != "true" {
		t.Fatalf("replay did not use persisted first snapshot: %s", rec.Body.String())
	}
}

func TestPersistedExecutionErrorReturnsRecoverableAcceptedResult(t *testing.T) {
	tests := []struct {
		name        string
		target      string
		body        string
		executionID string
		service     *fakeHTTPExecution
	}{
		{
			name:        "submit",
			target:      "/api/v1/orders",
			body:        `{"opportunityId":"opp-1","previewId":"preview-1","capital":"5000","leverage":2,"operationKind":"open"}`,
			executionID: "execution-open-recovering",
			service: &fakeHTTPExecution{
				submitResult: executionservice.Result{Execution: execution.Execution{
					ID: "execution-open-recovering", Operation: execution.OperationOpen, State: execution.StateLeg1Working,
				}},
				submitErr: executionservice.ErrServiceUnavailable,
			},
		},
		{
			name:        "close",
			target:      "/api/v1/positions/position-1/close",
			executionID: "execution-close-recovering",
			service: &fakeHTTPExecution{
				closeResult: executionservice.Result{Execution: execution.Execution{
					ID: "execution-close-recovering", Operation: execution.OperationClose, PositionID: "position-1", State: execution.StatePartiallyHedged,
				}},
				closeErr: executionservice.ErrServiceUnavailable,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newExecutionHTTPHandler(test.service)
			req := authenticatedExecutionRequest(http.MethodPost, test.target, test.body, "csrf")
			req.Header.Set("Idempotency-Key", "idem-"+test.name)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var response struct {
				Data map[string]json.RawMessage `json:"data"`
				Meta map[string]json.RawMessage `json:"meta"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if string(response.Data["executionId"]) != `"`+test.executionID+`"` ||
				string(response.Meta["degraded"]) != "true" ||
				string(response.Meta["recoverable"]) != "true" {
				t.Fatalf("persisted execution error did not preserve recoverable result: %s", rec.Body.String())
			}
		})
	}
}

func TestExecutionErrorsUseSafeStableHTTPEnvelopes(t *testing.T) {
	service := &fakeHTTPExecution{submitErr: executionservice.ErrIdempotencyConflict}
	handler := newExecutionHTTPHandler(service)
	req := authenticatedExecutionRequest(http.MethodPost, "/api/v1/orders",
		`{"opportunityId":"opp-1","previewId":"preview-1","capital":"5000","leverage":2,"operationKind":"open"}`, "csrf")
	req.Header.Set("Idempotency-Key", "idem-open")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("super-secret")) {
		t.Fatalf("response leaked raw upstream detail: %s", rec.Body.String())
	}

	service.submitErr = errors.New("gateway super-secret raw failure")
	req = authenticatedExecutionRequest(http.MethodPost, "/api/v1/orders",
		`{"opportunityId":"opp-1","previewId":"preview-1","capital":"5000","leverage":2,"operationKind":"open"}`, "csrf")
	req.Header.Set("Idempotency-Key", "idem-other")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable ||
		bytes.Contains(rec.Body.Bytes(), []byte("super-secret")) {
		t.Fatalf("unsafe upstream error response status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func newExecutionHTTPHandler(service *fakeHTTPExecution) http.Handler {
	return New(ServerConfig{
		Mode: "test", Version: "test", Build: "test",
		Auth: &executionAuthStub{}, Execution: service,
	})
}

func authenticatedExecutionRequest(method, target, body, csrf string) *http.Request {
	var requestBody *bytes.Reader
	if body == "" {
		requestBody = bytes.NewReader(nil)
	} else {
		requestBody = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, target, requestBody)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session"})
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	return req
}

func assertExactJSONKeys(t *testing.T, object map[string]json.RawMessage, expected ...string) {
	t.Helper()
	if len(object) != len(expected) {
		t.Fatalf("JSON keys=%v want exactly %v", jsonKeys(object), expected)
	}
	for _, key := range expected {
		if _, ok := object[key]; !ok {
			t.Fatalf("JSON keys=%v missing %q", jsonKeys(object), key)
		}
	}
}

func jsonKeys(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	return keys
}

type executionAuthStub struct{}

func (*executionAuthStub) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{}, errors.New("not implemented")
}

func (*executionAuthStub) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	if token != "session" {
		return auth.Principal{}, auth.ErrInvalidSession
	}
	return auth.Principal{ID: auth.DefaultPrincipalID, Mode: "test"}, nil
}

func (*executionAuthStub) CurrentSession(_ context.Context, token string) (auth.CurrentSessionResult, error) {
	if token != "session" {
		return auth.CurrentSessionResult{}, auth.ErrInvalidSession
	}
	return auth.CurrentSessionResult{
		Principal: auth.Principal{ID: auth.DefaultPrincipalID, Mode: "test"},
		Session: auth.Session{
			Token: token, CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour),
		},
	}, nil
}

func (*executionAuthStub) RequireCSRF(_ context.Context, token, csrf string) error {
	if token != "session" {
		return auth.ErrInvalidSession
	}
	if csrf != "csrf" {
		return auth.ErrInvalidCSRF
	}
	return nil
}

func (*executionAuthStub) Logout(context.Context, string, string, string) error { return nil }

func (*executionAuthStub) AuditSecurityOutcome(context.Context, string, string, string, string) error {
	return nil
}

func (*executionAuthStub) CookieSecure() bool { return false }

type fakeHTTPExecution struct {
	preview        execution.PreviewResult
	previewErr     error
	previewCalls   int
	previewScope   executionservice.Scope
	previewRequest executionservice.PreviewRequest

	submitResult    executionservice.Result
	submitErr       error
	submitScope     executionservice.Scope
	submitRequest   executionservice.SubmitRequest
	submitKey       string
	submitRequestID string

	closeResult    executionservice.Result
	closeErr       error
	closeScope     executionservice.Scope
	closeRequest   executionservice.SubmitRequest
	closeKey       string
	closeRequestID string

	getExecution execution.Execution
	getErr       error
	getScope     executionservice.Scope
	getID        string
}

func (service *fakeHTTPExecution) Preview(
	_ context.Context,
	scope executionservice.Scope,
	request executionservice.PreviewRequest,
) (execution.PreviewResult, error) {
	service.previewCalls++
	service.previewScope = scope
	service.previewRequest = request
	return service.preview, service.previewErr
}

func (service *fakeHTTPExecution) Submit(
	_ context.Context,
	scope executionservice.Scope,
	request executionservice.SubmitRequest,
	key string,
	requestID string,
) (executionservice.Result, error) {
	service.submitScope = scope
	service.submitRequest = request
	service.submitKey = key
	service.submitRequestID = requestID
	return service.submitResult, service.submitErr
}

func (service *fakeHTTPExecution) Close(
	_ context.Context,
	scope executionservice.Scope,
	request executionservice.SubmitRequest,
	key string,
	requestID string,
) (executionservice.Result, error) {
	service.closeScope = scope
	service.closeRequest = request
	service.closeKey = key
	service.closeRequestID = requestID
	return service.closeResult, service.closeErr
}

func (service *fakeHTTPExecution) Get(
	_ context.Context,
	scope executionservice.Scope,
	id string,
) (execution.Execution, error) {
	service.getScope = scope
	service.getID = id
	return service.getExecution, service.getErr
}
