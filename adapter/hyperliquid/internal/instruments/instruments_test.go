package instruments

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
)

func TestBuildSpotInstrumentsUsesOfficialAssetIDFormulaAndPreservesRawSymbol(t *testing.T) {
	meta := loadSpotMeta(t, "testdata/spot_meta.json")

	insts, err := BuildSpotInstruments(meta)
	if err != nil {
		t.Fatalf("BuildSpotInstruments: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("len(insts)=%d, want 1", len(insts))
	}
	inst := insts[0]
	assertInstrumentIdentity(t, inst, model.InstrumentID{Venue: VenueName, Symbol: "PURR-USDC", Kind: enums.KindSpot}, "PURR/USDC", 10007)
	if inst.Base != "PURR" || inst.Quote != "USDC" || inst.Settle != "USDC" {
		t.Fatalf("unexpected currencies: base=%q quote=%q settle=%q", inst.Base, inst.Quote, inst.Settle)
	}
	if got := inst.SizeStep.String(); got != "1" {
		t.Fatalf("SizeStep=%s, want 1", got)
	}
}

func TestBuildStandardPerpInstrumentsUsesUniverseIndexAssetID(t *testing.T) {
	meta := loadPerpMeta(t, "testdata/perp_meta.json")

	insts, err := BuildStandardPerpInstruments(meta)
	if err != nil {
		t.Fatalf("BuildStandardPerpInstruments: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("len(insts)=%d, want 2", len(insts))
	}
	inst := insts[0]
	assertInstrumentIdentity(t, inst, model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDC", Kind: enums.KindPerp}, "BTC", 0)
	if inst.Base != "BTC" || inst.Quote != "USDC" || inst.Settle != "USDC" {
		t.Fatalf("unexpected currencies: base=%q quote=%q settle=%q", inst.Base, inst.Quote, inst.Settle)
	}
	if got := inst.SizeStep.String(); got != "0.00001" {
		t.Fatalf("SizeStep=%s, want 0.00001", got)
	}
}

func TestBuildHIP3PerpInstrumentsUsesDexAssetIDFormulaAndCollateral(t *testing.T) {
	spotMeta := loadSpotMeta(t, "testdata/spot_meta.json")
	hip3Meta := loadPerpMeta(t, "testdata/hip3_meta.json")

	insts, err := BuildHIP3PerpInstruments(sdkperp.PerpDex{Index: 2, Name: "testdex"}, hip3Meta, spotMeta)
	if err != nil {
		t.Fatalf("BuildHIP3PerpInstruments: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("len(insts)=%d, want 1", len(insts))
	}
	inst := insts[0]
	assertInstrumentIdentity(t, inst, model.InstrumentID{Venue: VenueName, Symbol: "testdex:COIN-USDC", Kind: enums.KindPerp}, "testdex:COIN", 120000)
	if inst.Base != "testdex:COIN" || inst.Quote != "USDC" || inst.Settle != "USDC" {
		t.Fatalf("unexpected currencies: base=%q quote=%q settle=%q", inst.Base, inst.Quote, inst.Settle)
	}
	if got := inst.SizeStep.String(); got != "0.01" {
		t.Fatalf("SizeStep=%s, want 0.01", got)
	}
}

func TestBuildHIP3PerpInstrumentsDoesNotDuplicateDexPrefixedCoin(t *testing.T) {
	spotMeta := loadSpotMeta(t, "testdata/spot_meta.json")
	hip3Meta := loadPerpMeta(t, "testdata/hip3_meta.json")
	hip3Meta.Universe[0].Name = "testdex:COIN"

	insts, err := BuildHIP3PerpInstruments(sdkperp.PerpDex{Index: 2, Name: "testdex"}, hip3Meta, spotMeta)
	if err != nil {
		t.Fatalf("BuildHIP3PerpInstruments: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("len(insts)=%d, want 1", len(insts))
	}
	inst := insts[0]
	assertInstrumentIdentity(t, inst, model.InstrumentID{Venue: VenueName, Symbol: "testdex:COIN-USDC", Kind: enums.KindPerp}, "testdex:COIN", 120000)
	if inst.Base != "testdex:COIN" || inst.Quote != "USDC" || inst.Settle != "USDC" {
		t.Fatalf("unexpected currencies: base=%q quote=%q settle=%q", inst.Base, inst.Quote, inst.Settle)
	}
}

func TestBuildHIP3PerpInstrumentsSanitizesWildcardVenueNameForNeutralIDOnly(t *testing.T) {
	spotMeta := loadSpotMeta(t, "testdata/spot_meta.json")
	hip3Meta := loadPerpMeta(t, "testdata/hip3_meta.json")
	hip3Meta.Universe[0].Name = "testdex:STREAMABCD****"

	insts, err := BuildHIP3PerpInstruments(sdkperp.PerpDex{Index: 2, Name: "testdex"}, hip3Meta, spotMeta)
	if err != nil {
		t.Fatalf("BuildHIP3PerpInstruments: %v", err)
	}
	inst := insts[0]
	assertInstrumentIdentity(t, inst, model.InstrumentID{Venue: VenueName, Symbol: "testdex:STREAMABCDxxxx-USDC", Kind: enums.KindPerp}, "testdex:STREAMABCD****", 120000)
	if inst.Base != "testdex:STREAMABCD****" {
		t.Fatalf("Base=%q, want raw HIP-3 venue name", inst.Base)
	}
}

func TestBuildHIP3PerpInstrumentsClassifiesMissingCollateralAsBlocked(t *testing.T) {
	spotMeta := loadSpotMeta(t, "testdata/spot_meta.json")
	hip3Meta := loadPerpMeta(t, "testdata/hip3_meta.json")
	hip3Meta.CollateralToken = 999

	_, err := BuildHIP3PerpInstruments(sdkperp.PerpDex{Index: 2, Name: "testdex"}, hip3Meta, spotMeta)
	if !errors.Is(err, ErrHIP3CollateralNotResolved) {
		t.Fatalf("err=%v, want ErrHIP3CollateralNotResolved", err)
	}
}

func TestRegistryResolvesByNeutralIDAndRawVenueSymbol(t *testing.T) {
	spotMeta := loadSpotMeta(t, "testdata/spot_meta.json")
	insts, err := BuildSpotInstruments(spotMeta)
	if err != nil {
		t.Fatalf("BuildSpotInstruments: %v", err)
	}
	reg := NewRegistry(insts...)

	got, ok := reg.Instrument(model.InstrumentID{Venue: VenueName, Symbol: "PURR-USDC", Kind: enums.KindSpot})
	if !ok || got.VenueSymbol != "PURR/USDC" {
		t.Fatalf("neutral lookup got=(%v,%v), want PURR/USDC", got, ok)
	}
	id, ok := reg.ResolveVenueSymbol("PURR/USDC")
	if !ok || id.Symbol != "PURR-USDC" {
		t.Fatalf("venue lookup got=(%v,%v), want PURR-USDC", id, ok)
	}
	if all := reg.All(); len(all) != 1 || all[0] == got {
		t.Fatalf("All should return copied instrument values, got len=%d same_ptr=%v", len(all), len(all) == 1 && all[0] == got)
	}
}

func assertInstrumentIdentity(t *testing.T, inst *model.Instrument, wantID model.InstrumentID, wantVenueSymbol string, wantAssetID int) {
	t.Helper()
	if inst == nil {
		t.Fatal("instrument is nil")
	}
	if inst.ID != wantID {
		t.Fatalf("ID=%v, want %v", inst.ID, wantID)
	}
	if inst.VenueSymbol != wantVenueSymbol {
		t.Fatalf("VenueSymbol=%q, want %q", inst.VenueSymbol, wantVenueSymbol)
	}
	if inst.AssetIndex == nil || *inst.AssetIndex != wantAssetID {
		t.Fatalf("AssetIndex=%v, want %d", inst.AssetIndex, wantAssetID)
	}
}

func loadSpotMeta(t *testing.T, path string) *sdkspot.SpotMeta {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var meta sdkspot.SpotMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return &meta
}

func loadPerpMeta(t *testing.T, path string) *sdkperp.PrepMeta {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var meta sdkperp.PrepMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return &meta
}
