package sdk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"
)

const defaultRecvWindow = "5000"

func buildTimestamp() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 10)
}

func sign(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *Client) signHeaders(req *http.Request, queryString, body string) {
	timestamp := buildTimestamp()
	payload := timestamp + c.apiKey + defaultRecvWindow
	if req.Method == http.MethodGet {
		payload += queryString
	} else {
		payload += body
	}

	req.Header.Set("X-BAPI-API-KEY", c.apiKey)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-RECV-WINDOW", defaultRecvWindow)
	req.Header.Set("X-BAPI-SIGN", sign(c.secretKey, payload))
}
