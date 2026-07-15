package perp_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/okx/perp"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

// TestLiveOKXAdapterSmoke is env-gated and read-only. It runs only with
// BOLTER_ENABLE_LIVE_READ_TESTS=1 and OKX_API_KEY / OKX_API_SECRET /
// OKX_API_PASSPHRASE present.
//
//	BOLTER_ENABLE_LIVE_READ_TESTS=1 OKX_API_KEY=... OKX_API_SECRET=... OKX_API_PASSPHRASE=... \
//	go test -run TestLiveOKXAdapterSmoke ./adapter/okx/perp/
func TestLiveOKXAdapterSmoke(t *testing.T) {
	testenv.RequireLiveRead(t, "OKX_API_KEY", "OKX_API_SECRET", "OKX_API_PASSPHRASE")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	a, err := perp.New(ctx, perp.Config{
		APIKey:     os.Getenv("OKX_API_KEY"),
		APISecret:  os.Getenv("OKX_API_SECRET"),
		Passphrase: os.Getenv("OKX_API_PASSPHRASE"),
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "okx.New")
		t.Fatalf("adapter: %v", err)
	}
	defer a.Close()

	if len(a.Market.InstrumentProvider().All()) == 0 {
		t.Fatal("no SWAP instruments loaded")
	}

	inst := model.InstrumentID{Venue: "OKX", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	got, ok := a.Market.InstrumentProvider().Instrument(inst)
	if !ok {
		t.Skip("BTC-USDT SWAP not present")
	}
	if got.VenueSymbol != "BTC-USDT-SWAP" {
		t.Errorf("VenueSymbol=%q, want BTC-USDT-SWAP", got.VenueSymbol)
	}

	book, err := a.Market.OrderBook(ctx, inst, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OrderBook")
		t.Fatalf("orderbook: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty book: bids=%d asks=%d", len(book.Bids), len(book.Asks))
	}
}
