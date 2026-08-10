package maintenance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGateRejectsEveryNonHealthRequestBeforeDownstreamWork(t *testing.T) {
	gate := NewGate()
	var calls atomic.Int64
	handler := gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	gate.Enter("product_mode_cutover")

	for _, path := range []string{
		"/api/v1/auth/login",
		"/api/v1/execution-previews",
		"/api/v1/orders",
		"/api/v1/positions/position-1/close",
		"/api/v1/audit-events",
		"/api/v1/stream",
		"/metrics",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Fatalf("%s omitted Retry-After", path)
		}
		var response struct {
			Error struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Retryable bool   `json:"retryable"`
				Details   struct {
					Reason string `json:"reason"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("%s response: %v", path, err)
		}
		if response.Error.Code != "service_maintenance" ||
			response.Error.Message != "service is under operator maintenance" ||
			!response.Error.Retryable ||
			response.Error.Details.Reason != "product_mode_cutover" {
			t.Fatalf("%s response=%+v", path, response)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("downstream calls=%d during maintenance", calls.Load())
	}
}

func TestGateKeepsHealthEndpointsAvailable(t *testing.T) {
	gate := NewGate()
	handler := gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	gate.Enter("cutover")

	for _, path := range []string{"/health/live", "/health/ready"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s status=%d", path, rec.Code)
		}
	}
}

func TestEnterCancelsAndDrainsInflightRequestsThenRejectsNewWork(t *testing.T) {
	gate := NewGate()
	started := make(chan struct{})
	released := make(chan struct{})
	handler := gate.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(released)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(
			httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil),
		)
	}()
	<-started

	if active := gate.Enter("cutover"); active != 1 {
		t.Fatalf("active at maintenance entry=%d, want 1", active)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if drained, err := gate.Drain(ctx); err != nil || drained != 1 {
		t.Fatalf("Drain() drained=%d error=%v", drained, err)
	}
	<-released
	<-done

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("new request after maintenance status=%d", rec.Code)
	}
}
