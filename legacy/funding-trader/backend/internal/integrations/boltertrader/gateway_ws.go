package boltertrader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type PrivateEventStream struct {
	conn               *websocket.Conn
	accountID          string
	bootID             string
	sequence           uint64
	resnapshotRequired bool
	resnapshotReason   string
}

type ResnapshotRequiredError struct {
	Reason string
}

func (err *ResnapshotRequiredError) Error() string {
	if err == nil || err.Reason == "" {
		return ErrGatewayResnapshotNeeded.Error()
	}
	return ErrGatewayResnapshotNeeded.Error() + ": " + safeResyncReason(err.Reason)
}

func (err *ResnapshotRequiredError) Unwrap() error { return ErrGatewayResnapshotNeeded }

func (client *GatewayClient) SubscribePrivateEvents(ctx context.Context, request PrivateEventSubscribeRequest) (*PrivateEventStream, error) {
	if client == nil {
		return nil, fmt.Errorf("gateway client is nil")
	}
	wsURL, headers, err := client.privateEventsTarget(request)
	if err != nil {
		return nil, err
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: client.httpClient, HTTPHeader: headers})
	if err != nil {
		return nil, fmt.Errorf("gateway websocket dial failed")
	}
	applyPrivateEventReadLimit(conn, client.bodyLimit)
	return &PrivateEventStream{conn: conn, accountID: request.AccountID}, nil
}

type privateEventReadLimiter interface {
	SetReadLimit(int64)
}

func applyPrivateEventReadLimit(connection privateEventReadLimiter, limit int64) {
	connection.SetReadLimit(limit)
}

func (client *GatewayClient) privateEventsTarget(request PrivateEventSubscribeRequest) (string, http.Header, error) {
	if err := domain.AccountID(request.AccountID).Validate(); err != nil {
		return "", nil, fmt.Errorf("%w: subscription account", ErrGatewayEventInvalid)
	}
	values := url.Values{}
	values.Set("account_id", request.AccountID)
	if request.Cursor != "" {
		values.Set("cursor", request.Cursor)
	}
	for _, kind := range request.Kinds {
		if !privateEventSubscriptionKindAllowed(kind) {
			return "", nil, fmt.Errorf("%w: subscription kind", ErrGatewayEventInvalid)
		}
		values.Add("kind", kind)
	}
	target := client.endpoint(withQuery("/v1/ws", values))
	wsURL, err := url.Parse(target)
	if err != nil {
		return "", nil, fmt.Errorf("build gateway websocket url failed")
	}
	switch wsURL.Scheme {
	case "http":
		wsURL.Scheme = "ws"
	case "https":
		wsURL.Scheme = "wss"
	default:
		return "", nil, fmt.Errorf("gateway websocket scheme is invalid")
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+client.token)
	return wsURL.String(), headers, nil
}

func (stream *PrivateEventStream) Next(ctx context.Context) (PrivateEvent, error) {
	if stream == nil || stream.conn == nil {
		return PrivateEvent{}, fmt.Errorf("gateway event stream is closed")
	}
	if stream.resnapshotRequired {
		return PrivateEvent{}, &ResnapshotRequiredError{Reason: stream.resnapshotReason}
	}
	var event PrivateEvent
	if err := wsjson.Read(ctx, stream.conn, &event); err != nil {
		return PrivateEvent{}, fmt.Errorf("gateway event stream read failed")
	}
	return stream.accept(event)
}

func (stream *PrivateEventStream) accept(event PrivateEvent) (PrivateEvent, error) {
	if stream.resnapshotRequired {
		return event, &ResnapshotRequiredError{Reason: stream.resnapshotReason}
	}
	if err := validatePrivateEvent(event); err != nil {
		return event, err
	}
	if stream.accountID != "" && event.AccountID != stream.accountID {
		return event, fmt.Errorf("%w: account", ErrGatewayEventInvalid)
	}
	if event.Kind == "resync_required" {
		return event, stream.requireResnapshot(resnapshotReason(event))
	}
	if stream.bootID == "" {
		stream.bootID = event.BootID
		stream.sequence = event.Sequence
		return event, nil
	}
	if event.BootID != stream.bootID {
		return event, stream.requireResnapshot("process_restarted")
	}
	if event.Sequence != stream.sequence+1 {
		return event, stream.requireResnapshot("sequence_gap")
	}
	stream.sequence = event.Sequence
	return event, nil
}

func validatePrivateEvent(event PrivateEvent) error {
	if err := domain.BootID(event.BootID).Validate(); err != nil {
		return fmt.Errorf("%w: boot id", ErrGatewayEventInvalid)
	}
	if event.Sequence == 0 {
		return fmt.Errorf("%w: sequence", ErrGatewayEventInvalid)
	}
	if err := domain.EventID(event.EventID).Validate(); err != nil {
		return fmt.Errorf("%w: event id", ErrGatewayEventInvalid)
	}
	if err := domain.AccountID(event.AccountID).Validate(); err != nil {
		return fmt.Errorf("%w: account", ErrGatewayEventInvalid)
	}
	if err := domain.ResourceVersion(event.ResourceVersion).Validate(); err != nil {
		return fmt.Errorf("%w: resource version", ErrGatewayEventInvalid)
	}
	if !privateEventKindAllowed(event.Kind) {
		return fmt.Errorf("%w: kind", ErrGatewayEventInvalid)
	}
	if event.OccurredAt.IsZero() {
		return fmt.Errorf("%w: occurred at", ErrGatewayEventInvalid)
	}
	if len(event.Payload) == 0 || !json.Valid(event.Payload) {
		return fmt.Errorf("%w: payload", ErrGatewayEventInvalid)
	}
	return nil
}

func (stream *PrivateEventStream) Close() error {
	if stream == nil || stream.conn == nil {
		return nil
	}
	return stream.conn.Close(websocket.StatusNormalClosure, "client_closed")
}

func (stream *PrivateEventStream) requireResnapshot(reason string) error {
	stream.resnapshotRequired = true
	stream.resnapshotReason = safeResyncReason(reason)
	return &ResnapshotRequiredError{Reason: stream.resnapshotReason}
}

func resnapshotReason(event PrivateEvent) string {
	var payload ResyncPayload
	if len(event.Payload) != 0 && json.Unmarshal(event.Payload, &payload) == nil && payload.Reason != "" {
		return payload.Reason
	}
	return "resync_required"
}

func safeResyncReason(reason string) string {
	reason = safeGatewayToken(reason, "")
	if reason == "" {
		return "resync-required"
	}
	reason = stableReason(reason)
	if reason == "not-ready" {
		return "resync-required"
	}
	return reason
}

func privateEventKindAllowed(kind string) bool {
	switch kind {
	case "order", "fill", "balance", "position", "account_health", "resync_required":
		return true
	default:
		return false
	}
}

func privateEventSubscriptionKindAllowed(kind string) bool {
	switch kind {
	case "order", "fill", "balance", "position", "account_health":
		return true
	default:
		return false
	}
}

func IsResnapshotRequired(err error) bool {
	return errors.Is(err, ErrGatewayResnapshotNeeded)
}
