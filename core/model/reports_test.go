package model

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/shopspring/decimal"
)

func TestExecutionMassStatusIndexesReports(t *testing.T) {
	inst := InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	mass := NewExecutionMassStatus("T", "acct", time.Unix(1, 0))

	order := Order{
		Request:      OrderRequest{ClientID: "c1", InstrumentID: inst, Quantity: decimal.NewFromInt(2)},
		VenueOrderID: "v1",
		Status:       enums.StatusNew,
	}
	if err := mass.AddOrderReport(OrderStatusReport{Venue: "T", AccountID: "acct", Order: order}); err != nil {
		t.Fatalf("add order report: %v", err)
	}
	fill := Fill{InstrumentID: inst, VenueOrderID: "v1", ClientID: "c1", TradeID: "t1", Quantity: decimal.NewFromInt(1)}
	if err := mass.AddFillReport(FillReport{Venue: "T", AccountID: "acct", Fill: fill}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	position := Position{InstrumentID: inst, Side: enums.PosNet, Quantity: decimal.NewFromInt(1)}
	if err := mass.AddPositionReport(PositionReport{Venue: "T", AccountID: "acct", Position: position}); err != nil {
		t.Fatalf("add position report: %v", err)
	}

	if _, ok := mass.OrderReports["v1"]; !ok {
		t.Fatalf("missing order report by venue order id: %+v", mass.OrderReports)
	}
	if got := len(mass.FillReports["v1"]); got != 1 {
		t.Fatalf("fill reports under v1=%d, want 1", got)
	}
	if got := len(mass.PositionReports[PositionReportKey("acct", position)]); got != 1 {
		t.Fatalf("position reports=%d, want 1", got)
	}
	if err := mass.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestExecutionMassStatusCloneIsolation(t *testing.T) {
	inst := InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	mass := NewExecutionMassStatus("T", "acct", time.Unix(1, 0))
	if err := mass.AddOrderReport(OrderStatusReport{
		Venue: "T",
		Order: Order{Request: OrderRequest{ClientID: "c1", InstrumentID: inst, Quantity: decimal.NewFromInt(1)}, VenueOrderID: "v1"},
	}); err != nil {
		t.Fatalf("add order report: %v", err)
	}
	clone := mass.Clone()
	delete(clone.OrderReports, "v1")
	if _, ok := mass.OrderReports["v1"]; !ok {
		t.Fatal("clone mutation removed original order report")
	}
}

func TestReportValidation(t *testing.T) {
	inst := InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	if err := (FillReport{Venue: "T", Fill: Fill{InstrumentID: inst, VenueOrderID: "v1"}}).Validate(); err == nil {
		t.Fatal("zero fill quantity should fail validation")
	}
	err := (OrderStatusReport{
		Venue: "T",
		Order: Order{
			Request:      OrderRequest{ClientID: "c1", InstrumentID: inst, Quantity: decimal.NewFromInt(1)},
			VenueOrderID: "v1",
			FilledQty:    decimal.NewFromInt(2),
		},
	}).Validate()
	if err == nil {
		t.Fatal("overfilled order report should fail without explicit allowance")
	}
}

func TestReportQueryMatchersRespectAccountID(t *testing.T) {
	inst := InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	order := Order{
		Request:      OrderRequest{AccountID: "acct-a", ClientID: "c1", InstrumentID: inst},
		VenueOrderID: "v1",
	}
	if !OrderMatchesStatusQuery(order, OrderStatusReportQuery{AccountID: "acct-a", ClientID: "c1", VenueOrderID: "v1"}) {
		t.Fatal("matching account order report query should match")
	}
	if OrderMatchesStatusQuery(order, OrderStatusReportQuery{AccountID: "acct-b", ClientID: "c1", VenueOrderID: "v1"}) {
		t.Fatal("mismatched account order report query should not match")
	}

	fill := Fill{AccountID: "acct-a", InstrumentID: inst, ClientID: "c1", VenueOrderID: "v1", TradeID: "t1"}
	if !FillMatchesReportQuery(fill, FillReportQuery{AccountID: "acct-a", ClientID: "c1", VenueOrderID: "v1"}) {
		t.Fatal("matching account fill report query should match")
	}
	if FillMatchesReportQuery(fill, FillReportQuery{AccountID: "acct-b", ClientID: "c1", VenueOrderID: "v1"}) {
		t.Fatal("mismatched account fill report query should not match")
	}
}
