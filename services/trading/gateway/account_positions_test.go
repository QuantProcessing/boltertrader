package gateway

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/accountpositions"
)

func TestProjectGatewayPositionEventPreservesIdentityAndRemove(t *testing.T) {
	occurredAt := time.Date(2026, 8, 24, 8, 31, 22, 0, time.UTC)
	payload, err := json.Marshal(map[string]any{
		"account_id":    "account-a",
		"route_id":      "perp-usdt",
		"instrument_id": "binance:live:perp:USDT:BTC-USDT",
		"position_slot": "short",
		"operation":     "remove",
		"position": map[string]any{
			"position_slot":  "short",
			"side":           "sell",
			"quantity":       "-0.001",
			"entry_price":    "77000",
			"mark_price":     "76900",
			"unrealized_pnl": "0.1",
		},
		"reason": "position_closed",
	})
	if err != nil {
		t.Fatal(err)
	}

	projected, err := projectGatewayPositionEvent(gatewayPrivateEvent{
		BootID: "boot-a", Sequence: 42, AccountID: "account-a", ResourceVersion: 9,
		Kind: "position", OccurredAt: occurredAt, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if projected.Kind != accountpositions.SourceEventPosition || projected.Operation != accountpositions.OperationRemove ||
		projected.Cursor != "boot-a:42" || projected.ResourceVersion != 9 || projected.PositionSlot != "short" ||
		projected.Position.PositionSlot != "short" || projected.Position.Quantity != "-0.001" || projected.Reason != "position_closed" ||
		!projected.OccurredAt.Equal(occurredAt) {
		t.Fatalf("projected event = %+v", projected)
	}
}

func TestProjectGatewayPositionEventRequiresResnapshotOnGap(t *testing.T) {
	projected, err := projectGatewayPositionEvent(gatewayPrivateEvent{
		BootID: "boot-a", Sequence: 43, AccountID: "account-a", Kind: "resync_required",
		Payload: json.RawMessage(`{"reason":"cursor_too_old"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if projected.Kind != accountpositions.SourceEventResync || projected.Reason != "cursor_too_old" || projected.Cursor != "boot-a:43" {
		t.Fatalf("projected event = %+v", projected)
	}
}
