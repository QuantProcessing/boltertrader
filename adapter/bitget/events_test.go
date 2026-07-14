package bitget

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

func TestBitgetOrderWSEventDoesNotCarryIncrementalFillQuantity(t *testing.T) {
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}
	events, err := execEventsFromOrderMessage(&bitgetsdk.WSOrderMessage{
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
			HoldMode:    "one_way_mode",
			HoldSide:    "long",
		}},
	}, func(category, symbol string) (model.InstrumentID, bool) { return id, true }, AccountIDUnified)
	if err != nil {
		t.Fatalf("execEventsFromOrderMessage: %v", err)
	}

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
	}, func(category, symbol string) (model.InstrumentID, bool) { return id, true }, AccountIDUnified)

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

func TestBitgetPrivateWSEventsRouteSameSymbolByNormalizedCategory(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
	})

	orders, err := execEventsFromOrderMessage(&bitgetsdk.WSOrderMessage{Data: []bitgetsdk.OrderRecord{
		{OrderID: "spot-order", Category: "spot", Symbol: "BTCUSDT", Side: "buy", OrderType: "limit", Qty: "1", OrderStatus: "new"},
		{OrderID: "perp-order", Category: "usdt-futures", Symbol: "BTCUSDT", Side: "buy", OrderType: "limit", Qty: "1", OrderStatus: "new", HoldMode: "one_way_mode", HoldSide: "long"},
	}}, provider.ResolveVenueCategorySymbol, AccountIDUnified)
	if err != nil {
		t.Fatalf("order events: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("order events len=%d, want 2", len(orders))
	}
	spotOrder := orders[0].(contract.OrderEvent).Order
	perpOrder := orders[1].(contract.OrderEvent).Order
	if spotOrder.Request.InstrumentID.Kind != enums.KindSpot || perpOrder.Request.InstrumentID.Kind != enums.KindPerp {
		t.Fatalf("same-symbol order routing spot=%+v perp=%+v", spotOrder.Request.InstrumentID, perpOrder.Request.InstrumentID)
	}

	fills := execEventsFromFillMessage(&bitgetsdk.WSFillMessage{Data: []bitgetsdk.FillRecord{
		{ExecID: "spot-fill", Category: "spot", Symbol: "BTCUSDT", Side: "buy", ExecPrice: "50000", ExecQty: "0.1"},
		{ExecID: "perp-fill", Category: "usdt-futures", Symbol: "BTCUSDT", Side: "buy", ExecPrice: "50000", ExecQty: "0.1"},
	}}, provider.ResolveVenueCategorySymbol, AccountIDUnified)
	if len(fills) != 2 {
		t.Fatalf("fill events len=%d, want 2", len(fills))
	}
	spotFill := fills[0].(contract.FillEvent).Fill
	perpFill := fills[1].(contract.FillEvent).Fill
	if spotFill.InstrumentID.Kind != enums.KindSpot || perpFill.InstrumentID.Kind != enums.KindPerp {
		t.Fatalf("same-symbol fill routing spot=%+v perp=%+v", spotFill.InstrumentID, perpFill.InstrumentID)
	}

	positions, err := accountEventsFromPositionMessage(&bitgetsdk.WSPositionMessage{Data: []bitgetsdk.PositionRecord{
		{Category: "spot", Symbol: "BTCUSDT", Qty: "0.1", PosSide: "long"},
		{Category: "usdt-futures", Symbol: "BTCUSDT", Qty: "0.2", PosSide: "long", HoldMode: "one_way_mode"},
	}}, provider.ResolveVenueCategorySymbol, AccountIDUnified, time.Unix(1, 0))
	if err != nil {
		t.Fatalf("position events: %v", err)
	}
	if len(positions) != 2 {
		t.Fatalf("position events len=%d, want 2", len(positions))
	}
	spotPosition := positions[0].(contract.PositionEvent).Position
	perpPosition := positions[1].(contract.PositionEvent).Position
	if spotPosition.InstrumentID.Kind != enums.KindSpot || perpPosition.InstrumentID.Kind != enums.KindPerp {
		t.Fatalf("same-symbol position routing spot=%+v perp=%+v", spotPosition.InstrumentID, perpPosition.InstrumentID)
	}
}

func TestBitgetPrivateWSEventsSkipUnknownOrOutOfScopeIdentity(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
	})

	orders, err := execEventsFromOrderMessage(&bitgetsdk.WSOrderMessage{Data: []bitgetsdk.OrderRecord{
		{OrderID: "valid", Category: "spot", Symbol: "BTCUSDT", Qty: "1"},
		{OrderID: "out-of-scope", Category: "usdt-futures", Symbol: "BTCUSDT", Qty: "1"},
		{OrderID: "unknown-category", Category: "coin-futures", Symbol: "BTCUSDT", Qty: "1"},
		{OrderID: "missing-category", Symbol: "BTCUSDT", Qty: "1"},
		{OrderID: "unknown-symbol", Category: "spot", Symbol: "ETHUSDT", Qty: "1"},
	}}, provider.ResolveVenueCategorySymbol, AccountIDUnified)
	if err != nil {
		t.Fatalf("order events: %v", err)
	}
	if len(orders) != 1 || orders[0].(contract.OrderEvent).Order.VenueOrderID != "valid" {
		t.Fatalf("unexpected order events: %+v", orders)
	}

	fills := execEventsFromFillMessage(&bitgetsdk.WSFillMessage{Data: []bitgetsdk.FillRecord{
		{ExecID: "valid", Category: "spot", Symbol: "BTCUSDT", ExecQty: "1"},
		{ExecID: "out-of-scope", Category: "usdt-futures", Symbol: "BTCUSDT", ExecQty: "1"},
		{ExecID: "unknown-symbol", Category: "spot", Symbol: "ETHUSDT", ExecQty: "1"},
	}}, provider.ResolveVenueCategorySymbol, AccountIDUnified)
	if len(fills) != 1 || fills[0].(contract.FillEvent).Fill.TradeID != "valid" {
		t.Fatalf("unexpected fill events: %+v", fills)
	}

	positions, err := accountEventsFromPositionMessage(&bitgetsdk.WSPositionMessage{Data: []bitgetsdk.PositionRecord{
		{Category: "spot", Symbol: "BTCUSDT", Qty: "1"},
		{Category: "usdt-futures", Symbol: "BTCUSDT", Qty: "1"},
		{Category: "spot", Symbol: "ETHUSDT", Qty: "1"},
	}}, provider.ResolveVenueCategorySymbol, AccountIDUnified, time.Unix(1, 0))
	if err != nil {
		t.Fatalf("position events: %v", err)
	}
	if len(positions) != 1 {
		t.Fatalf("unexpected position events: %+v", positions)
	}
}

type discardReconnectHooks struct {
	started   func(error)
	recovered func()
}

func (h *discardReconnectHooks) SetReconnectHooks(started func(error), recovered func()) {
	h.started = started
	h.recovered = recovered
}

func TestBitgetConfiguredCategoryUnknownSymbolEmitsReconcileGap(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
	})
	exec := newExecutionClient(nil, provider, nil, AccountIDUnified)
	defer exec.Close()
	adapter := &Adapter{provider: provider, exec: exec}
	adapter.bindPrivateGapHooks(&discardReconnectHooks{})

	if id, ok := adapter.resolvePrivateInstrument("spot", "btcusdt"); !ok || id.Kind != enums.KindSpot {
		t.Fatalf("known in-scope instrument resolve=%+v ok=%v", id, ok)
	}
	select {
	case event := <-exec.Events():
		t.Fatalf("known instrument unexpectedly emitted %T", event.Payload)
	default:
	}

	if id, ok := adapter.resolvePrivateInstrument(" spot ", " missingusdt "); ok || id != (model.InstrumentID{}) {
		t.Fatalf("unknown in-scope instrument resolve=%+v ok=%v, want fail closed", id, ok)
	}
	for i, wantPhase := range []contract.StreamGapPhase{contract.StreamGapStarted, contract.StreamGapRecovered} {
		envelope := <-exec.Events()
		gap, ok := envelope.Payload.(contract.StreamGapEvent)
		if !ok {
			t.Fatalf("gap[%d] payload=%T", i, envelope.Payload)
		}
		if gap.Phase != wantPhase || gap.Generation != 1 || gap.Reason != "unresolved private instrument category=SPOT symbol=MISSINGUSDT" {
			t.Fatalf("gap[%d]=%+v, want phase=%s generation=1 and sanitized identity reason", i, gap, wantPhase)
		}
	}

	// A supported category outside the configured provider scope, and an
	// unsupported UTA category, are intentionally ignored without forcing a
	// reconciliation for state this adapter does not own.
	for _, category := range []string{"usdt-futures", "coin-futures", "margin"} {
		if _, ok := adapter.resolvePrivateInstrument(category, "BTCUSDT"); ok {
			t.Fatalf("out-of-scope category %q unexpectedly resolved", category)
		}
	}
	select {
	case event := <-exec.Events():
		t.Fatalf("out-of-scope identity unexpectedly emitted %T: %+v", event.Payload, event.Payload)
	default:
	}

	if _, ok := adapter.resolvePrivateInstrument(" unexpected ", "BTCUSDT"); ok {
		t.Fatal("unknown category unexpectedly resolved")
	}
	for i, wantPhase := range []contract.StreamGapPhase{contract.StreamGapStarted, contract.StreamGapRecovered} {
		envelope := <-exec.Events()
		gap, ok := envelope.Payload.(contract.StreamGapEvent)
		if !ok {
			t.Fatalf("unknown-category gap[%d] payload=%T", i, envelope.Payload)
		}
		if gap.Phase != wantPhase || gap.Generation != 2 || gap.Reason != "unresolved private category=UNEXPECTED symbol=BTCUSDT" {
			t.Fatalf("unknown-category gap[%d]=%+v, want phase=%s generation=2", i, gap, wantPhase)
		}
	}
}

func TestBitgetUnknownSymbolDoesNotPrematurelyRecoverActiveSocketGap(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
	})
	exec := newExecutionClient(nil, provider, nil, AccountIDUnified)
	defer exec.Close()
	adapter := &Adapter{provider: provider, exec: exec}
	hooks := &discardReconnectHooks{}
	adapter.bindPrivateGapHooks(hooks)

	hooks.started(errors.New("socket closed"))
	started := (<-exec.Events()).Payload.(contract.StreamGapEvent)
	if started.Phase != contract.StreamGapStarted || started.Generation != 1 {
		t.Fatalf("socket started gap=%+v", started)
	}
	if _, ok := adapter.resolvePrivateInstrument("spot", "MISSINGUSDT"); ok {
		t.Fatal("unknown symbol unexpectedly resolved")
	}
	select {
	case event := <-exec.Events():
		t.Fatalf("unknown symbol prematurely changed active socket gap: %+v", event.Payload)
	default:
	}

	hooks.recovered()
	recovered := (<-exec.Events()).Payload.(contract.StreamGapEvent)
	if recovered.Phase != contract.StreamGapRecovered || recovered.Generation != started.Generation {
		t.Fatalf("socket recovered gap=%+v, started=%+v", recovered, started)
	}
}
