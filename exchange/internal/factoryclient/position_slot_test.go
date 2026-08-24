package factoryclient

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestVenuePositionSlotsPreserveOneWayAndHedgeIdentity(t *testing.T) {
	t.Run("binance", func(t *testing.T) {
		cases := map[string]exchange.PositionSlot{
			"BOTH":  exchange.PositionSlotNet,
			"LONG":  exchange.PositionSlotLong,
			"SHORT": exchange.PositionSlotShort,
		}
		for native, want := range cases {
			got, err := binancePerpPositionSlot(native)
			if err != nil || got != want {
				t.Fatalf("binancePerpPositionSlot(%q) = %q, %v; want %q", native, got, err, want)
			}
		}
	})

	t.Run("okx", func(t *testing.T) {
		cases := map[string]exchange.PositionSlot{
			"net":   exchange.PositionSlotNet,
			"long":  exchange.PositionSlotLong,
			"short": exchange.PositionSlotShort,
		}
		for native, want := range cases {
			got, err := okxPositionSlot(native)
			if err != nil || got != want {
				t.Fatalf("okxPositionSlot(%q) = %q, %v; want %q", native, got, err, want)
			}
		}
	})

	t.Run("bybit", func(t *testing.T) {
		cases := map[int]exchange.PositionSlot{
			0: exchange.PositionSlotNet,
			1: exchange.PositionSlotLong,
			2: exchange.PositionSlotShort,
		}
		for native, want := range cases {
			got, err := bybitPositionSlot(native)
			if err != nil || got != want {
				t.Fatalf("bybitPositionSlot(%d) = %q, %v; want %q", native, got, err, want)
			}
		}
	})

	t.Run("bitget", func(t *testing.T) {
		cases := []struct {
			name       string
			mode       string
			nativeSide string
			side       exchange.Side
			want       exchange.PositionSlot
		}{
			{name: "one way buy", mode: "single_hold", side: exchange.SideBuy, want: exchange.PositionSlotNet},
			{name: "one way sell", mode: "single_hold", side: exchange.SideSell, want: exchange.PositionSlotNet},
			{name: "hedge long", mode: "double_hold", nativeSide: "long", side: exchange.SideBuy, want: exchange.PositionSlotLong},
			{name: "hedge short", mode: "double_hold", nativeSide: "short", side: exchange.SideSell, want: exchange.PositionSlotShort},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := bitgetPositionSlot(tc.mode, tc.nativeSide, tc.side); got != tc.want {
					t.Fatalf("bitgetPositionSlot(%q, %q, %q) = %q; want %q", tc.mode, tc.nativeSide, tc.side, got, tc.want)
				}
			})
		}
	})
}
