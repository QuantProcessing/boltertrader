// Package httpx holds the response envelope every BolterTrader HTTP surface
// shares. The frozen product contract fixes the shape of that envelope — a
// `meta` block on success, an `error` block on failure — so once the auth edge
// and the trading service became separate packages the encoder had to live
// somewhere neither one owns. Keeping one copy is what stops the two surfaces
// from drifting into two dialects of the same contract.
package httpx

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// ContractVersion is the frozen product-platform contract every listener
// stamps onto its responses.
const ContractVersion = "boltertrader-product-platform-upgrade/v7"

// Meta builds the success envelope's metadata block.
func Meta(r *http.Request) map[string]any {
	return map[string]any{"requestId": RequestID(r), "contractVersion": ContractVersion, "apiVersion": "v1"}
}

// RequestID returns the caller's correlation id, minting one when the caller
// did not supply it so every response can be traced back to a single request.
func RequestID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Request-Id")); id != "" {
		return id
	}
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return "req_" + hex.EncodeToString(buf)
}

func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// WriteError emits the contract's error envelope. details is never omitted:
// clients parse it unconditionally, so a nil map becomes an empty object
// rather than a null the caller has to guard.
func WriteError(w http.ResponseWriter, status int, code, message string, details map[string]any, retryable bool, requestID string) {
	if details == nil {
		details = map[string]any{}
	}
	WriteJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": details, "retryable": retryable, "requestId": requestID}})
}

// NormalizedBody lets an absent body validate as the empty object, so a
// contract rule about optional fields does not have to special-case "no body".
func NormalizedBody(body []byte) []byte {
	if len(bytes.TrimSpace(body)) == 0 {
		return []byte("{}")
	}
	return body
}

// ConfigErr reports a wiring mistake found while constructing a handler. It is
// distinct from a request error: it is returned before the server ever serves.
func ConfigErr(message string) error { return &configError{message: message} }

type configError struct{ message string }

func (e *configError) Error() string { return e.message }
