package runtimeaccept

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
)

type ReferenceDataReadOptions struct {
	Label       string
	Timeout     time.Duration
	MaxFieldAge time.Duration
}

type ReferenceDataReadReport struct {
	Reference    model.DerivativeReferenceSnapshot
	OpenInterest model.OpenInterestSnapshot
}

// CheckReferenceDataReadOnly verifies the phase-one funding/reference-data
// contract without submitting, canceling, modifying, or querying orders.
func CheckReferenceDataReadOnly(ctx context.Context, market contract.MarketDataClient, id model.InstrumentID, opts ReferenceDataReadOptions) (ReferenceDataReadReport, error) {
	label := opts.Label
	if label == "" {
		label = id.String()
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	maxFieldAge := opts.MaxFieldAge
	if maxFieldAge <= 0 {
		maxFieldAge = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if market == nil {
		return ReferenceDataReadReport{}, fmt.Errorf("%s: market client is nil", label)
	}
	refClient, ok := market.(contract.DerivativeReferenceDataClient)
	if !ok {
		return ReferenceDataReadReport{}, fmt.Errorf("%s: market client does not implement DerivativeReferenceDataClient", label)
	}
	if _, ok := market.(contract.OpenInterestClient); !ok {
		return ReferenceDataReadReport{}, fmt.Errorf("%s: market client does not implement OpenInterestClient", label)
	}
	if market.Capabilities().ReferenceData.OpenInterestCached {
		return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest must remain query-only, but capability advertises OpenInterestCached", label)
	}

	node := btruntime.NewNode(btruntime.Clients{Market: market}, clock.NewRealClock(), "reference-read")
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()
	go func() {
		defer close(done)
		node.Run(runCtx)
	}()
	if err := WaitForActive(ctx, node); err != nil {
		return ReferenceDataReadReport{}, fmt.Errorf("%s: runtime active: %w", label, err)
	}

	subscribeStarted := time.Now().Add(-time.Second)
	if err := refClient.SubscribeReference(ctx, id); err != nil {
		return ReferenceDataReadReport{}, fmt.Errorf("%s: subscribe reference: %w", label, err)
	}
	ref, err := waitForReferenceFields(ctx, node, id, subscribeStarted, maxFieldAge)
	if err != nil {
		return ReferenceDataReadReport{}, fmt.Errorf("%s: %w", label, err)
	}

	oi, err := node.OpenInterest(ctx, id)
	if err != nil {
		return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest: %w", label, err)
	}
	if oi.InstrumentID != id {
		return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest instrument=%s, want %s", label, oi.InstrumentID, id)
	}
	if !oi.Fields.Has(model.OpenInterestHasQuantity) {
		return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest missing quantity field: %+v", label, oi)
	}
	if oi.Timestamp.IsZero() || oi.ReceivedAt.IsZero() {
		return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest missing timestamps: %+v", label, oi)
	}
	if _, ok := any(node.Cache).(interface {
		OpenInterest(model.InstrumentID) (model.OpenInterestSnapshot, bool)
	}); ok {
		return ReferenceDataReadReport{}, fmt.Errorf("%s: runtime cache unexpectedly exposes open-interest storage", label)
	}

	return ReferenceDataReadReport{Reference: ref, OpenInterest: oi}, nil
}

func waitForReferenceFields(ctx context.Context, node *btruntime.TradingNode, id model.InstrumentID, started time.Time, maxAge time.Duration) (model.DerivativeReferenceSnapshot, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var last model.DerivativeReferenceSnapshot
	var saw bool
	for {
		if ref, ok := node.Cache.DerivativeReference(id); ok {
			last = ref
			saw = true
			if referenceReady(ref, started, maxAge) {
				return ref, nil
			}
		}
		select {
		case <-ctx.Done():
			if !saw {
				return model.DerivativeReferenceSnapshot{}, fmt.Errorf("timed out waiting for derivative reference cache for %s: %w", id, ctx.Err())
			}
			return model.DerivativeReferenceSnapshot{}, fmt.Errorf("timed out waiting for fresh funding/mark/index-or-oracle for %s; last fields=%b snapshot=%+v: %w", id, last.Fields, last, ctx.Err())
		case <-ticker.C:
		}
	}
}

func referenceReady(ref model.DerivativeReferenceSnapshot, started time.Time, maxAge time.Duration) bool {
	if ref.InstrumentID.Symbol == "" || ref.Timestamp.IsZero() || ref.ReceivedAt.IsZero() {
		return false
	}
	if !ref.Fields.Has(model.ReferenceHasFundingRate) || !ref.Fields.Has(model.ReferenceHasMarkPrice) {
		return false
	}
	if !ref.Fields.Has(model.ReferenceHasIndexPrice) && !ref.Fields.Has(model.ReferenceHasOraclePrice) {
		return false
	}
	return referenceFieldFresh(ref.FieldTimes.For(model.ReferenceFieldFundingRate), ref.ReceivedAt, started, maxAge) &&
		referenceFieldFresh(ref.FieldTimes.For(model.ReferenceFieldMarkPrice), ref.ReceivedAt, started, maxAge) &&
		(referenceFieldFresh(ref.FieldTimes.For(model.ReferenceFieldIndexPrice), ref.ReceivedAt, started, maxAge) ||
			referenceFieldFresh(ref.FieldTimes.For(model.ReferenceFieldOraclePrice), ref.ReceivedAt, started, maxAge))
}

func referenceFieldFresh(freshness model.FieldFreshness, fallbackReceived time.Time, started time.Time, maxAge time.Duration) bool {
	received := freshness.Received
	if received.IsZero() {
		received = fallbackReceived
	}
	if received.Before(started) {
		return false
	}
	return time.Since(received) <= maxAge
}
