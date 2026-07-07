package exec_test

import (
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
