package spot

import (
	"context"
	"fmt"
	"time"
)

// ListenKey Response

type ListenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

// Create ListenKey

func (c *Client) CreateListenKey(ctx context.Context) (string, error) {
	var res ListenKeyResponse
	err := c.Post(ctx, "/api/v1/listenKey", nil, false, &res) // API key required, but no signature
	if err != nil {
		fmt.Printf("CreateListenKey Error: %v\n", err) // Debug log
		return "", err
	}
	fmt.Printf("CreateListenKey Success: %s\n", res.ListenKey)
	return res.ListenKey, nil
}

// KeepAlive ListenKey

func (c *Client) KeepAliveListenKey(ctx context.Context, listenKey string) error {
	params := map[string]interface{}{
		"listenKey": listenKey,
	}
	return c.call(ctx, "PUT", "/api/v1/listenKey", params, false, nil)
}

// Close ListenKey

func (c *Client) CloseListenKey(ctx context.Context, listenKey string) error {
	params := map[string]interface{}{
		"listenKey": listenKey,
	}
	return c.Delete(ctx, "/api/v1/listenKey", params, false, nil)
}

// UserStreamManager handles the lifecycle of a ListenKey

type UserStreamManager struct {
	Client       *Client
	ListenKey    string
	StopCh       chan struct{}
	KeepAliveInt time.Duration
}

func NewUserStreamManager(client *Client) *UserStreamManager {
	return &UserStreamManager{
		Client:       client,
		StopCh:       make(chan struct{}),
		KeepAliveInt: 30 * time.Minute, // Binance recommends every 30 minutes
	}
}

func (m *UserStreamManager) Start(ctx context.Context) (string, error) {
	listenKey, err := m.Client.CreateListenKey(ctx)
	if err != nil {
		return "", err
	}
	m.ListenKey = listenKey

	go m.keepAliveLoop(ctx)

	return listenKey, nil
}

func (m *UserStreamManager) Stop(ctx context.Context) error {
	select {
	case <-m.StopCh:
		// Already closed
		return nil
	default:
		close(m.StopCh)
	}

	if m.ListenKey != "" {
		return m.Client.CloseListenKey(ctx, m.ListenKey)
	}
	return nil
}

func (m *UserStreamManager) keepAliveLoop(ctx context.Context) {
	ticker := time.NewTicker(m.KeepAliveInt)
	defer ticker.Stop()

	for {
		select {
		case <-m.StopCh:
			return
		case <-ticker.C:
			if err := m.Client.KeepAliveListenKey(ctx, m.ListenKey); err != nil {
				fmt.Printf("Failed to keep alive listen key: %v\n", err)
				// Retry or handle error? For now just log.
			}
		}
	}
}
