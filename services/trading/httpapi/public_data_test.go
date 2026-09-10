package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	platformsystem "github.com/QuantProcessing/boltertrader/internal/system"
)

type countingReadiness struct {
	statusCalls    int
	readinessCalls int
}

func (readiness *countingReadiness) Status() platformsystem.Snapshot {
	readiness.statusCalls++
	return platformsystem.Snapshot{Ready: true, Components: map[string]platformsystem.Component{}}
}

func (readiness *countingReadiness) Readiness() platformsystem.Snapshot {
	readiness.readinessCalls++
	return platformsystem.Snapshot{Ready: true, Components: map[string]platformsystem.Component{}}
}

func TestSystemEndpointEvaluatesReadinessOnlyOnce(t *testing.T) {
	readiness := &countingReadiness{}
	api := &PublicDataAPI{dependencies: PublicDataDependencies{Readiness: readiness}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/readiness", nil)
	response := httptest.NewRecorder()
	api.system(response, request, true)
	if response.Code != http.StatusOK || readiness.readinessCalls != 1 || readiness.statusCalls != 0 {
		t.Fatalf("readiness response=%d statusCalls=%d readinessCalls=%d", response.Code, readiness.statusCalls, readiness.readinessCalls)
	}
}

func TestTradingPublicSurfaceDoesNotMountGatewayMarketReads(t *testing.T) {
	readiness := &countingReadiness{}
	api, err := NewPublicDataAPI(PublicDataDependencies{Readiness: readiness})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := api.Register(mux); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/catalog/venues", "/api/v1/catalog/instruments", "/api/v1/market-data",
		"/api/v1/candles", "/api/v1/funding-board", "/api/v1/funding-history", "/api/v1/venues/connectivity",
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("Trading still serves %s: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}
