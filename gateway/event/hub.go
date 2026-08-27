package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
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
	KindOrder          Kind = "order"
	KindFill           Kind = "fill"
	KindBalance        Kind = "balance"
	KindPosition       Kind = "position"
	KindAccountHealth  Kind = "account_health"
	KindResyncRequired Kind = "resync_required"
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

type ResyncPayload struct {
	Reason    string   `json:"reason"`
	Resources []string `json:"resources"`
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
	if !request.Kind.valid() || request.Kind == KindResyncRequired {
		return fmt.Errorf("event kind is invalid")
	}
	if len(request.Payload) == 0 || !json.Valid(request.Payload) {
		return fmt.Errorf("event payload is invalid")
	}
	return nil
}

func (kind Kind) valid() bool {
	switch kind {
	case KindOrder, KindFill, KindBalance, KindPosition, KindAccountHealth, KindResyncRequired:
		return true
	default:
		return false
	}
}

type Options struct {
	SubscriberBuffer int
	Identity         *domain.IdentityGenerator
	Now              func() time.Time
	Observer         ResyncObserver
}

type ResyncObserver interface {
	ObserveResync(string)
}

// Hub fans out live account notifications. It deliberately keeps no replay
// log: durable state lives behind REST resources and is reconciled on a fixed
// cadence, while this hub only reduces the time to observe a change.
type Hub struct {
	mu               sync.Mutex
	subscriberBuffer int
	identity         *domain.IdentityGenerator
	now              func() time.Time
	observer         ResyncObserver
	subscribers      map[uint64]*subscriber
	nextSubscriber   uint64
	closed           bool
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
		observer:         options.Observer,
		subscribers:      make(map[uint64]*subscriber),
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

func (hub *Hub) PublishResync(principal domain.PrincipalID, accountID domain.AccountID, reason string) (Event, error) {
	if hub == nil {
		return Event{}, ErrClosed
	}
	if err := principal.Validate(); err != nil {
		return Event{}, err
	}
	if err := accountID.Validate(); err != nil {
		return Event{}, err
	}
	if strings.TrimSpace(reason) == "" || strings.TrimSpace(reason) != reason || strings.ContainsAny(reason, "\n\r\t") {
		return Event{}, fmt.Errorf("resync reason is invalid")
	}
	eventID, err := hub.identity.EventID()
	if err != nil {
		return Event{}, fmt.Errorf("allocate resync event failed")
	}
	payload, err := hub.resyncPayload(accountID, reason)
	if err != nil {
		return Event{}, err
	}
	event := Event{
		EventID: eventID, AccountID: accountID, Kind: KindResyncRequired,
		OccurredAt: hub.now().UTC(), Payload: payload, PrincipalID: principal,
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return Event{}, ErrClosed
	}
	hub.publishLocked(event)
	if hub.observer != nil {
		hub.observer.ObserveResync(reason)
	}
	return event, nil
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
		if !kind.valid() || kind == KindResyncRequired {
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
	return hub.subscribe(ctx, request.PrincipalID, request.AccountID, request.Kinds, request.Buffer)
}

// SubscribePrincipal receives live events for every account owned by a
// principal. Browser reconnects reload authoritative REST resources instead of
// asking this process-local fanout to replay history.
func (hub *Hub) SubscribePrincipal(ctx context.Context, principal domain.PrincipalID, buffer int) (*Subscription, error) {
	if err := principal.Validate(); err != nil {
		return nil, err
	}
	return hub.subscribe(ctx, principal, "", nil, buffer)
}

func (hub *Hub) subscribe(ctx context.Context, principal domain.PrincipalID, account domain.AccountID, kinds []Kind, buffer int) (*Subscription, error) {
	if hub == nil {
		return nil, ErrClosed
	}
	if ctx == nil {
		return nil, fmt.Errorf("subscription context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if buffer < 0 || buffer > 4096 {
		return nil, fmt.Errorf("subscription buffer is invalid")
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
		return nil, ErrClosed
	}
	hub.nextSubscriber++
	target.id = hub.nextSubscriber
	hub.subscribers[target.id] = target
	hub.mu.Unlock()

	subscription := &Subscription{hub: hub, id: target.id, events: target.events, errors: target.errors, stop: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			subscription.Close()
		case <-subscription.stop:
		}
	}()
	return subscription, nil
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

func (hub *Hub) resyncPayload(accountID domain.AccountID, reason string) (json.RawMessage, error) {
	payload, err := json.Marshal(ResyncPayload{
		Reason: reason,
		Resources: []string{
			"/v1/accounts/" + url.PathEscape(string(accountID)),
			"/v1/orders?account_id=" + url.QueryEscape(string(accountID)),
			"/v1/fills?account_id=" + url.QueryEscape(string(accountID)),
			"/v1/balances?account_id=" + url.QueryEscape(string(accountID)),
			"/v1/positions?account_id=" + url.QueryEscape(string(accountID)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode resync event failed")
	}
	return payload, nil
}

func (subscriber *subscriber) accepts(kind Kind) bool {
	if kind == KindResyncRequired {
		return true
	}
	_, selected := subscriber.kinds[kind]
	return len(subscriber.kinds) == 0 || selected
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
