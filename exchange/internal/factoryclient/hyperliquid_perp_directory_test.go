package factoryclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	hyperliquidbase "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func TestHyperliquidPerpDirectoryBoundsFanoutAndRetriesOneDex(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	active, maximum := 0, 0
	attempts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		switch payload["type"] {
		case "allPerpMetas":
			_, _ = writer.Write([]byte(`[
				{"universe":[{"name":"BTC","szDecimals":3}],"collateralToken":0},
				{"universe":[{"name":"xyz:TSLA","szDecimals":2}],"collateralToken":0},
				{"universe":[{"name":"flx:GOLD","szDecimals":2}],"collateralToken":0}
			]`))
		case "perpDexs":
			_, _ = writer.Write([]byte(`[null,{"name":"xyz"},{"name":"flx"}]`))
		case "spotMeta":
			_, _ = writer.Write([]byte(`{"tokens":[{"name":"USDC","index":0,"szDecimals":6}],"universe":[]}`))
		case "metaAndAssetCtxs":
			dex, _ := payload["dex"].(string)
			mu.Lock()
			attempts[dex]++
			attempt := attempts[dex]
			active++
			if active > maximum {
				maximum = active
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			active--
			mu.Unlock()
			if dex == "xyz" && attempt == 1 {
				http.Error(writer, "retry", http.StatusTooManyRequests)
				return
			}
			coin := "BTC"
			if dex == "xyz" {
				coin = "xyz:TSLA"
			} else if dex == "flx" {
				coin = "flx:GOLD"
			}
			_, _ = writer.Write([]byte(`[{"universe":[{"name":"` + coin + `"}]},[{"funding":"0.0001","markPx":"100","oraclePx":"100","openInterest":"1"}]]`))
		default:
			http.Error(writer, "unexpected request", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	base := hyperliquidbase.NewClient()
	base.BaseURL = server.URL
	base.Http = server.Client()
	directory, _, err := hyperliquidPerpDirectory(context.Background(), base, false, true)
	if err != nil {
		t.Fatalf("directory: %v", err)
	}
	if len(directory) != 3 {
		t.Fatalf("directory length = %d, want 3", len(directory))
	}
	mu.Lock()
	defer mu.Unlock()
	if maximum > hyperliquidDirectoryConcurrency {
		t.Fatalf("maximum concurrent meta reads = %d, want <= %d", maximum, hyperliquidDirectoryConcurrency)
	}
	if attempts["xyz"] != 2 {
		t.Fatalf("xyz attempts = %d, want 2", attempts["xyz"])
	}
}
