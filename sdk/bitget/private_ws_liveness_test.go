package sdk

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestPrivateWSReadIdleTimeoutRefreshesOnPongAndReconnectsBlackhole(t *testing.T) {
	const firstPongCount = 15

	var dials atomic.Int32
	var firstPongs atomic.Int32
	pongsFinished := make(chan struct{})
	replayed := make(chan struct{})
	finishServer := make(chan struct{})
	var finishOnce sync.Once
	var pongsFinishedOnce sync.Once
	t.Cleanup(func() { finishOnce.Do(func() { close(finishServer) }) })
	arg := WSArg{InstType: "UTA", Topic: "order"}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := dials.Add(1)
		if !serveBitgetLogin(conn) {
			return
		}
		req := readBitgetSubscription(t, conn)
		if err := writeBitgetSubscribeACK(conn, req, true, "success"); err != nil {
			return
		}
		if attempt == 1 {
			defer pongsFinishedOnce.Do(func() { close(pongsFinished) })
			for range firstPongCount {
				time.Sleep(20 * time.Millisecond)
				if err := conn.WriteMessage(websocket.TextMessage, []byte("pong")); err != nil {
					return
				}
				firstPongs.Add(1)
			}
			pongsFinishedOnce.Do(func() { close(pongsFinished) })
			<-finishServer
			return
		}
		if attempt == 2 {
			close(replayed)
		}
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.TextMessage, []byte("pong")); err != nil {
					return
				}
			case <-finishServer:
				return
			}
		}
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	client.pingInterval = 20 * time.Millisecond
	client.readIdleTimeout = 250 * time.Millisecond
	client.subscriptionAckTimeout = time.Second
	defer client.Close()

	started := make(chan struct{})
	recovered := make(chan struct{})
	var startedOnce sync.Once
	var recoveredOnce sync.Once
	client.SetReconnectHooks(func(error) {
		startedOnce.Do(func() { close(started) })
	}, func() {
		recoveredOnce.Do(func() { close(recovered) })
	})
	if err := client.Subscribe(context.Background(), arg, func(json.RawMessage) {}); err != nil {
		t.Fatalf("initial Subscribe: %v", err)
	}
	waitForBitgetSignal(t, pongsFinished, "initial heartbeat sequence")
	if got := firstPongs.Load(); got != firstPongCount {
		t.Fatalf("initial pong messages=%d, want %d before entering heartbeat blackhole", got, firstPongCount)
	}
	select {
	case <-started:
		t.Fatal("read-idle deadline was not refreshed by inbound pong messages")
	default:
	}
	waitForBitgetSignal(t, started, "read-idle reconnect start after heartbeat blackhole")
	waitForBitgetSignal(t, replayed, "fresh connection subscription replay after heartbeat blackhole")
	waitForBitgetSignal(t, recovered, "recovery after heartbeat blackhole")
	if got := dials.Load(); got < 2 {
		t.Fatalf("websocket dials=%d, want fresh connection after heartbeat blackhole", got)
	}
	finishOnce.Do(func() { close(finishServer) })
}

func TestPrivateWSPingWriteFailureClosesExactSocketAndStartsRecovery(t *testing.T) {
	clientConn, _ := bitgetPrivateWSPair(t)
	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws://127.0.0.1:1"
	client.pingInterval = 10 * time.Millisecond
	client.readIdleTimeout = time.Second
	client.mu.Lock()
	client.conn = clientConn
	client.authenticated = true
	client.mu.Unlock()
	defer client.Close()

	started := make(chan struct{})
	var startedOnce sync.Once
	client.SetReconnectHooks(func(error) {
		startedOnce.Do(func() { close(started) })
	}, nil)
	go client.readLoop(clientConn, make(chan error, 1))

	tcpConn, ok := clientConn.UnderlyingConn().(*net.TCPConn)
	if !ok {
		t.Fatalf("underlying websocket connection=%T, want *net.TCPConn", clientConn.UnderlyingConn())
	}
	if err := tcpConn.CloseWrite(); err != nil {
		t.Fatalf("half-close websocket writes: %v", err)
	}
	go client.pingLoop(clientConn)
	waitForBitgetSignal(t, started, "reconnect start after ping write failure")
}

func TestPrivateWSBlockingReconnectStartDoesNotGateRecovery(t *testing.T) {
	var dials atomic.Int32
	closeInitial := make(chan struct{})
	replayed := make(chan struct{})
	finishServer := make(chan struct{})
	arg := WSArg{InstType: "UTA", Topic: "order"}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := dials.Add(1)
		if !serveBitgetLogin(conn) {
			return
		}
		req := readBitgetSubscription(t, conn)
		if err := writeBitgetSubscribeACK(conn, req, true, "success"); err != nil {
			return
		}
		if attempt == 1 {
			<-closeInitial
			return
		}
		close(replayed)
		<-finishServer
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	client.subscriptionAckTimeout = time.Second
	defer client.Close()

	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	recovered := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseStart) }) })
	client.SetReconnectHooks(func(error) {
		close(startEntered)
		<-releaseStart
	}, func() {
		close(recovered)
	})

	if err := client.Subscribe(context.Background(), arg, func(json.RawMessage) {}); err != nil {
		t.Fatalf("initial Subscribe: %v", err)
	}
	close(closeInitial)
	waitForBitgetSignal(t, startEntered, "blocking reconnect-start hook")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.waitForRecovery(ctx); err != nil {
		t.Fatalf("recovery was gated by reconnect-start hook: %v", err)
	}
	waitForBitgetSignal(t, replayed, "subscription replay while reconnect-start hook is blocked")
	select {
	case <-recovered:
		t.Fatal("reconnect-done hook overtook blocked reconnect-start hook")
	default:
	}
	releaseOnce.Do(func() { close(releaseStart) })
	waitForBitgetSignal(t, recovered, "ordered reconnect-done hook after reconnect-start returns")
	close(finishServer)
}

func TestPrivateWSReconnectStartMayReenterSubscribe(t *testing.T) {
	var dials atomic.Int32
	closeInitial := make(chan struct{})
	addedSeen := make(chan struct{})
	finishServer := make(chan struct{})
	existingArg := WSArg{InstType: "UTA", Topic: "order"}
	addedArg := WSArg{InstType: "UTA", Topic: "fill"}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := dials.Add(1)
		if !serveBitgetLogin(conn) {
			return
		}
		req := readBitgetSubscription(t, conn)
		if err := writeBitgetSubscribeACK(conn, req, true, "success"); err != nil {
			return
		}
		if attempt == 1 {
			<-closeInitial
			return
		}
		added := readBitgetSubscription(t, conn)
		if len(added.Args) != 1 || wsKey(added.Args[0]) != wsKey(addedArg) {
			t.Errorf("subscription from reconnect-start hook = %+v, want %+v", added, addedArg)
			return
		}
		if err := writeBitgetSubscribeACK(conn, added, true, "success"); err != nil {
			return
		}
		close(addedSeen)
		<-finishServer
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	client.subscriptionAckTimeout = time.Second
	defer client.Close()

	hookResult := make(chan error, 1)
	client.SetReconnectHooks(func(error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hookResult <- client.Subscribe(ctx, addedArg, func(json.RawMessage) {})
	}, nil)

	if err := client.Subscribe(context.Background(), existingArg, func(json.RawMessage) {}); err != nil {
		t.Fatalf("initial Subscribe: %v", err)
	}
	close(closeInitial)
	if err := waitForBitgetResult(t, hookResult, "reentrant reconnect-start subscription"); err != nil {
		t.Fatalf("Subscribe from reconnect-start hook: %v", err)
	}
	waitForBitgetSignal(t, addedSeen, "subscription issued by reconnect-start hook")
	close(finishServer)
}

func TestPrivateWSBlockingReconnectDoneDoesNotGateLaterRecovery(t *testing.T) {
	var dials atomic.Int32
	closeConnection := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	thirdReplay := make(chan struct{})
	arg := WSArg{InstType: "UTA", Topic: "order"}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := int(dials.Add(1))
		if attempt > len(closeConnection) || !serveBitgetLogin(conn) {
			return
		}
		req := readBitgetSubscription(t, conn)
		if err := writeBitgetSubscribeACK(conn, req, true, "success"); err != nil {
			return
		}
		if attempt == 3 {
			close(thirdReplay)
			if err := writeBitgetPrivateWSEvent(conn, arg, 3); err != nil {
				return
			}
		}
		<-closeConnection[attempt-1]
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	client.subscriptionAckTimeout = time.Second
	defer client.Close()

	firstDoneEntered := make(chan struct{})
	releaseFirstDone := make(chan struct{})
	secondDone := make(chan struct{})
	secondData := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseFirstDone) }) })
	var sequenceMu sync.Mutex
	var sequence []string
	record := func(step string) {
		sequenceMu.Lock()
		sequence = append(sequence, step)
		sequenceMu.Unlock()
	}
	var startCalls atomic.Int32
	var doneCalls atomic.Int32
	client.SetReconnectHooks(func(error) {
		record("start" + strconv.FormatInt(int64(startCalls.Add(1)), 10))
	}, func() {
		call := doneCalls.Add(1)
		record("done" + strconv.FormatInt(int64(call), 10))
		if call == 1 {
			close(firstDoneEntered)
			<-releaseFirstDone
		} else if call == 2 {
			close(secondDone)
		}
	})

	if err := client.Subscribe(context.Background(), arg, func(payload json.RawMessage) {
		if strings.Contains(string(payload), `"seq":3`) {
			record("data2")
			close(secondData)
		}
	}); err != nil {
		t.Fatalf("initial Subscribe: %v", err)
	}
	close(closeConnection[0])
	waitForBitgetSignal(t, firstDoneEntered, "blocking reconnect-done hook")
	close(closeConnection[1])
	waitForPrivateWSReconnectState(t, client, true)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.waitForRecovery(ctx); err != nil {
		t.Fatalf("later recovery was gated by prior reconnect-done hook: %v", err)
	}
	waitForBitgetSignal(t, thirdReplay, "later subscription replay while reconnect-done hook is blocked")
	sequenceMu.Lock()
	beforeRelease := append([]string(nil), sequence...)
	sequenceMu.Unlock()
	if len(beforeRelease) != 2 || beforeRelease[0] != "start1" || beforeRelease[1] != "done1" {
		t.Fatalf("lifecycle callbacks overtook blocked done hook: %v", beforeRelease)
	}
	releaseOnce.Do(func() { close(releaseFirstDone) })
	waitForBitgetSignal(t, secondDone, "ordered callbacks for later recovery")
	waitForBitgetSignal(t, secondData, "second-generation data after ordered recovery")
	sequenceMu.Lock()
	gotSequence := append([]string(nil), sequence...)
	sequenceMu.Unlock()
	wantSequence := []string{"start1", "done1", "start2", "done2", "data2"}
	if len(gotSequence) != len(wantSequence) {
		t.Fatalf("lifecycle callback sequence = %v, want %v", gotSequence, wantSequence)
	}
	for index := range wantSequence {
		if gotSequence[index] != wantSequence[index] {
			t.Fatalf("lifecycle callback sequence = %v, want %v", gotSequence, wantSequence)
		}
	}
	close(closeConnection[2])
}

func TestPrivateWSHandlerMayReenterSubscribe(t *testing.T) {
	existingArg := WSArg{InstType: "UTA", Topic: "order"}
	addedArg := WSArg{InstType: "UTA", Topic: "fill"}
	finishServer := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if !serveBitgetLogin(conn) {
			return
		}
		initial := readBitgetSubscription(t, conn)
		if err := writeBitgetSubscribeACK(conn, initial, true, "success"); err != nil {
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"arg": existingArg, "action": "update", "data": []any{map[string]any{"seq": 1}},
		}); err != nil {
			return
		}
		added := readBitgetSubscription(t, conn)
		if len(added.Args) != 1 || wsKey(added.Args[0]) != wsKey(addedArg) {
			t.Errorf("subscription from message handler = %+v, want %+v", added, addedArg)
			return
		}
		if err := writeBitgetSubscribeACK(conn, added, true, "success"); err != nil {
			return
		}
		<-finishServer
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	client.subscriptionAckTimeout = 250 * time.Millisecond
	defer client.Close()

	handlerResult := make(chan error, 1)
	if err := client.Subscribe(context.Background(), existingArg, func(json.RawMessage) {
		handlerResult <- client.Subscribe(context.Background(), addedArg, func(json.RawMessage) {})
	}); err != nil {
		t.Fatalf("initial Subscribe: %v", err)
	}
	if err := waitForBitgetResult(t, handlerResult, "subscription from message handler"); err != nil {
		t.Fatalf("Subscribe from message handler: %v", err)
	}
	close(finishServer)
}

func TestPrivateWSHandlerDispatchPreservesOrder(t *testing.T) {
	clientConn, peer := bitgetPrivateWSPair(t)
	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	arg := WSArg{InstType: "UTA", Topic: "order"}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondHandled := make(chan struct{})
	var orderMu sync.Mutex
	var order []int
	client.mu.Lock()
	client.conn = clientConn
	client.authenticated = true
	client.subs[wsKey(arg)] = arg
	client.handlers[wsKey(arg)] = func(payload json.RawMessage) {
		var message struct {
			Data []struct {
				Seq int `json:"seq"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &message); err != nil || len(message.Data) != 1 {
			return
		}
		orderMu.Lock()
		order = append(order, message.Data[0].Seq)
		orderMu.Unlock()
		if message.Data[0].Seq == 1 {
			close(firstEntered)
			<-releaseFirst
		} else {
			close(secondHandled)
		}
	}
	client.mu.Unlock()
	defer client.Close()
	go client.readLoop(clientConn, make(chan error, 1))

	if err := writeBitgetPrivateWSEvent(peer, arg, 1); err != nil {
		t.Fatalf("write first private event: %v", err)
	}
	if err := writeBitgetPrivateWSEvent(peer, arg, 2); err != nil {
		t.Fatalf("write second private event: %v", err)
	}
	waitForBitgetSignal(t, firstEntered, "first ordered handler invocation")
	select {
	case <-secondHandled:
		t.Fatal("second message handler ran concurrently with the first")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	waitForBitgetSignal(t, secondHandled, "second ordered handler invocation")

	orderMu.Lock()
	got := append([]int(nil), order...)
	orderMu.Unlock()
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("handler order = %v, want [1 2]", got)
	}
}

func TestPrivateWSHandlerDispatchPreservesSocketOrderAcrossTopics(t *testing.T) {
	clientConn, peer := bitgetPrivateWSPair(t)
	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	orderArg := WSArg{InstType: "UTA", Topic: "order"}
	fillArg := WSArg{InstType: "UTA", Topic: "fill"}
	orderEntered := make(chan struct{})
	releaseOrder := make(chan struct{})
	fillHandled := make(chan struct{})
	client.mu.Lock()
	client.conn = clientConn
	client.authenticated = true
	client.subs[wsKey(orderArg)] = orderArg
	client.subs[wsKey(fillArg)] = fillArg
	client.handlers[wsKey(orderArg)] = func(json.RawMessage) {
		close(orderEntered)
		<-releaseOrder
	}
	client.handlers[wsKey(fillArg)] = func(json.RawMessage) {
		close(fillHandled)
	}
	client.mu.Unlock()
	defer client.Close()
	go client.readLoop(clientConn, make(chan error, 1))

	if err := writeBitgetPrivateWSEvent(peer, orderArg, 1); err != nil {
		t.Fatalf("write order event: %v", err)
	}
	if err := writeBitgetPrivateWSEvent(peer, fillArg, 2); err != nil {
		t.Fatalf("write fill event: %v", err)
	}
	waitForBitgetSignal(t, orderEntered, "blocking order handler")
	select {
	case <-fillHandled:
		t.Fatal("fill handler overtook earlier order handler from the same socket")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseOrder)
	waitForBitgetSignal(t, fillHandled, "fill handler after earlier order handler")
}

func TestPrivateWSRecoveryCallbacksFenceReplacementData(t *testing.T) {
	var dials atomic.Int32
	finishServer := make(chan struct{})
	firstEntered := make(chan struct{})
	arg := WSArg{InstType: "UTA", Topic: "order"}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := dials.Add(1)
		if !serveBitgetLogin(conn) {
			return
		}
		req := readBitgetSubscription(t, conn)
		if err := writeBitgetSubscribeACK(conn, req, true, "success"); err != nil {
			return
		}
		if err := writeBitgetPrivateWSEvent(conn, arg, int(attempt)); err != nil {
			return
		}
		if attempt == 1 {
			<-firstEntered
			return
		}
		<-finishServer
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	client.subscriptionAckTimeout = time.Second
	defer client.Close()

	releaseFirst := make(chan struct{})
	secondHandled := make(chan struct{})
	var sequenceMu sync.Mutex
	var sequence []string
	record := func(step string) {
		sequenceMu.Lock()
		sequence = append(sequence, step)
		sequenceMu.Unlock()
	}
	client.SetReconnectHooks(func(error) {
		record("started")
	}, func() {
		record("recovered")
	})
	if err := client.Subscribe(context.Background(), arg, func(payload json.RawMessage) {
		if strings.Contains(string(payload), `"seq":1`) {
			record("old")
			close(firstEntered)
			<-releaseFirst
			return
		}
		record("new")
		close(secondHandled)
	}); err != nil {
		t.Fatalf("initial Subscribe: %v", err)
	}
	waitForBitgetSignal(t, firstEntered, "old connection handler")
	waitForPrivateWSReconnectState(t, client, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.waitForRecovery(ctx); err != nil {
		t.Fatalf("internal recovery while old handler is blocked: %v", err)
	}

	sequenceMu.Lock()
	beforeRelease := append([]string(nil), sequence...)
	sequenceMu.Unlock()
	if len(beforeRelease) != 1 || beforeRelease[0] != "old" {
		t.Fatalf("callbacks or replacement data overtook old handler: %v", beforeRelease)
	}
	close(releaseFirst)
	waitForBitgetSignal(t, secondHandled, "replacement data after recovery callbacks")
	sequenceMu.Lock()
	got := append([]string(nil), sequence...)
	sequenceMu.Unlock()
	want := []string{"old", "started", "recovered", "new"}
	if len(got) != len(want) {
		t.Fatalf("recovery boundary sequence = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("recovery boundary sequence = %v, want %v", got, want)
		}
	}
	close(finishServer)
}

func TestPrivateWSRecoveryDrainsQueuedOldDataBeforeLifecycle(t *testing.T) {
	var dials atomic.Int32
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	replacementHandled := make(chan struct{})
	finishServer := make(chan struct{})
	arg := WSArg{InstType: "UTA", Topic: "order"}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := dials.Add(1)
		if !serveBitgetLogin(conn) {
			return
		}
		req := readBitgetSubscription(t, conn)
		if err := writeBitgetSubscribeACK(conn, req, true, "success"); err != nil {
			return
		}
		if attempt == 1 {
			if err := writeBitgetPrivateWSEvent(conn, arg, 1); err != nil {
				return
			}
			if err := writeBitgetPrivateWSEvent(conn, arg, 2); err != nil {
				return
			}
			<-firstEntered
			return
		}
		if err := writeBitgetPrivateWSEvent(conn, arg, 3); err != nil {
			return
		}
		<-finishServer
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	client.subscriptionAckTimeout = time.Second
	defer client.Close()
	defer close(finishServer)

	var sequenceMu sync.Mutex
	var sequence []string
	record := func(step string) {
		sequenceMu.Lock()
		sequence = append(sequence, step)
		sequenceMu.Unlock()
	}
	client.SetReconnectHooks(func(error) {
		record("started")
	}, func() {
		record("recovered")
	})
	if err := client.Subscribe(context.Background(), arg, func(payload json.RawMessage) {
		switch {
		case strings.Contains(string(payload), `"seq":1`):
			record("old1")
			close(firstEntered)
			<-releaseFirst
		case strings.Contains(string(payload), `"seq":2`):
			record("old2")
		case strings.Contains(string(payload), `"seq":3`):
			record("replacement")
			close(replacementHandled)
		}
	}); err != nil {
		t.Fatalf("initial Subscribe: %v", err)
	}

	waitForBitgetSignal(t, firstEntered, "first old callback")
	waitForPrivateWSReconnectState(t, client, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.waitForRecovery(ctx); err != nil {
		t.Fatalf("internal recovery while first old callback is blocked: %v", err)
	}
	sequenceMu.Lock()
	beforeRelease := append([]string(nil), sequence...)
	sequenceMu.Unlock()
	if len(beforeRelease) != 1 || beforeRelease[0] != "old1" {
		t.Fatalf("queued old data, lifecycle, or replacement data overtook first old callback: %v", beforeRelease)
	}

	close(releaseFirst)
	waitForBitgetSignal(t, replacementHandled, "replacement data after queued old data and lifecycle callbacks")
	sequenceMu.Lock()
	got := append([]string(nil), sequence...)
	sequenceMu.Unlock()
	want := []string{"old1", "old2", "started", "recovered", "replacement"}
	if len(got) != len(want) {
		t.Fatalf("recovery boundary sequence = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("recovery boundary sequence = %v, want %v", got, want)
		}
	}
}

func TestPrivateWSNewGapCancelsStaleRecoveredAndBufferedData(t *testing.T) {
	var dials atomic.Int32
	firstEntered := make(chan struct{})
	closeSecond := make(chan struct{})
	finishServer := make(chan struct{})
	arg := WSArg{InstType: "UTA", Topic: "order"}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := int(dials.Add(1))
		if !serveBitgetLogin(conn) {
			return
		}
		req := readBitgetSubscription(t, conn)
		if err := writeBitgetSubscribeACK(conn, req, true, "success"); err != nil {
			return
		}
		if err := writeBitgetPrivateWSEvent(conn, arg, attempt); err != nil {
			return
		}
		switch attempt {
		case 1:
			<-firstEntered
		case 2:
			<-closeSecond
		case 3:
			<-finishServer
		}
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	client.subscriptionAckTimeout = time.Second
	defer client.Close()

	releaseFirst := make(chan struct{})
	recovered := make(chan struct{}, 2)
	thirdHandled := make(chan struct{})
	secondHandled := make(chan struct{}, 1)
	var recoveredCalls atomic.Int32
	client.SetReconnectHooks(func(error) {}, func() {
		recoveredCalls.Add(1)
		recovered <- struct{}{}
	})
	if err := client.Subscribe(context.Background(), arg, func(payload json.RawMessage) {
		switch {
		case strings.Contains(string(payload), `"seq":1`):
			close(firstEntered)
			<-releaseFirst
		case strings.Contains(string(payload), `"seq":2`):
			secondHandled <- struct{}{}
		case strings.Contains(string(payload), `"seq":3`):
			close(thirdHandled)
		}
	}); err != nil {
		t.Fatalf("initial Subscribe: %v", err)
	}
	waitForBitgetSignal(t, firstEntered, "first-generation blocked data")
	waitForPrivateWSReconnectState(t, client, true)
	firstRecovery, cancelFirst := context.WithTimeout(context.Background(), 3*time.Second)
	if err := client.waitForRecovery(firstRecovery); err != nil {
		cancelFirst()
		t.Fatalf("first internal recovery: %v", err)
	}
	cancelFirst()
	close(closeSecond)
	waitForPrivateWSReconnectState(t, client, true)
	client.lifecycle.mu.Lock()
	pendingLifecycle := len(client.lifecycle.queue)
	client.lifecycle.mu.Unlock()
	if pendingLifecycle > 1 {
		t.Fatalf("superseded recovery accumulated %d lifecycle callbacks", pendingLifecycle)
	}
	secondRecovery, cancelSecond := context.WithTimeout(context.Background(), 3*time.Second)
	if err := client.waitForRecovery(secondRecovery); err != nil {
		cancelSecond()
		t.Fatalf("second internal recovery: %v", err)
	}
	cancelSecond()
	if got := recoveredCalls.Load(); got != 0 {
		t.Fatalf("stale recovered callback ran before old handler drained: %d", got)
	}
	close(releaseFirst)
	waitForBitgetSignal(t, recovered, "single final recovered callback")
	waitForBitgetSignal(t, thirdHandled, "latest-generation buffered data")
	if got := recoveredCalls.Load(); got != 1 {
		t.Fatalf("recovered callback count = %d, want 1", got)
	}
	select {
	case <-secondHandled:
		t.Fatal("stale replacement data from superseded recovery was delivered")
	default:
	}
	close(finishServer)
}

func TestPrivateWSCloseDropsQueuedHandlerMessages(t *testing.T) {
	clientConn, peer := bitgetPrivateWSPair(t)
	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	arg := WSArg{InstType: "UTA", Topic: "order"}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstReturned := make(chan struct{})
	secondHandled := make(chan struct{}, 1)
	client.mu.Lock()
	client.conn = clientConn
	client.authenticated = true
	client.subs[wsKey(arg)] = arg
	client.handlers[wsKey(arg)] = func(payload json.RawMessage) {
		if strings.Contains(string(payload), `"seq":1`) {
			close(firstEntered)
			<-releaseFirst
			close(firstReturned)
			return
		}
		secondHandled <- struct{}{}
	}
	client.mu.Unlock()
	go client.readLoop(clientConn, make(chan error, 1))

	if err := writeBitgetPrivateWSEvent(peer, arg, 1); err != nil {
		t.Fatalf("write first private event: %v", err)
	}
	if err := writeBitgetPrivateWSEvent(peer, arg, 2); err != nil {
		t.Fatalf("write second private event: %v", err)
	}
	waitForBitgetSignal(t, firstEntered, "blocking handler before close")
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(releaseFirst)
	waitForBitgetSignal(t, firstReturned, "in-flight handler return after close")
	select {
	case <-secondHandled:
		t.Fatal("queued handler message ran after Close")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPrivateWSHandlerQueueOverflowStartsRecovery(t *testing.T) {
	clientConn, peer := bitgetPrivateWSPair(t)
	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws://127.0.0.1:1"
	arg := WSArg{InstType: "UTA", Topic: "order"}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	started := make(chan error, 1)
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseFirst) }) })
	client.SetReconnectHooks(func(cause error) {
		started <- cause
	}, nil)
	client.mu.Lock()
	client.conn = clientConn
	client.authenticated = true
	client.subs[wsKey(arg)] = arg
	client.handlers[wsKey(arg)] = func(payload json.RawMessage) {
		var message struct {
			Data []struct {
				Seq int `json:"seq"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &message); err == nil && len(message.Data) == 1 && message.Data[0].Seq == 1 {
			close(firstEntered)
			<-releaseFirst
		}
	}
	client.mu.Unlock()
	defer client.Close()
	go client.readLoop(clientConn, make(chan error, 1))

	if err := writeBitgetPrivateWSEvent(peer, arg, 1); err != nil {
		t.Fatalf("write blocking private event: %v", err)
	}
	waitForBitgetSignal(t, firstEntered, "blocking handler before queue overflow")
	for sequence := 2; sequence <= bitgetPrivateWSHandlerQueueLimit+2; sequence++ {
		if err := writeBitgetPrivateWSEvent(peer, arg, sequence); err != nil {
			break
		}
	}
	waitForPrivateWSReconnectState(t, client, true)
	select {
	case cause := <-started:
		t.Fatalf("overflow Started overtook the in-flight handler: %v", cause)
	default:
	}
	releaseOnce.Do(func() { close(releaseFirst) })
	select {
	case cause := <-started:
		if cause == nil || !strings.Contains(cause.Error(), "handler queue overflow") {
			t.Fatalf("overflow reconnect cause = %v", cause)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for explicit reconnect-start hook after handler queue overflow")
	}
}

func waitForPrivateWSReconnectState(t *testing.T, client *PrivateWSClient, want bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		client.mu.RLock()
		got := client.reconnecting
		client.mu.RUnlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("reconnecting state did not become %v", want)
}

func writeBitgetPrivateWSEvent(conn *websocket.Conn, arg WSArg, sequence int) error {
	return conn.WriteJSON(map[string]any{
		"arg":    arg,
		"action": "update",
		"data":   []any{map[string]any{"seq": sequence}},
	})
}
