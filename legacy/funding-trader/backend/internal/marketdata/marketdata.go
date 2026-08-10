package marketdata

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

const MaxPageLimit = 500

var (
	ErrInstrumentNotFound      = errors.New("marketdata instrument not found")
	ErrInvalidMarketData       = errors.New("marketdata response is invalid")
	ErrResnapshotRequired      = errors.New("marketdata stream requires resnapshot")
	ErrStreamUnavailable       = errors.New("marketdata stream is unavailable")
	ErrNoConfiguredInstruments = errors.New("marketdata has no configured instruments")
)

type OptionalDecimal struct {
	Value decimal.Decimal
	Valid bool
}

type Rules struct {
	PriceIncrement    decimal.Decimal
	QuantityIncrement decimal.Decimal
	MinQuantity       decimal.Decimal
	MinNotional       OptionalDecimal
}

// Instrument is the public, account-scoped identity used by funding-trader.
// It intentionally has no venue-native symbol, native ID, transport encoding,
// credentials, or private account state.
type Instrument struct {
	AccountID        string
	InstrumentID     string
	Venue            string
	VenueEnvironment string
	Product          string
	Settlement       string
	Symbol           string
	BaseAsset        string
	QuoteAsset       string
	Rules            Rules
}

func (instrument Instrument) Key() string {
	return instrument.AccountID + "\x00" + instrument.InstrumentID
}

type Level struct {
	Price    decimal.Decimal
	Quantity decimal.Decimal
}

type FundingRate struct {
	Instrument      Instrument
	Rate            decimal.Decimal
	MarkPrice       OptionalDecimal
	ObservedAt      time.Time
	FundingTime     time.Time
	NextFundingTime time.Time
}

type FundingRateHistoryRequest struct {
	Instrument Instrument
	Start      time.Time
	End        time.Time
	Cursor     string
	Limit      int
}

type FundingRatePage struct {
	Rates      []FundingRate
	NextCursor string
}

type OrderBookRequest struct {
	Instrument Instrument
	Limit      int
}

type PageInfo struct {
	Cursor       string
	Limit        int
	WindowStart  time.Time
	WindowEnd    time.Time
	HasMoreKnown bool
	HasMore      bool
}

type OrderBook struct {
	Instrument Instrument
	Kind       string
	Bids       []Level
	Asks       []Level
	ObservedAt time.Time
	Sequence   string
	Previous   string
	Resync     bool
	Page       PageInfo
}

type BBO struct {
	Instrument Instrument
	Bid        Level
	Ask        Level
	ObservedAt time.Time
}

type MarkPrice struct {
	Instrument Instrument
	Price      decimal.Decimal
	ObservedAt time.Time
}

type StreamEventType string

const (
	StreamSnapshot StreamEventType = "snapshot"
	StreamUpdate   StreamEventType = "update"
)

type StreamEvent[T any] struct {
	BootID     string
	Sequence   uint64
	EventID    string
	Type       StreamEventType
	OccurredAt time.Time
	Instrument Instrument
	Data       T
}

type Subscription[T any] interface {
	Next(context.Context) (StreamEvent[T], error)
	Close() error
}

type PublicREST interface {
	Instruments(context.Context) ([]Instrument, error)
	FundingRate(context.Context, Instrument) (FundingRate, error)
	FundingRateHistory(context.Context, FundingRateHistoryRequest) (FundingRatePage, error)
	OrderBook(context.Context, OrderBookRequest) (OrderBook, error)
	BBO(context.Context, Instrument) (BBO, error)
	MarkPrice(context.Context, Instrument) (MarkPrice, error)
}

type PublicStream interface {
	WatchOrderBook(context.Context, Instrument) (Subscription[OrderBook], error)
	WatchBBO(context.Context, Instrument) (Subscription[BBO], error)
	WatchMarkPrice(context.Context, Instrument) (Subscription[MarkPrice], error)
	WatchFundingRate(context.Context, Instrument) (Subscription[FundingRate], error)
}

type ReadinessProbe interface {
	Probe(context.Context) (Readiness, error)
}

type Readiness struct {
	Mode        string
	Accounts    int
	Instruments int
}

type Provider struct {
	rest    PublicREST
	streams PublicStream
	probe   ReadinessProbe
	close   func() error
}

func NewProvider(rest PublicREST, streams PublicStream, probe ReadinessProbe, close func() error) (*Provider, error) {
	if rest == nil {
		return nil, fmt.Errorf("marketdata rest source is required")
	}
	if probe == nil {
		return nil, fmt.Errorf("marketdata readiness probe is required")
	}
	return &Provider{rest: rest, streams: streams, probe: probe, close: close}, nil
}

func (provider *Provider) Instruments(ctx context.Context) ([]Instrument, error) {
	return provider.rest.Instruments(ctx)
}

func (provider *Provider) FundingRate(ctx context.Context, instrument Instrument) (FundingRate, error) {
	return provider.rest.FundingRate(ctx, instrument)
}

func (provider *Provider) FundingRateHistory(ctx context.Context, request FundingRateHistoryRequest) (FundingRatePage, error) {
	return provider.rest.FundingRateHistory(ctx, request)
}

func (provider *Provider) OrderBook(ctx context.Context, request OrderBookRequest) (OrderBook, error) {
	return provider.rest.OrderBook(ctx, request)
}

func (provider *Provider) BBO(ctx context.Context, instrument Instrument) (BBO, error) {
	return provider.rest.BBO(ctx, instrument)
}

func (provider *Provider) MarkPrice(ctx context.Context, instrument Instrument) (MarkPrice, error) {
	return provider.rest.MarkPrice(ctx, instrument)
}

func (provider *Provider) WatchOrderBook(ctx context.Context, instrument Instrument) (Subscription[OrderBook], error) {
	if provider.streams == nil {
		return nil, ErrStreamUnavailable
	}
	return provider.streams.WatchOrderBook(ctx, instrument)
}

func (provider *Provider) WatchBBO(ctx context.Context, instrument Instrument) (Subscription[BBO], error) {
	if provider.streams == nil {
		return nil, ErrStreamUnavailable
	}
	return provider.streams.WatchBBO(ctx, instrument)
}

func (provider *Provider) WatchMarkPrice(ctx context.Context, instrument Instrument) (Subscription[MarkPrice], error) {
	if provider.streams == nil {
		return nil, ErrStreamUnavailable
	}
	return provider.streams.WatchMarkPrice(ctx, instrument)
}

func (provider *Provider) WatchFundingRate(ctx context.Context, instrument Instrument) (Subscription[FundingRate], error) {
	if provider.streams == nil {
		return nil, ErrStreamUnavailable
	}
	return provider.streams.WatchFundingRate(ctx, instrument)
}

func (provider *Provider) Probe(ctx context.Context) (Readiness, error) {
	if provider == nil || provider.probe == nil {
		return Readiness{}, fmt.Errorf("marketdata readiness probe is unavailable")
	}
	return provider.probe.Probe(ctx)
}

func (provider *Provider) Close() error {
	if provider == nil || provider.close == nil {
		return nil
	}
	return provider.close()
}
