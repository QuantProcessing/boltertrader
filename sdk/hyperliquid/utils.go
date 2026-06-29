package hyperliquid

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func FloatToString(x float64) (string, error) {
	rounded := fmt.Sprintf("%.8f", x)

	parsed, err := strconv.ParseFloat(rounded, 64)
	if err != nil {
		return "", err
	}

	if math.Abs(parsed-x) >= 1e-12 {
		return "", fmt.Errorf("float to wire error: %f -> %f", x, parsed)
	}

	if rounded == "0.00000000" {
		rounded = "0.00000000"
	}

	result := strings.TrimRight(rounded, "0")
	result = strings.TrimRight(result, ".")

	return result, nil
}
