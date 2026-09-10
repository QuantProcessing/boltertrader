package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type MarketEvent struct {
	Topic   MarketTopic  `json:"subscription"`
	Message wire.Message `json:"message"`
}
type StreamOptions struct {
	Buffer                              int
	ReconnectMin, ReconnectMax          time.Duration
	HeartbeatInterval, HeartbeatTimeout time.Duration
}
type MarketStream struct {
	events chan MarketEvent
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

func (s *MarketStream) Events() <-chan MarketEvent { return s.events }
func (s *MarketStream) Close() {
	s.cancel()
	<-s.done
}
func (s *MarketStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

var ErrConsumerLagged = errors.New("Gateway market consumer lagged; subscription closed")
var errReconnect = errors.New("Gateway market reconnect required")

// SubscribeMarkets maintains one socket for a fixed set of topics. It emits a
// gap for every topic on disconnect, resubscribes with bounded backoff, and
// accepts data only after a fresh snapshot. Close/cancellation releases all
// server subscriptions by closing the socket. No history is replayed.
func (c *Client) SubscribeMarkets(ctx context.Context, topics []MarketTopic, options StreamOptions) (*MarketStream, error) {
	if len(topics) == 0 || len(topics) > wire.MaxActiveTopics {
		return nil, invalid("subscription count")
	}
	seen := map[MarketTopic]bool{}
	for _, topic := range topics {
		if err := topic.validate(); err != nil {
			return nil, err
		}
		if seen[topic] {
			return nil, invalid("duplicate subscription")
		}
		seen[topic] = true
	}
	if options.Buffer < 0 || options.Buffer > 4096 || options.ReconnectMin < 0 || options.ReconnectMax < 0 || options.HeartbeatInterval < 0 || options.HeartbeatTimeout < 0 {
		return nil, invalid("stream options")
	}
	if options.Buffer == 0 {
		options.Buffer = 256
	}
	if options.ReconnectMin == 0 {
		options.ReconnectMin = time.Second
	}
	if options.ReconnectMax == 0 {
		options.ReconnectMax = 30 * time.Second
	}
	if options.ReconnectMax < options.ReconnectMin {
		return nil, invalid("reconnect range")
	}
	if options.HeartbeatInterval == 0 {
		options.HeartbeatInterval = 20 * time.Second
	}
	if options.HeartbeatTimeout == 0 {
		options.HeartbeatTimeout = 10 * time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	stream := &MarketStream{events: make(chan MarketEvent, options.Buffer), cancel: cancel, done: make(chan struct{})}
	topics = append([]MarketTopic(nil), topics...)
	go func() {
		defer close(stream.done)
		defer close(stream.events)
		defer cancel()
		delay := options.ReconnectMin
		for ctx.Err() == nil {
			healthy, err := c.marketSession(ctx, stream, topics, options)
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, ErrConsumerLagged) {
				stream.fail(err)
				return
			}
			var clientErr *Error
			if errors.As(err, &clientErr) && (clientErr.Kind == "protocol" || (clientErr.Kind == "http" && clientErr.StatusCode >= 400 && clientErr.StatusCode < 500 && clientErr.StatusCode != 429)) {
				stream.fail(err)
				return
			}
			for _, topic := range topics {
				if !stream.emit(MarketEvent{Topic: topic, Message: wire.Message{Type: wire.MessageResyncRequired, Topic: topic.Topic, ObservedAt: time.Now().UTC(), Code: "CONNECTION_LOST", Reason: "reconnect-requires-snapshot"}}) {
					stream.fail(ErrConsumerLagged)
					return
				}
			}
			if healthy {
				delay = options.ReconnectMin
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if delay < options.ReconnectMax/2 {
				delay *= 2
			} else {
				delay = options.ReconnectMax
			}
		}
	}()
	return stream, nil
}
func (s *MarketStream) fail(err error) { s.mu.Lock(); s.err = err; s.mu.Unlock() }
func (s *MarketStream) emit(event MarketEvent) bool {
	select {
	case s.events <- event:
		return true
	default:
		return false
	}
}

type subscriptionState struct {
	topic                MarketTopic
	ready                bool
	generation, sequence uint64
}

func (c *Client) marketSession(ctx context.Context, stream *MarketStream, topics []MarketTopic, options StreamOptions) (bool, error) {
	address := strings.Replace(c.origin, "http", "ws", 1) + "/v1/market/ws"
	// HTTPClient.Timeout also bounds upgraded connections in some transports.
	// The dial context sets the handshake deadline; the stream context owns life.
	transport := *c.http
	transport.Timeout = 0
	dialCtx, cancelDial := context.WithTimeout(ctx, c.timeout)
	conn, response, err := websocket.Dial(dialCtx, address, &websocket.DialOptions{HTTPClient: &transport, HTTPHeader: http.Header{"Authorization": {"Bearer " + c.token}}})
	cancelDial()
	if err != nil {
		if response != nil {
			if response.Body != nil {
				response.Body.Close()
			}
			return false, &Error{Kind: "http", StatusCode: response.StatusCode, Outcome: NotAttempted}
		}
		return false, transportError(err)
	}
	defer conn.CloseNow()
	// A peer that pongs but never acknowledges/snapshots must not hang startup.
	startup := time.AfterFunc(c.timeout, func() { _ = conn.CloseNow() })
	defer startup.Stop()
	conn.SetReadLimit(c.limit)
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(options.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-ticker.C:
				pingCtx, done := context.WithTimeout(sessionCtx, options.HeartbeatTimeout)
				err := conn.Ping(pingCtx)
				done()
				if err != nil {
					conn.CloseNow()
					return
				}
			}
		}
	}()
	defer func() { cancel(); conn.CloseNow(); <-heartbeatDone }()
	pending := map[string]MarketTopic{}
	for i, topic := range topics {
		id := strconv.Itoa(i + 1)
		pending[id] = topic
		writeCtx, done := context.WithTimeout(sessionCtx, c.timeout)
		err = wsjson.Write(writeCtx, conn, wire.Control{Operation: wire.OperationSubscribe, RequestID: id, Topics: []wire.TopicRequest{topic.request()}})
		done()
		if err != nil {
			return false, transportError(err)
		}
	}
	active := map[string]*subscriptionState{}
	healthy := false
	for {
		_, raw, err := conn.Read(sessionCtx)
		if err != nil {
			return healthy, transportError(err)
		}
		var message wire.Message
		if decode(raw, &message) != nil {
			return healthy, protocol("market message")
		}
		if message.Type == wire.MessagePong {
			continue
		}
		if message.Type == wire.MessageAck {
			topic, ok := pending[message.RequestID]
			if !ok || !validID(message.SubscriptionID) || message.Topic != topic.Topic || active[message.SubscriptionID] != nil {
				return healthy, protocol("subscription acknowledgement")
			}
			delete(pending, message.RequestID)
			active[message.SubscriptionID] = &subscriptionState{topic: topic}
			continue
		}
		if message.Type == wire.MessageError {
			return healthy, &Error{Kind: "protocol", Code: message.Code, Message: "market subscription rejected"}
		}
		state := active[message.SubscriptionID]
		if state == nil || message.Topic != state.topic.Topic {
			return healthy, protocol("market subscription scope")
		}
		if message.Type == wire.MessageResyncRequired {
			state.ready = false
			if !stream.emit(MarketEvent{Topic: state.topic, Message: message}) {
				return healthy, ErrConsumerLagged
			}
			if message.Code == "UPSTREAM_UNAVAILABLE" {
				return healthy, errReconnect
			}
			continue
		}
		switch message.Type {
		case wire.MessageSnapshot, wire.MessageDelta, wire.MessageFull:
		default:
			return healthy, protocol("market message type")
		}
		if !validMarketMessage(state.topic, message) {
			return healthy, protocol("market data")
		}
		if message.Generation < state.generation || (!state.ready && state.generation != 0 && message.Type == wire.MessageSnapshot && message.Generation <= state.generation) {
			return healthy, errReconnect
		}
		if message.Type != wire.MessageSnapshot {
			if !state.ready || message.Generation != state.generation {
				return healthy, errReconnect
			}
			if message.Sequence <= state.sequence {
				continue
			}
			// Gateway deliberately coalesces high-frequency BBO fanout. Each
			// BBO update replaces the complete quote, so an increasing sequence
			// can skip native observations without losing reconstruction state.
			// Book deltas and other non-coalesced topics still require continuity.
			if message.Sequence != state.sequence+1 && state.topic.Topic != BestBidOffer {
				return healthy, errReconnect
			}
		} else if state.ready && message.Generation == state.generation && message.Sequence <= state.sequence {
			continue
		}
		state.ready = true
		state.generation = message.Generation
		state.sequence = message.Sequence
		healthy = true
		allReady := len(pending) == 0
		for _, subscription := range active {
			allReady = allReady && subscription.ready
		}
		if allReady {
			startup.Stop()
		}
		if !stream.emit(MarketEvent{Topic: state.topic, Message: message}) {
			return healthy, ErrConsumerLagged
		}
	}
}
func (s *MarketStream) String() string {
	return fmt.Sprintf("Gateway market stream (buffered=%d)", len(s.events))
}
