package exchange

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MarketRequestCoordinator is the process-wide public REST gate shared by
// catalog refresh, broad derivative sweeps and execution quote fallbacks.
// Exchange adapters depend on this small contract rather than on Gateway's
// concrete limiter implementation.
type MarketRequestCoordinator interface {
	Do(context.Context, Venue, string, string, func(context.Context) (any, error)) (any, error)
}

// MarketDataset names a reusable venue fact. It deliberately says nothing
// about how a venue transports that fact; transport belongs in the capability
// matrix and may change without changing consumers.
type MarketDataset string

const (
	DatasetCatalog      MarketDataset = "catalog"
	DatasetBBO          MarketDataset = "bbo"
	DatasetDepth        MarketDataset = "depth"
	DatasetCandle       MarketDataset = "candle"
	DatasetMark         MarketDataset = "mark"
	DatasetIndex        MarketDataset = "index"
	DatasetFunding      MarketDataset = "funding"
	DatasetOpenInterest MarketDataset = "open_interest"
)

type MarketTransport string

const (
	TransportWebSocket MarketTransport = "websocket"
	TransportREST      MarketTransport = "rest"
)

type MarketGranularity string

const (
	GranularityAllMarket        MarketGranularity = "all_market"
	GranularityMultiSymbol      MarketGranularity = "multi_symbol"
	GranularitySingleInstrument MarketGranularity = "single_instrument"
	GranularityPollOnly         MarketGranularity = "poll_only"
	GranularityUnsupported      MarketGranularity = "unsupported"
)

// MarketCapability is a static description of a venue-native data path. It is
// intentionally free of runtime state: reconnects, freshness and hot-symbol
// selection are BolterTrader behavior, not exchange capabilities.
type MarketCapability struct {
	Venue                  Venue             `json:"venue"`
	NativeMarketFamily     string            `json:"nativeMarketFamily"`
	Dataset                MarketDataset     `json:"dataset"`
	Transport              MarketTransport   `json:"transport"`
	Granularity            MarketGranularity `json:"granularity"`
	NominalCadence         time.Duration     `json:"nominalCadence"`
	MaxTopicsPerConnection int               `json:"maxTopicsPerConnection,omitempty"`
	HasSnapshot            bool              `json:"hasSnapshot"`
	HasSourceSequence      bool              `json:"hasSourceSequence"`
	RateLimitOperation     string            `json:"rateLimitOperation,omitempty"`
}

func (capability MarketCapability) Validate() error {
	if !knownVenue(capability.Venue) || strings.TrimSpace(capability.NativeMarketFamily) == "" || !knownMarketDataset(capability.Dataset) {
		return fmt.Errorf("market capability identity is invalid")
	}
	if capability.Transport != TransportWebSocket && capability.Transport != TransportREST {
		return fmt.Errorf("market capability transport is invalid")
	}
	switch capability.Granularity {
	case GranularityAllMarket, GranularityMultiSymbol, GranularitySingleInstrument, GranularityPollOnly, GranularityUnsupported:
	default:
		return fmt.Errorf("market capability granularity is invalid")
	}
	if capability.NominalCadence < 0 || capability.MaxTopicsPerConnection < 0 {
		return fmt.Errorf("market capability limits are invalid")
	}
	return nil
}

func knownMarketDataset(dataset MarketDataset) bool {
	switch dataset {
	case DatasetCatalog, DatasetBBO, DatasetDepth, DatasetCandle, DatasetMark, DatasetIndex, DatasetFunding, DatasetOpenInterest:
		return true
	default:
		return false
	}
}

// DerivativeFact is the venue-neutral state shared by the trader, funding
// board, position PnL and future alerting. Optional values
// preserve absent data; a venue's missing value must never become numeric zero.
// Each field group owns its own observation clock so one fast mark update cannot
// make a stale funding rate or open-interest value appear fresh.
type DerivativeFact struct {
	InstrumentID              string             `json:"instrumentId"`
	RouteProfileID            string             `json:"routeProfileId"`
	Instrument                InstrumentIdentity `json:"instrument"`
	Environment               Environment        `json:"environment"`
	FundingRate               OptionalDecimal    `json:"fundingRate"`
	FundingIntervalHours      OptionalInt        `json:"fundingIntervalHours"`
	NextFundingTime           time.Time          `json:"nextFundingTime,omitzero"`
	MarkPrice                 OptionalDecimal    `json:"markPrice"`
	IndexPrice                OptionalDecimal    `json:"indexPrice"`
	OpenInterest              OptionalDecimal    `json:"openInterest"`
	OpenInterestNotional      OptionalDecimal    `json:"openInterestNotional"`
	OpenInterestNotionalAsset string             `json:"openInterestNotionalAsset,omitempty"`
	FundingObservedAt         time.Time          `json:"fundingObservedAt,omitzero"`
	MarkObservedAt            time.Time          `json:"markObservedAt,omitzero"`
	IndexObservedAt           time.Time          `json:"indexObservedAt,omitzero"`
	OpenInterestObservedAt    time.Time          `json:"openInterestObservedAt,omitzero"`
}

func (fact DerivativeFact) Validate() error {
	if strings.TrimSpace(fact.InstrumentID) == "" || strings.TrimSpace(fact.RouteProfileID) == "" || fact.Environment != EnvironmentLive {
		return fmt.Errorf("derivative fact identity is invalid")
	}
	identity := fact.Instrument
	if identity.Product != ProductPerp || !knownVenue(identity.Venue) || strings.TrimSpace(identity.NativeSymbol) == "" {
		return fmt.Errorf("derivative fact instrument is invalid")
	}
	fundingPresent := fact.FundingRate.Valid || fact.FundingIntervalHours.Valid || !fact.NextFundingTime.IsZero()
	if fundingPresent != !fact.FundingObservedAt.IsZero() {
		return fmt.Errorf("derivative funding value and clock disagree")
	}
	if fact.MarkPrice.Valid != !fact.MarkObservedAt.IsZero() {
		return fmt.Errorf("derivative mark value and clock disagree")
	}
	if fact.IndexPrice.Valid != !fact.IndexObservedAt.IsZero() {
		return fmt.Errorf("derivative index value and clock disagree")
	}
	if (fact.OpenInterest.Valid || fact.OpenInterestNotional.Valid) != !fact.OpenInterestObservedAt.IsZero() {
		return fmt.Errorf("derivative open-interest value and clock disagree")
	}
	if fact.OpenInterestNotional.Valid && strings.TrimSpace(fact.OpenInterestNotionalAsset) == "" {
		return fmt.Errorf("derivative open-interest notional asset is missing")
	}
	return nil
}

// DerivativeField is a partial-update mask. Adapters emit separate patches for
// differently shaped native messages so an absent field cannot clear another
// feed's most recent value.
type DerivativeField uint32

const (
	DerivativeFieldIdentity DerivativeField = 1 << iota
	DerivativeFieldFunding
	DerivativeFieldFundingInterval
	DerivativeFieldNextFundingTime
	DerivativeFieldMark
	DerivativeFieldIndex
	DerivativeFieldOpenInterest
	DerivativeFieldOpenInterestNotional
)

const DerivativeFieldAll = DerivativeFieldIdentity | DerivativeFieldFunding |
	DerivativeFieldFundingInterval | DerivativeFieldNextFundingTime |
	DerivativeFieldMark | DerivativeFieldIndex | DerivativeFieldOpenInterest |
	DerivativeFieldOpenInterestNotional

func (fields DerivativeField) Has(field DerivativeField) bool { return fields&field == field }

type DerivativePatch struct {
	Kind           EventKind        `json:"kind"`
	Scope          CatalogScope     `json:"scope"`
	NativeFamily   string           `json:"nativeFamily"`
	Generation     uint64           `json:"generation"`
	ObservedAt     time.Time        `json:"observedAt"`
	SourceSequence string           `json:"sourceSequence,omitempty"`
	Fields         DerivativeField  `json:"fields"`
	Facts          []DerivativeFact `json:"facts"`
}

// DerivativeInstrumentResolver maps a venue-native identity onto the current
// canonical catalog. Adapters use it at their output boundary, so no raw venue
// funding DTO escapes into Gateway business projections.
type DerivativeInstrumentResolver interface {
	ResolveDerivativeInstrument(context.Context, string, string, string) (DerivativeFact, bool)
}

// DerivativeFeed is the one physical upstream owned by a native-family Feed
// Owner. A feed may project its native rows onto several route profiles, but
// those projections never create additional exchange connections or polls.
type DerivativeFeed interface {
	Bootstrap(context.Context) (DerivativePatch, error)
	Stream(context.Context, func(DerivativePatch)) error
	Close() error
}

// DerivativeHotsetFeed is an optional extension for a broad feed that can
// raise the cadence of expensive fields for selected canonical instruments.
// The broad owner remains authoritative; callers only express demand and do
// not control native topics directly.
type DerivativeHotsetFeed interface {
	SetHotInstrument(instrumentID string, active bool)
}

func (patch DerivativePatch) Validate() error {
	if patch.Kind != EventSnapshot && patch.Kind != EventDelta {
		return fmt.Errorf("derivative patch kind is invalid")
	}
	if !knownVenue(patch.Scope.Venue) || patch.Scope.Environment != EnvironmentLive || patch.Generation == 0 ||
		patch.Scope.Product != ProductPerp || strings.TrimSpace(patch.NativeFamily) == "" || patch.ObservedAt.IsZero() {
		return fmt.Errorf("derivative patch scope is invalid")
	}
	if patch.Fields == 0 || patch.Fields&^DerivativeFieldAll != 0 {
		return fmt.Errorf("derivative patch field mask is invalid")
	}
	if patch.Kind == EventSnapshot && !patch.Fields.Has(DerivativeFieldIdentity) {
		return fmt.Errorf("derivative snapshot is missing identity fields")
	}
	seen := make(map[string]struct{}, len(patch.Facts))
	for _, fact := range patch.Facts {
		if strings.TrimSpace(fact.InstrumentID) == "" || strings.TrimSpace(fact.RouteProfileID) == "" ||
			fact.Environment != patch.Scope.Environment || fact.Instrument.Venue != patch.Scope.Venue ||
			fact.Instrument.Product != ProductPerp || strings.TrimSpace(fact.Instrument.NativeSymbol) == "" {
			return fmt.Errorf("derivative patch fact identity is invalid")
		}
		if patch.Scope.Settlement != "" && fact.Instrument.SettleAsset != "" && fact.Instrument.SettleAsset != patch.Scope.Settlement {
			return fmt.Errorf("derivative patch fact settlement is invalid")
		}
		if _, duplicate := seen[fact.InstrumentID]; duplicate {
			return fmt.Errorf("derivative patch contains a duplicate instrument")
		}
		seen[fact.InstrumentID] = struct{}{}
		if fact.OpenInterestNotional.Valid && strings.TrimSpace(fact.OpenInterestNotionalAsset) == "" {
			return fmt.Errorf("derivative patch OI notional asset is missing")
		}
	}
	return nil
}

func ApplyDerivativePatch(current DerivativeFact, patch DerivativeFact, fields DerivativeField) DerivativeFact {
	if fields.Has(DerivativeFieldIdentity) {
		current.InstrumentID = patch.InstrumentID
		current.RouteProfileID = patch.RouteProfileID
		current.Instrument = patch.Instrument
		current.Environment = patch.Environment
	}
	fundingFields := fields & (DerivativeFieldFunding | DerivativeFieldFundingInterval | DerivativeFieldNextFundingTime)
	if fundingFields != 0 && observationCurrent(current.FundingObservedAt, patch.FundingObservedAt) {
		if fields.Has(DerivativeFieldFunding) {
			current.FundingRate = patch.FundingRate
		}
		if fields.Has(DerivativeFieldFundingInterval) {
			current.FundingIntervalHours = patch.FundingIntervalHours
		}
		if fields.Has(DerivativeFieldNextFundingTime) {
			current.NextFundingTime = patch.NextFundingTime
		}
		current.FundingObservedAt = patch.FundingObservedAt
	}
	if fields.Has(DerivativeFieldMark) && observationCurrent(current.MarkObservedAt, patch.MarkObservedAt) {
		current.MarkPrice, current.MarkObservedAt = patch.MarkPrice, patch.MarkObservedAt
	}
	if fields.Has(DerivativeFieldIndex) && observationCurrent(current.IndexObservedAt, patch.IndexObservedAt) {
		current.IndexPrice, current.IndexObservedAt = patch.IndexPrice, patch.IndexObservedAt
	}
	if fields.Has(DerivativeFieldOpenInterest) && observationCurrent(current.OpenInterestObservedAt, patch.OpenInterestObservedAt) {
		current.OpenInterest, current.OpenInterestObservedAt = patch.OpenInterest, patch.OpenInterestObservedAt
	}
	if fields.Has(DerivativeFieldOpenInterestNotional) && observationCurrent(current.OpenInterestObservedAt, patch.OpenInterestObservedAt) {
		current.OpenInterestNotional = patch.OpenInterestNotional
		current.OpenInterestNotionalAsset = patch.OpenInterestNotionalAsset
		current.OpenInterestObservedAt = patch.OpenInterestObservedAt
	}
	return current
}

func observationCurrent(current, incoming time.Time) bool {
	return !incoming.IsZero() && (current.IsZero() || !incoming.Before(current))
}

type FieldFreshness struct {
	Funding      bool `json:"funding"`
	Mark         bool `json:"mark"`
	Index        bool `json:"index"`
	OpenInterest bool `json:"openInterest"`
}

func (fact DerivativeFact) Freshness(now time.Time, maxAge map[MarketDataset]time.Duration) FieldFreshness {
	return FieldFreshness{
		Funding:      fact.FundingRate.Valid && freshAt(now, fact.FundingObservedAt, maxAge[DatasetFunding]),
		Mark:         fact.MarkPrice.Valid && freshAt(now, fact.MarkObservedAt, maxAge[DatasetMark]),
		Index:        fact.IndexPrice.Valid && freshAt(now, fact.IndexObservedAt, maxAge[DatasetIndex]),
		OpenInterest: (fact.OpenInterest.Valid || fact.OpenInterestNotional.Valid) && freshAt(now, fact.OpenInterestObservedAt, maxAge[DatasetOpenInterest]),
	}
}

func freshAt(now, observedAt time.Time, maxAge time.Duration) bool {
	age := now.UTC().Sub(observedAt.UTC())
	return maxAge > 0 && !observedAt.IsZero() && age >= 0 && age <= maxAge
}
