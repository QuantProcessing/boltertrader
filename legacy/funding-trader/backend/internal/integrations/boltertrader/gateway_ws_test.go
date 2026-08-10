package boltertrader

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
)

func TestPrivateEventStreamDetectsSequenceGapBootChangeAndResync(t *testing.T) {
	t.Run("sequence gap", func(t *testing.T) {
		stream := &PrivateEventStream{bootID: "018f0000-0000-7000-8000-000000000001", sequence: 1}
		event := validPrivateEvent(3, "order")
		event.BootID = stream.bootID
		event, err := stream.accept(event)
		if event.Sequence != 3 || !errors.Is(err, ErrGatewayResnapshotNeeded) {
			t.Fatalf("gap event=%+v err=%v", event, err)
		}
	})
	t.Run("boot change", func(t *testing.T) {
		stream := &PrivateEventStream{bootID: "018f0000-0000-7000-8000-000000000001", sequence: 1}
		event := validPrivateEvent(1, "order")
		event.BootID = "018f0000-0000-7000-8000-000000000002"
		_, err := stream.accept(event)
		if !errors.Is(err, ErrGatewayResnapshotNeeded) {
			t.Fatalf("boot err=%v", err)
		}
	})
	t.Run("resync required", func(t *testing.T) {
		stream := &PrivateEventStream{}
		event := validPrivateEvent(1, "resync_required")
		event.Payload = []byte(`{"reason":"cursor_evicted","resources":["orders"]}`)
		_, err := stream.accept(event)
		var resync *ResnapshotRequiredError
		if !errors.As(err, &resync) || resync.Reason != "cursor-evicted" {
			t.Fatalf("resync err=%v", err)
		}
	})
	t.Run("resnapshot state is sticky", func(t *testing.T) {
		stream := &PrivateEventStream{bootID: "018f0000-0000-7000-8000-000000000001", sequence: 1}
		gap := validPrivateEvent(3, "order")
		gap.BootID = stream.bootID
		if _, err := stream.accept(gap); !errors.Is(err, ErrGatewayResnapshotNeeded) {
			t.Fatalf("initial gap err=%v", err)
		}
		recovered := validPrivateEvent(2, "order")
		recovered.BootID = stream.bootID
		if _, err := stream.accept(recovered); !errors.Is(err, ErrGatewayResnapshotNeeded) {
			t.Fatalf("post-gap err=%v, want sticky resnapshot requirement", err)
		}
	})
}

func TestPrivateEventStreamRejectsMalformedOrCrossAccountEvents(t *testing.T) {
	valid := validPrivateEvent(1, "order")
	tests := []struct {
		name  string
		event PrivateEvent
	}{
		{name: "empty boot id", event: func() PrivateEvent {
			event := valid
			event.BootID = ""
			return event
		}()},
		{name: "zero sequence", event: func() PrivateEvent {
			event := valid
			event.Sequence = 0
			return event
		}()},
		{name: "unknown kind", event: func() PrivateEvent {
			event := valid
			event.Kind = "credential"
			return event
		}()},
		{name: "missing event id", event: func() PrivateEvent {
			event := valid
			event.EventID = ""
			return event
		}()},
		{name: "negative resource version", event: func() PrivateEvent {
			event := valid
			event.ResourceVersion = -1
			return event
		}()},
		{name: "zero occurred at", event: func() PrivateEvent {
			event := valid
			event.OccurredAt = time.Time{}
			return event
		}()},
		{name: "invalid payload", event: func() PrivateEvent {
			event := valid
			event.Payload = []byte(`{`)
			return event
		}()},
		{name: "wrong account", event: func() PrivateEvent {
			event := valid
			event.AccountID = "other-acct"
			return event
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := &PrivateEventStream{accountID: "acct"}
			if _, err := stream.accept(test.event); err == nil {
				t.Fatalf("accepted invalid event: %+v", test.event)
			}
		})
	}
}

func TestPrivateEventsTargetUsesBearerAndFilters(t *testing.T) {
	const token = "ws-secret"
	client, err := NewGatewayClient(GatewayClientOptions{BaseURL: "http://gateway.local/base", Token: token, Mode: config.Test})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	target, headers, err := client.privateEventsTarget(PrivateEventSubscribeRequest{
		AccountID: "acct", Cursor: "018f0000-0000-7000-8000-000000000001:1", Kinds: []string{"order", "fill"},
	})
	if err != nil {
		t.Fatalf("privateEventsTarget: %v", err)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	if parsed.Scheme != "ws" || parsed.Path != "/base/v1/ws" {
		t.Fatalf("target = %s", target)
	}
	query := parsed.Query()
	if query.Get("account_id") != "acct" || query.Get("cursor") == "" || len(query["kind"]) != 2 {
		t.Fatalf("query = %s", parsed.RawQuery)
	}
	if headers.Get("Authorization") != "Bearer "+token {
		t.Fatalf("Authorization = %q", headers.Get("Authorization"))
	}
	for _, kind := range []string{"credential", "resync_required"} {
		if _, _, err := client.privateEventsTarget(PrivateEventSubscribeRequest{
			AccountID: "acct", Kinds: []string{kind},
		}); err == nil {
			t.Fatalf("invalid private event subscription kind %q was accepted", kind)
		}
	}
}

func TestPrivateEventStreamAppliesConfiguredMessageLimit(t *testing.T) {
	limiter := &recordingReadLimiter{}
	applyPrivateEventReadLimit(limiter, 128)
	if limiter.limit != 128 {
		t.Fatalf("read limit = %d, want 128", limiter.limit)
	}
}

type recordingReadLimiter struct {
	limit int64
}

func (limiter *recordingReadLimiter) SetReadLimit(limit int64) {
	limiter.limit = limit
}

func validPrivateEvent(sequence uint64, kind string) PrivateEvent {
	return PrivateEvent{
		BootID:          "018f0000-0000-7000-8000-000000000001",
		Sequence:        sequence,
		EventID:         "018f0000-0000-7000-8000-000000000002",
		AccountID:       "acct",
		ResourceVersion: 1,
		Kind:            kind,
		OccurredAt:      time.Unix(1, 0).UTC(),
		Payload:         []byte(`{}`),
	}
}
