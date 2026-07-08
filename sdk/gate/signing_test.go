package sdk

import (
	"net/http"
	"testing"
	"time"
)

func TestBuildSigningPayload(t *testing.T) {
	payload := buildSigningPayload(http.MethodGet, "/api/v4/spot/accounts", "currency=USDT", "", "1700000000")
	want := "GET\n/api/v4/spot/accounts\ncurrency=USDT\ncf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e\n1700000000"
	if payload != want {
		t.Fatalf("payload=%q want %q", payload, want)
	}
}

func TestSignUsesHMACSHA512Hex(t *testing.T) {
	got := sign("secret", "GET\n/api/v4/spot/accounts\ncurrency=USDT\ncf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e\n1700000000")
	want := "c787d4b8adad55149be36a5dfcb2509fc17ef0e23ed5b1c90da2dc85042ca79998221b1deb5afc0b85823fa7c28cab66a951b1ecd612b9e1f4f8820123463e26"
	if got != want {
		t.Fatalf("sign=%q want %q", got, want)
	}
}

func TestBuildTimestampUsesSeconds(t *testing.T) {
	if got, want := buildTimestamp(time.Unix(1700000000, 999000000)), "1700000000"; got != want {
		t.Fatalf("timestamp=%q want %q", got, want)
	}
}
