package lighter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClient_WithCredentials(t *testing.T) {
	client := NewClient().WithCredentials("0x"+strings.Repeat("01", 40), 42, 7)

	require.Equal(t, strings.Repeat("01", 40), client.PrivateKey)
	require.Equal(t, int64(42), client.AccountIndex)
	require.Equal(t, uint8(7), client.KeyIndex)
	require.NotNil(t, client.KeyManager)
}

func TestClient_InvalidateNonce(t *testing.T) {
	client := &Client{
		nonce:     123,
		nonceInit: true,
	}

	client.InvalidateNonce()

	require.False(t, client.nonceInit)
	require.Equal(t, int64(123), client.nonce)
}

func TestClient_CreateAuthToken(t *testing.T) {
	client := NewClient().WithCredentials(strings.Repeat("01", 40), 42, 7)

	token, err := client.CreateAuthToken(time.Unix(100, 0))
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Contains(t, token, "100:42:7:")
}

func TestClient_GetBlockHeight(t *testing.T) {
	height, err := newLiveClient().GetBlockHeight(context.Background())
	require.NoError(t, err)
	require.Positive(t, height)
}
