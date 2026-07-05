package lighter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientWithEnvironmentSelectsLighterURLsAndChainID(t *testing.T) {
	client := NewClient().WithEnvironment(EnvironmentTestnet)

	require.Equal(t, TestnetAPIURL, client.BaseURL)
	require.Equal(t, uint32(TestnetChainID), client.ChainId)

	client.WithEnvironment(EnvironmentMainnet)

	require.Equal(t, MainnetAPIURL, client.BaseURL)
	require.Equal(t, uint32(MainnetChainID), client.ChainId)
}

func TestWebsocketClientWithEnvironmentSelectsLighterURL(t *testing.T) {
	client := NewWebsocketClient(context.Background()).WithEnvironment(EnvironmentTestnet)

	got, err := client.buildURL()
	require.NoError(t, err)
	require.Equal(t, "wss://testnet.zklighter.elliot.ai/stream?encoding=msgpack", got)
}
