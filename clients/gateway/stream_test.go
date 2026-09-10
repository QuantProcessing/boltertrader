package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func streamData(topic wire.Topic, subscription string, generation, sequence uint64, kind wire.MessageType) wire.Message {
	data := json.RawMessage(`{"bid":{"price":"100.000000000000001","quantity":"1"},"ask":{"price":"101","quantity":"2"}}`)
	if topic == OrderBook {
		data = json.RawMessage(`{"bids":[{"price":"100","quantity":"1"}],"asks":[{"price":"101","quantity":"2"}]}`)
	}
	return wire.Message{Type: kind, SubscriptionID: subscription, Topic: topic, Generation: generation, Sequence: sequence, ObservedAt: time.Now().UTC(), Data: data}
}
func acceptMarket(t *testing.T, w http.ResponseWriter, r *http.Request) (*websocket.Conn, wire.Control) {
	t.Helper()
	if r.URL.Path != "/v1/market/ws" || r.Header.Get("Authorization") != "Bearer "+testToken {
		t.Error("market path or authentication lost")
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		t.Error(err)
		return nil, wire.Control{}
	}
	var control wire.Control
	if err := wsjson.Read(r.Context(), conn, &control); err != nil {
		t.Error(err)
		conn.CloseNow()
		return nil, wire.Control{}
	}
	if control.Operation != wire.OperationSubscribe || len(control.Topics) != 1 {
		t.Error("invalid subscribe")
	}
	return conn, control
}
func readEvent(t *testing.T, stream *MarketStream) MarketEvent {
	t.Helper()
	select {
	case event, ok := <-stream.Events():
		if !ok {
			t.Fatalf("stream closed: %v", stream.Err())
		}
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("market event timeout")
		return MarketEvent{}
	}
}
func TestMarketReconnectRequiresSnapshotAndPreservesGap(t *testing.T) {
	var sessions atomic.Int64
	closed := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := sessions.Add(1)
		conn, control := acceptMarket(t, w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		defer func() { closed <- struct{}{} }()
		id := "market-" + strconv.FormatInt(n, 10)
		wsjson.Write(r.Context(), conn, wire.Message{Type: wire.MessageAck, RequestID: control.RequestID, SubscriptionID: id, Topic: BestBidOffer})
		wsjson.Write(r.Context(), conn, streamData(BestBidOffer, id, uint64(n), 10, wire.MessageSnapshot))
		if n == 1 {
			return
		}
		wsjson.Write(r.Context(), conn, streamData(BestBidOffer, id, uint64(n), 11, wire.MessageDelta))
		// A duplicate must not be exposed twice.
		wsjson.Write(r.Context(), conn, streamData(BestBidOffer, id, uint64(n), 11, wire.MessageDelta))
		wsjson.Write(r.Context(), conn, streamData(BestBidOffer, id, uint64(n), 12, wire.MessageDelta))
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	stream, err := clientAt(t, server).SubscribeMarkets(t.Context(), []MarketTopic{{Topic: BestBidOffer, InstrumentID: testInstrument}}, StreamOptions{ReconnectMin: time.Millisecond, ReconnectMax: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	first := readEvent(t, stream)
	gap := readEvent(t, stream)
	next := readEvent(t, stream)
	delta := readEvent(t, stream)
	last := readEvent(t, stream)
	if first.Message.Type != wire.MessageSnapshot || gap.Message.Type != wire.MessageResyncRequired || next.Message.Type != wire.MessageSnapshot || next.Message.Generation != 2 || delta.Message.Sequence != 11 || last.Message.Sequence != 12 {
		t.Fatalf("bad reconnect sequence: %+v %+v %+v %+v %+v", first, gap, next, delta, last)
	}
	quote, err := DecodeBBO(first.Message)
	if err != nil || quote.Bid.Price != "100.000000000000001" {
		t.Fatal("decimal quote changed")
	}
	stream.Close()
	for i := 0; i < 2; i++ {
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("server subscription not closed")
		}
	}
}
func TestMarketDepthSequenceGapNeverReachesConsumerAsData(t *testing.T) {
	var sessions atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions.Add(1)
		conn, control := acceptMarket(t, w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		wsjson.Write(r.Context(), conn, wire.Message{Type: wire.MessageAck, RequestID: control.RequestID, SubscriptionID: "one", Topic: OrderBook})
		wsjson.Write(r.Context(), conn, streamData(OrderBook, "one", 1, 1, wire.MessageSnapshot))
		wsjson.Write(r.Context(), conn, streamData(OrderBook, "one", 1, 3, wire.MessageDelta))
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	stream, err := clientAt(t, server).SubscribeMarkets(t.Context(), []MarketTopic{{Topic: OrderBook, InstrumentID: testInstrument, Depth: 10}}, StreamOptions{ReconnectMin: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if first := readEvent(t, stream); first.Message.Sequence != 1 {
		t.Fatal(first)
	}
	if second := readEvent(t, stream); second.Message.Type != wire.MessageResyncRequired {
		t.Fatal("sequence gap accepted as valid update")
	}
	stream.Close()
	if sessions.Load() != 1 {
		t.Fatal("unexpected unbounded reconnect")
	}
}
func TestMarketProtocolMismatchAndSlowConsumerCloseWithError(t *testing.T) {
	for _, scenario := range []string{"wrong_subscription", "wrong_topic", "bad_price", "slow_consumer"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, control := acceptMarket(t, w, r)
				if conn == nil {
					return
				}
				defer conn.CloseNow()
				wsjson.Write(r.Context(), conn, wire.Message{Type: wire.MessageAck, RequestID: control.RequestID, SubscriptionID: "one", Topic: BestBidOffer})
				message := streamData(BestBidOffer, "one", 1, 1, wire.MessageSnapshot)
				switch scenario {
				case "wrong_subscription":
					message.SubscriptionID = "unknown"
				case "wrong_topic":
					message.Topic = MarkPrice
				case "bad_price":
					message.Data = []byte(`{"bid":{"price":"NaN","quantity":"1"},"ask":{"price":"101","quantity":"1"}}`)
				}
				wsjson.Write(r.Context(), conn, message)
				if scenario == "slow_consumer" {
					for i := uint64(2); i < 10; i++ {
						if wsjson.Write(r.Context(), conn, streamData(BestBidOffer, "one", 1, i, wire.MessageDelta)) != nil {
							break
						}
					}
				}
				for {
					if _, _, err := conn.Read(r.Context()); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			stream, err := clientAt(t, server).SubscribeMarkets(t.Context(), []MarketTopic{{Topic: BestBidOffer, InstrumentID: testInstrument}}, StreamOptions{Buffer: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			select {
			case <-stream.done:
			case <-time.After(3 * time.Second):
				t.Fatal("failed stream did not terminate")
			}
			if scenario == "slow_consumer" {
				if stream.Err() != ErrConsumerLagged {
					t.Fatalf("err=%v", stream.Err())
				}
			} else {
				errorKind(t, stream.Err(), "protocol")
			}
		})
	}
}
func TestMarketUpstreamGapWaitsForReplacementSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, control := acceptMarket(t, w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		wsjson.Write(r.Context(), conn, wire.Message{Type: wire.MessageAck, RequestID: control.RequestID, SubscriptionID: "one", Topic: BestBidOffer})
		wsjson.Write(r.Context(), conn, streamData(BestBidOffer, "one", 1, 1, wire.MessageSnapshot))
		wsjson.Write(r.Context(), conn, wire.Message{Type: wire.MessageResyncRequired, SubscriptionID: "one", Topic: BestBidOffer, Code: "UPSTREAM_GAP"})
		wsjson.Write(r.Context(), conn, streamData(BestBidOffer, "one", 2, 1, wire.MessageSnapshot))
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	stream, err := clientAt(t, server).SubscribeMarkets(t.Context(), []MarketTopic{{Topic: BestBidOffer, InstrumentID: testInstrument}}, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	readEvent(t, stream)
	gap := readEvent(t, stream)
	snapshot := readEvent(t, stream)
	if gap.Message.Type != wire.MessageResyncRequired || snapshot.Message.Type != wire.MessageSnapshot || snapshot.Message.Generation != 2 {
		t.Fatal("upstream gap not reflected")
	}
}
func TestMarketHeartbeatDetectsSilentPeer(t *testing.T) {
	stopPeer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, control := acceptMarket(t, w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		wsjson.Write(r.Context(), conn, wire.Message{Type: wire.MessageAck, RequestID: control.RequestID, SubscriptionID: "one", Topic: BestBidOffer})
		wsjson.Write(r.Context(), conn, streamData(BestBidOffer, "one", 1, 1, wire.MessageSnapshot))
		// Never read the socket again, so RFC pings receive no pong.
		<-stopPeer
	}))
	defer server.Close()
	defer close(stopPeer)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := clientAt(t, server).SubscribeMarkets(ctx, []MarketTopic{{Topic: BestBidOffer, InstrumentID: testInstrument}}, StreamOptions{HeartbeatInterval: 10 * time.Millisecond, HeartbeatTimeout: 10 * time.Millisecond, ReconnectMin: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	readEvent(t, stream)
	if event := readEvent(t, stream); event.Message.Type != wire.MessageResyncRequired {
		t.Fatal("heartbeat failed to publish gap")
	}
}

func TestMarketStartupIsBoundedEvenWhenPeerPongs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := acceptMarket(t, w, r)
		if conn == nil {
			return
		}
		defer conn.CloseNow()
		// Process RFC pings, but never acknowledge or provide market data.
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	consumer, err := New(Config{URL: server.URL, Token: testToken, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := consumer.SubscribeMarkets(t.Context(), []MarketTopic{{Topic: BestBidOffer, InstrumentID: testInstrument}}, StreamOptions{HeartbeatInterval: 5 * time.Millisecond, HeartbeatTimeout: 20 * time.Millisecond, ReconnectMin: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if event := readEvent(t, stream); event.Message.Type != wire.MessageResyncRequired {
		t.Fatal("startup failure was not exposed")
	}
}
