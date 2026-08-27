package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/realtimewire"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// RealtimeMarketSource owns Trading's single multiplexed connection to
// Gateway. Browser subscriptions create logical topics on this connection, not
// additional Gateway sockets.
type RealtimeMarketSource struct {
	client     *Client
	connectMu  sync.Mutex
	writeMu    sync.Mutex
	mu         sync.Mutex
	connection *websocket.Conn
	heartbeat  context.CancelFunc
	pending    map[string]*marketSubscription
	active     map[string]*marketSubscription
	next       uint64
	closed     bool
	observer   RealtimeMarketObserver
}

type RealtimeMarketObserver interface {
	ObserveMarketMessage(topic realtimewire.Topic, observedAt time.Time)
}

func (source *RealtimeMarketSource) SetObserver(observer RealtimeMarketObserver) {
	if source != nil {
		source.observer = observer
	}
}

type marketSubscription struct {
	request   realtimewire.TopicRequest
	requestID string
	id        string
	updates   chan realtimewire.Message
	ready     chan struct{}
	err       error
	closed    bool
	once      sync.Once
	source    *RealtimeMarketSource
}

func (subscription *marketSubscription) Events() <-chan realtimewire.Message {
	if subscription == nil {
		return nil
	}
	return subscription.updates
}
func (subscription *marketSubscription) Close() {
	if subscription == nil {
		return
	}
	subscription.once.Do(func() { subscription.source.remove(subscription) })
}

func (client *Client) RealtimeMarket() *RealtimeMarketSource {
	if client == nil {
		return nil
	}
	client.realtimeMu.Lock()
	defer client.realtimeMu.Unlock()
	if client.realtime == nil {
		client.realtime = &RealtimeMarketSource{client: client, pending: make(map[string]*marketSubscription), active: make(map[string]*marketSubscription)}
	}
	return client.realtime
}

func (source *RealtimeMarketSource) Subscribe(ctx context.Context, request realtimewire.TopicRequest, buffer int) (realtimewire.Subscription, error) {
	if source == nil || source.client == nil || ctx == nil || ctx.Err() != nil || request.Validate() != nil || request.Topic == realtimewire.TopicAccountEvents || request.Topic == realtimewire.TopicArbitrageOpportunities {
		return nil, fmt.Errorf("Gateway realtime market subscription is invalid")
	}
	if buffer <= 0 {
		buffer = realtimewire.OutboundBuffer
	}
	if err := source.ensureConnected(ctx); err != nil {
		return nil, err
	}
	source.mu.Lock()
	if source.closed {
		source.mu.Unlock()
		return nil, fmt.Errorf("Gateway realtime market source is closed")
	}
	source.next++
	requestID := fmt.Sprintf("trading-market-%d", source.next)
	subscription := &marketSubscription{request: request, requestID: requestID, updates: make(chan realtimewire.Message, buffer), ready: make(chan struct{}), source: source}
	source.pending[requestID] = subscription
	connection := source.connection
	source.mu.Unlock()

	control := realtimewire.Control{Operation: realtimewire.OperationSubscribe, RequestID: requestID, Topics: []realtimewire.TopicRequest{request}}
	source.writeMu.Lock()
	err := wsjson.Write(ctx, connection, control)
	source.writeMu.Unlock()
	if err != nil {
		source.failConnection(connection, fmt.Errorf("write Gateway market subscription"))
		return nil, fmt.Errorf("write Gateway market subscription")
	}
	select {
	case <-ctx.Done():
		subscription.Close()
		return nil, ctx.Err()
	case <-subscription.ready:
		if subscription.err != nil {
			return nil, subscription.err
		}
		return subscription, nil
	}
}

func (source *RealtimeMarketSource) ensureConnected(ctx context.Context) error {
	source.connectMu.Lock()
	defer source.connectMu.Unlock()
	source.mu.Lock()
	if source.closed {
		source.mu.Unlock()
		return fmt.Errorf("Gateway realtime market source is closed")
	}
	if source.connection != nil {
		source.mu.Unlock()
		return nil
	}
	source.mu.Unlock()
	parsed, err := url.Parse(source.client.internalBaseURL)
	if err != nil {
		return fmt.Errorf("Gateway market stream URL is invalid")
	}
	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	} else {
		parsed.Scheme = "ws"
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/internal/v1/market/ws"
	parsed.RawQuery = ""
	headers := http.Header{"Authorization": {"Bearer " + string(source.client.internalToken)}}
	connection, response, err := websocket.Dial(ctx, parsed.String(), &websocket.DialOptions{HTTPClient: source.client.httpClient, HTTPHeader: headers})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		return fmt.Errorf("connect Gateway market stream")
	}
	connection.SetReadLimit(fundingBoardResponseLimit)
	source.mu.Lock()
	if source.closed {
		source.mu.Unlock()
		connection.CloseNow()
		return fmt.Errorf("Gateway realtime market source is closed")
	}
	source.connection = connection
	heartbeatCtx, heartbeatCancel := context.WithCancel(context.Background())
	source.heartbeat = heartbeatCancel
	source.mu.Unlock()
	go source.read(connection)
	go monitorGatewayWebSocket(heartbeatCtx, connection, gatewayWebSocketHeartbeatInterval, gatewayWebSocketHeartbeatTimeout, &source.writeMu)
	return nil
}

func (source *RealtimeMarketSource) read(connection *websocket.Conn) {
	ctx := context.Background()
	for {
		var message realtimewire.Message
		if err := wsjson.Read(ctx, connection, &message); err != nil {
			source.failConnection(connection, fmt.Errorf("read Gateway market stream"))
			return
		}
		if source.observer != nil && (message.Type == realtimewire.MessageSnapshot || message.Type == realtimewire.MessageDelta || message.Type == realtimewire.MessageFull) {
			source.observer.ObserveMarketMessage(message.Topic, message.ObservedAt)
		}
		source.mu.Lock()
		if source.connection != connection {
			source.mu.Unlock()
			return
		}
		if message.Type == realtimewire.MessageAck && message.RequestID != "" {
			subscription := source.pending[message.RequestID]
			if subscription != nil {
				delete(source.pending, message.RequestID)
				subscription.id = message.SubscriptionID
				source.active[message.SubscriptionID] = subscription
				close(subscription.ready)
			}
			source.mu.Unlock()
			continue
		}
		if message.Type == realtimewire.MessageError && message.RequestID != "" {
			subscription := source.pending[message.RequestID]
			if subscription != nil {
				delete(source.pending, message.RequestID)
				subscription.err = fmt.Errorf("Gateway market subscription rejected: %s", message.Code)
				subscription.closed = true
				close(subscription.ready)
				close(subscription.updates)
			}
			source.mu.Unlock()
			continue
		}
		subscription := source.active[message.SubscriptionID]
		if subscription != nil {
			select {
			case subscription.updates <- message:
			default:
				delete(source.active, subscription.id)
				select {
				case subscription.updates <- realtimewire.Message{Type: realtimewire.MessageResyncRequired, SubscriptionID: subscription.id, Topic: subscription.request.Topic, Code: "CONSUMER_LAGGED", Reason: "trading-consumer-lag", ObservedAt: time.Now().UTC()}:
				default:
				}
				subscription.closed = true
				close(subscription.updates)
			}
		}
		source.mu.Unlock()
	}
}

func (source *RealtimeMarketSource) remove(subscription *marketSubscription) {
	if source == nil || subscription == nil {
		return
	}
	source.mu.Lock()
	if subscription.closed {
		source.mu.Unlock()
		return
	}
	if current := source.pending[subscription.requestID]; current == subscription {
		delete(source.pending, subscription.requestID)
		subscription.err = context.Canceled
		close(subscription.ready)
	}
	if current := source.active[subscription.id]; current == subscription {
		delete(source.active, subscription.id)
	}
	connection, id := source.connection, subscription.id
	subscription.closed = true
	close(subscription.updates)
	source.mu.Unlock()
	if connection != nil && id != "" {
		control := realtimewire.Control{Operation: realtimewire.OperationUnsubscribe, RequestID: "unsubscribe-" + id, Topics: []realtimewire.TopicRequest{{SubscriptionID: id, Topic: subscription.request.Topic}}}
		source.writeMu.Lock()
		_ = wsjson.Write(context.Background(), connection, control)
		source.writeMu.Unlock()
	}
}

func (source *RealtimeMarketSource) failConnection(connection *websocket.Conn, reason error) {
	source.mu.Lock()
	if source.connection != connection {
		source.mu.Unlock()
		return
	}
	source.connection = nil
	if source.heartbeat != nil {
		source.heartbeat()
		source.heartbeat = nil
	}
	for requestID, subscription := range source.pending {
		delete(source.pending, requestID)
		subscription.err = reason
		subscription.closed = true
		close(subscription.ready)
		close(subscription.updates)
	}
	for id, subscription := range source.active {
		delete(source.active, id)
		select {
		case subscription.updates <- realtimewire.Message{Type: realtimewire.MessageResyncRequired, SubscriptionID: id, Topic: subscription.request.Topic, Code: "UPSTREAM_DISCONNECTED", Reason: "gateway-market-disconnected", ObservedAt: time.Now().UTC()}:
		default:
		}
		subscription.closed = true
		close(subscription.updates)
	}
	source.mu.Unlock()
	connection.CloseNow()
}

func (source *RealtimeMarketSource) Close() error {
	if source == nil {
		return nil
	}
	source.mu.Lock()
	source.closed = true
	connection := source.connection
	source.mu.Unlock()
	if connection != nil {
		source.failConnection(connection, fmt.Errorf("Gateway realtime market source closed"))
	}
	return nil
}

// RunFundingBoard keeps the Trading projection API while sourcing it from the
// shared internal market connection.
func (source *RealtimeMarketSource) RunFundingBoard(ctx context.Context, replace func(marketdata.BoardSnapshot) error) error {
	if source == nil || ctx == nil || replace == nil {
		return fmt.Errorf("Gateway funding-board stream is unavailable")
	}
	backoff := time.Second
	for ctx.Err() == nil {
		subscription, err := source.Subscribe(ctx, realtimewire.TopicRequest{Topic: realtimewire.TopicFundingBoard}, realtimewire.OutboundBuffer)
		if err == nil {
			for message := range subscription.Events() {
				if message.Type == realtimewire.MessageResyncRequired {
					err = fmt.Errorf("Gateway funding-board resnapshot required")
					break
				}
				if message.Type != realtimewire.MessageSnapshot && message.Type != realtimewire.MessageDelta {
					continue
				}
				var snapshot marketdata.BoardSnapshot
				if decodeErr := json.Unmarshal(message.Data, &snapshot); decodeErr != nil {
					err = fmt.Errorf("Gateway funding-board payload is invalid")
					break
				}
				if replaceErr := replace(snapshot); replaceErr != nil {
					err = replaceErr
					break
				}
			}
			subscription.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if err == nil {
			backoff = time.Second
		} else if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
	return ctx.Err()
}
