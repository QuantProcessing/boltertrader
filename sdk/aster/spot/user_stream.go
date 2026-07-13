package spot

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type ListenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

func (c *Client) CreateListenKey(ctx context.Context) (string, error) {
	var response ListenKeyResponse
	if err := c.Post(ctx, "/api/v3/listenKey", nil, true, &response); err != nil {
		return "", err
	}
	if response.ListenKey == "" {
		return "", fmt.Errorf("aster spot user stream: empty listen key response")
	}
	return response.ListenKey, nil
}

func (c *Client) KeepAliveListenKey(ctx context.Context, listenKey string) error {
	if listenKey == "" {
		return fmt.Errorf("aster spot user stream: listen key is required")
	}
	return c.call(ctx, "PUT", "/api/v3/listenKey", map[string]interface{}{
		"listenKey": listenKey,
	}, true, nil)
}

func (c *Client) CloseListenKey(ctx context.Context, listenKey string) error {
	if listenKey == "" {
		return nil
	}
	return c.Delete(ctx, "/api/v3/listenKey", map[string]interface{}{
		"listenKey": listenKey,
	}, true, nil)
}

type UserStreamManager struct {
	Client       *Client
	KeepAliveInt time.Duration

	mu        sync.Mutex
	listenKey string
	cancel    context.CancelFunc
	done      chan struct{}
	running   bool
	onRenew   func(string)
}

func NewUserStreamManager(client *Client) *UserStreamManager {
	return &UserStreamManager{
		Client:       client,
		KeepAliveInt: 30 * time.Minute,
	}
}

func (m *UserStreamManager) Start(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return m.listenKey, nil
	}
	if m.Client == nil {
		return "", fmt.Errorf("aster spot user stream: REST client is required")
	}
	listenKey, err := m.Client.CreateListenKey(ctx)
	if err != nil {
		return "", err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	m.listenKey = listenKey
	m.cancel = cancel
	m.done = make(chan struct{})
	m.running = true
	go m.keepAliveLoop(streamCtx, m.done)
	return listenKey, nil
}

func (m *UserStreamManager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return nil
	}
	m.running = false
	listenKey := m.listenKey
	cancel := m.cancel
	done := m.done
	m.listenKey = ""
	m.cancel = nil
	m.done = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.Client.CloseListenKey(ctx, listenKey)
}

func (m *UserStreamManager) ListenKey() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listenKey
}

func (m *UserStreamManager) SetRenewHandler(handler func(string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onRenew = handler
}

func (m *UserStreamManager) keepAliveLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	interval := m.KeepAliveInt
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			listenKey := m.ListenKey()
			if listenKey == "" {
				return
			}
			if err := m.Client.KeepAliveListenKey(ctx, listenKey); err == nil {
				continue
			}
			renewed, err := m.Client.CreateListenKey(ctx)
			if err != nil {
				m.Client.Logger.Warnw("failed to renew Aster Spot user stream", "error", err)
				continue
			}

			m.mu.Lock()
			if !m.running {
				m.mu.Unlock()
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = m.Client.CloseListenKey(cleanupCtx, renewed)
				cancel()
				return
			}
			changed := renewed != m.listenKey
			m.listenKey = renewed
			handler := m.onRenew
			m.mu.Unlock()
			if changed && handler != nil {
				handler(renewed)
			}
		}
	}
}
