package spot

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/shopspring/decimal"
)

func TestBinanceSpotDemoDataAcceptance(t *testing.T) {
	testenv.RequireLiveRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	httpClient, err := demoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Demo HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{Demo: true, HTTPClient: httpClient})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Spot Demo adapter initialization")
		t.Fatalf("new Binance Spot Demo adapter: %v", err)
	}
	defer adapter.Close()

	symbolInput := demoEnvOrDefault("BINANCE_DEMO_SYMBOL", "ETH-USDT")
	info, err := adapter.rest.ExchangeInfo(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Spot Demo exchangeInfo")
		t.Fatalf("exchange info: %v", err)
	}
	spec, err := demoAcceptanceSymbolSpecFromExchangeInfo(info, symbolInput)
	if err != nil {
		t.Fatalf("resolve Spot Demo symbol: %v", err)
	}
	instID := adapter.provider.resolveVenueSymbol(spec.VenueSymbol)

	book, err := adapter.Market.OrderBook(ctx, instID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Spot Demo order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("order book for %s has no bid/ask levels: %+v", spec.VenueSymbol, book)
	}

	bars, err := adapter.Market.Bars(ctx, instID, "1m", 2)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Spot Demo klines")
		t.Fatalf("bars: %v", err)
	}
	if len(bars) == 0 {
		t.Fatalf("bars for %s are empty", spec.VenueSymbol)
	}

	ticker, err := adapter.rest.BookTicker(ctx, spec.VenueSymbol)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Spot Demo bookTicker")
		t.Fatalf("bookTicker: %v", err)
	}
	if decimal.RequireFromString(ticker.BidPrice).LessThanOrEqual(decimal.Zero) ||
		decimal.RequireFromString(ticker.AskPrice).LessThanOrEqual(decimal.Zero) {
		t.Fatalf("bookTicker for %s has non-positive prices: %+v", spec.VenueSymbol, ticker)
	}
}
