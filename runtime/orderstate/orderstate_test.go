package orderstate

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

var testInstrument = model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}

func TestApplyFillPromotesTerminalNonFilledOrderWhenQuantityIsComplete(t *testing.T) {
	for _, status := range []enums.OrderStatus{enums.StatusCanceled, enums.StatusExpired, enums.StatusRejected, enums.StatusUnknown} {
		t.Run(status.String(), func(t *testing.T) {
			order := model.Order{
				Request: model.OrderRequest{
					InstrumentID: testInstrument,
					ClientID:     "late-fill",
					Quantity:     decimal.NewFromInt(1),
				},
				Status: status,
			}
			fill := model.Fill{
				InstrumentID: testInstrument,
				ClientID:     "late-fill",
				TradeID:      "late-fill-trade",
				Price:        decimal.NewFromInt(100),
				Quantity:     decimal.NewFromInt(1),
				Timestamp:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			}
			got := ApplyFill(order, fill, time.Time{})
			if got.Status != enums.StatusFilled || !got.FilledQty.Equal(decimal.NewFromInt(1)) {
				t.Fatalf("order=%+v, want FILLED qty 1", got)
			}
		})
	}
}

func TestApplyFillPreservesCanceledStatusForPartialLateFill(t *testing.T) {
	order := model.Order{
		Request: model.OrderRequest{
			InstrumentID: testInstrument,
			ClientID:     "partial-late-fill",
			Quantity:     decimal.NewFromInt(2),
		},
		Status: enums.StatusCanceled,
	}
	fill := model.Fill{
		InstrumentID: testInstrument,
		ClientID:     "partial-late-fill",
		TradeID:      "partial-late-fill-trade",
		Price:        decimal.NewFromInt(100),
		Quantity:     decimal.NewFromInt(1),
		Timestamp:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	got := ApplyFill(order, fill, time.Time{})
	if got.Status != enums.StatusCanceled || !got.FilledQty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("order=%+v, want CANCELED with qty 1", got)
	}
}

func TestApplyFillDoesNotDoubleCountVenueCumulativeQuantity(t *testing.T) {
	order := model.Order{
		Request: model.OrderRequest{
			InstrumentID: testInstrument,
			ClientID:     "cumulative-before-fill",
			Quantity:     decimal.NewFromInt(1),
		},
		Status:       enums.StatusFilled,
		FilledQty:    decimal.NewFromInt(1),
		AvgFillPrice: decimal.NewFromInt(100),
	}
	fill := model.Fill{
		InstrumentID: testInstrument,
		ClientID:     "cumulative-before-fill",
		TradeID:      "trade-1",
		Price:        decimal.NewFromInt(100),
		Quantity:     decimal.NewFromInt(1),
		Timestamp:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	got := ApplyFill(order, fill, time.Time{})
	if !got.FilledQty.Equal(decimal.NewFromInt(1)) || !got.AvgFillPrice.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("order=%+v, want cumulative quantity and average unchanged", got)
	}
}

func TestMergePromotesFillBeforeAckWhenCumulativeQuantityIsComplete(t *testing.T) {
	existing := model.Order{
		Request: model.OrderRequest{
			AccountID:    "account",
			InstrumentID: testInstrument,
			ClientID:     "fill-before-ack",
		},
		VenueOrderID: "venue-order",
		Status:       enums.StatusPartiallyFilled,
		FilledQty:    decimal.NewFromInt(1),
		AvgFillPrice: decimal.NewFromInt(100),
	}
	incoming := model.Order{
		Request: model.OrderRequest{
			AccountID:    "account",
			InstrumentID: testInstrument,
			ClientID:     "fill-before-ack",
			Quantity:     decimal.NewFromInt(1),
		},
		VenueOrderID: "venue-order",
		Status:       enums.StatusNew,
	}
	got := Merge(existing, incoming)
	if got.Status != enums.StatusFilled || !got.FilledQty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("order=%+v, want FILLED qty 1", got)
	}
}

func TestMergePreservesNewerKnownTerminalStatusOverStaleTerminal(t *testing.T) {
	newer := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	older := newer.Add(-time.Minute)
	existing := model.Order{
		Request:   model.OrderRequest{InstrumentID: testInstrument, ClientID: "terminal"},
		Status:    enums.StatusCanceled,
		UpdatedAt: newer,
	}
	incoming := model.Order{
		Request:   model.OrderRequest{InstrumentID: testInstrument, ClientID: "terminal"},
		Status:    enums.StatusRejected,
		UpdatedAt: older,
	}
	got := Merge(existing, incoming)
	if got.Status != enums.StatusCanceled || !got.UpdatedAt.Equal(newer) {
		t.Fatalf("order=%+v, want CANCELED with newer cached timestamp", got)
	}
}

func TestMergeAllowsLaterTerminalCorrection(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)
	existing := model.Order{
		Request:   model.OrderRequest{InstrumentID: testInstrument, ClientID: "terminal"},
		Status:    enums.StatusCanceled,
		UpdatedAt: older,
	}
	incoming := model.Order{
		Request:   model.OrderRequest{InstrumentID: testInstrument, ClientID: "terminal"},
		Status:    enums.StatusRejected,
		UpdatedAt: newer,
	}
	got := Merge(existing, incoming)
	if got.Status != enums.StatusRejected || !got.UpdatedAt.Equal(newer) {
		t.Fatalf("order=%+v, want newer REJECTED correction", got)
	}
}

func TestMergeDoesNotDowngradeKnownTerminalToUnknown(t *testing.T) {
	existing := model.Order{
		Request: model.OrderRequest{InstrumentID: testInstrument, ClientID: "terminal"},
		Status:  enums.StatusExpired,
	}
	incoming := model.Order{
		Request: model.OrderRequest{InstrumentID: testInstrument, ClientID: "terminal"},
		Status:  enums.StatusUnknown,
	}
	got := Merge(existing, incoming)
	if got.Status != enums.StatusExpired {
		t.Fatalf("status=%s, want EXPIRED", got.Status)
	}
}

func TestMergeAllowsUnknownToKnownTerminal(t *testing.T) {
	existing := model.Order{
		Request: model.OrderRequest{InstrumentID: testInstrument, ClientID: "terminal"},
		Status:  enums.StatusUnknown,
	}
	incoming := model.Order{
		Request: model.OrderRequest{InstrumentID: testInstrument, ClientID: "terminal"},
		Status:  enums.StatusCanceled,
	}
	got := Merge(existing, incoming)
	if got.Status != enums.StatusCanceled {
		t.Fatalf("status=%s, want CANCELED", got.Status)
	}
}

func TestMergeDoesNotRegressNewerOpenOrderFromOlderSnapshot(t *testing.T) {
	newer := time.Date(2026, 7, 11, 0, 1, 0, 0, time.UTC)
	older := newer.Add(-time.Minute)
	existing := model.Order{
		Request: model.OrderRequest{
			AccountID:    "account",
			InstrumentID: testInstrument,
			ClientID:     "partial",
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     decimal.NewFromInt(2),
			Price:        decimal.NewFromInt(101),
		},
		VenueOrderID: "venue-order",
		Status:       enums.StatusPartiallyFilled,
		FilledQty:    decimal.NewFromInt(1),
		AvgFillPrice: decimal.NewFromInt(100),
		UpdatedAt:    newer,
	}
	incoming := model.Order{
		Request: model.OrderRequest{
			AccountID:    "account",
			InstrumentID: testInstrument,
			ClientID:     "partial",
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     decimal.NewFromInt(2),
			Price:        decimal.NewFromInt(99),
		},
		VenueOrderID: "venue-order",
		Status:       enums.StatusNew,
		UpdatedAt:    older,
	}

	got := Merge(existing, incoming)
	if got.Status != enums.StatusPartiallyFilled || !got.UpdatedAt.Equal(newer) {
		t.Fatalf("order=%+v, want newer PARTIALLY_FILLED lifecycle", got)
	}
	if !got.Request.Price.Equal(decimal.NewFromInt(101)) || !got.FilledQty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("older snapshot overwrote newer order fields: %+v", got)
	}
}
