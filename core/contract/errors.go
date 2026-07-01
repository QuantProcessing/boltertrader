package contract

import "errors"

// ErrNotSupported marks a capability that is intentionally outside an adapter,
// venue, or runtime component's declared surface.
var ErrNotSupported = errors.New("not supported")
