package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/maintenance"
)

func TestServeConstructorRejectsNonServeStartupBeforeExternalAccess(t *testing.T) {
	_, err := New(context.Background(), config.Config{
		Mode:        config.Test,
		StartupMode: config.StartupMaintenance,
		HTTPAddr:    "127.0.0.1:0",
	}, "test", "test", slog.Default())
	if !errors.Is(err, ErrStartupMode) {
		t.Fatalf("New error=%v, want ErrStartupMode", err)
	}
}

func TestMaintenanceAppHasNoDatabaseGatewayOrCredentialDependency(t *testing.T) {
	application, err := NewMaintenance(config.Config{
		Mode:              config.Test,
		StartupMode:       config.StartupMaintenance,
		HTTPAddr:          "127.0.0.1:0",
		MaintenanceReason: "product_mode_cutover",
	}, "test", "build", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if application.Pool != nil || application.GatewayClient != nil ||
		application.MarketData != nil || application.Server == nil {
		t.Fatalf("maintenance app initialized external dependencies: %+v", application)
	}

	live := httptest.NewRecorder()
	application.Server.Handler.ServeHTTP(
		live,
		httptest.NewRequest(http.MethodGet, "/health/live", nil),
	)
	if live.Code != http.StatusOK {
		t.Fatalf("liveness status=%d body=%s", live.Code, live.Body.String())
	}
	login := httptest.NewRecorder()
	application.Server.Handler.ServeHTTP(
		login,
		httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil),
	)
	if login.Code != http.StatusServiceUnavailable {
		t.Fatalf("maintenance login status=%d body=%s", login.Code, login.Body.String())
	}
}

func TestMigrationRunnerEntryRejectsServeConfigurationBeforeDatabaseAccess(t *testing.T) {
	_, err := RunCutover(context.Background(), config.Config{
		Mode:        config.Test,
		StartupMode: config.StartupServe,
	}, slog.Default())
	if !errors.Is(err, ErrStartupMode) {
		t.Fatalf("RunCutover error=%v, want ErrStartupMode", err)
	}
}

func TestShutdownEntersMaintenanceAndDrainsInflightHTTPWork(t *testing.T) {
	gate := maintenance.NewGate()
	started := make(chan struct{})
	released := make(chan struct{})
	handler := gate.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(released)
	}))
	go handler.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil),
	)
	<-started

	application := &App{MaintenanceGate: gate}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := application.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("in-flight request was not canceled and drained")
	}
	state := gate.Snapshot()
	if !state.Enabled || state.TotalDrained != 1 {
		t.Fatalf("maintenance state after shutdown=%+v", state)
	}
}
