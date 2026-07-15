package bybit

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
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
	"github.com/shopspring/decimal"
)

type bybitRoundTripFunc func(*http.Request) (*http.Response, error)

func (f bybitRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBybitExecutionMapsTypedCommandResponseRejections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, map[string]any{"retCode": 110001, "retMsg": "order does not exist", "result": map[string]any{}})
	}))
	t.Cleanup(server.Close)
	provider := bybitTestProvider()
	exec := newExecutionClient(bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock())
	id := provider.All()[0].ID
	req := validBybitValidationRequest(id)

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
			var responseErr *bybitsdk.ResponseError
			if !errors.As(err, &responseErr) || responseErr.Code != 110001 {
				t.Fatalf("mapped error lost typed Bybit response: %v", err)
			}
		})
	}
}

func TestBybitExecutionLeavesHTTPAndMalformedFailuresAmbiguous(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http 5xx", status: http.StatusServiceUnavailable, body: `{"retCode":10016,"retMsg":"server error"}`},
		{name: "malformed success", status: http.StatusOK, body: `{`},
		{name: "partial success", status: http.StatusOK, body: `{"retCode":0,"retMsg":"OK"}`},
		{name: "typed temporary response", status: http.StatusOK, body: `{"retCode":10016,"retMsg":"server error","result":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			provider := bybitTestProvider()
			exec := newExecutionClient(bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock())
			id := provider.All()[0].ID
			commands := []struct {
				name string
				run  func() error
			}{
				{name: "submit", run: func() error { _, err := exec.Submit(context.Background(), validBybitValidationRequest(id)); return err }},
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

func TestBybitExecutionLeavesContextCancellationAmbiguous(t *testing.T) {
	provider := bybitTestProvider()
	exec := newExecutionClient(bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL("http://127.0.0.1:1"), provider, clock.NewRealClock())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := exec.Submit(ctx, validBybitValidationRequest(provider.All()[0].ID))
	if !errors.Is(err, context.Canceled) || errors.Is(err, contract.ErrVenueRejected) {
		t.Fatalf("Submit error=%v, want ambiguous context cancellation", err)
	}
}

func TestBybitExecutionLeavesAfterHandoffTransportEOFAmbiguousForEverySupportedCommand(t *testing.T) {
	provider := bybitTestProvider()
	id := provider.All()[0].ID
	commands := []struct {
		name string
		path string
		run  func(*executionClient) error
	}{
		{name: "submit", path: "/v5/order/create", run: func(exec *executionClient) error {
			_, err := exec.Submit(context.Background(), validBybitValidationRequest(id))
			return err
		}},
		{name: "cancel", path: "/v5/order/cancel", run: func(exec *executionClient) error {
			return exec.Cancel(context.Background(), id, "order-1")
		}},
		{name: "modify", path: "/v5/order/amend", run: func(exec *executionClient) error {
			_, err := exec.Modify(context.Background(), id, "order-1", decimal.NewFromInt(100), decimal.NewFromInt(1))
			return err
		}},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			var calls atomic.Int32
			httpClient := &http.Client{Transport: bybitRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if req.URL.Path != command.path {
					t.Errorf("request path=%q, want %q", req.URL.Path, command.path)
				}
				return nil, io.EOF
			})}
			exec := newExecutionClient(bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL("https://bybit.test").WithHTTPClient(httpClient), provider, clock.NewRealClock())

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

func TestBybitExecutionLeavesIdentityMismatchAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"orderId": "wrong", "orderLinkId": "other"}})
	}))
	t.Cleanup(server.Close)
	provider := bybitTestProvider()
	exec := newExecutionClient(bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock())
	id := provider.All()[0].ID
	commands := []struct {
		name string
		run  func() error
	}{
		{name: "submit", run: func() error { _, err := exec.Submit(context.Background(), validBybitValidationRequest(id)); return err }},
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
