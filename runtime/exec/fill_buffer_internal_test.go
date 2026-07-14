package exec

import (
	"fmt"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestFillBufferActiveCoveragePersistsWhileTradeIDsStayBounded(t *testing.T) {
	const limit = 3
	buffer := NewFillBufferWithAppliedLimit(limit)
	instrument := model.InstrumentID{Venue: "TEST", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	for i := 0; i < 100; i++ {
		fill := model.Fill{
			AccountID:    "acct",
			InstrumentID: instrument,
			ClientID:     "client-1",
			VenueOrderID: "venue-1",
			TradeID:      fmt.Sprintf("trade-%d", i),
			Quantity:     decimal.NewFromInt(1),
		}
		if applied, _ := buffer.MarkAppliedWithCoverage(fill); !applied {
			t.Fatalf("fill %d should apply", i)
		}
	}

	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if got := len(buffer.seen); got != limit {
		t.Fatalf("retained fills=%d, want %d", got, limit)
	}
	// Active-order aliases persist while trade-ID retention remains bounded.
	if got, want := len(buffer.orderAliasGroups), 2; got != want {
		t.Fatalf("order aliases=%d, want %d", got, want)
	}
	groups := make(map[*appliedOrderGroup]struct{})
	for _, group := range buffer.orderAliasGroups {
		groups[appliedOrderGroupRoot(group)] = struct{}{}
	}
	if len(groups) != 1 {
		t.Fatalf("logical order groups=%d, want 1", len(groups))
	}
	for group := range groups {
		if group.retained != limit || len(group.aliases) != 2 || !group.quantity.Equal(decimal.NewFromInt(100)) {
			t.Fatalf("group retained=%d aliases=%d quantity=%s, want %d/2/100", group.retained, len(group.aliases), group.quantity, limit)
		}
	}
}
