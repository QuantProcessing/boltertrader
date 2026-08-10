package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantProcessing/funding-trader/backend/internal/observability"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
)

func TestMetricsExposeRequiredOperationalStatesWithoutIdentityLabels(t *testing.T) {
	handler := New(ServerConfig{
		Mode: "test",
		Auth: &executionAuthStub{},
		Metrics: fakeMetricsReader{snapshot: observability.Snapshot{
			PartiallyHedged:            2,
			Compensating:               3,
			Uncertain:                  5,
			ManualInterventionRequired: 7,
			ResnapshotRequired:         11,
		}},
	})
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics status=%d body=%s", response.Code, response.Body.String())
	}

	request = authenticatedExecutionRequest(http.MethodGet, "/metrics", "", "")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("metrics content type=%q", contentType)
	}
	body := response.Body.String()
	for _, expected := range []string{
		`funding_trader_execution_state_current{state="partiallyHedged"} 2`,
		`funding_trader_execution_state_current{state="compensating"} 3`,
		`funding_trader_execution_state_current{state="uncertain"} 5`,
		`funding_trader_execution_state_current{state="manualInterventionRequired"} 7`,
		`funding_trader_resnapshot_required_events 11`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics missing %q:\n%s", expected, body)
		}
	}
	for _, forbidden := range []string{"principal-default", "demo", "account", "token", "secret"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("metrics leaked %q:\n%s", forbidden, body)
		}
	}
}

func TestMetricsFailureIsSafeAndDoesNotExposeRawError(t *testing.T) {
	handler := New(ServerConfig{
		Mode:    "test",
		Auth:    &executionAuthStub{},
		Metrics: fakeMetricsReader{err: errors.New("postgres://owner:secret@db/internal")},
	})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, authenticatedExecutionRequest(http.MethodGet, "/metrics", "", ""))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("metrics status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "postgres") || strings.Contains(body, "secret") {
		t.Fatalf("metrics exposed raw error: %s", body)
	}
}

type fakeMetricsReader struct {
	snapshot observability.Snapshot
	err      error
}

func (reader fakeMetricsReader) Snapshot(context.Context, readmodel.Scope) (observability.Snapshot, error) {
	return reader.snapshot, reader.err
}
