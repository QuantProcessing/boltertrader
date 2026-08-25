package factoryclient

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

// nativeDerivative* are adapter-private normalization types. They carry the
// shape shared by venue SDK payloads until the canonical catalog resolver turns
// them into exchange.DerivativePatch at the adapter output boundary.
type DerivativeSnapshotEntry struct {
	Symbol                    string
	NativeSymbol              string
	NativeID                  string
	BaseAsset                 string
	QuoteAsset                string
	Rate                      decimal.Decimal
	RateValid                 bool
	IntervalHours             exchange.OptionalInt
	NextFundingTime           time.Time
	MarkPrice                 exchange.OptionalDecimal
	IndexPrice                exchange.OptionalDecimal
	OpenInterest              exchange.OptionalDecimal
	OpenInterestNotional      exchange.OptionalDecimal
	OpenInterestNotionalAsset string
	FundingObservedAt         time.Time
	MarkObservedAt            time.Time
	IndexObservedAt           time.Time
	PriceObservedAt           time.Time
	OpenInterestObservedAt    time.Time
}

type DerivativeSnapshot struct {
	SchemaVersion int
	Scope         exchange.CatalogScope
	ObservedAt    time.Time
	Entries       []DerivativeSnapshotEntry
}

type DerivativeSnapshotLoader interface {
	LoadDerivativeSnapshot(context.Context, exchange.CatalogScope) (DerivativeSnapshot, error)
}

func (snapshot DerivativeSnapshot) Validate(scope exchange.CatalogScope) error {
	if snapshot.SchemaVersion != 1 {
		return fmt.Errorf("derivative snapshot schema version is unsupported")
	}
	if snapshot.Scope != scope {
		return fmt.Errorf("derivative snapshot scope does not match requested scope")
	}
	seen := make(map[string]struct{}, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if strings.TrimSpace(entry.Symbol) == "" {
			return fmt.Errorf("derivative snapshot entry is missing a canonical symbol")
		}
		if _, duplicate := seen[entry.Symbol]; duplicate {
			return fmt.Errorf("derivative snapshot contains a duplicate canonical symbol")
		}
		seen[entry.Symbol] = struct{}{}
	}
	return nil
}

type nativeDerivativeField uint32

const (
	nativeDerivativeFieldIdentity nativeDerivativeField = 1 << iota
	nativeDerivativeFieldFunding
	nativeDerivativeFieldFundingInterval
	nativeDerivativeFieldNextFundingTime
	nativeDerivativeFieldMarkPrice
	nativeDerivativeFieldIndexPrice
	nativeDerivativeFieldOpenInterest
	nativeDerivativeFieldOpenInterestNotional
)

const nativeDerivativeFieldAll = nativeDerivativeFieldIdentity |
	nativeDerivativeFieldFunding |
	nativeDerivativeFieldFundingInterval |
	nativeDerivativeFieldNextFundingTime |
	nativeDerivativeFieldMarkPrice |
	nativeDerivativeFieldIndexPrice |
	nativeDerivativeFieldOpenInterest |
	nativeDerivativeFieldOpenInterestNotional

func (fields nativeDerivativeField) Has(field nativeDerivativeField) bool {
	return fields&field == field
}

type nativeDerivativePatchKind string

const (
	nativeDerivativePatchSnapshot nativeDerivativePatchKind = "snapshot"
	nativeDerivativePatchDelta    nativeDerivativePatchKind = "delta"
)

type nativeDerivativeFact struct {
	Instrument                exchange.InstrumentIdentity
	FundingRate               exchange.OptionalDecimal
	FundingIntervalHours      exchange.OptionalInt
	NextFundingTime           time.Time
	MarkPrice                 exchange.OptionalDecimal
	IndexPrice                exchange.OptionalDecimal
	OpenInterest              exchange.OptionalDecimal
	OpenInterestNotional      exchange.OptionalDecimal
	OpenInterestNotionalAsset string
	FundingObservedAt         time.Time
	MarkObservedAt            time.Time
	IndexObservedAt           time.Time
	PriceObservedAt           time.Time
	OpenInterestObservedAt    time.Time
}

func (fact nativeDerivativeFact) key() string {
	if fact.Instrument.NativeID != "" {
		return string(fact.Instrument.Venue) + "|" + string(fact.Instrument.Product) + "|id:" + fact.Instrument.NativeID
	}
	return string(fact.Instrument.Venue) + "|" + string(fact.Instrument.Product) + "|symbol:" + fact.Instrument.NativeSymbol
}

type nativeDerivativePatch struct {
	Kind       nativeDerivativePatchKind
	Scope      exchange.CatalogScope
	ObservedAt time.Time
	Sequence   uint64
	Fields     nativeDerivativeField
	Facts      []nativeDerivativeFact
}

func (patch nativeDerivativePatch) validate() error {
	if patch.Kind != nativeDerivativePatchSnapshot && patch.Kind != nativeDerivativePatchDelta {
		return fmt.Errorf("native derivative patch kind is invalid")
	}
	if patch.Scope.Venue == "" || patch.Scope.Product != exchange.ProductPerp || patch.Scope.Environment != exchange.EnvironmentLive || patch.ObservedAt.IsZero() {
		return fmt.Errorf("native derivative patch scope is invalid")
	}
	if patch.Fields == 0 || patch.Fields&^nativeDerivativeFieldAll != 0 {
		return fmt.Errorf("native derivative patch field mask is invalid")
	}
	if patch.Kind == nativeDerivativePatchSnapshot && !patch.Fields.Has(nativeDerivativeFieldIdentity) {
		return fmt.Errorf("native derivative snapshot is missing identity fields")
	}
	seen := make(map[string]struct{}, len(patch.Facts))
	for _, fact := range patch.Facts {
		identity := fact.Instrument
		if identity.Venue != patch.Scope.Venue || identity.Product != exchange.ProductPerp ||
			strings.TrimSpace(identity.Symbol) == "" || strings.TrimSpace(identity.NativeSymbol) == "" ||
			strings.TrimSpace(identity.BaseAsset) == "" || strings.TrimSpace(identity.QuoteAsset) == "" {
			return fmt.Errorf("native derivative fact identity is invalid")
		}
		if patch.Scope.Settlement != "" && identity.SettleAsset != "" && identity.SettleAsset != patch.Scope.Settlement {
			return fmt.Errorf("native derivative fact settlement does not match scope")
		}
		key := fact.key()
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("native derivative patch contains duplicate instrument")
		}
		seen[key] = struct{}{}
		if fact.OpenInterestNotional.Valid && strings.TrimSpace(fact.OpenInterestNotionalAsset) == "" {
			return fmt.Errorf("native derivative OI notional is missing its asset")
		}
	}
	return nil
}
