package proxy

import (
	"context"
	"net/http"
	"strings"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/internal/httpx"
	"github.com/QuantProcessing/boltertrader/services/authedge/session"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

var realtimeUpstreams = map[string]Route{
	"market":  {Method: http.MethodGet, Path: "/internal/v1/market/ws", Upstream: Gateway},
	"account": {Method: http.MethodGet, Path: "/internal/v1/account/ws", Upstream: Gateway},
	"trading": {Method: http.MethodGet, Path: "/api/v1/trading/realtime/ws", Upstream: Trading},
}

// realtime multiplexes service-owned subscriptions onto one browser transport.
// It translates only subscription identities, never trading or market facts.
func (proxy *Proxy) realtime(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) != 0 {
		httpx.WriteError(w, 400, "VALIDATION_ERROR", "query is not supported", nil, false, httpx.RequestID(r))
		return
	}
	var principal session.Principal
	if hasSessionCookie(r) {
		var ok bool
		principal, ok = proxy.authorize(w, r)
		if !ok {
			return
		}
	}
	browser, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer browser.CloseNow()
	browser.SetReadLimit(wire.MaxControlBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	type incoming struct {
		owner   string
		message wire.Message
		err     error
	}
	incomingMessages := make(chan incoming, wire.OutboundBuffer)
	controls := make(chan wire.Control, 1)
	go func() {
		defer cancel()
		for {
			var c wire.Control
			if wsjson.Read(ctx, browser, &c) != nil {
				return
			}
			select {
			case controls <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	upstreams := map[string]*websocket.Conn{}
	defer func() {
		for _, c := range upstreams {
			c.CloseNow()
		}
	}()
	send := func(m wire.Message) error {
		writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		return wsjson.Write(writeCtx, browser, m)
	}
	dial := func(owner string) (*websocket.Conn, error) {
		if c := upstreams[owner]; c != nil {
			return c, nil
		}
		route := realtimeUpstreams[owner]
		outbound, ok := proxy.build(w, r, route, proxy.upstreams[route.Upstream], principal)
		if !ok {
			return nil, context.Canceled
		}
		target := *outbound.URL
		if target.Scheme == "https" {
			target.Scheme = "wss"
		} else {
			target.Scheme = "ws"
		}
		dialCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		c, response, err := websocket.Dial(dialCtx, target.String(), &websocket.DialOptions{HTTPClient: proxy.client, HTTPHeader: outbound.Header})
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if err != nil {
			return nil, err
		}
		c.SetReadLimit(16 << 20)
		upstreams[owner] = c
		go func() {
			for {
				var m wire.Message
				err := wsjson.Read(ctx, c, &m)
				select {
				case incomingMessages <- incoming{owner, m, err}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
		return c, nil
	}
	// Membership is tracked independently of the caller-supplied topic on cancel.
	active := map[string]string{}
	pending := map[string]int{}
	sessionCheck := time.NewTicker(30 * time.Second)
	defer sessionCheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sessionCheck.C:
			if principal.ID != "" {
				cookie, _ := r.Cookie(session.CookieName)
				result, e := proxy.sessions.Current(ctx, cookie.Value)
				if e != nil || result.Principal.ID != principal.ID {
					_ = browser.Close(websocket.StatusPolicyViolation, "session_expired")
					return
				}
			}
			// The browser heartbeat proves only browser-to-edge health. Probe
			// the idle private upstream too, so a half-open bridge cannot stay
			// apparently online while account changes stop arriving.
			for _, upstream := range upstreams {
				pingCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				err := upstream.Ping(pingCtx)
				stop()
				if err != nil {
					_ = browser.Close(websocket.StatusTryAgainLater, "upstream_disconnected")
					return
				}
			}
		case result := <-incomingMessages:
			if result.err != nil {
				_ = browser.Close(websocket.StatusTryAgainLater, "upstream_disconnected")
				return
			}
			m := result.message
			if m.SubscriptionID != "" {
				m.SubscriptionID = result.owner + ":" + m.SubscriptionID
			}
			pendingKey := result.owner + ":" + m.RequestID
			if m.Type == wire.MessageAck && pending[pendingKey] > 0 {
				active[m.SubscriptionID] = result.owner
				pending[pendingKey]--
				if pending[pendingKey] == 0 {
					delete(pending, pendingKey)
				}
			}
			if m.Type == wire.MessageError && m.RequestID != "" {
				pending[pendingKey]--
				if pending[pendingKey] <= 0 {
					delete(pending, pendingKey)
				}
			}
			if send(m) != nil {
				return
			}
		case c := <-controls:
			if c.Validate() != nil {
				_ = browser.Close(websocket.StatusPolicyViolation, "invalid_control")
				return
			}
			if c.Operation == wire.OperationPing {
				if send(wire.Message{Type: wire.MessagePong, RequestID: c.RequestID, ObservedAt: time.Now().UTC()}) != nil {
					return
				}
				continue
			}
			for _, topic := range c.Topics {
				owner := "market"
				if topic.Topic == wire.TopicAccountEvents {
					owner = "account"
				}
				if topic.Topic == wire.TopicTradingOrders {
					owner = "trading"
				}
				if c.Operation == wire.OperationUnsubscribe {
					var found bool
					owner, found = active[topic.SubscriptionID]
					if !found {
						continue
					}
					delete(active, topic.SubscriptionID)
					topic.SubscriptionID = strings.TrimPrefix(topic.SubscriptionID, owner+":")
				}
				if owner != "market" && principal.ID == "" {
					if send(wire.Message{Type: wire.MessageError, RequestID: c.RequestID, Topic: topic.Topic, Code: "AUTH_REQUIRED"}) != nil {
						return
					}
					continue
				}
				count := len(active)
				for _, n := range pending {
					count += n
				}
				if c.Operation == wire.OperationSubscribe && count >= wire.MaxInternalActiveTopics {
					if send(wire.Message{Type: wire.MessageError, RequestID: c.RequestID, Topic: topic.Topic, Code: "TOPIC_LIMIT"}) != nil {
						return
					}
					continue
				}
				topic.InternalPurpose = "browser"
				upstream, e := dial(owner)
				if e != nil {
					if send(wire.Message{Type: wire.MessageError, RequestID: c.RequestID, Topic: topic.Topic, Code: "UPSTREAM_UNAVAILABLE"}) != nil {
						return
					}
					continue
				}
				if c.Operation == wire.OperationSubscribe {
					pending[owner+":"+c.RequestID]++
				}
				writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				e = wsjson.Write(writeCtx, upstream, wire.Control{Operation: c.Operation, RequestID: c.RequestID, Topics: []wire.TopicRequest{topic}})
				stop()
				if e != nil {
					return
				}
			}
		}
	}
}
