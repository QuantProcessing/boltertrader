package opportunityengine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/domain/alerts"
	"github.com/QuantProcessing/funding-trader/backend/internal/domain/opportunity"
	"github.com/QuantProcessing/funding-trader/backend/internal/domain/risk"
	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
	"github.com/shopspring/decimal"
)

const (
	defaultRefreshInterval = 30 * time.Second
	defaultHistoryWindow   = 30 * 24 * time.Hour
	defaultBookLimit       = 100
	defaultHistoryLimit    = 100
	maxInstrumentLoaders   = 4
)

var (
	defaultReferenceNotional = decimal.NewFromInt(10000)
	defaultTakerFeeRate      = decimal.RequireFromString("0.00020")
	defaultBufferPct         = decimal.NewFromInt(60)
)

type MarketSource interface {
	Instruments(context.Context) ([]marketdata.Instrument, error)
	FundingRate(context.Context, marketdata.Instrument) (marketdata.FundingRate, error)
	FundingRateHistory(context.Context, marketdata.FundingRateHistoryRequest) (marketdata.FundingRatePage, error)
	OrderBook(context.Context, marketdata.OrderBookRequest) (marketdata.OrderBook, error)
	BBO(context.Context, marketdata.Instrument) (marketdata.BBO, error)
	MarkPrice(context.Context, marketdata.Instrument) (marketdata.MarkPrice, error)
	WatchFundingRate(context.Context, marketdata.Instrument) (marketdata.Subscription[marketdata.FundingRate], error)
}

type ProjectionStore interface {
	ApplyMarketSnapshot(context.Context, string, readmodel.MarketSnapshot) ([]readmodel.Event, error)
}

type Publisher interface {
	BroadcastStored(uistream.StoredEvent) error
	Publish(context.Context, uistream.EventInput) (uistream.StoredEvent, error)
}

type Config struct {
	Mode              config.Mode
	BootID            string
	Source            MarketSource
	Store             ProjectionStore
	Publisher         Publisher
	Now               func() time.Time
	ReferenceNotional decimal.Decimal
	TakerFeeRate      decimal.Decimal
	LiquidationBuffer decimal.Decimal
	RefreshInterval   time.Duration
	HistoryWindow     time.Duration
	MarketSymbols     []string
}

type Status struct {
	Ready         bool
	LastRefresh   time.Time
	LastError     string
	Opportunities int
	Alerts        int
}

type Engine struct {
	config Config

	refreshMu sync.Mutex
	snapshots map[string]instrumentSnapshot
	statusMu  sync.RWMutex
	status    Status
}

func New(options Config) (*Engine, error) {
	if options.Mode != config.Test && options.Mode != config.Live ||
		strings.TrimSpace(options.BootID) == "" ||
		options.Source == nil ||
		options.Store == nil ||
		options.Publisher == nil {
		return nil, fmt.Errorf("opportunity engine dependencies are required")
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if !options.ReferenceNotional.IsPositive() {
		options.ReferenceNotional = defaultReferenceNotional
	}
	if !options.TakerFeeRate.IsPositive() {
		options.TakerFeeRate = defaultTakerFeeRate
	}
	if !options.LiquidationBuffer.IsPositive() {
		options.LiquidationBuffer = defaultBufferPct
	}
	if options.RefreshInterval <= 0 {
		options.RefreshInterval = defaultRefreshInterval
	}
	if options.HistoryWindow <= 0 {
		options.HistoryWindow = defaultHistoryWindow
	}
	return &Engine{config: options, snapshots: make(map[string]instrumentSnapshot)}, nil
}

func (engine *Engine) Refresh(ctx context.Context) error {
	engine.refreshMu.Lock()
	defer engine.refreshMu.Unlock()

	historyEnd := engine.config.Now().UTC()
	instruments, err := engine.config.Source.Instruments(ctx)
	if err != nil {
		engine.recordFailure("instrument-discovery")
		return fmt.Errorf("discover opportunity instruments: %w", err)
	}
	instruments = engine.marketInstruments(instruments)
	eligible := make([]marketdata.Instrument, 0, len(instruments))
	for _, instrument := range instruments {
		if !engine.config.Mode.AllowsVenueEnvironment(instrument.VenueEnvironment) {
			continue
		}
		eligible = append(eligible, instrument)
	}
	results := engine.loadInstruments(ctx, eligible, historyEnd)
	loaded := make([]instrumentSnapshot, 0, len(results))
	degraded := false
	usedStale := false
	skippedIncomplete := false
	for _, result := range results {
		if result.err != nil {
			cached, exists := engine.snapshots[result.instrument.Key()]
			if !exists {
				degraded = true
				skippedIncomplete = true
				continue
			}
			loaded = append(loaded, cached)
			degraded = true
			usedStale = true
			continue
		}
		engine.snapshots[result.instrument.Key()] = result.snapshot
		loaded = append(loaded, result.snapshot)
	}
	if len(eligible) > 0 && len(loaded) == 0 {
		engine.recordFailure("market-data-unavailable")
		return fmt.Errorf("no complete market-data observations")
	}
	sort.Slice(loaded, func(left, right int) bool {
		return loaded[left].instrument.Key() < loaded[right].instrument.Key()
	})

	evaluationTime := engine.config.Now().UTC()
	opportunities, instrumentOpportunities, err := engine.buildOpportunities(loaded, evaluationTime)
	if err != nil {
		engine.recordFailure("opportunity-calculation")
		return err
	}
	alertProjections, err := engine.buildAlerts(loaded, instrumentOpportunities)
	if err != nil {
		engine.recordFailure("alert-calculation")
		return err
	}
	funding := fundingObservations(loaded)
	projection := readmodel.MarketSnapshot{
		Mode: engine.config.Mode.String(), ObservedAt: evaluationTime,
		Opportunities: opportunities, Alerts: alertProjections, Funding: funding,
	}
	events, err := engine.config.Store.ApplyMarketSnapshot(ctx, engine.config.BootID, projection)
	if err != nil {
		engine.recordFailure("projection-persistence")
		return fmt.Errorf("persist market projection: %w", err)
	}
	for _, event := range events {
		if event.ID < 1 {
			return fmt.Errorf("persisted projection event has invalid sequence")
		}
		if err := engine.config.Publisher.BroadcastStored(uistream.StoredEvent{
			Sequence: uint64(event.ID), Scope: "all", Kind: string(event.Kind),
			Data: event.Payload, OccurredAt: event.OccurredAt,
		}); err != nil {
			engine.recordFailure("stream-broadcast")
			return fmt.Errorf("broadcast market projection: %w", err)
		}
	}
	engine.statusMu.Lock()
	engine.status = Status{
		Ready: !degraded, LastRefresh: evaluationTime,
		Opportunities: len(opportunities), Alerts: len(alertProjections),
	}
	if degraded {
		engine.status.LastError = "market-data-partial"
		if usedStale && !skippedIncomplete {
			engine.status.LastError = "market-data-stale"
		}
	}
	engine.statusMu.Unlock()
	return nil
}

func (engine *Engine) FreshOpportunity(
	ctx context.Context,
	longAccountID string,
	longInstrumentID string,
	shortAccountID string,
	shortInstrumentID string,
) (json.RawMessage, error) {
	engine.refreshMu.Lock()
	defer engine.refreshMu.Unlock()

	instruments, err := engine.config.Source.Instruments(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover route instruments: %w", err)
	}
	longInstrument, err := exactRouteInstrument(
		instruments, engine.config.Mode, longAccountID, longInstrumentID,
	)
	if err != nil {
		return nil, fmt.Errorf("select long route instrument: %w", err)
	}
	shortInstrument, err := exactRouteInstrument(
		instruments, engine.config.Mode, shortAccountID, shortInstrumentID,
	)
	if err != nil {
		return nil, fmt.Errorf("select short route instrument: %w", err)
	}
	if !compatible(engine.config.Mode, longInstrument, shortInstrument) {
		return nil, fmt.Errorf("route instruments are not compatible")
	}

	historyEnd := engine.config.Now().UTC()
	results := engine.loadInstruments(ctx, []marketdata.Instrument{longInstrument, shortInstrument}, historyEnd)
	loaded := make([]instrumentSnapshot, 0, len(results))
	for _, result := range results {
		if result.err != nil {
			return nil, fmt.Errorf(
				"load complete market data for account %s instrument %s: %w",
				result.instrument.AccountID,
				result.instrument.InstrumentID,
				result.err,
			)
		}
		loaded = append(loaded, result.snapshot)
	}
	if len(loaded) != 2 {
		return nil, fmt.Errorf("route market-data observations incomplete")
	}
	sort.Slice(loaded, func(left, right int) bool {
		return loaded[left].instrument.Key() < loaded[right].instrument.Key()
	})
	opportunities, _, err := engine.buildOpportunities(loaded, engine.config.Now().UTC())
	if err != nil {
		return nil, err
	}
	if len(opportunities) != 1 {
		return nil, fmt.Errorf("route produced %d opportunities", len(opportunities))
	}
	return append(json.RawMessage(nil), opportunities[0].Payload...), nil
}

func (engine *Engine) HandleStreamError(ctx context.Context, streamErr error) error {
	if !errors.Is(streamErr, marketdata.ErrResnapshotRequired) {
		return streamErr
	}
	if err := engine.Refresh(ctx); err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		Reason string   `json:"reason"`
		Scope  []string `json:"scope"`
	}{
		Reason: "upstream-resync",
		Scope:  []string{"opportunities", "alerts"},
	})
	if err != nil {
		return err
	}
	_, err = engine.config.Publisher.Publish(ctx, uistream.EventInput{
		Scope: "all", Kind: uistream.EventResnapshotRequired, Data: payload,
	})
	return err
}

func (engine *Engine) Run(ctx context.Context) error {
	if err := engine.Refresh(ctx); err != nil {
		engine.recordFailure("initial-refresh")
	}
	instruments, err := engine.config.Source.Instruments(ctx)
	if err != nil {
		engine.recordFailure("stream-instrument-discovery")
		instruments = nil
	}
	instruments = engine.marketInstruments(instruments)
	watchContext, cancelWatchers := context.WithCancel(ctx)
	defer cancelWatchers()
	updates := make(chan error, max(1, len(instruments)*2))
	var watchers sync.WaitGroup
	for _, instrument := range instruments {
		if !engine.config.Mode.AllowsVenueEnvironment(instrument.VenueEnvironment) {
			continue
		}
		watchers.Add(1)
		go func(current marketdata.Instrument) {
			defer watchers.Done()
			engine.watchFunding(watchContext, current, updates)
		}(instrument)
	}
	defer watchers.Wait()
	ticker := time.NewTicker(engine.config.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case streamErr := <-updates:
			if streamErr == nil {
				_ = engine.Refresh(ctx)
				continue
			}
			if errors.Is(streamErr, marketdata.ErrResnapshotRequired) {
				_ = engine.HandleStreamError(ctx, streamErr)
				continue
			}
			engine.recordFailure("funding-stream")
		case <-ticker.C:
			_ = engine.Refresh(ctx)
		}
	}
}

func (engine *Engine) watchFunding(
	ctx context.Context,
	instrument marketdata.Instrument,
	updates chan<- error,
) {
	const reconnectDelay = 250 * time.Millisecond
	for ctx.Err() == nil {
		subscription, err := engine.config.Source.WatchFundingRate(ctx, instrument)
		if err != nil {
			sendUpdate(ctx, updates, err)
		} else {
			for ctx.Err() == nil {
				_, nextErr := subscription.Next(ctx)
				if nextErr != nil {
					if ctx.Err() == nil {
						sendUpdate(ctx, updates, nextErr)
					}
					break
				}
				sendUpdate(ctx, updates, nil)
			}
			_ = subscription.Close()
		}
		timer := time.NewTimer(reconnectDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func sendUpdate(ctx context.Context, updates chan<- error, err error) {
	select {
	case updates <- err:
	case <-ctx.Done():
	default:
	}
}

func (engine *Engine) Status() Status {
	engine.statusMu.RLock()
	defer engine.statusMu.RUnlock()
	return engine.status
}

type instrumentSnapshot struct {
	instrument marketdata.Instrument
	current    marketdata.FundingRate
	history    []marketdata.FundingRate
	book       marketdata.OrderBook
	bbo        marketdata.BBO
	mark       marketdata.MarkPrice
}

type instrumentLoadResult struct {
	instrument marketdata.Instrument
	snapshot   instrumentSnapshot
	err        error
}

type instrumentHistoryResult struct {
	instrument marketdata.Instrument
	history    []marketdata.FundingRate
	err        error
}

func (engine *Engine) loadInstruments(
	ctx context.Context,
	instruments []marketdata.Instrument,
	historyEnd time.Time,
) []instrumentLoadResult {
	if len(instruments) == 0 {
		return nil
	}
	historyResults := engine.loadHistories(ctx, instruments, historyEnd)
	results := make([]instrumentLoadResult, 0, len(instruments))
	loadable := make([]instrumentHistoryResult, 0, len(historyResults))
	for _, result := range historyResults {
		if result.err != nil {
			results = append(results, instrumentLoadResult{
				instrument: result.instrument,
				err:        result.err,
			})
			continue
		}
		loadable = append(loadable, result)
	}
	results = append(results, engine.loadMarketSnapshots(ctx, loadable)...)
	return results
}

func (engine *Engine) loadHistories(
	ctx context.Context,
	instruments []marketdata.Instrument,
	historyEnd time.Time,
) []instrumentHistoryResult {
	jobs := make(chan marketdata.Instrument)
	results := make(chan instrumentHistoryResult, len(instruments))
	workers := min(len(instruments), maxInstrumentLoaders)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for instrument := range jobs {
				history, err := engine.loadHistory(ctx, instrument, historyEnd)
				results <- instrumentHistoryResult{instrument: instrument, history: history, err: err}
			}
		}()
	}
	for _, instrument := range instruments {
		jobs <- instrument
	}
	close(jobs)
	group.Wait()
	close(results)

	loaded := make([]instrumentHistoryResult, 0, len(instruments))
	for result := range results {
		loaded = append(loaded, result)
	}
	return loaded
}

func (engine *Engine) loadMarketSnapshots(
	ctx context.Context,
	histories []instrumentHistoryResult,
) []instrumentLoadResult {
	if len(histories) == 0 {
		return nil
	}
	jobs := make(chan instrumentHistoryResult)
	results := make(chan instrumentLoadResult, len(histories))
	workers := min(len(histories), maxInstrumentLoaders)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for history := range jobs {
				snapshot, err := engine.loadMarketSnapshot(ctx, history.instrument, history.history)
				results <- instrumentLoadResult{
					instrument: history.instrument,
					snapshot:   snapshot,
					err:        err,
				}
			}
		}()
	}
	for _, history := range histories {
		jobs <- history
	}
	close(jobs)
	group.Wait()
	close(results)

	loaded := make([]instrumentLoadResult, 0, len(histories))
	for result := range results {
		loaded = append(loaded, result)
	}
	loaded = engine.refreshStaleMarketFields(ctx, loaded)
	return loaded
}

func (engine *Engine) refreshStaleMarketFields(
	ctx context.Context,
	results []instrumentLoadResult,
) []instrumentLoadResult {
	const maxRefreshPasses = 2
	for pass := 0; pass < maxRefreshPasses; pass++ {
		now := engine.config.Now().UTC()
		refreshedAny := false
		for index := range results {
			if results[index].err != nil {
				continue
			}
			snapshot, refreshed, err := engine.refreshStaleMarketFieldsForSnapshot(
				ctx,
				results[index].snapshot,
				now,
			)
			if err != nil {
				results[index].err = err
				continue
			}
			if refreshed {
				results[index].snapshot = snapshot
				refreshedAny = true
			}
		}
		if !refreshedAny {
			break
		}
	}
	return results
}

func (engine *Engine) refreshStaleMarketFieldsForSnapshot(
	ctx context.Context,
	snapshot instrumentSnapshot,
	now time.Time,
) (instrumentSnapshot, bool, error) {
	cutoff := now.Add(-risk.MaxDataAge)
	refreshed := false
	if observationIsStale(snapshot.current.ObservedAt, cutoff) {
		current, err := engine.config.Source.FundingRate(ctx, snapshot.instrument)
		if err != nil {
			return instrumentSnapshot{}, false, err
		}
		snapshot.current = current
		refreshed = true
	}
	if observationIsStale(snapshot.book.ObservedAt, cutoff) {
		book, err := engine.config.Source.OrderBook(ctx, marketdata.OrderBookRequest{
			Instrument: snapshot.instrument, Limit: defaultBookLimit,
		})
		if err != nil {
			return instrumentSnapshot{}, false, err
		}
		snapshot.book = book
		refreshed = true
	}
	if observationIsStale(snapshot.bbo.ObservedAt, cutoff) {
		bbo, err := engine.config.Source.BBO(ctx, snapshot.instrument)
		if err != nil {
			return instrumentSnapshot{}, false, err
		}
		snapshot.bbo = bbo
		refreshed = true
	}
	if observationIsStale(snapshot.mark.ObservedAt, cutoff) {
		mark, err := engine.config.Source.MarkPrice(ctx, snapshot.instrument)
		if err != nil {
			return instrumentSnapshot{}, false, err
		}
		snapshot.mark = mark
		refreshed = true
	}
	if refreshed && (snapshot.current.Instrument.Key() != snapshot.instrument.Key() ||
		snapshot.book.Instrument.Key() != snapshot.instrument.Key() ||
		snapshot.bbo.Instrument.Key() != snapshot.instrument.Key() ||
		snapshot.mark.Instrument.Key() != snapshot.instrument.Key()) {
		return instrumentSnapshot{}, false, marketdata.ErrInvalidMarketData
	}
	return snapshot, refreshed, nil
}

func observationIsStale(observedAt time.Time, cutoff time.Time) bool {
	return !observedAt.IsZero() && observedAt.Before(cutoff)
}

func (engine *Engine) loadMarketSnapshot(
	ctx context.Context,
	instrument marketdata.Instrument,
	history []marketdata.FundingRate,
) (instrumentSnapshot, error) {
	var (
		current    marketdata.FundingRate
		book       marketdata.OrderBook
		bbo        marketdata.BBO
		mark       marketdata.MarkPrice
		currentErr error
		bookErr    error
		bboErr     error
		markErr    error
		group      sync.WaitGroup
	)
	group.Add(4)
	go func() {
		defer group.Done()
		current, currentErr = engine.config.Source.FundingRate(ctx, instrument)
	}()
	go func() {
		defer group.Done()
		book, bookErr = engine.config.Source.OrderBook(ctx, marketdata.OrderBookRequest{
			Instrument: instrument, Limit: defaultBookLimit,
		})
	}()
	go func() {
		defer group.Done()
		bbo, bboErr = engine.config.Source.BBO(ctx, instrument)
	}()
	go func() {
		defer group.Done()
		mark, markErr = engine.config.Source.MarkPrice(ctx, instrument)
	}()
	group.Wait()

	switch {
	case currentErr != nil:
		return instrumentSnapshot{}, currentErr
	case bookErr != nil:
		return instrumentSnapshot{}, bookErr
	case bboErr != nil:
		return instrumentSnapshot{}, bboErr
	case markErr != nil:
		return instrumentSnapshot{}, markErr
	}
	if current.Instrument.Key() != instrument.Key() ||
		book.Instrument.Key() != instrument.Key() ||
		bbo.Instrument.Key() != instrument.Key() ||
		mark.Instrument.Key() != instrument.Key() {
		return instrumentSnapshot{}, marketdata.ErrInvalidMarketData
	}
	return instrumentSnapshot{
		instrument: instrument, current: current, history: history,
		book: book, bbo: bbo, mark: mark,
	}, nil
}

func (engine *Engine) loadHistory(
	ctx context.Context,
	instrument marketdata.Instrument,
	now time.Time,
) ([]marketdata.FundingRate, error) {
	request := marketdata.FundingRateHistoryRequest{
		Instrument: instrument, Start: now.Add(-engine.config.HistoryWindow),
		End: now, Limit: defaultHistoryLimit,
	}
	var result []marketdata.FundingRate
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		page, err := engine.config.Source.FundingRateHistory(ctx, request)
		if err != nil {
			return nil, err
		}
		for _, rate := range page.Rates {
			if rate.Instrument.Key() != instrument.Key() {
				return nil, marketdata.ErrInvalidMarketData
			}
			result = append(result, rate)
		}
		if page.NextCursor == "" {
			sort.Slice(result, func(left, right int) bool {
				return result[left].FundingTime.Before(result[right].FundingTime)
			})
			return result, nil
		}
		request.Cursor = page.NextCursor
	}
	return nil, fmt.Errorf("funding history exceeds safe page bound")
}

func (engine *Engine) buildOpportunities(
	loaded []instrumentSnapshot,
	now time.Time,
) ([]readmodel.Projection, map[string]string, error) {
	var projections []readmodel.Projection
	related := make(map[string]string)
	for left := 0; left < len(loaded); left++ {
		for right := left + 1; right < len(loaded); right++ {
			if !compatible(engine.config.Mode, loaded[left].instrument, loaded[right].instrument) {
				continue
			}
			long, short := loaded[left], loaded[right]
			if long.current.Rate.GreaterThan(short.current.Rate) ||
				long.current.Rate.Equal(short.current.Rate) &&
					long.instrument.Key() > short.instrument.Key() {
				long, short = short, long
			}
			longHistory, shortHistory := pairedHistory(long.history, short.history)
			snapshot := opportunity.Evaluate(opportunity.Input{
				ObservedAt: now, Notional: engine.config.ReferenceNotional,
				Long:  opportunityLeg(long, longHistory),
				Short: opportunityLeg(short, shortHistory),
				Fees: opportunity.Fees{
					LongTakerRate:  engine.config.TakerFeeRate,
					ShortTakerRate: engine.config.TakerFeeRate,
				},
				LiquidationBufferPct:  engine.config.LiquidationBuffer,
				RequireHistoryPairing: true,
			})
			id := stableID("opp", engine.config.Mode.String(), snapshot.RouteID)
			symbol := normalizedSymbol(long.instrument)
			payload, err := opportunityPayload(id, symbol, snapshot)
			if err != nil {
				return nil, nil, err
			}
			status := opportunityStatus(snapshot)
			projections = append(projections, readmodel.Projection{
				ID: id, Status: status, Symbol: symbol,
				LongAccountID: long.instrument.AccountID, ShortAccountID: short.instrument.AccountID,
				NetAPR: snapshot.NetAPR, FormulaVersion: snapshot.FormulaVersion,
				ObservedAt: now, Payload: payload,
			})
			if _, exists := related[long.instrument.Key()]; !exists {
				related[long.instrument.Key()] = id
			}
			if _, exists := related[short.instrument.Key()]; !exists {
				related[short.instrument.Key()] = id
			}
		}
	}
	sort.Slice(projections, func(left, right int) bool {
		if !projections[left].NetAPR.Equal(projections[right].NetAPR) {
			return projections[left].NetAPR.GreaterThan(projections[right].NetAPR)
		}
		return projections[left].ID < projections[right].ID
	})
	return projections, related, nil
}

func (engine *Engine) buildAlerts(
	loaded []instrumentSnapshot,
	related map[string]string,
) ([]readmodel.AlertProjection, error) {
	result := make([]readmodel.AlertProjection, 0)
	for _, snapshot := range loaded {
		observedAt := observationTime(snapshot.current)
		history := make([]decimal.Decimal, 0, len(snapshot.history))
		for _, rate := range snapshot.history {
			history = append(history, rate.Rate)
		}
		alert, ok := alerts.Evaluate(alerts.Input{
			Token:                snapshot.instrument.BaseAsset,
			ExchangeID:           snapshot.instrument.Venue,
			Contract:             snapshot.instrument.Symbol,
			CurrentRate:          snapshot.current.Rate,
			History:              history,
			NextFundingAt:        snapshot.current.NextFundingTime,
			ObservedAt:           observedAt,
			RelatedOpportunityID: related[snapshot.instrument.Key()],
		})
		if !ok {
			continue
		}
		id := stableID("alert", engine.config.Mode.String(),
			snapshot.instrument.AccountID, snapshot.instrument.InstrumentID)
		payload, err := alertPayload(id, alert)
		if err != nil {
			return nil, err
		}
		result = append(result, readmodel.AlertProjection{
			Projection: readmodel.Projection{
				ID: id, Status: string(alert.Severity), Symbol: normalizedSymbol(snapshot.instrument),
				ObservedAt: observedAt, Payload: payload,
			},
			OpportunityID: alert.RelatedOpportunityID,
			AccountID:     snapshot.instrument.AccountID,
			InstrumentID:  snapshot.instrument.InstrumentID,
		})
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].ID < result[right].ID
	})
	return result, nil
}

func (engine *Engine) recordFailure(reason string) {
	engine.statusMu.Lock()
	engine.status.Ready = false
	engine.status.LastError = reason
	engine.statusMu.Unlock()
}

func compatible(mode config.Mode, left, right marketdata.Instrument) bool {
	return left.Key() != right.Key() &&
		!strings.EqualFold(left.Venue, right.Venue) &&
		mode.AllowsVenueEnvironment(left.VenueEnvironment) &&
		mode.AllowsVenueEnvironment(right.VenueEnvironment) &&
		strings.EqualFold(left.Product, "perp") &&
		strings.EqualFold(right.Product, "perp") &&
		strings.EqualFold(left.Settlement, right.Settlement) &&
		strings.EqualFold(left.BaseAsset, right.BaseAsset) &&
		strings.EqualFold(left.QuoteAsset, right.QuoteAsset)
}

func (engine *Engine) marketInstruments(instruments []marketdata.Instrument) []marketdata.Instrument {
	if len(engine.config.MarketSymbols) == 0 {
		return append([]marketdata.Instrument(nil), instruments...)
	}
	selected := make(map[string]struct{}, len(engine.config.MarketSymbols))
	for _, symbol := range engine.config.MarketSymbols {
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		if symbol == "*" {
			return append([]marketdata.Instrument(nil), instruments...)
		}
		if symbol != "" {
			selected[symbol] = struct{}{}
		}
	}
	result := make([]marketdata.Instrument, 0, len(instruments))
	for _, instrument := range instruments {
		if _, exists := selected[normalizedSymbol(instrument)]; exists {
			result = append(result, instrument)
		}
	}
	return result
}

func exactRouteInstrument(
	instruments []marketdata.Instrument,
	mode config.Mode,
	accountID string,
	instrumentID string,
) (marketdata.Instrument, error) {
	var match marketdata.Instrument
	matches := 0
	for _, instrument := range instruments {
		if instrument.AccountID != accountID || instrument.InstrumentID != instrumentID {
			continue
		}
		matches++
		match = instrument
	}
	if matches != 1 {
		return marketdata.Instrument{}, fmt.Errorf("matched %d instruments", matches)
	}
	if !mode.AllowsVenueEnvironment(match.VenueEnvironment) ||
		!strings.EqualFold(match.Product, "perp") ||
		match.AccountID == "" ||
		match.InstrumentID == "" ||
		match.Venue == "" ||
		match.VenueEnvironment == "" ||
		match.Settlement == "" ||
		match.BaseAsset == "" ||
		match.QuoteAsset == "" {
		return marketdata.Instrument{}, fmt.Errorf("instrument identity is incomplete or out of mode")
	}
	return match, nil
}

func pairedHistory(
	longRates []marketdata.FundingRate,
	shortRates []marketdata.FundingRate,
) ([]marketdata.FundingRate, []marketdata.FundingRate) {
	shortByTime := make(map[int64]marketdata.FundingRate, len(shortRates))
	for _, rate := range shortRates {
		if !rate.FundingTime.IsZero() {
			shortByTime[rate.FundingTime.UTC().Round(time.Second).UnixNano()] = rate
		}
	}
	var pairedLong, pairedShort []marketdata.FundingRate
	for _, longRate := range longRates {
		if longRate.FundingTime.IsZero() {
			continue
		}
		shortRate, ok := shortByTime[longRate.FundingTime.UTC().Round(time.Second).UnixNano()]
		if !ok {
			continue
		}
		pairedLong = append(pairedLong, longRate)
		pairedShort = append(pairedShort, shortRate)
	}
	return pairedLong, pairedShort
}

func opportunityLeg(
	snapshot instrumentSnapshot,
	history []marketdata.FundingRate,
) opportunity.Leg {
	points := make([]opportunity.FundingPoint, 0, len(history))
	for _, rate := range history {
		points = append(points, opportunity.FundingPoint{
			Rate: rate.Rate, ObservedAt: observationTime(rate),
			FundingTime: rate.FundingTime, NextFunding: rate.NextFundingTime,
		})
	}
	bids := make([]opportunity.Level, 0, len(snapshot.book.Bids))
	for _, level := range snapshot.book.Bids {
		bids = append(bids, opportunity.Level{Price: level.Price, Quantity: level.Quantity})
	}
	asks := make([]opportunity.Level, 0, len(snapshot.book.Asks))
	for _, level := range snapshot.book.Asks {
		asks = append(asks, opportunity.Level{Price: level.Price, Quantity: level.Quantity})
	}
	return opportunity.Leg{
		AccountID:        snapshot.instrument.AccountID,
		InstrumentID:     snapshot.instrument.InstrumentID,
		Venue:            snapshot.instrument.Venue,
		VenueEnvironment: snapshot.instrument.VenueEnvironment,
		Symbol:           snapshot.instrument.Symbol,
		Current: opportunity.FundingPoint{
			Rate: snapshot.current.Rate, ObservedAt: snapshot.current.ObservedAt,
			FundingTime: snapshot.current.FundingTime,
			NextFunding: snapshot.current.NextFundingTime,
		},
		History: historyPoints(points),
		MarketObservedAt: oldestObservation(
			snapshot.book.ObservedAt,
			snapshot.bbo.ObservedAt,
			snapshot.mark.ObservedAt,
		),
		MarkPrice: snapshot.mark.Price,
		BBO: opportunity.Quote{
			BidPrice: snapshot.bbo.Bid.Price, BidSize: snapshot.bbo.Bid.Quantity,
			AskPrice: snapshot.bbo.Ask.Price, AskSize: snapshot.bbo.Ask.Quantity,
		},
		Depth: opportunity.Depth{Bids: bids, Asks: asks},
	}
}

func historyPoints(points []opportunity.FundingPoint) []opportunity.FundingPoint {
	result := make([]opportunity.FundingPoint, len(points))
	copy(result, points)
	return result
}

func fundingObservations(loaded []instrumentSnapshot) []readmodel.FundingObservation {
	var result []readmodel.FundingObservation
	for _, snapshot := range loaded {
		rates := append(append([]marketdata.FundingRate(nil), snapshot.history...), snapshot.current)
		for _, rate := range rates {
			fundingTime := rate.FundingTime
			if fundingTime.IsZero() {
				fundingTime = rate.NextFundingTime
			}
			observedAt := observationTime(rate)
			if fundingTime.IsZero() || observedAt.IsZero() {
				continue
			}
			observation := readmodel.FundingObservation{
				AccountID:        snapshot.instrument.AccountID,
				InstrumentID:     snapshot.instrument.InstrumentID,
				VenueEnvironment: snapshot.instrument.VenueEnvironment,
				FundingTime:      fundingTime, ObservedAt: observedAt,
				NextFundingTime: rate.NextFundingTime, Rate: rate.Rate,
			}
			if rate.MarkPrice.Valid {
				observation.MarkPrice = rate.MarkPrice.Value
				observation.HasMarkPrice = true
			}
			result = append(result, observation)
		}
	}
	return result
}

func observationTime(rate marketdata.FundingRate) time.Time {
	if !rate.ObservedAt.IsZero() {
		return rate.ObservedAt.UTC()
	}
	return rate.FundingTime.UTC()
}

func oldestObservation(values ...time.Time) time.Time {
	var oldest time.Time
	for _, value := range values {
		if value.IsZero() {
			return time.Time{}
		}
		value = value.UTC()
		if oldest.IsZero() || value.Before(oldest) {
			oldest = value
		}
	}
	return oldest
}

func opportunityPayload(id, symbol string, snapshot opportunity.Snapshot) (json.RawMessage, error) {
	type finding struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	blockers := make([]finding, 0, len(snapshot.Blockers))
	for _, item := range snapshot.Blockers {
		blockers = append(blockers, finding{Code: string(item.Code), Message: item.Message})
	}
	warnings := make([]finding, 0, len(snapshot.Warnings))
	for _, item := range snapshot.Warnings {
		warnings = append(warnings, finding{Code: string(item.Code), Message: item.Message})
	}
	fundingSeries := make([]map[string]any, 0)
	pairs := min(len(snapshot.Long.History), len(snapshot.Short.History))
	for index := 0; index < pairs; index++ {
		at := snapshot.Long.History[index].FundingTime
		if at.IsZero() {
			continue
		}
		fundingSeries = append(fundingSeries, map[string]any{
			"at": at.UTC().Format(time.RFC3339Nano),
			"spreadPct": decimalNumber(snapshot.Short.History[index].Rate.
				Sub(snapshot.Long.History[index].Rate).Mul(decimal.NewFromInt(100))),
		})
	}
	payload := map[string]any{
		"opportunityId":     id,
		"symbol":            symbol,
		"longVenue":         snapshot.Long.Venue,
		"shortVenue":        snapshot.Short.Venue,
		"longVenueId":       snapshot.Long.Venue,
		"shortVenueId":      snapshot.Short.Venue,
		"longAccountId":     snapshot.Long.AccountID,
		"shortAccountId":    snapshot.Short.AccountID,
		"longInstrumentId":  snapshot.Long.InstrumentID,
		"shortInstrumentId": snapshot.Short.InstrumentID,
		"grossAprPct":       decimalNumber(snapshot.GrossAPR.Mul(decimal.NewFromInt(100))),
		"netAprPct":         decimalNumber(snapshot.NetAPR.Mul(decimal.NewFromInt(100))),
		"stabilityPct":      decimalNumber(snapshot.Stability),
		"capacityUsd":       snapshot.CapacityNotional.Round(2).StringFixed(2),
		"roundTripFeeBps": decimalNumber(snapshot.RoundTripFees.
			Div(snapshot.Calculation.RequestedNotional).Mul(decimal.NewFromInt(10000))),
		"estimatedSlippageBps": decimalNumber(snapshot.Slippage.
			Div(snapshot.Calculation.RequestedNotional).Mul(decimal.NewFromInt(10000))),
		"basisPct":       decimalNumber(snapshot.Basis.Mul(decimal.NewFromInt(100))),
		"dataAgeSeconds": snapshot.DataAge.Seconds(),
		"nextFundingAt":  formatTime(snapshot.NextFunding),
		"blockers":       blockers,
		"warnings":       warnings,
		"formulaVersion": snapshot.FormulaVersion,
		"observedAt":     snapshot.ObservedAt.UTC().Format(time.RFC3339Nano),
		"status":         opportunityStatus(snapshot),
		"fundingSeries":  fundingSeries,
		"calculationInputs": map[string]any{
			"longRate":             snapshot.Calculation.LongRate.String(),
			"shortRate":            snapshot.Calculation.ShortRate.String(),
			"spreadPerPeriod":      snapshot.Calculation.SpreadPerPeriod.String(),
			"periodsPerYear":       snapshot.Calculation.PeriodsPerYear.String(),
			"requestedNotionalUsd": snapshot.Calculation.RequestedNotional.StringFixed(2),
			"capacityUsagePct":     decimalNumber(snapshot.Calculation.CapacityUsagePercent),
			"longTakerFeeRate":     snapshot.Calculation.LongTakerFeeRate.String(),
			"shortTakerFeeRate":    snapshot.Calculation.ShortTakerFeeRate.String(),
			"liquidationBufferPct": snapshot.Calculation.LiquidationBufferPct.String(),
			"longLeg":              calculationLegPayload(snapshot.Long),
			"shortLeg":             calculationLegPayload(snapshot.Short),
		},
	}
	return json.Marshal(payload)
}

func calculationLegPayload(leg opportunity.Leg) map[string]any {
	return map[string]any{
		"accountId":        leg.AccountID,
		"instrumentId":     leg.InstrumentID,
		"venue":            leg.Venue,
		"venueEnvironment": leg.VenueEnvironment,
		"venueSymbol":      leg.Symbol,
		"marketObservedAt": formatTime(leg.MarketObservedAt),
		"markPrice":        leg.MarkPrice.String(),
		"bbo": map[string]any{
			"bidPrice": leg.BBO.BidPrice.String(),
			"bidSize":  leg.BBO.BidSize.String(),
			"askPrice": leg.BBO.AskPrice.String(),
			"askSize":  leg.BBO.AskSize.String(),
		},
		"depth": map[string]any{
			"bids": levelPayloads(leg.Depth.Bids),
			"asks": levelPayloads(leg.Depth.Asks),
		},
		"currentFunding": fundingPointPayload(leg.Current),
		"fundingHistory": fundingPointPayloads(leg.History),
	}
}

func levelPayloads(levels []opportunity.Level) []map[string]string {
	payloads := make([]map[string]string, 0, len(levels))
	for _, level := range levels {
		payloads = append(payloads, map[string]string{
			"price": level.Price.String(), "quantity": level.Quantity.String(),
		})
	}
	return payloads
}

func fundingPointPayloads(points []opportunity.FundingPoint) []map[string]any {
	payloads := make([]map[string]any, 0, len(points))
	for _, point := range points {
		payloads = append(payloads, fundingPointPayload(point))
	}
	return payloads
}

func fundingPointPayload(point opportunity.FundingPoint) map[string]any {
	return map[string]any{
		"rate":            point.Rate.String(),
		"observedAt":      formatTime(point.ObservedAt),
		"fundingTime":     formatTime(point.FundingTime),
		"nextFundingTime": formatTime(point.NextFunding),
	}
}

func alertPayload(id string, alert alerts.Alert) (json.RawMessage, error) {
	var related any
	if alert.RelatedOpportunityID != "" {
		related = alert.RelatedOpportunityID
	}
	return json.Marshal(map[string]any{
		"alertId": id, "token": alert.Token,
		"exchangeId": alert.ExchangeID, "contract": alert.Contract,
		"currentRatePct":       decimalNumber(alert.CurrentRatePct),
		"thresholdLabel":       alert.Rule.Label,
		"changePct":            decimalNumber(alert.ChangePct),
		"severity":             string(alert.Severity),
		"nextFundingAt":        formatTime(alert.NextFundingAt),
		"relatedOpportunityId": related,
		"observedAt":           alert.ObservedAt.UTC().Format(time.RFC3339Nano),
	})
}

func opportunityStatus(snapshot opportunity.Snapshot) string {
	if snapshot.Blocked() {
		return "blocked"
	}
	if len(snapshot.Warnings) > 0 {
		return "attention"
	}
	return "executable"
}

func stableID(prefix string, values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(sum[:18])
}

func normalizedSymbol(instrument marketdata.Instrument) string {
	if instrument.BaseAsset != "" && instrument.QuoteAsset != "" {
		return strings.ToUpper(instrument.BaseAsset) + "-" + strings.ToUpper(instrument.QuoteAsset)
	}
	return instrument.Symbol
}

func decimalNumber(value decimal.Decimal) json.Number {
	return json.Number(value.Round(8).String())
}

func formatTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
