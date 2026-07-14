package spot

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
)

func TestStatusFromBinanceMapsPendingNew(t *testing.T) {
	if got := statusFromBinance("PENDING_NEW"); got != enums.StatusPendingNew {
		t.Fatalf("status=%s, want PENDING_NEW", got)
	}
}
