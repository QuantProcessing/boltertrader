package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// ordersStream delivers persisted Trading identities and their fill evidence.
// Gateway venue events never acquire product ownership in the browser.
func (handler *tradingHandler) ordersStream(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok {
		return
	}
	if len(r.URL.Query()) != 0 {
		tradingError(w, 400, "VALIDATION_ERROR")
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(wire.MaxControlBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	controls := make(chan wire.Control, 1)
	go func() {
		defer cancel()
		for {
			var control wire.Control
			if wsjson.Read(ctx, c, &control) != nil {
				return
			}
			select {
			case controls <- control:
			case <-ctx.Done():
				return
			}
		}
	}()
	changes, unsubscribe := handler.options.Commands.SubscribeChanges(principal)
	defer unsubscribe()
	repair := time.NewTicker(30 * time.Second)
	defer repair.Stop()
	active := ""
	sequence := uint64(0)
	next := uint64(0)
	send := func(m wire.Message) error {
		writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		return wsjson.Write(writeCtx, c, m)
	}
	snapshot := func() error {
		items, e := handler.options.Commands.ListOwned(ctx, principal, "manual")
		if e != nil {
			return e
		}
		fills := make([]FillProjection, 0)
		for _, command := range items {
			for _, fill := range command.ExecutionEvidence.Fills {
				fills = append(fills, fillProjection(command, fill))
			}
		}
		payload := map[string]any{"orders": items, "fills": fills}
		if entries, err := handler.options.Ledger.List(ctx, ledger.Query{PrincipalID: principal}); err == nil {
			if positions, err := handler.options.Portfolio.ListContext(ctx, principal, "", ""); err == nil {
				payload["ledger"] = projectLedgerEntries(entries, positions)
			}
		}
		if reader, ok := handler.options.Activity.(ActivityProjectionReader); ok {
			if events, err := reader.ListActivity(ctx, principal, ActivityQuery{}); err == nil {
				payload["activity"] = events
			}
		}
		sequence++
		return send(wire.Message{Type: wire.MessageSnapshot, SubscriptionID: active, Topic: wire.TopicTradingOrders, Generation: 1, Sequence: sequence, ObservedAt: time.Now().UTC(), Data: wire.EncodeData(payload)})
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			if active != "" && snapshot() != nil {
				return
			}
		case <-repair.C:
			if active != "" && snapshot() != nil {
				return
			}
		case control := <-controls:
			if control.Validate() != nil {
				_ = c.Close(websocket.StatusPolicyViolation, "invalid_control")
				return
			}
			if control.Operation == wire.OperationPing {
				if send(wire.Message{Type: wire.MessagePong, RequestID: control.RequestID}) != nil {
					return
				}
				continue
			}
			for _, topic := range control.Topics {
				if control.Operation == wire.OperationUnsubscribe {
					if topic.SubscriptionID == active {
						active = ""
					}
					if send(wire.Message{Type: wire.MessageAck, RequestID: control.RequestID, SubscriptionID: topic.SubscriptionID, Topic: topic.Topic}) != nil {
						return
					}
					continue
				}
				if topic.Topic != wire.TopicTradingOrders || active != "" {
					if send(wire.Message{Type: wire.MessageError, RequestID: control.RequestID, Topic: topic.Topic, Code: "TOPIC_UNSUPPORTED"}) != nil {
						return
					}
					continue
				}
				next++
				active = fmt.Sprintf("orders-%d", next)
				sequence = 0
				if send(wire.Message{Type: wire.MessageAck, RequestID: control.RequestID, SubscriptionID: active, Topic: topic.Topic}) != nil {
					return
				}
				if snapshot() != nil {
					return
				}
			}
		}
	}
}
