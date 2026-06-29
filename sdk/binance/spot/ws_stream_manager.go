package spot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"
)

const (
	binanceSpotWSMaxSubscriptionsPerClient = 200
	binanceSpotWSMaxClients                = 20
)

type binanceSpotWSStreamManager struct {
	ctx     context.Context
	base    *WsClient
	handler func([]byte)

	mu                        sync.Mutex
	started                   bool
	nextClientID              int
	maxSubscriptionsPerClient int
	maxClients                int
	streams                   []string
	handlers                  map[string]func([]byte) error
	clients                   map[int]*WsClient
	clientStreams             map[int][]string
	streamClient              map[string]int
	postReconnect             func()
}

func newBinanceSpotWSStreamManager(ctx context.Context, base *WsClient, handler func([]byte)) *binanceSpotWSStreamManager {
	if ctx == nil {
		ctx = context.Background()
	}
	return &binanceSpotWSStreamManager{
		ctx:                       ctx,
		base:                      base,
		handler:                   handler,
		maxSubscriptionsPerClient: binanceSpotWSMaxSubscriptionsPerClient,
		maxClients:                binanceSpotWSMaxClients,
		handlers:                  make(map[string]func([]byte) error),
		clients:                   make(map[int]*WsClient),
		clientStreams:             make(map[int][]string),
		streamClient:              make(map[string]int),
	}
}

func (m *binanceSpotWSStreamManager) Connect() error {
	m.mu.Lock()
	m.started = true
	streams := append([]string(nil), m.streams...)
	m.mu.Unlock()

	if len(streams) == 0 {
		return nil
	}

	grouped := make(map[int][]string)
	for _, stream := range streams {
		clientID, err := m.ensureAssigned(stream)
		if err != nil {
			return err
		}
		grouped[clientID] = append(grouped[clientID], stream)
	}

	var errs []error
	for clientID, clientStreams := range grouped {
		if err := m.connectClient(clientID, clientStreams); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *binanceSpotWSStreamManager) Subscribe(stream string, handler func([]byte) error) error {
	stream = normalizeBinanceSpotStream(stream)
	m.mu.Lock()
	_, exists := m.handlers[stream]
	if !exists {
		m.streams = append(m.streams, stream)
	}
	m.handlers[stream] = handler
	started := m.started
	m.mu.Unlock()

	clientID, err := m.ensureAssigned(stream)
	if err != nil {
		return err
	}
	if !started {
		return nil
	}

	m.mu.Lock()
	client := m.clients[clientID]
	clientStreams := append([]string(nil), m.clientStreams[clientID]...)
	m.mu.Unlock()

	if client == nil || !client.IsConnected() {
		return m.connectClient(clientID, clientStreams)
	}
	if len(clientStreams) > 0 && clientStreams[0] == stream {
		client.SetHandler(stream, handler)
		return nil
	}
	if exists {
		client.SetSubscriptionHandler(stream, handler)
		return nil
	}
	return client.Subscribe(stream, handler)
}

func (m *binanceSpotWSStreamManager) Unsubscribe(stream string) error {
	stream = normalizeBinanceSpotStream(stream)
	m.mu.Lock()
	delete(m.handlers, stream)
	for i, existing := range m.streams {
		if existing == stream {
			m.streams = append(m.streams[:i], m.streams[i+1:]...)
			break
		}
	}
	clientID, assigned := m.streamClient[stream]
	if assigned {
		delete(m.streamClient, stream)
	}
	client := m.clients[clientID]
	clientStreams := m.clientStreams[clientID]
	remaining := make([]string, 0, len(clientStreams))
	for _, existing := range clientStreams {
		if existing != stream {
			remaining = append(remaining, existing)
		}
	}
	if assigned {
		m.clientStreams[clientID] = remaining
	}
	started := m.started
	m.mu.Unlock()

	if !started || !assigned || client == nil {
		return nil
	}
	if client.IsConnected() {
		if err := client.Unsubscribe(stream); err != nil {
			return err
		}
	}
	if len(remaining) == 0 {
		client.Close()
		m.mu.Lock()
		delete(m.clients, clientID)
		delete(m.clientStreams, clientID)
		m.mu.Unlock()
	}
	return nil
}

func (m *binanceSpotWSStreamManager) SetPostReconnect(handler func()) {
	m.mu.Lock()
	m.postReconnect = handler
	clients := make([]*WsClient, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}
	m.mu.Unlock()
	for _, client := range clients {
		client.SetPostReconnect(handler)
	}
}

func (m *binanceSpotWSStreamManager) CallSubscription(stream string, message []byte) bool {
	stream = normalizeBinanceSpotStream(stream)
	m.mu.Lock()
	handler := m.handlers[stream]
	m.mu.Unlock()
	if handler == nil {
		return false
	}
	if err := handler(message); err != nil {
		if m.base != nil && m.base.Logger != nil {
			m.base.Logger.Error("callback error", "error", err)
		}
	}
	return true
}

func (m *binanceSpotWSStreamManager) ensureAssigned(stream string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if clientID, ok := m.streamClient[stream]; ok {
		return clientID, nil
	}
	for clientID, streams := range m.clientStreams {
		if len(streams) < m.maxSubscriptionsPerClient {
			m.clientStreams[clientID] = append(streams, stream)
			m.streamClient[stream] = clientID
			return clientID, nil
		}
	}
	if len(m.clientStreams) >= m.maxClients {
		return 0, fmt.Errorf("binance spot websocket subscription limit exceeded: max %d streams", m.maxClients*m.maxSubscriptionsPerClient)
	}

	clientID := m.nextClientID
	m.nextClientID++
	m.clientStreams[clientID] = []string{stream}
	m.streamClient[stream] = clientID
	return clientID, nil
}

func (m *binanceSpotWSStreamManager) connectClient(clientID int, streams []string) error {
	if len(streams) == 0 {
		return nil
	}

	m.mu.Lock()
	client := m.clients[clientID]
	if client == nil || client.isClosed {
		client = m.newClient(clientID)
		m.clients[clientID] = client
	}
	clientHandlers := make(map[string]func([]byte) error, len(streams))
	for _, stream := range streams {
		clientHandlers[stream] = m.handlers[stream]
	}
	m.mu.Unlock()

	if client.IsConnected() {
		for _, stream := range streams {
			if stream == streams[0] {
				client.SetHandler(stream, clientHandlers[stream])
				continue
			}
			client.SetSubscriptionHandler(stream, clientHandlers[stream])
		}
		return nil
	}

	initialStream := streams[0]
	for stream, handler := range clientHandlers {
		if stream == initialStream {
			client.SetHandler(stream, handler)
		}
	}

	var errs []error
	for _, candidate := range m.combinedURLCandidates(initialStream) {
		client.URL = candidate
		if err := client.Connect(); err != nil {
			errs = append(errs, err)
			continue
		}
		errs = nil
		break
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	for _, stream := range streams[1:] {
		if err := client.Subscribe(stream, clientHandlers[stream]); err != nil {
			client.Close()
			return err
		}
	}
	return nil
}

func (m *binanceSpotWSStreamManager) newClient(clientID int) *WsClient {
	if clientID == 0 && m.base != nil && !m.base.isClosed {
		m.base.Handler = m.handler
		m.base.SetPostReconnect(m.postReconnect)
		return m.base
	}
	client := NewWSClient(m.ctx, m.combinedURL(m.clientStreams[clientID][0]))
	client.Logger = zap.NewNop().Sugar().Named(fmt.Sprintf("binance-spot-market-%d", clientID))
	client.Handler = m.handler
	client.SetPostReconnect(m.postReconnect)
	return client
}

func (m *binanceSpotWSStreamManager) clientsSnapshot() []*WsClient {
	m.mu.Lock()
	defer m.mu.Unlock()
	clients := make([]*WsClient, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}
	return clients
}

func (m *binanceSpotWSStreamManager) clientCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.clientStreams)
}

func (m *binanceSpotWSStreamManager) streamCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.streams)
}

func (m *binanceSpotWSStreamManager) setMaxSubscriptionsPerClientForTest(limit int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxSubscriptionsPerClient = limit
}

func (m *binanceSpotWSStreamManager) combinedURL(initialStream string) string {
	return binanceSpotWSCombinedStreamURL(m.baseURL(), initialStream)
}

func (m *binanceSpotWSStreamManager) combinedURLCandidates(initialStream string) []string {
	primary := m.combinedURL(initialStream)
	if strings.HasPrefix(primary, "ws://") {
		return []string{primary}
	}
	candidates := []string{primary}
	fallback := strings.Replace(primary, "stream.binance.com:9443", "stream.binance.com:443", 1)
	if fallback != primary {
		candidates = append(candidates, fallback)
	}
	candidates = append(candidates, binanceSpotWSCombinedStreamURL("wss://data-stream.binance.vision/ws", initialStream))
	return dedupeSpotWSURLs(candidates)
}

func dedupeSpotWSURLs(urls []string) []string {
	out := make([]string, 0, len(urls))
	seen := make(map[string]struct{}, len(urls))
	for _, url := range urls {
		if _, ok := seen[url]; ok {
			continue
		}
		seen[url] = struct{}{}
		out = append(out, url)
	}
	return out
}

func (m *binanceSpotWSStreamManager) baseURL() string {
	if m.base == nil || m.base.URL == "" {
		return WSBaseURL
	}
	baseURL := m.base.URL
	if idx := strings.Index(baseURL, "/stream?"); idx >= 0 {
		return baseURL[:idx] + "/ws"
	}
	if idx := strings.Index(baseURL, "/ws/"); idx >= 0 {
		return baseURL[:idx] + "/ws"
	}
	return baseURL
}

func normalizeBinanceSpotStream(stream string) string {
	if stream == "" || strings.HasPrefix(stream, "!") {
		return stream
	}
	parts := strings.SplitN(stream, "@", 2)
	if len(parts) != 2 {
		return strings.ToLower(stream)
	}
	return strings.ToLower(parts[0]) + "@" + parts[1]
}

func binanceSpotWSCombinedStreamURL(baseURL string, initialStream string) string {
	baseURL = strings.TrimSuffix(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/ws")
	baseURL = strings.TrimSuffix(baseURL, "/stream")
	return baseURL + "/stream?streams=" + initialStream
}
