package perp_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/binance/perp"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

// TestLiveAdapterSmoke is an env-gated integration test. It runs ONLY when
// BOLTER_ENABLE_LIVE_READ_TESTS=1 and BINANCE_API_KEY / BINANCE_API_SECRET are
// present; otherwise it skips. It is read-only: it loads instruments, fetches a
// depth snapshot, and reconciles account state — it places no orders.
//
//	BOLTER_ENABLE_LIVE_READ_TESTS=1 BINANCE_API_KEY=... BINANCE_API_SECRET=... go test -run TestLiveAdapterSmoke ./adapter/binance/perp/
func TestLiveAdapterSmoke(t *testing.T) {
	testenv.RequireLiveRead(t, "BINANCE_API_KEY", "BINANCE_API_SECRET")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	a, err := perp.New(ctx, perp.Config{
		APIKey:    mustEnv(t, "BINANCE_API_KEY"),
		APISecret: mustEnv(t, "BINANCE_API_SECRET"),
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "adapter.New")
		t.Fatalf("adapter: %v", err)
	}
	defer a.Close()

	// Instruments loaded.
	if len(a.Market.InstrumentProvider().All()) == 0 {
		t.Fatal("no instruments loaded from exchangeInfo")
	}

	inst := model.InstrumentID{Venue: "BINANCE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	if _, ok := a.Market.InstrumentProvider().Instrument(inst); !ok {
		t.Skip("BTC-USDT perp not present; skipping depth check")
	}

	// Depth snapshot (public).
	book, err := a.Market.OrderBook(ctx, inst, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OrderBook")
		t.Fatalf("orderbook: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty book: bids=%d asks=%d", len(book.Bids), len(book.Asks))
	}
	if book.Bids[0].Price.GreaterThanOrEqual(book.Asks[0].Price) {
		t.Errorf("crossed book: bid %s >= ask %s", book.Bids[0].Price, book.Asks[0].Price)
	}

	// Account read (signed).
	if _, err := a.Account.Balances(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Balances")
		t.Fatalf("balances: %v", err)
	}
}

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("missing env %s", key)
	}
	return v
}
