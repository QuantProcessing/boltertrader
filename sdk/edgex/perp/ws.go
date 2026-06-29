package perp

import "time"

const (
	WSBaseURL = "wss://edgex-quote-prod-v2.edgex.exchange/api/v1"

	// Reconnection constants
	maxReconnectAttempts = 10
	reconnectInterval    = 1 * time.Second
	maxReconnectInterval = 30 * time.Second
)
