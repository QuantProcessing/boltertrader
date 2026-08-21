package exchange

import (
	"context"
	"strings"
	"time"
)

// FundingMarketField identifies the values carried by a FundingMarketPatch.
//
// Venue WebSocket payloads are frequently partial. Keeping an explicit mask is
// what prevents an OKX mark-price message, for example, from clearing the last
// funding rate and open-interest observation for the same instrument.
type FundingMarketField uint32

const (
	FundingMarketFieldIdentity FundingMarketField = 1 << iota
	FundingMarketFieldFunding
	FundingMarketFieldFundingInterval
	FundingMarketFieldNextFundingTime
	FundingMarketFieldMarkPrice
	FundingMarketFieldIndexPrice
	FundingMarketFieldOpenInterest
	FundingMarketFieldOpenInterestNotional
)

const FundingMarketFieldAll = FundingMarketFieldIdentity |
	FundingMarketFieldFunding |
	FundingMarketFieldFundingInterval |
	FundingMarketFieldNextFundingTime |
	FundingMarketFieldMarkPrice |
	FundingMarketFieldIndexPrice |
	FundingMarketFieldOpenInterest |
	FundingMarketFieldOpenInterestNotional

func (fields FundingMarketField) Has(field FundingMarketField) bool {
	return fields&field == field
}

type FundingMarketPatchKind string

const (
	FundingMarketPatchSnapshot FundingMarketPatchKind = "snapshot"
	FundingMarketPatchDelta    FundingMarketPatchKind = "delta"
)

// FundingMarketFact is the venue-neutral current state used by funding and
// basis products. Prices, funding and open interest keep independent clocks so
// a fresh price update cannot make an old open-interest value look current.
//
// OpenInterest is the venue-native quantity. OpenInterestNotional is converted
// into OpenInterestNotionalAsset (normally USDT or USDC) and is the only OI
// value suitable for cross-venue display and sorting.
type FundingMarketFact struct {
	Instrument                InstrumentIdentity `json:"instrument"`
	FundingRate               OptionalDecimal    `json:"funding_rate"`
	FundingIntervalHours      OptionalInt        `json:"funding_interval_hours"`
	NextFundingTime           time.Time          `json:"next_funding_time,omitempty"`
	MarkPrice                 OptionalDecimal    `json:"mark_price"`
	IndexPrice                OptionalDecimal    `json:"index_price"`
	OpenInterest              OptionalDecimal    `json:"open_interest"`
	OpenInterestNotional      OptionalDecimal    `json:"open_interest_notional"`
	OpenInterestNotionalAsset string             `json:"open_interest_notional_asset,omitempty"`
	FundingObservedAt         time.Time          `json:"funding_observed_at,omitempty"`
	PriceObservedAt           time.Time          `json:"price_observed_at,omitempty"`
	OpenInterestObservedAt    time.Time          `json:"open_interest_observed_at,omitempty"`
}

func (fact FundingMarketFact) Key() string {
	identity := fact.Instrument
	if identity.NativeID != "" {
		return string(identity.Venue) + "|" + string(identity.Product) + "|id:" + identity.NativeID
	}
	return string(identity.Venue) + "|" + string(identity.Product) + "|symbol:" + identity.NativeSymbol
}

// FundingMarketPatch is either a full replacement for one route-profile scope
// or a set of partial instrument updates. Fields applies to every fact in the
// patch; producers that receive differently shaped updates emit separate
// patches instead of pretending absent values were zero.
type FundingMarketPatch struct {
	Kind       FundingMarketPatchKind `json:"kind"`
	Scope      CatalogScope           `json:"scope"`
	ObservedAt time.Time              `json:"observed_at"`
	Sequence   uint64                 `json:"sequence,omitempty"`
	Fields     FundingMarketField     `json:"fields"`
	Facts      []FundingMarketFact    `json:"facts,omitempty"`
}

func (patch FundingMarketPatch) Validate() error {
	if patch.Kind != FundingMarketPatchSnapshot && patch.Kind != FundingMarketPatchDelta {
		return invalidFundingMarket("funding market patch kind is invalid")
	}
	if patch.Scope.Venue == "" || patch.Scope.Product != ProductPerp || patch.Scope.Environment != EnvironmentLive {
		return invalidFundingMarket("funding market patch scope is invalid")
	}
	if patch.ObservedAt.IsZero() {
		return invalidFundingMarket("funding market patch observation time is missing")
	}
	if patch.Fields == 0 || patch.Fields&^FundingMarketFieldAll != 0 {
		return invalidFundingMarket("funding market patch field mask is invalid")
	}
	if patch.Kind == FundingMarketPatchSnapshot && !patch.Fields.Has(FundingMarketFieldIdentity) {
		return invalidFundingMarket("funding market snapshot is missing identity fields")
	}
	seen := make(map[string]struct{}, len(patch.Facts))
	for _, fact := range patch.Facts {
		identity := fact.Instrument
		if identity.Venue != patch.Scope.Venue || identity.Product != ProductPerp ||
			strings.TrimSpace(identity.Symbol) == "" || strings.TrimSpace(identity.NativeSymbol) == "" ||
			strings.TrimSpace(identity.BaseAsset) == "" || strings.TrimSpace(identity.QuoteAsset) == "" {
			return invalidFundingMarket("funding market fact identity is invalid")
		}
		if patch.Scope.Settlement != "" && identity.SettleAsset != "" && identity.SettleAsset != patch.Scope.Settlement {
			return invalidFundingMarket("funding market fact settlement does not match its scope")
		}
		key := fact.Key()
		if _, duplicate := seen[key]; duplicate {
			return invalidFundingMarket("funding market patch contains a duplicate instrument")
		}
		seen[key] = struct{}{}
		if fact.OpenInterestNotional.Valid && strings.TrimSpace(fact.OpenInterestNotionalAsset) == "" {
			return invalidFundingMarket("funding market OI notional is missing its asset")
		}
	}
	return nil
}

type FundingMarketFeedState string

const (
	FundingMarketFeedUnavailable FundingMarketFeedState = "unavailable"
	FundingMarketFeedDegraded    FundingMarketFeedState = "degraded"
	FundingMarketFeedLive        FundingMarketFeedState = "live"
)

type FundingMarketFeedStatus struct {
	State         FundingMarketFeedState `json:"state"`
	LastSuccessAt time.Time              `json:"last_success_at,omitempty"`
	LastError     string                 `json:"last_error,omitempty"`
	Reconnects    uint64                 `json:"reconnects,omitempty"`
}

// FundingMarketFeed owns one public route-profile feed. Bootstrap is a bounded
// REST read; Stream blocks until its context ends or the transport requires the
// caller to reconnect. No method requires an account or credential.
type FundingMarketFeed interface {
	Bootstrap(context.Context) (FundingMarketPatch, error)
	Stream(context.Context, func(FundingMarketPatch)) error
	Status() FundingMarketFeedStatus
	Close() error
}

func invalidFundingMarket(message string) error {
	return NewError(KindInvalidConfig, ErrorDetails{
		Operation:   "FundingMarket.Feed",
		SafeMessage: message,
	})
}
