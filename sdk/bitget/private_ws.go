package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type PrivateWSClient struct {
	url        string
	apiKey     string
	secretKey  string
	passphrase string
	useSeconds bool

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	writeMu  sync.Mutex
	conn     *websocket.Conn
	closed   bool
	subs     map[string]WSArg
	handlers map[string]func(json.RawMessage)
	loginCh  chan error

	pendingMu       sync.Mutex
	pendingRequests map[string]chan []byte
	requestTimeout  time.Duration
	debug           bool
}

type wsLoginRequest struct {
	Op   string        `json:"op"`
	Args []wsLoginArgs `json:"args"`
}

type wsLoginArgs struct {
	APIKey     string `json:"apiKey"`
	Passphrase string `json:"passphrase"`
	Timestamp  string `json:"timestamp"`
	Sign       string `json:"sign"`
}

type WSOrderMessage struct {
	Arg    WSArg         `json:"arg"`
	Action string        `json:"action"`
	Data   []OrderRecord `json:"data"`
}

type WSPositionMessage struct {
	Arg    WSArg            `json:"arg"`
	Action string           `json:"action"`
	Data   []PositionRecord `json:"data"`
}

type WSFillMessage struct {
	Arg    WSArg        `json:"arg"`
	Action string       `json:"action"`
	Data   []FillRecord `json:"data"`
}

func DecodeOrderMessage(payload []byte) (*WSOrderMessage, error) {
	var msg WSOrderMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func DecodePositionMessage(payload []byte) (*WSPositionMessage, error) {
	var msg WSPositionMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func DecodeFillMessage(payload []byte) (*WSFillMessage, error) {
	var msg WSFillMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func NewPrivateWSClient() *PrivateWSClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &PrivateWSClient{
		url:             privateWSURL,
		ctx:             ctx,
		cancel:          cancel,
		subs:            make(map[string]WSArg),
		handlers:        make(map[string]func(json.RawMessage)),
		pendingRequests: make(map[string]chan []byte),
		requestTimeout:  10 * time.Second,
		debug:           os.Getenv("BITGET_WS_DEBUG") == "1",
	}
}

func (c *PrivateWSClient) WithCredentials(apiKey, secretKey, passphrase string) *PrivateWSClient {
	c.apiKey = apiKey
	c.secretKey = secretKey
	c.passphrase = passphrase
	return c
}

func (c *PrivateWSClient) WithClassicMode() *PrivateWSClient {
	c.url = classicWSURL
	c.useSeconds = true
	return c
}

func (c *PrivateWSClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("bitget private ws: client closed")
	}
	if c.conn != nil {
		c.mu.Unlock()
		return nil
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, nil)
	if err != nil {
		c.mu.Unlock()
		return err
	}

	c.conn = conn
	c.loginCh = make(chan error, 1)
	go c.readLoop(conn)
	go c.pingLoop(conn)

	if err := c.sendLoginLocked(); err != nil {
		_ = conn.Close()
		c.conn = nil
		c.mu.Unlock()
		return err
	}

	loginCh := c.loginCh
	c.mu.Unlock()

	select {
	case err := <-loginCh:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("bitget private ws: login timeout")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *PrivateWSClient) Subscribe(ctx context.Context, arg WSArg, handler func(json.RawMessage)) error {
	if !c.hasCredentials() {
		return fmt.Errorf("bitget private ws: credentials required")
	}
	if err := c.Connect(ctx); err != nil {
		return err
	}

	key := wsKey(arg)
	c.mu.Lock()
	c.subs[key] = arg
	c.handlers[key] = handler
	c.mu.Unlock()

	if err := c.writeJSON(wsRequest{Op: "subscribe", Args: []WSArg{arg}}); err != nil {
		c.mu.Lock()
		delete(c.subs, key)
		delete(c.handlers, key)
		c.mu.Unlock()
		return err
	}
	return nil
}

func (c *PrivateWSClient) Unsubscribe(ctx context.Context, arg WSArg) error {
	_ = ctx
	key := wsKey(arg)
	c.mu.Lock()
	delete(c.subs, key)
	delete(c.handlers, key)
	c.mu.Unlock()

	if err := c.writeJSON(wsRequest{Op: "unsubscribe", Args: []WSArg{arg}}); err != nil && err.Error() != "bitget private ws: not connected" {
		return err
	}
	return nil
}

func (c *PrivateWSClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	if c.conn == nil {
		return nil
	}
	_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(5*time.Second))
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *PrivateWSClient) hasCredentials() bool {
	return c.apiKey != "" && c.secretKey != "" && c.passphrase != ""
}

func (c *PrivateWSClient) sendLoginLocked() error {
	timestamp := buildTimestamp()
	if c.useSeconds {
		timestamp = strconv.FormatInt(time.Now().Unix(), 10)
	}
	signature := sign(c.secretKey, buildPayload(timestamp, http.MethodGet, "/user/verify", "", ""))
	return c.writeJSONLocked(wsLoginRequest{
		Op: "login",
		Args: []wsLoginArgs{{
			APIKey:     c.apiKey,
			Passphrase: c.passphrase,
			Timestamp:  timestamp,
			Sign:       signature,
		}},
	})
}

func (c *PrivateWSClient) writeJSON(v any) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("bitget private ws: not connected")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.debug {
		if payload, err := json.Marshal(v); err == nil {
			fmt.Printf("bitget private ws send: %s\n", string(payload))
		}
	}
	return conn.WriteJSON(v)
}

func (c *PrivateWSClient) writeJSONLocked(v any) error {
	if c.conn == nil {
		return fmt.Errorf("bitget private ws: not connected")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.debug {
		if payload, err := json.Marshal(v); err == nil {
			fmt.Printf("bitget private ws send: %s\n", string(payload))
		}
	}
	return c.conn.WriteJSON(v)
}

func (c *PrivateWSClient) sendRequest(id string, req any) ([]byte, error) {
	return c.sendRequestWithTimeout(id, req, c.requestTimeout)
}

func (c *PrivateWSClient) sendRequestWithTimeout(id string, req any, timeout time.Duration) ([]byte, error) {
	ch := make(chan []byte, 1)

	c.pendingMu.Lock()
	c.pendingRequests[id] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pendingRequests, id)
		c.pendingMu.Unlock()
	}()

	if err := c.writeJSON(req); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if err := extractEventError(resp); err != nil {
			return nil, err
		}
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("bitget private ws: request timeout")
	}
}

func (c *PrivateWSClient) pingLoop(conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		c.mu.RLock()
		active := c.conn == conn
		c.mu.RUnlock()
		if !active {
			return
		}
		c.writeMu.Lock()
		err := conn.WriteMessage(websocket.TextMessage, []byte("ping"))
		c.writeMu.Unlock()
		if err != nil {
			return
		}
	}
}

func (c *PrivateWSClient) readLoop(conn *websocket.Conn) {
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			if c.conn == conn {
				c.conn = nil
			}
			closed := c.closed
			loginCh := c.loginCh
			c.mu.Unlock()
			if loginCh != nil {
				select {
				case loginCh <- err:
				default:
				}
			}
			if !closed {
				go c.reconnect()
			}
			return
		}

		if string(payload) == "pong" {
			continue
		}
		if c.debug {
			fmt.Printf("bitget private ws recv: %s\n", string(payload))
		}

		if c.dispatchPendingResponse(payload) {
			continue
		}

		var env WSEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}

		if env.Event == "login" {
			c.mu.RLock()
			loginCh := c.loginCh
			c.mu.RUnlock()
			if loginCh != nil {
				if env.Code == "" || string(env.Code) == "0" {
					select {
					case loginCh <- nil:
					default:
					}
				} else {
					select {
					case loginCh <- fmt.Errorf("bitget private ws: login failed: %s %s", env.Code, env.Msg):
					default:
					}
				}
			}
			continue
		}

		if env.Event == "error" || (env.Arg.Topic == "" && env.Arg.Channel == "") {
			continue
		}

		key := wsKey(env.Arg)
		c.mu.RLock()
		handler := c.handlers[key]
		c.mu.RUnlock()
		if handler != nil {
			handler(payload)
		}
	}
}

func (c *PrivateWSClient) dispatchPendingResponse(payload []byte) bool {
	id, ok := extractResponseID(payload)
	if !ok {
		if !isStandaloneError(payload) {
			return false
		}
		c.pendingMu.Lock()
		if len(c.pendingRequests) != 1 {
			c.pendingMu.Unlock()
			return false
		}
		for _, only := range c.pendingRequests {
			select {
			case only <- payload:
			default:
			}
			c.pendingMu.Unlock()
			return true
		}
		c.pendingMu.Unlock()
		return false
	}

	c.pendingMu.Lock()
	ch, found := c.pendingRequests[id]
	c.pendingMu.Unlock()
	if !found {
		return false
	}

	select {
	case ch <- payload:
	default:
	}
	return true
}

func extractResponseID(payload []byte) (string, bool) {
	var env struct {
		ID   json.RawMessage `json:"id"`
		Arg  json.RawMessage `json:"arg"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return "", false
	}

	if id := parseResponseID(env.ID); id != "" {
		return id, true
	}
	if id := parseNestedResponseID(env.Arg); id != "" {
		return id, true
	}
	if id := parseNestedResponseID(env.Args); id != "" {
		return id, true
	}
	return "", false
}

func parseNestedResponseID(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed[0] != '[' {
		return ""
	}

	var entries []struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 {
		return ""
	}
	return parseResponseID(entries[0].ID)
}

func parseResponseID(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}

	var asNumber json.Number
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		return asNumber.String()
	}

	return strings.Trim(trimmed, `"`)
}

func isStandaloneError(payload []byte) bool {
	var env struct {
		Event string `json:"event"`
		Code  any    `json:"code"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return false
	}
	return strings.EqualFold(env.Event, "error") && env.Msg != ""
}

func extractEventError(payload []byte) error {
	var env struct {
		Event string       `json:"event"`
		Code  NumberString `json:"code"`
		Msg   string       `json:"msg"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil
	}
	if !strings.EqualFold(env.Event, "error") {
		return nil
	}
	return fmt.Errorf("bitget private ws: %s %s", env.Code, env.Msg)
}

func (c *PrivateWSClient) reconnect() {
	select {
	case <-c.ctx.Done():
		return
	case <-time.After(time.Second):
	}

	if err := c.Connect(c.ctx); err != nil {
		go c.reconnect()
		return
	}

	c.mu.RLock()
	subs := make([]WSArg, 0, len(c.subs))
	for _, arg := range c.subs {
		subs = append(subs, arg)
	}
	c.mu.RUnlock()

	for _, arg := range subs {
		select {
		case <-c.ctx.Done():
			return
		default:
		}
		if err := c.writeJSON(wsRequest{Op: "subscribe", Args: []WSArg{arg}}); err != nil {
			go c.reconnect()
			return
		}
	}
}
