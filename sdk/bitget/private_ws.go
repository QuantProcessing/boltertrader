package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	bitgetPrivateWSHandlerQueueLimit = 1024
	bitgetPrivateWSPingInterval      = 30 * time.Second
	bitgetPrivateWSReadIdleTimeout   = 90 * time.Second
)

type PrivateWSClient struct {
	url        string
	apiKey     string
	secretKey  string
	passphrase string
	useSeconds bool

	ctx    context.Context
	cancel context.CancelFunc

	connectMu      sync.Mutex
	lifecycleMu    sync.Mutex
	mu             sync.RWMutex
	writeMu        sync.Mutex
	conn           *websocket.Conn
	authenticated  bool
	reconnecting   bool
	recoveryWait   chan struct{}
	closed         bool
	subs           map[string]WSArg
	handlers       map[string]func(json.RawMessage)
	dispatcher     *bitgetPrivateWSDataDispatcher
	lifecycle      *bitgetPrivateWSLifecycleQueue
	generation     uint64
	recoveryDrain  <-chan struct{}
	reconnectStart func(error)
	reconnectDone  func()

	pendingMu       sync.Mutex
	pendingRequests map[string]chan []byte
	requestTimeout  time.Duration
	pingInterval    time.Duration
	readIdleTimeout time.Duration
	debug           bool

	subscribeMu            sync.Mutex
	subscriptionMu         sync.Mutex
	subscriptionWaiters    map[string]bitgetSubscriptionWaiter
	subscriptionAckTimeout time.Duration
}

type bitgetSubscriptionWaiter struct {
	conn *websocket.Conn
	ch   chan error
}

type bitgetPrivateWSDispatch struct {
	key     string
	handler func(json.RawMessage)
	payload json.RawMessage
}

type bitgetPrivateWSDataDispatcher struct {
	mu         sync.Mutex
	queue      []bitgetPrivateWSDispatch
	buffer     []bitgetPrivateWSDispatch
	wake       chan struct{}
	limit      int
	stopped    bool
	blocked    bool
	generation uint64
	inFlight   bool
	drain      chan struct{}
}

func newBitgetPrivateWSDataDispatcher() *bitgetPrivateWSDataDispatcher {
	dispatcher := &bitgetPrivateWSDataDispatcher{
		wake:  make(chan struct{}, 1),
		limit: bitgetPrivateWSHandlerQueueLimit,
	}
	go dispatcher.run()
	return dispatcher
}

// enqueue preserves the websocket read order across every private topic. While
// a recovery boundary is active, replacement-connection data is retained in a
// separate bounded buffer until the matching recovered callback returns.
func (d *bitgetPrivateWSDataDispatcher) enqueue(dispatch bitgetPrivateWSDispatch) bool {
	if d == nil || dispatch.handler == nil {
		return true
	}
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return true
	}
	if d.blocked {
		if len(d.buffer) >= d.limit {
			d.mu.Unlock()
			return false
		}
		d.buffer = append(d.buffer, dispatch)
		d.mu.Unlock()
		return true
	}
	if len(d.queue) >= d.limit {
		d.mu.Unlock()
		return false
	}
	d.queue = append(d.queue, dispatch)
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
	return true
}

func (d *bitgetPrivateWSDataDispatcher) beginGap(generation uint64) <-chan struct{} {
	d.mu.Lock()
	d.blocked = true
	d.generation = generation
	d.buffer = nil
	if !d.inFlight && len(d.queue) == 0 {
		d.mu.Unlock()
		drained := make(chan struct{})
		close(drained)
		return drained
	}
	if d.drain == nil {
		d.drain = make(chan struct{})
	}
	drain := d.drain
	d.mu.Unlock()
	d.signal()
	return drain
}

func (d *bitgetPrivateWSDataDispatcher) resetGapBuffer(generation uint64) {
	d.mu.Lock()
	if d.blocked && d.generation == generation {
		d.buffer = nil
	}
	d.mu.Unlock()
}

func (d *bitgetPrivateWSDataDispatcher) finishGap(generation uint64) {
	d.mu.Lock()
	if d.stopped || !d.blocked || d.generation != generation {
		d.mu.Unlock()
		return
	}
	d.blocked = false
	if len(d.buffer) > 0 {
		d.queue = append(d.queue, d.buffer...)
		d.buffer = nil
	}
	d.mu.Unlock()
	d.signal()
}

func (d *bitgetPrivateWSDataDispatcher) dropKey(key string) {
	d.mu.Lock()
	d.queue = filterBitgetPrivateWSDispatches(d.queue, key)
	d.buffer = filterBitgetPrivateWSDispatches(d.buffer, key)
	d.closeDrainLocked()
	d.mu.Unlock()
}

func (d *bitgetPrivateWSDataDispatcher) closeDrainLocked() {
	if d.inFlight || len(d.queue) != 0 || d.drain == nil {
		return
	}
	close(d.drain)
	d.drain = nil
}

func filterBitgetPrivateWSDispatches(entries []bitgetPrivateWSDispatch, key string) []bitgetPrivateWSDispatch {
	kept := make([]bitgetPrivateWSDispatch, 0, len(entries))
	for _, entry := range entries {
		if entry.key == key {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func (d *bitgetPrivateWSDataDispatcher) stop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.stopped = true
	d.queue = nil
	d.buffer = nil
	d.mu.Unlock()
	d.signal()
}

func (d *bitgetPrivateWSDataDispatcher) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *bitgetPrivateWSDataDispatcher) run() {
	for range d.wake {
		for {
			d.mu.Lock()
			if d.stopped {
				d.mu.Unlock()
				return
			}
			if len(d.queue) == 0 {
				d.closeDrainLocked()
				d.mu.Unlock()
				break
			}
			dispatch := d.queue[0]
			if len(d.queue) == 1 {
				d.queue = nil
			} else {
				d.queue[0] = bitgetPrivateWSDispatch{}
				d.queue = d.queue[1:]
			}
			d.inFlight = true
			d.mu.Unlock()

			dispatch.handler(dispatch.payload)

			d.mu.Lock()
			d.inFlight = false
			d.closeDrainLocked()
			d.mu.Unlock()
		}
	}
}

type bitgetPrivateWSLifecycleKind uint8

const (
	bitgetPrivateWSLifecycleStarted bitgetPrivateWSLifecycleKind = iota + 1
	bitgetPrivateWSLifecycleRecovered
)

type bitgetPrivateWSLifecycleCallback struct {
	kind       bitgetPrivateWSLifecycleKind
	generation uint64
	callback   func() bool
}

// bitgetPrivateWSLifecycleQueue serializes control callbacks across recovery
// generations. Started is queued independently of transport recovery but its
// callback observes the data-dispatch drain barrier before it runs.
type bitgetPrivateWSLifecycleQueue struct {
	mu       sync.Mutex
	queue    []bitgetPrivateWSLifecycleCallback
	wake     chan struct{}
	stopped  bool
	inFlight bitgetPrivateWSLifecycleKind
	gapOpen  bool
}

func newBitgetPrivateWSLifecycleQueue() *bitgetPrivateWSLifecycleQueue {
	queue := &bitgetPrivateWSLifecycleQueue{wake: make(chan struct{}, 1)}
	go queue.run()
	return queue
}

func (q *bitgetPrivateWSLifecycleQueue) enqueueStarted(generation uint64, callback func() bool) {
	if q == nil {
		return
	}
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return
	}
	filtered := make([]bitgetPrivateWSLifecycleCallback, 0, len(q.queue))
	removedRecovered := false
	for _, entry := range q.queue {
		if entry.kind == bitgetPrivateWSLifecycleRecovered {
			removedRecovered = true
			continue
		}
		filtered = append(filtered, entry)
	}
	q.queue = filtered
	hasStarted := false
	for _, entry := range q.queue {
		if entry.kind == bitgetPrivateWSLifecycleStarted {
			hasStarted = true
			break
		}
	}
	needsStarted := !q.gapOpen || (q.inFlight == bitgetPrivateWSLifecycleRecovered && !hasStarted)
	if !removedRecovered && needsStarted {
		q.queue = append(q.queue, bitgetPrivateWSLifecycleCallback{
			kind:       bitgetPrivateWSLifecycleStarted,
			generation: generation,
			callback:   callback,
		})
		q.gapOpen = true
	}
	q.mu.Unlock()
	q.signal()
}

func (q *bitgetPrivateWSLifecycleQueue) enqueueRecovered(generation uint64, callback func() bool) {
	if q == nil {
		return
	}
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return
	}
	q.queue = append(q.queue, bitgetPrivateWSLifecycleCallback{
		kind:       bitgetPrivateWSLifecycleRecovered,
		generation: generation,
		callback:   callback,
	})
	q.mu.Unlock()
	q.signal()
}

func (q *bitgetPrivateWSLifecycleQueue) stop() {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.stopped = true
	q.queue = nil
	q.inFlight = 0
	q.gapOpen = false
	q.mu.Unlock()
	q.signal()
}

func (q *bitgetPrivateWSLifecycleQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *bitgetPrivateWSLifecycleQueue) run() {
	for range q.wake {
		for {
			q.mu.Lock()
			if q.stopped {
				q.mu.Unlock()
				return
			}
			if len(q.queue) == 0 {
				q.mu.Unlock()
				break
			}
			entry := q.queue[0]
			if len(q.queue) == 1 {
				q.queue = nil
			} else {
				q.queue[0] = bitgetPrivateWSLifecycleCallback{}
				q.queue = q.queue[1:]
			}
			q.inFlight = entry.kind
			q.mu.Unlock()

			applied := true
			if entry.callback != nil {
				applied = entry.callback()
			}
			q.mu.Lock()
			q.inFlight = 0
			if entry.kind == bitgetPrivateWSLifecycleRecovered && applied {
				hasStarted := false
				for _, pending := range q.queue {
					if pending.kind == bitgetPrivateWSLifecycleStarted {
						hasStarted = true
						break
					}
				}
				if !hasStarted {
					q.gapOpen = false
				}
			}
			q.mu.Unlock()
		}
	}
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

type WSAccountMessage struct {
	Arg    WSArg          `json:"arg"`
	Action string         `json:"action"`
	Data   []AccountAsset `json:"data"`
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

func DecodeAccountMessage(payload []byte) (*WSAccountMessage, error) {
	var envelope struct {
		Arg    WSArg             `json:"arg"`
		Action string            `json:"action"`
		Data   []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	msg := &WSAccountMessage{
		Arg:    envelope.Arg,
		Action: envelope.Action,
	}
	for _, raw := range envelope.Data {
		var shape struct {
			Coin json.RawMessage `json:"coin"`
		}
		if err := json.Unmarshal(raw, &shape); err != nil {
			return nil, err
		}
		if strings.HasPrefix(strings.TrimSpace(string(shape.Coin)), "[") {
			var assets []AccountAsset
			if err := json.Unmarshal(shape.Coin, &assets); err != nil {
				return nil, err
			}
			msg.Data = append(msg.Data, assets...)
			continue
		}
		var asset AccountAsset
		if err := json.Unmarshal(raw, &asset); err != nil {
			return nil, err
		}
		msg.Data = append(msg.Data, asset)
	}
	return msg, nil
}

func NewPrivateWSClient() *PrivateWSClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &PrivateWSClient{
		url:                    privateWSURL,
		ctx:                    ctx,
		cancel:                 cancel,
		subs:                   make(map[string]WSArg),
		handlers:               make(map[string]func(json.RawMessage)),
		dispatcher:             newBitgetPrivateWSDataDispatcher(),
		lifecycle:              newBitgetPrivateWSLifecycleQueue(),
		pendingRequests:        make(map[string]chan []byte),
		requestTimeout:         10 * time.Second,
		pingInterval:           bitgetPrivateWSPingInterval,
		readIdleTimeout:        bitgetPrivateWSReadIdleTimeout,
		debug:                  os.Getenv("BITGET_WS_DEBUG") == "1",
		subscriptionWaiters:    make(map[string]bitgetSubscriptionWaiter),
		subscriptionAckTimeout: 5 * time.Second,
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

// SetReconnectHooks reports unexpected authenticated-session loss and the
// point at which a fresh authenticated connection has restored every private
// subscription. Initial connection attempts do not invoke these hooks.
// Callbacks are dispatched asynchronously so user code cannot stall recovery.
func (c *PrivateWSClient) SetReconnectHooks(started func(error), recovered func()) {
	c.mu.Lock()
	c.reconnectStart = started
	c.reconnectDone = recovered
	c.mu.Unlock()
}

func (c *PrivateWSClient) startReconnectLocked() bool {
	if c.closed || c.reconnecting {
		return false
	}
	c.reconnecting = true
	c.recoveryWait = make(chan struct{})
	c.generation++
	c.recoveryDrain = c.dispatcher.beginGap(c.generation)
	return true
}

func (c *PrivateWSClient) finishReconnectLocked() {
	if !c.reconnecting {
		return
	}
	c.reconnecting = false
	if c.recoveryWait != nil {
		close(c.recoveryWait)
		c.recoveryWait = nil
	}
}

func (c *PrivateWSClient) waitForRecovery(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.RLock()
		closed := c.closed
		reconnecting := c.reconnecting
		wait := c.recoveryWait
		c.mu.RUnlock()
		if closed {
			return fmt.Errorf("bitget private ws: client closed")
		}
		if !reconnecting {
			return nil
		}
		if wait == nil {
			return fmt.Errorf("bitget private ws: recovery state unavailable")
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		case <-c.ctx.Done():
			return fmt.Errorf("bitget private ws: client closed")
		}
	}
}

// lockSubscriptionLifecycle returns with lifecycleMu held. A caller that
// arrives during recovery waits until the saved subscription set is restored,
// so it cannot use the replacement connection before replay completes.
func (c *PrivateWSClient) lockSubscriptionLifecycle(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := c.waitForRecovery(ctx); err != nil {
			return err
		}
		c.lifecycleMu.Lock()
		c.mu.RLock()
		closed := c.closed
		reconnecting := c.reconnecting
		c.mu.RUnlock()
		if closed {
			c.lifecycleMu.Unlock()
			return fmt.Errorf("bitget private ws: client closed")
		}
		if reconnecting {
			c.lifecycleMu.Unlock()
			continue
		}
		if err := c.Connect(ctx); err != nil {
			c.lifecycleMu.Unlock()
			return err
		}
		c.mu.RLock()
		closed = c.closed
		reconnecting = c.reconnecting
		c.mu.RUnlock()
		if closed {
			c.lifecycleMu.Unlock()
			return fmt.Errorf("bitget private ws: client closed")
		}
		if reconnecting {
			c.lifecycleMu.Unlock()
			continue
		}
		return nil
	}
}

func (c *PrivateWSClient) Connect(ctx context.Context) error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	c.mu.RLock()
	closed := c.closed
	conn := c.conn
	authenticated := c.authenticated
	c.mu.RUnlock()
	if closed {
		return fmt.Errorf("bitget private ws: client closed")
	}
	if conn != nil && authenticated {
		return nil
	}
	if conn != nil {
		c.clearConnection(conn)
	}

	conn, _, err := websocketDialerFromEnvironment().DialContext(ctx, c.url, nil)
	if err != nil {
		return err
	}

	loginCh := make(chan error, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return fmt.Errorf("bitget private ws: client closed")
	}
	c.conn = conn
	c.authenticated = false
	c.mu.Unlock()
	go c.readLoop(conn, loginCh)
	go c.pingLoop(conn)

	if err := c.sendLogin(conn); err != nil {
		c.clearConnection(conn)
		return err
	}

	var authErr error
	select {
	case authErr = <-loginCh:
	case <-time.After(5 * time.Second):
		authErr = fmt.Errorf("bitget private ws: login timeout")
	case <-ctx.Done():
		authErr = ctx.Err()
	}
	if authErr != nil {
		c.clearConnection(conn)
		return authErr
	}
	c.mu.RLock()
	ready := c.conn == conn && c.authenticated && !c.closed
	c.mu.RUnlock()
	if !ready {
		c.clearConnection(conn)
		return fmt.Errorf("bitget private ws: authentication connection lost")
	}
	return nil
}

func (c *PrivateWSClient) Subscribe(ctx context.Context, arg WSArg, handler func(json.RawMessage)) error {
	if !c.hasCredentials() {
		return fmt.Errorf("bitget private ws: credentials required")
	}
	if err := c.lockSubscriptionLifecycle(ctx); err != nil {
		return err
	}

	key := wsKey(arg)
	c.mu.Lock()
	conn := c.conn
	previousSub, hadPreviousSub := c.subs[key]
	previousHandler, hadPreviousHandler := c.handlers[key]
	c.subs[key] = arg
	c.handlers[key] = handler
	c.mu.Unlock()
	if conn == nil {
		c.mu.Lock()
		c.restoreSubscriptionLocked(key, previousSub, hadPreviousSub, previousHandler, hadPreviousHandler)
		c.mu.Unlock()
		c.lifecycleMu.Unlock()
		return fmt.Errorf("bitget private ws: not connected")
	}

	if err := c.subscribeOnConn(ctx, conn, arg); err != nil {
		c.mu.Lock()
		c.restoreSubscriptionLocked(key, previousSub, hadPreviousSub, previousHandler, hadPreviousHandler)
		recoverExisting := len(c.subs) > 0
		c.mu.Unlock()
		startRecovery := c.detachSubscriptionConnection(conn, err, recoverExisting)
		c.lifecycleMu.Unlock()
		if startRecovery != nil {
			startRecovery()
		}
		return err
	}
	c.lifecycleMu.Unlock()
	return nil
}

func (c *PrivateWSClient) Unsubscribe(ctx context.Context, arg WSArg) error {
	_ = ctx
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	key := wsKey(arg)
	c.mu.Lock()
	delete(c.subs, key)
	delete(c.handlers, key)
	dispatcher := c.dispatcher
	c.mu.Unlock()
	dispatcher.dropKey(key)

	if err := c.writeJSON(wsRequest{Op: "unsubscribe", Args: []WSArg{arg}}); err != nil && err.Error() != "bitget private ws: not connected" {
		return err
	}
	return nil
}

func (c *PrivateWSClient) Close() error {
	c.mu.Lock()
	c.closed = true
	c.finishReconnectLocked()
	conn := c.conn
	c.conn = nil
	c.authenticated = false
	dispatcher := c.dispatcher
	lifecycle := c.lifecycle
	cancel := c.cancel
	c.mu.Unlock()

	dispatcher.stop()
	lifecycle.stop()
	if cancel != nil {
		cancel()
	}
	if conn == nil {
		return nil
	}
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(5*time.Second))
	err := conn.Close()
	return err
}

func (c *PrivateWSClient) hasCredentials() bool {
	return c.apiKey != "" && c.secretKey != "" && c.passphrase != ""
}

func (c *PrivateWSClient) sendLogin(conn *websocket.Conn) error {
	timestamp := buildTimestamp()
	if c.useSeconds {
		timestamp = strconv.FormatInt(time.Now().Unix(), 10)
	}
	signature := sign(c.secretKey, buildPayload(timestamp, http.MethodGet, "/user/verify", "", ""))
	return c.writeJSONConn(conn, wsLoginRequest{
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
		if payload, err := marshalPrivateWSDebugPayload(v); err == nil {
			fmt.Printf("bitget private ws send: %s\n", string(payload))
		}
	}
	return conn.WriteJSON(v)
}

func (c *PrivateWSClient) writeJSONConn(conn *websocket.Conn, v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.debug {
		if payload, err := marshalPrivateWSDebugPayload(v); err == nil {
			fmt.Printf("bitget private ws send: %s\n", string(payload))
		}
	}
	return conn.WriteJSON(v)
}

func marshalPrivateWSDebugPayload(v any) ([]byte, error) {
	switch req := v.(type) {
	case wsLoginRequest:
		req.Args = redactWSLoginArgs(req.Args)
		return json.Marshal(req)
	case *wsLoginRequest:
		if req == nil {
			return json.Marshal(req)
		}
		redacted := *req
		redacted.Args = redactWSLoginArgs(req.Args)
		return json.Marshal(redacted)
	default:
		return json.Marshal(v)
	}
}

func redactWSLoginArgs(args []wsLoginArgs) []wsLoginArgs {
	if len(args) == 0 {
		return args
	}
	redacted := make([]wsLoginArgs, len(args))
	copy(redacted, args)
	for i := range redacted {
		redacted[i].APIKey = "<redacted>"
		redacted[i].Passphrase = "<redacted>"
		redacted[i].Sign = "<redacted>"
	}
	return redacted
}

func (c *PrivateWSClient) sendRequest(id string, req any) ([]byte, error) {
	return c.sendRequestContext(context.Background(), id, req)
}

func (c *PrivateWSClient) sendRequestWithTimeout(id string, req any, timeout time.Duration) ([]byte, error) {
	return c.sendRequestContextWithTimeout(context.Background(), id, req, timeout)
}

func (c *PrivateWSClient) sendRequestContext(ctx context.Context, id string, req any) ([]byte, error) {
	return c.sendRequestContextWithTimeout(ctx, id, req, c.requestTimeout)
}

func (c *PrivateWSClient) sendRequestContextWithTimeout(ctx context.Context, id string, req any, timeout time.Duration) ([]byte, error) {
	if ctx == nil {
		return nil, fmt.Errorf("bitget private ws: context is required")
	}
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
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *PrivateWSClient) pingLoop(conn *websocket.Conn) {
	interval := c.pingInterval
	if interval <= 0 {
		interval = bitgetPrivateWSPingInterval
	}
	ticker := time.NewTicker(interval)
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
			_ = conn.Close()
			return
		}
	}
}

func (c *PrivateWSClient) readLoop(conn *websocket.Conn, loginCh chan<- error) {
	c.refreshReadDeadline(conn)
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			exact := c.conn == conn
			wasAuthenticated := exact && c.authenticated
			if exact {
				c.conn = nil
				c.authenticated = false
			}
			startReconnect := exact && wasAuthenticated && c.startReconnectLocked()
			reconnectStart := c.reconnectStart
			generation := c.generation
			drain := c.recoveryDrain
			if exact && c.reconnecting && !startReconnect {
				c.dispatcher.resetGapBuffer(generation)
			}
			c.mu.Unlock()
			c.failSubscriptionWaiters(conn, fmt.Errorf("bitget private ws: subscription connection lost: %w", err))
			select {
			case loginCh <- err:
			default:
			}
			if startReconnect {
				c.launchReconnect(err, reconnectStart, generation, drain)
			}
			return
		}
		c.refreshReadDeadline(conn)

		if string(payload) == "pong" {
			continue
		}
		if c.debug {
			fmt.Printf("bitget private ws recv: kind=%s bytes=%d\n", privateWSDebugReceiveKind(payload), len(payload))
		}

		var env WSEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			if c.dispatchPendingResponse(payload) {
				continue
			}
			continue
		}

		if env.Event == "login" {
			var loginErr error
			if env.Code == "" || string(env.Code) == "0" {
				c.mu.Lock()
				if c.conn == conn && !c.closed {
					c.authenticated = true
				} else {
					loginErr = fmt.Errorf("bitget private ws: authentication connection lost")
				}
				c.mu.Unlock()
			} else {
				loginErr = fmt.Errorf("bitget private ws: login failed: %s %s", env.Code, env.Msg)
			}
			select {
			case loginCh <- loginErr:
			default:
			}
			continue
		}

		_, hasResponseID := extractResponseID(payload)
		if strings.EqualFold(env.Event, "subscribe") || (strings.EqualFold(env.Event, "error") && !hasResponseID) {
			if c.resolveSubscriptionWaiter(conn, env) {
				continue
			}
		}
		if c.dispatchPendingResponse(payload) {
			continue
		}

		if env.Event == "error" || (env.Arg.Topic == "" && env.Arg.Channel == "") {
			continue
		}

		c.dispatchPrivateMessage(conn, wsKey(env.Arg), payload)
	}
}

func privateWSDebugReceiveKind(payload []byte) string {
	var metadata struct {
		Event  string          `json:"event"`
		Action string          `json:"action"`
		Arg    WSArg           `json:"arg"`
		ID     json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(payload, &metadata); err != nil {
		return "invalid"
	}
	switch strings.ToLower(strings.TrimSpace(metadata.Event)) {
	case "login":
		return "login"
	case "subscribe", "unsubscribe":
		return "subscription"
	case "error":
		return "error"
	case "":
	default:
		return "event"
	}
	if len(metadata.ID) != 0 {
		return "response"
	}
	if metadata.Action != "" || metadata.Arg.Topic != "" || metadata.Arg.Channel != "" {
		return "data"
	}
	return "unknown"
}

func (c *PrivateWSClient) refreshReadDeadline(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	timeout := c.readIdleTimeout
	if timeout <= 0 {
		timeout = bitgetPrivateWSReadIdleTimeout
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		_ = conn.Close()
	}
}

func (c *PrivateWSClient) dispatchPrivateMessage(conn *websocket.Conn, key string, payload []byte) {
	c.mu.Lock()
	if c.closed || c.conn != conn || !c.authenticated {
		c.mu.Unlock()
		return
	}
	handler := c.handlers[key]
	dispatcher := c.dispatcher
	c.mu.Unlock()
	if handler == nil || dispatcher == nil {
		return
	}
	if dispatcher.enqueue(bitgetPrivateWSDispatch{
		key:     key,
		handler: handler,
		payload: append(json.RawMessage(nil), payload...),
	}) {
		return
	}
	c.handlePrivateDispatchOverflow(conn, key)
}

func (c *PrivateWSClient) handlePrivateDispatchOverflow(conn *websocket.Conn, key string) {
	cause := fmt.Errorf("bitget private ws: handler queue overflow for %s", key)
	c.mu.Lock()
	exact := !c.closed && c.conn == conn && c.authenticated
	if exact {
		c.conn = nil
		c.authenticated = false
	}
	startReconnect := exact && c.startReconnectLocked()
	reconnectStart := c.reconnectStart
	generation := c.generation
	drain := c.recoveryDrain
	if exact && c.reconnecting && !startReconnect {
		c.dispatcher.resetGapBuffer(generation)
	}
	c.mu.Unlock()
	if !exact {
		return
	}
	c.failSubscriptionWaiters(conn, cause)
	_ = conn.Close()
	if startReconnect {
		c.launchReconnect(cause, reconnectStart, generation, drain)
	}
}

func (c *PrivateWSClient) clearConnection(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	c.mu.Lock()
	if c.conn == conn {
		c.conn = nil
		c.authenticated = false
		if c.reconnecting {
			c.dispatcher.resetGapBuffer(c.generation)
		}
	}
	c.mu.Unlock()
	c.failSubscriptionWaiters(conn, fmt.Errorf("bitget private ws: subscription connection closed"))
	_ = conn.Close()
}

func (c *PrivateWSClient) restoreSubscriptionLocked(
	key string,
	arg WSArg,
	hadArg bool,
	handler func(json.RawMessage),
	hadHandler bool,
) {
	if hadArg {
		c.subs[key] = arg
	} else {
		delete(c.subs, key)
	}
	if hadHandler {
		c.handlers[key] = handler
	} else {
		delete(c.handlers, key)
	}
}

func (c *PrivateWSClient) detachSubscriptionConnection(conn *websocket.Conn, cause error, recoverExisting bool) func() {
	if conn == nil {
		return nil
	}
	c.mu.Lock()
	exact := c.conn == conn
	if exact {
		c.conn = nil
		c.authenticated = false
	}
	startReconnect := exact && recoverExisting && c.startReconnectLocked()
	reconnectStart := c.reconnectStart
	generation := c.generation
	drain := c.recoveryDrain
	c.mu.Unlock()
	c.failSubscriptionWaiters(conn, fmt.Errorf("bitget private ws: subscription connection closed: %w", cause))
	_ = conn.Close()
	if !startReconnect {
		return nil
	}
	return func() {
		c.launchReconnect(cause, reconnectStart, generation, drain)
	}
}

func (c *PrivateWSClient) launchReconnect(
	cause error,
	reconnectStart func(error),
	generation uint64,
	drain <-chan struct{},
) {
	c.lifecycle.enqueueStarted(generation, func() bool {
		if drain != nil {
			select {
			case <-drain:
			case <-c.ctx.Done():
				return false
			}
		}
		if reconnectStart != nil {
			reconnectStart(cause)
		}
		return true
	})
	go c.reconnect()
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

func (c *PrivateWSClient) subscribeOnConn(ctx context.Context, conn *websocket.Conn, arg WSArg) error {
	if conn == nil {
		return fmt.Errorf("bitget private ws: not connected")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c.subscribeMu.Lock()
	defer c.subscribeMu.Unlock()
	key := wsKey(arg)
	waiter := bitgetSubscriptionWaiter{conn: conn, ch: make(chan error, 1)}
	c.subscriptionMu.Lock()
	c.subscriptionWaiters[key] = waiter
	c.subscriptionMu.Unlock()
	defer func() {
		c.subscriptionMu.Lock()
		delete(c.subscriptionWaiters, key)
		c.subscriptionMu.Unlock()
	}()

	if err := c.writeJSONConn(conn, wsRequest{Op: "subscribe", Args: []WSArg{arg}}); err != nil {
		return err
	}
	timeout := c.subscriptionAckTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-waiter.ch:
		if err != nil {
			return fmt.Errorf("bitget private ws: subscribe failed for %s: %w", key, err)
		}
		return nil
	case <-timer.C:
		return fmt.Errorf("bitget private ws: subscription timeout for %s", key)
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return fmt.Errorf("bitget private ws: client closed")
	}
}

func (c *PrivateWSClient) resolveSubscriptionWaiter(conn *websocket.Conn, env WSEnvelope) bool {
	key := wsKey(env.Arg)
	c.subscriptionMu.Lock()
	waiter, ok := c.subscriptionWaiters[key]
	if ok && waiter.conn == conn {
		delete(c.subscriptionWaiters, key)
	} else {
		ok = false
	}
	c.subscriptionMu.Unlock()
	if !ok {
		return false
	}

	var err error
	code := strings.TrimSpace(string(env.Code))
	if strings.EqualFold(env.Event, "error") || (code != "" && code != "0") {
		err = fmt.Errorf("bitget private ws: subscribe failed: %s %s", env.Code, env.Msg)
	}
	select {
	case waiter.ch <- err:
	default:
	}
	return true
}

func (c *PrivateWSClient) failSubscriptionWaiters(conn *websocket.Conn, err error) {
	if conn == nil {
		return
	}
	c.subscriptionMu.Lock()
	waiters := make([]bitgetSubscriptionWaiter, 0)
	for key, waiter := range c.subscriptionWaiters {
		if waiter.conn == conn {
			delete(c.subscriptionWaiters, key)
			waiters = append(waiters, waiter)
		}
	}
	c.subscriptionMu.Unlock()
	for _, waiter := range waiters {
		select {
		case waiter.ch <- err:
		default:
		}
	}
}

func (c *PrivateWSClient) reconnect() {
	for {
		select {
		case <-c.ctx.Done():
			c.mu.Lock()
			c.finishReconnectLocked()
			c.mu.Unlock()
			return
		case <-time.After(time.Second):
		}

		if err := c.Connect(c.ctx); err != nil {
			continue
		}
		c.mu.RLock()
		conn := c.conn
		authenticated := c.authenticated
		c.mu.RUnlock()
		if conn == nil || !authenticated {
			continue
		}
		if err := c.resubscribeAll(conn); err != nil {
			c.clearConnection(conn)
			continue
		}

		c.mu.Lock()
		if c.closed || c.conn != conn || !c.authenticated {
			c.mu.Unlock()
			continue
		}
		generation := c.generation
		reconnectDone := c.reconnectDone
		dispatcher := c.dispatcher
		c.lifecycle.enqueueRecovered(generation, func() bool {
			c.mu.RLock()
			current := !c.closed && !c.reconnecting && c.generation == generation
			c.mu.RUnlock()
			if !current {
				return false
			}
			if reconnectDone != nil {
				reconnectDone()
			}
			c.mu.RLock()
			current = !c.closed && !c.reconnecting && c.generation == generation
			c.mu.RUnlock()
			if current {
				dispatcher.finishGap(generation)
			}
			return current
		})
		c.finishReconnectLocked()
		c.mu.Unlock()
		return
	}
}

func (c *PrivateWSClient) resubscribeAll(conn *websocket.Conn) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if conn == nil {
		return fmt.Errorf("bitget private ws: not connected")
	}
	c.mu.RLock()
	keys := make([]string, 0, len(c.subs))
	for key := range c.subs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	subs := make([]WSArg, 0, len(keys))
	for _, key := range keys {
		subs = append(subs, c.subs[key])
	}
	c.mu.RUnlock()

	for _, arg := range subs {
		if err := c.subscribeOnConn(c.ctx, conn, arg); err != nil {
			return err
		}
	}
	return nil
}
