package spot

import "testing"

func TestUtils_BuildQueryString(t *testing.T) {
	got := BuildQueryString(map[string]interface{}{"symbol": "BTCUSDT", "limit": 10})
	if got != "limit=10&symbol=BTCUSDT" {
		t.Fatalf("unexpected query string: %s", got)
	}
}
