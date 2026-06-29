package lighter

import "fmt"

// Common error types
var (
	ErrInvalidSignature = fmt.Errorf("invalid signature")
	ErrOrderNotFound    = fmt.Errorf("order not found")
)
