package cache

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestDerivativeReferenceMergesPartialUpdatesByPresence(t *testing.T) {
	c := New()
	fundingVenue := time.Unix(100, 0)
	fundingRecv := time.Unix(101, 0)
	markVenue := time.Unix(200, 0)
	markRecv := time.Unix(201, 0)

	c.UpsertDerivativeReference(model.DerivativeReferenceSnapshot{
		InstrumentID: inst,
		FundingRate:  decimal.RequireFromString("0.0001"),
		IndexPrice:   decimal.RequireFromString("64000"),
		Timestamp:    fundingVenue,
		ReceivedAt:   fundingRecv,
		Fields:       model.ReferenceHasFundingRate.With(model.ReferenceHasIndexPrice),
	})
	c.UpsertDerivativeReference(model.DerivativeReferenceSnapshot{
		InstrumentID: inst,
		MarkPrice:    decimal.RequireFromString("64100"),
		Timestamp:    markVenue,
		ReceivedAt:   markRecv,
		Fields:       model.ReferenceHasMarkPrice,
	})

	got, ok := c.DerivativeReference(inst)
	if !ok {
		t.Fatal("missing derivative reference snapshot")
	}
	if !got.Fields.Has(model.ReferenceHasFundingRate) || !got.Fields.Has(model.ReferenceHasIndexPrice) || !got.Fields.Has(model.ReferenceHasMarkPrice) {
		t.Fatalf("merged fields=%b", got.Fields)
	}
	if !got.FundingRate.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("funding=%s", got.FundingRate)
	}
	if !got.MarkPrice.Equal(decimal.RequireFromString("64100")) {
		t.Fatalf("mark=%s", got.MarkPrice)
	}
	if !got.Timestamp.Equal(markVenue) || !got.ReceivedAt.Equal(markRecv) {
		t.Fatalf("aggregate times venue=%s recv=%s", got.Timestamp, got.ReceivedAt)
	}
	fundingFresh := got.FieldTimes.For(model.ReferenceFieldFundingRate)
	if !fundingFresh.Venue.Equal(fundingVenue) || !fundingFresh.Received.Equal(fundingRecv) {
		t.Fatalf("funding freshness changed after mark-only update: %+v", fundingFresh)
	}
	markFresh := got.FieldTimes.For(model.ReferenceFieldMarkPrice)
	if !markFresh.Venue.Equal(markVenue) || !markFresh.Received.Equal(markRecv) {
		t.Fatalf("mark freshness=%+v", markFresh)
	}
}

func TestDerivativeReferenceRejectsOlderFieldUpdates(t *testing.T) {
	c := New()
	newVenue := time.Unix(200, 0)
	newRecv := time.Unix(201, 0)
	oldVenue := time.Unix(100, 0)
	oldRecv := time.Unix(300, 0)

	c.UpsertDerivativeReference(model.DerivativeReferenceSnapshot{
		InstrumentID: inst,
		MarkPrice:    decimal.RequireFromString("64100"),
		Timestamp:    newVenue,
		ReceivedAt:   newRecv,
		Fields:       model.ReferenceHasMarkPrice,
	})
	c.UpsertDerivativeReference(model.DerivativeReferenceSnapshot{
		InstrumentID: inst,
		MarkPrice:    decimal.RequireFromString("63000"),
		Timestamp:    oldVenue,
		ReceivedAt:   oldRecv,
		Fields:       model.ReferenceHasMarkPrice,
	})

	got, ok := c.DerivativeReference(inst)
	if !ok {
		t.Fatal("missing derivative reference snapshot")
	}
	if !got.MarkPrice.Equal(decimal.RequireFromString("64100")) {
		t.Fatalf("mark was overwritten by older event: %s", got.MarkPrice)
	}
	if !got.Timestamp.Equal(newVenue) || !got.ReceivedAt.Equal(newRecv) {
		t.Fatalf("aggregate times changed after rejected older field: venue=%s recv=%s", got.Timestamp, got.ReceivedAt)
	}
	markFresh := got.FieldTimes.For(model.ReferenceFieldMarkPrice)
	if !markFresh.Venue.Equal(newVenue) || !markFresh.Received.Equal(newRecv) {
		t.Fatalf("mark freshness changed after rejected older field: %+v", markFresh)
	}
}
