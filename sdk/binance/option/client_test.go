package option

import (
	"net/http"
	"testing"
	"time"
)

func TestClient_DefaultHTTPTimeout(t *testing.T) {
	client := NewClient()
	if client.HTTPClient.Timeout <= 0 {
		t.Fatal("expected default HTTP timeout")
	}
}

func TestClient_WithHTTPClient(t *testing.T) {
	httpClient := &http.Client{Timeout: 42 * time.Second}
	client := NewClient().WithHTTPClient(httpClient)
	if client.HTTPClient != httpClient {
		t.Fatal("WithHTTPClient did not install provided client")
	}
}
