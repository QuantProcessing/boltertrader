package bybit

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

func TestPrivateRecordResolverUsesCategoryForSharedVenueSymbol(t *testing.T) {
	provider := newInstrumentProvider()
	spotID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot}
	perpID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}
	provider.LoadSnapshot([]*model.Instrument{
		{ID: spotID, VenueSymbol: "BTCUSDT", Settle: bybitsdk.SettleCoinUSDT},
		{ID: perpID, VenueSymbol: "BTCUSDT", Settle: bybitsdk.SettleCoinUSDT},
	})

	tests := []struct {
		name     string
		category string
		symbol   string
		want     model.InstrumentID
		wantOK   bool
	}{
		{name: "spot", category: "spot", symbol: "BTCUSDT", want: spotID, wantOK: true},
		{name: "linear", category: "linear", symbol: "BTCUSDT", want: perpID, wantOK: true},
		{name: "inverse", category: "inverse", symbol: "BTCUSD", wantOK: false},
		{name: "option", category: "option", symbol: "BTC-26JUN26-50000-C", wantOK: false},
		{name: "dated linear", category: "linear", symbol: "BTCUSDT-26JUN26", wantOK: false},
		{name: "unknown category", category: "mystery", symbol: "BTCUSDT", wantOK: false},
		{name: "missing category", category: "", symbol: "BTCUSDT", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotOK := provider.resolvePrivateRecord(tt.category, tt.symbol)
			if gotOK != tt.wantOK || got != tt.want {
				t.Fatalf("resolvePrivateRecord(%q, %q)=(%+v, %v), want (%+v, %v)", tt.category, tt.symbol, got, gotOK, tt.want, tt.wantOK)
			}
		})
	}
}

func TestPrivateTradingEventsRejectUnsupportedCategories(t *testing.T) {
	provider := privateEventTestProvider()

	order, err := bybitsdk.DecodeOrderMessage([]byte(`{"topic":"order","data":[{"category":"inverse","symbol":"BTCUSD","orderId":"order-1"}]}`))
	if err != nil {
		t.Fatalf("DecodeOrderMessage: %v", err)
	}
	if events := execEventsFromOrderMessage(order, provider.resolvePrivateRecord, AccountIDUnified); len(events) != 0 {
		t.Fatalf("inverse order produced %d normalized events, want 0", len(events))
	}

	execution, err := bybitsdk.DecodeExecutionMessage([]byte(`{"topic":"execution","data":[{"category":"option","execType":"Trade","symbol":"BTC-26JUN26-50000-C","execQty":"1"}]}`))
	if err != nil {
		t.Fatalf("DecodeExecutionMessage: %v", err)
	}
	events, normalizeErr := execEventsFromExecutionMessage(execution, provider.resolvePrivateRecord, AccountIDUnified)
	if normalizeErr != nil {
		t.Fatalf("normalize option execution: %v", normalizeErr)
	}
	if len(events) != 0 {
		t.Fatalf("option execution produced %d normalized events, want 0", len(events))
	}

	position, err := bybitsdk.DecodePositionMessage([]byte(`{"topic":"position","data":[{"category":"linear","symbol":"BTCUSDT-26JUN26","side":"Buy","size":"1"}]}`))
	if err != nil {
		t.Fatalf("DecodePositionMessage: %v", err)
	}
	if events := accountEventsFromPositionMessage(position, provider.resolvePrivateRecord, AccountIDUnified, time.Unix(1, 0)); len(events) != 0 {
		t.Fatalf("dated linear position produced %d normalized events, want 0", len(events))
	}
}

func TestPrivateExecutionEventsOnlyNormalizeTrades(t *testing.T) {
	resolve := privateEventTestProvider().resolvePrivateRecord

	funding, err := bybitsdk.DecodeExecutionMessage([]byte(`{"topic":"execution","data":[{"category":"linear","execType":"Funding","symbol":"BTCUSDT","execId":"funding","execQty":"1"}]}`))
	if err != nil {
		t.Fatalf("DecodeExecutionMessage Funding: %v", err)
	}
	fundingEvents, materialErr := execEventsFromExecutionMessage(funding, resolve, AccountIDUnified)
	if materialErr != nil || len(fundingEvents) != 0 {
		t.Fatalf("Funding events=%d err=%v, want benign skip", len(fundingEvents), materialErr)
	}

	for _, execType := range []string{"AdlTrade", "BustTrade", "Delivery", "Settle", "", "Unknown"} {
		t.Run(execType, func(t *testing.T) {
			msg, err := bybitsdk.DecodeExecutionMessage([]byte(`{"topic":"execution","data":[{"category":"linear","execType":"` + execType + `","symbol":"BTCUSDT","execId":"exec-1","execQty":"1"}]}`))
			if err != nil {
				t.Fatalf("DecodeExecutionMessage: %v", err)
			}
			events, materialErr := execEventsFromExecutionMessage(msg, resolve, AccountIDUnified)
			if len(events) != 0 || materialErr == nil {
				t.Fatalf("execType=%q events=%d err=%v, want fail-closed material signal", execType, len(events), materialErr)
			}
		})
	}

	trade, err := bybitsdk.DecodeExecutionMessage([]byte(`{"topic":"execution","data":[{"category":"linear","execType":"Trade","symbol":"BTCUSDT","execId":"exec-2","execQty":"1","execPrice":"100"}]}`))
	if err != nil {
		t.Fatalf("DecodeExecutionMessage: %v", err)
	}
	events, materialErr := execEventsFromExecutionMessage(trade, resolve, AccountIDUnified)
	if materialErr != nil || len(events) != 1 {
		t.Fatalf("Trade produced %d fills, want 1", len(events))
	}
}

func TestPrivateTradingEventsResolveSharedSymbolByCategory(t *testing.T) {
	provider := privateEventTestProvider()

	spotOrder, err := bybitsdk.DecodeOrderMessage([]byte(`{"topic":"order","data":[{"category":"spot","symbol":"BTCUSDT","orderId":"spot-order"}]}`))
	if err != nil {
		t.Fatalf("DecodeOrderMessage: %v", err)
	}
	spotEvents := execEventsFromOrderMessage(spotOrder, provider.resolvePrivateRecord, AccountIDUnified)
	if len(spotEvents) != 1 {
		t.Fatalf("spot order events=%d, want 1", len(spotEvents))
	}

	linearExecution, err := bybitsdk.DecodeExecutionMessage([]byte(`{"topic":"execution","data":[{"category":"linear","execType":"Trade","symbol":"BTCUSDT","execId":"linear-fill","execQty":"1","execPrice":"100"}]}`))
	if err != nil {
		t.Fatalf("DecodeExecutionMessage: %v", err)
	}
	linearEvents, materialErr := execEventsFromExecutionMessage(linearExecution, provider.resolvePrivateRecord, AccountIDUnified)
	if materialErr != nil {
		t.Fatalf("normalize linear execution: %v", materialErr)
	}
	if len(linearEvents) != 1 {
		t.Fatalf("linear execution events=%d, want 1", len(linearEvents))
	}
	linearPosition, err := bybitsdk.DecodePositionMessage([]byte(`{"topic":"position","data":[{"category":"linear","symbol":"BTCUSDT","side":"Buy","size":"1"}]}`))
	if err != nil {
		t.Fatalf("DecodePositionMessage: %v", err)
	}
	positionEvents := accountEventsFromPositionMessage(linearPosition, provider.resolvePrivateRecord, AccountIDUnified, time.Unix(1, 0))
	if len(positionEvents) != 1 {
		t.Fatalf("linear position events=%d, want 1", len(positionEvents))
	}

	spotOrderEvent, ok := spotEvents[0].(contract.OrderEvent)
	if !ok {
		t.Fatalf("spot event type=%T, want contract.OrderEvent", spotEvents[0])
	}
	if got := spotOrderEvent.Order.Request.InstrumentID; got != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot}) {
		t.Fatalf("spot order instrument=%+v, want spot BTC-USDT", got)
	}
	linearFillEvent, ok := linearEvents[0].(contract.FillEvent)
	if !ok {
		t.Fatalf("linear event type=%T, want contract.FillEvent", linearEvents[0])
	}
	if got := linearFillEvent.Fill.InstrumentID; got != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}) {
		t.Fatalf("linear fill instrument=%+v, want perp BTC-USDT", got)
	}
	linearPositionEvent, ok := positionEvents[0].(contract.PositionEvent)
	if !ok {
		t.Fatalf("linear position event type=%T, want contract.PositionEvent", positionEvents[0])
	}
	if got := linearPositionEvent.Position.InstrumentID; got != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}) {
		t.Fatalf("linear position instrument=%+v, want perp BTC-USDT", got)
	}
}

func TestPrivateEventsPreserveBybitPositionMode(t *testing.T) {
	provider := privateEventTestProvider()
	resolve := provider.resolvePrivateRecord

	orderMessage, err := bybitsdk.DecodeOrderMessage([]byte(`{"topic":"order","data":[{"category":"linear","symbol":"BTCUSDT","positionIdx":1,"orderId":"hedge-long"}]}`))
	if err != nil {
		t.Fatalf("DecodeOrderMessage: %v", err)
	}
	orderEvents := execEventsFromOrderMessage(orderMessage, resolve, AccountIDUnified)
	if len(orderEvents) != 1 {
		t.Fatalf("order events=%d, want 1", len(orderEvents))
	}
	orderEvent, ok := orderEvents[0].(contract.OrderEvent)
	if !ok || orderEvent.Order.Request.PositionSide != enums.PosLong {
		t.Fatalf("order event=%+v, want hedge long position side", orderEvents[0])
	}

	for _, tc := range []struct {
		name        string
		positionIdx int
		side        string
		wantSide    enums.PositionSide
		wantQty     string
	}{
		{name: "one-way short inventory", positionIdx: 0, side: "Sell", wantSide: enums.PosNet, wantQty: "-2"},
		{name: "hedge long leg", positionIdx: 1, side: "Buy", wantSide: enums.PosLong, wantQty: "2"},
		{name: "hedge short leg", positionIdx: 2, side: "Sell", wantSide: enums.PosShort, wantQty: "-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := fmt.Sprintf(`{"topic":"position","data":[{"category":"linear","symbol":"BTCUSDT","positionIdx":%d,"side":%q,"size":"2"}]}`, tc.positionIdx, tc.side)
			message, err := bybitsdk.DecodePositionMessage([]byte(payload))
			if err != nil {
				t.Fatalf("DecodePositionMessage: %v", err)
			}
			events := accountEventsFromPositionMessage(message, resolve, AccountIDUnified, time.Unix(1, 0))
			if len(events) != 1 {
				t.Fatalf("position events=%d, want 1", len(events))
			}
			event, ok := events[0].(contract.PositionEvent)
			if !ok || event.Position.Side != tc.wantSide || event.Position.Quantity.String() != tc.wantQty {
				t.Fatalf("position event=%+v, want side=%s qty=%s", events[0], tc.wantSide, tc.wantQty)
			}
		})
	}

	invalidMessage, err := bybitsdk.DecodePositionMessage([]byte(`{"topic":"position","data":[{"category":"linear","symbol":"BTCUSDT","positionIdx":3,"side":"Buy","size":"2"}]}`))
	if err != nil {
		t.Fatalf("DecodePositionMessage invalid idx: %v", err)
	}
	if events := accountEventsFromPositionMessage(invalidMessage, resolve, AccountIDUnified, time.Unix(1, 0)); len(events) != 0 {
		t.Fatalf("invalid positionIdx produced %d events, want fail-closed skip", len(events))
	}

	invalidOrder, err := bybitsdk.DecodeOrderMessage([]byte(`{"topic":"order","data":[{"category":"linear","symbol":"BTCUSDT","positionIdx":3,"orderId":"invalid-leg"}]}`))
	if err != nil {
		t.Fatalf("DecodeOrderMessage invalid idx: %v", err)
	}
	if events := execEventsFromOrderMessage(invalidOrder, resolve, AccountIDUnified); len(events) != 0 {
		t.Fatalf("invalid order positionIdx produced %d events, want fail-closed skip", len(events))
	}
}

func TestMaterialPrivateExecutionEmitsReconciliationGapPair(t *testing.T) {
	exec := newExecutionClient(nil, privateEventTestProvider(), clock.NewRealClock())
	defer exec.Close()
	adapter := &Adapter{exec: exec}
	adapter.reportMaterialPrivateExecution("unsupported Settle execution")

	startedEnvelope := <-exec.Events()
	recoveredEnvelope := <-exec.Events()
	started, ok := startedEnvelope.Payload.(contract.StreamGapEvent)
	if !ok {
		t.Fatalf("started payload=%T, want StreamGapEvent", startedEnvelope.Payload)
	}
	recovered, ok := recoveredEnvelope.Payload.(contract.StreamGapEvent)
	if !ok {
		t.Fatalf("recovered payload=%T, want StreamGapEvent", recoveredEnvelope.Payload)
	}
	if started.StreamID != privateMaterialStreamID || started.Phase != contract.StreamGapStarted ||
		recovered.StreamID != privateMaterialStreamID || recovered.Phase != contract.StreamGapRecovered ||
		started.Generation != recovered.Generation {
		t.Fatalf("gap pair started=%+v recovered=%+v", started, recovered)
	}
}

func privateEventTestProvider() *instrumentProvider {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		{
			ID:          model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot},
			VenueSymbol: "BTCUSDT",
			Settle:      bybitsdk.SettleCoinUSDT,
		},
		{
			ID:          model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp},
			VenueSymbol: "BTCUSDT",
			Settle:      bybitsdk.SettleCoinUSDT,
		},
	})
	return provider
}
