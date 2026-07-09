package model

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/shopspring/decimal"
)

func TestReferenceFieldMaskDistinguishesMissingFromZero(t *testing.T) {
	s := DerivativeReferenceSnapshot{
		InstrumentID: InstrumentID{Venue: "BINANCE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		FundingRate:  decimal.Zero,
		Fields:       ReferenceHasFundingRate,
	}
	if !s.Fields.Has(ReferenceHasFundingRate) {
		t.Fatal("present zero funding rate should be distinguishable from missing")
	}
	if s.Fields.Has(ReferenceHasMarkPrice) {
		t.Fatal("unset mark price should remain missing")
	}
}

func TestReferenceFieldTimesArePerField(t *testing.T) {
	venueFunding := time.Unix(100, 0)
	recvFunding := time.Unix(101, 0)
	venueMark := time.Unix(200, 0)
	recvMark := time.Unix(201, 0)

	var times ReferenceFieldTimes
	times.Set(ReferenceFieldFundingRate, FieldFreshness{Venue: venueFunding, Received: recvFunding})
	times.Set(ReferenceFieldMarkPrice, FieldFreshness{Venue: venueMark, Received: recvMark})

	if got := times.For(ReferenceFieldFundingRate); !got.Venue.Equal(venueFunding) || !got.Received.Equal(recvFunding) {
		t.Fatalf("funding freshness=%+v", got)
	}
	if got := times.For(ReferenceFieldMarkPrice); !got.Venue.Equal(venueMark) || !got.Received.Equal(recvMark) {
		t.Fatalf("mark freshness=%+v", got)
	}
	if got := times.For(ReferenceFieldIndexPrice); !got.Venue.IsZero() || !got.Received.IsZero() {
		t.Fatalf("missing field freshness should be zero, got %+v", got)
	}
}

func TestReferenceSnapshotKeepsDecimalPrecision(t *testing.T) {
	s := DerivativeReferenceSnapshot{
		MarkPrice: decimal.RequireFromString("64251.12345678"),
		Premium:   decimal.RequireFromString("0.00012345"),
		Fields:    ReferenceHasMarkPrice.With(ReferenceHasPremium),
	}
	if !s.MarkPrice.Equal(decimal.RequireFromString("64251.12345678")) {
		t.Fatalf("mark price precision lost: %s", s.MarkPrice)
	}
	if !s.Premium.Equal(decimal.RequireFromString("0.00012345")) {
		t.Fatalf("premium precision lost: %s", s.Premium)
	}
}

func TestOpenInterestSnapshotPresenceFlags(t *testing.T) {
	s := OpenInterestSnapshot{
		OpenInterest: decimal.Zero,
		Unit:         "contracts",
		Fields:       OpenInterestHasQuantity.With(OpenInterestHasUnit),
	}
	if !s.Fields.Has(OpenInterestHasQuantity) || !s.Fields.Has(OpenInterestHasUnit) {
		t.Fatalf("OI presence flags not retained: %b", s.Fields)
	}
	if s.Fields.Has(OpenInterestHasNotional) {
		t.Fatal("notional should be missing until a venue supplies it")
	}
}
