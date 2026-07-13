package common

import (
	"errors"
	"fmt"
	"strings"
)

type UnsafeSymbolError struct {
	environment Environment
	product     Product
	symbol      string
}

func (e *UnsafeSymbolError) Error() string {
	return fmt.Sprintf("aster profile: symbol %q is not allowed for %s/%s", e.symbol, e.environment, e.product)
}

func (e *UnsafeSymbolError) Symbol() string { return e.symbol }

func NormalizeSymbol(profile Profile, symbol string) (string, error) {
	if err := profile.Validate(); err != nil {
		return "", err
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return "", fmt.Errorf("aster profile: symbol is required")
	}
	if profile.Environment() == EnvironmentTestnet && strings.HasPrefix(normalized, "TEST") {
		return "", &UnsafeSymbolError{
			environment: profile.Environment(),
			product:     profile.Product(),
			symbol:      normalized,
		}
	}
	return normalized, nil
}

type NoSafeSymbolError struct {
	environment Environment
	product     Product
}

func (e *NoSafeSymbolError) Error() string {
	return fmt.Sprintf("aster profile: no safe symbol candidate remains for %s/%s", e.environment, e.product)
}

func FilterDiscoverySymbols(profile Profile, candidates []string) ([]string, error) {
	filtered := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		normalized, err := NormalizeSymbol(profile, candidate)
		if err != nil {
			var unsafe *UnsafeSymbolError
			if errors.As(err, &unsafe) {
				continue
			}
			return nil, err
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		filtered = append(filtered, normalized)
	}
	if len(filtered) == 0 {
		return nil, &NoSafeSymbolError{
			environment: profile.Environment(),
			product:     profile.Product(),
		}
	}
	return filtered, nil
}
