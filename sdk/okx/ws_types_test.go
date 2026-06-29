package okx

import (
	"encoding/json"
	"testing"
)

func TestWSTypes_WsSubscribeResDecode(t *testing.T) {
	var res WsSubscribeRes
	if err := json.Unmarshal([]byte(`{"id":"1","event":"subscribe","arg":{"channel":"tickers","instId":"BTC-USDT"},"code":"0"}`), &res); err != nil {
		t.Fatalf("decode subscribe response: %v", err)
	}
	if res.ID == nil || *res.ID != "1" || res.Arg == nil || res.Arg.Channel != "tickers" {
		t.Fatalf("unexpected response: %+v", res)
	}
}
