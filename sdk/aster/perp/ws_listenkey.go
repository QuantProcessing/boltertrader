package perp

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Create ListenKey

func (c *Client) CreateListenKey(ctx context.Context) (string, error) {
	var res ListenKeyResponse
	err := c.Post(ctx, "/fapi/v3/listenKey", nil, true, &res)
	if err != nil {
		return "", err
	}
	if res.ListenKey == "" {
		return "", fmt.Errorf("aster perp user stream: empty listen key response")
	}
	return res.ListenKey, nil
}

// KeepAlive ListenKey

func (c *Client) KeepAliveListenKey(ctx context.Context) error {
	return c.call(ctx, "PUT", "/fapi/v3/listenKey", nil, true, nil)
}

// Close ListenKey

func (c *Client) CloseListenKey(ctx context.Context) error {
	return c.Delete(ctx, "/fapi/v3/listenKey", nil, true, nil)
}

// ListenKey Response

type ListenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

type PerpUserStreamManager struct {
	Client       *Client
	KeepAliveInt time.Duration

	mu        sync.Mutex
	listenKey string
	cancel    context.CancelFunc
	done      chan struct{}
	running   bool
	onRenew   func(string)
}

func NewPerpUserStreamManager(client *Client) *PerpUserStreamManager {
	return &PerpUserStreamManager{
		Client:       client,
		KeepAliveInt: 30 * time.Minute,
	}
}

func (m *PerpUserStreamManager) Start(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return m.listenKey, nil
	}
	if m.Client == nil {
		return "", fmt.Errorf("aster perp user stream: REST client is required")
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

func (m *PerpUserStreamManager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return nil
	}
	m.running = false
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
	return m.Client.CloseListenKey(ctx)
}

func (m *PerpUserStreamManager) ListenKey() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listenKey
}

func (m *PerpUserStreamManager) SetRenewHandler(handler func(string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onRenew = handler
}

func (m *PerpUserStreamManager) Renew(ctx context.Context) (string, error) {
	renewed, err := m.Client.CreateListenKey(ctx)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = m.Client.CloseListenKey(cleanupCtx)
		cancel()
		return renewed, nil
	}
	changed := renewed != m.listenKey
	m.listenKey = renewed
	handler := m.onRenew
	m.mu.Unlock()
	if changed && handler != nil {
		handler(renewed)
	}
	return renewed, nil
}

func (m *PerpUserStreamManager) keepAliveLoop(ctx context.Context, done chan struct{}) {
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
			if m.ListenKey() == "" {
				return
			}
			if err := m.Client.KeepAliveListenKey(ctx); err == nil {
				continue
			}
			if _, err := m.Renew(ctx); err != nil {
				m.Client.Logger.Warnw("failed to renew Aster Perp user stream", "error", err)
			}
		}
	}
}
