package portfolio

import (
	"context"
	"testing"
	"time"
)

func TestProjectionMaintainsProductAttributedPositionAndRealizedPnL(t *testing.T) {
	projection := NewProjection()
	base := Fill{
		FillID: "fill-1", CommandID: "command-1", LineageID: "lineage-a", ProductID: "manual",
		Mode: ModeTest, AccountID: "account-a", InstrumentID: "BTC-USDT", Side: SideBuy,
		Quantity: "2", Price: "100", Fee: "1", FeeCurrency: "USDT", OccurredAt: time.Now(),
	}
	position, result, err := projection.ApplyFill(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if position.Quantity != "2" || position.AverageEntryPrice != "100" || result.RealizedPnL != "0" {
		t.Fatalf("open position=%+v result=%+v", position, result)
	}
	closeFill := base
	closeFill.FillID = "fill-2"
	closeFill.Side = SideSell
	closeFill.Quantity = "1"
	closeFill.Price = "110"
	closeFill.Fee = "0.5"
	position, result, err = projection.ApplyFill(context.Background(), closeFill)
	if err != nil {
		t.Fatal(err)
	}
	if position.Quantity != "1" || position.RealizedPnL != "10" || result.RealizedPnL != "10" {
		t.Fatalf("closed position=%+v result=%+v", position, result)
	}
	marked, err := projection.ApplyMark(ModeTest, "account-a", "manual", "lineage-a", "BTC-USDT", "105", time.Now())
	if err != nil || marked.UnrealizedPnL != "5" {
		t.Fatalf("marked=%+v err=%v", marked, err)
	}
	if _, _, err := projection.ApplyFill(context.Background(), Fill{FillID: "unattributed"}); err == nil {
		t.Fatal("unattributed fill accepted")
	}
}

func TestProjectionSeparatesIdenticalOwnershipAcrossModes(t *testing.T) {
	projection := NewProjection()
	fill := Fill{
		FillID: "test-fill", CommandID: "test-command", LineageID: "lineage-a", ProductID: "manual", Mode: ModeTest,
		AccountID: "account-a", InstrumentID: "BTC-USDT", Side: SideBuy, Quantity: "1", Price: "100", Fee: "0", FeeCurrency: "USDT", OccurredAt: time.Now(),
	}
	if _, _, err := projection.ApplyFill(context.Background(), fill); err != nil {
		t.Fatal(err)
	}
	fill.FillID, fill.CommandID, fill.Mode, fill.Price = "live-fill", "live-command", ModeLive, "200"
	if _, _, err := projection.ApplyFill(context.Background(), fill); err != nil {
		t.Fatal(err)
	}
	testPositions := projection.List(ModeTest, "account-a", "manual")
	livePositions := projection.List(ModeLive, "account-a", "manual")
	if len(testPositions) != 1 || len(livePositions) != 1 || testPositions[0].AverageEntryPrice != "100" || livePositions[0].AverageEntryPrice != "200" {
		t.Fatalf("test=%+v live=%+v", testPositions, livePositions)
	}
}
