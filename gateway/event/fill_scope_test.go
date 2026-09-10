package event

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

func TestFillEventVersionsAreScopedByInstrument(t *testing.T) {
	hub := newTestHub(t, 32, 16)
	manager := &StreamManager{hub: hub, principal: "principal-a", versions: make(map[string]domain.ResourceVersion)}
	subscription := subscribeTest(t, hub, "principal-a", "acct-a", "")
	btc, eth := domain.InstrumentID("binance:live:spot:BTC-USDT"), domain.InstrumentID("binance:live:spot:ETH-USDT")
	for i, instrumentID := range []domain.InstrumentID{btc, eth, btc, eth} {
		if err := manager.publishFill("acct-a", instrumentID, exchange.FillEvent{Fill: exchange.Fill{FillID: "123", Time: time.Now()}}); err != nil {
			t.Fatal(err)
		}
		event := receiveEvent(t, subscription)
		if event.ResourceVersion != domain.ResourceVersion(i/2+1) {
			t.Fatalf("instrument %s version=%d", instrumentID, event.ResourceVersion)
		}
		var payload FillPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.InstrumentID != instrumentID || payload.Fill.FillID != "123" {
			t.Fatalf("identity changed: %+v", payload)
		}
	}
}
