package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	realtimewire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/hotset"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
)

// PublicMarketSource is account-independent catalog and historical data access.
// Live data always goes through MarketHub, never an adapter subscription here.
type PublicMarketSource interface {
	MarketKeys
	Snapshot(context.Context) (publicmarket.Snapshot, error)
	ResolveInstrument(context.Context, string) (publicmarket.Instrument, error)
	Candles(context.Context, publicmarket.Query, string, int) (publicmarket.CandleResult, error)
}

type MarketHub interface {
	Subscribe(context.Context, markethub.SubscriptionKey, int) (*markethub.Subscription, error)
}

type MarketKeys interface {
	SubscriptionKey(context.Context, string, exchange.MarketDataset, string, int) (markethub.SubscriptionKey, error)
}

type MarketHotset interface {
	Acquire(context.Context, hotset.Key, hotset.Reason) (*hotset.Lease, error)
}

type MarketOptions struct {
	Source PublicMarketSource
	Hub    MarketHub
	Hotset MarketHotset
}

type marketInstrument = realtimewire.MarketInstrument
type marketCatalogResponse = realtimewire.MarketCatalog
type marketSnapshotResponse = realtimewire.MarketSnapshot
type marketCandlesResponse = realtimewire.MarketCandles

func (handler *Handler) marketCatalog(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) != 0 {
		handler.invalid(w)
		return
	}
	if handler.markets.Source == nil {
		handler.marketFailure(w, publicmarket.ErrUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), publicMarketSnapshotTimeout)
	defer cancel()
	snapshot, err := handler.markets.Source.Snapshot(ctx)
	if err != nil {
		handler.marketFailure(w, err)
		return
	}
	response := marketCatalogResponse{
		Revision: snapshot.Revision, ObservedAt: snapshot.ObservedAt,
		Complete:            len(snapshot.UnavailableProfiles) == 0,
		UnavailableProfiles: append([]string{}, snapshot.UnavailableProfiles...),
		Items:               make([]marketInstrument, 0, len(snapshot.Instruments)),
	}
	for _, item := range snapshot.Instruments {
		instrument := item.Catalog
		response.Items = append(response.Items, marketInstrument{
			InstrumentID: string(instrument.ID), RouteProfileID: item.RouteProfileID,
			Venue: string(instrument.Venue), Environment: string(instrument.Environment), Product: string(instrument.Product),
			Settlement: instrument.Settlement, Symbol: instrument.Symbol, BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset,
			Ready: item.Ready,
		})
		response.Complete = response.Complete && item.Ready
	}
	writeJSON(w, http.StatusOK, response)
}

func (handler *Handler) marketCandles(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if !marketQuery(query, []string{"instrument_id", "interval"}, "limit") || !validMarketInterval(query.Get("interval")) || domain.InstrumentID(query.Get("instrument_id")).Validate() != nil {
		handler.invalid(w)
		return
	}
	limit := defaultListLimit
	if value := query.Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > maxListLimit {
			handler.invalid(w)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), publicMarketSnapshotTimeout)
	defer cancel()
	instrument, err := handler.resolveMarketInstrument(ctx, query.Get("instrument_id"))
	if err != nil {
		handler.marketFailure(w, err)
		return
	}
	result, err := handler.markets.Source.Candles(ctx, publicmarket.Query{
		Venue: string(instrument.Catalog.Venue), Environment: string(instrument.Catalog.Environment),
		Product: string(instrument.Catalog.Product), InstrumentID: query.Get("instrument_id"),
	}, query.Get("interval"), limit)
	if err != nil {
		handler.marketFailure(w, err)
		return
	}
	candles := make([]realtimewire.Candle, 0, len(result.Page.Candles))
	for _, candle := range result.Page.Candles {
		candles = append(candles, realtimewire.Candle{OpenTime: candle.OpenTime, CloseTime: candle.CloseTime,
			Open: candle.Open.String(), High: candle.High.String(), Low: candle.Low.String(), Close: candle.Close.String(),
			Volume: candle.Volume.String(), Complete: candle.Complete})
	}
	page := result.Page.Page
	writeJSON(w, http.StatusOK, marketCandlesResponse{
		InstrumentID: query.Get("instrument_id"), Interval: query.Get("interval"),
		Candles: candles, Page: realtimewire.PageInfo{Cursor: page.Cursor, Limit: page.Limit, WindowStart: page.WindowStart, WindowEnd: page.WindowEnd, HasMoreKnown: page.HasMoreKnown, HasMore: page.HasMore},
	})
}

func (handler *Handler) marketSnapshot(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if !marketQuery(query, []string{"instrument_id", "topic"}, "depth", "interval") {
		handler.invalid(w)
		return
	}
	request := realtimewire.TopicRequest{Topic: realtimewire.Topic(query.Get("topic")), InstrumentID: query.Get("instrument_id"), Interval: query.Get("interval")}
	if query.Has("depth") != (request.Topic == realtimewire.TopicOrderBook) || query.Has("interval") != (request.Topic == realtimewire.TopicCandles) {
		handler.invalid(w)
		return
	}
	if value := query.Get("depth"); value != "" {
		var err error
		request.Depth, err = strconv.Atoi(value)
		if err != nil {
			handler.invalid(w)
			return
		}
	}
	if validatePublicMarketTopic(request) != nil {
		handler.invalid(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), publicMarketSnapshotTimeout)
	defer cancel()
	var snapshot realtimewire.Message
	err := handler.runPublicMarketTopic(ctx, "", request, func(message realtimewire.Message) bool {
		snapshot = message
		return false // One shared snapshot, then release the logical subscriber.
	})
	if err != nil {
		handler.marketFailure(w, err)
		return
	}
	if snapshot.Type != realtimewire.MessageSnapshot {
		handler.marketFailure(w, markethub.ErrUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, marketSnapshotResponse{InstrumentID: request.InstrumentID, Snapshot: snapshot})
}

func (handler *Handler) marketStream(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) != 0 {
		handler.invalid(w)
		return
	}
	if handler.markets.Source == nil || handler.markets.Hub == nil {
		handler.marketFailure(w, publicmarket.ErrUnavailable)
		return
	}
	serveMarketStream(w, r, realtimewire.MaxActiveTopics, validatePublicMarketTopic, handler.runPublicMarketTopic)
}

func (handler *Handler) resolveMarketInstrument(ctx context.Context, id string) (publicmarket.Instrument, error) {
	if handler.markets.Source == nil {
		return publicmarket.Instrument{}, publicmarket.ErrUnavailable
	}
	instrument, err := handler.markets.Source.ResolveInstrument(ctx, id)
	if err != nil {
		return publicmarket.Instrument{}, err
	}
	if string(instrument.Catalog.ID) != id {
		return publicmarket.Instrument{}, publicmarket.ErrUnavailable
	}
	return instrument, nil
}

func (handler *Handler) runPublicMarketTopic(ctx context.Context, id string, request realtimewire.TopicRequest, emit func(realtimewire.Message) bool) error {
	startupCtx, cancel := context.WithTimeout(ctx, publicMarketSnapshotTimeout)
	instrument, err := handler.resolveMarketInstrument(startupCtx, request.InstrumentID)
	cancel()
	if err != nil {
		return err
	}
	if request.Topic == realtimewire.TopicMarkPrice && instrument.Catalog.Product != exchange.ProductPerp {
		return exchange.ErrUnsupported
	}
	if handler.markets.Hub == nil {
		return markethub.ErrUnavailable
	}
	// Application callers cannot select a protected execution/position priority.
	request.InternalPurpose = string(hotset.ReasonBrowser)
	return (marketStreams{hub: handler.markets.Hub, keys: handler.markets.Source, hotset: handler.markets.Hotset}).run(ctx, id, request, emit)
}

func (handler *Handler) marketFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, publicmarket.ErrUnavailable) || errors.Is(err, markethub.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		writeError(w, http.StatusServiceUnavailable, "market_unavailable", "market data is unavailable", handler.correlationID(), true)
		return
	}
	handler.fail(w, err)
}

func validatePublicMarketTopic(request realtimewire.TopicRequest) error {
	if !isSharedMarketTopic(request.Topic) || request.InternalPurpose != "" || request.Cursor != "" || request.SubscriptionID != "" || domain.InstrumentID(request.InstrumentID).Validate() != nil {
		return fmt.Errorf("public market topic is invalid")
	}
	if request.Topic == realtimewire.TopicCandles && !validMarketInterval(request.Interval) {
		return fmt.Errorf("candle interval is invalid")
	}
	return request.Validate()
}

func validMarketInterval(interval string) bool {
	switch interval {
	case "1m", "5m", "15m", "30m", "1h", "4h", "12h", "1d":
		return true
	default:
		return false
	}
}

func marketQuery(query url.Values, required []string, optional ...string) bool {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = true
		if query.Get(name) == "" {
			return false
		}
	}
	for _, name := range optional {
		allowed[name] = true
	}
	for name, values := range query {
		if !allowed[name] || len(values) != 1 || strings.TrimSpace(values[0]) == "" || values[0] != strings.TrimSpace(values[0]) {
			return false
		}
	}
	return true
}
