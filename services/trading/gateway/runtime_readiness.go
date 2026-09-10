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
	ready, err := client.protocol.RuntimeReady(ctx)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("Gateway runtime route is unavailable")
	}
	return nil
}
