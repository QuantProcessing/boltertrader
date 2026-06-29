package spot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// BuildQueryString builds a query string from a map
func BuildQueryString(params map[string]interface{}) string {
	if len(params) == 0 {
		return ""
	}
	query := ""
	for k, v := range params {
		if query != "" {
			query += "&"
		}
		query += fmt.Sprintf("%s=%v", k, v)
	}
	return query
}
