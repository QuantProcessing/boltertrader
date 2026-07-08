package sdk

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWSPrivateSubscribeRequestUsesGateSignature(t *testing.T) {
	client := MustNewWSClient(ProductSpot).
		WithCredentials("key", "secret").
		WithClock(func() time.Time { return time.Unix(123, 0) })

	req, err := client.subscribeRequest(ChannelSpotOrder, "subscribe", []string{"BTC_USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if req.Auth == nil {
		t.Fatal("expected auth")
	}
	if got, want := req.Auth.Method, "api_key"; got != want {
		t.Fatalf("method=%q want %q", got, want)
	}
	if got, want := req.Auth.Key, "key"; got != want {
		t.Fatalf("key=%q want %q", got, want)
	}
	wantSign := sign("secret", "channel=spot.orders&event=subscribe&time=123")
	if req.Auth.Sign != wantSign {
		t.Fatalf("sign=%q want %q", req.Auth.Sign, wantSign)
	}
}

func TestWSPublicSubscribeDoesNotRequireCredentials(t *testing.T) {
	client := MustNewWSClient(ProductFuturesUSDT).WithClock(func() time.Time { return time.Unix(123, 0) })
	req, err := client.subscribeRequest(ChannelFuturesTrade, "subscribe", []string{"BTC_USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if req.Auth != nil {
		t.Fatalf("unexpected auth: %+v", req.Auth)
	}
	if got, want := req.Channel, ChannelFuturesTrade; got != want {
		t.Fatalf("channel=%q want %q", got, want)
	}
}

func TestWSPrivateSubscribeRequiresCredentials(t *testing.T) {
	client := MustNewWSClient(ProductFuturesUSDT)
	_, err := client.subscribeRequest(ChannelFuturesOrder, "subscribe", []string{"BTC_USDT"})
	if err == nil || !strings.Contains(err.Error(), "credentials required") {
		t.Fatalf("expected credentials error, got %v", err)
	}
}

func TestDecodeWSMessages(t *testing.T) {
	spotPayload := []byte(`{"time":1,"time_ms":1000,"channel":"spot.orders","event":"update","result":[{"id":"1","currency_pair":"BTC_USDT","side":"buy","amount":"0.01","status":"open"}]}`)
	spot, err := DecodeSpotOrderMessage(spotPayload)
	if err != nil {
		t.Fatal(err)
	}
	if spot.Channel != ChannelSpotOrder || len(spot.Orders) != 1 || spot.Orders[0].ID != "1" {
		t.Fatalf("unexpected spot message: %+v", spot)
	}

	futuresPayload := []byte(`{"time":1,"channel":"futures.positions","event":"update","result":[{"contract":"BTC_USDT","size":2,"entry_price":"100"}]}`)
	futures, err := DecodeFuturesPositionMessage(futuresPayload)
	if err != nil {
		t.Fatal(err)
	}
	if futures.Channel != ChannelFuturesPosition || len(futures.Positions) != 1 || futures.Positions[0].Size != 2 {
		t.Fatalf("unexpected futures message: %+v", futures)
	}
}

func TestWSKeyKeepsPayloadSpecificSubscriptionsDistinct(t *testing.T) {
	if got, want := wsKey(ChannelSpotOrderBook, []string{"BTC_USDT", "100ms"}), "spot.order_book|BTC_USDT,100ms"; got != want {
		t.Fatalf("key=%q want %q", got, want)
	}
	if got, want := wsKey(ChannelSpotOrderBook, nil), ChannelSpotOrderBook; got != want {
		t.Fatalf("key=%q want %q", got, want)
	}
}

func TestWSUnsupportedProduct(t *testing.T) {
	if _, err := NewWSClient("option"); err == nil {
		t.Fatal("expected unsupported product error")
	}
}

func TestWSUnsubscribeWithoutConnectionIsNoop(t *testing.T) {
	client := MustNewWSClient(ProductSpot)
	if err := client.Unsubscribe(context.Background(), ChannelSpotTrade, []string{"BTC_USDT"}); err != nil {
		t.Fatal(err)
	}
}

func TestWSRequestMarshalsGateAuthKeys(t *testing.T) {
	req := wsRequest{
		Time:    123,
		Channel: ChannelSpotOrder,
		Event:   "subscribe",
		Payload: []string{"BTC_USDT"},
		Auth:    &WSAuth{Method: "api_key", Key: "key", Sign: "sig"},
	}
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, want := range []string{`"KEY":"key"`, `"SIGN":"sig"`, `"method":"api_key"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s missing %s", text, want)
		}
	}
}
