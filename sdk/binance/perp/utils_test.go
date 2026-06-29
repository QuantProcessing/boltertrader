package perp

import (
	"strings"
	"testing"
)

func TestUtils_BuildQueryString(t *testing.T) {
	got := BuildQueryString(map[string]interface{}{"symbol": "BTCUSDT", "limit": 10})
	if !strings.Contains(got, "symbol=BTCUSDT") || !strings.Contains(got, "limit=10") {
		t.Fatalf("unexpected query string: %s", got)
	}
}
