package news

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestDialErrorRedactsNestedSignedWebSocketURL(t *testing.T) {
	const (
		signature = "binance-news-signature-secret"
		timestamp = "1700000000000"
	)
	sentinel := errors.New("synthetic dial failure")
	signedURL := "wss://api.binance.test/sapi/wss?signature=" + signature + "&timestamp=" + timestamp
	err := binanceNewsDialError(fmt.Errorf("wrapped dial failure: %w", &url.Error{
		Op:  "GET",
		URL: signedURL,
		Err: sentinel,
	}))
	if err == nil {
		t.Fatal("sanitized dial error is nil")
	}
	for _, secret := range []string{signedURL, signature, timestamp, "signature=", "timestamp="} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("dial error leaked %q: %v", secret, err)
		}
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("dial error lost cause: %v", err)
	}
}
