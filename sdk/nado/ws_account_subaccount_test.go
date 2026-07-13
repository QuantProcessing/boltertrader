package nado

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWsAccountClientSubscribeOrdersUsesConfiguredSubaccount(t *testing.T) {
	t.Parallel()

	privateKey := "1111111111111111111111111111111111111111111111111111111111111111"
	restClient, err := newNadoTestnetClient(t).WithCredentials(privateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)
	require.NoError(t, client.SetSubaccount("arb"))

	require.NoError(t, client.SubscribeOrders(nil, nil))

	sub, ok := client.subscriptions["order_update"]
	require.True(t, ok)

	signer, err := NewSigner(privateKey, restClient.Profile().ChainID())
	require.NoError(t, err)
	expected := BuildSender(signer.GetAddress(), "arb")
	require.Equal(t, expected, sub.params.Subaccount)

	payload, err := json.Marshal(SubscriptionRequest{Method: "subscribe", Stream: sub.params, Id: 1})
	require.NoError(t, err)
	var decoded struct {
		Stream map[string]any `json:"stream"`
	}
	require.NoError(t, json.Unmarshal(payload, &decoded))
	_, hasProductID := decoded.Stream["product_id"]
	require.False(t, hasProductID, "wildcard subscription must omit product_id: %s", payload)
}

func TestWsAccountClientSetSubaccountRejectsLongNames(t *testing.T) {
	t.Parallel()

	restClient, err := newNadoTestnetClient(t).WithCredentials(wsTestPrivateKey, "default")
	require.NoError(t, err)
	client, err := NewWsAccountClient(context.Background(), restClient)
	require.NoError(t, err)

	require.Error(t, client.SetSubaccount("too-long-name"))
}
