package perp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestLowLevelWSClientKeepsEndpointPrivate(t *testing.T) {
	client := newWSClient(context.Background(), "wss://example.com/ws")
	t.Cleanup(client.Close)

	var legacyTyped *WsClient = client
	if legacyTyped != client {
		t.Fatal("legacy type alias should reference the same concrete type")
	}
	if client.endpoint != "wss://example.com/ws" {
		t.Fatalf("endpoint = %q", client.endpoint)
	}
}

func TestWsMarketClientKeepsLegacyEmbeddedFieldName(t *testing.T) {
	client := newTestWSMarketClient(t, context.Background())
	t.Cleanup(client.Close)

	if client.WsClient == nil {
		t.Fatal("expected legacy embedded WsClient field to remain available")
	}
	var modernTyped *WSClient = client.WsClient
	if modernTyped != client.WsClient {
		t.Fatal("legacy embedded field should still reference the WSClient implementation")
	}
}

func TestWsMarketClientRoutesThreeSecondMarkPriceStream(t *testing.T) {
	client := newTestWSMarketClient(t, context.Background())
	t.Cleanup(client.Close)

	var got *WsMarkPriceEvent
	err := client.SubscribeMarkPrice("btcusdt", "3s", func(event *WsMarkPriceEvent) error {
		got = event
		return nil
	})
	if err == nil || (!strings.Contains(err.Error(), "not connected") && !strings.Contains(err.Error(), "not established")) {
		t.Fatalf("expected disconnected subscribe error, got %v", err)
	}

	client.handleMessage([]byte(`{"e":"markPriceUpdate","E":7000,"s":"BTCUSDT","p":"200","i":"199","r":"0.0007","T":28800000}`))
	if got == nil || got.Symbol != "BTCUSDT" || got.FundingRate != "0.0007" {
		t.Fatalf("expected 3s mark price route, got %#v", got)
	}
}

func TestWsMarketClientRoutesAllMarkPriceStream(t *testing.T) {
	client := newTestWSMarketClient(t, context.Background())
	t.Cleanup(client.Close)

	var got []*WsMarkPriceEvent
	err := client.SubscribeAllMarkPrice("1s", func(events []*WsMarkPriceEvent) error {
		got = events
		return nil
	})
	if err == nil || (!strings.Contains(err.Error(), "not connected") && !strings.Contains(err.Error(), "not established")) {
		t.Fatalf("expected disconnected subscribe error, got %v", err)
	}
	if _, ok := client.subs["!markPrice@arr@1s"]; !ok {
		t.Fatalf("expected !markPrice@arr@1s subscription, got %#v", client.subs)
	}

	raw := []byte(`[{"e":"markPriceUpdate","E":7000,"s":"BTCUSDT","p":"200","i":"199","r":"0.0007","T":28800000}]`)
	client.CallSubscription("!markPrice@arr@1s", raw)
	if len(got) != 1 || got[0].Symbol != "BTCUSDT" || got[0].FundingRate != "0.0007" {
		t.Fatalf("expected manual all mark price route, got %#v", got)
	}
	got = nil
	var headers []struct {
		EventType string `json:"e"`
		EventTime int64  `json:"E"`
	}
	if err := json.Unmarshal(raw, &headers); err != nil {
		t.Fatalf("unexpected array header unmarshal error: %v", err)
	}
	if len(headers) != 1 || headers[0].EventType != "markPriceUpdate" {
		t.Fatalf("unexpected parsed array header: %#v", headers)
	}
	client.handleArrayMessage(raw)
	if len(got) != 1 || got[0].Symbol != "BTCUSDT" || got[0].FundingRate != "0.0007" {
		t.Fatalf("expected direct array mark price route, got %#v", got)
	}
	got = nil
	client.handleMessage(raw)
	if len(got) != 1 || got[0].Symbol != "BTCUSDT" || got[0].FundingRate != "0.0007" {
		t.Fatalf("expected all mark price route, got %#v", got)
	}
}
