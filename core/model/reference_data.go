package model

import (
	"time"

	"github.com/shopspring/decimal"
)

// ReferenceField identifies a derivative reference-data field whose presence
// and freshness must be tracked independently. Venue payloads may contain only a
// subset of these fields.
type ReferenceField uint8

const (
	ReferenceFieldFundingRate ReferenceField = iota
	ReferenceFieldNextFundingTime
	ReferenceFieldFundingInterval
	ReferenceFieldMarkPrice
	ReferenceFieldIndexPrice
	ReferenceFieldOraclePrice
	ReferenceFieldPremium
)

// ReferenceFieldMask records which fields in a derivative reference snapshot
// are actually present. A missing field is not the same as a zero value.
type ReferenceFieldMask uint64

const (
	ReferenceHasFundingRate ReferenceFieldMask = 1 << iota
	ReferenceHasNextFundingTime
	ReferenceHasFundingInterval
	ReferenceHasMarkPrice
	ReferenceHasIndexPrice
	ReferenceHasOraclePrice
	ReferenceHasPremium
)

func (m ReferenceFieldMask) Has(flag ReferenceFieldMask) bool { return m&flag != 0 }

func (m ReferenceFieldMask) With(flag ReferenceFieldMask) ReferenceFieldMask {
	return m | flag
}

// FieldFreshness records per-field timing for merged derivative reference data.
// Snapshot-level timestamps describe the latest accepted update; consumers use
// this struct when they need to know whether a retained field itself is fresh.
type FieldFreshness struct {
	Venue    time.Time
	Received time.Time
}

// ReferenceFieldTimes stores freshness independently for each optional
// derivative reference field.
type ReferenceFieldTimes struct {
	FundingRate     FieldFreshness
	NextFundingTime FieldFreshness
	FundingInterval FieldFreshness
	MarkPrice       FieldFreshness
	IndexPrice      FieldFreshness
	OraclePrice     FieldFreshness
	Premium         FieldFreshness
}

func (t ReferenceFieldTimes) For(field ReferenceField) FieldFreshness {
	switch field {
	case ReferenceFieldFundingRate:
		return t.FundingRate
	case ReferenceFieldNextFundingTime:
		return t.NextFundingTime
	case ReferenceFieldFundingInterval:
		return t.FundingInterval
	case ReferenceFieldMarkPrice:
		return t.MarkPrice
	case ReferenceFieldIndexPrice:
		return t.IndexPrice
	case ReferenceFieldOraclePrice:
		return t.OraclePrice
	case ReferenceFieldPremium:
		return t.Premium
	default:
		return FieldFreshness{}
	}
}

func (t *ReferenceFieldTimes) Set(field ReferenceField, freshness FieldFreshness) {
	switch field {
	case ReferenceFieldFundingRate:
		t.FundingRate = freshness
	case ReferenceFieldNextFundingTime:
		t.NextFundingTime = freshness
	case ReferenceFieldFundingInterval:
		t.FundingInterval = freshness
	case ReferenceFieldMarkPrice:
		t.MarkPrice = freshness
	case ReferenceFieldIndexPrice:
		t.IndexPrice = freshness
	case ReferenceFieldOraclePrice:
		t.OraclePrice = freshness
	case ReferenceFieldPremium:
		t.Premium = freshness
	}
}

// DerivativeReferenceSnapshot is the normalized current funding/reference-price
// snapshot for derivatives. It can represent partial venue payloads; Fields and
// FieldTimes declare which values are present and how fresh each retained value
// is.
type DerivativeReferenceSnapshot struct {
	InstrumentID    InstrumentID
	FundingRate     decimal.Decimal
	NextFundingTime time.Time
	FundingInterval time.Duration
	MarkPrice       decimal.Decimal
	IndexPrice      decimal.Decimal
	OraclePrice     decimal.Decimal
	Premium         decimal.Decimal
	Timestamp       time.Time
	ReceivedAt      time.Time
	StaleAfter      time.Duration
	Fields          ReferenceFieldMask
	FieldTimes      ReferenceFieldTimes
}

// OpenInterestFieldMask records which current OI fields are present in a
// normalized open-interest snapshot.
type OpenInterestFieldMask uint64

const (
	OpenInterestHasQuantity OpenInterestFieldMask = 1 << iota
	OpenInterestHasNotional
	OpenInterestHasUnit
)

func (m OpenInterestFieldMask) Has(flag OpenInterestFieldMask) bool { return m&flag != 0 }

func (m OpenInterestFieldMask) With(flag OpenInterestFieldMask) OpenInterestFieldMask {
	return m | flag
}

// OpenInterestSnapshot is a query-only phase-one model. It is intentionally not
// a market event and is not stored in runtime cache.
type OpenInterestSnapshot struct {
	InstrumentID         InstrumentID
	OpenInterest         decimal.Decimal
	OpenInterestNotional decimal.Decimal
	Unit                 string
	Timestamp            time.Time
	ReceivedAt           time.Time
	Fields               OpenInterestFieldMask
}

// FundingRateHistoryQuery bounds optional venue funding-history requests.
type FundingRateHistoryQuery struct {
	Start time.Time
	End   time.Time
	Limit int
}

// FundingRateHistoryEntry is the normalized optional funding history row.
type FundingRateHistoryEntry struct {
	InstrumentID InstrumentID
	FundingRate  decimal.Decimal
	MarkPrice    decimal.Decimal
	IndexPrice   decimal.Decimal
	OraclePrice  decimal.Decimal
	Timestamp    time.Time
	Fields       ReferenceFieldMask
}

// OpenInterestHistoryQuery bounds optional venue OI-history requests.
type OpenInterestHistoryQuery struct {
	Start    time.Time
	End      time.Time
	Limit    int
	Interval string
}

// OpenInterestHistoryEntry is the normalized optional OI history row.
type OpenInterestHistoryEntry struct {
	InstrumentID         InstrumentID
	OpenInterest         decimal.Decimal
	OpenInterestNotional decimal.Decimal
	Unit                 string
	Timestamp            time.Time
	Fields               OpenInterestFieldMask
}
