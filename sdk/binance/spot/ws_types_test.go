package spot

import (
	"encoding/json"
	"testing"
)

func TestWSTypes_WSEnvelopeDecode(t *testing.T) {
	var env struct {
		EventType string `json:"e"`
		Symbol    string `json:"s"`
	}
	if err := json.Unmarshal([]byte(`{"e":"trade","s":"BTCUSDT"}`), &env); err != nil {
		t.Fatalf("decode ws event: %v", err)
	}
	if env.EventType != "trade" || env.Symbol != "BTCUSDT" {
		t.Fatalf("unexpected event: %+v", env)
	}
}
