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

func TestGenerateSignatureMatchesBinanceUSDMFuturesDocsHMACExample(t *testing.T) {
	const secret = "2b5eb11e18796d12d88f13dc27dbbd02c2cc51ff7059765ed9821957d82bb4d9"
	const payload = "symbol=BTCUSDT&side=BUY&type=LIMIT&quantity=1&price=9000&timeInForce=GTC&recvWindow=5000&timestamp=1591702613943"
	const want = "3c661234138461fcc7a7d8746c6558c9842d4e10870d2ecbedf7777cad694af9"

	if got := GenerateSignature(secret, payload); got != want {
		t.Fatalf("unexpected signature: got %s want %s", got, want)
	}
}
