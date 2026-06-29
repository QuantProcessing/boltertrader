package sdk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func sign(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func buildTimestamp() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 10)
}

func buildPayload(timestamp, method, requestPath, queryString, body string) string {
	var builder strings.Builder
	builder.WriteString(timestamp)
	builder.WriteString(strings.ToUpper(method))
	builder.WriteString(requestPath)
	if queryString != "" {
		builder.WriteByte('?')
		builder.WriteString(queryString)
	}
	builder.WriteString(body)
	return builder.String()
}

func (c *Client) signHeaders(req *http.Request, queryString, body string) {
	timestamp := buildTimestamp()
	payload := buildPayload(timestamp, req.Method, req.URL.Path, queryString, body)
	req.Header.Set("ACCESS-KEY", c.apiKey)
	req.Header.Set("ACCESS-SIGN", sign(c.secretKey, payload))
	req.Header.Set("ACCESS-TIMESTAMP", timestamp)
	req.Header.Set("ACCESS-PASSPHRASE", c.passphrase)
	req.Header.Set("locale", "en-US")
	req.Header.Set("Content-Type", "application/json")
}
