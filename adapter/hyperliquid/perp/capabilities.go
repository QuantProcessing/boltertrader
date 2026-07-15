package perp

import (
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
)

func (c *marketDataClient) Capabilities() contract.Capabilities {
	streaming := c.ws != nil
	return contract.Capabilities{
		Venue:     venueName,
		Products:  []contract.ProductCapability{{Kind: enums.KindPerp, Market: true, Trading: true, Account: true}},
		Reports:   contract.ReportCapabilities{OpenOrders: true, OpenOnlyNotFoundAmbiguous: true},
		Streaming: contract.StreamCapabilities{Market: streaming},
		ReferenceData: contract.ReferenceDataCapabilities{
			CurrentFunding:      true,
			CurrentMarkPrice:    true,
			CurrentOraclePrice:  true,
			ReferencePolling:    true,
			CurrentOpenInterest: true,
		},
	}
}

func (c *executionClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:    venueName,
		Products: []contract.ProductCapability{{Kind: enums.KindPerp, Trading: true}},
		Reports: contract.ReportCapabilities{
			SingleOrderStatus:         true,
			OpenOrders:                true,
			OpenOnlyNotFoundAmbiguous: true,
		},
		Streaming: contract.StreamCapabilities{Execution: c.stream != nil},
		Trading:   contract.TradingCapabilities{Submit: true, Cancel: true, CancelAll: true, Modify: true},
	}
}

func (c *accountClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:     venueName,
		Products:  []contract.ProductCapability{{Kind: enums.KindPerp, Account: true}},
		Reports:   contract.ReportCapabilities{PositionReports: true, AccountBalanceSnapshots: true},
		Streaming: contract.StreamCapabilities{Account: c.stream != nil},
	}
}
