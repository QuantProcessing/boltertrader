package grvt

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func newLiveClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t)
	client := NewClient()
	client.HttpClient.Timeout = 20 * time.Second
	return client
}

func retryGRVTLive(t *testing.T, op string, fn func() error) {
	t.Helper()

	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = fn()
		if err == nil {
			return
		}
		if !isTransientGRVTError(err) {
			t.Fatalf("%s failed: %v", op, err)
		}
		time.Sleep(time.Second)
	}

	t.Fatalf("%s failed after retries: %v", op, err)
}

func isTransientGRVTError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "eof") ||
		strings.Contains(lower, "connection reset")
}
