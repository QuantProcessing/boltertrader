package publicconn

import (
	"context"

	"github.com/QuantProcessing/boltertrader/routeprofile"
)

type Factory interface {
	Build(context.Context, routeprofile.Key) (Connector, error)
}
