package standx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStandXAccountAuthStreamsDefineSupportedPrivateChannels(t *testing.T) {
	streams := standxAccountAuthStreams()
	require.Equal(t, []SubscribeAuthChannel{
		{Channel: "order"},
		{Channel: "position"},
		{Channel: "balance"},
		{Channel: "trade"},
	}, streams)

	for _, stream := range streams {
		require.True(t, isStandXAccountChannel(stream.Channel), "auth stream %q must be restorable after reconnect", stream.Channel)
	}
	require.False(t, isStandXAccountChannel("margin"))
}

func TestWsAccountClient_SubscribeRejectsUnsupportedChannel(t *testing.T) {
	client := NewWsAccountClient(context.Background(), nil)

	err := client.Subscribe("margin", func([]byte) {})
	require.ErrorContains(t, err, `unsupported standx account channel "margin"`)

	client.mu.RLock()
	_, ok := client.handlers["margin"]
	client.mu.RUnlock()
	require.False(t, ok, "unsupported channel must be rejected before handler registration")
}
