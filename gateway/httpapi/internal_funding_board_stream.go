package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/fundingboard"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const fundingBoardWriteTimeout = 10 * time.Second

// Source owns collection; Authority owns the wire projection and catalog join.
type InternalFundingBoardChanges interface {
	Subscribe(context.Context) (<-chan fundingboard.Change, func(), error)
}

type internalFundingBoardEvent struct {
	EventType  string    `json:"eventType"`
	Sequence   uint64    `json:"sequence"`
	ObservedAt time.Time `json:"observedAt"`
	Data       any       `json:"data,omitempty"`
	Reason     string    `json:"reason,omitempty"`
}

func (handler *InternalHandler) fundingBoardStream(w http.ResponseWriter, r *http.Request) {
	if handler.fundingBoardChanges == nil || len(r.URL.Query()) != 0 {
		if handler.fundingBoardChanges == nil {
			handler.writeError(w, r, http.StatusServiceUnavailable, "FUNDING_BOARD_STREAM_UNAVAILABLE", true)
		} else {
			handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		}
		return
	}
	changes, cancel, err := handler.fundingBoardChanges.Subscribe(r.Context())
	if err != nil {
		handler.writeError(w, r, http.StatusServiceUnavailable, "FUNDING_BOARD_STREAM_UNAVAILABLE", true)
		return
	}
	defer cancel()
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(handler.bodyLimit)
	ctx := connection.CloseRead(r.Context())

	first := true
	var lastSequence uint64
	for {
		select {
		case <-ctx.Done():
			return
		case change, open := <-changes:
			if !open {
				_ = writeFundingBoardEvent(ctx, connection, internalFundingBoardEvent{
					EventType: "resync_required", Sequence: lastSequence, ObservedAt: time.Now().UTC(), Reason: "consumer-gap",
				})
				_ = connection.Close(websocket.StatusPolicyViolation, "funding_board_resnapshot_required")
				return
			}
			response, executeErr := handler.authority.Execute(ctx, AuthorityRequest{Operation: AuthorityFundingBoard})
			if executeErr != nil {
				_ = writeFundingBoardEvent(ctx, connection, internalFundingBoardEvent{
					EventType: "resync_required", Sequence: change.Sequence, ObservedAt: time.Now().UTC(), Reason: "projection-unavailable",
				})
				return
			}
			eventType := string(change.Kind)
			if first {
				eventType = "snapshot"
				first = false
			}
			if err := writeFundingBoardEvent(ctx, connection, internalFundingBoardEvent{
				EventType: eventType, Sequence: change.Sequence, ObservedAt: time.Now().UTC(), Data: response.Body,
			}); err != nil {
				return
			}
			lastSequence = change.Sequence
		}
	}
}

func writeFundingBoardEvent(ctx context.Context, connection *websocket.Conn, event internalFundingBoardEvent) error {
	writeCtx, cancel := context.WithTimeout(ctx, fundingBoardWriteTimeout)
	defer cancel()
	return wsjson.Write(writeCtx, connection, event)
}
