package domain_test

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

func TestWriteFenceRequiresRouteID(t *testing.T) {
	fence := domain.WriteFence{AccountFence: domain.AccountFence{AccountID: "acct_1", RuntimeID: "connection-1"}}
	if err := fence.Validate(); err == nil {
		t.Fatal("missing route id accepted")
	}
}

func TestWriteFenceRequiresAccountAndConnection(t *testing.T) {
	valid := domain.WriteFence{AccountFence: domain.AccountFence{AccountID: "acct_1", RuntimeID: "connection-1"}, RouteID: "route_1"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid fence: %v", err)
	}
	tests := map[string]domain.WriteFence{
		"account":           {AccountFence: domain.AccountFence{RuntimeID: "connection-1"}, RouteID: "route_1"},
		"connection":        {AccountFence: domain.AccountFence{AccountID: "acct_1"}, RouteID: "route_1"},
		"unsafe connection": {AccountFence: domain.AccountFence{AccountID: "acct_1", RuntimeID: "bad\nconnection"}, RouteID: "route_1"},
	}

	for name, fence := range tests {
		t.Run(name, func(t *testing.T) {
			if err := fence.Validate(); err == nil {
				t.Fatal("incomplete fence accepted")
			}
		})
	}
}
