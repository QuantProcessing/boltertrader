package bitget

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
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

type bitgetRoundTripFunc func(*http.Request) (*http.Response, error)

func (f bitgetRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBitgetExecutionMapsTypedCommandResponseRejections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, map[string]any{"code": "43001", "msg": "order does not exist", "data": map[string]any{}})
	}))
	t.Cleanup(server.Close)
	provider := bitgetTestProvider()
	exec := newExecutionClient(bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock())
	id := provider.All()[0].ID
	req := validBitgetValidationRequest(id)
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "submit", run: func() error { _, err := exec.Submit(context.Background(), req); return err }},
		{name: "cancel", run: func() error { return exec.Cancel(context.Background(), id, "order-1") }},
		{name: "modify", run: func() error {
			_, err := exec.Modify(context.Background(), id, "order-1", decimal.NewFromInt(100), decimal.NewFromInt(1))
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			if !errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("error=%v, want ErrVenueRejected", err)
			}
			var responseErr *bitgetsdk.ResponseError
			if !errors.As(err, &responseErr) || responseErr.Code != "43001" {
				t.Fatalf("mapped error lost typed Bitget response: %v", err)
			}
		})
	}
}

func TestBitgetExecutionLeavesHTTPAndMalformedFailuresAmbiguous(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http 5xx", status: http.StatusServiceUnavailable, body: `{"code":"50000","msg":"server error"}`},
		{name: "malformed success", status: http.StatusOK, body: `{`},
		{name: "partial success", status: http.StatusOK, body: `{"code":"00000","msg":"success"}`},
		{name: "typed temporary response", status: http.StatusOK, body: `{"code":"50000","msg":"server error","data":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			provider := bitgetTestProvider()
			exec := newExecutionClient(bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock())
			id := provider.All()[0].ID
			commands := []struct {
				name string
				run  func() error
			}{
				{name: "submit", run: func() error {
					_, err := exec.Submit(context.Background(), validBitgetValidationRequest(id))
					return err
				}},
				{name: "cancel", run: func() error { return exec.Cancel(context.Background(), id, "order-1") }},
				{name: "modify", run: func() error {
					_, err := exec.Modify(context.Background(), id, "order-1", decimal.NewFromInt(100), decimal.NewFromInt(1))
					return err
				}},
			}
			for _, command := range commands {
				t.Run(command.name, func(t *testing.T) {
					err := command.run()
					if err == nil || errors.Is(err, contract.ErrVenueRejected) {
						t.Fatalf("error=%v, want ambiguous non-nil failure", err)
					}
				})
			}
		})
	}
}

func TestBitgetExecutionLeavesContextCancellationAmbiguous(t *testing.T) {
	provider := bitgetTestProvider()
	exec := newExecutionClient(bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL("http://127.0.0.1:1"), provider, clock.NewRealClock())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := exec.Submit(ctx, validBitgetValidationRequest(provider.All()[0].ID))
	if !errors.Is(err, context.Canceled) || errors.Is(err, contract.ErrVenueRejected) {
		t.Fatalf("Submit error=%v, want ambiguous context cancellation", err)
	}
}

func TestBitgetExecutionLeavesAfterHandoffTransportEOFAmbiguousForEverySupportedCommand(t *testing.T) {
	provider := bitgetTestProvider()
	id := provider.All()[0].ID
	commands := []struct {
		name string
		path string
		run  func(*executionClient) error
	}{
		{name: "submit", path: "/api/v3/trade/place-order", run: func(exec *executionClient) error {
			_, err := exec.Submit(context.Background(), validBitgetValidationRequest(id))
			return err
		}},
		{name: "cancel", path: "/api/v3/trade/cancel-order", run: func(exec *executionClient) error {
			return exec.Cancel(context.Background(), id, "order-1")
		}},
		{name: "modify", path: "/api/v3/trade/modify-order", run: func(exec *executionClient) error {
			_, err := exec.Modify(context.Background(), id, "order-1", decimal.NewFromInt(100), decimal.NewFromInt(1))
			return err
		}},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			var calls atomic.Int32
			httpClient := &http.Client{Transport: bitgetRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if req.URL.Path != command.path {
					t.Errorf("request path=%q, want %q", req.URL.Path, command.path)
				}
				return nil, io.EOF
			})}
			exec := newExecutionClient(bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL("https://bitget.test").WithHTTPClient(httpClient), provider, clock.NewRealClock())

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

func TestBitgetExecutionLeavesIdentityMismatchAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"orderId": "wrong", "clientOid": "other"}})
	}))
	t.Cleanup(server.Close)
	provider := bitgetTestProvider()
	exec := newExecutionClient(bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock())
	id := provider.All()[0].ID
	commands := []struct {
		name string
		run  func() error
	}{
		{name: "submit", run: func() error {
			_, err := exec.Submit(context.Background(), validBitgetValidationRequest(id))
			return err
		}},
		{name: "cancel", run: func() error { return exec.Cancel(context.Background(), id, "order-1") }},
		{name: "modify", run: func() error {
			_, err := exec.Modify(context.Background(), id, "order-1", decimal.NewFromInt(100), decimal.NewFromInt(1))
			return err
		}},
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
