package account

import (
	"context"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type probeSpotClient struct {
	exchange.SpotClient
	balanceCalls int
}

func (client *probeSpotClient) Balances(context.Context) ([]exchange.Balance, error) {
	client.balanceCalls++
	return nil, nil
}

type probeClientHandle struct {
	spotClient   *probeSpotClient
	refreshCalls int
}

func (handle *probeClientHandle) refresh(context.Context) ([]exchange.Instrument, error) {
	handle.refreshCalls++
	return nil, nil
}
func (*probeClientHandle) close() error { return nil }
func (handle *probeClientHandle) spot() (exchange.SpotClient, bool) {
	return handle.spotClient, true
}
func (*probeClientHandle) perp() (exchange.PerpClient, bool) { return nil, false }
func (*probeClientHandle) exactLookupReady() bool            { return true }

func TestFactoryCandidateProbeUsesPrivateReadinessWithoutRefreshingPublicInstruments(t *testing.T) {
	spot := &probeSpotClient{}
	handle := &probeClientHandle{spotClient: spot}
	route := factoryCandidateRoute{handle: handle}

	if err := route.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if spot.balanceCalls != 1 || handle.refreshCalls != 0 {
		t.Fatalf("balance calls=%d instrument refreshes=%d", spot.balanceCalls, handle.refreshCalls)
	}
}
