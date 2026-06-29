package okx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"time"
)

// Signer handles OKX API signature generation.
type Signer struct {
	SecretKey string
}

func NewSigner(secretKey string) *Signer {
	return &Signer{SecretKey: secretKey}
}

// SignRequest adds necessary authentication headers to the request.
// OKX requires:
// OK-ACCESS-KEY
// OK-ACCESS-SIGN
// OK-ACCESS-TIMESTAMP
// OK-ACCESS-PASSPHRASE
// OK-ACCESS-SIMULATED-TRADING (if demo)
func (s *Signer) SignRequest(req *http.Request, method, path, body string, apiKey, passphrase string) {
	// 1. Timestamp (ISO8601) e.g. 2020-12-08T09:08:57.715Z
	// For simplicity, we can use UTC format.
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.999Z07:00") // OKX accepts ISO 8601

	// 2. PreHash String: timestamp + method + requestPath + body
	preHash := ts + method + path + body

	// 3. HMAC SHA256
	h := hmac.New(sha256.New, []byte(s.SecretKey))
	h.Write([]byte(preHash))
	signature := base64.StdEncoding.EncodeToString(h.Sum(nil))

	// 4. Set Headers
	req.Header.Set("OK-ACCESS-KEY", apiKey)
	req.Header.Set("OK-ACCESS-SIGN", signature)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", passphrase)
}
