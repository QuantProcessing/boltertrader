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
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/gorilla/websocket"

	"github.com/QuantProcessing/boltertrader/internal/wsdispatch"
)

const (
	ReadTimeout       = 60 * time.Second
	ReconnectWait     = 5 * time.Second
	subscriptionWait  = 5 * time.Second
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
	pendingReqConns map[int64]*websocket.Conn
	Dialer          *websocket.Dialer
	urlRole         wsURLRole
	explicitURL     bool
	endpointErr     error
	dispatcher      *wsdispatch.Dispatcher
	callbacks       *okxWebsocketCallbackDispatcher

	ctx       context.Context
	connectMu sync.Mutex
	pingOnce  sync.Once

	subscriptionMu sync.Mutex

	reconnectWait        time.Duration
	recovering           bool
	recoveryGeneration   uint64
	authenticatedConn    *websocket.Conn
	readyConn            *websocket.Conn
	onReconnectStarted   func(error)
	onReconnectRecovered func()

	subscriptionTimeout  time.Duration
	pendingSubscriptions map[WsSubscribeArgs]pendingSubscription

	lifecycleCtx      context.Context
	lifecycleCancel   context.CancelFunc
	lifecycleRevision uint64
	closed            bool

	Connected chan bool // for private connection
	Logger    *zap.SugaredLogger
}

type WsClient = WSClient

type pendingSubscription struct {
	handler func([]byte)
}

type wsURLRole int

const (
	wsURLRolePublic wsURLRole = iota
	wsURLRolePrivate
	wsURLRoleBusiness
)

func NewWSClient(ctx context.Context) *WSClient {
	if ctx == nil {
		ctx = context.Background()
	}
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
		if err == nil {
			dialer.Proxy = http.ProxyURL(proxyURL)
		}
	}

	lifecycleCtx, lifecycleCancel := context.WithCancel(ctx)

	// Use provided context for lifecycle management
	client := &WSClient{
		Subs:                 make(map[WsSubscribeArgs]func([]byte)),
		PendingReqs:          make(map[int64]*PendingRequest),
		pendingReqConns:      make(map[int64]*websocket.Conn),
		ctx:                  ctx,
		Dialer:               dialer,
		Logger:               zap.NewNop().Sugar().Named("okx"),
		Connected:            make(chan bool, 1),
		Environment:          Production,
		DemoHostProfile:      DemoHostProfileGlobal,
		urlRole:              wsURLRolePublic,
		dispatcher:           wsdispatch.NewDispatcher(),
		callbacks:            newOKXWebsocketCallbackDispatcher(),
		reconnectWait:        ReconnectWait,
		subscriptionTimeout:  subscriptionWait,
		pendingSubscriptions: make(map[WsSubscribeArgs]pendingSubscription),
		lifecycleCtx:         lifecycleCtx,
		lifecycleCancel:      lifecycleCancel,
		lifecycleRevision:    1,
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

// SetReconnectHooks registers private-stream lifecycle callbacks. Recovered
// runs only after login and every private subscription acknowledgement succeed.
func (c *WSClient) SetReconnectHooks(started func(error), recovered func()) {
	c.mu.Lock()
	c.onReconnectStarted = started
	c.onReconnectRecovered = recovered
	c.mu.Unlock()
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
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()

	lifecycleCtx, lifecycleRevision := c.lifecycleForExplicitConnect()
	conn, fresh, err := c.connectAndLogin(lifecycleCtx, lifecycleRevision)
	if err != nil {
		return err
	}
	if !fresh {
		if err := c.ensureReadyConnection(conn); err != nil {
			return err
		}
		if !c.completeReconnect(conn) {
			c.dropConnection(conn)
			return fmt.Errorf("okx: websocket recovery did not become ready on the captured connection")
		}
		return nil
	}
	if c.IsPrivate {
		err = c.replayPrivateSubscriptionsOnLocked(conn)
	} else {
		err = c.replayPublicSubscriptionsLocked(conn)
	}
	if err != nil {
		c.dropConnection(conn)
		return fmt.Errorf("okx: replay retained subscriptions after connect: %w", err)
	}
	if err := c.markConnectionReady(conn); err != nil {
		c.dropConnection(conn)
		return err
	}
	if !c.completeReconnect(conn) {
		c.dropConnection(conn)
		return fmt.Errorf("okx: websocket recovery did not become ready on the captured connection")
	}
	return nil
}

func (c *WSClient) connectAndLogin(lifecycleCtx context.Context, lifecycleRevision uint64) (*websocket.Conn, bool, error) {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	c.mu.Lock()
	if !c.lifecycleActiveLocked(lifecycleRevision) {
		c.mu.Unlock()
		return nil, false, lifecycleStoppedError(lifecycleCtx)
	}
	if c.Conn != nil {
		conn := c.Conn
		c.mu.Unlock()
		return conn, false, nil
	}
	if c.endpointErr != nil {
		err := c.endpointErr
		c.mu.Unlock()
		return nil, false, err
	}
	if c.URL == "" {
		c.mu.Unlock()
		return nil, false, fmt.Errorf("okx: websocket URL is empty")
	}
	url := c.URL
	isPrivate := c.IsPrivate
	c.mu.Unlock()

	// Use lifecycle context with 10 second timeout for connection
	ctx, cancel := context.WithTimeout(lifecycleCtx, 10*time.Second)
	defer cancel()

	conn, _, err := c.Dialer.DialContext(ctx, url, nil)
	if err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	if !c.lifecycleActiveLocked(lifecycleRevision) {
		c.mu.Unlock()
		_ = conn.Close()
		return nil, false, lifecycleStoppedError(lifecycleCtx)
	}
	if c.Conn != nil {
		current := c.Conn
		c.mu.Unlock()
		_ = conn.Close()
		return current, false, nil
	}
	c.Conn = conn
	c.authenticatedConn = nil
	c.readyConn = nil
	if isPrivate {
		c.Connected = make(chan bool, 1)
	}
	connected := c.Connected
	recovering := c.recovering
	recoveryGeneration := c.recoveryGeneration
	callbacks := c.callbacks
	if callbacks != nil {
		callbacks.activateConnection(recoveryGeneration, conn, recovering)
	}
	c.mu.Unlock()

	go c.readLoop(conn)
	c.pingOnce.Do(func() { go c.pingLoop(lifecycleCtx, lifecycleRevision) })

	// If private, login immediately
	if isPrivate {
		if err := c.loginOn(conn); err != nil {
			c.dropConnection(conn)
			return nil, false, err
		}
		// wait connected
		timeout := time.NewTimer(10 * time.Second)
		defer timeout.Stop()
		select {
		case <-timeout.C:
			c.dropConnection(conn)
			return nil, false, fmt.Errorf("timeout waiting for connection")
		case <-connected:
			// do nothing
		case <-ctx.Done():
			c.dropConnection(conn)
			return nil, false, ctx.Err()
		}
		if err := c.ensureCurrentConnection(conn); err != nil {
			c.dropConnection(conn)
			return nil, false, err
		}
		c.mu.Lock()
		authenticated := c.authenticatedConn == conn
		c.mu.Unlock()
		if !authenticated {
			c.dropConnection(conn)
			return nil, false, fmt.Errorf("okx: login acknowledgement did not authenticate captured connection")
		}
	}

	return conn, true, nil
}

// IsConnected reports whether the underlying socket is currently established.
// It is best-effort: the transport auto-reconnects in the background, so a drop
// may briefly still read as connected until the read loop detects the failure.
func (c *WSClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn != nil
}

func (c *WSClient) pingLoop(lifecycleCtx context.Context, lifecycleRevision uint64) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-lifecycleCtx.Done():
			return
		case <-ticker.C:
			if !c.lifecycleActive(lifecycleRevision) {
				return
			}
			conn := c.currentConnection()
			if conn == nil {
				continue
			}
			c.WriteMu.Lock()
			err := conn.WriteMessage(websocket.TextMessage, []byte("ping"))
			c.WriteMu.Unlock()
			if err != nil {
				c.Logger.Errorw("WS ping failed", "error", err)
				continue
			}
			c.Logger.Debug("WS ping sent")
		}
	}
}

func (c *WSClient) Login() error {
	return c.loginOn(c.currentConnection())
}

func (c *WSClient) loginOn(conn *websocket.Conn) error {
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

	c.Logger.Debugw("WS login sent", "op", "login")

	return c.writeJSONOn(conn, req)
}

func (c *WSClient) Subscribe(args WsSubscribeArgs, handler func(data []byte)) error {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()

	c.beginSubscribe(args, handler)
	err := c.sendSubscribe(args)
	c.mu.Lock()
	delete(c.pendingSubscriptions, args)
	if err == nil {
		c.Subs[args] = handler
	}
	c.mu.Unlock()
	return err
}

func (c *WSClient) sendSubscribe(args WsSubscribeArgs) error {
	return c.sendSubscribeOn(c.currentConnection(), args)
}

func (c *WSClient) sendSubscribeOn(conn *websocket.Conn, args WsSubscribeArgs) error {
	timeout, lifecycleCtx := c.subscriptionWaitConfig()

	// Request ID
	id := rand.Int63()

	// Channels
	successCh, errorCh := c.addPendingRequestOn(id, conn)
	defer c.RemovePendingRequest(id)

	req := map[string]interface{}{
		"id":   id,
		"op":   "subscribe",
		"args": []WsSubscribeArgs{args},
	}
	if err := c.writeJSONOn(conn, req); err != nil {
		return err
	}
	c.Logger.Debugw("WS subscribe sent", "req", req)

	// Wait for response (ACK or Error) for the subscription
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-successCh:
		// The read loop validates and delivers this ACK atomically against the
		// exact socket. Once delivered it is authoritative even if that socket
		// disconnects immediately afterward; reconnect replay will restore the
		// newly committed desired subscription.
		return nil
	case msg := <-errorCh:
		// Parse error message
		var errRes struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
		}
		json.Unmarshal(msg, &errRes)
		return fmt.Errorf("subscribe error: %s - %s", errRes.Code, errRes.Msg)
	case <-timer.C:
		return fmt.Errorf("subscribe timeout")
	case <-lifecycleCtx.Done():
		return lifecycleCtx.Err()
	}
}

func (c *WSClient) Unsubscribe(args WsSubscribeArgs) error {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()

	c.beginUnsubscribe(args)
	if err := c.sendUnsubscribeOn(c.currentConnection(), args); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.Subs, args)
	c.mu.Unlock()
	return nil
}

func (c *WSClient) sendUnsubscribeOn(conn *websocket.Conn, args WsSubscribeArgs) error {
	timeout, lifecycleCtx := c.subscriptionWaitConfig()

	// Request ID
	id := rand.Int63()

	// Channels
	successCh, errorCh := c.addPendingRequestOn(id, conn)
	defer c.RemovePendingRequest(id)

	req := map[string]interface{}{
		"id":   id,
		"op":   "unsubscribe",
		"args": []WsSubscribeArgs{args},
	}
	if err := c.writeJSONOn(conn, req); err != nil {
		return err
	}
	c.Logger.Debugw("WS unsubscribe sent", "req", req)

	// Wait for response (ACK or Error)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
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
	case <-timer.C:
		return fmt.Errorf("unsubscribe timeout")
	case <-lifecycleCtx.Done():
		return lifecycleCtx.Err()
	}
}

func (c *WSClient) readLoop(conn *websocket.Conn) {
	var readErr error
	defer func() { c.handleDisconnect(conn, readErr) }()
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			_, msg, err := conn.ReadMessage()
			if err != nil {
				readErr = err
				c.Logger.Debugw("Websocket read failed", "error", err)
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(ReadTimeout))
			c.handleSocketMessageFrom(conn, msg)
		}
	}
}

func (c *WSClient) handleDisconnect(conn *websocket.Conn, err error) {
	if c.ctx.Err() != nil {
		return
	}
	if err == nil {
		err = fmt.Errorf("okx websocket: connection lost")
	}
	c.mu.Lock()
	if c.closed || c.Conn != conn {
		c.mu.Unlock()
		return
	}
	c.Conn = nil
	if c.authenticatedConn == conn {
		c.authenticatedConn = nil
	}
	if c.readyConn == conn {
		c.readyConn = nil
	}
	startReconnect := !c.recovering
	if startReconnect {
		c.recovering = true
		c.recoveryGeneration++
	}
	handler := c.onReconnectStarted
	lifecycleCtx := c.lifecycleCtx
	lifecycleRevision := c.lifecycleRevision
	generation := c.recoveryGeneration
	callbacks := c.callbacks
	if callbacks != nil {
		if startReconnect {
			callbacks.beginGap(generation, func() {
				if handler != nil {
					handler(err)
				}
			})
		} else if c.recovering {
			callbacks.discardReplacement(generation, conn)
		}
	}
	c.mu.Unlock()
	_ = conn.Close()
	if !startReconnect {
		return
	}
	go c.reconnectLifecycle(lifecycleCtx, lifecycleRevision)
}

func (c *WSClient) handleMessage(msg []byte) {
	c.handleMessageFrom(c.currentConnection(), msg)
}

func (c *WSClient) handleMessageFrom(conn *websocket.Conn, msg []byte) {
	c.handleMessageFromMode(conn, msg, false)
}

func (c *WSClient) handleSocketMessageFrom(conn *websocket.Conn, msg []byte) {
	c.handleMessageFromMode(conn, msg, true)
}

func (c *WSClient) handleMessageFromMode(conn *websocket.Conn, msg []byte, asyncCallback bool) {
	if conn != nil && !c.isCurrentConnection(conn) {
		return
	}
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
			boundConn := c.pendingReqConns[id]
			if ok && conn != nil && (c.Conn != conn || (boundConn != nil && boundConn != conn)) {
				ok = false
			}
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
			}
			c.mu.Unlock()
			if !ok {
				c.Logger.Debugw("WS response ID not found", "id", id)
			}
		}
	}

	if base.Event != nil {
		if *base.Event == "subscribe" {
			return
		}
		if *base.Event == "login" {
			if base.Code != nil && *base.Code == "0" && (conn == nil || c.isCurrentConnection(conn)) {
				c.mu.Lock()
				if conn == nil || c.Conn == conn {
					c.authenticatedConn = conn
				}
				connected := c.Connected
				c.mu.Unlock()
				select {
				case connected <- true:
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
		if pending, ok := c.pendingSubscriptions[*base.Arg]; ok {
			handler = pending.handler
			exists = true
		}
		c.mu.Unlock()

		if exists {
			if asyncCallback {
				c.dispatchMessageAsync(conn, handler, msg)
			} else {
				c.dispatchMessage(handler, msg)
			}
		} else {
			c.Logger.Debugw("WS unhandled arg", "arg", *base.Arg)
		}
	}
}

func (c *WSClient) dispatchMessageAsync(conn *websocket.Conn, handler func([]byte), msg []byte) {
	if handler == nil {
		return
	}
	copied := append([]byte(nil), msg...)
	callback := okxWebsocketCallback{
		kind: okxWebsocketCallbackData,
		conn: conn,
		run:  func() { handler(copied) },
	}
	c.mu.Lock()
	callbacks := c.callbacks
	c.mu.Unlock()
	if callbacks != nil && !callbacks.enqueueData(conn, []okxWebsocketCallback{callback}) {
		c.handleDisconnect(conn, fmt.Errorf("okx websocket: callback queue overflow"))
	}
}

func (c *WSClient) dispatchMessage(handler func([]byte), msg []byte) {
	if handler == nil {
		return
	}
	copied := append([]byte(nil), msg...)
	c.dispatcher.Dispatch(func() {
		handler(copied)
	})
}

func (c *WSClient) PauseDispatch() {
	c.dispatcher.Pause()
}

func (c *WSClient) ResumeDispatch(beforeDrain func()) {
	c.dispatcher.Resume(beforeDrain)
}

func (c *WSClient) Close() {
	c.mu.Lock()
	conn := c.Conn
	c.Conn = nil
	c.authenticatedConn = nil
	c.readyConn = nil
	c.recovering = false
	c.recoveryGeneration++
	c.closed = true
	c.lifecycleRevision++
	cancel := c.lifecycleCancel
	callbacks := c.callbacks
	c.callbacks = nil
	dispatcher := c.dispatcher
	c.mu.Unlock()
	if callbacks != nil {
		callbacks.stop()
	}
	if dispatcher != nil {
		dispatcher.Reset()
	}
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *WSClient) currentConnection() *websocket.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn
}

func (c *WSClient) isCurrentConnection(conn *websocket.Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return conn != nil && c.Conn == conn
}

func (c *WSClient) ensureCurrentConnection(conn *websocket.Conn) error {
	if conn == nil {
		return fmt.Errorf("okx: websocket not connected")
	}
	if !c.isCurrentConnection(conn) {
		return fmt.Errorf("okx: websocket connection changed during recovery")
	}
	return nil
}

func (c *WSClient) ensureReadyConnection(conn *websocket.Conn) error {
	if conn == nil {
		return fmt.Errorf("okx: websocket not connected")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Conn != conn {
		return fmt.Errorf("okx: websocket connection changed during recovery")
	}
	if c.readyConn != conn {
		return fmt.Errorf("okx: websocket connection is not ready")
	}
	return nil
}

func (c *WSClient) markConnectionReady(conn *websocket.Conn) error {
	if conn == nil {
		return fmt.Errorf("okx: websocket not connected")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Conn != conn {
		return fmt.Errorf("okx: websocket connection changed during recovery")
	}
	if c.IsPrivate && c.authenticatedConn != conn {
		return fmt.Errorf("okx: private websocket connection is not authenticated")
	}
	c.readyConn = conn
	return nil
}

func (c *WSClient) writeJSONOn(conn *websocket.Conn, value any) error {
	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}
	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()
	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}
	if err := conn.WriteJSON(value); err != nil {
		return err
	}
	return c.ensureCurrentConnection(conn)
}

func (c *WSClient) writeReadyJSONOn(conn *websocket.Conn, value any) error {
	if err := c.ensureReadyConnection(conn); err != nil {
		return err
	}
	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()
	if err := c.ensureReadyConnection(conn); err != nil {
		return err
	}
	if err := conn.WriteJSON(value); err != nil {
		return err
	}
	return c.ensureCurrentConnection(conn)
}

func (c *WSClient) dropConnection(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	c.mu.Lock()
	if c.Conn == conn {
		c.Conn = nil
	}
	if c.authenticatedConn == conn {
		c.authenticatedConn = nil
	}
	if c.readyConn == conn {
		c.readyConn = nil
	}
	generation := c.recoveryGeneration
	recovering := c.recovering
	callbacks := c.callbacks
	if callbacks != nil && recovering {
		callbacks.discardReplacement(generation, conn)
	}
	c.mu.Unlock()
	_ = conn.Close()
}

func (c *WSClient) completeReconnect(conn *websocket.Conn) bool {
	c.mu.Lock()
	if c.Conn != conn || conn == nil {
		c.mu.Unlock()
		return false
	}
	if c.IsPrivate && c.authenticatedConn != conn {
		c.mu.Unlock()
		return false
	}
	if c.readyConn != conn {
		c.mu.Unlock()
		return false
	}
	if !c.recovering {
		c.mu.Unlock()
		return true
	}
	generation := c.recoveryGeneration
	handler := c.onReconnectRecovered
	callbacks := c.callbacks
	if callbacks != nil && !callbacks.enqueueRecovered(generation, conn, handler) {
		c.mu.Unlock()
		return false
	}
	c.recovering = false
	c.mu.Unlock()
	if callbacks == nil {
		if handler != nil {
			handler()
		}
	}
	return true
}

func (c *WSClient) lifecycleForExplicitConnect() (context.Context, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		c.lifecycleCtx, c.lifecycleCancel = context.WithCancel(c.ctx)
		c.lifecycleRevision++
		c.closed = false
		c.pingOnce = sync.Once{}
		if c.callbacks == nil {
			c.callbacks = newOKXWebsocketCallbackDispatcher()
		}
	}
	return c.lifecycleCtx, c.lifecycleRevision
}

func (c *WSClient) lifecycleActiveLocked(revision uint64) bool {
	return !c.closed && c.lifecycleRevision == revision
}

func (c *WSClient) lifecycleActive(revision uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lifecycleActiveLocked(revision)
}

func (c *WSClient) recoverySatisfied(revision uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lifecycleActiveLocked(revision) {
		return true
	}
	return !c.recovering && c.Conn != nil && c.readyConn == c.Conn
}

func lifecycleStoppedError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}

func (c *WSClient) beginSubscribe(args WsSubscribeArgs, handler func([]byte)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pendingSubscriptions == nil {
		c.pendingSubscriptions = make(map[WsSubscribeArgs]pendingSubscription)
	}
	c.pendingSubscriptions[args] = pendingSubscription{handler: handler}
}

func (c *WSClient) beginUnsubscribe(args WsSubscribeArgs) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pendingSubscriptions, args)
}

func (c *WSClient) subscriptionWaitConfig() (time.Duration, context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	timeout := c.subscriptionTimeout
	if timeout <= 0 {
		timeout = subscriptionWait
	}
	return timeout, c.lifecycleCtx
}

// PendingRequest holds channels for success and error responses
type PendingRequest struct {
	Success chan []byte
	Error   chan []byte
}

// AddPendingRequest adds a channel for a specific ID
func (c *WSClient) AddPendingRequest(id int64) (chan []byte, chan []byte) {
	return c.addPendingRequestOn(id, nil)
}

func (c *WSClient) addPendingRequestOn(id int64, conn *websocket.Conn) (chan []byte, chan []byte) {
	successCh := make(chan []byte, 1)
	errorCh := make(chan []byte, 1)

	req := &PendingRequest{
		Success: successCh,
		Error:   errorCh,
	}

	c.mu.Lock()
	c.PendingReqs[id] = req
	if conn != nil {
		c.pendingReqConns[id] = conn
	} else {
		delete(c.pendingReqConns, id)
	}
	c.mu.Unlock()
	return successCh, errorCh
}

// RemovePendingRequest removes the channel for a specific ID
func (c *WSClient) RemovePendingRequest(id int64) {
	c.mu.Lock()
	delete(c.PendingReqs, id)
	delete(c.pendingReqConns, id)
	c.mu.Unlock()
}

func (c *WSClient) reconnect() {
	c.mu.Lock()
	lifecycleCtx := c.lifecycleCtx
	lifecycleRevision := c.lifecycleRevision
	active := c.lifecycleActiveLocked(lifecycleRevision)
	c.mu.Unlock()
	if !active {
		return
	}
	c.reconnectLifecycle(lifecycleCtx, lifecycleRevision)
}

func (c *WSClient) reconnectLifecycle(lifecycleCtx context.Context, lifecycleRevision uint64) {
	firstAttempt := true
	for {
		if !c.lifecycleActive(lifecycleRevision) {
			return
		}
		if c.recoverySatisfied(lifecycleRevision) {
			return
		}
		c.mu.Lock()
		wait := c.reconnectWait
		c.mu.Unlock()
		if !firstAttempt {
			timer := time.NewTimer(wait)
			select {
			case <-lifecycleCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		firstAttempt = false

		c.subscriptionMu.Lock()
		if c.recoverySatisfied(lifecycleRevision) {
			c.subscriptionMu.Unlock()
			return
		}
		conn, fresh, err := c.connectAndLogin(lifecycleCtx, lifecycleRevision)
		if err != nil {
			c.subscriptionMu.Unlock()
			if !c.lifecycleActive(lifecycleRevision) {
				return
			}
			c.Logger.Warnw("WS reconnect failed", "error", err)
			continue
		}
		if !fresh {
			c.subscriptionMu.Unlock()
			if c.ensureReadyConnection(conn) == nil && c.completeReconnect(conn) {
				return
			}
			c.dropConnection(conn)
			continue
		}
		if c.IsPrivate {
			if err := c.replayPrivateSubscriptionsOnLocked(conn); err != nil {
				c.subscriptionMu.Unlock()
				c.Logger.Errorw("WS private subscription replay failed", "error", err)
				c.dropConnection(conn)
				continue
			}
		} else if err := c.replayPublicSubscriptionsLocked(conn); err != nil {
			c.subscriptionMu.Unlock()
			c.Logger.Errorw("WS public subscription replay failed", "error", err)
			c.dropConnection(conn)
			continue
		}
		if err := c.markConnectionReady(conn); err != nil {
			c.subscriptionMu.Unlock()
			c.Logger.Errorw("WS connection ready transition failed", "error", err)
			c.dropConnection(conn)
			continue
		}
		c.subscriptionMu.Unlock()
		if c.completeReconnect(conn) {
			return
		}
		c.dropConnection(conn)
	}
}

func (c *WSClient) replayPrivateSubscriptionsOn(conn *websocket.Conn) error {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	return c.replayPrivateSubscriptionsOnLocked(conn)
}

func (c *WSClient) replayPrivateSubscriptionsOnLocked(conn *websocket.Conn) error {
	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}
	c.mu.Lock()
	args := make([]WsSubscribeArgs, 0, len(c.Subs))
	for arg := range c.Subs {
		args = append(args, arg)
	}
	c.mu.Unlock()
	for _, arg := range args {
		if err := c.sendSubscribeOn(conn, arg); err != nil {
			return fmt.Errorf("restore %s subscription: %w", arg.Channel, err)
		}
	}
	return c.ensureCurrentConnection(conn)
}

func (c *WSClient) replayPublicSubscriptions(conn *websocket.Conn) error {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	return c.replayPublicSubscriptionsLocked(conn)
}

func (c *WSClient) replayPublicSubscriptionsLocked(conn *websocket.Conn) error {
	c.mu.Lock()
	args := make([]WsSubscribeArgs, 0, len(c.Subs))
	for arg := range c.Subs {
		args = append(args, arg)
	}
	c.mu.Unlock()
	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}
	if len(args) == 0 {
		return nil
	}
	req := map[string]interface{}{
		"op":   "subscribe",
		"args": args,
	}
	return c.writeJSONOn(conn, req)
}
