package lighter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

func TestLighterSupportedCommandsMapStructuredRejectedResult(t *testing.T) {
	inst, _ := lighterCoverageInstruments()
	for _, command := range []string{"submit", "cancel", "modify"} {
		t.Run(command, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/nextNonce":
					_, _ = w.Write([]byte(`{"code":200,"nonce":123}`))
				case "/api/v1/sendTx":
					_, _ = w.Write([]byte(`{"code":400,"message":"insufficient margin","tx_hash":""}`))
				default:
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
			}))
			defer server.Close()
			exec := lighterRejectionClient(server.URL, inst)

			var order *model.Order
			var err error
			switch command {
			case "submit":
				order, err = exec.Submit(context.Background(), lighterRejectionRequest(inst.ID))
			case "cancel":
				err = exec.Cancel(context.Background(), inst.ID, "123")
			case "modify":
				_, err = exec.Modify(context.Background(), inst.ID, "123", decimal.NewFromInt(101), decimal.NewFromInt(1))
			}
			if !errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("err=%v, want ErrVenueRejected", err)
			}
			if command == "submit" && (order == nil || order.Status != enums.StatusRejected || order.RejectReason != "insufficient margin") {
				t.Fatalf("rejected order=%+v", order)
			}
		})
	}
}

func TestLighterRejectedClassificationPreservesMalformedAndServerAmbiguity(t *testing.T) {
	inst, _ := lighterCoverageInstruments()
	for _, command := range []string{"submit", "cancel", "modify"} {
		for _, tc := range []struct {
			name         string
			status       int
			body         string
			transport    error
			missingTxAck bool
		}{
			{name: "server failure", status: http.StatusInternalServerError, body: `{"code":500,"message":"internal"}`},
			{name: "unknown successful-http code", status: http.StatusOK, body: `{"code":451,"message":"future terminal state"}`},
			{name: "server code in successful-http envelope", status: http.StatusOK, body: `{"code":599,"message":"upstream uncertain"}`},
			{name: "empty known code envelope", status: http.StatusOK, body: `{"code":400,"message":""}`},
			{name: "empty success envelope", status: http.StatusOK, body: `{}`, missingTxAck: true},
			{name: "success code missing transaction hash", status: http.StatusOK, body: `{"code":200,"message":"success"}`, missingTxAck: true},
			{name: "malformed response", status: http.StatusOK, body: `not-json`},
			{name: "transport failure", transport: io.ErrUnexpectedEOF},
			{name: "deadline", transport: context.DeadlineExceeded},
		} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				activeOrderCalls := 0
				nextNonceCalls := 0
				sendTxCalls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v1/nextNonce":
						_, _ = w.Write([]byte(`{"code":200,"nonce":123}`))
					case "/api/v1/sendTx":
						w.WriteHeader(tc.status)
						_, _ = w.Write([]byte(tc.body))
					case "/api/v1/accountActiveOrders":
						activeOrderCalls++
						_, _ = fmt.Fprintf(w, `{"code":200,"orders":[{"order_index":123,"client_order_index":%d,"market_index":%d,"initial_base_amount":"1","remaining_base_amount":"1","price":"100","status":"open","side":"buy"}]}`, clientOrderIndex("reject"), *inst.AssetIndex)
					}
				}))
				defer server.Close()
				exec := lighterRejectionClient(server.URL, inst)
				if tc.transport != nil {
					exec.rest.HTTPClient = &http.Client{Transport: lighterRoundTripFunc(func(r *http.Request) (*http.Response, error) {
						switch r.URL.Path {
						case "/api/v1/nextNonce":
							nextNonceCalls++
							return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":200,"nonce":123}`)), Header: make(http.Header)}, nil
						case "/api/v1/sendTx":
							sendTxCalls++
							return nil, tc.transport
						default:
							return nil, fmt.Errorf("unexpected path %s", r.URL.Path)
						}
					})}
				}
				err := invokeLighterCommand(exec, command, inst.ID)
				if err == nil || errors.Is(err, contract.ErrVenueRejected) {
					t.Fatalf("err=%v, want ambiguous non-venue-rejection", err)
				}
				if tc.transport != nil && !errors.Is(err, tc.transport) {
					t.Fatalf("err=%v, want preserved cause %v", err, tc.transport)
				}
				if tc.transport != nil && (nextNonceCalls != 1 || sendTxCalls != 1) {
					t.Fatalf("nextNonce calls=%d sendTx calls=%d, want one successful nonce then one failed handoff", nextNonceCalls, sendTxCalls)
				}
				if tc.missingTxAck && activeOrderCalls != 0 {
					t.Fatalf("active-order calls=%d, want zero before missing tx_hash rejection", activeOrderCalls)
				}
			})
		}
	}
}

func invokeLighterCommand(exec *executionClient, command string, id model.InstrumentID) error {
	switch command {
	case "submit":
		_, err := exec.Submit(context.Background(), lighterRejectionRequest(id))
		return err
	case "cancel":
		return exec.Cancel(context.Background(), id, "123")
	default:
		_, err := exec.Modify(context.Background(), id, "123", decimal.NewFromInt(101), decimal.NewFromInt(1))
		return err
	}
}

type lighterRoundTripFunc func(*http.Request) (*http.Response, error)

func (f lighterRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func lighterRejectionClient(baseURL string, inst *model.Instrument) *executionClient {
	rest := sdk.NewClient().WithEnvironment(sdk.EnvironmentTestnet).WithCredentials(testLighterPrivateKey(), 66, 7)
	rest.BaseURL = baseURL
	return newExecutionClient(rest, newRegistry([]*model.Instrument{inst}), clock.NewSimulatedClock(time.Unix(100, 0)), 66)
}

func lighterRejectionRequest(id model.InstrumentID) model.OrderRequest {
	return model.OrderRequest{
		AccountID: AccountIDDefault, InstrumentID: id, ClientID: "reject",
		Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC,
		Quantity: decimal.NewFromInt(1), Price: decimal.NewFromInt(100), PositionSide: enums.PosNet,
	}
}
