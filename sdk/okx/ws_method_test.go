package okx

import (
	"context"
	"os"
	"testing"

	"github.com/QuantProcessing/exchanges/internal/testenv"
	"github.com/stretchr/testify/require"
)

func TestWSClient_WithCredentials(t *testing.T) {
	client := NewWSClient(context.Background())
	got := client.WithCredentials("api-key", "secret-key", "passphrase")

	require.Same(t, client, got)
	require.True(t, client.IsPrivate)
	require.Equal(t, WSPrivateBaseURL, client.URL)
	require.Equal(t, "api-key", client.ApiKey)
	require.Equal(t, "secret-key", client.SecretKey)
	require.Equal(t, "passphrase", client.Passphrase)
}

func TestWSClient_AddPendingRequest(t *testing.T) {
	client := NewWSClient(context.Background())
	success, failure := client.AddPendingRequest(12)

	require.NotNil(t, success)
	require.NotNil(t, failure)
	require.Equal(t, client.PendingReqs[int64(12)].Success, success)
	require.Equal(t, client.PendingReqs[int64(12)].Error, failure)
}

func TestWSClient_RemovePendingRequest(t *testing.T) {
	client := NewWSClient(context.Background())
	client.AddPendingRequest(12)

	client.RemovePendingRequest(12)

	require.Nil(t, client.PendingReqs[int64(12)])
}

func TestWSClient_Connect(t *testing.T) {
	client := newLivePublicOKXWSClient(t)

	require.NotNil(t, client.Conn)
}

func TestWSClient_Login(t *testing.T) {
	client := newLivePrivateOKXWSClient(t)

	require.NotNil(t, client.Conn)
	require.True(t, client.IsPrivate)
}

func TestWSClient_Subscribe(t *testing.T) {
	client := newLivePublicOKXWSClient(t)

	err := client.Subscribe(WsSubscribeArgs{Channel: "tickers", InstId: "BTC-USDT"}, func([]byte) {})
	require.NoError(t, err)
	require.NotNil(t, client.Subs[WsSubscribeArgs{Channel: "tickers", InstId: "BTC-USDT"}])
}

func TestWSClient_Unsubscribe(t *testing.T) {
	client := newLivePublicOKXWSClient(t)
	args := WsSubscribeArgs{Channel: "tickers", InstId: "BTC-USDT"}
	client.Subs[args] = func([]byte) {}

	err := client.Unsubscribe(args)
	require.NoError(t, err)
	require.Nil(t, client.Subs[args])
}

func newLivePublicOKXWSClient(t *testing.T) *WSClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	client := NewWSClient(ctx)
	require.NoError(t, client.Connect())
	t.Cleanup(func() {
		cancel()
		if client.Conn != nil {
			_ = client.Conn.Close()
		}
	})
	return client
}

func newLivePrivateOKXWSClient(t *testing.T) *WSClient {
	t.Helper()
	testenv.RequireLiveCredentials(t, "OKX_API_KEY", "OKX_API_SECRET", "OKX_API_PASSPHRASE")
	ctx, cancel := context.WithCancel(context.Background())
	client := NewWSClient(ctx).WithCredentials(os.Getenv("OKX_API_KEY"), os.Getenv("OKX_API_SECRET"), os.Getenv("OKX_API_PASSPHRASE"))
	require.NoError(t, client.Connect())
	t.Cleanup(func() {
		cancel()
		if client.Conn != nil {
			_ = client.Conn.Close()
		}
	})
	return client
}
