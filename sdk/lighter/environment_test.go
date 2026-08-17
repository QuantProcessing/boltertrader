package lighter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The chain ID is not decoration: it is hashed into every signed create and
// cancel transaction, so a client that carries the wrong one produces
// signatures the exchange rejects. With only one environment left, the
// constructor is the single place that can get it wrong, which is what this
// asserts.
func TestNewClientCarriesMainnetURLAndChainID(t *testing.T) {
	client := NewClient()

	require.Equal(t, MainnetAPIURL, client.BaseURL)
	require.Equal(t, uint32(MainnetChainID), client.ChainId)
}

func TestWebsocketClientBuildsMainnetURLWithMsgpackEncoding(t *testing.T) {
	client := NewWebsocketClient(context.Background())

	got, err := client.buildURL()
	require.NoError(t, err)
	require.Equal(t, "wss://mainnet.zklighter.elliot.ai/stream?encoding=msgpack", got)
}
