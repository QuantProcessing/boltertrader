package exec

import (
	"context"
	"errors"
	"io"
	"net"
	"os"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
)

type OutcomeClass string

const (
	OutcomeLocalDenied             OutcomeClass = "local_denied"
	OutcomeUnsupported             OutcomeClass = "unsupported"
	OutcomeDefinitiveVenueRejected OutcomeClass = "definitive_venue_rejected"
	OutcomeConfirmedAccepted       OutcomeClass = "confirmed_accepted"
	OutcomeAmbiguous               OutcomeClass = "ambiguous"
)

var (
	ErrVenueRejected   = errors.New("exec: definitive venue rejection")
	ErrAmbiguousResult = errors.New("exec: ambiguous venue result")
)

type Outcome struct {
	Class OutcomeClass
	Sent  bool
	Err   error
}

func ClassifySubmitResult(sent bool, order *model.Order, err error) Outcome {
	if !sent {
		return classifyPreBoundary(err)
	}
	if err == nil && order != nil {
		if order.Status == enums.StatusRejected || order.Status == enums.StatusExpired {
			return Outcome{Class: OutcomeDefinitiveVenueRejected, Sent: true}
		}
		return Outcome{Class: OutcomeConfirmedAccepted, Sent: true}
	}
	return classifyPostBoundary(err)
}

func ClassifyCommandResult(sent bool, err error) Outcome {
	if !sent {
		return classifyPreBoundary(err)
	}
	if err == nil {
		return Outcome{Class: OutcomeConfirmedAccepted, Sent: true}
	}
	return classifyPostBoundary(err)
}

func IsAmbiguousError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrAmbiguousResult) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func DefinitiveReject(reason string) error {
	if reason == "" {
		return ErrVenueRejected
	}
	return errors.Join(ErrVenueRejected, errors.New(reason))
}

func classifyPreBoundary(err error) Outcome {
	if errors.Is(err, contract.ErrNotSupported) {
		return Outcome{Class: OutcomeUnsupported, Sent: false, Err: err}
	}
	return Outcome{Class: OutcomeLocalDenied, Sent: false, Err: err}
}

func classifyPostBoundary(err error) Outcome {
	if errors.Is(err, contract.ErrNotSupported) {
		return Outcome{Class: OutcomeUnsupported, Sent: true, Err: err}
	}
	if errors.Is(err, ErrVenueRejected) || errors.Is(err, contract.ErrVenueRejected) {
		return Outcome{Class: OutcomeDefinitiveVenueRejected, Sent: true, Err: err}
	}
	if IsAmbiguousError(err) {
		return Outcome{Class: OutcomeAmbiguous, Sent: true, Err: err}
	}
	return Outcome{Class: OutcomeAmbiguous, Sent: true, Err: err}
}
