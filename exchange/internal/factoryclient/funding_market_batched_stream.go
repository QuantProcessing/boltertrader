package factoryclient

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	bybit "github.com/QuantProcessing/boltertrader/sdk/bybit"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	nadosdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func bybitTickerFact(identity exchange.InstrumentIdentity, row bybit.Ticker, observedAt time.Time) (exchange.FundingMarketFact, exchange.FundingMarketField) {
	fact := exchange.FundingMarketFact{Instrument: identity}
	fields := exchange.FundingMarketField(0)
	if row.FundingRate != "" {
		fact.FundingRate = fundingOptionalSigned(row.FundingRate)
		if fact.FundingRate.Valid {
			fact.FundingObservedAt = observedAt
			fields |= exchange.FundingMarketFieldFunding
		}
	}
	if row.FundingIntervalHour != "" {
		fact.FundingIntervalHours = fundingIntervalHours(row.FundingIntervalHour)
		if fact.FundingIntervalHours.Valid {
			fields |= exchange.FundingMarketFieldFundingInterval
		}
	}
	if row.NextFundingTime != "" {
		fact.NextFundingTime = fundingUnixMillis(row.NextFundingTime)
		if !fact.NextFundingTime.IsZero() {
			fields |= exchange.FundingMarketFieldNextFundingTime
		}
	}
	if row.MarkPrice != "" {
		fact.MarkPrice = fundingOptionalDecimal(row.MarkPrice)
		if fact.MarkPrice.Valid {
			fact.PriceObservedAt = observedAt
			fields |= exchange.FundingMarketFieldMarkPrice
		}
	}
	if row.IndexPrice != "" {
		fact.IndexPrice = fundingOptionalDecimal(row.IndexPrice)
		if fact.IndexPrice.Valid {
			fact.PriceObservedAt = observedAt
			fields |= exchange.FundingMarketFieldIndexPrice
		}
	}
	if row.OpenInterest != "" {
		fact.OpenInterest = fundingOptionalNonNegative(row.OpenInterest)
		if fact.OpenInterest.Valid {
			fact.OpenInterestObservedAt = observedAt
			fields |= exchange.FundingMarketFieldOpenInterest
		}
		fact.OpenInterestNotional = fundingOpenInterestNotional(row.OpenInterest, row.MarkPrice, decimal.NewFromInt(1))
		if fact.OpenInterestNotional.Valid {
			fact.OpenInterestNotionalAsset = identity.SettleAsset
			fields |= exchange.FundingMarketFieldOpenInterestNotional
		}
	}
	return fact, fields
}

func bitgetTickerFact(identity exchange.InstrumentIdentity, row bitget.Ticker, observedAt time.Time) (exchange.FundingMarketFact, exchange.FundingMarketField) {
	fact := exchange.FundingMarketFact{Instrument: identity}
	fields := exchange.FundingMarketField(0)
	if row.FundingRate != "" {
		fact.FundingRate = fundingOptionalSigned(row.FundingRate)
		if fact.FundingRate.Valid {
			fact.FundingObservedAt = observedAt
			fields |= exchange.FundingMarketFieldFunding
		}
	}
	if row.FundingRateInterval != "" {
		fact.FundingIntervalHours = fundingIntervalHours(row.FundingRateInterval)
		if fact.FundingIntervalHours.Valid {
			fields |= exchange.FundingMarketFieldFundingInterval
		}
	}
	if row.NextFundingTime != "" {
		fact.NextFundingTime = fundingUnixMillis(row.NextFundingTime)
		if !fact.NextFundingTime.IsZero() {
			fields |= exchange.FundingMarketFieldNextFundingTime
		}
	}
	if row.MarkPrice != "" {
		fact.MarkPrice = fundingOptionalDecimal(row.MarkPrice)
		if fact.MarkPrice.Valid {
			fact.PriceObservedAt = observedAt
			fields |= exchange.FundingMarketFieldMarkPrice
		}
	}
	if row.IndexPrice != "" {
		fact.IndexPrice = fundingOptionalDecimal(row.IndexPrice)
		if fact.IndexPrice.Valid {
			fact.PriceObservedAt = observedAt
			fields |= exchange.FundingMarketFieldIndexPrice
		}
	}
	openInterest := firstNonEmpty(row.OpenInterest, row.HoldingAmount)
	if openInterest != "" {
		fact.OpenInterest = fundingOptionalNonNegative(openInterest)
		if fact.OpenInterest.Valid {
			fact.OpenInterestObservedAt = observedAt
			fields |= exchange.FundingMarketFieldOpenInterest
		}
		fact.OpenInterestNotional = fundingOpenInterestNotional(openInterest, row.MarkPrice, decimal.NewFromInt(1))
		if fact.OpenInterestNotional.Valid {
			fact.OpenInterestNotionalAsset = identity.SettleAsset
			fields |= exchange.FundingMarketFieldOpenInterestNotional
		}
	}
	return fact, fields
}

type gateFundingMarketWatcher struct {
	settings Settings
	closeSlot
}

func (watcher *gateFundingMarketWatcher) stream(ctx context.Context, bootstrap exchange.FundingMarketPatch, emit func(exchange.FundingMarketPatch)) error {
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
	identities := fundingIdentityByNative(bootstrap.Facts)
	multipliers := make(map[string]decimal.Decimal, len(bootstrap.Facts))
	for _, fact := range bootstrap.Facts {
		multiplier := decimal.NewFromInt(1)
		if fact.OpenInterest.Valid && fact.OpenInterestNotional.Valid && fact.MarkPrice.Valid && fact.OpenInterest.Value.IsPositive() {
			multiplier = fact.OpenInterestNotional.Value.Div(fact.OpenInterest.Value).Div(fact.MarkPrice.Value)
		}
		multipliers[fact.Instrument.NativeSymbol] = multiplier
	}
	payload := make([]string, 0, len(identities))
	for native := range identities {
		payload = append(payload, native)
	}
	if err := client.Subscribe(ctx, "futures.tickers", payload, func(raw json.RawMessage) {
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
		for _, row := range rows {
			identity, ok := identities[row.Contract]
			if !ok {
				continue
			}
			fact, fields := gateTickerFact(identity, row, multipliers[row.Contract], observedAt)
			if fields != 0 {
				emit(fundingDelta(bootstrap.Scope, observedAt, fields, fact))
			}
		}
	}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (watcher *gateFundingMarketWatcher) close() error { return watcher.run() }

func gateTickerFact(identity exchange.InstrumentIdentity, row gate.FuturesTicker, multiplier decimal.Decimal, observedAt time.Time) (exchange.FundingMarketFact, exchange.FundingMarketField) {
	fact := exchange.FundingMarketFact{Instrument: identity}
	fields := exchange.FundingMarketField(0)
	if row.FundingRate != "" {
		fact.FundingRate = fundingOptionalSigned(row.FundingRate)
		if fact.FundingRate.Valid {
			fact.FundingObservedAt = observedAt
			fields |= exchange.FundingMarketFieldFunding
		}
	}
	fact.MarkPrice = fundingOptionalDecimal(row.MarkPrice)
	fact.IndexPrice = fundingOptionalDecimal(row.IndexPrice)
	if fact.MarkPrice.Valid {
		fact.PriceObservedAt = observedAt
		fields |= exchange.FundingMarketFieldMarkPrice
	}
	if fact.IndexPrice.Valid {
		fact.PriceObservedAt = observedAt
		fields |= exchange.FundingMarketFieldIndexPrice
	}
	if row.TotalSize != "" {
		fact.OpenInterest = fundingOptionalNonNegative(row.TotalSize)
		if fact.OpenInterest.Valid {
			fact.OpenInterestObservedAt = observedAt
			fields |= exchange.FundingMarketFieldOpenInterest
		}
		fact.OpenInterestNotional = fundingOpenInterestNotional(row.TotalSize, row.MarkPrice, multiplier)
		if fact.OpenInterestNotional.Valid {
			fact.OpenInterestNotionalAsset = identity.SettleAsset
			fields |= exchange.FundingMarketFieldOpenInterestNotional
		}
	}
	return fact, fields
}

type nadoFundingMarketWatcher struct {
	settings Settings
	closeSlot
}

func (watcher *nadoFundingMarketWatcher) stream(ctx context.Context, bootstrap exchange.FundingMarketPatch, emit func(exchange.FundingMarketPatch)) error {
	base := newNadoBase(exchange.ProductPerp, "", "", watcher.settings)
	if base.sdk == nil {
		return exchange.NewError(exchange.KindInvalidConfig, exchange.ErrorDetails{Venue: exchange.VenueNado, Product: exchange.ProductPerp, Operation: fundingBoardOperation, SafeMessage: "Nado public SDK is unavailable"})
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
		fact := exchange.FundingMarketFact{Instrument: identity, FundingRate: exchange.OptionalDecimal{Value: rate, Valid: true}, FundingObservedAt: observedAt}
		emit(fundingDelta(bootstrap.Scope, observedAt, exchange.FundingMarketFieldFunding, fact))
	}); err != nil {
		return err
	}
	go watcher.runNadoFacts(ctx, base, bootstrap.Scope, identities, emit)
	<-ctx.Done()
	return ctx.Err()
}

func (watcher *nadoFundingMarketWatcher) runNadoFacts(ctx context.Context, base *nadoBase, scope exchange.CatalogScope, identities map[int64]exchange.InstrumentIdentity, emit func(exchange.FundingMarketPatch)) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		contracts, err := base.sdk.GetContracts(ctx, nil)
		if err == nil {
			observedAt := time.Now().UTC()
			for _, row := range contracts {
				identity, ok := identities[int64(row.ProductID)]
				if !ok {
					continue
				}
				fact := exchange.FundingMarketFact{Instrument: identity, PriceObservedAt: observedAt, OpenInterestObservedAt: observedAt}
				fields := exchange.FundingMarketField(0)
				if row.MarkPrice != nil && *row.MarkPrice > 0 {
					fact.MarkPrice = exchange.OptionalDecimal{Value: decimal.NewFromFloat(*row.MarkPrice), Valid: true}
					fields |= exchange.FundingMarketFieldMarkPrice
				}
				if row.IndexPrice != nil && *row.IndexPrice > 0 {
					fact.IndexPrice = exchange.OptionalDecimal{Value: decimal.NewFromFloat(*row.IndexPrice), Valid: true}
					fields |= exchange.FundingMarketFieldIndexPrice
				}
				if row.OpenInterest != nil && *row.OpenInterest >= 0 {
					fact.OpenInterest = exchange.OptionalDecimal{Value: decimal.NewFromFloat(*row.OpenInterest), Valid: true}
					fields |= exchange.FundingMarketFieldOpenInterest
				}
				if row.OpenInterestUsd != nil && *row.OpenInterestUsd >= 0 {
					fact.OpenInterestNotional = exchange.OptionalDecimal{Value: decimal.NewFromFloat(*row.OpenInterestUsd), Valid: true}
					fact.OpenInterestNotionalAsset = "USD"
					fields |= exchange.FundingMarketFieldOpenInterestNotional
				}
				if next := nadoFundingTimestamp(row.NextFundingRateTimestamp); !next.IsZero() {
					fact.NextFundingTime = next
					fact.FundingObservedAt = observedAt
					fields |= exchange.FundingMarketFieldNextFundingTime
				}
				if fields != 0 {
					emit(fundingDelta(scope, observedAt, fields, fact))
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (watcher *nadoFundingMarketWatcher) close() error { return watcher.run() }

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
