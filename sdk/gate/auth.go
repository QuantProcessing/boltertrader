package sdk

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func sign(secret, payload string) string {
	mac := hmac.New(sha512.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func hashPayload(body string) string {
	sum := sha512.Sum512([]byte(body))
	return hex.EncodeToString(sum[:])
}

func buildTimestamp(now time.Time) string {
	return strconv.FormatInt(now.Unix(), 10)
}

func buildSigningPayload(method, requestPath, rawQuery, body, timestamp string) string {
	return fmt.Sprintf("%s\n%s\n%s\n%s\n%s",
		strings.ToUpper(method),
		requestPath,
		rawQuery,
		hashPayload(body),
		timestamp,
	)
}

func (c *Client) signHeaders(req *http.Request, rawQuery, body string) {
	timestamp := buildTimestamp(c.now())
	payload := buildSigningPayload(req.Method, req.URL.Path, rawQuery, body, timestamp)
	req.Header.Set("KEY", c.apiKey)
	req.Header.Set("Timestamp", timestamp)
	req.Header.Set("SIGN", sign(c.secretKey, payload))
	req.Header.Set("Content-Type", "application/json")
}

func buildWSAuthPayload(channel, event string, timestamp int64) string {
	return fmt.Sprintf("channel=%s&event=%s&time=%d", channel, event, timestamp)
}
