package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

var (
	ErrClosed       = errors.New("event hub closed")
	ErrSlowConsumer = errors.New("event subscriber too slow")
)

type Kind string

const (
	KindOrder             Kind = "order"
	KindFill              Kind = "fill"
	KindBalance           Kind = "balance"
	KindPosition          Kind = "position"
	KindAccountConnection Kind = "account_connection"
)

// Event is a live account change notification. EventID deduplicates repeated
// delivery; ResourceVersion and OccurredAt let consumers merge the event with
// periodic REST snapshots without depending on a process-local replay cursor.
type Event struct {
	EventID         domain.EventID         `json:"event_id"`
	AccountID       domain.AccountID       `json:"account_id"`
	ResourceVersion domain.ResourceVersion `json:"resource_version"`
	Kind            Kind                   `json:"kind"`
	OccurredAt      time.Time              `json:"occurred_at"`
	Payload         json.RawMessage        `json:"payload"`
	PrincipalID     domain.PrincipalID     `json:"-"`
}

type AccountConnectionState string

const (
	AccountConnected    AccountConnectionState = "connected"
	AccountDisconnected AccountConnectionState = "disconnected"
)

type AccountConnectionPayload struct {
	AccountID domain.AccountID       `json:"account_id"`
	RouteID   domain.RouteID         `json:"route_id,omitempty"`
	State     AccountConnectionState `json:"state"`
	Reason    string                 `json:"reason,omitempty"`
}

type OrderPayload struct {
	CommandID       domain.CommandID       `json:"command_id"`
	GatewayOrderID  domain.GatewayOrderID  `json:"gateway_order_id"`
	ClientOrderID   string                 `json:"client_order_id"`
	AccountID       domain.AccountID       `json:"account_id"`
	InstrumentID    domain.InstrumentID    `json:"instrument_id"`
	State           domain.OrderState      `json:"state"`
	ResourceVersion domain.ResourceVersion `json:"resource_version"`
	VenueOrderID    string                 `json:"-"`
}

type PublishRequest struct {
	PrincipalID     domain.PrincipalID
	AccountID       domain.AccountID
	EventID         domain.EventID
	ResourceVersion domain.ResourceVersion
	Kind            Kind
	OccurredAt      time.Time
	Payload         json.RawMessage
}

func (request PublishRequest) validate() error {
	if err := request.PrincipalID.Validate(); err != nil {
		return err
	}
	if err := request.AccountID.Validate(); err != nil {
		return err
	}
	if err := request.EventID.Validate(); err != nil {
		return err
	}
	if err := request.ResourceVersion.Validate(); err != nil {
		return err
	}
	if !request.Kind.valid() {
		return fmt.Errorf("event kind is invalid")
	}
	if len(request.Payload) == 0 || !json.Valid(request.Payload) {
		return fmt.Errorf("event payload is invalid")
	}
	return nil
}

func (kind Kind) valid() bool {
	switch kind {
	case KindOrder, KindFill, KindBalance, KindPosition, KindAccountConnection:
		return true
	default:
		return false
	}
}

type Options struct {
	SubscriberBuffer int
	Identity         *domain.IdentityGenerator
	Now              func() time.Time
}

// Hub fans out live account notifications. It deliberately keeps no replay
// log: durable state lives behind REST resources and is reconciled on a fixed
// cadence, while this hub only reduces the time to observe a change.
type Hub struct {
	mu               sync.Mutex
	subscriberBuffer int
	identity         *domain.IdentityGenerator
	now              func() time.Time
	subscribers      map[uint64]*subscriber
	connections      map[accountConnectionKey]Event
	nextSubscriber   uint64
	closed           bool
}

type accountConnectionKey struct {
	principal domain.PrincipalID
	account   domain.AccountID
	route     domain.RouteID
}

type subscriber struct {
	id        uint64
	principal domain.PrincipalID
	account   domain.AccountID
	kinds     map[Kind]struct{}
	events    chan Event
	errors    chan error
}

func NewHub(options Options) (*Hub, error) {
	if options.Identity == nil {
		return nil, fmt.Errorf("event identity generator is required")
	}
	if options.SubscriberBuffer <= 0 {
		options.SubscriberBuffer = 64
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Hub{
		subscriberBuffer: options.SubscriberBuffer,
		identity:         options.Identity,
		now:              options.Now,
		subscribers:      make(map[uint64]*subscriber),
		connections:      make(map[accountConnectionKey]Event),
	}, nil
}

func (hub *Hub) Publish(request PublishRequest) (Event, error) {
	if hub == nil {
		return Event{}, ErrClosed
	}
	if err := request.validate(); err != nil {
		return Event{}, err
	}
	if request.OccurredAt.IsZero() {
		request.OccurredAt = hub.now().UTC()
	}
	event := Event{
		EventID: request.EventID, AccountID: request.AccountID,
		ResourceVersion: request.ResourceVersion, Kind: request.Kind,
		OccurredAt: request.OccurredAt.UTC(), Payload: append(json.RawMessage(nil), request.Payload...),
		PrincipalID: request.PrincipalID,
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return Event{}, ErrClosed
	}
	if event.Kind == KindAccountConnection {
		if err := hub.updateConnectionLocked(event); err != nil {
			return Event{}, err
		}
	}
	hub.publishLocked(event)
	return event, nil
}

func (hub *Hub) PublishCommitted(ctx context.Context, command store.StoredCommand) error {
	if ctx == nil {
		return fmt.Errorf("publish committed context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(OrderPayload{
		CommandID: command.CommandID, GatewayOrderID: command.GatewayOrderID,
		ClientOrderID: command.ClientOrderID, AccountID: command.AccountID,
		InstrumentID: command.InstrumentID, State: command.State,
		ResourceVersion: command.ResourceVersion,
	})
	if err != nil {
		return fmt.Errorf("encode order event failed")
	}
	_, err = hub.Publish(PublishRequest{
		PrincipalID: command.PrincipalID, AccountID: command.AccountID,
		EventID: command.EventID, ResourceVersion: command.ResourceVersion,
		Kind: KindOrder, Payload: payload,
	})
	return err
}

type SubscribeRequest struct {
	PrincipalID domain.PrincipalID
	AccountID   domain.AccountID
	Kinds       []Kind
	Buffer      int
}

func (request SubscribeRequest) validate() error {
	if err := request.PrincipalID.Validate(); err != nil {
		return err
	}
	if err := request.AccountID.Validate(); err != nil {
		return err
	}
	for _, kind := range request.Kinds {
		if !kind.valid() {
			return fmt.Errorf("subscription event kind is invalid")
		}
	}
	if request.Buffer < 0 || request.Buffer > 4096 {
		return fmt.Errorf("subscription buffer is invalid")
	}
	return nil
}

func (hub *Hub) Subscribe(ctx context.Context, request SubscribeRequest) (*Subscription, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	subscription, _, err := hub.subscribe(ctx, request.PrincipalID, request.AccountID, request.Kinds, request.Buffer, false)
	return subscription, err
}

// SubscribePrincipal receives live events for every account owned by a
// principal. Browser reconnects reload authoritative REST resources instead of
// asking this process-local fanout to replay history.
func (hub *Hub) SubscribePrincipal(ctx context.Context, principal domain.PrincipalID, buffer int) (*Subscription, error) {
	if err := principal.Validate(); err != nil {
		return nil, err
	}
	subscription, _, err := hub.subscribe(ctx, principal, "", nil, buffer, false)
	return subscription, err
}

// SubscribePrincipalWithConnections atomically captures the latest connection
// state before releasing the hub lock. Events published after that snapshot are
// queued on the returned subscription, so a late browser cannot observe an old
// connection state after a newer one.
func (hub *Hub) SubscribePrincipalWithConnections(ctx context.Context, principal domain.PrincipalID, buffer int) (*Subscription, []Event, error) {
	if err := principal.Validate(); err != nil {
		return nil, nil, err
	}
	return hub.subscribe(ctx, principal, "", nil, buffer, true)
}

func (hub *Hub) AccountConnections(principal domain.PrincipalID) []Event {
	if hub == nil || principal.Validate() != nil {
		return nil
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	connections := make([]Event, 0)
	for key, event := range hub.connections {
		if key.principal == principal {
			connections = append(connections, event)
		}
	}
	return connections
}

func (hub *Hub) subscribe(ctx context.Context, principal domain.PrincipalID, account domain.AccountID, kinds []Kind, buffer int, includeConnections bool) (*Subscription, []Event, error) {
	if hub == nil {
		return nil, nil, ErrClosed
	}
	if ctx == nil {
		return nil, nil, fmt.Errorf("subscription context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if buffer < 0 || buffer > 4096 {
		return nil, nil, fmt.Errorf("subscription buffer is invalid")
	}
	if buffer == 0 {
		buffer = hub.subscriberBuffer
	}
	acceptedKinds := make(map[Kind]struct{}, len(kinds))
	for _, kind := range kinds {
		acceptedKinds[kind] = struct{}{}
	}
	target := &subscriber{
		principal: principal, account: account, kinds: acceptedKinds,
		events: make(chan Event, buffer), errors: make(chan error, 1),
	}
	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		close(target.events)
		close(target.errors)
		return nil, nil, ErrClosed
	}
	hub.nextSubscriber++
	target.id = hub.nextSubscriber
	hub.subscribers[target.id] = target
	connections := make([]Event, 0)
	if includeConnections {
		for key, event := range hub.connections {
			if key.principal == principal {
				connections = append(connections, event)
			}
		}
		sort.Slice(connections, func(i, j int) bool {
			if connections[i].AccountID != connections[j].AccountID {
				return connections[i].AccountID < connections[j].AccountID
			}
			return connections[i].ResourceVersion < connections[j].ResourceVersion
		})
	}
	hub.mu.Unlock()

	subscription := &Subscription{hub: hub, id: target.id, events: target.events, errors: target.errors, stop: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			subscription.Close()
		case <-subscription.stop:
		}
	}()
	return subscription, connections, nil
}

type Subscription struct {
	hub    *Hub
	id     uint64
	events <-chan Event
	errors <-chan error
	stop   chan struct{}
	once   sync.Once
}

func (subscription *Subscription) Events() <-chan Event {
	if subscription == nil {
		return nil
	}
	return subscription.events
}

func (subscription *Subscription) Errors() <-chan error {
	if subscription == nil {
		return nil
	}
	return subscription.errors
}

func (subscription *Subscription) Close() {
	if subscription == nil {
		return
	}
	subscription.once.Do(func() {
		close(subscription.stop)
		if subscription.hub != nil {
			subscription.hub.unsubscribe(subscription.id)
		}
	})
}

func (hub *Hub) publishLocked(event Event) {
	for id, subscription := range hub.subscribers {
		if subscription.principal != event.PrincipalID ||
			(subscription.account != "" && subscription.account != event.AccountID) ||
			!subscription.accepts(event.Kind) {
			continue
		}
		select {
		case subscription.events <- event:
		default:
			hub.closeSubscriberLocked(id, ErrSlowConsumer)
		}
	}
}

func (subscriber *subscriber) accepts(kind Kind) bool {
	_, selected := subscriber.kinds[kind]
	return len(subscriber.kinds) == 0 || selected
}

func (hub *Hub) updateConnectionLocked(event Event) error {
	var payload AccountConnectionPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.AccountID != event.AccountID ||
		(payload.State != AccountConnected && payload.State != AccountDisconnected) {
		return fmt.Errorf("account connection event is invalid")
	}
	key := accountConnectionKey{principal: event.PrincipalID, account: event.AccountID, route: payload.RouteID}
	if payload.State == AccountDisconnected && payload.RouteID == "" {
		for existing := range hub.connections {
			if existing.principal == key.principal && existing.account == key.account {
				delete(hub.connections, existing)
			}
		}
	}
	if payload.State == AccountConnected && payload.RouteID != "" {
		delete(hub.connections, accountConnectionKey{principal: key.principal, account: key.account})
	}
	hub.connections[key] = event
	return nil
}

func (hub *Hub) unsubscribe(id uint64) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.closeSubscriberLocked(id, nil)
}

func (hub *Hub) closeSubscriberLocked(id uint64, reason error) {
	subscription := hub.subscribers[id]
	if subscription == nil {
		return
	}
	delete(hub.subscribers, id)
	if reason != nil {
		select {
		case subscription.errors <- reason:
		default:
		}
	}
	close(subscription.events)
	close(subscription.errors)
}

func (hub *Hub) Close() {
	if hub == nil {
		return
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return
	}
	hub.closed = true
	for id := range hub.subscribers {
		hub.closeSubscriberLocked(id, ErrClosed)
	}
}
