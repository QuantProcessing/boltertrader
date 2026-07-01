package contracttest

import (
	"context"
	"fmt"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/contract"
)

func TestRunPerpCapabilitySuiteSupportsDeclaredUnsupportedProbes(t *testing.T) {
	suite := PerpCapabilitySuite{
		Venue: "FAKE",
		Market: MarketCapabilities{
			OrderBook: CapabilityProbe{
				Support: Unsupported("fake market has no order book snapshot"),
				Probe: func(context.Context) error {
					return fmt.Errorf("fake order book: %w", contract.ErrNotSupported)
				},
			},
			SubscribeTrades: CapabilityProbe{Support: Supported(), Probe: func(context.Context) error { return nil }},
		},
	}

	RunPerpCapabilitySuite(t, suite)
}

func TestRunSpotCapabilitySuiteSupportsDeclaredUnsupportedMarginOps(t *testing.T) {
	suite := SpotCapabilitySuite{
		Venue: "FAKE",
		Account: AccountCapabilities{
			Balances: CapabilityProbe{Support: Supported(), Probe: func(context.Context) error { return nil }},
			SetLeverage: CapabilityProbe{
				Support: Unsupported("spot cash accounts do not support leverage"),
				Probe: func(context.Context) error {
					return fmt.Errorf("fake spot leverage: %w", contract.ErrNotSupported)
				},
			},
			SetCrossMargin: CapabilityProbe{
				Support: Unsupported("spot cash accounts do not support margin mode"),
				Probe: func(context.Context) error {
					return fmt.Errorf("fake spot cross margin: %w", contract.ErrNotSupported)
				},
			},
			SetIsolatedMargin: CapabilityProbe{
				Support: Unsupported("spot cash accounts do not support margin mode"),
				Probe: func(context.Context) error {
					return fmt.Errorf("fake spot isolated margin: %w", contract.ErrNotSupported)
				},
			},
		},
	}

	RunSpotCapabilitySuite(t, suite)
}
