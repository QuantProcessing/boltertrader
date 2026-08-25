package factoryclient

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
	asterperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

type closeSlot struct {
	mu    sync.Mutex
	close func()
}

func (slot *closeSlot) set(closeFn func()) {
	slot.mu.Lock()
	slot.close = closeFn
	slot.mu.Unlock()
}

func (slot *closeSlot) run() error {
	slot.mu.Lock()
	closeFn := slot.close
	slot.close = nil
	slot.mu.Unlock()
	if closeFn != nil {
		closeFn()
	}
	return nil
}

type binanceDerivativeWatcher struct {
	settings Settings
	aster    bool
	hotMu    sync.RWMutex
	hot      map[string]bool
	hotWake  chan struct{}
	closeSlot
}

func (watcher *binanceDerivativeWatcher) setHotInstrument(nativeSymbol string, active bool) {
	watcher.hotMu.Lock()
	if watcher.hot == nil {
		watcher.hot = make(map[string]bool)
	}
	if watcher.hotWake == nil {
		watcher.hotWake = make(chan struct{}, 1)
	}
	if active {
		watcher.hot[nativeSymbol] = true
	} else {
		delete(watcher.hot, nativeSymbol)
	}
	wake := watcher.hotWake
	watcher.hotMu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

type openInterestSample struct {
	value      string
	observedAt time.Time
}

func (watcher *binanceDerivativeWatcher) stream(ctx context.Context, bootstrap nativeDerivativePatch, emit func(nativeDerivativePatch)) error {
	identities := derivativeIdentityByNative(bootstrap.Facts)
	marks := derivativeMarkByNative(bootstrap.Facts)
	var marksMu sync.RWMutex

	if watcher.aster {
		return watcher.streamAster(ctx, bootstrap.Scope, identities, marks, &marksMu, emit)
	}
	profile := binanceperp.MainnetEndpointProfile()
	if watcher.settings.WebSocketEndpoint != "" {
		profile.WSPublicBaseURL = watcher.settings.WebSocketEndpoint
		profile.WSMarketBaseURL = watcher.settings.WebSocketEndpoint
		profile.WSMarketFallbackBaseURL = watcher.settings.WebSocketEndpoint
	}
	client := binanceperp.NewWsMarketClientWithEndpointProfile(ctx, profile)
	watcher.set(client.Close)
	defer watcher.run()
	if err := client.Connect(); err != nil {
		return err
	}
	if err := client.SubscribeAllMarkPrice("1s", func(events []*binanceperp.WsMarkPriceEvent) error {
		for _, event := range events {
			if event == nil {
				continue
			}
			identity, ok := identities[event.Symbol]
			if !ok {
				continue
			}
			observedAt := fundingMillisOrNow(event.EventTime)
			fact, fields := markStreamFact(identity, event.FundingRate, event.MarkPrice, event.IndexPrice, event.NextFundingTime, observedAt)
			if fact.MarkPrice.Valid {
				marksMu.Lock()
				marks[event.Symbol] = fact.MarkPrice
				marksMu.Unlock()
			}
			if fields != 0 {
				emit(derivativeDelta(bootstrap.Scope, observedAt, fields, fact))
			}
		}
		return nil
	}); err != nil {
		return err
	}
	rest := newBinancePerpPublicSDK(watcher.settings)
	loadOpenInterest := func(callCtx context.Context, symbol string) (string, time.Time, error) {
		sample, err := coordinatedMarketRequest(callCtx, watcher.settings, exchange.VenueBinance, "open_interest", symbol, func(requestCtx context.Context) (openInterestSample, error) {
			row, requestErr := rest.GetOpenInterest(requestCtx, symbol)
			if requestErr != nil || row == nil {
				return openInterestSample{}, requestErr
			}
			return openInterestSample{value: row.OpenInterest, observedAt: fundingMillisOrNow(row.Time)}, nil
		})
		return sample.value, sample.observedAt, err
	}
	go runOpenInterestEnrichment(ctx, bootstrap.Scope, identities, marks, &marksMu, emit, loadOpenInterest)
	go watcher.runHotOpenInterest(ctx, bootstrap.Scope, identities, marks, &marksMu, emit, loadOpenInterest)
	<-ctx.Done()
	return ctx.Err()
}

func (watcher *binanceDerivativeWatcher) streamAster(ctx context.Context, scope exchange.CatalogScope, identities map[string]exchange.InstrumentIdentity, marks map[string]exchange.OptionalDecimal, marksMu *sync.RWMutex, emit func(nativeDerivativePatch)) error {
	profile, err := astercommon.NewProfile(astercommon.ProductPerp)
	if err != nil {
		return err
	}
	profile, err = profile.WithEndpointOverrides(watcher.settings.Endpoint, watcher.settings.WebSocketEndpoint, watcher.settings.WebSocketEndpoint)
	if err != nil {
		return err
	}
	client, err := asterperp.NewWsMarketClient(ctx, profile)
	if err != nil {
		return err
	}
	watcher.set(client.Close)
	defer watcher.run()
	if err := client.Connect(); err != nil {
		return err
	}
	if err := client.SubscribeAllMarkPrice("1s", func(events []*asterperp.WsMarkPriceEvent) error {
		for _, event := range events {
			if event == nil {
				continue
			}
			identity, ok := identities[event.Symbol]
			if !ok {
				continue
			}
			observedAt := fundingMillisOrNow(event.EventTime)
			fact, fields := markStreamFact(identity, event.FundingRate, event.MarkPrice, event.IndexPrice, event.NextFundingTime, observedAt)
			if fact.MarkPrice.Valid {
				marksMu.Lock()
				marks[event.Symbol] = fact.MarkPrice
				marksMu.Unlock()
			}
			if fields != 0 {
				emit(derivativeDelta(scope, observedAt, fields, fact))
			}
		}
		return nil
	}); err != nil {
		return err
	}
	rest, err := newAsterPerpPublicSDK(watcher.settings)
	if err != nil {
		return err
	}
	loadOpenInterest := func(callCtx context.Context, symbol string) (string, time.Time, error) {
		sample, requestErr := coordinatedMarketRequest(callCtx, watcher.settings, exchange.VenueAster, "open_interest", symbol, func(requestCtx context.Context) (openInterestSample, error) {
			row, loadErr := rest.GetOpenInterest(requestCtx, symbol)
			if loadErr != nil || row == nil {
				return openInterestSample{}, loadErr
			}
			return openInterestSample{value: row.OpenInterest, observedAt: fundingMillisOrNow(row.Time)}, nil
		})
		return sample.value, sample.observedAt, requestErr
	}
	go runOpenInterestEnrichment(ctx, scope, identities, marks, marksMu, emit, loadOpenInterest)
	go watcher.runHotOpenInterest(ctx, scope, identities, marks, marksMu, emit, loadOpenInterest)
	<-ctx.Done()
	return ctx.Err()
}

func (watcher *binanceDerivativeWatcher) close() error { return watcher.run() }

func (watcher *binanceDerivativeWatcher) runHotOpenInterest(ctx context.Context, scope exchange.CatalogScope, identities map[string]exchange.InstrumentIdentity, marks map[string]exchange.OptionalDecimal, marksMu *sync.RWMutex, emit func(nativeDerivativePatch), load func(context.Context, string) (string, time.Time, error)) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-watcher.hotWakeChannel():
		}
		hot := watcher.hotIdentities(identities)
		if len(hot) > 0 {
			runOpenInterestEnrichmentRound(ctx, scope, hot, marks, marksMu, emit, load, 4, 25*time.Millisecond)
		}
	}
}

func (watcher *binanceDerivativeWatcher) hotWakeChannel() <-chan struct{} {
	watcher.hotMu.Lock()
	if watcher.hotWake == nil {
		watcher.hotWake = make(chan struct{}, 1)
	}
	wake := watcher.hotWake
	watcher.hotMu.Unlock()
	return wake
}

func (watcher *binanceDerivativeWatcher) hotIdentities(all map[string]exchange.InstrumentIdentity) map[string]exchange.InstrumentIdentity {
	watcher.hotMu.RLock()
	hot := make(map[string]exchange.InstrumentIdentity, len(watcher.hot))
	for native := range watcher.hot {
		if identity, ok := all[native]; ok {
			hot[native] = identity
		}
	}
	watcher.hotMu.RUnlock()
	return hot
}

type lighterDerivativeWatcher struct {
	settings Settings
	closeSlot
}

func (watcher *lighterDerivativeWatcher) stream(ctx context.Context, bootstrap nativeDerivativePatch, emit func(nativeDerivativePatch)) error {
	identities := make(map[int]exchange.InstrumentIdentity, len(bootstrap.Facts))
	for _, fact := range bootstrap.Facts {
		id, err := strconv.Atoi(fact.Instrument.NativeID)
		if err == nil {
			identities[id] = fact.Instrument
		}
	}
	config := lighter.WSConfig{ReadOnly: true, Encoding: lighter.WSEncodingJSON}
	if watcher.settings.WebSocketEndpoint != "" {
		config.URL = watcher.settings.WebSocketEndpoint
	}
	client := lighter.NewWebsocketClientWithConfig(ctx, config)
	watcher.set(client.Close)
	defer watcher.run()
	if err := client.Connect(); err != nil {
		return err
	}
	if err := client.SubscribeAllMarketStats(func(payload []byte) {
		for _, row := range decodeLighterMarketStats(payload) {
			identity, ok := identities[row.MarketID]
			if !ok {
				continue
			}
			observedAt := fundingMillisOrNow(row.Timestamp)
			fact, fields := lighterMarketStatFact(identity, row, observedAt)
			if fields != 0 {
				emit(derivativeDelta(bootstrap.Scope, observedAt, fields, fact))
			}
		}
	}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (watcher *lighterDerivativeWatcher) close() error { return watcher.run() }

type hyperliquidDerivativeWatcher struct {
	settings Settings
	closeSlot
}

func (watcher *hyperliquidDerivativeWatcher) stream(ctx context.Context, bootstrap nativeDerivativePatch, emit func(nativeDerivativePatch)) error {
	identities := hyperliquidFundingIdentities(bootstrap.Facts)
	base := hyperliquid.NewWebsocketClient(ctx)
	if watcher.settings.WebSocketEndpoint != "" {
		base.WithURL(watcher.settings.WebSocketEndpoint)
	}
	client := hyperliquidperp.NewWebsocketClient(base)
	watcher.set(base.Close)
	defer watcher.run()
	if err := base.Connect(); err != nil {
		return err
	}
	if err := client.SubscribeAllDexsAssetCtxs(func(event hyperliquid.WsAllDexsAssetCtxs) {
		observedAt := time.Now().UTC()
		for dex, rows := range event.Ctxs {
			marketIdentities := identities[dex]
			for index, row := range rows {
				if index >= len(marketIdentities) || marketIdentities[index].NativeSymbol == "" {
					continue
				}
				identity := marketIdentities[index]
				fact := nativeDerivativeFact{
					Instrument:  identity,
					FundingRate: fundingOptionalSigned(row.Funding), MarkPrice: fundingOptionalDecimal(row.MarkPx),
					IndexPrice: fundingOptionalDecimal(row.OraclePx), OpenInterest: fundingOptionalNonNegative(row.OpenInterest),
					FundingObservedAt: observedAt, PriceObservedAt: observedAt, OpenInterestObservedAt: observedAt,
				}
				fact.OpenInterestNotional = fundingOpenInterestNotional(row.OpenInterest, row.MarkPx, decimal.NewFromInt(1))
				if fact.OpenInterestNotional.Valid {
					fact.OpenInterestNotionalAsset = identity.SettleAsset
				}
				fields := nativeDerivativeFieldFunding | nativeDerivativeFieldMarkPrice |
					nativeDerivativeFieldIndexPrice | nativeDerivativeFieldOpenInterest
				if fact.OpenInterestNotional.Valid {
					fields |= nativeDerivativeFieldOpenInterestNotional
				}
				emit(derivativeDelta(bootstrap.Scope, observedAt, fields, fact))
			}
		}
	}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (watcher *hyperliquidDerivativeWatcher) close() error { return watcher.run() }

func markStreamFact(identity exchange.InstrumentIdentity, rate, mark, index string, nextFundingMillis int64, observedAt time.Time) (nativeDerivativeFact, nativeDerivativeField) {
	fact := nativeDerivativeFact{Instrument: identity}
	fields := nativeDerivativeField(0)
	if parsed, err := decimalOrSkip(rate); err == nil {
		fact.FundingRate = exchange.OptionalDecimal{Value: parsed, Valid: true}
		fact.FundingObservedAt = observedAt
		fields |= nativeDerivativeFieldFunding
	}
	if parsed := fundingOptionalDecimal(mark); parsed.Valid {
		fact.MarkPrice = parsed
		fact.PriceObservedAt = observedAt
		fields |= nativeDerivativeFieldMarkPrice
	}
	if parsed := fundingOptionalDecimal(index); parsed.Valid {
		fact.IndexPrice = parsed
		fact.PriceObservedAt = observedAt
		fields |= nativeDerivativeFieldIndexPrice
	}
	if nextFundingMillis > 0 {
		fact.NextFundingTime = time.UnixMilli(nextFundingMillis).UTC()
		fields |= nativeDerivativeFieldNextFundingTime
	}
	return fact, fields
}

func derivativeDelta(scope exchange.CatalogScope, observedAt time.Time, fields nativeDerivativeField, facts ...nativeDerivativeFact) nativeDerivativePatch {
	return nativeDerivativePatch{Kind: nativeDerivativePatchDelta, Scope: scope, ObservedAt: observedAt, Fields: fields, Facts: facts}
}

func derivativeIdentityByNative(facts []nativeDerivativeFact) map[string]exchange.InstrumentIdentity {
	result := make(map[string]exchange.InstrumentIdentity, len(facts))
	for _, fact := range facts {
		result[fact.Instrument.NativeSymbol] = fact.Instrument
	}
	return result
}

func derivativeMarkByNative(facts []nativeDerivativeFact) map[string]exchange.OptionalDecimal {
	result := make(map[string]exchange.OptionalDecimal, len(facts))
	for _, fact := range facts {
		if fact.MarkPrice.Valid {
			result[fact.Instrument.NativeSymbol] = fact.MarkPrice
		}
	}
	return result
}

func fundingOptionalSigned(value string) exchange.OptionalDecimal {
	parsed, err := decimalOrSkip(value)
	return exchange.OptionalDecimal{Value: parsed, Valid: err == nil}
}

func fundingMillisOrNow(millis int64) time.Time {
	if millis > 0 {
		return time.UnixMilli(millis).UTC()
	}
	return time.Now().UTC()
}

func runOpenInterestEnrichment(ctx context.Context, scope exchange.CatalogScope, identities map[string]exchange.InstrumentIdentity, marks map[string]exchange.OptionalDecimal, marksMu *sync.RWMutex, emit func(nativeDerivativePatch), load func(context.Context, string) (string, time.Time, error)) {
	runOpenInterestEnrichmentRound(ctx, scope, identities, marks, marksMu, emit, load, 4, 25*time.Millisecond)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOpenInterestEnrichmentRound(ctx, scope, identities, marks, marksMu, emit, load, 4, 25*time.Millisecond)
		}
	}
}

func runOpenInterestEnrichmentRound(ctx context.Context, scope exchange.CatalogScope, identities map[string]exchange.InstrumentIdentity, marks map[string]exchange.OptionalDecimal, marksMu *sync.RWMutex, emit func(nativeDerivativePatch), load func(context.Context, string) (string, time.Time, error), workerLimit int, pace time.Duration) {
	if workerLimit < 1 {
		workerLimit = 1
	}
	symbols := make([]string, 0, len(identities))
	for symbol := range identities {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	jobs := make(chan string)
	var workers sync.WaitGroup
	var failures atomic.Int64
	for range workerLimit {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for symbol := range jobs {
				callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				raw, observedAt, err := load(callCtx, symbol)
				cancel()
				if err != nil || raw == "" {
					if ctx.Err() == nil {
						failures.Add(1)
					}
					continue
				}
				oi := fundingOptionalNonNegative(raw)
				if !oi.Valid {
					failures.Add(1)
					continue
				}
				if observedAt.IsZero() {
					observedAt = time.Now().UTC()
				}
				identity := identities[symbol]
				fact := nativeDerivativeFact{Instrument: identity, OpenInterest: oi, OpenInterestObservedAt: observedAt}
				fields := nativeDerivativeFieldOpenInterest
				marksMu.RLock()
				mark := marks[symbol]
				marksMu.RUnlock()
				if mark.Valid {
					fact.OpenInterestNotional = exchange.OptionalDecimal{Value: oi.Value.Mul(mark.Value), Valid: true}
					fact.OpenInterestNotionalAsset = identity.SettleAsset
					fields |= nativeDerivativeFieldOpenInterestNotional
				}
				emit(derivativeDelta(scope, observedAt, fields, fact))
			}
		}()
	}

	var limiter *time.Ticker
	if pace > 0 {
		limiter = time.NewTicker(pace)
		defer limiter.Stop()
	}
	for _, symbol := range symbols {
		if limiter != nil {
			select {
			case <-ctx.Done():
				close(jobs)
				workers.Wait()
				return
			case <-limiter.C:
			}
		}
		select {
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		case jobs <- symbol:
		}
	}
	close(jobs)
	workers.Wait()
	if count := failures.Load(); count > 0 && ctx.Err() == nil {
		slog.WarnContext(ctx, "funding open-interest enrichment incomplete",
			"venue", scope.Venue, "settlement", scope.Settlement, "failed_symbols", count)
	}
}

type lighterMarketStat struct {
	MarketID           int    `json:"market_id"`
	IndexPrice         string `json:"index_price"`
	MarkPrice          string `json:"mark_price"`
	OpenInterest       string `json:"open_interest"`
	CurrentFundingRate string `json:"current_funding_rate"`
	FundingRate        string `json:"funding_rate"`
	FundingTimestamp   int64  `json:"funding_timestamp"`
	Timestamp          int64  `json:"timestamp"`
}

func lighterMarketStatFact(identity exchange.InstrumentIdentity, row lighterMarketStat, observedAt time.Time) (nativeDerivativeFact, nativeDerivativeField) {
	fact := nativeDerivativeFact{Instrument: identity}
	fields := nativeDerivativeField(0)
	rate := row.CurrentFundingRate
	if rate == "" {
		rate = row.FundingRate
	}
	if parsed, err := decimalOrSkip(rate); err == nil {
		fact.FundingRate = exchange.OptionalDecimal{Value: parsed, Valid: true}
		fact.FundingIntervalHours = exchange.OptionalInt{Value: lighterFundingQuoteHours, Valid: true}
		fact.FundingObservedAt = observedAt
		fields |= nativeDerivativeFieldFunding | nativeDerivativeFieldFundingInterval
	}
	fact.MarkPrice = fundingOptionalDecimal(row.MarkPrice)
	fact.IndexPrice = fundingOptionalDecimal(row.IndexPrice)
	if fact.MarkPrice.Valid || fact.IndexPrice.Valid {
		fact.PriceObservedAt = observedAt
		fields |= nativeDerivativeFieldMarkPrice | nativeDerivativeFieldIndexPrice
	}
	fact.OpenInterest = fundingOptionalNonNegative(row.OpenInterest)
	fact.OpenInterestNotional = fundingOpenInterestNotional(row.OpenInterest, row.MarkPrice, decimal.NewFromInt(1))
	if fact.OpenInterest.Valid {
		fact.OpenInterestObservedAt = observedAt
		fields |= nativeDerivativeFieldOpenInterest
	}
	if fact.OpenInterestNotional.Valid {
		fact.OpenInterestNotionalAsset = identity.SettleAsset
		fields |= nativeDerivativeFieldOpenInterestNotional
	}
	if row.FundingTimestamp > 0 {
		fact.NextFundingTime = time.UnixMilli(row.FundingTimestamp).UTC()
		fields |= nativeDerivativeFieldNextFundingTime
	}
	return fact, fields
}

func decodeLighterMarketStats(payload []byte) []lighterMarketStat {
	var envelope struct {
		Timestamp   int64           `json:"timestamp"`
		MarketStats json.RawMessage `json:"market_stats"`
	}
	if json.Unmarshal(payload, &envelope) != nil || len(envelope.MarketStats) == 0 {
		return nil
	}
	var single lighterMarketStat
	if json.Unmarshal(envelope.MarketStats, &single) == nil && single.MarketID != 0 {
		single.Timestamp = envelope.Timestamp
		return []lighterMarketStat{single}
	}
	var list []lighterMarketStat
	if json.Unmarshal(envelope.MarketStats, &list) == nil && len(list) > 0 {
		for index := range list {
			list[index].Timestamp = envelope.Timestamp
		}
		return list
	}
	var byID map[string]lighterMarketStat
	if json.Unmarshal(envelope.MarketStats, &byID) != nil {
		return nil
	}
	list = make([]lighterMarketStat, 0, len(byID))
	for key, row := range byID {
		if row.MarketID == 0 {
			row.MarketID, _ = strconv.Atoi(key)
		}
		row.Timestamp = envelope.Timestamp
		list = append(list, row)
	}
	return list
}

func hyperliquidFundingIdentities(facts []nativeDerivativeFact) map[string][]exchange.InstrumentIdentity {
	result := map[string][]exchange.InstrumentIdentity{}
	for _, fact := range facts {
		assetID, err := strconv.Atoi(fact.Instrument.NativeID)
		if err != nil {
			continue
		}
		dex := ""
		index := assetID
		if prefix, _, ok := strings.Cut(fact.Instrument.NativeSymbol, ":"); ok {
			dex = prefix
			if assetID >= hyperliquidBuilderAssetBase {
				index = (assetID - hyperliquidBuilderAssetBase) % hyperliquidBuilderAssetStride
			}
		}
		for len(result[dex]) <= index {
			result[dex] = append(result[dex], exchange.InstrumentIdentity{})
		}
		result[dex][index] = fact.Instrument
	}
	return result
}
