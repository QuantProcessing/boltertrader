package health_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/health"
)

type dependencyFunc func() bool

func (fn dependencyFunc) Healthy() bool { return fn() }

func TestMonitorSeparatesLivenessFromReadinessAndReportsSafeCodes(t *testing.T) {
	postgresHealthy := false
	ownershipHealthy := true
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(false)
	monitor.SetOwnership("acct-a", dependencyFunc(func() bool { return ownershipHealthy }))

	if live := monitor.Liveness(); !live.Live {
		t.Fatalf("liveness = %+v, want live even when dependencies are down", live)
	}
	ready := monitor.Readiness()
	if ready.Ready {
		t.Fatalf("readiness = %+v, want not ready", ready)
	}
	if !containsReason(ready.Reasons, "postgres_unhealthy") {
		t.Fatalf("reasons = %v, want postgres_unhealthy", ready.Reasons)
	}

	monitor.SetPostgresHealthy(true)
	postgresHealthy = true
	_ = postgresHealthy
	if ready := monitor.Readiness(); !ready.Ready {
		t.Fatalf("readiness after postgres recovery = %+v, want ready", ready)
	}

	ownershipHealthy = false
	ready = monitor.Readiness()
	if ready.Ready {
		t.Fatalf("readiness after ownership loss = %+v, want not ready", ready)
	}
	if !containsReason(ready.Reasons, "ownership_unhealthy") {
		t.Fatalf("reasons = %v, want ownership_unhealthy", ready.Reasons)
	}
	for _, reason := range ready.Reasons {
		if reason == "acct-a" {
			t.Fatalf("reason leaked account id: %v", ready.Reasons)
		}
	}
}

func TestMonitorReadinessTracksPostgresDependencyLoss(t *testing.T) {
	postgresHealthy := true
	monitor := health.NewMonitor()
	monitor.SetPostgresDependency(dependencyFunc(func() bool { return postgresHealthy }))
	if ready := monitor.Readiness(); !ready.Ready {
		t.Fatalf("readiness = %+v, want ready", ready)
	}

	postgresHealthy = false
	ready := monitor.Readiness()
	if ready.Ready {
		t.Fatalf("readiness after postgres dependency loss = %+v, want not ready", ready)
	}
	if !containsReason(ready.Reasons, "postgres_unhealthy") {
		t.Fatalf("reasons = %v, want postgres_unhealthy", ready.Reasons)
	}
}

func TestMonitorReadinessTracksOwnershipDependencyLoss(t *testing.T) {
	ownershipHealthy := true
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	monitor.SetOwnership("acct-a", dependencyFunc(func() bool { return ownershipHealthy }))
	if ready := monitor.Readiness(); !ready.Ready {
		t.Fatalf("readiness = %+v, want ready", ready)
	}

	ownershipHealthy = false
	ready := monitor.Readiness()
	if ready.Ready {
		t.Fatalf("readiness after ownership dependency loss = %+v, want not ready", ready)
	}
	if !containsReason(ready.Reasons, "ownership_unhealthy") {
		t.Fatalf("reasons = %v, want ownership_unhealthy", ready.Reasons)
	}
}

func TestHandlerServesOnlyLiveAndReadyHealthRoutes(t *testing.T) {
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	handler := health.NewHandler(monitor)

	tests := map[string]int{
		"/v1/health/live":  http.StatusOK,
		"/v1/health/ready": http.StatusOK,
		"/v1/accounts":     http.StatusNotFound,
	}
	for path, want := range tests {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("%s status = %d, want %d", path, recorder.Code, want)
		}
		if path == "/v1/health/ready" {
			var body health.Readiness
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode readiness: %v", err)
			}
			if !body.Ready {
				t.Fatalf("ready response = %+v, want ready", body)
			}
		}
	}
}

func TestMonitorAllowsConcurrentReadinessTransitions(t *testing.T) {
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			monitor.SetPostgresHealthy(false)
			monitor.SetPostgresHealthy(true)
		}()
		go func() {
			defer wg.Done()
			_ = monitor.Liveness()
			_ = monitor.Readiness()
		}()
	}
	wg.Wait()
}

func containsReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}
