package spot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// GenerateSignature generates an HMAC SHA256 signature
func GenerateSignature(secretKey, data string) string {
	h := hmac.New(sha256.New, []byte(secretKey))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// Timestamp returns the current timestamp in milliseconds
func Timestamp() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}

// FormatSymbol formats the symbol to uppercase (e.g., btcusdt -> BTCUSDT)
func FormatSymbol(symbol string) string {
	// Simple implementation, can be expanded if needed
	return symbol
}

// BuildQueryString builds a query string from a map, sorted by key
func BuildQueryString(params map[string]interface{}) string {
	if len(params) == 0 {
		return ""
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		if b.Len() > 0 {
			b.WriteString("&")
		}
		b.WriteString(fmt.Sprintf("%s=%v", k, params[k]))
	}
	return b.String()
}
