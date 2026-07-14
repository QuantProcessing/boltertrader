package cache

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
)

func TestTerminalOrderRetentionPreservesActiveAndUnknownOrders(t *testing.T) {
	c := NewWithTerminalOrderLimit(2)
	upsert := func(clientID string, status enums.OrderStatus) {
		c.UpsertOrder(model.Order{Request: model.OrderRequest{ClientID: clientID}, Status: status})
	}
	upsert("terminal-oldest", enums.StatusFilled)
	upsert("terminal-recent", enums.StatusCanceled)
	upsert("unknown", enums.StatusUnknown)
	upsert("open", enums.StatusNew)
	upsert("terminal-newest", enums.StatusRejected)

	if got := len(c.Orders()); got != 4 {
		t.Fatalf("orders=%d, want 2 retained terminal + UNKNOWN + open", got)
	}
	if _, ok := c.Order("terminal-oldest"); ok {
		t.Fatal("oldest evictable terminal order should be removed")
	}
	for _, id := range []string{"terminal-recent", "terminal-newest", "unknown", "open"} {
		if _, ok := c.Order(id); !ok {
			t.Fatalf("order %q should be retained", id)
		}
	}
}

func TestTerminalOrderRetentionNeverCountsUnknownAsEvictable(t *testing.T) {
	c := NewWithTerminalOrderLimit(1)
	for _, id := range []string{"unknown-1", "unknown-2", "unknown-3"} {
		c.UpsertOrder(model.Order{Request: model.OrderRequest{ClientID: id}, Status: enums.StatusUnknown})
	}
	if got := len(c.Orders()); got != 3 {
		t.Fatalf("UNKNOWN orders=%d, want all 3 retained for reconciliation", got)
	}
}

func TestTerminalOrderRetentionMetadataIsAlsoBounded(t *testing.T) {
	c := NewWithTerminalOrderLimit(2)
	for i := 0; i < 20; i++ {
		c.UpsertOrder(model.Order{
			Request:   model.OrderRequest{ClientID: "same-terminal"},
			Status:    enums.StatusFilled,
			UpdatedAt: time.Unix(int64(i+1), 0),
		})
	}
	if got := len(c.terminalOrder); got > 4 {
		t.Fatalf("terminal retention metadata=%d, want bounded near configured window", got)
	}
}
