package contract

import "errors"

var (
	// ErrNotSupported marks a capability that is intentionally outside an
	// adapter, venue, or runtime component's declared surface.
	ErrNotSupported = errors.New("not supported")

	// ErrVenueRejected marks a definitive venue-side command rejection after the
	// adapter boundary. Wrapping it tells the runtime the command is terminal, not
	// ambiguous/recoverable.
	ErrVenueRejected = errors.New("venue rejected command")

	// ErrPreparedStateUnavailable marks a pre-venue failure to consume state
	// previously created by venue-backed pre-trade validation. The runtime records
	// this as local denial rather than an ambiguous write.
	ErrPreparedStateUnavailable = errors.New("prepared execution state unavailable")
)
