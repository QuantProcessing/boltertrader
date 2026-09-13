package notifications

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testService(t *testing.T) (*Service, *MemoryStore) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	sender := &Sender{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"code":200}`)), Header: make(http.Header)}, nil
	})}}
	service, err := New(store, key, sender)
	if err != nil {
		t.Fatal(err)
	}
	return service, store
}

func TestSaveNeverReturnsOrStoresPlaintext(t *testing.T) {
	service, store := testService(t)
	channel, err := service.Save(context.Background(), "principal", Telegram, Input{Secret: "123:token_value", Enabled: true, ChatID: "-1001"})
	if err != nil {
		t.Fatal(err)
	}
	if channel.ChatID != "-1001" {
		t.Fatalf("channel=%+v", channel)
	}
	record, err := store.Get(context.Background(), "principal", Telegram)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record.Sealed), "123:token_value") {
		t.Fatal("token was stored in plaintext")
	}
	list, err := service.List(context.Background(), "principal")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ChatID != "-1001" {
		t.Fatalf("list=%+v", list)
	}
}

func TestTestDeliveryAndCriticalBarkValidation(t *testing.T) {
	service, _ := testService(t)
	if _, err := service.Save(context.Background(), "principal", Bark, Input{Secret: "device-key", ServerURL: "https://api.day.app", Critical: true, Volume: 10}); err != nil {
		t.Fatal(err)
	}
	if err := service.Test(context.Background(), "principal", Bark); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Save(context.Background(), "principal", Bark, Input{Secret: "device-key", ServerURL: "http://127.0.0.1"}); err == nil {
		t.Fatal("accepted non-public Bark origin")
	}
}

func TestDisabledChannelSkipsFutureSend(t *testing.T) {
	service, _ := testService(t)
	if _, err := service.Save(context.Background(), "principal", Telegram, Input{Secret: "123:token_value", Enabled: false, ChatID: "-1001"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Send(context.Background(), "principal", Telegram, Message{Title: "x", Body: "y"}); err != nil {
		t.Fatal(err)
	}
}
