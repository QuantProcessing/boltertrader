package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
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

type Event struct {
	BootID          domain.BootID          `json:"boot_id"`
	Sequence        uint64                 `json:"sequence"`
	EventID         domain.EventID         `json:"event_id"`
	AccountID       domain.AccountID       `json:"account_id"`
	ResourceVersion domain.ResourceVersion `json:"resource_version"`
	Kind            Kind                   `json:"kind"`
	OccurredAt      time.Time              `json:"occurred_at"`
	Payload         json.RawMessage        `json:"payload"`
	PrincipalID     domain.PrincipalID     `json:"-"`
}

func (event Event) Cursor() string {
	return string(event.BootID) + ":" + strconv.FormatUint(event.Sequence, 10)
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
	Capacity         int
	SubscriberBuffer int
	Identity         *domain.IdentityGenerator
	Now              func() time.Time
	Observer         ResyncObserver
}

type ResyncObserver interface {
	ObserveResync(string)
}

type Hub struct {
	mu               sync.Mutex
	bootID           domain.BootID
	capacity         int
	subscriberBuffer int
	identity         *domain.IdentityGenerator
	now              func() time.Time
	observer         ResyncObserver
	accounts         map[domain.AccountID]*accountBuffer
	subscribers      map[uint64]*subscriber
	nextSubscriber   uint64
	closed           bool
}

type accountBuffer struct {
	sequence uint64
	events   []Event
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
	if options.Capacity <= 0 {
		options.Capacity = 1024
	}
	if options.SubscriberBuffer <= 0 {
		options.SubscriberBuffer = 64
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	bootID, err := options.Identity.BootID()
	if err != nil {
		return nil, fmt.Errorf("allocate event boot id failed")
	}
	return &Hub{
		bootID: bootID, capacity: options.Capacity, subscriberBuffer: options.SubscriberBuffer,
		identity: options.Identity, now: options.Now, observer: options.Observer,
		accounts:    make(map[domain.AccountID]*accountBuffer),
		subscribers: make(map[uint64]*subscriber),
	}, nil
}

func (hub *Hub) BootID() domain.BootID {
	if hub == nil {
		return ""
	}
	return hub.bootID
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
	payload := append(json.RawMessage(nil), request.Payload...)

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return Event{}, ErrClosed
	}
	buffer := hub.accounts[request.AccountID]
	if buffer == nil {
		buffer = &accountBuffer{events: make([]Event, 0, hub.capacity)}
		hub.accounts[request.AccountID] = buffer
	}
	buffer.sequence++
	event := Event{
		BootID: hub.bootID, Sequence: buffer.sequence, EventID: request.EventID,
		AccountID: request.AccountID, ResourceVersion: request.ResourceVersion,
		Kind: request.Kind, OccurredAt: request.OccurredAt, Payload: payload,
		PrincipalID: request.PrincipalID,
	}
	buffer.events = append(buffer.events, event)
	if len(buffer.events) > hub.capacity {
		copy(buffer.events, buffer.events[len(buffer.events)-hub.capacity:])
		buffer.events = buffer.events[:hub.capacity]
	}
	for id, subscription := range hub.subscribers {
		if subscription.principal != event.PrincipalID || subscription.account != event.AccountID ||
			!subscription.accepts(event.Kind) {
			continue
		}
		select {
		case subscription.events <- event:
		default:
			hub.closeSubscriberLocked(id, ErrSlowConsumer)
		}
	}
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
	if strings.TrimSpace(reason) == "" || strings.TrimSpace(reason) != reason ||
		strings.ContainsAny(reason, "\n\r\t") {
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

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.closed {
		return Event{}, ErrClosed
	}
	buffer := hub.accounts[accountID]
	if buffer == nil {
		buffer = &accountBuffer{events: make([]Event, 0, hub.capacity)}
		hub.accounts[accountID] = buffer
	}
	buffer.sequence++
	event := Event{
		BootID: hub.bootID, Sequence: buffer.sequence, EventID: eventID,
		AccountID: accountID, ResourceVersion: 0, Kind: KindResyncRequired,
		OccurredAt: hub.now().UTC(), Payload: payload, PrincipalID: principal,
	}
	buffer.events = append(buffer.events, event)
	if len(buffer.events) > hub.capacity {
		copy(buffer.events, buffer.events[len(buffer.events)-hub.capacity:])
		buffer.events = buffer.events[:hub.capacity]
	}
	for id, subscription := range hub.subscribers {
		if subscription.principal != principal || subscription.account != accountID {
			continue
		}
		select {
		case subscription.events <- event:
		default:
			hub.closeSubscriberLocked(id, ErrSlowConsumer)
			continue
		}
		hub.closeSubscriberLocked(id, nil)
	}
	if hub.observer != nil {
		hub.observer.ObserveResync(reason)
	}
	return event, nil
}

type SubscribeRequest struct {
	PrincipalID domain.PrincipalID
	AccountID   domain.AccountID
	Cursor      string
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

func (hub *Hub) Subscribe(ctx context.Context, request SubscribeRequest) (*Subscription, error) {
	if hub == nil {
		return nil, ErrClosed
	}
	if ctx == nil {
		return nil, fmt.Errorf("subscription context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := request.validate(); err != nil {
		return nil, err
	}
	cursor, hasCursor, err := parseCursor(request.Cursor)
	if err != nil {
		return nil, err
	}
	bufferSize := request.Buffer
	if bufferSize == 0 {
		bufferSize = hub.subscriberBuffer
	}
	kinds := make(map[Kind]struct{}, len(request.Kinds))
	for _, kind := range request.Kinds {
		kinds[kind] = struct{}{}
	}
	target := &subscriber{
		principal: request.PrincipalID, account: request.AccountID, kinds: kinds,
	}

	hub.mu.Lock()
	if hub.closed {
		hub.mu.Unlock()
		return nil, ErrClosed
	}
	terminalReason := ""
	account := hub.accounts[request.AccountID]
	if hasCursor {
		switch {
		case cursor.BootID != hub.bootID:
			terminalReason = "process_restarted"
		case account == nil && cursor.Sequence != 0:
			terminalReason = "cursor_not_available"
		case account != nil && cursor.Sequence > account.sequence:
			terminalReason = "cursor_ahead"
		case account != nil && len(account.events) > 0 &&
			cursor.Sequence+1 < account.events[0].Sequence:
			terminalReason = "cursor_evicted"
		}
	}
	var replay []Event
	if terminalReason == "" && hasCursor && account != nil {
		replay = make([]Event, 0, len(account.events))
		for _, event := range account.events {
			if event.Sequence <= cursor.Sequence || event.PrincipalID != request.PrincipalID ||
				!target.accepts(event.Kind) {
				continue
			}
			replay = append(replay, event)
		}
	}
	if len(replay) > bufferSize {
		bufferSize = len(replay)
	}
	target.events = make(chan Event, bufferSize)
	target.errors = make(chan error, 1)
	if terminalReason != "" {
		event, buildErr := hub.resyncEventLocked(request.PrincipalID, request.AccountID, terminalReason)
		if buildErr != nil {
			close(target.events)
			close(target.errors)
			hub.mu.Unlock()
			return nil, buildErr
		}
		target.events <- event
		close(target.events)
		close(target.errors)
		if hub.observer != nil {
			hub.observer.ObserveResync(terminalReason)
		}
	} else {
		hub.nextSubscriber++
		target.id = hub.nextSubscriber
		hub.subscribers[target.id] = target
		for _, event := range replay {
			target.events <- event
		}
	}
	hub.mu.Unlock()

	subscription := &Subscription{
		hub: hub, id: target.id, events: target.events, errors: target.errors,
		stop: make(chan struct{}),
	}
	if done := ctx.Done(); done != nil {
		go func() {
			select {
			case <-done:
				subscription.Close()
			case <-subscription.stop:
			}
		}()
	}
	return subscription, nil
}

type Cursor struct {
	BootID   domain.BootID
	Sequence uint64
}

func parseCursor(value string) (Cursor, bool, error) {
	if value == "" {
		return Cursor{}, false, nil
	}
	bootValue, sequenceValue, ok := strings.Cut(value, ":")
	if !ok || strings.Contains(sequenceValue, ":") {
		return Cursor{}, false, fmt.Errorf("event cursor is invalid")
	}
	bootID := domain.BootID(bootValue)
	if err := bootID.Validate(); err != nil {
		return Cursor{}, false, fmt.Errorf("event cursor is invalid")
	}
	sequence, err := strconv.ParseUint(sequenceValue, 10, 64)
	if err != nil {
		return Cursor{}, false, fmt.Errorf("event cursor is invalid")
	}
	return Cursor{BootID: bootID, Sequence: sequence}, true, nil
}

func (hub *Hub) resyncEventLocked(principal domain.PrincipalID, accountID domain.AccountID, reason string) (Event, error) {
	eventID, err := hub.identity.EventID()
	if err != nil {
		return Event{}, fmt.Errorf("allocate resync event failed")
	}
	payload, err := hub.resyncPayload(accountID, reason)
	if err != nil {
		return Event{}, err
	}
	buffer := hub.accounts[accountID]
	if buffer == nil {
		buffer = &accountBuffer{events: make([]Event, 0, hub.capacity)}
		hub.accounts[accountID] = buffer
	}
	buffer.sequence++
	event := Event{
		BootID: hub.bootID, Sequence: buffer.sequence, EventID: eventID,
		AccountID: accountID, ResourceVersion: 0, Kind: KindResyncRequired,
		OccurredAt: hub.now().UTC(), Payload: payload, PrincipalID: principal,
	}
	buffer.events = append(buffer.events, event)
	if len(buffer.events) > hub.capacity {
		copy(buffer.events, buffer.events[len(buffer.events)-hub.capacity:])
		buffer.events = buffer.events[:hub.capacity]
	}
	return event, nil
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
	return len(subscriber.kinds) == 0 || hasKind(subscriber.kinds, kind)
}

func hasKind(kinds map[Kind]struct{}, kind Kind) bool {
	_, ok := kinds[kind]
	return ok
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
