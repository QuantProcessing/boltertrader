package contract

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
)

type Capabilities struct {
	Venue     string
	Products  []ProductCapability
	Reports   ReportCapabilities
	Streaming StreamCapabilities
	Trading   TradingCapabilities
	Latency   LatencyCapabilities
}

type ProductCapability struct {
	Kind    enums.InstrumentKind
	Trading bool
	Market  bool
	Account bool
}

type ReportCapabilities struct {
	SingleOrderStatus         bool
	OpenOrders                bool
	OrderHistory              bool
	FillHistory               bool
	PositionReports           bool
	AccountBalanceSnapshots   bool
	MaxLookback               time.Duration
	ClosedOrderRetention      string
	DefinitiveNotFound        bool
	OpenOnlyNotFoundAmbiguous bool
}

type StreamCapabilities struct {
	Market    bool
	Execution bool
	Account   bool
	Health    bool
}

type TradingCapabilities struct {
	Submit    bool
	Cancel    bool
	CancelAll bool
	Modify    bool
}

type LatencyCapabilities struct {
	VenueTimestamps   bool
	AdapterTimestamps bool
	SequenceNumbers   bool
}
