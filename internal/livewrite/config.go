package livewrite

import (
	"fmt"
	"os"
	"strings"
)

// EnvName is the single operator switch shared by the gateway and trading
// processes. Both processes must make the same admission decision or an order
// can pass one boundary only to be rejected by the next one.
const EnvName = "BT_LIVE_PRIVATE_WRITES_ENABLED"

// Enabled reads the live-write switch. Live writes are enabled by default;
// false is the explicit operator kill switch.
func Enabled() (bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvName))) {
	case "", "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false", EnvName)
	}
}
