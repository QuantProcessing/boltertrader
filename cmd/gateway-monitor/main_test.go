package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/services/monitor"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestCommandConfigurationAndBoundedPublicRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/market/ws" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer local-service-token" {
			t.Error("unexpected request")
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		var control wire.Control
		if err := wsjson.Read(r.Context(), conn, &control); err != nil {
			t.Error(err)
			return
		}
		_ = wsjson.Write(r.Context(), conn, wire.Message{Type: wire.MessageAck, Topic: wire.TopicBBO, SubscriptionID: "one", RequestID: control.RequestID})
		for i := range 2 {
			kind := wire.MessageDelta
			if i == 0 {
				kind = wire.MessageSnapshot
			}
			_ = wsjson.Write(r.Context(), conn, wire.Message{Type: kind, Topic: wire.TopicBBO, SubscriptionID: "one", Generation: 1, Sequence: uint64(i + 1), ObservedAt: time.Now().UTC().Add(time.Duration(i-1) * time.Second), Data: json.RawMessage(`{"bid":{"price":"99","quantity":"1"},"ask":{"price":"101","quantity":"1"}}`)})
		}
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()
	var output, diagnostics bytes.Buffer
	args := []string{"-gateway", server.URL, "-instrument", "btc", "-state", filepath.Join(t.TempDir(), "state.json"), "-window", "10s", "-min-samples", "2", "-max-gap", "3s", "-duration", "500ms"}
	if err := run(t.Context(), args, "local-service-token", &output, &diagnostics); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	var events []monitor.Event
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var e monitor.Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if len(events) != 2 || events[0].State != monitor.Insufficient || events[1].State != monitor.Normal || strings.Contains(output.String(), "local-service-token") {
		t.Fatal(output.String())
	}
}

func TestCommandRejectsInvalidRulesBeforeConnecting(t *testing.T) {
	for _, extra := range [][]string{
		{"-change-percent", "0"}, {"-change-percent", "NaN"}, {"-change-percent", "101"}, {"-window", "500ms"}, {"-window", "24h"}, {"-max-gap", "0s"}, {"-min-samples", "1"}, {"-min-samples", "10000"}, {"-cooldown", "0s"}, {"-instrument", "btc"}, {"-duration", "-1s"}, {"positional"},
	} {
		t.Run(strings.Join(extra, "_"), func(t *testing.T) {
			args := append([]string{"-instrument", "btc", "-state", filepath.Join(t.TempDir(), "state.json")}, extra...)
			var output, diagnostics bytes.Buffer
			if err := run(context.Background(), args, "offline-token", &output, &diagnostics); err == nil {
				t.Fatal("invalid rule accepted")
			}
			if output.Len() != 0 {
				t.Fatal("invalid rule started monitoring")
			}
		})
	}
}
