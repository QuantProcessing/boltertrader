package common

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/mbx"
)

type classifiedAsterTransportError struct {
	secret string
	cause  error
}

func (e *classifiedAsterTransportError) Error() string { return e.secret }

func (e *classifiedAsterTransportError) Unwrap() error { return e.cause }

func TestVenueErrorIsTypedAndRedacted(t *testing.T) {
	err := NewVenueError(401, "POST", "/fapi/v3/order", -1022, "Signature is invalid")
	var venueErr *VenueError
	if !errors.As(err, &venueErr) {
		t.Fatalf("error type = %T", err)
	}
	if venueErr.StatusCode() != 401 || venueErr.Code() != -1022 {
		t.Fatalf("status/code = %d/%d", venueErr.StatusCode(), venueErr.Code())
	}
	rendered := err.Error()
	if !strings.Contains(rendered, "POST /fapi/v3/order") || !strings.Contains(rendered, "-1022") {
		t.Fatalf("error lacks stable context: %q", rendered)
	}
	for _, forbidden := range []string{"signature=", "nonce=", "private", "0x"} {
		if strings.Contains(strings.ToLower(rendered), forbidden) {
			t.Fatalf("error contains sensitive detail %q: %q", forbidden, rendered)
		}
	}
}

func TestVenueErrorRedactsEchoedAuthMaterial(t *testing.T) {
	err := NewVenueError(400, "POST", "/fapi/v3/order", -1000, "bad signature=0xabc&nonce=123&symbol=ASTERUSDT")
	rendered := err.Error()
	for _, forbidden := range []string{"0xabc", "nonce=", "ASTERUSDT"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("venue error leaked signed request material %q: %q", forbidden, rendered)
		}
	}
	if !strings.Contains(rendered, "<redacted>") {
		t.Fatalf("venue error lacks redaction marker: %q", rendered)
	}
}

func TestTransportErrorRedactsSignedURL(t *testing.T) {
	cause := errors.New("connection reset")
	err := NewTransportError("POST", "/fapi/v3/order", &url.Error{
		Op:  "Post",
		URL: "https://fapi.asterdex-testnet.com/fapi/v3/order?symbol=ASTERUSDT&nonce=1&signature=0xabc",
		Err: cause,
	})
	if !errors.Is(err, cause) {
		t.Fatal("transport error does not unwrap to network cause")
	}
	rendered := err.Error()
	for _, forbidden := range []string{"ASTERUSDT", "nonce=", "0xabc", "asterdex-testnet.com"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("transport error leaked signed URL material %q: %q", forbidden, rendered)
		}
	}
}

func TestTransportErrorRedactsArbitraryErrorTreesAndPreservesClassification(t *testing.T) {
	const secret = "https://example.invalid/private?signature=SENTINEL_ASTER_SIGNATURE"
	sentinel := errors.New("sentinel transport classification")
	leaf := &classifiedAsterTransportError{secret: secret, cause: sentinel}

	tests := []struct {
		name  string
		cause error
	}{
		{name: "fmt wrapper", cause: fmt.Errorf("wrapped transport: %w", leaf)},
		{name: "errors.Join", cause: errors.Join(errors.New("independent failure"), leaf)},
		{name: "multiple percent-w", cause: fmt.Errorf("two causes: %w / %w", errors.New("independent failure"), leaf)},
		{name: "leaf url.Error", cause: &url.Error{Op: "POST", URL: secret, Err: leaf}},
		{name: "leaf error text", cause: leaf},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewTransportError("POST", "/fapi/v3/order", tt.cause)
			if got.Error() != "aster sdk: POST /fapi/v3/order transport failed" {
				t.Fatalf("transport error = %q, want fixed safe text", got.Error())
			}
			if strings.Contains(got.Error(), secret) {
				t.Fatalf("transport error leaked signed URL: %v", got)
			}
			if !errors.Is(got, sentinel) {
				t.Fatalf("transport error lost errors.Is classification: %v", got)
			}
			var classified *classifiedAsterTransportError
			if !errors.As(got, &classified) || classified != leaf {
				t.Fatalf("transport error lost errors.As classification: %v", got)
			}
			var transportErr *TransportError
			if !errors.As(got, &transportErr) {
				t.Fatalf("public TransportError type was lost: %T", got)
			}
		})
	}
}

func TestTransportErrorRedactsLeafURLErrorAndPreservesType(t *testing.T) {
	const secret = "https://example.invalid/private?signature=SENTINEL_ASTER_LEAF_URL"
	cause := &url.Error{Op: "POST", URL: secret}
	got := NewTransportError("POST", "/fapi/v3/order", cause)
	if strings.Contains(got.Error(), secret) {
		t.Fatalf("transport error leaked leaf URL: %v", got)
	}
	var urlErr *url.Error
	if !errors.As(got, &urlErr) || urlErr != cause {
		t.Fatalf("transport error lost leaf *url.Error type: %T %v", got, got)
	}
}

func TestVenueErrorUsesOnlyFixedSafeMessages(t *testing.T) {
	tests := []struct {
		name        string
		rawMessage  string
		wantMessage string
		wantLimited bool
	}{
		{
			name:        "unstructured authentication echo",
			rawMessage:  `{"msg":"signature 0xSENTINEL_ASTER_AUTH"}`,
			wantMessage: "<redacted>",
		},
		{
			name:        "too many requests",
			rawMessage:  "too many requests; token 0xSENTINEL_ASTER_RATE",
			wantMessage: "too many requests",
			wantLimited: true,
		},
		{
			name:        "request weight",
			rawMessage:  "request weight exceeded; signature 0xSENTINEL_ASTER_WEIGHT",
			wantMessage: "request weight limit exceeded",
			wantLimited: true,
		},
		{
			name:        "banned until",
			rawMessage:  "IP banned until 999999; api key 0xSENTINEL_ASTER_BAN",
			wantMessage: "request banned until rate limit reset",
			wantLimited: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewVenueError(429, "POST", "/fapi/v3/order", -1003, tt.rawMessage)
			var venueErr *VenueError
			if !errors.As(err, &venueErr) {
				t.Fatalf("public VenueError type was lost: %T", err)
			}
			if venueErr.StatusCode() != 429 || venueErr.Code() != -1003 {
				t.Fatalf("status/code = %d/%d, want 429/-1003", venueErr.StatusCode(), venueErr.Code())
			}
			if venueErr.Message() != tt.wantMessage {
				t.Fatalf("Message() = %q, want %q", venueErr.Message(), tt.wantMessage)
			}
			if strings.Contains(err.Error(), "SENTINEL_") || strings.Contains(venueErr.Message(), "SENTINEL_") {
				t.Fatalf("venue error leaked server-provided authentication material: %v", err)
			}
			if got := mbx.IsRateLimitMessage(venueErr.Message()); got != tt.wantLimited {
				t.Fatalf("safe message rate-limit classification = %v, want %v", got, tt.wantLimited)
			}
		})
	}
}

func TestRedactURLDropsSignedPreimage(t *testing.T) {
	got := RedactURL("https://fapi.asterdex-testnet.com/fapi/v3/order?symbol=ASTERUSDT&nonce=1&signature=0xabc")
	if strings.Contains(got, "ASTERUSDT") || strings.Contains(got, "nonce") || strings.Contains(got, "0xabc") {
		t.Fatalf("signed URL was not redacted: %q", got)
	}
	if !strings.Contains(got, "query=%3Credacted%3E") && !strings.Contains(got, "<redacted>") {
		t.Fatalf("redaction marker missing: %q", got)
	}
}
