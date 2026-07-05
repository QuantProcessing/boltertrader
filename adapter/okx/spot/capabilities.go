package spot

import (
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
)

func (c *marketDataClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:     venueName,
		Products:  []contract.ProductCapability{{Kind: enums.KindSpot, Market: true, Trading: true, Account: true}},
		Reports:   contract.ReportCapabilities{OpenOrders: true, OpenOnlyNotFoundAmbiguous: true},
		Streaming: contract.StreamCapabilities{Market: c.ws != nil},
		Latency:   contract.LatencyCapabilities{},
	}
}

func (c *executionClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:    venueName,
		Products: []contract.ProductCapability{{Kind: enums.KindSpot, Trading: true}},
		Reports: contract.ReportCapabilities{
			OpenOrders:                true,
			OpenOnlyNotFoundAmbiguous: true,
		},
		Streaming: contract.StreamCapabilities{Execution: true},
		Trading:   contract.TradingCapabilities{Submit: true, Cancel: true, CancelAll: true, Modify: true},
		Latency:   contract.LatencyCapabilities{},
	}
}

func (c *accountClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:     venueName,
		Products:  []contract.ProductCapability{{Kind: enums.KindSpot, Account: true}},
		Reports:   contract.ReportCapabilities{AccountBalanceSnapshots: true, AccountStateSnapshots: true},
		Streaming: contract.StreamCapabilities{Account: true},
		Latency:   contract.LatencyCapabilities{},
	}
}
