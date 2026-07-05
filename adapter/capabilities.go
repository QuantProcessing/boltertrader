package adapter

type CapabilityRow struct {
	Venue              string
	Product            string
	MarketStream       bool
	ExecutionStream    bool
	AccountStream      bool
	Submit             bool
	Cancel             bool
	Modify             bool
	OrderStatusReports string
	FillReports        string
	PositionReports    string
	MassStatus         string
	SingleOrderQuery   string
	OpenOnlyCaveat     bool
	LatencyTimestamps  bool
	DemoTarget         string
}

func CapabilityMatrix() []CapabilityRow {
	return []CapabilityRow{
		{
			Venue:              "BINANCE",
			Product:            "USD-M Perp",
			MarketStream:       true,
			ExecutionStream:    true,
			AccountStream:      true,
			Submit:             true,
			Cancel:             true,
			Modify:             true,
			OrderStatusReports: "open orders",
			FillReports:        "unsupported",
			PositionReports:    "account snapshot",
			MassStatus:         "open-order mass status",
			SingleOrderQuery:   "unsupported",
			OpenOnlyCaveat:     true,
			LatencyTimestamps:  false,
			DemoTarget:         "make test-binance-demo-runtime-perp",
		},
		{
			Venue:              "BINANCE",
			Product:            "Spot",
			MarketStream:       true,
			ExecutionStream:    true,
			AccountStream:      true,
			Submit:             true,
			Cancel:             true,
			Modify:             true,
			OrderStatusReports: "open orders",
			FillReports:        "unsupported",
			PositionReports:    "unsupported",
			MassStatus:         "open-order mass status",
			SingleOrderQuery:   "unsupported",
			OpenOnlyCaveat:     true,
			LatencyTimestamps:  false,
			DemoTarget:         "make test-binance-demo-spot",
		},
		{
			Venue:              "OKX",
			Product:            "USDT-linear SWAP",
			MarketStream:       true,
			ExecutionStream:    true,
			AccountStream:      true,
			Submit:             true,
			Cancel:             true,
			Modify:             true,
			OrderStatusReports: "open orders",
			FillReports:        "unsupported",
			PositionReports:    "account snapshot",
			MassStatus:         "open-order mass status",
			SingleOrderQuery:   "unsupported",
			OpenOnlyCaveat:     true,
			LatencyTimestamps:  false,
			DemoTarget:         "make test-okx-demo-runtime-perp",
		},
		{
			Venue:              "OKX",
			Product:            "Spot cash",
			MarketStream:       true,
			ExecutionStream:    true,
			AccountStream:      true,
			Submit:             true,
			Cancel:             true,
			Modify:             true,
			OrderStatusReports: "open orders",
			FillReports:        "unsupported",
			PositionReports:    "unsupported",
			MassStatus:         "open-order mass status",
			SingleOrderQuery:   "unsupported",
			OpenOnlyCaveat:     true,
			LatencyTimestamps:  false,
			DemoTarget:         "make test-okx-demo-runtime-spot",
		},
		{
			Venue:              "HYPERLIQUID",
			Product:            "Spot cash",
			MarketStream:       false,
			ExecutionStream:    false,
			AccountStream:      false,
			Submit:             true,
			Cancel:             true,
			Modify:             true,
			OrderStatusReports: "open orders",
			FillReports:        "unsupported",
			PositionReports:    "unsupported",
			MassStatus:         "open-order mass status",
			SingleOrderQuery:   "open order filter",
			OpenOnlyCaveat:     true,
			LatencyTimestamps:  false,
			DemoTarget:         "make test-hyperliquid-testnet-runtime-spot",
		},
		{
			Venue:              "HYPERLIQUID",
			Product:            "Perp",
			MarketStream:       true,
			ExecutionStream:    true,
			AccountStream:      true,
			Submit:             true,
			Cancel:             true,
			Modify:             true,
			OrderStatusReports: "open orders",
			FillReports:        "unsupported",
			PositionReports:    "account snapshot",
			MassStatus:         "open-order mass status",
			SingleOrderQuery:   "venue order id",
			OpenOnlyCaveat:     true,
			LatencyTimestamps:  false,
			DemoTarget:         "make test-hyperliquid-testnet-runtime-perp",
		},
		{
			Venue:              "HYPERLIQUID",
			Product:            "HIP-3 Perp",
			MarketStream:       true,
			ExecutionStream:    true,
			AccountStream:      true,
			Submit:             true,
			Cancel:             true,
			Modify:             true,
			OrderStatusReports: "open orders",
			FillReports:        "unsupported",
			PositionReports:    "account snapshot",
			MassStatus:         "open-order mass status",
			SingleOrderQuery:   "venue order id",
			OpenOnlyCaveat:     true,
			LatencyTimestamps:  false,
			DemoTarget:         "make test-hyperliquid-testnet-runtime-hip3",
		},
	}
}
