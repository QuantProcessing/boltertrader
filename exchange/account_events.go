package exchange

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type AccountEventKind string

const (
	AccountEventOrder          AccountEventKind = "order"
	AccountEventFill           AccountEventKind = "fill"
	AccountEventBalance        AccountEventKind = "balance"
	AccountEventPosition       AccountEventKind = "position"
	AccountEventStreamStatus   AccountEventKind = "stream_status"
	AccountEventResyncRequired AccountEventKind = "resync_required"
)

type AccountEventPhase string

const (
	AccountEventPhaseSnapshot AccountEventPhase = "snapshot"
	AccountEventPhaseLive     AccountEventPhase = "live"
)

// AccountRoute is the native private-stream boundary. Instrument is
// intentionally absent: a Gateway account route owns one private event stream
// regardless of catalog size.
type AccountRoute struct {
	AccountID    string      `json:"accountId"`
	RouteID      string      `json:"routeId"`
	Venue        Venue       `json:"venue"`
	Environment  Environment `json:"environment"`
	Product      Product     `json:"product"`
	Settlement   string      `json:"settlement,omitempty"`
	NativeFamily string      `json:"nativeFamily"`
}

func (route AccountRoute) Validate() error {
	if strings.TrimSpace(route.AccountID) == "" || strings.TrimSpace(route.RouteID) == "" ||
		!knownVenue(route.Venue) || route.Environment != EnvironmentLive ||
		(route.Product != ProductSpot && route.Product != ProductPerp) || strings.TrimSpace(route.NativeFamily) == "" {
		return fmt.Errorf("account route is invalid")
	}
	return nil
}

type AccountEvent struct {
	Kind           AccountEventKind   `json:"kind"`
	Route          AccountRoute       `json:"route"`
	InstrumentID   string             `json:"instrumentId,omitempty"`
	Generation     uint64             `json:"generation"`
	Sequence       uint64             `json:"sequence"`
	SourceSequence string             `json:"sourceSequence,omitempty"`
	Phase          AccountEventPhase  `json:"phase"`
	ObservedAt     time.Time          `json:"observedAt"`
	Order          *OrderEvent        `json:"order,omitempty"`
	Fill           *FillEvent         `json:"fill,omitempty"`
	Balance        *BalanceEvent      `json:"balance,omitempty"`
	Position       *PositionEvent     `json:"position,omitempty"`
	Status         *StreamStatusEvent `json:"status,omitempty"`
	Reason         string             `json:"reason,omitempty"`
}

func (event AccountEvent) Validate() error {
	if err := event.Route.Validate(); err != nil || event.ObservedAt.IsZero() || event.Generation == 0 || event.Sequence == 0 {
		return fmt.Errorf("account event identity is invalid")
	}
	if event.Phase != AccountEventPhaseSnapshot && event.Phase != AccountEventPhaseLive {
		return fmt.Errorf("account event phase is invalid")
	}
	payloads := 0
	for _, present := range []bool{event.Order != nil, event.Fill != nil, event.Balance != nil, event.Position != nil, event.Status != nil} {
		if present {
			payloads++
		}
	}
	switch event.Kind {
	case AccountEventOrder:
		if event.Order == nil || payloads != 1 {
			return fmt.Errorf("account order event is invalid")
		}
	case AccountEventFill:
		if event.Fill == nil || payloads != 1 {
			return fmt.Errorf("account fill event is invalid")
		}
	case AccountEventBalance:
		if event.Balance == nil || payloads != 1 {
			return fmt.Errorf("account balance event is invalid")
		}
	case AccountEventPosition:
		if event.Position == nil || payloads != 1 {
			return fmt.Errorf("account position event is invalid")
		}
	case AccountEventStreamStatus:
		if event.Status == nil || payloads != 1 {
			return fmt.Errorf("account status event is invalid")
		}
	case AccountEventResyncRequired:
		if strings.TrimSpace(event.Reason) == "" || payloads != 0 {
			return fmt.Errorf("account resync event is invalid")
		}
	default:
		return fmt.Errorf("account event kind is invalid")
	}
	return nil
}

type WatchAccountEventsRequest struct {
	Route   AccountRoute `json:"route"`
	Options WatchOptions `json:"options,omitempty"`
}

type AccountInstrument struct {
	InstrumentID string     `json:"instrumentId"`
	Instrument   Instrument `json:"instrument"`
}

type AccountWebSocket interface {
	WatchAccountEvents(context.Context, WatchAccountEventsRequest) (Subscription[AccountEvent], error)
}

// AccountInstrumentIndexer injects the immutable native-to-canonical index
// when a route client is assembled. It is deliberately separate from the
// subscription request: catalog size must not define private stream scope.
type AccountInstrumentIndexer interface {
	SetAccountInstruments([]AccountInstrument) error
}
