package option

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// GenerateSignature returns the HMAC-SHA256 signature Binance expects for
// signed REST/WS calls. Same algorithm as fapi/spapi.
func GenerateSignature(secretKey, data string) string {
	h := hmac.New(sha256.New, []byte(secretKey))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// Timestamp returns the current epoch in milliseconds, matching Binance's
// wire convention.
func Timestamp() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}
