package common

import (
	"fmt"
	"strings"
)

// NormalizeSymbol upper-cases a caller-supplied symbol after checking that the
// profile it will be sent under is coherent.
//
// It used to also reject symbols beginning with "TEST" when the profile was
// aster's testnet, because that venue listed throwaway instruments under names
// a production symbol could plausibly collide with. Testnet is gone, and with
// it both that rule and FilterDiscoverySymbols, which existed only to apply it
// across a candidate list and had no caller outside its own tests.
func NormalizeSymbol(profile Profile, symbol string) (string, error) {
	if err := profile.Validate(); err != nil {
		return "", err
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return "", fmt.Errorf("aster profile: symbol is required")
	}
	return normalized, nil
}
