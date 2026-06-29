package standx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestWSClientCompatibility(t *testing.T) {
	ctx := context.Background()
	logger := zap.NewNop().Sugar()

	modern := NewWSClient(ctx, MarketStreamURL, logger)
	legacy := NewWsClient(ctx, MarketStreamURL, logger)

	require.NotNil(t, modern)
	require.NotNil(t, legacy)

	var modernFromLegacy *WSClient = legacy
	var legacyFromModern *WsClient = modern

	require.Same(t, legacy, modernFromLegacy)
	require.Same(t, modern, legacyFromModern)

	market := NewWsMarketClient(ctx)
	require.NotNil(t, market.WsClient)
	require.IsType(t, &WSClient{}, market.WsClient)
}
