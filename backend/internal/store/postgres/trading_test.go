package postgres

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
)

func TestTradingStoresImplementPlatformContracts(t *testing.T) {
	var _ reservations.Store = (*ReservationStore)(nil)
	var _ orders.Store = (*CommandStore)(nil)
	var _ ledger.Store = (*LedgerStore)(nil)
	var _ portfolio.Store = (*PortfolioStore)(nil)
	if NewReservationStore(nil) != nil || NewCommandStore(nil) != nil || NewLedgerStore(nil) != nil || NewPortfolioStore(nil) != nil {
		t.Fatal("nil database accepted")
	}
}
