package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExampleReadsGatewayWithNoVenueDependencies(t *testing.T) {
	const instrument = "binance:live:spot:BTC-USDT"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer offline-token" {
			t.Error("unexpected request")
		}
		if r.URL.Path == "/v1/market/instruments" {
			fmt.Fprint(w, `{"revision":"one","observed_at":"2026-09-10T00:00:00Z","complete":true,"unavailable_profiles":[],"items":[{"instrument_id":"binance:live:spot:BTC-USDT","venue":"binance","environment":"live","product":"spot","route_profile_id":"binance.spot.v1","base_asset":"BTC","quote_asset":"USDT","ready":true}]}`)
		} else if r.URL.Path == "/v1/market/snapshot" {
			fmt.Fprint(w, `{"instrument_id":"binance:live:spot:BTC-USDT","snapshot":{"type":"snapshot","topic":"market.bbo","generation":1,"sequence":1,"observedAt":"2026-09-10T00:00:00Z","data":{"bid":{"price":"100","quantity":"1"},"ask":{"price":"101","quantity":"1"}}}}`)
		} else {
			t.Error("example called a nonmarket path")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	for _, args := range [][]string{{"-gateway", server.URL}, {"-gateway", server.URL, "-instrument", instrument}} {
		var output bytes.Buffer
		if err := run(context.Background(), args, "offline-token", &output); err != nil || !strings.Contains(output.String(), instrument) {
			t.Fatalf("output=%s err=%v", output.String(), err)
		}
	}
}
