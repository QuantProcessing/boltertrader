package perp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
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

// BuildQueryString builds a query string from a map
func BuildQueryString(params map[string]interface{}) string {
	q := url.Values{}
	for k, v := range params {
		q.Add(k, fmt.Sprintf("%v", v))
	}
	return q.Encode()
}
