package nado

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func TestNadoSDKFixturesBackAdapterDiscoveryAndAccountMapping(t *testing.T) {
	products := readNadoFixture[sdk.AllProductsResponse](t, "all_products.json")
	symbols := readNadoFixture[sdk.SymbolsInfo](t, "symbols.json")
	provider, err := newInstrumentProviderFromDiscovery(products, symbols, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
	if err != nil {
		t.Fatalf("fixture discovery: %v", err)
	}

	spotID := model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT0", Kind: enums.KindSpot}
	spot, ok := provider.Instrument(spotID)
	if !ok {
		t.Fatalf("missing fixture spot instrument")
	}
	if spot.VenueSymbol != "ETH_USDT0" || spot.AssetIndex != nil {
		t.Fatalf("spot identity mismatch: %+v", spot)
	}
	if productID, ok := provider.ProductID(spotID); !ok || productID != 1 {
		t.Fatalf("spot product id=%d ok=%v", productID, ok)
	}
	if !spot.PriceTick.Equal(decimal.RequireFromString("0.1")) ||
		!spot.SizeStep.Equal(decimal.RequireFromString("0.001")) ||
		!spot.MinQty.Equal(decimal.RequireFromString("0.001")) ||
		!spot.MinNotional.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("spot increments mismatch: %+v", spot)
	}

	perpID := model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT0", Kind: enums.KindPerp}
	perp, ok := provider.Instrument(perpID)
	if !ok {
		t.Fatalf("missing fixture perp instrument")
	}
	if perp.VenueSymbol != "ETH-PERP_USDT0" || perp.AssetIndex != nil || perp.Settle != "USDT0" {
		t.Fatalf("perp identity mismatch: %+v", perp)
	}

	account := readNadoFixture[sdk.AccountInfo](t, "subaccount_info.json")
	snapshot := &sdk.AccountSnapshot{Account: account, ReceivedAt: time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)}
	state, err := accountStateFromNado(snapshot, provider, "NADO-EXACT", snapshot.ReceivedAt)
	if err != nil {
		t.Fatalf("account state conversion: %v", err)
	}
	if state.AccountID != "NADO-EXACT" || state.Type != model.AccountMargin || state.BaseCurrency != "USDT0" {
		t.Fatalf("state identity/type mismatch: %+v", state)
	}
	if len(state.Margins) != 0 {
		t.Fatalf("health must not be relabeled as margin requirements: %+v", state.Margins)
	}
	if state.Summary == nil || !state.Summary.AvailableCollateral.Equal(decimal.RequireFromString("800")) || !state.Summary.Equity.Equal(decimal.RequireFromString("900")) {
		t.Fatalf("summary health mapping mismatch: %+v", state.Summary)
	}
	if len(state.Balances) != 2 {
		t.Fatalf("balances len=%d: %+v", len(state.Balances), state.Balances)
	}
	if !state.Balances[0].Free.IsZero() {
		t.Fatalf("Nado adapter must not invent currency free balance: %+v", state.Balances[0])
	}
	if !state.Balances[1].Total.Equal(decimal.RequireFromString("-2")) || !state.Balances[1].Borrowed.Equal(decimal.RequireFromString("2")) {
		t.Fatalf("negative signed spot balance must preserve total and borrowed magnitude: %+v", state.Balances[1])
	}

	positions, err := positionsFromNado(account, provider, "NADO-EXACT", snapshot.ReceivedAt)
	if err != nil {
		t.Fatalf("position conversion: %v", err)
	}
	if len(positions) != 1 || positions[0].InstrumentID != perpID || !positions[0].Quantity.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("perp position mapping mismatch: %+v", positions)
	}
}

func readNadoFixture[T any](t *testing.T, name string) T {
	t.Helper()
	path := filepath.Join("..", "..", "sdk", "nado", "testdata", name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope sdk.ApiV1Response
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(envelope.Data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
