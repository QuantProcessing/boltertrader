package contract

import "github.com/QuantProcessing/boltertrader/core/enums"

type Capabilities struct {
	Venue         string
	Products      []ProductCapability
	Reports       ReportCapabilities
	Streaming     StreamCapabilities
	Trading       TradingCapabilities
	ReferenceData ReferenceDataCapabilities
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
	OpenOnlyNotFoundAmbiguous bool
}

type StreamCapabilities struct {
	Market       bool
	Execution    bool
	Account      bool
	AccountState bool
	Health       bool
}

type TradingCapabilities struct {
	Submit    bool
	Cancel    bool
	CancelAll bool
	Modify    bool
}

type ReferenceDataCapabilities struct {
	CurrentFunding      bool
	CurrentMarkPrice    bool
	CurrentIndexPrice   bool
	CurrentOraclePrice  bool
	ReferenceStream     bool
	ReferencePolling    bool
	FundingHistory      bool
	CurrentOpenInterest bool
	OpenInterestCached  bool
}
