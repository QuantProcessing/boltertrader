package executionruntime

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
)

type Publisher struct {
	stream *uistream.Service
	scope  string
}

func NewPublisher(stream *uistream.Service) *Publisher {
	return &Publisher{stream: stream, scope: "all"}
}

func (publisher *Publisher) Publish(_ context.Context, events []executionservice.CommittedEvent) error {
	if publisher == nil || publisher.stream == nil {
		return fmt.Errorf("uistream publisher is not configured")
	}
	for _, event := range events {
		if strings.TrimSpace(event.ID) == "" {
			continue
		}
		sequence, err := strconv.ParseUint(event.ID, 10, 64)
		if err != nil || sequence == 0 {
			return fmt.Errorf("committed event id is invalid")
		}
		if err := publisher.stream.BroadcastStored(uistream.StoredEvent{
			Sequence:   sequence,
			Scope:      publisher.scope,
			Kind:       event.Kind,
			Data:       cloneJSON(event.Payload),
			OccurredAt: event.Occurred,
		}); err != nil {
			return err
		}
	}
	return nil
}
