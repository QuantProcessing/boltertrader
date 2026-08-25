package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/accountpositions"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const accountPositionSnapshotLimit = 8 << 20

// ListAccounts returns the account directory after the Gateway's internal
// principal boundary has authorized the caller. Removed accounts are already
// excluded by that authority route.
func (client *Client) ListAccounts(ctx context.Context, principal string) ([]accountpositions.Account, error) {
	if strings.TrimSpace(principal) == "" {
		return nil, fmt.Errorf("Gateway account directory scope is invalid")
	}
	var envelope readEnvelope[struct {
		Accounts []accountRecordView `json:"accounts"`
	}]
	if err := client.readLimit(ctx, principal, "/internal/v1/accounts", nil, &envelope, accountPositionSnapshotLimit); err != nil {
		return nil, err
	}
	accounts := make([]accountpositions.Account, 0, len(envelope.Data.Accounts))
	for _, source := range envelope.Data.Accounts {
		routes := make([]accountpositions.AccountRoute, 0, len(source.Routes))
		for _, route := range source.Routes {
			routes = append(routes, accountpositions.AccountRoute{
				RouteID: route.RouteID, Product: route.Product,
				SettlementAsset: route.SettlementAsset, RequestedState: route.RequestedState,
			})
		}
		label := strings.TrimSpace(source.DisplayName)
		if label == "" {
			label = source.ID
		}
		accounts = append(accounts, accountpositions.Account{
			AccountID: source.ID, Label: label, Venue: source.Venue,
			Environment: source.NativeEnvironment, Status: source.Status, Routes: routes,
		})
	}
	return accounts, nil
}

func (client *Client) PositionSnapshot(ctx context.Context, principal, accountID string) (accountpositions.SourceSnapshot, error) {
	if strings.TrimSpace(principal) == "" || strings.TrimSpace(accountID) == "" {
		return accountpositions.SourceSnapshot{}, fmt.Errorf("Gateway position snapshot scope is invalid")
	}
	if _, err := client.Accounts().Get(ctx, principal, accountID); err != nil {
		return accountpositions.SourceSnapshot{}, fmt.Errorf("authorize Gateway position snapshot: %w", err)
	}
	var envelope readEnvelope[accountpositions.SourceSnapshot]
	if err := client.readLimit(ctx, principal, "/v1/position-snapshot", url.Values{"account_id": {accountID}}, &envelope, accountPositionSnapshotLimit); err != nil {
		return accountpositions.SourceSnapshot{}, fmt.Errorf("read Gateway position snapshot: %w", err)
	}
	if envelope.Data.AccountID != accountID {
		return accountpositions.SourceSnapshot{}, fmt.Errorf("Gateway position snapshot response is invalid")
	}
	return envelope.Data, nil
}

func (client *Client) WatchAccountEvents(
	ctx context.Context,
	principal string,
	accountID string,
	cursor string,
	apply func(accountpositions.SourceEvent) error,
) error {
	if strings.TrimSpace(principal) == "" || strings.TrimSpace(accountID) == "" || apply == nil {
		return fmt.Errorf("Gateway account stream scope is invalid")
	}
	origin, token := client.originFor("/v1/ws")
	parsed, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("Gateway account stream origin is invalid")
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return fmt.Errorf("Gateway account stream origin is invalid")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/ws"
	query := parsed.Query()
	query.Set("account_id", accountID)
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	parsed.RawQuery = query.Encode()
	headers := http.Header{
		"Authorization":      []string{"Bearer " + string(token)},
		"X-Bolter-Principal": []string{principal},
	}
	connection, response, err := websocket.Dial(ctx, parsed.String(), &websocket.DialOptions{HTTPClient: client.httpClient, HTTPHeader: headers})
	if err != nil {
		if response != nil {
			return fmt.Errorf("Gateway account stream rejected with status %d", response.StatusCode)
		}
		return fmt.Errorf("Gateway account stream connection failed")
	}
	defer connection.CloseNow()
	if err := apply(accountpositions.SourceEvent{Kind: accountpositions.SourceEventConnected}); err != nil {
		return err
	}
	for {
		var event gatewayPrivateEvent
		if err := wsjson.Read(ctx, connection, &event); err != nil {
			return err
		}
		projected, err := projectGatewayAccountEvent(event)
		if err != nil {
			return err
		}
		if err := apply(projected); err != nil {
			return err
		}
	}
}

type gatewayPrivateEvent struct {
	BootID          string          `json:"boot_id"`
	Sequence        uint64          `json:"sequence"`
	EventID         string          `json:"event_id"`
	AccountID       string          `json:"account_id"`
	ResourceVersion uint64          `json:"resource_version"`
	Kind            string          `json:"kind"`
	OccurredAt      time.Time       `json:"occurred_at"`
	Payload         json.RawMessage `json:"payload"`
}

func projectGatewayPositionEvent(event gatewayPrivateEvent) (accountpositions.SourceEvent, error) {
	return projectGatewayAccountEvent(event)
}

func projectGatewayAccountEvent(event gatewayPrivateEvent) (accountpositions.SourceEvent, error) {
	if strings.TrimSpace(event.BootID) == "" || event.Sequence == 0 || strings.TrimSpace(event.AccountID) == "" {
		return accountpositions.SourceEvent{}, fmt.Errorf("Gateway private event identity is invalid")
	}
	projected := accountpositions.SourceEvent{
		BootID: event.BootID, Sequence: event.Sequence,
		Cursor:          event.BootID + ":" + strconv.FormatUint(event.Sequence, 10),
		ResourceVersion: event.ResourceVersion, AccountID: event.AccountID, OccurredAt: event.OccurredAt.UTC(),
	}
	switch event.Kind {
	case "resync_required":
		var payload struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil {
			return accountpositions.SourceEvent{}, fmt.Errorf("Gateway resync event is invalid")
		}
		projected.Kind, projected.Reason = accountpositions.SourceEventResync, payload.Reason
		return projected, nil
	case "position":
		var payload struct {
			AccountID    string                          `json:"account_id"`
			RouteID      string                          `json:"route_id"`
			InstrumentID string                          `json:"instrument_id"`
			PositionSlot string                          `json:"position_slot"`
			Operation    accountpositions.Operation      `json:"operation"`
			Position     accountpositions.SourcePosition `json:"position"`
			Reason       string                          `json:"reason,omitempty"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil || payload.AccountID != event.AccountID ||
			(payload.Operation != accountpositions.OperationUpsert && payload.Operation != accountpositions.OperationRemove) {
			return accountpositions.SourceEvent{}, fmt.Errorf("Gateway position event is invalid")
		}
		projected.Kind, projected.Operation = accountpositions.SourceEventPosition, payload.Operation
		projected.RouteID, projected.InstrumentID, projected.PositionSlot = payload.RouteID, payload.InstrumentID, payload.PositionSlot
		projected.Position, projected.Reason = payload.Position, payload.Reason
		return projected, nil
	case "order", "fill", "balance", "account_health":
		if len(event.Payload) == 0 || !json.Valid(event.Payload) {
			return accountpositions.SourceEvent{}, fmt.Errorf("Gateway account event payload is invalid")
		}
		projected.Kind = event.Kind
		projected.Payload = append(json.RawMessage(nil), event.Payload...)
		return projected, nil
	default:
		return accountpositions.SourceEvent{}, fmt.Errorf("Gateway private event kind is invalid")
	}
}
