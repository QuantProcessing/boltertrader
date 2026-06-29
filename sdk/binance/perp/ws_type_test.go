package perp

import (
	"encoding/json"
	"testing"
)

func TestWSTypeCompanion_DepthEventDecode(t *testing.T) {
	var event WsDepthEvent
	if err := json.Unmarshal([]byte(`{"e":"depthUpdate","s":"BTCUSDT"}`), &event); err != nil {
		t.Fatalf("decode depth event: %v", err)
	}
	if event.EventType != "depthUpdate" || event.Symbol != "BTCUSDT" {
		t.Fatalf("unexpected event: %+v", event)
	}
}

func TestWSTypeCompanion_KlineEventDecodeNumericTradeCount(t *testing.T) {
	var event WsKlineEvent
	payload := []byte(`{"e":"kline","E":1781879128076,"s":"BTCUSDT","k":{"t":1781879100000,"T":1781879159999,"s":"BTCUSDT","i":"1m","f":507407010,"L":507407441,"o":"63044.10","c":"63075.60","h":"63075.60","l":"63026.70","v":"5845.7165","n":432,"x":false,"q":"368652273.272550","V":"5835.0569","Q":"367980433.115530","B":"0"}}`)
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode kline event: %v", err)
	}
	if event.Symbol != "BTCUSDT" || event.Kline.Interval != "1m" || event.Kline.NumberOfTrades != 432 {
		t.Fatalf("unexpected kline event: %+v", event)
	}
}
