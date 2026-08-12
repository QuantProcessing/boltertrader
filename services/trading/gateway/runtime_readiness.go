package gateway

import (
	"context"
	"fmt"
)

// CheckRuntimeRoute answers whether the Gateway's private runtime plane is
// reachable. It is the only honest readiness signal this side can give about
// accounts: they are the Gateway's record now, so the question worth asking is
// whether their owner responds, not whether we hold a copy.
func (client *Client) CheckRuntimeRoute(ctx context.Context) error {
	var result readEnvelope[struct {
		Ready bool `json:"ready"`
	}]
	if err := client.read(ctx, "/internal/v1/runtime/readiness", nil, &result); err != nil {
		return fmt.Errorf("probe Gateway runtime route: %w", err)
	}
	if !result.Data.Ready {
		return fmt.Errorf("Gateway runtime route is unavailable")
	}
	return nil
}
