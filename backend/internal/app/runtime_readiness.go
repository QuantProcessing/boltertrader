package app

import (
	"context"
	"fmt"
)

type runtimeRouteProbe interface {
	CheckRuntimeRoute(context.Context) error
}

func (adapter *gatewayAdapter) CheckRuntimeRoute(ctx context.Context) error {
	var result gatewayReadEnvelope[struct {
		Ready bool `json:"ready"`
	}]
	if err := adapter.read(ctx, "/internal/v1/runtime/readiness", nil, &result); err != nil {
		return fmt.Errorf("probe Gateway runtime route: %w", err)
	}
	if !result.Data.Ready {
		return fmt.Errorf("Gateway runtime route is unavailable")
	}
	return nil
}
