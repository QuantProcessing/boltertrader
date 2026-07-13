package common

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

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

func TestRedactURLDropsSignedPreimage(t *testing.T) {
	got := RedactURL("https://fapi.asterdex-testnet.com/fapi/v3/order?symbol=ASTERUSDT&nonce=1&signature=0xabc")
	if strings.Contains(got, "ASTERUSDT") || strings.Contains(got, "nonce") || strings.Contains(got, "0xabc") {
		t.Fatalf("signed URL was not redacted: %q", got)
	}
	if !strings.Contains(got, "query=%3Credacted%3E") && !strings.Contains(got, "<redacted>") {
		t.Fatalf("redaction marker missing: %q", got)
	}
}
