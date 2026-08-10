package uistream

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
)

var (
	ErrInvalidCursor  = errors.New("uistream cursor is invalid")
	ErrResyncRequired = errors.New("uistream resync required")
)

const (
	EventSnapshot           = "snapshot"
	EventData               = "event"
	EventHeartbeat          = "heartbeat"
	EventResnapshotRequired = "resnapshot.required"
	defaultReplayLimit      = 1000
)

type Request struct {
	Scope  string
	Cursor string
	Kinds  []string
}

type Snapshot struct {
	Scope string
	Data  json.RawMessage
}

type EventInput struct {
	Scope           string
	PrincipalID     string
	Mode            string
	ContractVersion string
	Kind            string
	Data            json.RawMessage
}

type StoredEvent struct {
	Sequence        uint64
	Scope           string
	PrincipalID     string
	Mode            string
	ContractVersion string
	Kind            string
	Data            json.RawMessage
	OccurredAt      time.Time
	Cursor          string
}

type StreamEvent struct {
	Name            string          `json:"name"`
	Scope           string          `json:"scope"`
	PrincipalID     string          `json:"principalId"`
	Mode            string          `json:"mode"`
	ContractVersion string          `json:"contractVersion"`
	Kind            string          `json:"kind,omitempty"`
	Cursor          string          `json:"cursor"`
	Data            json.RawMessage `json:"data,omitempty"`
	OccurredAt      time.Time       `json:"occurredAt,omitempty"`
	Sequence        uint64          `json:"-"`
}

type SnapshotSource interface {
	Snapshot(context.Context, Request) (Snapshot, error)
}

type EventStore interface {
	Latest(context.Context, string) (uint64, error)
	Replay(context.Context, string, uint64, []string, int) ([]StoredEvent, error)
	Append(context.Context, EventInput) (StoredEvent, error)
}

type Sink interface {
	Send(context.Context, StreamEvent) error
}

type ServiceConfig struct {
	BootID            string
	PrincipalID       string
	Mode              string
	ContractVersion   string
	SigningKey        []byte
	Store             EventStore
	SnapshotSource    SnapshotSource
	HeartbeatInterval time.Duration
	SubscriberBuffer  int
	ReplayLimit       int
}

type Service struct {
	bootID            string
	principalID       string
	mode              string
	contractVersion   string
	codec             *cursorCodec
	store             EventStore
	source            SnapshotSource
	heartbeatInterval time.Duration
	subscriberBuffer  int
	replayLimit       int

	mu          sync.Mutex
	nextSubID   uint64
	subscribers map[uint64]*subscriber
}

func NewService(config ServiceConfig) (*Service, error) {
	if config.BootID == "" {
		boot, err := randomBootID()
		if err != nil {
			return nil, err
		}
		config.BootID = boot
	}
	if strings.TrimSpace(config.BootID) == "" {
		return nil, errors.New("boot id is required")
	}
	config.PrincipalID = strings.TrimSpace(config.PrincipalID)
	config.Mode = strings.TrimSpace(config.Mode)
	if config.PrincipalID == "" || (config.Mode != "test" && config.Mode != "live") {
		return nil, errors.New("principal id and product mode are required")
	}
	if config.Store == nil {
		return nil, errors.New("event store is required")
	}
	if config.SnapshotSource == nil {
		return nil, errors.New("snapshot source is required")
	}
	codec, err := newCursorCodec(config.SigningKey)
	if err != nil {
		return nil, err
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 30 * time.Second
	}
	if config.SubscriberBuffer <= 0 {
		config.SubscriberBuffer = 64
	}
	if config.ReplayLimit <= 0 {
		config.ReplayLimit = defaultReplayLimit
	}
	if config.ContractVersion == "" {
		config.ContractVersion = readmodel.ContractVersion
	}
	if config.ContractVersion != readmodel.ContractVersion {
		return nil, errors.New("unsupported stream contract version")
	}
	return &Service{
		bootID:            config.BootID,
		principalID:       config.PrincipalID,
		mode:              config.Mode,
		contractVersion:   config.ContractVersion,
		codec:             codec,
		store:             config.Store,
		source:            config.SnapshotSource,
		heartbeatInterval: config.HeartbeatInterval,
		subscriberBuffer:  config.SubscriberBuffer,
		replayLimit:       config.ReplayLimit,
		subscribers:       make(map[uint64]*subscriber),
	}, nil
}

func (service *Service) Serve(ctx context.Context, sink Sink, request Request) error {
	if service == nil {
		return errors.New("uistream service is nil")
	}
	if sink == nil {
		return errors.New("sink is required")
	}
	request.Scope = strings.TrimSpace(request.Scope)
	if request.Scope == "" {
		return errors.New("scope is required")
	}
	request.Kinds = normalizeKinds(request.Kinds)

	if request.Cursor == "" {
		sub := service.subscribe(request)
		defer service.unsubscribe(sub.id)
		if err := service.sendSnapshot(ctx, sink, request); err != nil {
			return err
		}
		return service.pump(ctx, sink, request, sub, 0)
	}

	position, err := service.codec.decode(
		request.Cursor,
		service.bootID,
		service.principalID,
		service.mode,
		service.contractVersion,
		request.Scope,
	)
	if err != nil {
		return service.sendResnapshot(ctx, sink, request)
	}
	sub := service.subscribe(request)
	defer service.unsubscribe(sub.id)

	replayKinds := request.Kinds
	if len(replayKinds) > 0 {
		replayKinds = normalizeKinds(append(append([]string(nil), replayKinds...), EventResnapshotRequired))
	}
	replayed, err := service.store.Replay(ctx, request.Scope, position.Sequence, replayKinds, service.replayLimit)
	if err != nil {
		if errors.Is(err, ErrResyncRequired) {
			return service.sendResnapshot(ctx, sink, request)
		}
		return err
	}
	for _, event := range replayed {
		stream, err := service.streamEvent(event)
		if err != nil {
			return err
		}
		if err := sink.Send(ctx, stream); err != nil {
			return err
		}
		if stream.Kind == EventResnapshotRequired {
			return nil
		}
	}
	lastReplayed := position.Sequence
	for _, event := range replayed {
		if event.Sequence > lastReplayed {
			lastReplayed = event.Sequence
		}
	}
	return service.pump(ctx, sink, request, sub, lastReplayed)
}

func (service *Service) Publish(ctx context.Context, input EventInput) (StoredEvent, error) {
	input.Scope = strings.TrimSpace(input.Scope)
	if input.PrincipalID == "" {
		input.PrincipalID = service.principalID
	}
	if input.Mode == "" {
		input.Mode = service.mode
	}
	if input.ContractVersion == "" {
		input.ContractVersion = service.contractVersion
	}
	input.Kind = strings.TrimSpace(input.Kind)
	if input.Scope == "" || input.Kind == "" ||
		input.PrincipalID != service.principalID ||
		input.Mode != service.mode ||
		input.ContractVersion != service.contractVersion {
		return StoredEvent{}, errors.New("principal, mode, scope, contract version, and kind are required")
	}
	stored, err := service.store.Append(ctx, input)
	if err != nil {
		return StoredEvent{}, err
	}
	stored.Cursor, err = service.encodeCursor(stored.Scope, stored.Sequence)
	if err != nil {
		return StoredEvent{}, err
	}
	stream, err := service.streamEvent(stored)
	if err != nil {
		return StoredEvent{}, err
	}
	service.broadcast(stream)
	return stored, nil
}

// BroadcastStored publishes an event that was already committed atomically with
// its projection. It deliberately does not append to the EventStore again.
func (service *Service) BroadcastStored(stored StoredEvent) error {
	if service == nil ||
		stored.Sequence == 0 ||
		strings.TrimSpace(stored.Scope) == "" ||
		strings.TrimSpace(stored.Kind) == "" ||
		!json.Valid(stored.Data) ||
		stored.OccurredAt.IsZero() {
		return errors.New("stored event is invalid")
	}
	if stored.ContractVersion == "" {
		stored.ContractVersion = service.contractVersion
	}
	if stored.PrincipalID == "" {
		stored.PrincipalID = service.principalID
	}
	if stored.Mode == "" {
		stored.Mode = service.mode
	}
	if stored.PrincipalID != service.principalID ||
		stored.Mode != service.mode ||
		stored.ContractVersion != service.contractVersion {
		return errors.New("stored event scope or contract version is invalid")
	}
	stream, err := service.streamEvent(stored)
	if err != nil {
		return err
	}
	service.broadcast(stream)
	return nil
}

func (service *Service) SubscriberCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return len(service.subscribers)
}

func (service *Service) sendSnapshot(ctx context.Context, sink Sink, request Request) error {
	latest, err := service.store.Latest(ctx, request.Scope)
	if err != nil {
		return err
	}
	cursor, err := service.encodeCursor(request.Scope, latest)
	if err != nil {
		return err
	}
	snapshot, err := service.source.Snapshot(ctx, request)
	if err != nil {
		return err
	}
	if snapshot.Scope == "" {
		snapshot.Scope = request.Scope
	}
	return sink.Send(ctx, StreamEvent{
		Name: EventSnapshot, Scope: snapshot.Scope,
		PrincipalID: service.principalID, Mode: service.mode,
		ContractVersion: service.contractVersion, Cursor: cursor,
		Data: cloneRaw(snapshot.Data), OccurredAt: time.Now().UTC(),
	})
}

func (service *Service) sendResnapshot(ctx context.Context, sink Sink, request Request) error {
	current, err := service.currentCursor(ctx, request.Scope)
	if err != nil {
		return err
	}
	payload, err := resnapshotPayload(request.Scope)
	if err != nil {
		return err
	}
	return sink.Send(ctx, StreamEvent{
		Name: EventResnapshotRequired, Scope: request.Scope,
		PrincipalID: service.principalID, Mode: service.mode,
		ContractVersion: service.contractVersion, Cursor: current,
		Data: payload, OccurredAt: time.Now().UTC(),
	})
}

func (service *Service) pump(ctx context.Context, sink Sink, request Request, sub *subscriber, skipThrough uint64) error {
	ticker := time.NewTicker(service.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-sub.events:
			if event.Sequence != 0 && event.Sequence <= skipThrough {
				sub.markSent()
				continue
			}
			if err := sink.Send(ctx, event); err != nil {
				return err
			}
			sub.markSent()
			if event.Name == EventResnapshotRequired || event.Kind == EventResnapshotRequired {
				return nil
			}
		case <-ticker.C:
			cursor, err := service.currentCursor(ctx, request.Scope)
			if err != nil {
				return err
			}
			if err := sink.Send(ctx, StreamEvent{
				Name: EventHeartbeat, Scope: request.Scope,
				PrincipalID: service.principalID, Mode: service.mode,
				ContractVersion: service.contractVersion, Cursor: cursor,
				OccurredAt: time.Now().UTC(),
			}); err != nil {
				return err
			}
		}
	}
}

func (service *Service) subscribe(request Request) *subscriber {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.nextSubID++
	sub := &subscriber{
		id:     service.nextSubID,
		scope:  request.Scope,
		kinds:  kindSet(request.Kinds),
		events: make(chan StreamEvent, service.subscriberBuffer),
	}
	service.subscribers[sub.id] = sub
	return sub
}

func (service *Service) unsubscribe(id uint64) {
	service.mu.Lock()
	defer service.mu.Unlock()
	delete(service.subscribers, id)
}

func (service *Service) broadcast(event StreamEvent) {
	service.mu.Lock()
	defer service.mu.Unlock()
	for _, sub := range service.subscribers {
		if sub.scope != event.Scope || !sub.matches(event.Kind) {
			continue
		}
		if sub.markPending(service.subscriberBuffer) {
			sub.events <- event
			continue
		}
		payload, err := resnapshotPayload(event.Scope)
		if err != nil {
			continue
		}
		resnapshot := StreamEvent{
			Name: EventResnapshotRequired, Scope: event.Scope,
			PrincipalID: service.principalID, Mode: service.mode,
			ContractVersion: service.contractVersion, Cursor: event.Cursor,
			Data: payload, OccurredAt: time.Now().UTC(),
		}
		select {
		case sub.events <- resnapshot:
		default:
			select {
			case <-sub.events:
				sub.markDropped()
			default:
			}
			sub.events <- resnapshot
		}
	}
}

func (service *Service) streamEvent(event StoredEvent) (StreamEvent, error) {
	if event.PrincipalID == "" {
		event.PrincipalID = service.principalID
	}
	if event.Mode == "" {
		event.Mode = service.mode
	}
	if event.ContractVersion == "" {
		event.ContractVersion = service.contractVersion
	}
	if event.PrincipalID != service.principalID ||
		event.Mode != service.mode ||
		event.ContractVersion != service.contractVersion {
		return StreamEvent{}, errors.New("stored event scope or contract version is invalid")
	}
	cursor := event.Cursor
	if cursor == "" {
		var err error
		cursor, err = service.encodeCursor(event.Scope, event.Sequence)
		if err != nil {
			return StreamEvent{}, err
		}
	}
	return StreamEvent{
		Name: EventData, Scope: event.Scope,
		PrincipalID: event.PrincipalID, Mode: event.Mode,
		ContractVersion: event.ContractVersion, Kind: event.Kind,
		Cursor: cursor, Data: cloneRaw(event.Data),
		OccurredAt: event.OccurredAt.UTC(), Sequence: event.Sequence,
	}, nil
}

func (service *Service) currentCursor(ctx context.Context, scope string) (string, error) {
	latest, err := service.store.Latest(ctx, scope)
	if err != nil {
		return "", err
	}
	return service.encodeCursor(scope, latest)
}

func (service *Service) encodeCursor(scope string, sequence uint64) (string, error) {
	return service.codec.encode(cursorPosition{
		BootID: service.bootID, PrincipalID: service.principalID,
		Mode: service.mode, ContractVersion: service.contractVersion,
		Scope: scope, Sequence: sequence,
	})
}

func (service *Service) mustEncodeCursor(t interface{ Fatal(...any) }, scope string, sequence uint64) string {
	cursor, err := service.encodeCursor(scope, sequence)
	if err != nil {
		t.Fatal(err)
	}
	return cursor
}

type subscriber struct {
	id      uint64
	scope   string
	kinds   map[string]struct{}
	events  chan StreamEvent
	mu      sync.Mutex
	pending int
}

func (sub *subscriber) matches(kind string) bool {
	if kind == EventResnapshotRequired {
		return true
	}
	if len(sub.kinds) == 0 {
		return true
	}
	_, ok := sub.kinds[kind]
	return ok
}

func (sub *subscriber) markPending(limit int) bool {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.pending >= limit {
		sub.pending++
		return false
	}
	sub.pending++
	return true
}

func (sub *subscriber) markSent() {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.pending > 0 {
		sub.pending--
	}
}

func (sub *subscriber) markDropped() {
	sub.markSent()
}

type cursorPosition struct {
	BootID          string `json:"bootId"`
	PrincipalID     string `json:"principalId"`
	Mode            string `json:"mode"`
	ContractVersion string `json:"contractVersion"`
	Scope           string `json:"scope"`
	Sequence        uint64 `json:"sequence"`
}

type cursorCodec struct {
	key  []byte
	aead cipher.AEAD
}

func newCursorCodec(key []byte) (*cursorCodec, error) {
	if len(key) < sha256.Size {
		return nil, fmt.Errorf("cursor signing key must be at least %d bytes", sha256.Size)
	}
	copied := make([]byte, len(key))
	copy(copied, key)
	block, err := aes.NewCipher(copied[:sha256.Size])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &cursorCodec{key: copied, aead: aead}, nil
}

func (codec *cursorCodec) encode(position cursorPosition) (string, error) {
	if strings.TrimSpace(position.BootID) == "" ||
		strings.TrimSpace(position.PrincipalID) == "" ||
		(position.Mode != "test" && position.Mode != "live") ||
		position.ContractVersion != readmodel.ContractVersion ||
		strings.TrimSpace(position.Scope) == "" {
		return "", ErrInvalidCursor
	}
	payload, err := json.Marshal(struct {
		Version  int            `json:"version"`
		Position cursorPosition `json:"position"`
	}{Version: 2, Position: position})
	if err != nil {
		return "", err
	}
	nonceSeed := codec.sign(payload)
	nonce := nonceSeed[:codec.aead.NonceSize()]
	ciphertext := codec.aead.Seal(nil, nonce, payload, nil)
	body := append(nonce, ciphertext...)
	signature := codec.sign(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (codec *cursorCodec) decode(
	raw, bootID, principalID, mode, contractVersion, scope string,
) (cursorPosition, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return cursorPosition{}, ErrInvalidCursor
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return cursorPosition{}, ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, codec.sign(body)) {
		return cursorPosition{}, ErrInvalidCursor
	}
	nonceSize := codec.aead.NonceSize()
	if len(body) <= nonceSize {
		return cursorPosition{}, ErrInvalidCursor
	}
	payload, err := codec.aead.Open(nil, body[:nonceSize], body[nonceSize:], nil)
	if err != nil {
		return cursorPosition{}, ErrInvalidCursor
	}
	var envelope struct {
		Version  int            `json:"version"`
		Position cursorPosition `json:"position"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil ||
		envelope.Version != 2 ||
		envelope.Position.BootID != bootID ||
		envelope.Position.PrincipalID != principalID ||
		envelope.Position.Mode != mode ||
		envelope.Position.ContractVersion != contractVersion ||
		envelope.Position.Scope != scope {
		return cursorPosition{}, ErrInvalidCursor
	}
	return envelope.Position, nil
}

func (codec *cursorCodec) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, codec.key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func randomBootID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func normalizeKinds(kinds []string) []string {
	if len(kinds) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(kinds))
	normalized := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		if _, ok := seen[kind]; ok {
			continue
		}
		seen[kind] = struct{}{}
		normalized = append(normalized, kind)
	}
	slices.Sort(normalized)
	return normalized
}

func kindSet(kinds []string) map[string]struct{} {
	if len(kinds) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		set[kind] = struct{}{}
	}
	return set
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	copied := make([]byte, len(raw))
	copy(copied, raw)
	return copied
}

func resnapshotPayload(scope string) (json.RawMessage, error) {
	return json.Marshal(struct {
		Scope []string `json:"scope"`
	}{Scope: []string{scope}})
}
