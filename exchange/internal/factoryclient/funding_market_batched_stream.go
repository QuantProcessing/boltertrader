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
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

type bybitFundingMarketWatcher struct {
	settings Settings
	closeSlot
}

func (watcher *bybitFundingMarketWatcher) stream(ctx context.Context, bootstrap exchange.FundingMarketPatch, emit func(exchange.FundingMarketPatch)) error {
	identities := fundingIdentityByNative(bootstrap.Facts)
	profile := bybit.MainnetEnvironmentProfile()
	if watcher.settings.WebSocketEndpoint != "" {
		profile.PublicLinearWSURL = watcher.settings.WebSocketEndpoint
	}
	client := bybit.NewPublicWSClientWithProfile(profile, "linear")
	watcher.set(func() { _ = client.Close() })
	defer watcher.run()
	for native, identity := range identities {
		topic := "tickers." + native
		identity := identity
		if err := client.Subscribe(ctx, topic, func(payload json.RawMessage) {
			var envelope struct {
				Type string          `json:"type"`
				TS   int64           `json:"ts"`
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(payload, &envelope) != nil {
				return
			}
			var row bybit.Ticker
			if json.Unmarshal(envelope.Data, &row) != nil {
				return
			}
			observedAt := fundingMillisOrNow(envelope.TS)
			fact, fields := bybitTickerFact(identity, row, observedAt)
			if fields != 0 {
				emit(fundingDelta(bootstrap.Scope, observedAt, fields, fact))
			}
		}); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func (watcher *bybitFundingMarketWatcher) close() error { return watcher.run() }

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

type bitgetFundingMarketWatcher struct {
	settings Settings
	closeSlot
}

func (watcher *bitgetFundingMarketWatcher) stream(ctx context.Context, bootstrap exchange.FundingMarketPatch, emit func(exchange.FundingMarketPatch)) error {
	profile := bitget.MainnetEnvironmentProfile()
	if watcher.settings.WebSocketEndpoint != "" {
		profile.PublicWSURL = watcher.settings.WebSocketEndpoint
	}
	clients := make([]*bitget.PublicWSClient, 0, (len(bootstrap.Facts)+bitgetFundingSocketChannels-1)/bitgetFundingSocketChannels)
	watcher.set(func() {
		for _, client := range clients {
			_ = client.Close()
		}
	})
	defer watcher.run()
	instType := strings.ToLower("USDT-FUTURES")
	if strings.EqualFold(bootstrap.Scope.Settlement, "USDC") {
		instType = strings.ToLower("USDC-FUTURES")
	}
	identities := fundingIdentityByNative(bootstrap.Facts)
	for start := 0; start < len(bootstrap.Facts); start += bitgetFundingSocketChannels {
		end := start + bitgetFundingSocketChannels
		if end > len(bootstrap.Facts) {
			end = len(bootstrap.Facts)
		}
		args := make([]bitget.WSArg, 0, end-start)
		for _, baseFact := range bootstrap.Facts[start:end] {
			args = append(args, bitget.WSArg{InstType: instType, Topic: "ticker", Symbol: baseFact.Instrument.NativeSymbol})
		}
		client := bitget.NewPublicWSClientWithProfile(profile)
		clients = append(clients, client)
		if err := client.SubscribeMany(ctx, args, func(payload json.RawMessage) {
			var envelope struct {
				Arg  bitget.WSArg        `json:"arg"`
				Data []bitget.Ticker     `json:"data"`
				TS   bitget.NumberString `json:"ts"`
			}
			if json.Unmarshal(payload, &envelope) != nil || len(envelope.Data) == 0 {
				return
			}
			identity, ok := identities[envelope.Arg.Symbol]
			if !ok {
				return
			}
			row := envelope.Data[0]
			observedAt := fundingStringMillisOrNow(firstNonEmpty(row.Timestamp, string(envelope.TS)))
			fact, fields := bitgetTickerFact(identity, row, observedAt)
			if fields != 0 {
				emit(fundingDelta(bootstrap.Scope, observedAt, fields, fact))
			}
		}); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

const bitgetFundingSocketChannels = 50

func (watcher *bitgetFundingMarketWatcher) close() error { return watcher.run() }

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

type okxFundingMarketWatcher struct {
	settings Settings
	closeSlot
}

func (watcher *okxFundingMarketWatcher) stream(ctx context.Context, bootstrap exchange.FundingMarketPatch, emit func(exchange.FundingMarketPatch)) error {
	client := okx.NewWSClient(ctx)
	if watcher.settings.WebSocketEndpoint != "" {
		client.WithURL(watcher.settings.WebSocketEndpoint)
	}
	watcher.set(client.Close)
	defer watcher.run()
	if err := client.Connect(); err != nil {
		return err
	}
	for _, baseFact := range bootstrap.Facts {
		identity := baseFact.Instrument
		native := identity.NativeSymbol
		emitOne := func(observedAt time.Time, fields exchange.FundingMarketField, fact exchange.FundingMarketFact) {
			emit(fundingDelta(bootstrap.Scope, observedAt, fields, fact))
		}
		if err := client.SubscribeFundingRate(native, func(row *okx.FundingRate) {
			if row == nil {
				return
			}
			observedAt := fundingStringMillisOrNow(row.Ts)
			fact := exchange.FundingMarketFact{Instrument: identity, FundingRate: fundingOptionalSigned(row.FundingRate), FundingObservedAt: observedAt}
			fields := exchange.FundingMarketFieldFunding
			if funding, err := strconv.ParseInt(row.FundingTime, 10, 64); err == nil && funding > 0 {
				fact.NextFundingTime = time.UnixMilli(funding).UTC()
				fields |= exchange.FundingMarketFieldNextFundingTime
			}
			if next, err := strconv.ParseInt(row.NextFundingTime, 10, 64); err == nil && next > 0 && !fact.NextFundingTime.IsZero() {
				if hours := int64(time.UnixMilli(next).Sub(fact.NextFundingTime).Hours()); hours > 0 {
					fact.FundingIntervalHours = exchange.OptionalInt{Value: hours, Valid: true}
					fields |= exchange.FundingMarketFieldFundingInterval
				}
			}
			emitOne(observedAt, fields, fact)
		}); err != nil {
			return err
		}
		if err := client.SubscribeMarkPrice(native, func(row *okx.MarkPrice) {
			if row == nil {
				return
			}
			observedAt := fundingStringMillisOrNow(row.Ts)
			emitOne(observedAt, exchange.FundingMarketFieldMarkPrice, exchange.FundingMarketFact{Instrument: identity, MarkPrice: fundingOptionalDecimal(row.MarkPx), PriceObservedAt: observedAt})
		}); err != nil {
			return err
		}
		indexID := strings.TrimSuffix(native, "-SWAP")
		if err := client.SubscribeIndexTicker(indexID, func(row *okx.IndexTicker) {
			if row == nil {
				return
			}
			observedAt := fundingStringMillisOrNow(row.Ts)
			emitOne(observedAt, exchange.FundingMarketFieldIndexPrice, exchange.FundingMarketFact{Instrument: identity, IndexPrice: fundingOptionalDecimal(row.IdxPx), PriceObservedAt: observedAt})
		}); err != nil {
			return err
		}
		if err := client.SubscribeOpenInterest(native, func(row *okx.OpenInterest) {
			if row == nil {
				return
			}
			observedAt := fundingStringMillisOrNow(row.Ts)
			fact := exchange.FundingMarketFact{Instrument: identity, OpenInterest: fundingOptionalNonNegative(firstNonEmpty(row.OICcy, row.OI)), OpenInterestObservedAt: observedAt}
			fields := exchange.FundingMarketFieldOpenInterest
			if oiUSD := fundingOptionalNonNegative(row.OIUsd); oiUSD.Valid {
				fact.OpenInterestNotional = oiUSD
				fact.OpenInterestNotionalAsset = "USD"
				fields |= exchange.FundingMarketFieldOpenInterestNotional
			}
			emitOne(observedAt, fields, fact)
		}); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func (watcher *okxFundingMarketWatcher) close() error { return watcher.run() }

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
	ticker := time.NewTicker(10 * time.Second)
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
