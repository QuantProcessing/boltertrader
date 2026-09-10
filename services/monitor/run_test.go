package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/clients/gateway"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type eventWriter struct {
	bytes.Buffer
	events []Event
	stopOn string
	cancel context.CancelFunc
}

func (w *eventWriter) Write(data []byte) (int, error) {
	var event Event
	if err := json.Unmarshal(bytes.TrimSpace(data), &event); err != nil {
		return 0, err
	}
	w.events = append(w.events, event)
	if event.Kind == w.stopOn {
		w.cancel()
	}
	return w.Buffer.Write(data)
}

func TestRunnerGatewayDisconnectAndFreshSnapshot(t *testing.T) {
	testRunnerGatewayRecovery(t, false)
}

func TestRunnerNewGenerationSnapshotRebuildsWindow(t *testing.T) {
	testRunnerGatewayRecovery(t, true)
}

func testRunnerGatewayRecovery(t *testing.T, sameConnection bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	var sessions atomic.Int32
	closed := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/market/ws" || r.Header.Get("Authorization") != "Bearer offline-token" {
			t.Error("monitor left public market boundary")
		}
		n := sessions.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		defer func() { closed <- struct{}{} }()
		var request wire.Control
		if err := wsjson.Read(ctx, conn, &request); err != nil {
			t.Error(err)
			return
		}
		if request.Operation != wire.OperationSubscribe || len(request.Topics) != 1 || request.Topics[0].Topic != wire.TopicBBO || request.Topics[0].InstrumentID != "btc" {
			t.Error("wrong subscription")
		}
		if err := wsjson.Write(ctx, conn, wire.Message{Type: wire.MessageAck, Topic: wire.TopicBBO, RequestID: request.RequestID, SubscriptionID: "one"}); err != nil {
			t.Error(err)
			return
		}
		now := time.Now().UTC()
		for i, value := range []string{"100", "100", "90"} {
			kind, at := wire.MessageDelta, now.Add(time.Duration(i-2)*time.Second)
			if i == 0 {
				kind = wire.MessageSnapshot
			}
			if n == 2 {
				value, at = "45", now
			}
			data, _ := json.Marshal(wire.BBO{Bid: wire.BookLevel{Price: value, Quantity: "1"}, Ask: wire.BookLevel{Price: value, Quantity: "1"}})
			// Generation starts at 1 again after a process/socket restart.
			if err := wsjson.Write(ctx, conn, wire.Message{Type: kind, Topic: wire.TopicBBO, SubscriptionID: "one", Generation: 1, Sequence: uint64(i + 1), ObservedAt: at, Data: data}); err != nil {
				t.Error(err)
				return
			}
			if n == 2 {
				break
			}
		}
		if sameConnection {
			data := json.RawMessage(`{"bid":{"price":"45","quantity":"1"},"ask":{"price":"45","quantity":"1"}}`)
			if err := wsjson.Write(ctx, conn, wire.Message{Type: wire.MessageSnapshot, Topic: wire.TopicBBO, SubscriptionID: "one", Generation: 2, Sequence: 1, ObservedAt: time.Now().UTC(), Data: data}); err != nil {
				t.Error(err)
				return
			}
		} else if n == 1 {
			return
		} // abrupt connection loss, no replayed history
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	client, err := gateway.New(gateway.Config{URL: server.URL, Token: "offline-token"})
	if err != nil {
		t.Fatal(err)
	}
	c := testConfig()
	c.Source = server.URL
	w := &eventWriter{stopOn: "recovered", cancel: cancel}
	if err := Run(ctx, client, c, filepath.Join(t.TempDir(), "state.json"), w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kinds(w.events), []string{"state", "price_change", "interrupted", "recovered"}) {
		t.Fatalf("unexpected fault output: %s", w.String())
	}
	wantSessions := int32(2)
	if sameConnection {
		wantSessions = 1
	}
	if w.events[3].State != Insufficient || w.events[3].Samples != 1 || sessions.Load() != wantSessions {
		t.Fatal("reconnected data used old baseline")
	}
	for range wantSessions {
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("subscription leaked on exit")
		}
	}
}

func TestRunnerSilentHealthySocketStillTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		var control wire.Control
		if err := wsjson.Read(ctx, conn, &control); err != nil {
			t.Error(err)
			return
		}
		_ = wsjson.Write(ctx, conn, wire.Message{Type: wire.MessageAck, Topic: wire.TopicBBO, RequestID: control.RequestID, SubscriptionID: "one"})
		_ = wsjson.Write(ctx, conn, wire.Message{Type: wire.MessageSnapshot, Topic: wire.TopicBBO, SubscriptionID: "one", Generation: 1, Sequence: 1, ObservedAt: time.Now().UTC(), Data: json.RawMessage(`{"bid":{"price":"100","quantity":"1"},"ask":{"price":"101","quantity":"1"}}`)})
		_, _, _ = conn.Read(ctx) // services pings but never sends another quote
	}))
	defer server.Close()
	client, err := gateway.New(gateway.Config{URL: server.URL, Token: "offline-token"})
	if err != nil {
		t.Fatal(err)
	}
	c := testConfig()
	c.Source, c.MaxGap = server.URL, time.Second
	w := &eventWriter{stopOn: "interrupted", cancel: cancel}
	if err := Run(ctx, client, c, filepath.Join(t.TempDir(), "state.json"), w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kinds(w.events), []string{"state", "interrupted"}) || w.events[1].Reason != "data_timeout" {
		t.Fatal(w.String())
	}
}

func TestRunnerAuthFailureExitsAndPersistsInterruption(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "denied", http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := gateway.New(gateway.Config{URL: server.URL, Token: "offline-token"})
	if err != nil {
		t.Fatal(err)
	}
	c := testConfig()
	c.Source = server.URL
	w := &eventWriter{}
	if err := Run(t.Context(), client, c, filepath.Join(t.TempDir(), "state.json"), w); err == nil {
		t.Fatal("auth failure hidden")
	}
	if requests.Load() != 1 || !reflect.DeepEqual(kinds(w.events), []string{"state", "interrupted"}) || w.events[1].Reason != "stream_stopped" {
		t.Fatal(w.String())
	}
}

func TestMonitorImportsOnlyGatewayClientAndWire(t *testing.T) {
	const module = "github.com/QuantProcessing/boltertrader/"
	for _, dir := range []string{".", "../../cmd/gateway-monitor"} {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range file.Imports {
				name := strings.Trim(imp.Path.Value, "\"")
				if strings.HasPrefix(name, module) && name != module+"clients/gateway" && name != module+"api/exchange-gateway/v1" && name != module+"services/monitor" {
					t.Errorf("%s depends on %s", path, name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
