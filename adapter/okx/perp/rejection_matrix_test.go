package perp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

func TestOKXPerpSupportedCommandsMapSCodeRejection(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	for _, command := range []string{"submit", "cancel", "modify"} {
		t.Run(command, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"555","clOrdId":"client","sCode":"51008","sMsg":"insufficient balance"}]}`))
			}))
			defer server.Close()
			exec := newExecutionClient(okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL), testOKXProvider(inst), clock.NewRealClock(), defaultDerivativeTdMode)
			var err error
			switch command {
			case "submit":
				_, err = exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst.ID, ClientID: "client", Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC, Quantity: dd("1"), Price: dd("100"), PositionSide: enums.PosNet})
			case "cancel":
				err = exec.Cancel(context.Background(), inst.ID, "555")
			case "modify":
				_, err = exec.Modify(context.Background(), inst.ID, "555", dd("101"), dd("1"))
			}
			if !errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("err=%v, want ErrVenueRejected", err)
			}
		})
	}
}

func TestOKXPerpCommandAmbiguityPreservesTransportDeadlineAndEnvelopes(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	for _, command := range []string{"submit", "cancel", "modify"} {
		for _, outcome := range []struct {
			name      string
			status    int
			body      string
			transport error
		}{
			{name: "transport", transport: io.ErrUnexpectedEOF},
			{name: "deadline", transport: context.DeadlineExceeded},
			{name: "server", status: http.StatusInternalServerError, body: `{"code":"50000","msg":"internal","data":[]}`},
			{name: "malformed", status: http.StatusOK, body: `not-json`},
		} {
			t.Run(command+"/"+outcome.name, func(t *testing.T) {
				rest := okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL("https://okx.invalid")
				rest.WithHTTPClient(&http.Client{Transport: okxPerpRoundTripFunc(func(*http.Request) (*http.Response, error) {
					if outcome.transport != nil {
						return nil, outcome.transport
					}
					return &http.Response{StatusCode: outcome.status, Body: io.NopCloser(strings.NewReader(outcome.body)), Header: make(http.Header)}, nil
				})})
				exec := newExecutionClient(rest, testOKXProvider(inst), clock.NewRealClock(), defaultDerivativeTdMode)
				err := invokeOKXPerpCommand(exec, command, inst.ID)
				if err == nil || errors.Is(err, contract.ErrVenueRejected) {
					t.Fatalf("err=%v, want ambiguous non-venue-rejection", err)
				}
				if outcome.transport != nil && !errors.Is(err, outcome.transport) {
					t.Fatalf("err=%v, want preserved cause %v", err, outcome.transport)
				}
			})
		}
	}
}

func invokeOKXPerpCommand(exec *executionClient, command string, id model.InstrumentID) error {
	switch command {
	case "submit":
		_, err := exec.Submit(context.Background(), model.OrderRequest{InstrumentID: id, ClientID: "client", Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC, Quantity: dd("1"), Price: dd("100"), PositionSide: enums.PosNet})
		return err
	case "cancel":
		return exec.Cancel(context.Background(), id, "555")
	default:
		_, err := exec.Modify(context.Background(), id, "555", dd("101"), dd("1"))
		return err
	}
}

type okxPerpRoundTripFunc func(*http.Request) (*http.Response, error)

func (f okxPerpRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestOKXPerpSingleCommandIdentityAnomaliesRemainAmbiguous(t *testing.T) {
	inst := testOKXLinearInstrument(t)
	for _, command := range []string{"submit", "cancel", "modify"} {
		t.Run(command, func(t *testing.T) {
			body := `{"code":"0","msg":"","data":[{"ordId":"555","clOrdId":"client","sCode":"0"}]}`
			switch command {
			case "submit":
				body = `{"code":"0","msg":"","data":[{"ordId":"555","clOrdId":"wrong","sCode":"0"},{"ordId":"556","clOrdId":"client","sCode":"0"}]}`
			case "cancel":
				body = `{"code":"0","msg":"","data":[{"ordId":"other","sCode":"0"}]}`
			case "modify":
				body = `{"code":"0","msg":"","data":[]}`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			exec := newExecutionClient(okx.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL), testOKXProvider(inst), clock.NewRealClock(), defaultDerivativeTdMode)
			var err error
			switch command {
			case "submit":
				_, err = exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst.ID, ClientID: "client", Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC, Quantity: dd("1"), Price: dd("100"), PositionSide: enums.PosNet})
			case "cancel":
				err = exec.Cancel(context.Background(), inst.ID, "555")
			case "modify":
				_, err = exec.Modify(context.Background(), inst.ID, "555", dd("101"), dd("1"))
			}
			if err == nil || errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("err=%v, want ambiguous identity/envelope error", err)
			}
		})
	}
}
