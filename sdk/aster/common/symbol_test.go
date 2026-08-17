package common

import "testing"

func TestNormalizeSymbolTrimsAndUppercases(t *testing.T) {
	profile, err := NewProfile(ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NormalizeSymbol(profile, "  asterusdt  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ASTERUSDT" {
		t.Fatalf("normalized symbol = %q", got)
	}
}

// A symbol starting with "TEST" used to be rejected under the testnet profile.
// Nothing rejects it now, and this pins that: the venue lists real instruments
// whose names begin with those four letters, so a resurrected prefix rule would
// silently drop tradable symbols.
func TestNormalizeSymbolAcceptsTestPrefix(t *testing.T) {
	profile, err := NewProfile(ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NormalizeSymbol(profile, " testasset ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "TESTASSET" {
		t.Fatalf("normalized symbol = %q", got)
	}
}

func TestNormalizeSymbolRequiresNonEmpty(t *testing.T) {
	profile, err := NewProfile(ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeSymbol(profile, "   "); err == nil {
		t.Fatal("empty symbol accepted")
	}
}
