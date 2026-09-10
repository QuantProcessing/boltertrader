package factoryclient

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	bybit "github.com/QuantProcessing/boltertrader/sdk/bybit"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	nadosdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func bybitTickerFact(identity exchange.InstrumentIdentity, row bybit.Ticker, observedAt time.Time) (nativeDerivativeFact, nativeDerivativeField) {
	fact := nativeDerivativeFact{Instrument: identity}
	fields := nativeDerivativeField(0)
	if row.FundingRate != "" {
		fact.FundingRate = fundingOptionalSigned(row.FundingRate)
		if fact.FundingRate.Valid {
			fact.FundingObservedAt = observedAt
			fields |= nativeDerivativeFieldFunding
		}
	}
	if row.FundingIntervalHour != "" {
		fact.FundingIntervalHours = fundingIntervalHours(row.FundingIntervalHour)
		if fact.FundingIntervalHours.Valid {
			fields |= nativeDerivativeFieldFundingInterval
		}
	}
	if row.NextFundingTime != "" {
		fact.NextFundingTime = fundingUnixMillis(row.NextFundingTime)
		if !fact.NextFundingTime.IsZero() {
			fields |= nativeDerivativeFieldNextFundingTime
		}
	}
	if row.MarkPrice != "" {
		fact.MarkPrice = fundingOptionalDecimal(row.MarkPrice)
		if fact.MarkPrice.Valid {
			fact.PriceObservedAt = observedAt
			fields |= nativeDerivativeFieldMarkPrice
		}
	}
	if row.IndexPrice != "" {
		fact.IndexPrice = fundingOptionalDecimal(row.IndexPrice)
		if fact.IndexPrice.Valid {
			fact.PriceObservedAt = observedAt
			fields |= nativeDerivativeFieldIndexPrice
		}
	}
	if row.OpenInterest != "" {
		fact.OpenInterest = fundingOptionalNonNegative(row.OpenInterest)
		if fact.OpenInterest.Valid {
			fact.OpenInterestObservedAt = observedAt
			fields |= nativeDerivativeFieldOpenInterest
		}
		fact.OpenInterestNotional = fundingOpenInterestNotional(row.OpenInterest, row.MarkPrice, decimal.NewFromInt(1))
		if fact.OpenInterestNotional.Valid {
			fact.OpenInterestNotionalAsset = identity.SettleAsset
			fields |= nativeDerivativeFieldOpenInterestNotional
		}
	}
	return fact, fields
}

func bitgetTickerFact(identity exchange.InstrumentIdentity, row bitget.Ticker, observedAt time.Time) (nativeDerivativeFact, nativeDerivativeField) {
	fact := nativeDerivativeFact{Instrument: identity}
	fields := nativeDerivativeField(0)
	if row.FundingRate != "" {
		fact.FundingRate = fundingOptionalSigned(row.FundingRate)
		if fact.FundingRate.Valid {
			fact.FundingObservedAt = observedAt
			fields |= nativeDerivativeFieldFunding
		}
	}
	if row.FundingRateInterval != "" {
		fact.FundingIntervalHours = fundingIntervalHours(row.FundingRateInterval)
		if fact.FundingIntervalHours.Valid {
			fields |= nativeDerivativeFieldFundingInterval
		}
	}
	if row.NextFundingTime != "" {
		fact.NextFundingTime = fundingUnixMillis(row.NextFundingTime)
		if !fact.NextFundingTime.IsZero() {
			fields |= nativeDerivativeFieldNextFundingTime
		}
	}
	if row.MarkPrice != "" {
		fact.MarkPrice = fundingOptionalDecimal(row.MarkPrice)
		if fact.MarkPrice.Valid {
			fact.PriceObservedAt = observedAt
			fields |= nativeDerivativeFieldMarkPrice
		}
	}
	if row.IndexPrice != "" {
		fact.IndexPrice = fundingOptionalDecimal(row.IndexPrice)
		if fact.IndexPrice.Valid {
			fact.PriceObservedAt = observedAt
			fields |= nativeDerivativeFieldIndexPrice
		}
	}
	openInterest := firstNonEmpty(row.OpenInterest, row.HoldingAmount)
	if openInterest != "" {
		fact.OpenInterest = fundingOptionalNonNegative(openInterest)
		if fact.OpenInterest.Valid {
			fact.OpenInterestObservedAt = observedAt
			fields |= nativeDerivativeFieldOpenInterest
		}
		fact.OpenInterestNotional = fundingOpenInterestNotional(openInterest, row.MarkPrice, decimal.NewFromInt(1))
		if fact.OpenInterestNotional.Valid {
			fact.OpenInterestNotionalAsset = identity.SettleAsset
			fields |= nativeDerivativeFieldOpenInterestNotional
		}
	}
	return fact, fields
}

type gateDerivativeWatcher struct {
	scope    exchange.CatalogScope
	loader   DerivativeSnapshotLoader
	settings Settings
	closeSlot
}

func (watcher *gateDerivativeWatcher) stream(ctx context.Context, bootstrap nativeDerivativePatch, emit func(nativeDerivativePatch)) error {
	profile := gate.MainnetEnvironmentProfile()
	if watcher.settings.WebSocketEndpoint != "" {
		profile.FuturesUSDTWSURL = watcher.settings.WebSocketEndpoint
	}
	client, err := gate.NewWSClientWithProfile(profile, gate.ProductFuturesUSDT)
	if err != nil {
		return err
	}
	watcher.set(func() { _ = client.Close() })
	defer watcher.run()
	if err := client.Connect(ctx); err != nil {
		return err
	}
	var factsMu sync.RWMutex
	identities, multipliers, payload := gateSubscriptionFacts(bootstrap.Facts)
	handler := func(raw json.RawMessage) {
		var envelope gate.WSEnvelope
		if json.Unmarshal(raw, &envelope) != nil {
			return
		}
		var rows []gate.FuturesTicker
		if json.Unmarshal(envelope.Result, &rows) != nil {
			var row gate.FuturesTicker
			if json.Unmarshal(envelope.Result, &row) != nil {
				return
			}
			rows = []gate.FuturesTicker{row}
		}
		observedAt := fundingGateTime(envelope)
		batches := make(map[nativeDerivativeField]*nativeDerivativeBatch)
		for _, row := range rows {
			factsMu.RLock()
			identity, ok := identities[row.Contract]
			multiplier := multipliers[row.Contract]
			factsMu.RUnlock()
			if !ok {
				continue
			}
			fact, fields := gateTickerFact(identity, row, multiplier, observedAt)
			if fields != 0 {
				appendNativeDerivativeBatch(batches, observedAt, fields, fact)
			}
		}
		emitNativeDerivativeBatches(bootstrap.Scope, batches, emit)
	}
	if err := client.Subscribe(ctx, "futures.tickers", payload, handler); err != nil {
		return err
	}
	refresh := time.NewTicker(time.Minute)
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-refresh.C:
			snapshot, loadErr := loadDerivativeSnapshot(ctx, watcher.scope, watcher.loader, watcher.settings)
			if loadErr != nil {
				return loadErr
			}
			patch := nativeDerivativePatchFromSnapshot(snapshot)
			nextIdentities, nextMultipliers, nextPayload := gateSubscriptionFacts(patch.Facts)
			if sameStrings(payload, nextPayload) {
				emit(patch)
				continue
			}
			// Gate allows subscribe/unsubscribe updates on the live client. Replace
			// only the topic payload when catalog membership changes; the owner,
			// socket and generation remain intact.
			if unsubscribeErr := client.Unsubscribe(ctx, "futures.tickers", payload); unsubscribeErr != nil {
				return unsubscribeErr
			}
			if subscribeErr := client.Subscribe(ctx, "futures.tickers", nextPayload, handler); subscribeErr != nil {
				return subscribeErr
			}
			factsMu.Lock()
			identities, multipliers, payload = nextIdentities, nextMultipliers, nextPayload
			factsMu.Unlock()
			emit(patch)
		}
	}
}

func (watcher *gateDerivativeWatcher) close() error { return watcher.run() }

func gateSubscriptionFacts(facts []nativeDerivativeFact) (map[string]exchange.InstrumentIdentity, map[string]decimal.Decimal, []string) {
	identities := derivativeIdentityByNative(facts)
	multipliers := make(map[string]decimal.Decimal, len(facts))
	payload := make([]string, 0, len(identities))
	for _, fact := range facts {
		multiplier := decimal.NewFromInt(1)
		if fact.OpenInterest.Valid && fact.OpenInterestNotional.Valid && fact.MarkPrice.Valid && fact.OpenInterest.Value.IsPositive() {
			multiplier = fact.OpenInterestNotional.Value.Div(fact.OpenInterest.Value).Div(fact.MarkPrice.Value)
		}
		multipliers[fact.Instrument.NativeSymbol] = multiplier
	}
	for native := range identities {
		payload = append(payload, native)
	}
	sort.Strings(payload)
	return identities, multipliers, payload
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func gateTickerFact(identity exchange.InstrumentIdentity, row gate.FuturesTicker, multiplier decimal.Decimal, observedAt time.Time) (nativeDerivativeFact, nativeDerivativeField) {
	fact := nativeDerivativeFact{Instrument: identity}
	fields := nativeDerivativeField(0)
	if row.FundingRate != "" {
		fact.FundingRate = fundingOptionalSigned(row.FundingRate)
		if fact.FundingRate.Valid {
			fact.FundingObservedAt = observedAt
			fields |= nativeDerivativeFieldFunding
		}
	}
	fact.MarkPrice = fundingOptionalDecimal(row.MarkPrice)
	fact.IndexPrice = fundingOptionalDecimal(row.IndexPrice)
	if fact.MarkPrice.Valid {
		fact.PriceObservedAt = observedAt
		fields |= nativeDerivativeFieldMarkPrice
	}
	if fact.IndexPrice.Valid {
		fact.PriceObservedAt = observedAt
		fields |= nativeDerivativeFieldIndexPrice
	}
	if row.TotalSize != "" {
		fact.OpenInterest = fundingOptionalNonNegative(row.TotalSize)
		if fact.OpenInterest.Valid {
			fact.OpenInterestObservedAt = observedAt
			fields |= nativeDerivativeFieldOpenInterest
		}
		fact.OpenInterestNotional = fundingOpenInterestNotional(row.TotalSize, row.MarkPrice, multiplier)
		if fact.OpenInterestNotional.Valid {
			fact.OpenInterestNotionalAsset = identity.SettleAsset
			fields |= nativeDerivativeFieldOpenInterestNotional
		}
	}
	return fact, fields
}

type nadoDerivativeWatcher struct {
	settings Settings
	closeSlot
}

func (watcher *nadoDerivativeWatcher) stream(ctx context.Context, bootstrap nativeDerivativePatch, emit func(nativeDerivativePatch)) error {
	base := newNadoBase(exchange.ProductPerp, "", "", watcher.settings)
	if base.sdk == nil {
		return exchange.NewError(exchange.KindInvalidConfig, exchange.ErrorDetails{Venue: exchange.VenueNado, Product: exchange.ProductPerp, Operation: derivativeSnapshotOperation, SafeMessage: "Nado public SDK is unavailable"})
	}
	client, err := nadosdk.NewWsMarketClient(ctx, base.sdk.Profile())
	if err != nil {
		return err
	}
	watcher.set(client.Close)
	defer watcher.run()
	if err := client.Connect(); err != nil {
		return err
	}
	identities := make(map[int64]exchange.InstrumentIdentity, len(bootstrap.Facts))
	for _, fact := range bootstrap.Facts {
		id, err := strconv.ParseInt(fact.Instrument.NativeID, 10, 64)
		if err == nil {
			identities[id] = fact.Instrument
		}
	}
	if err := client.SubscribeFundingRate(nil, func(row *nadosdk.FundingRate) {
		if row == nil {
			return
		}
		identity, ok := identities[row.ProductId]
		if !ok {
			return
		}
		observedAt := fundingStringSecondsOrNow(firstNonEmpty(row.UpdateTime, row.Timestamp))
		rate, err := nadoX18(row.FundingRateX18)
		if err != nil {
			return
		}
		fact := nativeDerivativeFact{Instrument: identity, FundingRate: exchange.OptionalDecimal{Value: rate, Valid: true}, FundingObservedAt: observedAt}
		emit(derivativeDelta(bootstrap.Scope, observedAt, nativeDerivativeFieldFunding, fact))
	}); err != nil {
		return err
	}
	go watcher.runNadoFacts(ctx, base, bootstrap.Scope, identities, emit)
	<-ctx.Done()
	return ctx.Err()
}

func (watcher *nadoDerivativeWatcher) runNadoFacts(ctx context.Context, base *nadoBase, scope exchange.CatalogScope, identities map[int64]exchange.InstrumentIdentity, emit func(nativeDerivativePatch)) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		contracts, err := coordinatedMarketRequest(ctx, watcher.settings, exchange.VenueNado, "contracts", "usdt0-perpetual", func(requestCtx context.Context) (nadosdk.ContractV2Map, error) {
			return base.sdk.GetContracts(requestCtx, nil)
		})
		if err == nil {
			observedAt := time.Now().UTC()
			batches := make(map[nativeDerivativeField]*nativeDerivativeBatch)
			for _, row := range contracts {
				identity, ok := identities[int64(row.ProductID)]
				if !ok {
					continue
				}
				fact := nativeDerivativeFact{Instrument: identity, PriceObservedAt: observedAt, OpenInterestObservedAt: observedAt}
				fields := nativeDerivativeField(0)
				if row.MarkPrice != nil && *row.MarkPrice > 0 {
					fact.MarkPrice = exchange.OptionalDecimal{Value: decimal.NewFromFloat(*row.MarkPrice), Valid: true}
					fields |= nativeDerivativeFieldMarkPrice
				}
				if row.IndexPrice != nil && *row.IndexPrice > 0 {
					fact.IndexPrice = exchange.OptionalDecimal{Value: decimal.NewFromFloat(*row.IndexPrice), Valid: true}
					fields |= nativeDerivativeFieldIndexPrice
				}
				if row.OpenInterest != nil && *row.OpenInterest >= 0 {
					fact.OpenInterest = exchange.OptionalDecimal{Value: decimal.NewFromFloat(*row.OpenInterest), Valid: true}
					fields |= nativeDerivativeFieldOpenInterest
				}
				if row.OpenInterestUsd != nil && *row.OpenInterestUsd >= 0 {
					fact.OpenInterestNotional = exchange.OptionalDecimal{Value: decimal.NewFromFloat(*row.OpenInterestUsd), Valid: true}
					fact.OpenInterestNotionalAsset = "USD"
					fields |= nativeDerivativeFieldOpenInterestNotional
				}
				if next := nadoFundingTimestamp(row.NextFundingRateTimestamp); !next.IsZero() {
					fact.NextFundingTime = next
					fact.FundingObservedAt = observedAt
					fields |= nativeDerivativeFieldNextFundingTime
				}
				if fields != 0 {
					appendNativeDerivativeBatch(batches, observedAt, fields, fact)
				}
			}
			emitNativeDerivativeBatches(scope, batches, emit)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (watcher *nadoDerivativeWatcher) close() error { return watcher.run() }

func fundingStringMillisOrNow(value string) time.Time {
	millis, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || millis <= 0 {
		return time.Now().UTC()
	}
	return time.UnixMilli(millis).UTC()
}

func fundingStringSecondsOrNow(value string) time.Time {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Now().UTC()
	}
	return time.Unix(seconds, 0).UTC()
}

func fundingGateTime(envelope gate.WSEnvelope) time.Time {
	if envelope.TimeMS > 0 {
		return time.UnixMilli(envelope.TimeMS).UTC()
	}
	if envelope.Time > 0 {
		return time.Unix(envelope.Time, 0).UTC()
	}
	return time.Now().UTC()
}
