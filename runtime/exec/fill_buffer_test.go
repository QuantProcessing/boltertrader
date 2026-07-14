package exec_test

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/shopspring/decimal"
)

func TestFillBufferCountIncludesVenueOnlyFills(t *testing.T) {
	buf := exec.NewFillBuffer()
	fill := model.Fill{
		InstrumentID: model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		VenueOrderID: "venue-only",
		TradeID:      "trade-1",
		Side:         enums.SideBuy,
		Price:        decimal.NewFromInt(100),
		Quantity:     decimal.NewFromInt(1),
		Timestamp:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	buf.Buffer(fill)
	if got := buf.Count(); got != 1 {
		t.Fatalf("count=%d, want 1", got)
	}
}

func TestFillBufferDedupesByAccountID(t *testing.T) {
	buf := exec.NewFillBuffer()
	fill := model.Fill{
		AccountID:    "acct-a",
		InstrumentID: model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		VenueOrderID: "venue",
		TradeID:      "shared-trade",
		Side:         enums.SideBuy,
		Price:        decimal.NewFromInt(100),
		Quantity:     decimal.NewFromInt(1),
		Timestamp:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if !buf.MarkApplied(fill) {
		t.Fatal("first account fill should apply")
	}
	fill.AccountID = "acct-b"
	if !buf.MarkApplied(fill) {
		t.Fatal("same venue/trade id on another account should apply independently")
	}
	fill.AccountID = "acct-a"
	if buf.MarkApplied(fill) {
		t.Fatal("same account fill should be deduped")
	}
}

func TestFillBufferDrainsOnlyMatchingAccountScope(t *testing.T) {
	buf := exec.NewFillBuffer()
	inst := model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	fill := model.Fill{
		AccountID:    "acct-a",
		InstrumentID: inst,
		ClientID:     "same-client",
		VenueOrderID: "same-venue",
		TradeID:      "trade-a",
		Side:         enums.SideBuy,
		Price:        decimal.NewFromInt(100),
		Quantity:     decimal.NewFromInt(1),
		Timestamp:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	buf.Buffer(fill)
	buf.Buffer(model.Fill{
		InstrumentID: inst,
		ClientID:     "same-client",
		VenueOrderID: "same-venue",
		TradeID:      "trade-unscoped",
		Side:         enums.SideBuy,
		Price:        decimal.NewFromInt(100),
		Quantity:     decimal.NewFromInt(1),
		Timestamp:    time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC),
	})

	acctB := model.Order{Request: model.OrderRequest{AccountID: "acct-b", ClientID: "same-client"}, VenueOrderID: "same-venue"}
	drained := buf.DrainBuffered(acctB)
	if len(drained) != 1 || drained[0].Fill.TradeID != "trade-unscoped" {
		t.Fatalf("acct-b drained=%+v, want only unscoped fill", drained)
	}
	if got := buf.Count(); got != 1 {
		t.Fatalf("remaining buffered fills=%d, want acct-a fill retained", got)
	}

	acctA := model.Order{Request: model.OrderRequest{AccountID: "acct-a", ClientID: "same-client"}, VenueOrderID: "same-venue"}
	drained = buf.DrainBuffered(acctA)
	if len(drained) != 1 || drained[0].Fill.TradeID != "trade-a" {
		t.Fatalf("acct-a drained=%+v, want acct-a fill", drained)
	}
	if got := buf.Count(); got != 0 {
		t.Fatalf("remaining buffered fills=%d, want none", got)
	}
}

func TestFillBufferTracksPriorAppliedQuantityPerOrder(t *testing.T) {
	buffer := exec.NewFillBufferWithAppliedLimit(2)
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	fill := func(tradeID, venueOrderID, qty string) model.Fill {
		return model.Fill{
			AccountID: "acct", InstrumentID: id, VenueOrderID: venueOrderID,
			TradeID: tradeID, Quantity: decimal.RequireFromString(qty),
		}
	}

	applied, prior := buffer.MarkAppliedWithCoverage(fill("t1", "o1", "0.6"))
	if !applied || !prior.IsZero() {
		t.Fatalf("first applied=%v prior=%s, want true/0", applied, prior)
	}
	applied, prior = buffer.MarkAppliedWithCoverage(fill("t2", "o1", "0.4"))
	if !applied || !prior.Equal(decimal.RequireFromString("0.6")) {
		t.Fatalf("second applied=%v prior=%s, want true/0.6", applied, prior)
	}
	if applied, _ = buffer.MarkAppliedWithCoverage(fill("t2", "o1", "0.4")); applied {
		t.Fatal("duplicate retained trade id was applied")
	}
	if applied, _ = buffer.MarkAppliedWithCoverage(fill("other", "o2", "1")); !applied {
		t.Fatal("different order fill was not applied")
	}
	// Trade-ID retention is bounded, but active-order coverage must retain the
	// full applied quantity until the order is explicitly marked terminal.
	applied, prior = buffer.MarkAppliedWithCoverage(fill("t1", "o1", "0.6"))
	if !applied || !prior.Equal(decimal.RequireFromString("1")) {
		t.Fatalf("replayed evicted fill applied=%v prior=%s, want true/1", applied, prior)
	}
}

func TestFillBufferActiveCoverageSurvivesGlobalTradeIDEviction(t *testing.T) {
	buffer := exec.NewFillBufferWithAppliedLimit(2)
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	otherID := model.InstrumentID{Venue: "TEST", Symbol: "ETH-USDC", Kind: enums.KindSpot}
	fill := func(instrument model.InstrumentID, tradeID, venueOrderID string) model.Fill {
		return model.Fill{
			AccountID: "acct", InstrumentID: instrument, VenueOrderID: venueOrderID,
			TradeID: tradeID, Quantity: decimal.NewFromInt(1),
		}
	}
	for _, item := range []model.Fill{
		fill(id, "a-1", "order-a"),
		fill(id, "a-2", "order-a"),
		fill(otherID, "b-1", "order-b"), // globally evicts a-1
	} {
		if applied, _, err := buffer.MarkAppliedWithCoverageChecked(item); err != nil || !applied {
			t.Fatalf("mark %+v applied=%v err=%v", item, applied, err)
		}
	}
	if applied, prior, err := buffer.MarkAppliedWithCoverageChecked(fill(id, "a-3", "order-a")); err != nil || !applied || !prior.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("a-3 applied=%v prior=%s err=%v, want true/2/nil", applied, prior, err)
	}
}

func TestFillBufferTerminalCoverageReleasesAfterDedupeWindowEvicts(t *testing.T) {
	buffer := exec.NewFillBufferWithAppliedLimit(1)
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	fill := func(tradeID string) model.Fill {
		return model.Fill{AccountID: "acct", InstrumentID: id, ClientID: "client-a", VenueOrderID: "venue-a", TradeID: tradeID, Quantity: decimal.NewFromInt(1)}
	}
	if applied, _, err := buffer.MarkAppliedWithCoverageChecked(fill("a-1")); err != nil || !applied {
		t.Fatalf("a-1 applied=%v err=%v", applied, err)
	}
	if err := buffer.MarkOrderTerminal(model.Order{
		Request:      model.OrderRequest{AccountID: "acct", InstrumentID: id, ClientID: "client-a"},
		VenueOrderID: "venue-a", Status: enums.StatusFilled,
	}); err != nil {
		t.Fatalf("mark order terminal: %v", err)
	}
	other := model.Fill{
		AccountID: "acct", InstrumentID: model.InstrumentID{Venue: "TEST", Symbol: "ETH-USDC", Kind: enums.KindSpot},
		VenueOrderID: "venue-b", TradeID: "b-1", Quantity: decimal.NewFromInt(1),
	}
	if applied, _, err := buffer.MarkAppliedWithCoverageChecked(other); err != nil || !applied {
		t.Fatalf("b-1 applied=%v err=%v", applied, err)
	}
	if applied, prior, err := buffer.MarkAppliedWithCoverageChecked(fill("a-2")); err != nil || !applied || !prior.IsZero() {
		t.Fatalf("a-2 applied=%v prior=%s err=%v, want released terminal coverage", applied, prior, err)
	}
}

func TestFillBufferRejectsCrossedCompleteAliasGroups(t *testing.T) {
	buffer := exec.NewFillBuffer()
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	fill := func(tradeID, clientID, venueOrderID string) model.Fill {
		return model.Fill{
			AccountID: "acct", InstrumentID: id, ClientID: clientID, VenueOrderID: venueOrderID,
			TradeID: tradeID, Quantity: decimal.NewFromInt(1),
		}
	}
	for _, item := range []model.Fill{fill("a-1", "client-a", "venue-a"), fill("b-1", "client-b", "venue-b")} {
		if applied, _, err := buffer.MarkAppliedWithCoverageChecked(item); err != nil || !applied {
			t.Fatalf("mark %+v applied=%v err=%v", item, applied, err)
		}
	}
	if applied, _, err := buffer.MarkAppliedWithCoverageChecked(fill("crossed", "client-a", "venue-b")); !errors.Is(err, exec.ErrFillOrderAliasConflict) || applied {
		t.Fatalf("crossed aliases applied=%v err=%v, want alias conflict", applied, err)
	}
	if applied, prior, err := buffer.MarkAppliedWithCoverageChecked(fill("a-2", "client-a", "venue-a")); err != nil || !applied || !prior.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("order A coverage contaminated: applied=%v prior=%s err=%v", applied, prior, err)
	}
	if applied, prior, err := buffer.MarkAppliedWithCoverageChecked(fill("b-2", "client-b", "venue-b")); err != nil || !applied || !prior.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("order B coverage contaminated: applied=%v prior=%s err=%v", applied, prior, err)
	}
}

func TestFillBufferCoverageKeepsClientIdentityWhenVenueIDAppears(t *testing.T) {
	buffer := exec.NewFillBuffer()
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	first := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "client-1",
		TradeID: "t1", Quantity: decimal.RequireFromString("0.6"),
	}
	second := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "client-1", VenueOrderID: "venue-1",
		TradeID: "t2", Quantity: decimal.RequireFromString("0.4"),
	}
	if applied, prior := buffer.MarkAppliedWithCoverage(first); !applied || !prior.IsZero() {
		t.Fatalf("first applied=%v prior=%s, want true/0", applied, prior)
	}
	if applied, prior := buffer.MarkAppliedWithCoverage(second); !applied || !prior.Equal(decimal.RequireFromString("0.6")) {
		t.Fatalf("second applied=%v prior=%s, want true/0.6 across venue-id transition", applied, prior)
	}
}

func TestFillBufferCoverageKeepsVenueIdentityWhenClientIDAppears(t *testing.T) {
	buffer := exec.NewFillBuffer()
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	first := model.Fill{
		AccountID: "acct", InstrumentID: id, VenueOrderID: "venue-1",
		TradeID: "t1", Quantity: decimal.RequireFromString("0.6"),
	}
	second := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "client-1", VenueOrderID: "venue-1",
		TradeID: "t2", Quantity: decimal.RequireFromString("0.4"),
	}
	if applied, prior := buffer.MarkAppliedWithCoverage(first); !applied || !prior.IsZero() {
		t.Fatalf("first applied=%v prior=%s, want true/0", applied, prior)
	}
	if applied, prior := buffer.MarkAppliedWithCoverage(second); !applied || !prior.Equal(decimal.RequireFromString("0.6")) {
		t.Fatalf("second applied=%v prior=%s, want true/0.6 across client-id enrichment", applied, prior)
	}
}

func TestFillBufferCoverageBridgesPreviouslySeparatedOrderAliases(t *testing.T) {
	buffer := exec.NewFillBuffer()
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	clientOnly := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "client-1",
		TradeID: "t1", Quantity: decimal.RequireFromString("0.2"),
	}
	venueOnly := model.Fill{
		AccountID: "acct", InstrumentID: id, VenueOrderID: "venue-1",
		TradeID: "t2", Quantity: decimal.RequireFromString("0.3"),
	}
	bridged := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "client-1", VenueOrderID: "venue-1",
		TradeID: "t3", Quantity: decimal.RequireFromString("0.1"),
	}
	for _, fill := range []model.Fill{clientOnly, venueOnly} {
		if applied, _ := buffer.MarkAppliedWithCoverage(fill); !applied {
			t.Fatalf("fill %+v should apply", fill)
		}
	}
	if applied, prior := buffer.MarkAppliedWithCoverage(bridged); !applied || !prior.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("bridged applied=%v prior=%s, want true/0.5 from both aliases", applied, prior)
	}
}

func TestFillBufferCoverageEvictsThroughMergedAliasGroups(t *testing.T) {
	buffer := exec.NewFillBufferWithAppliedLimit(2)
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	fill := func(tradeID, clientID, venueOrderID, qty string) model.Fill {
		return model.Fill{
			AccountID: "acct", InstrumentID: id, ClientID: clientID, VenueOrderID: venueOrderID,
			TradeID: tradeID, Quantity: decimal.RequireFromString(qty),
		}
	}
	for _, item := range []model.Fill{
		fill("t1", "client-1", "", "0.2"),
		fill("t2", "", "venue-1", "0.3"),
	} {
		if applied, _ := buffer.MarkAppliedWithCoverage(item); !applied {
			t.Fatalf("fill %+v should apply", item)
		}
	}
	if applied, prior := buffer.MarkAppliedWithCoverage(fill("t3", "client-1", "venue-1", "0.1")); !applied || !prior.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("bridge applied=%v prior=%s, want true/0.5", applied, prior)
	}
	if applied, prior := buffer.MarkAppliedWithCoverage(fill("t4", "", "venue-1", "0.2")); !applied || !prior.Equal(decimal.RequireFromString("0.6")) {
		t.Fatalf("post-merge eviction applied=%v prior=%s, want true/0.6", applied, prior)
	}
	if applied, prior := buffer.MarkAppliedWithCoverage(fill("t5", "client-1", "", "0.1")); !applied || !prior.Equal(decimal.RequireFromString("0.8")) {
		t.Fatalf("source-group eviction applied=%v prior=%s, want true/0.8", applied, prior)
	}
}

func TestFillBufferDuplicateEnrichmentBindsVenueAliasForCoverage(t *testing.T) {
	buffer := exec.NewFillBuffer()
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	first := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "client-1",
		TradeID: "t1", Quantity: decimal.RequireFromString("0.6"),
	}
	if applied, _ := buffer.MarkAppliedWithCoverage(first); !applied {
		t.Fatal("first fill should apply")
	}
	enrichedDuplicate := first
	enrichedDuplicate.VenueOrderID = "venue-1"
	if applied, _ := buffer.MarkAppliedWithCoverage(enrichedDuplicate); applied {
		t.Fatal("enriched duplicate should not apply")
	}
	venueOnlyNextTrade := model.Fill{
		AccountID: "acct", InstrumentID: id, VenueOrderID: "venue-1",
		TradeID: "t2", Quantity: decimal.RequireFromString("0.4"),
	}
	if applied, prior := buffer.MarkAppliedWithCoverage(venueOnlyNextTrade); !applied || !prior.Equal(decimal.RequireFromString("0.6")) {
		t.Fatalf("next fill applied=%v prior=%s, want true/0.6 through enriched duplicate alias", applied, prior)
	}
}

func TestFillBufferConflictingDuplicateDoesNotBindAnotherOrderAlias(t *testing.T) {
	buffer := exec.NewFillBuffer()
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	first := model.Fill{
		AccountID: "acct", InstrumentID: id, VenueOrderID: "venue-1",
		TradeID: "shared-trade", Quantity: decimal.RequireFromString("0.6"),
	}
	if applied, _ := buffer.MarkAppliedWithCoverage(first); !applied {
		t.Fatal("first fill should apply")
	}
	conflictingDuplicate := first
	conflictingDuplicate.VenueOrderID = "venue-2"
	if applied, _ := buffer.MarkAppliedWithCoverage(conflictingDuplicate); applied {
		t.Fatal("conflicting duplicate should not apply")
	}
	nextTradeOnOtherOrder := model.Fill{
		AccountID: "acct", InstrumentID: id, VenueOrderID: "venue-2",
		TradeID: "next-trade", Quantity: decimal.RequireFromString("0.4"),
	}
	if applied, prior := buffer.MarkAppliedWithCoverage(nextTradeOnOtherOrder); !applied || !prior.IsZero() {
		t.Fatalf("other order applied=%v prior=%s, want true/0 without conflicting alias contamination", applied, prior)
	}
}

func TestFillBufferTerminalCoverageForgetsAliasesAfterRetentionEviction(t *testing.T) {
	buffer := exec.NewFillBufferWithAppliedLimit(1)
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	old := model.Fill{
		AccountID: "acct", InstrumentID: id, VenueOrderID: "venue-old",
		TradeID: "old", Quantity: decimal.RequireFromString("0.6"),
	}
	unrelated := model.Fill{
		AccountID: "acct", InstrumentID: id, VenueOrderID: "venue-other",
		TradeID: "other", Quantity: decimal.RequireFromString("1"),
	}
	if applied, _ := buffer.MarkAppliedWithCoverage(old); !applied {
		t.Fatal("old fill should apply")
	}
	if err := buffer.MarkOrderTerminal(model.Order{
		Request:      model.OrderRequest{AccountID: "acct", InstrumentID: id},
		VenueOrderID: "venue-old",
		Status:       enums.StatusFilled,
	}); err != nil {
		t.Fatalf("mark old order terminal: %v", err)
	}
	if applied, _ := buffer.MarkAppliedWithCoverage(unrelated); !applied {
		t.Fatal("unrelated fill should apply and evict old fill")
	}
	enrichedAfterEviction := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "client-new", VenueOrderID: "venue-old",
		TradeID: "new", Quantity: decimal.RequireFromString("0.4"),
	}
	if applied, prior := buffer.MarkAppliedWithCoverage(enrichedAfterEviction); !applied || !prior.IsZero() {
		t.Fatalf("post-eviction applied=%v prior=%s, want true/0 without stale alias coverage", applied, prior)
	}
}

func TestFillBufferDedupesTradeAfterVenueOrderIdentityEnrichment(t *testing.T) {
	buffer := exec.NewFillBuffer()
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	clientOnly := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: "client-1",
		TradeID: "trade-1", Quantity: decimal.RequireFromString("0.6"),
	}
	if !buffer.MarkApplied(clientOnly) {
		t.Fatal("first fill should apply")
	}
	enriched := clientOnly
	enriched.VenueOrderID = "venue-1"
	if buffer.MarkApplied(enriched) {
		t.Fatal("same trade was reapplied after venue-order identity enrichment")
	}
}

func TestFillBufferSkipsConflictingVenueOrdersForSameTradeID(t *testing.T) {
	buffer := exec.NewFillBuffer()
	id := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	first := model.Fill{
		AccountID: "acct", InstrumentID: id, VenueOrderID: "venue-1",
		TradeID: "trade-1", Quantity: decimal.RequireFromString("0.6"),
	}
	if !buffer.MarkApplied(first) {
		t.Fatal("first fill should apply")
	}
	conflicting := first
	conflicting.VenueOrderID = "venue-2"
	if buffer.MarkApplied(conflicting) {
		t.Fatal("same venue trade id on another order must be skipped as a conflicting duplicate")
	}

	otherVenue := conflicting
	otherVenue.InstrumentID.Venue = "OTHER"
	if !buffer.MarkApplied(otherVenue) {
		t.Fatal("same trade id from another venue must apply independently")
	}
}
