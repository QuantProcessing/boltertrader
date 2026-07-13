package spot

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/aster/spot"
)

type marketDataClient struct {
	rest     *sdkspot.Client
	ws       spotMarketWebsocket
	provider *instrumentProvider
	clk      clock.Clock
	stream   *wsstream.Stream[contract.MarketEnvelope]
}

type spotMarketWebsocket interface {
	Connect() error
	Close()
	IsConnected() bool
	SubscribeLimitOrderBook(symbol string, depth int, speed string, handler func(*sdkspot.DepthEvent) error) error
	SubscribeBookTicker(symbol string, handler func(*sdkspot.BookTickerEvent) error) error
	SubscribeAggTrade(symbol string, handler func(*sdkspot.AggTradeEvent) error) error
}

func newMarketDataClient(rest *sdkspot.Client, ws any, provider *instrumentProvider, clk clock.Clock) *marketDataClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	marketWS, _ := ws.(spotMarketWebsocket)
	return &marketDataClient{rest: rest, ws: marketWS, provider: provider, clk: clk, stream: wsstream.New[contract.MarketEnvelope](1024)}
}

func (c *marketDataClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:     VenueName,
		Products:  []contract.ProductCapability{{Kind: enums.KindSpot, Market: true}},
		Reports:   contract.ReportCapabilities{},
		Streaming: contract.StreamCapabilities{Market: c.ws != nil},
	}
}

func (c *marketDataClient) InstrumentProvider() model.InstrumentProvider { return c.provider }

func (c *marketDataClient) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	inst, err := c.provider.instrument(id)
	if err != nil {
		return nil, err
	}
	if c.rest == nil {
		return nil, fmt.Errorf("aster spot: rest client not configured: %w", errs.ErrNotSupported)
	}
	book, err := c.rest.Depth(ctx, inst.VenueSymbol, depth)
	if err != nil {
		return nil, mapAsterError(err)
	}
	bids, err := levels(book.Bids)
	if err != nil {
		return nil, fmt.Errorf("aster spot: bid levels: %w", err)
	}
	asks, err := levels(book.Asks)
	if err != nil {
		return nil, fmt.Errorf("aster spot: ask levels: %w", err)
	}
	return &model.OrderBook{InstrumentID: id, Bids: bids, Asks: asks, Sequence: book.LastUpdateID, Timestamp: firstNonZeroTime(timeFromMillis(book.TransactionTime), timeFromMillis(book.EventTime))}, nil
}

func (c *marketDataClient) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	_, err := c.provider.instrument(id)
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("aster spot: bar conversion is not implemented in Story 5: %w", errs.ErrNotSupported)
}

func (c *marketDataClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(ctx, id, func(symbol string) error {
		return c.ws.SubscribeLimitOrderBook(symbol, 20, "100ms", func(e *sdkspot.DepthEvent) error {
			if e == nil || e.Symbol != symbol {
				return fmt.Errorf("aster spot: depth event symbol mismatch %q for %q", eventSymbol(e), symbol)
			}
			book, err := bookFromDepthEvent(id, e, c.clk.Now())
			if err != nil {
				return err
			}
			c.emit(contract.BookEvent{Book: book})
			return nil
		})
	})
}
func (c *marketDataClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(ctx, id, func(symbol string) error {
		return c.ws.SubscribeBookTicker(symbol, func(e *sdkspot.BookTickerEvent) error {
			if e == nil || e.Symbol != symbol {
				return fmt.Errorf("aster spot: book ticker symbol mismatch %q for %q", bookTickerSymbol(e), symbol)
			}
			quote, err := quoteFromBookTicker(id, e, c.clk.Now())
			if err != nil {
				return err
			}
			c.emitQuote(quote, e)
			return nil
		})
	})
}
func (c *marketDataClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error {
	return c.subscribe(ctx, id, func(symbol string) error {
		return c.ws.SubscribeAggTrade(symbol, func(e *sdkspot.AggTradeEvent) error {
			if e == nil || e.Symbol != symbol {
				return fmt.Errorf("aster spot: aggregate trade symbol mismatch %q for %q", aggTradeSymbol(e), symbol)
			}
			trade, err := tradeFromAggTradeEvent(id, e)
			if err != nil {
				return err
			}
			c.emit(contract.TradeEvent{Trade: trade})
			return nil
		})
	})
}
func (c *marketDataClient) Events() <-chan contract.MarketEnvelope { return c.stream.C() }
func (c *marketDataClient) Close() error {
	if c.ws != nil {
		c.ws.Close()
	}
	c.stream.Close()
	return nil
}

func (c *marketDataClient) subscribe(ctx context.Context, id model.InstrumentID, fn func(symbol string) error) error {
	if c.ws == nil {
		return fmt.Errorf("aster spot: market websocket not configured: %w", errs.ErrNotSupported)
	}
	inst, err := c.provider.instrument(id)
	if err != nil {
		return err
	}
	if err := c.ws.Connect(); err != nil {
		return fmt.Errorf("aster spot: market websocket connect: %w", err)
	}
	if err := waitConnected(ctx, c.ws.IsConnected); err != nil {
		return err
	}
	return fn(inst.VenueSymbol)
}

func (c *marketDataClient) Connected() bool { return c.ws != nil && c.ws.IsConnected() }

func (c *marketDataClient) Reconnect(ctx context.Context) error {
	if c.ws == nil {
		return fmt.Errorf("aster spot: market websocket not configured: %w", errs.ErrNotSupported)
	}
	if err := c.ws.Connect(); err != nil {
		return fmt.Errorf("aster spot: market websocket connect: %w", err)
	}
	return waitConnected(ctx, c.ws.IsConnected)
}

func (c *marketDataClient) emit(ev contract.MarketEvent) {
	c.stream.Emit(contract.NewMarketEnvelope(ev))
}

func (c *marketDataClient) emitQuote(quote model.QuoteTick, e *sdkspot.BookTickerEvent) {
	c.stream.Emit(contract.NewMarketEnvelopeWithMeta(contract.QuoteEvent{Quote: quote}, contract.EventMeta{
		EventID:  model.EventID(fmt.Sprintf("market|quote|%s|%s|%d", quote.InstrumentID.String(), e.Symbol, e.UpdateID)),
		Sequence: uint64(e.UpdateID),
		Source:   contract.SourceAdapterStream,
		Flags:    contract.EventFlagFromStream,
	}))
}

func bookFromDepthEvent(id model.InstrumentID, e *sdkspot.DepthEvent, fallback time.Time) (model.OrderBook, error) {
	if e == nil {
		return model.OrderBook{}, fmt.Errorf("aster spot: depth event is required")
	}
	bids, err := levels(e.Bids)
	if err != nil {
		return model.OrderBook{}, fmt.Errorf("aster spot: depth event bids: %w", err)
	}
	asks, err := levels(e.Asks)
	if err != nil {
		return model.OrderBook{}, fmt.Errorf("aster spot: depth event asks: %w", err)
	}
	return model.OrderBook{InstrumentID: id, Bids: bids, Asks: asks, Sequence: e.FinalUpdateID, Timestamp: firstNonZeroTime(timeFromMillis(e.TransactionTime), timeFromMillis(e.EventTime), fallback)}, nil
}

func quoteFromBookTicker(id model.InstrumentID, e *sdkspot.BookTickerEvent, fallback time.Time) (model.QuoteTick, error) {
	if e == nil {
		return model.QuoteTick{}, fmt.Errorf("aster spot: book ticker event is required")
	}
	bidPrice, err := parseRequiredSDKDecimal("bidPrice", e.BestBidPrice)
	if err != nil || !bidPrice.IsPositive() {
		return model.QuoteTick{}, fmt.Errorf("aster spot: invalid bid price %q", e.BestBidPrice)
	}
	bidSize, err := parseRequiredSDKDecimal("bidSize", e.BestBidQty)
	if err != nil || bidSize.IsNegative() {
		return model.QuoteTick{}, fmt.Errorf("aster spot: invalid bid size %q", e.BestBidQty)
	}
	askPrice, err := parseRequiredSDKDecimal("askPrice", e.BestAskPrice)
	if err != nil || !askPrice.IsPositive() {
		return model.QuoteTick{}, fmt.Errorf("aster spot: invalid ask price %q", e.BestAskPrice)
	}
	askSize, err := parseRequiredSDKDecimal("askSize", e.BestAskQty)
	if err != nil || askSize.IsNegative() {
		return model.QuoteTick{}, fmt.Errorf("aster spot: invalid ask size %q", e.BestAskQty)
	}
	return model.QuoteTick{InstrumentID: id, BidPrice: bidPrice, BidSize: bidSize, AskPrice: askPrice, AskSize: askSize, Timestamp: fallback}, nil
}

func tradeFromAggTradeEvent(id model.InstrumentID, e *sdkspot.AggTradeEvent) (model.TradeTick, error) {
	if e == nil {
		return model.TradeTick{}, fmt.Errorf("aster spot: aggregate trade event is required")
	}
	if e.AggTradeID == 0 || e.TradeTime <= 0 {
		return model.TradeTick{}, fmt.Errorf("aster spot: aggregate trade id and timestamp are required")
	}
	price, err := parseRequiredSDKDecimal("price", e.Price)
	if err != nil || !price.IsPositive() {
		return model.TradeTick{}, fmt.Errorf("aster spot: invalid trade price %q", e.Price)
	}
	qty, err := parseRequiredSDKDecimal("quantity", e.Quantity)
	if err != nil || !qty.IsPositive() {
		return model.TradeTick{}, fmt.Errorf("aster spot: invalid trade quantity %q", e.Quantity)
	}
	side := enums.SideBuy
	if e.IsBuyerMaker {
		side = enums.SideSell
	}
	return model.TradeTick{InstrumentID: id, Price: price, Quantity: qty, AggressorSide: side, TradeID: strconv.FormatInt(e.AggTradeID, 10), Timestamp: timeFromMillis(e.TradeTime)}, nil
}

func eventSymbol(e *sdkspot.DepthEvent) string {
	if e == nil {
		return ""
	}
	return e.Symbol
}

func bookTickerSymbol(e *sdkspot.BookTickerEvent) string {
	if e == nil {
		return ""
	}
	return e.Symbol
}

func aggTradeSymbol(e *sdkspot.AggTradeEvent) string {
	if e == nil {
		return ""
	}
	return e.Symbol
}

func waitConnected(ctx context.Context, isUp func() bool) error {
	if isUp() {
		return nil
	}
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if isUp() {
				return nil
			}
		}
	}
}

func levels(rows [][]string) ([]model.BookLevel, error) {
	out := make([]model.BookLevel, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			return nil, fmt.Errorf("book level requires price and quantity")
		}
		price, err := parseRequiredSDKDecimal("price", row[0])
		if err != nil || !price.IsPositive() {
			return nil, fmt.Errorf("invalid book price %q", row[0])
		}
		qty, err := parseRequiredSDKDecimal("quantity", row[1])
		if err != nil || qty.IsNegative() {
			return nil, fmt.Errorf("invalid book quantity %q", row[1])
		}
		out = append(out, model.BookLevel{Price: price, Quantity: qty})
	}
	return out, nil
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, v := range values {
		if !v.IsZero() {
			return v
		}
	}
	return time.Time{}
}
