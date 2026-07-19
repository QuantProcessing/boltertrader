package exchange

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNormalizedErrorKindsAndMetadata(t *testing.T) {
	tests := []struct {
		kind   ErrorKind
		target error
	}{
		{KindInvalidConfig, ErrInvalidConfig},
		{KindInvalidRequest, ErrInvalidRequest},
		{KindAuthentication, ErrAuthentication},
		{KindPermission, ErrPermission},
		{KindRateLimit, ErrRateLimit},
		{KindNotFound, ErrNotFound},
		{KindUnsupported, ErrUnsupported},
		{KindVenueRejected, ErrVenueRejected},
		{KindTransport, ErrTransport},
		{KindAmbiguousOutcome, ErrAmbiguousOutcome},
		{KindMalformedResponse, ErrMalformedResponse},
		{KindCanceled, ErrCanceled},
		{KindDeadlineExceeded, ErrDeadlineExceeded},
		{KindSubscriptionGap, ErrSubscriptionGap},
		{KindSubscriptionClosed, ErrSubscriptionClosed},
	}

	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			details := ErrorDetails{
				Venue:       VenueOKX,
				Product:     ProductPerp,
				Operation:   "PlaceOrder",
				Code:        "safe-code",
				SafeMessage: "safe-message",
				RetryAfter:  3 * time.Second,
			}
			err := NewError(test.kind, details)
			if err.Kind() != test.kind {
				t.Fatalf("kind = %q, want %q", err.Kind(), test.kind)
			}
			if err.Details() != details {
				t.Fatalf("details = %#v, want %#v", err.Details(), details)
			}
			if !errors.Is(err, test.target) {
				t.Fatalf("errors.Is(%v, %v) = false", err, test.target)
			}
			text := err.Error()
			for _, required := range []string{"okx", "perp", "PlaceOrder", "safe-code", "safe-message"} {
				if !strings.Contains(text, required) {
					t.Fatalf("error text %q missing %q", text, required)
				}
			}
			if _, ok := any(err).(interface{ Unwrap() error }); ok {
				t.Fatal("normalized Error must not expose an Unwrap chain")
			}
			if errors.Unwrap(err) != nil {
				t.Fatal("normalized Error unexpectedly unwraps")
			}
		})
	}
}

func TestContextSentinelDetectionWithoutUnwrap(t *testing.T) {
	canceled := NewError(KindCanceled, ErrorDetails{Operation: "OrderBook"})
	if !errors.Is(canceled, context.Canceled) {
		t.Fatal("canceled error must match context.Canceled")
	}

	deadline := NewError(KindDeadlineExceeded, ErrorDetails{Operation: "Candles"})
	if !errors.Is(deadline, context.DeadlineExceeded) {
		t.Fatal("deadline error must match context.DeadlineExceeded")
	}
}

func TestNormalizedErrorFormattingContainsNoHiddenCause(t *testing.T) {
	canary := "SECRET-CANARY-DO-NOT-LEAK"
	err := NewError(KindTransport, ErrorDetails{
		Venue:       VenueBinance,
		Product:     ProductSpot,
		Operation:   "OrderBook",
		SafeMessage: "request failed",
	})
	for _, formatted := range []string{
		fmt.Sprintf("%s", err),
		fmt.Sprintf("%v", err),
		fmt.Sprintf("%+v", err),
		fmt.Sprintf("%#v", err),
	} {
		if strings.Contains(formatted, canary) {
			t.Fatalf("formatted error leaked canary: %s", formatted)
		}
	}
}
