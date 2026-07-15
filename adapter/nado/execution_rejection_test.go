package nado

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestNadoCancelCommandOutcomeMatrixUsesVenueRejectedOnlyForCode2001(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		statusCode int
		transport  bool
		definitive bool
	}{
		{name: "inactive product 2001", body: `{"status":"failure","error_code":2001,"error":"product is not active","request_type":"execute_cancel_orders"}`, definitive: true},
		{name: "unknown code 2500", body: `{"status":"failure","error_code":2500,"error":"unproven application failure","request_type":"execute_cancel_orders"}`},
		{name: "http 5xx", statusCode: http.StatusInternalServerError, body: `{"status":"failure","error_code":2001,"error":"internal error","request_type":"execute_cancel_orders"}`},
		{name: "transport", transport: true},
		{name: "malformed", body: `{not-json`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/query"):
					_, _ = w.Write([]byte(`{"status":"success","data":{"chain_id":"763373","endpoint_addr":"0x4444444444444444444444444444444444444444"},"request_type":"query_contracts"}`))
				case strings.HasSuffix(r.URL.Path, "/execute") && test.transport:
					hijacker, ok := w.(http.Hijacker)
					if !ok {
						http.Error(w, "hijacking unavailable", http.StatusInternalServerError)
						return
					}
					connection, _, err := hijacker.Hijack()
					if err == nil {
						_ = connection.Close()
					}
				case strings.HasSuffix(r.URL.Path, "/execute"):
					if test.statusCode != 0 {
						w.WriteHeader(test.statusCode)
					}
					_, _ = w.Write([]byte(test.body))
				default:
					http.Error(w, "unexpected path", http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)

			rest := nadoTestRESTClient(t, server)
			rest, err := rest.WithCredentials(strings.Repeat("1", 64), "arb")
			if err != nil {
				t.Fatal(err)
			}
			id := model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT0", Kind: enums.KindSpot}
			exec := newExecutionClient(rest, nadoTestProvider(), clock.NewRealClock(), enums.KindSpot, AccountIDUnified)
			err = exec.Cancel(context.Background(), id, "0x"+strings.Repeat("a", 64))
			if err == nil {
				t.Fatal("Cancel unexpectedly succeeded")
			}
			if got := errors.Is(err, contract.ErrVenueRejected); got != test.definitive {
				t.Fatalf("err=%v venueRejected=%v, want %v", err, got, test.definitive)
			}
		})
	}
}

func TestNadoCancelRequiresOneExactAuthoritativeResponse(t *testing.T) {
	digest := "0x" + strings.Repeat("a", 64)
	tests := []struct {
		name          string
		cancelledRows string
		wantErr       bool
	}{
		{name: "one exact row", cancelledRows: `[{"product_id":1,"digest":"0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}]`},
		{name: "empty rows", cancelledRows: `[]`, wantErr: true},
		{name: "multiple rows", cancelledRows: `[{"product_id":1,"digest":"` + digest + `"},{"product_id":1,"digest":"` + digest + `"}]`, wantErr: true},
		{name: "mismatched product", cancelledRows: `[{"product_id":2,"digest":"` + digest + `"}]`, wantErr: true},
		{name: "empty digest", cancelledRows: `[{"product_id":1,"digest":""}]`, wantErr: true},
		{name: "mismatched digest", cancelledRows: `[{"product_id":1,"digest":"0x` + strings.Repeat("b", 64) + `"}]`, wantErr: true},
		{name: "padded matching digest", cancelledRows: `[{"product_id":1,"digest":" ` + digest + ` "}]`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/query"):
					_, _ = w.Write([]byte(`{"status":"success","data":{"chain_id":"763373","endpoint_addr":"0x4444444444444444444444444444444444444444"},"request_type":"query_contracts"}`))
				case strings.HasSuffix(r.URL.Path, "/execute"):
					_, _ = w.Write([]byte(`{"status":"success","data":{"cancelled_orders":` + test.cancelledRows + `},"request_type":"execute_cancel_orders"}`))
				default:
					http.Error(w, "unexpected path", http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)

			rest := nadoTestRESTClient(t, server)
			rest, err := rest.WithCredentials(strings.Repeat("1", 64), "arb")
			if err != nil {
				t.Fatal(err)
			}
			clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC))
			id := model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT0", Kind: enums.KindSpot}
			exec := newExecutionClient(rest, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
			if err := exec.correlations.remember(nadoOrderCorrelation{
				accountID: AccountIDUnified, instrumentID: id, clientID: "cancel-client", venueOrderID: digest,
				request: model.OrderRequest{AccountID: AccountIDUnified, InstrumentID: id, ClientID: "cancel-client"},
			}, clk.Now()); err != nil {
				t.Fatalf("seed correlation: %v", err)
			}

			err = exec.Cancel(context.Background(), id, digest)
			if (err != nil) != test.wantErr {
				t.Fatalf("Cancel err=%v, wantErr=%v", err, test.wantErr)
			}
			if errors.Is(err, contract.ErrVenueRejected) {
				t.Fatalf("HTTP-200 response validation err=%v must remain ambiguous", err)
			}
			correlation, ok := exec.correlations.byVenueOrderID(AccountIDUnified, id, digest, clk.Now())
			if !ok {
				t.Fatal("seeded correlation disappeared")
			}
			wantTerminal := enums.StatusUnknown
			if !test.wantErr {
				wantTerminal = enums.StatusCanceled
			}
			if correlation.terminalStatus != wantTerminal {
				t.Fatalf("terminal status=%s, want %s", correlation.terminalStatus, wantTerminal)
			}
		})
	}
}

func TestNadoModifyIsExplicitlyUnsupported(t *testing.T) {
	id := model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT0", Kind: enums.KindSpot}
	exec := newExecutionClient(nil, nadoTestProvider(), clock.NewRealClock(), enums.KindSpot, AccountIDUnified)
	order, err := exec.Modify(context.Background(), id, "0x"+strings.Repeat("a", 64), decimal.RequireFromString("1.1"), decimal.NewFromInt(1))
	if order != nil || !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("Modify order=%+v err=%v, want ErrNotSupported", order, err)
	}
}
