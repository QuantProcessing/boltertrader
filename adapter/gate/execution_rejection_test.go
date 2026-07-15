package gate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
)

type gateRoundTripFunc func(*http.Request) (*http.Response, error)

func (f gateRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGateExecutionMapsOnlyTypedDefinitiveCommandRejections(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantReject bool
	}{
		{name: "business rejection", status: http.StatusBadRequest, body: `{"label":"INVALID_PARAM_VALUE","message":"bad order"}`, wantReject: true},
		{name: "unknown business label", status: http.StatusBadRequest, body: `{"label":"FUTURE_UNCLASSIFIED_LABEL","message":"unknown"}`},
		{name: "temporary server error", status: http.StatusServiceUnavailable, body: `{"label":"SERVER_ERROR","message":"retry"}`},
		{name: "malformed success", status: http.StatusOK, body: `{`},
		{name: "partial success", status: http.StatusOK, body: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			provider := gateSpotTestProvider()
			exec := newExecutionClient(
				gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
				provider,
				clock.NewRealClock(),
			)
			id := provider.All()[0].ID
			commands := []struct {
				name string
				run  func() error
			}{
				{name: "submit", run: func() error { _, err := exec.Submit(context.Background(), validGateValidationRequest(id)); return err }},
				{name: "cancel", run: func() error { return exec.Cancel(context.Background(), id, "123") }},
			}
			for _, command := range commands {
				t.Run(command.name, func(t *testing.T) {
					err := command.run()
					if err == nil {
						t.Fatal("command returned nil error")
					}
					if got := errors.Is(err, contract.ErrVenueRejected); got != test.wantReject {
						t.Fatalf("error=%v venueRejected=%t, want %t", err, got, test.wantReject)
					}
					if test.wantReject {
						var apiErr *gatesdk.APIError
						if !errors.As(err, &apiErr) || apiErr.Label != "INVALID_PARAM_VALUE" {
							t.Fatalf("mapped error lost typed Gate response: %v", err)
						}
					}
				})
			}
		})
	}
}

func TestGateExecutionLeavesContextCancellationAmbiguous(t *testing.T) {
	provider := gateSpotTestProvider()
	exec := newExecutionClient(gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL("http://127.0.0.1:1"), provider, clock.NewRealClock())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := exec.Submit(ctx, validGateValidationRequest(provider.All()[0].ID))
	if !errors.Is(err, context.Canceled) || errors.Is(err, contract.ErrVenueRejected) {
		t.Fatalf("Submit error=%v, want ambiguous context cancellation", err)
	}
}

func TestGateExecutionLeavesAfterHandoffTransportEOFAmbiguousForEverySupportedCommand(t *testing.T) {
	provider := gateSpotTestProvider()
	id := provider.All()[0].ID
	commands := []struct {
		name   string
		method string
		path   string
		run    func(*executionClient) error
	}{
		{name: "submit", method: http.MethodPost, path: "/spot/orders", run: func(exec *executionClient) error {
			_, err := exec.Submit(context.Background(), validGateValidationRequest(id))
			return err
		}},
		{name: "cancel", method: http.MethodDelete, path: "/spot/orders/123", run: func(exec *executionClient) error {
			return exec.Cancel(context.Background(), id, "123")
		}},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			var calls atomic.Int32
			httpClient := &http.Client{Transport: gateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if req.Method != command.method || req.URL.Path != command.path {
					t.Errorf("request=%s %s, want %s %s", req.Method, req.URL.Path, command.method, command.path)
				}
				return nil, io.EOF
			})}
			exec := newExecutionClient(gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL("https://gate.test").WithHTTPClient(httpClient), provider, clock.NewRealClock())

			err := command.run(exec)
			if calls.Load() != 1 {
				t.Fatalf("transport calls=%d, want one after-handoff request", calls.Load())
			}
			if !errors.Is(err, io.EOF) || errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("error=%v, want ambiguous after-handoff EOF", err)
			}
		})
	}
}

func TestGateExecutionLeavesIdentityMismatchAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"wrong","text":"t-other","currency_pair":"ETH_USDT"}`))
	}))
	t.Cleanup(server.Close)
	provider := gateSpotTestProvider()
	exec := newExecutionClient(gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock())
	id := provider.All()[0].ID
	commands := []struct {
		name string
		run  func() error
	}{
		{name: "submit", run: func() error { _, err := exec.Submit(context.Background(), validGateValidationRequest(id)); return err }},
		{name: "cancel", run: func() error { return exec.Cancel(context.Background(), id, "123") }},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			err := command.run()
			if err == nil || errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("error=%v, want ambiguous identity mismatch", err)
			}
		})
	}
}

func TestGateCancelMapsBusinessRejectionAndModifyRemainsUnsupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"label":"ORDER_NOT_FOUND","message":"missing"}`))
	}))
	t.Cleanup(server.Close)
	provider := gateSpotTestProvider()
	exec := newExecutionClient(gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock())
	id := provider.All()[0].ID
	if err := exec.Cancel(context.Background(), id, "123"); !errors.Is(err, contract.ErrVenueRejected) {
		t.Fatalf("Cancel error=%v, want ErrVenueRejected", err)
	}
	if _, err := exec.Modify(context.Background(), id, "123", d("1"), d("1")); !errors.Is(err, errs.ErrNotSupported) || errors.Is(err, contract.ErrVenueRejected) {
		t.Fatalf("Modify error=%v, want only ErrNotSupported", err)
	}
}
