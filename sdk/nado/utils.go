package nado

import (
	"fmt"
	"math/big"
	"strings"
)

var x18Scale = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

func ParseX18(value string) (*big.Int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("nado x18: empty decimal")
	}
	rational, ok := new(big.Rat).SetString(value)
	if !ok {
		return nil, fmt.Errorf("nado x18: invalid decimal %q", value)
	}
	scaled := new(big.Rat).Mul(rational, new(big.Rat).SetInt(x18Scale))
	if !scaled.IsInt() {
		return nil, fmt.Errorf("nado x18: %q has more than 18 decimal places", value)
	}
	return new(big.Int).Set(scaled.Num()), nil
}

// ToX18 converts a number to an 18-decimal fixed-point big.Int.
// It supports int, int64, float32, and float64.
func ToX18(v interface{}) *big.Int {
	scale := new(big.Int).Set(x18Scale)

	switch val := v.(type) {
	case int:
		res := big.NewInt(int64(val))
		return res.Mul(res, scale)
	case int64:
		res := big.NewInt(val)
		return res.Mul(res, scale)
	case float64:
		f := new(big.Float).SetPrec(256).SetFloat64(val)
		s := new(big.Float).SetPrec(256).SetInt(scale)
		f.Mul(f, s)
		res := new(big.Int)
		// Standard truncation
		f.Int(res)
		return res
	case float32:
		f := new(big.Float).SetPrec(256).SetFloat64(float64(val))
		s := new(big.Float).SetPrec(256).SetInt(scale)
		f.Mul(f, s)
		res := new(big.Int)
		f.Int(res)
		return res
	case string:
		res, err := ParseX18(val)
		if err != nil {
			return big.NewInt(0)
		}
		return res
	default:
		return big.NewInt(0)
	}
}

// MulX18 multiplies two 18-decimal fixed-point numbers.
// result = (x * y) / 1e18
func MulX18(x, y *big.Int) *big.Int {
	product := new(big.Int).Mul(x, y)
	scale := new(big.Int).Set(x18Scale)
	return product.Div(product, scale)
}

// RoundX18 rounds a value to the nearest increment.
// result = (val / increment) * increment
// Note: This implements floor/truncation rounding similar to finding the nearest tick grid.
// If typical rounding is needed, (val + increment/2) / increment * increment.
// Given SDK context "round_x18", usually means "align to grid".
// Let's implement standard grid alignment (floor/truncation for safety on price increments usually safer for bids, but varies).
// Python "round_x18" usually rounds to nearest.
// Python: `round(value / interval) * interval`
func RoundX18(val, increment *big.Int) *big.Int {
	if increment.Sign() == 0 {
		return new(big.Int).Set(val)
	}

	// Standard round to nearest: floor((val + increment/2) / increment) * increment
	halfInc := new(big.Int).Div(increment, big.NewInt(2))
	numerator := new(big.Int).Add(val, halfInc)
	quotient := new(big.Int).Div(numerator, increment)
	return quotient.Mul(quotient, increment)
}

// string to bigint
func ToBigInt(val string) *big.Int {
	p := new(big.Int)
	p.SetString(val, 10)
	return p
}
