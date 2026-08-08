package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayRuntimeReadinessProbeFailsClosed(t *testing.T) {
	const token = "runtime-readiness-token-at-least-32-bytes"
	ready := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/internal/v1/runtime/readiness" || request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if ready {
			_, _ = w.Write([]byte(`{"data":{"ready":true}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ready":false}}`))
	}))
	defer server.Close()
	adapter, err := newGatewayAdapter(server.URL, []byte(token), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckRuntimeRoute(context.Background()); err != nil {
		t.Fatalf("ready CheckRuntimeRoute() error=%v", err)
	}
	ready = false
	if err := adapter.CheckRuntimeRoute(context.Background()); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unready CheckRuntimeRoute() error=%v", err)
	}
}
