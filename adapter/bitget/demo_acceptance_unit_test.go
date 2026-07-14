package bitget

import (
	"reflect"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

func TestBitgetAcceptanceCategoriesAreProductScoped(t *testing.T) {
	tests := []struct {
		name   string
		kind   enums.InstrumentKind
		settle string
		want   []string
	}{
		{"spot", enums.KindSpot, "", []string{"SPOT"}},
		{"USDT perp", enums.KindPerp, "USDT", []string{bitgetsdk.ProductTypeUSDTFutures}},
		{"USDC perp", enums.KindPerp, "USDC", []string{bitgetsdk.ProductTypeUSDCFutures}},
		{"unsupported perp", enums.KindPerp, "BTC", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bitgetAcceptanceCategories(tt.kind, tt.settle); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("categories=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestBitgetAcceptanceLifecycleQuantityCoversRestingMinNotional(t *testing.T) {
	provider := newInstrumentProvider()
	inst := &model.Instrument{
		ID:          model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot},
		Base:        "BTC",
		Quote:       "USDT",
		Settle:      "USDT",
		VenueSymbol: "BTCUSDT",
		PriceTick:   decimal.RequireFromString("0.01"),
		SizeStep:    decimal.RequireFromString("0.000001"),
		MinQty:      decimal.RequireFromString("0.000001"),
		MinNotional: decimal.RequireFromString("1"),
	}
	provider.LoadSnapshot([]*model.Instrument{inst})
	adapter := &Adapter{provider: provider}
	book := &model.OrderBook{
		Bids: []model.BookLevel{{Price: decimal.RequireFromString("100")}},
		Asks: []model.BookLevel{{Price: decimal.RequireFromString("101")}},
	}

	spec := bitgetAcceptanceLifecycleSpec(t, adapter, "Bitget Demo Spot", inst.ID, book, decimal.RequireFromString("20"))
	if spec.Quantity.Mul(spec.RestingPrice).LessThan(inst.MinNotional) {
		t.Fatalf("resting notional %s below min %s with spec %+v", spec.Quantity.Mul(spec.RestingPrice), inst.MinNotional, spec)
	}
	if !spec.CloseQuantity.IsPositive() || !spec.CloseQuantity.LessThan(spec.Quantity) {
		t.Fatalf("close quantity=%s quantity=%s, want fee-buffered Spot close", spec.CloseQuantity, spec.Quantity)
	}
	if spec.CloseQuantity.LessThan(inst.MinQty) || spec.CloseQuantity.Mul(spec.ClosePrice).LessThan(inst.MinNotional) {
		t.Fatalf("buffered close is not tradable: close_qty=%s close_price=%s min_qty=%s min_notional=%s", spec.CloseQuantity, spec.ClosePrice, inst.MinQty, inst.MinNotional)
	}
}

func TestBitgetPerpAcceptanceDoesNotAutoFlattenPreExistingPositions(t *testing.T) {
	provider := newInstrumentProvider()
	inst := &model.Instrument{
		ID:          model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Base:        "BTC",
		Quote:       "USDT",
		Settle:      "USDT",
		VenueSymbol: "BTCUSDT",
		PriceTick:   decimal.RequireFromString("0.01"),
		SizeStep:    decimal.RequireFromString("0.001"),
		MinQty:      decimal.RequireFromString("0.001"),
		MinNotional: decimal.RequireFromString("1"),
	}
	provider.LoadSnapshot([]*model.Instrument{inst})
	adapter := &Adapter{provider: provider}
	book := &model.OrderBook{
		Bids: []model.BookLevel{{Price: decimal.RequireFromString("100")}},
		Asks: []model.BookLevel{{Price: decimal.RequireFromString("101")}},
	}

	spec := bitgetAcceptanceLifecycleSpec(t, adapter, "Bitget Demo USDT Perp", inst.ID, book, decimal.RequireFromString("20"))
	if spec.CleanExistingPosition {
		t.Fatal("Bitget Perp acceptance must reject, not auto-flatten, pre-existing positions")
	}
}

func TestBitgetAcceptancePositionSideMatchesAccountHoldMode(t *testing.T) {
	tests := []struct {
		name     string
		kind     enums.InstrumentKind
		holdMode string
		want     enums.PositionSide
		wantErr  bool
	}{
		{name: "spot", kind: enums.KindSpot, want: enums.PosNet},
		{name: "one way", kind: enums.KindPerp, holdMode: "one_way_mode", want: enums.PosNet},
		{name: "legacy one way", kind: enums.KindPerp, holdMode: "single_hold", want: enums.PosNet},
		{name: "hedge", kind: enums.KindPerp, holdMode: "hedge_mode", want: enums.PosLong},
		{name: "legacy hedge", kind: enums.KindPerp, holdMode: "double_hold", want: enums.PosLong},
		{name: "unknown", kind: enums.KindPerp, holdMode: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bitgetAcceptancePositionSideForHoldMode(tt.kind, tt.holdMode)
			if (err != nil) != tt.wantErr {
				t.Fatalf("position side=%s err=%v, wantErr=%v", got, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("position side=%s, want %s", got, tt.want)
			}
		})
	}
}
