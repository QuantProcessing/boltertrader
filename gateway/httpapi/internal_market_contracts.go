package httpapi

import (
	"context"

	"github.com/QuantProcessing/boltertrader/gateway/fundingboard"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
)

// InternalPublicMarketStreams is the read-only native source consumed by the
// unified internal market protocol. Market Hub owns deduplication and fanout.
type InternalPublicMarketStreams interface {
	WatchOrderBook(context.Context, string, int) (publicmarket.OrderBookStream, error)
	WatchCandles(context.Context, string, string) (publicmarket.CandleStream, error)
	WatchMarkPrice(context.Context, string) (publicmarket.MarkPriceStream, error)
}

// InternalFundingBoardChanges wakes the unified funding.board projection. The
// board payload itself is read from the authority snapshot.
type InternalFundingBoardChanges interface {
	Subscribe(context.Context) (<-chan fundingboard.Change, func(), error)
}
