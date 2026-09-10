package httpapi

import (
	"context"

	"github.com/QuantProcessing/boltertrader/gateway/fundingboard"
)

// InternalFundingBoardChanges wakes the unified funding.board projection. The
// board payload itself is read from the authority snapshot.
type InternalFundingBoardChanges interface {
	Subscribe(context.Context) (<-chan fundingboard.Change, func(), error)
}
