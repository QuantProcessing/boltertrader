package sdk

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestPrivateWSClient_Subscribe(t *testing.T) {
	client := newLivePrivateWSClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	arg := WSArg{InstType: "UTA", Topic: "order"}
	if err := client.Subscribe(ctx, arg, func(json.RawMessage) {}); err != nil {
		skipIfBitgetPrivateWSUnavailable(t, err)
		t.Fatalf("Subscribe: %v", err)
	}
	if client.handlers[wsKey(arg)] == nil {
		t.Fatal("expected handler to be registered")
	}
}

func TestPrivateWSClient_Unsubscribe(t *testing.T) {
	client := newLivePrivateWSClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	arg := WSArg{InstType: "UTA", Topic: "order"}
	if err := client.Subscribe(ctx, arg, func(json.RawMessage) {}); err != nil {
		skipIfBitgetPrivateWSUnavailable(t, err)
		t.Fatalf("Subscribe: %v", err)
	}
	if err := client.Unsubscribe(ctx, arg); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	if client.handlers[wsKey(arg)] != nil {
		t.Fatal("expected handler to be removed")
	}
}

func TestDecodeAccountMessage(t *testing.T) {
	msg, err := DecodeAccountMessage([]byte(`{"arg":{"instType":"UTA","topic":"account"},"action":"snapshot","data":[{"coin":"USDT","available":"10","equity":"12","usdtValue":"12"}]}`))
	if err != nil {
		t.Fatalf("DecodeAccountMessage: %v", err)
	}
	if msg.Arg.Topic != "account" || msg.Action != "snapshot" {
		t.Fatalf("unexpected account envelope: %+v", msg)
	}
	if len(msg.Data) != 1 || msg.Data[0].Coin != "USDT" || msg.Data[0].Equity != "12" {
		t.Fatalf("unexpected account data: %+v", msg.Data)
	}
}

func TestPrivateWSDebugPayloadRedactsLoginCredentials(t *testing.T) {
	req := wsLoginRequest{
		Op: "login",
		Args: []wsLoginArgs{{
			APIKey:     "bitget-api-key",
			Passphrase: "bitget-passphrase",
			Timestamp:  "1700000000",
			Sign:       "secret-derived-signature",
		}},
	}

	payload, err := marshalPrivateWSDebugPayload(req)
	if err != nil {
		t.Fatalf("marshalPrivateWSDebugPayload: %v", err)
	}
	got := string(payload)
	for _, secret := range []string{"bitget-api-key", "bitget-passphrase", "secret-derived-signature"} {
		if strings.Contains(got, secret) {
			t.Fatalf("debug payload leaked %q in %s", secret, got)
		}
	}
	var decoded wsLoginRequest
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal debug payload: %v", err)
	}
	if decoded.Op != "login" || len(decoded.Args) != 1 {
		t.Fatalf("unexpected debug payload: %+v", decoded)
	}
	arg := decoded.Args[0]
	if arg.APIKey != "<redacted>" || arg.Passphrase != "<redacted>" || arg.Sign != "<redacted>" {
		t.Fatalf("login credentials were not redacted: %+v", arg)
	}
	if arg.Timestamp != "1700000000" {
		t.Fatalf("timestamp = %q, want 1700000000", arg.Timestamp)
	}
}

func newLivePrivateWSClient(t *testing.T) *PrivateWSClient {
	t.Helper()
	testenv.RequireLiveRead(t, "BITGET_API_KEY", "BITGET_SECRET_KEY", "BITGET_PASSPHRASE")
	client := NewPrivateWSClient().WithCredentials(os.Getenv("BITGET_API_KEY"), os.Getenv("BITGET_SECRET_KEY"), os.Getenv("BITGET_PASSPHRASE"))
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client
}
