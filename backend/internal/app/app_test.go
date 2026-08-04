package app

import (
	"context"
	"crypto/sha256"
	"net/http"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/config"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
)

func TestAppStartsAndStops(t *testing.T) {
	passwordHash, err := session.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		BrowserListenAddr: "127.0.0.1:18191", WorkListenAddr: "127.0.0.1:18192",
		BootstrapUsername: "operator", BootstrapPasswordHash: passwordHash,
		FundingWorkerTokenFile:   "/protected/test-worker.token",
		FundingWorkerTokenDigest: sha256.Sum256([]byte("test-worker-token-with-at-least-32-bytes")),
		Mode:                     "test",
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Start(ctx) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil && err != http.ErrServerClosed {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("app did not stop")
	}
}
