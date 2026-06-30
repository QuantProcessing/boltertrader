package spot

import (
	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

type Client struct {
	*hyperliquid.Client
}

func NewClient(base *hyperliquid.Client) *Client {
	return &Client{Client: base}
}
