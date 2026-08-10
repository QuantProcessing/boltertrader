package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDatabaseFreeMaintenanceHandlerExposesOnlyHealth(t *testing.T) {
	handler := NewMaintenance(MaintenanceConfig{
		Mode:    "test",
		Version: "v1-test",
		Build:   "build-test",
		Reason:  "product_mode_cutover",
	})

	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("liveness status=%d body=%s", live.Code, live.Body.String())
	}
	var liveBody struct {
		Data struct {
			Status      string `json:"status"`
			Maintenance bool   `json:"maintenance"`
			Mode        string `json:"mode"`
		} `json:"data"`
	}
	if err := json.Unmarshal(live.Body.Bytes(), &liveBody); err != nil {
		t.Fatal(err)
	}
	if liveBody.Data.Status != "live" || !liveBody.Data.Maintenance ||
		liveBody.Data.Mode != "test" {
		t.Fatalf("liveness body=%+v", liveBody)
	}

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status=%d body=%s", ready.Code, ready.Body.String())
	}

	for _, path := range []string{
		"/api/v1/auth/login",
		"/api/v1/system/status",
		"/api/v1/stream",
		"/metrics",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != "service_maintenance" {
			t.Fatalf("%s error code=%q", path, body.Error.Code)
		}
	}
}
