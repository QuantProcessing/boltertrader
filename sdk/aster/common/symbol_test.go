package common

import (
	"errors"
	"testing"
)

func TestNormalizeSymbolRejectsTestPrefixForEveryTestnetProduct(t *testing.T) {
	for _, product := range []Product{ProductSpot, ProductPerp} {
		t.Run(string(product), func(t *testing.T) {
			profile, err := NewProfile(EnvironmentTestnet, product)
			if err != nil {
				t.Fatal(err)
			}
			for _, symbol := range []string{"TESTUSDT", " test-usdt ", "TeStAsset"} {
				_, err := NormalizeSymbol(profile, symbol)
				var unsafe *UnsafeSymbolError
				if !errors.As(err, &unsafe) {
					t.Fatalf("NormalizeSymbol(%q) error = %T %v", symbol, err, err)
				}
			}
		})
	}
}

func TestNormalizeSymbolTrimsAndUppercasesAllowedSymbol(t *testing.T) {
	profile, err := NewProfile(EnvironmentTestnet, ProductSpot)
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

func TestNormalizeSymbolAllowsTestPrefixInProduction(t *testing.T) {
	profile, err := NewProfile(EnvironmentProduction, ProductSpot)
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

func TestFilterDiscoverySymbolsDropsTestCandidatesForEveryTestnetProduct(t *testing.T) {
	for _, product := range []Product{ProductSpot, ProductPerp} {
		t.Run(string(product), func(t *testing.T) {
			profile, err := NewProfile(EnvironmentTestnet, product)
			if err != nil {
				t.Fatal(err)
			}
			got, err := FilterDiscoverySymbols(profile, []string{" TESTONE ", "asterusdt", "testTwo", " ethusdt "})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0] != "ASTERUSDT" || got[1] != "ETHUSDT" {
				t.Fatalf("filtered symbols = %v", got)
			}
		})
	}
}

func TestFilterDiscoverySymbolsFailsWhenNoSafeCandidateRemains(t *testing.T) {
	profile, err := NewProfile(EnvironmentTestnet, ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	_, err = FilterDiscoverySymbols(profile, []string{"TESTONE", " testtwo "})
	var noCandidate *NoSafeSymbolError
	if !errors.As(err, &noCandidate) {
		t.Fatalf("error = %T %v", err, err)
	}
}
