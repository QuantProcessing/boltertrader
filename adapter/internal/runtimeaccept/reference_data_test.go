package runtimeaccept

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

func TestCheckReferenceDataReadOnlyUsesRuntimeCacheAndQueryOnlyOI(t *testing.T) {
	market := runtimetest.NewFakeMarket()
	defer market.Close()

	id := model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	now := time.Now()
	market.SetOpenInterestSnapshot(model.OpenInterestSnapshot{
		InstrumentID: id,
		OpenInterest: decimal.RequireFromString("12345"),
		Unit:         "contracts",
		Timestamp:    now,
		ReceivedAt:   now,
		Fields:       model.OpenInterestHasQuantity.With(model.OpenInterestHasUnit),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	var report ReferenceDataReadReport
	var err error
	go func() {
		defer close(done)
		report, err = CheckReferenceDataReadOnly(ctx, market, id, ReferenceDataReadOptions{Label: "fake reference", Timeout: 3 * time.Second})
	}()

	time.Sleep(100 * time.Millisecond)
	market.EmitDerivativeReference(model.DerivativeReferenceSnapshot{
		InstrumentID: id,
		FundingRate:  decimal.RequireFromString("0.0001"),
		MarkPrice:    decimal.RequireFromString("64000"),
		IndexPrice:   decimal.RequireFromString("63990"),
		Timestamp:    now,
		ReceivedAt:   time.Now(),
		Fields: model.ReferenceHasFundingRate.
			With(model.ReferenceHasMarkPrice).
			With(model.ReferenceHasIndexPrice),
	})

	<-done
	if err != nil {
		t.Fatalf("CheckReferenceDataReadOnly: %v", err)
	}
	if !report.Reference.Fields.Has(model.ReferenceHasFundingRate) || !report.Reference.Fields.Has(model.ReferenceHasMarkPrice) || !report.Reference.Fields.Has(model.ReferenceHasIndexPrice) {
		t.Fatalf("reference fields=%b, want funding+mark+index", report.Reference.Fields)
	}
	if !report.OpenInterest.OpenInterest.Equal(decimal.RequireFromString("12345")) {
		t.Fatalf("open interest=%s", report.OpenInterest.OpenInterest)
	}
}
