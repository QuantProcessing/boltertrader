package perp

import "context"

// Create ListenKey

func (c *Client) CreateListenKey(ctx context.Context) (string, error) {
	var res ListenKeyResponse
	err := c.Post(ctx, "/fapi/v1/listenKey", nil, true, &res) // API key required
	if err != nil {
		return "", err
	}
	return res.ListenKey, nil
}

// KeepAlive ListenKey

func (c *Client) KeepAliveListenKey(ctx context.Context) error {
	return c.call(ctx, "PUT", "/fapi/v1/listenKey", nil, true, nil)
}

// Close ListenKey

func (c *Client) CloseListenKey(ctx context.Context) error {
	return c.Delete(ctx, "/fapi/v1/listenKey", nil, true, nil)
}

// ListenKey Response

type ListenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}
