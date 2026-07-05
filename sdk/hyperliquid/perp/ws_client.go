package perp

import (
	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

type WebsocketClient struct {
	*hyperliquid.WebsocketClient
}

func NewWebsocketClient(base *hyperliquid.WebsocketClient) *WebsocketClient {
	return &WebsocketClient{WebsocketClient: base}
}

func (c *WebsocketClient) WithCredentials(privateKey, accountAddr string) *WebsocketClient {
	c.WebsocketClient.WithCredentials(privateKey, nil)
	if accountAddr != "" {
		c.AccountAddr = accountAddr
	}
	return c
}
