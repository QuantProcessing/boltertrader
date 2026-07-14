package gate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

func TestGateFuturesOrderPositionSideUsesModeAndReduceSemantics(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		order gatesdk.FuturesOrder
		want  enums.PositionSide
	}{
		{name: "single buy", mode: "single", order: gatesdk.FuturesOrder{Size: 1}, want: enums.PosNet},
		{name: "single sell", mode: "single", order: gatesdk.FuturesOrder{Size: -1}, want: enums.PosNet},
		{name: "dual open long", mode: "dual", order: gatesdk.FuturesOrder{Size: 1}, want: enums.PosLong},
		{name: "dual open short", mode: "dual", order: gatesdk.FuturesOrder{Size: -1}, want: enums.PosShort},
		{name: "dual close short", mode: "dual", order: gatesdk.FuturesOrder{Size: 1, ReduceOnly: true}, want: enums.PosShort},
		{name: "dual close long", mode: "dual", order: gatesdk.FuturesOrder{Size: -1, IsReduceOnly: true}, want: enums.PosLong},
		{name: "dual auto close long", mode: "dual", order: gatesdk.FuturesOrder{AutoSize: "close_long"}, want: enums.PosLong},
		{name: "dual auto close short", mode: "dual", order: gatesdk.FuturesOrder{AutoSize: "close_short"}, want: enums.PosShort},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := positionSideFromGateOrder(test.order, test.mode)
			if !ok || got != test.want {
				t.Fatalf("position side=%s, want %s", got, test.want)
			}
		})
	}
	if _, ok := positionSideFromGateOrder(gatesdk.FuturesOrder{Size: 1}, ""); ok {
		t.Fatal("unknown account mode unexpectedly resolved")
	}
}

func TestGateFuturesPositionModeNormalizesQuantityAndFailsClosed(t *testing.T) {
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}
	resolve := func(string) model.InstrumentID { return id }
	tests := []struct {
		name      string
		record    gatesdk.Position
		wantSide  enums.PositionSide
		wantQty   string
		wantKnown bool
	}{
		{name: "single short", record: gatesdk.Position{Contract: "BTC_USDT", Size: -2, Mode: "single"}, wantSide: enums.PosNet, wantQty: "-2", wantKnown: true},
		{name: "dual long", record: gatesdk.Position{Contract: "BTC_USDT", Size: -2, Mode: "dual_long"}, wantSide: enums.PosLong, wantQty: "2", wantKnown: true},
		{name: "dual short", record: gatesdk.Position{Contract: "BTC_USDT", Size: 2, Mode: "dual_short"}, wantSide: enums.PosShort, wantQty: "-2", wantKnown: true},
		{name: "unknown", record: gatesdk.Position{Contract: "BTC_USDT", Size: 2, Mode: "mystery"}, wantKnown: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			position := positionFromGate(test.record, resolve, AccountIDUnified, time.Now())
			if !test.wantKnown {
				if position.InstrumentID.Symbol != "" {
					t.Fatalf("unknown mode position=%+v, want fail closed", position)
				}
				return
			}
			if position.Side != test.wantSide || position.Quantity.String() != test.wantQty {
				t.Fatalf("position=%+v, want side=%s qty=%s", position, test.wantSide, test.wantQty)
			}
		})
	}
}

func TestGateFuturesUnsupportedPositionModesFailClosed(t *testing.T) {
	state := newFuturesPositionModeState()
	for _, mode := range []string{"split", "dual_plus", "mystery"} {
		if err := state.setAccount(&gatesdk.FuturesAccount{PositionMode: mode}); err == nil {
			t.Fatalf("mode %q unexpectedly accepted", mode)
		}
	}
}

func TestGateFuturesSubmitPositionSideMatrix(t *testing.T) {
	client := newExecutionClient(nil, newInstrumentProvider(), nil)
	tests := []struct {
		name    string
		mode    string
		reqSide enums.PositionSide
		order   gatesdk.FuturesOrder
		wantErr bool
	}{
		{name: "single net", mode: "single", reqSide: enums.PosNet, order: gatesdk.FuturesOrder{Size: 1}},
		{name: "single rejects long", mode: "single", reqSide: enums.PosLong, order: gatesdk.FuturesOrder{Size: 1}, wantErr: true},
		{name: "dual open long", mode: "dual", reqSide: enums.PosLong, order: gatesdk.FuturesOrder{Size: 1}},
		{name: "dual open short", mode: "dual", reqSide: enums.PosShort, order: gatesdk.FuturesOrder{Size: -1}},
		{name: "dual close short", mode: "dual", reqSide: enums.PosShort, order: gatesdk.FuturesOrder{Size: 1, ReduceOnly: true}},
		{name: "dual close long", mode: "dual", reqSide: enums.PosLong, order: gatesdk.FuturesOrder{Size: -1, ReduceOnly: true}},
		{name: "dual rejects net", mode: "dual", reqSide: enums.PosNet, order: gatesdk.FuturesOrder{Size: 1}, wantErr: true},
		{name: "dual rejects wrong leg", mode: "dual", reqSide: enums.PosLong, order: gatesdk.FuturesOrder{Size: -1}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := client.futuresMode.setAccount(&gatesdk.FuturesAccount{PositionMode: test.mode}); err != nil {
				t.Fatal(err)
			}
			err := client.validateFuturesPositionSide(model.OrderRequest{Side: sideFromSignedSize(test.order.Size), PositionSide: test.reqSide, ReduceOnly: test.order.ReduceOnly}, test.order)
			if test.wantErr && !errors.Is(err, errs.ErrNotSupported) {
				t.Fatalf("err=%v, want ErrNotSupported", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGateFuturesSubmitRefreshesModeBeforeEveryWrite(t *testing.T) {
	var dual atomic.Bool
	var accountCalls atomic.Int32
	var orderCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/futures/usdt/accounts":
			accountCalls.Add(1)
			mode := "single"
			if dual.Load() {
				mode = "dual"
			}
			writeJSON(t, w, map[string]any{"user": 42, "position_mode": mode})
		case "/futures/usdt/orders":
			orderCalls.Add(1)
			writeJSON(t, w, map[string]any{"id": 123, "contract": "BTC_USDT", "size": 1, "status": "open"})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{{ID: id, VenueSymbol: "BTC_USDT", Settle: "USDT"}})
	client := newExecutionClient(gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, nil)
	req := model.OrderRequest{
		AccountID: AccountIDUnified, InstrumentID: id, ClientID: "mode-refresh", Side: enums.SideBuy,
		Type: enums.TypeLimit, TIF: enums.TifGTC, Quantity: decimal.NewFromInt(1), Price: decimal.NewFromInt(1), PositionSide: enums.PosNet,
	}
	if _, err := client.Submit(context.Background(), req); err != nil {
		t.Fatalf("single-mode Submit: %v", err)
	}
	dual.Store(true)
	if _, err := client.Submit(context.Background(), req); !errors.Is(err, errs.ErrNotSupported) {
		t.Fatalf("dual-mode stale NET Submit err=%v, want ErrNotSupported", err)
	}
	if accountCalls.Load() != 2 || orderCalls.Load() != 1 {
		t.Fatalf("account calls=%d order calls=%d, want refresh before both writes and block second order", accountCalls.Load(), orderCalls.Load())
	}
}
