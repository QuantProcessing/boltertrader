package reconcile

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
)

func TestReconcilerBoundsCompletedFillHistoryButRetainsPendingDurability(t *testing.T) {
	r := New(nil, nil, cache.New()).WithFillRetentionLimit(2)
	r.pending["pending"] = pendingAppliedFill{
		meta:      contract.EventMeta{EventID: "pending-event"},
		fill:      model.Fill{TradeID: "pending"},
		appliedAt: time.Unix(1, 0),
	}

	r.rememberAppliedFill("oldest", "record-oldest")
	r.rememberAppliedFill("recent-1", "record-recent-1")
	r.rememberAppliedFill("recent-2", "record-recent-2")

	if got := len(r.fills); got != 2 {
		t.Fatalf("completed fill history=%d, want limit 2", got)
	}
	if _, ok := r.fills["oldest"]; ok {
		t.Fatal("oldest completed fill should be evicted")
	}
	if _, ok := r.fills["recent-2"]; !ok {
		t.Fatal("most recent completed fill should be retained")
	}
	if _, ok := r.pending["pending"]; !ok {
		t.Fatal("pending applied fill must survive completed-history eviction")
	}
}
