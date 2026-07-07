package bitget

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

func TestBitgetOrderWSEventDoesNotCarryIncrementalFillQuantity(t *testing.T) {
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}
	events := execEventsFromOrderMessage(&bitgetsdk.WSOrderMessage{
		Data: []bitgetsdk.OrderRecord{{
			OrderID:     "order-1",
			ClientOID:   "client-1",
			Symbol:      "BTCUSDT",
			Side:        "buy",
			OrderType:   "limit",
			Qty:         "0.001",
			FilledQty:   "0.001",
			AvgPrice:    "50000",
			OrderStatus: "filled",
		}},
	}, func(string) model.InstrumentID { return id }, AccountIDUnified)

	if len(events) != 1 {
		t.Fatalf("events len=%d", len(events))
	}
	orderEvent, ok := events[0].(contract.OrderEvent)
	if !ok {
		t.Fatalf("event type=%T, want OrderEvent", events[0])
	}
	if !orderEvent.Order.FilledQty.Equal(decimal.Zero) || !orderEvent.Order.AvgFillPrice.Equal(decimal.Zero) {
		t.Fatalf("order WS must not carry fill increments into runtime: %+v", orderEvent.Order)
	}
	if orderEvent.Order.Status != enums.StatusFilled {
		t.Fatalf("order status=%s, want filled", orderEvent.Order.Status)
	}
	if orderEvent.Order.Request.AccountID != AccountIDUnified {
		t.Fatalf("order account_id=%q", orderEvent.Order.Request.AccountID)
	}
}

func TestBitgetFillWSEventCarriesIncrementalFillQuantity(t *testing.T) {
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}
	events := execEventsFromFillMessage(&bitgetsdk.WSFillMessage{
		Data: []bitgetsdk.FillRecord{{
			OrderID:   "order-1",
			ClientOID: "client-1",
			ExecID:    "fill-1",
			Symbol:    "BTCUSDT",
			Side:      "buy",
			ExecPrice: "50000",
			ExecQty:   "0.001",
		}},
	}, func(string) model.InstrumentID { return id }, AccountIDUnified)

	if len(events) != 1 {
		t.Fatalf("events len=%d", len(events))
	}
	fillEvent, ok := events[0].(contract.FillEvent)
	if !ok {
		t.Fatalf("event type=%T, want FillEvent", events[0])
	}
	if !fillEvent.Fill.Quantity.Equal(decimal.RequireFromString("0.001")) || fillEvent.Fill.TradeID != "fill-1" {
		t.Fatalf("unexpected fill event: %+v", fillEvent.Fill)
	}
	if fillEvent.Fill.AccountID != AccountIDUnified {
		t.Fatalf("fill account_id=%q", fillEvent.Fill.AccountID)
	}
}
