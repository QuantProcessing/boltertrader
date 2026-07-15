package spot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

func TestOKXSpotSupportedCommandsMapSCodeRejection(t *testing.T) {
	inst := testSpotInstrument()
	for _, command := range []string{"submit", "cancel", "modify"} {
		t.Run(command, func(t *testing.T) {
			rest := testREST(func(*http.Request) (string, int) {
				return `{"code":"0","msg":"","data":[{"ordId":"555","clOrdId":"client","sCode":"51008","sMsg":"insufficient balance"}]}`, http.StatusOK
			})
			exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock(), defaultSpotTdMode)
			var err error
			switch command {
			case "submit":
				_, err = exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst.ID, ClientID: "client", Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC, Quantity: d("1"), Price: d("100"), PositionSide: enums.PosNet})
			case "cancel":
				err = exec.Cancel(context.Background(), inst.ID, "555")
			case "modify":
				_, err = exec.Modify(context.Background(), inst.ID, "555", d("101"), d("1"))
			}
			if !errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("err=%v, want ErrVenueRejected", err)
			}
		})
	}
}

func TestOKXSpotCommandAmbiguityPreservesTransportDeadlineAndEnvelopes(t *testing.T) {
	inst := testSpotInstrument()
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
				rest.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					if outcome.transport != nil {
						return nil, outcome.transport
					}
					return &http.Response{StatusCode: outcome.status, Body: io.NopCloser(strings.NewReader(outcome.body)), Header: make(http.Header)}, nil
				})})
				exec := newExecutionClient(rest, testProvider(inst), clock.NewRealClock(), defaultSpotTdMode)
				err := invokeOKXSpotCommand(exec, command, inst.ID)
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

func invokeOKXSpotCommand(exec *executionClient, command string, id model.InstrumentID) error {
	switch command {
	case "submit":
		_, err := exec.Submit(context.Background(), model.OrderRequest{InstrumentID: id, ClientID: "client", Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC, Quantity: d("1"), Price: d("100"), PositionSide: enums.PosNet})
		return err
	case "cancel":
		return exec.Cancel(context.Background(), id, "555")
	default:
		_, err := exec.Modify(context.Background(), id, "555", d("101"), d("1"))
		return err
	}
}

func TestOKXSpotSingleCommandIdentityAnomaliesRemainAmbiguous(t *testing.T) {
	inst := testSpotInstrument()
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
			exec := newExecutionClient(testREST(func(*http.Request) (string, int) { return body, http.StatusOK }), testProvider(inst), clock.NewRealClock(), defaultSpotTdMode)
			var err error
			switch command {
			case "submit":
				_, err = exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst.ID, ClientID: "client", Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC, Quantity: d("1"), Price: d("100"), PositionSide: enums.PosNet})
			case "cancel":
				err = exec.Cancel(context.Background(), inst.ID, "555")
			case "modify":
				_, err = exec.Modify(context.Background(), inst.ID, "555", d("101"), d("1"))
			}
			if err == nil || errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("err=%v, want ambiguous identity/envelope error", err)
			}
		})
	}
}
