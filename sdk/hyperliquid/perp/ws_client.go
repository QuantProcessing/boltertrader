package perp

import (
	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"

	"github.com/ethereum/go-ethereum/crypto"
)

type WebsocketClient struct {
	*hyperliquid.WebsocketClient
}

func NewWebsocketClient(base *hyperliquid.WebsocketClient) *WebsocketClient {
	return &WebsocketClient{WebsocketClient: base}
}

func (c *WebsocketClient) WithCredentials(privateKey, accountAddr string) *WebsocketClient {
	pk, _ := crypto.HexToECDSA(privateKey)
	c.PrivateKey = pk
	c.AccountAddr = accountAddr

	return c
}
