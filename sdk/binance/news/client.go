package news

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/gorilla/websocket"
)

const (
	// Binance CMS API WebSocket endpoint (requires authentication)
	BaseURL = "wss://api.binance.com/sapi/wss"
)

type Client struct {
	apiKey    string
	secretKey string
	conn      *websocket.Conn
	mu        sync.Mutex
	handlers  map[string]func(WsNewsEvent)
	done      chan struct{}
	logger    *zap.SugaredLogger
	isClosed  bool
}

func NewClient(apiKey, secretKey string) *Client {
	return &Client{
		apiKey:    apiKey,
		secretKey: secretKey,
		handlers:  make(map[string]func(WsNewsEvent)),
		done:      make(chan struct{}),
		logger:    zap.NewNop().Sugar().Named("binance-news"),
	}
}

func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil
	}

	// Generate random string for signature
	random := fmt.Sprintf("%d", time.Now().UnixNano())
	topic := "com_announcement_en"
	recvWindow := "60000"
	timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())

	// Build query parameters for signature
	params := url.Values{}
	params.Add("random", random)
	params.Add("topic", topic)
	params.Add("recvWindow", recvWindow)
	params.Add("timestamp", timestamp)

	// Sign the query string
	queryString := params.Encode()
	signature := c.sign(queryString)

	// Construct full URL with signature
	fullURL := fmt.Sprintf("%s?%s&signature=%s", BaseURL, queryString, signature)

	// Configure dialer with proxy
	dialer := websocket.DefaultDialer
	proxyURL := os.Getenv("HTTPS_PROXY")
	if proxyURL == "" {
		proxyURL = os.Getenv("HTTP_PROXY")
	}
	if proxyURL == "" {
		proxyURL = os.Getenv("PROXY")
	}

	if proxyURL != "" {
		parsedURL, err := url.Parse(proxyURL)
		if err == nil {
			dialer = &websocket.Dialer{
				Proxy:            http.ProxyURL(parsedURL),
				HandshakeTimeout: 10 * time.Second,
			}
		} else {
			c.logger.Warnw("Invalid proxy URL", "url", proxyURL, "error", err)
		}
	}

	headers := http.Header{}
	headers.Add("X-MBX-APIKEY", c.apiKey)

	c.logger.Infow("Connecting to Binance CMS API", "url", BaseURL, "topic", topic)
	conn, _, err := dialer.Dial(fullURL, headers)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}

	c.conn = conn
	c.isClosed = false
	c.done = make(chan struct{})

	go c.readLoop()

	c.logger.Info("Successfully connected and subscribed to Binance CMS")
	return nil
}

func (c *Client) sign(data string) string {
	mac := hmac.New(sha256.New, []byte(c.secretKey))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

// Subscribe is a no-op for CMS API since subscription happens at connection time
func (c *Client) Subscribe(topic string, handler func(WsNewsEvent)) error {
	c.mu.Lock()
	c.handlers[topic] = handler
	c.mu.Unlock()
	return nil
}

func (c *Client) Unsubscribe(topic string) error {
	c.mu.Lock()
	delete(c.handlers, topic)
	c.mu.Unlock()
	return nil
}

func (c *Client) readLoop() {
	defer func() {
		// Check if we should reconnect BEFORE closing
		shouldReconnect := !c.isClosed

		// Close the connection
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
			c.conn = nil
		}
		c.mu.Unlock()

		// Trigger reconnection if not explicitly closed by user
		if shouldReconnect {
			c.logger.Info("Connection lost, will attempt to reconnect...")
			go c.reconnect()
		}
	}()

	for {
		select {
		case <-c.done:
			return
		default:
			_, message, err := c.conn.ReadMessage()
			if err != nil {
				c.logger.Errorw("read error", "error", err)
				return
			}
			c.handleMessage(message)
		}
	}
}

func (c *Client) handleMessage(message []byte) {
	// Log raw message for debugging
	c.logger.Debugw("Received message", "msg", string(message))

	// Parse the outer structure
	var wrapper struct {
		Type  string `json:"type"`
		Topic string `json:"topic"`
		Data  string `json:"data"` // data is a JSON string, not object
	}

	if err := json.Unmarshal(message, &wrapper); err != nil {
		c.logger.Errorw("failed to unmarshal wrapper", "error", err, "msg", string(message))
		return
	}

	// Skip non-DATA messages
	if wrapper.Type != "DATA" {
		c.logger.Debugw("Skipping non-DATA message", "type", wrapper.Type)
		return
	}

	// Parse the nested JSON string in data field
	var event WsNewsEvent
	if err := json.Unmarshal([]byte(wrapper.Data), &event); err != nil {
		c.logger.Errorw("failed to unmarshal news event", "error", err, "data", wrapper.Data)
		return
	}

	// Additional validation
	if event.CatalogId == 0 && event.Title == "" {
		c.logger.Debugw("Skipping invalid event", "event", event)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Dispatch to all handlers (usually just one for com_announcement_en)
	for topic, handler := range c.handlers {
		if handler != nil {
			c.logger.Infow("Dispatching announcement", "topic", topic, "title", event.Title)
			go handler(event)
		}
	}
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.isClosed = true
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}

func (c *Client) reconnect() {
	backoff := 1 * time.Second
	maxBackoff := 30 * time.Second

	for attempt := 1; ; attempt++ {
		c.logger.Infow("Reconnecting...", "attempt", attempt, "backoff", backoff)
		time.Sleep(backoff)

		if err := c.Connect(); err != nil {
			c.logger.Errorw("Reconnect failed, will retry", "attempt", attempt, "error", err)

			// Exponential backoff with max cap
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		c.logger.Info("Reconnection successful")
		return
	}
}
