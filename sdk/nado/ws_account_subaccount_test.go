package nado

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWsAccountClientSubscribeOrdersUsesConfiguredSubaccount(t *testing.T) {
	t.Parallel()

	privateKey := "1111111111111111111111111111111111111111111111111111111111111111"
	client := NewWsAccountClient(context.Background()).
		WithCredentials(privateKey)
	client.SetSubaccount("arb")

	require.NoError(t, client.SubscribeOrders(nil, nil))

	sub, ok := client.subscriptions["order_update"]
	require.True(t, ok)

	signer, err := NewSigner(privateKey)
	require.NoError(t, err)
	expected := BuildSender(signer.GetAddress(), "arb")
	require.Equal(t, expected, sub.params.Subaccount)
}
