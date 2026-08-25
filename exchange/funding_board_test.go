package exchange

import "testing"

func TestFundingBoardEntryPreservesMissingValues(t *testing.T) {
	entry := FundingBoardEntry{Symbol: "BTC-USDT"}
	if entry.MarkPrice.Valid || entry.IndexPrice.Valid || entry.OpenInterest.Valid || entry.RateValid {
		t.Fatal("absent funding-board values must remain absent")
	}
}
