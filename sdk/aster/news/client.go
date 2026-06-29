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
		logger:    zap.NewNop().Sugar().Named("aster-news"),
	}
}

func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil
	}

	// Construct signed URL
	params := url.Values{}
	params.Add("timestamp", fmt.Sprintf("%d", time.Now().UnixMilli()))
	params.Add("recvWindow", "10000")
	params.Add("random", fmt.Sprintf("%d", time.Now().UnixNano()))

	query := params.Encode()
	signature := c.sign(query)
	fullURL := fmt.Sprintf("%s?%s&signature=%s", BaseURL, query, signature)

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
				HandshakeTimeout: 5 * time.Second,
			}
		} else {
			c.logger.Warnw("Invalid proxy URL", "url", proxyURL, "error", err)
		}
	}

	headers := http.Header{}
	headers.Add("X-MBX-APIKEY", c.apiKey)

	conn, _, err := dialer.Dial(fullURL, headers)
	if err != nil {
		return err
	}

	c.conn = conn
	c.isClosed = false
	c.done = make(chan struct{})

	go c.readLoop()

	return nil
}

func (c *Client) sign(data string) string {
	mac := hmac.New(sha256.New, []byte(c.secretKey))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *Client) Subscribe(topic string, handler func(WsNewsEvent)) error {
	c.mu.Lock()
	c.handlers[topic] = handler
	c.mu.Unlock()

	req := WsRequest{
		Command: "SUBSCRIBE",
		Value:   topic,
	}
	return c.writeJSON(req)
}

func (c *Client) Unsubscribe(topic string) error {
	c.mu.Lock()
	delete(c.handlers, topic)
	c.mu.Unlock()

	req := WsRequest{
		Command: "UNSUBSCRIBE",
		Value:   topic,
	}
	return c.writeJSON(req)
}

func (c *Client) writeJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("websocket not connected")
	}
	return c.conn.WriteJSON(v)
}

func (c *Client) readLoop() {
	defer func() {
		c.Close()
		if !c.isClosed {
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
	var resp WsResponse
	if err := json.Unmarshal(message, &resp); err != nil {
		c.logger.Errorw("unmarshal error", "error", err, "msg", string(message))
		return
	}

	if resp.Topic != "" && resp.Data != "" {
		c.mu.Lock()
		handler, ok := c.handlers[resp.Topic]
		c.mu.Unlock()

		if ok && handler != nil {
			var event WsNewsEvent
			if err := json.Unmarshal([]byte(resp.Data), &event); err == nil {
				go handler(event)
			} else {
				c.logger.Errorw("failed to unmarshal news event", "error", err)
			}
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
	time.Sleep(1 * time.Second)
	c.logger.Info("reconnecting...")
	if err := c.Connect(); err != nil {
		c.logger.Errorw("reconnect failed", "error", err)
		go c.reconnect()
	} else {
		// Resubscribe
		c.mu.Lock()
		handlers := make(map[string]func(WsNewsEvent))
		for k, v := range c.handlers {
			handlers[k] = v
		}
		c.mu.Unlock()

		for topic := range handlers {
			req := WsRequest{
				Command: "SUBSCRIBE",
				Value:   topic,
			}
			if err := c.writeJSON(req); err != nil {
				c.logger.Errorw("resubscribe failed", "topic", topic, "error", err)
			}
		}
	}
}
