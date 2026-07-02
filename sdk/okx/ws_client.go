package okx

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/gorilla/websocket"
)

const (
	ReadTimeout       = 60 * time.Second
	ReconnectWait     = 5 * time.Second
	WSBaseURL         = "wss://ws.okx.com:8443/ws/v5/public"
	WSPrivateBaseURL  = "wss://ws.okx.com:8443/ws/v5/private"
	WSBusinessBaseURL = "wss://ws.okx.com:8443/ws/v5/business"
)

type WSClient struct {
	Conn            *websocket.Conn
	mu              sync.Mutex
	WriteMu         sync.Mutex
	IsPrivate       bool
	URL             string
	Environment     Environment
	DemoHostProfile DemoHostProfile
	ApiKey          string
	SecretKey       string
	Passphrase      string
	Subs            map[WsSubscribeArgs]func([]byte)
	PendingReqs     map[int64]*PendingRequest
	Dialer          *websocket.Dialer
	urlRole         wsURLRole
	explicitURL     bool
	endpointErr     error

	ctx context.Context

	Connected chan bool // for private connection
	Logger    *zap.SugaredLogger
}

type WsClient = WSClient

type wsURLRole int

const (
	wsURLRolePublic wsURLRole = iota
	wsURLRolePrivate
	wsURLRoleBusiness
)

func NewWSClient(ctx context.Context) *WSClient {
	// Proxy check
	dialer := &websocket.Dialer{
		ReadBufferSize:    65535,
		WriteBufferSize:   8192,
		HandshakeTimeout:  45 * time.Second,
		EnableCompression: true, // Enable compression to handle OKX compressed frames
	}
	proxyEnv := os.Getenv("PROXY")
	if proxyEnv != "" {
		proxyURL, err := url.Parse(proxyEnv)
		if err != nil {
			fmt.Printf("Invalid PROXY URL: %s, error: %v\n", proxyEnv, err)
		} else {
			dialer.Proxy = http.ProxyURL(proxyURL)
		}
	}

	// Use provided context for lifecycle management
	client := &WSClient{
		Subs:            make(map[WsSubscribeArgs]func([]byte)),
		PendingReqs:     make(map[int64]*PendingRequest),
		ctx:             ctx,
		Dialer:          dialer,
		Logger:          zap.NewNop().Sugar().Named("okx"),
		Connected:       make(chan bool, 1),
		Environment:     Production,
		DemoHostProfile: DemoHostProfileGlobal,
		urlRole:         wsURLRolePublic,
	}
	client.applyEndpointURL()
	return client
}

func NewWsClient(ctx context.Context) *WSClient {
	return NewWSClient(ctx)
}

func (c *WSClient) WithCredentials(apiKey, secretKey, passphrase string) *WSClient {
	c.IsPrivate = true
	c.urlRole = wsURLRolePrivate
	c.applyEndpointURL()
	// keys
	c.ApiKey = apiKey
	c.SecretKey = secretKey
	c.Passphrase = passphrase
	return c
}

func (c *WSClient) WithBusinessURL() *WSClient {
	c.urlRole = wsURLRoleBusiness
	c.applyEndpointURL()
	return c
}

func (c *WSClient) WithEnvironment(env Environment) *WSClient {
	c.Environment = defaultEnvironment(env)
	c.applyEndpointURL()
	return c
}

func (c *WSClient) WithDemoHostProfile(profile DemoHostProfile) *WSClient {
	c.DemoHostProfile = defaultDemoHostProfile(profile)
	c.applyEndpointURL()
	return c
}

func (c *WSClient) WithURL(rawURL string) *WSClient {
	c.URL = rawURL
	c.explicitURL = true
	c.endpointErr = nil
	return c
}

func (c *WSClient) applyEndpointURL() {
	if c.explicitURL {
		c.endpointErr = nil
		return
	}
	endpoints, err := DefaultEndpointURLs(c.Environment, c.DemoHostProfile)
	if err != nil {
		c.URL = ""
		c.endpointErr = err
		return
	}
	c.endpointErr = nil
	switch c.urlRole {
	case wsURLRolePrivate:
		c.URL = endpoints.WSPrivate
	case wsURLRoleBusiness:
		c.URL = endpoints.WSBusiness
	default:
		c.URL = endpoints.WSPublic
	}
}

func (c *WSClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Conn != nil {
		return nil
	}
	if c.endpointErr != nil {
		return c.endpointErr
	}
	if c.URL == "" {
		return fmt.Errorf("okx: websocket URL is empty")
	}

	// Use lifecycle context with 10 second timeout for connection
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()

	conn, _, err := c.Dialer.DialContext(ctx, c.URL, nil)
	if err != nil {
		return err
	}
	c.Conn = conn

	go c.readLoop()
	go c.pingLoop()

	// If private, login immediately
	if c.IsPrivate {
		c.Connected = make(chan bool, 1) // Buffered channel to prevent blocking
		if err := c.Login(); err != nil {
			c.Conn.Close()
			c.Conn = nil
			return err
		}
		// wait connected
		timeout := time.NewTimer(10 * time.Second)
		select {
		case <-timeout.C:
			c.Conn.Close()
			c.Conn = nil
			return fmt.Errorf("timeout waiting for connection")
		case <-c.Connected:
			// do nothing
		case <-ctx.Done():
			c.Conn.Close()
			c.Conn = nil
			return ctx.Err()
		}
	}

	return nil
}

// IsConnected reports whether the underlying socket is currently established.
// It is best-effort: the transport auto-reconnects in the background, so a drop
// may briefly still read as connected until the read loop detects the failure.
func (c *WSClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn != nil
}

func (c *WSClient) pingLoop() {
	ticker := time.NewTicker(15 * time.Second)
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.WriteMu.Lock()
			err := c.Conn.WriteMessage(websocket.TextMessage, []byte("ping"))
			c.WriteMu.Unlock()
			if err != nil {
				c.Logger.Errorw("WS ping failed", "error", err)
				return
			}
			c.Logger.Debug("WS ping sent")
		}
	}
}

func (c *WSClient) Login() error {
	// Docs say: timestamp as String, e.g. "1597026383.085" (seconds + decimal)
	// Or just unix epoch seconds?
	// OKX V5 WS login: timestamp in seconds as string
	t := time.Now().UTC().Unix()
	timestamp := fmt.Sprintf("%d", t)

	// Sign: timestamp + "GET" + "/users/self/verify"
	preHash := timestamp + "GET" + "/users/self/verify"
	h := hmac.New(sha256.New, []byte(c.SecretKey))
	h.Write([]byte(preHash))
	sign := base64.StdEncoding.EncodeToString(h.Sum(nil))

	req := map[string]interface{}{
		"op": "login",
		"args": []map[string]string{
			{
				"apiKey":     c.ApiKey,
				"passphrase": c.Passphrase,
				"timestamp":  timestamp,
				"sign":       sign,
			},
		},
	}

	c.Logger.Debugw("WS login sent", "req", req)

	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()
	return c.Conn.WriteJSON(req)
}

func (c *WSClient) Subscribe(args WsSubscribeArgs, handler func(data []byte)) error {
	c.mu.Lock()
	c.Subs[args] = handler
	c.mu.Unlock()

	// Request ID
	id := rand.Int63()

	// Channels
	successCh, errorCh := c.AddPendingRequest(id)
	defer c.RemovePendingRequest(id)

	req := map[string]interface{}{
		"id":   id,
		"op":   "subscribe",
		"args": []WsSubscribeArgs{args},
	}
	c.WriteMu.Lock()
	err := c.Conn.WriteJSON(req)
	c.WriteMu.Unlock()
	if err != nil {
		return err
	}
	c.Logger.Debugw("WS subscribe sent", "req", req)

	// Wait for response (ACK or Error) for the subscription
	select {
	case <-successCh:
		// Subscription accepted
		return nil
	case msg := <-errorCh:
		// Parse error message
		var errRes struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
		}
		json.Unmarshal(msg, &errRes)
		return fmt.Errorf("subscribe error: %s - %s", errRes.Code, errRes.Msg)
	case <-time.After(5 * time.Second):
		return fmt.Errorf("subscribe timeout")
	}
}

func (c *WSClient) Unsubscribe(args WsSubscribeArgs) error {
	c.mu.Lock()
	delete(c.Subs, args)
	c.mu.Unlock()

	// Request ID
	id := rand.Int63()

	// Channels
	successCh, errorCh := c.AddPendingRequest(id)
	defer c.RemovePendingRequest(id)

	req := map[string]interface{}{
		"id":   id,
		"op":   "unsubscribe",
		"args": []WsSubscribeArgs{args},
	}
	c.WriteMu.Lock()
	err := c.Conn.WriteJSON(req)
	c.WriteMu.Unlock()
	if err != nil {
		return err
	}
	c.Logger.Debugw("WS unsubscribe sent", "req", req)

	// Wait for response (ACK or Error)
	select {
	case <-successCh:
		return nil
	case msg := <-errorCh:
		var errRes struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
		}
		json.Unmarshal(msg, &errRes)
		return fmt.Errorf("unsubscribe error: %s - %s", errRes.Code, errRes.Msg)
	case <-time.After(5 * time.Second):
		return fmt.Errorf("unsubscribe timeout")
	}
}

func (c *WSClient) readLoop() {
	defer c.Conn.Close()
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			_, msg, err := c.Conn.ReadMessage()
			if err != nil {
				// Check if context was canceled (normal shutdown)
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					c.Logger.Debug("Websocket closed normally")
					return
				}
				// Ignore "use of closed network connection" error which happens on Close()
				if err.Error() != "" && (strings.Contains(err.Error(), "use of closed network connection") || strings.Contains(err.Error(), "closed")) {
					c.Logger.Debug("Websocket connection closed")
					return
				}
				c.reconnect()
				return
			}
			c.Conn.SetReadDeadline(time.Now().Add(ReadTimeout))
			c.handleMessage(msg)
		}
	}
}

func (c *WSClient) handleMessage(msg []byte) {
	c.Logger.Debugw("WS received msg", "msg", string(msg))

	if string(msg) == "pong" {
		c.Logger.Debug("WS received pong")
		return
	}

	var base WsSubscribeRes
	if err := json.Unmarshal(msg, &base); err != nil {
		c.Logger.Errorw("WS parsing msg error", "msg", string(msg), "error", err)
		return
	}

	// id map response
	if base.ID != nil {
		var id int64
		if _, err := fmt.Sscanf(*base.ID, "%d", &id); err == nil {
			c.mu.Lock()
			req, ok := c.PendingReqs[id]
			// We don't delete here immediately because sometimes multiple messages might come?
			// Usually for order place/cancel, it's one ACK.
			// But let's follow standard flow: user calls RemovePendingRequest via defer.
			c.mu.Unlock()

			if ok {
				isError := false
				if base.Code != nil && *base.Code != "0" {
					isError = true
				}
				if base.Event != nil && *base.Event == "error" {
					isError = true
				}

				if isError {
					// Non-blocking send
					select {
					case req.Error <- msg:
					default:
					}
				} else {
					// Success
					select {
					case req.Success <- msg:
					default:
					}
				}
			} else {
				c.Logger.Debugw("WS response ID not found", "id", id)
			}
		}
	}

	if base.Event != nil {
		if *base.Event == "subscribe" {
			return
		}
		if *base.Event == "login" {
			if *base.Code == "0" {
				select {
				case c.Connected <- true:
				default:
				}
			}
			return
		}
		// Error events without ID might be general errors
		if *base.Event == "error" && base.ID == nil {
			c.Logger.Errorw("WS error event:", "msg", string(msg))
			return
		}
	}

	// Data push
	if base.Arg != nil {
		c.mu.Lock()
		// With value-based WsSubscribeArgs, we can do direct lookup
		// Assuming the json unmarshal produces an Arg that matches our subscription exactly
		handler, exists := c.Subs[*base.Arg]
		c.mu.Unlock()

		if exists {
			go handler(msg)
		} else {
			c.Logger.Debugw("WS unhandled arg", "arg", *base.Arg)
		}
	}
}

// PendingRequest holds channels for success and error responses
type PendingRequest struct {
	Success chan []byte
	Error   chan []byte
}

// AddPendingRequest adds a channel for a specific ID
func (c *WSClient) AddPendingRequest(id int64) (chan []byte, chan []byte) {
	successCh := make(chan []byte, 1)
	errorCh := make(chan []byte, 1)

	req := &PendingRequest{
		Success: successCh,
		Error:   errorCh,
	}

	c.mu.Lock()
	c.PendingReqs[id] = req
	c.mu.Unlock()
	return successCh, errorCh
}

// RemovePendingRequest removes the channel for a specific ID
func (c *WSClient) RemovePendingRequest(id int64) {
	c.mu.Lock()
	delete(c.PendingReqs, id)
	c.mu.Unlock()
}

func (c *WSClient) reconnect() {
	// Simple reconnect
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	// Retry loop
	for {
		c.mu.Lock()
		if c.ctx.Err() != nil {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		// Attempt to connect
		// Use a separate context for the dial timeout
		dialCtx, dialCancel := context.WithTimeout(c.ctx, 10*time.Second)
		conn, _, err := c.Dialer.DialContext(dialCtx, c.URL, nil)
		dialCancel()

		if err == nil {
			c.mu.Lock()
			c.Conn = conn
			c.mu.Unlock()

			if c.IsPrivate {
				if err := c.Login(); err != nil {
					c.Logger.Errorw("WS login failed after reconnect", "error", err)
					conn.Close()
					// Continue to next iteration after sleep
				} else {
					c.Logger.Info("WS reconnected and logged in")
					// Restore subscriptions logic...
					c.mu.Lock()
					if len(c.Subs) > 0 {
						var allArgs []WsSubscribeArgs
						for arg := range c.Subs {
							allArgs = append(allArgs, arg)
						}
						c.mu.Unlock()

						req := map[string]interface{}{
							"op":   "subscribe",
							"args": allArgs,
						}
						// Use WriteJSON directly to avoid deadlock if send uses lock,
						// though here we are safe as we aren't holding c.mu
						c.WriteMu.Lock()
						err := conn.WriteJSON(req)
						c.WriteMu.Unlock()
						if err != nil {
							c.Logger.Errorw("WS resubscribe failed", "error", err)
						}
					} else {
						c.mu.Unlock()
					}

					go c.readLoop()
					return
				}
			} else {
				// Public
				c.Logger.Info("WS reconnected")
				c.mu.Lock()
				// Similar resub logic for public...
				if len(c.Subs) > 0 {
					var allArgs []WsSubscribeArgs
					for arg := range c.Subs {
						allArgs = append(allArgs, arg)
					}
					c.mu.Unlock()
					req := map[string]interface{}{
						"op":   "subscribe",
						"args": allArgs,
					}
					c.WriteMu.Lock()
					conn.WriteJSON(req)
					c.WriteMu.Unlock()
				} else {
					c.mu.Unlock()
				}
				go c.readLoop()
				return
			}
		} else {
			c.Logger.Warnw("WS reconnect dial failed", "error", err)
		}

		select {
		case <-c.ctx.Done():
			return
		case <-time.After(time.Second * 2):
		}
	}
}
