package standx

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

type WsApiClient struct {
	client         *Client
	wsClient       *WSClient
	sessionID      string
	LoginRequestID string
	Logger         *zap.SugaredLogger

	ctx    context.Context
	cancel context.CancelFunc

	// requestID -> handler
	pendingRequestIDs map[string]func(data []byte) error
	authDone          chan error
	mu                sync.Mutex
	IsAuth            bool
}

func NewWsApiClient(ctx context.Context, client *Client) *WsApiClient {
	ctx, cancel := context.WithCancel(ctx)
	logger := zap.NewNop().Sugar().Named("standx-ws-api")
	return &WsApiClient{
		client:            client,
		wsClient:          NewWSClient(ctx, APIStreamURL, logger),
		Logger:            logger,
		ctx:               ctx,
		cancel:            cancel,
		pendingRequestIDs: make(map[string]func(data []byte) error),
		authDone:          make(chan error, 1),
	}
}

func (c *WsApiClient) Connect() error {
	c.wsClient.HandleMsg = c.HandleMsg
	c.wsClient.OnReconnect = c.onReconnect
	return c.wsClient.Connect()
}

func (c *WsApiClient) onReconnect() error {
	c.Logger.Info("Restoring API session (Re-Auth)...")
	c.mu.Lock()
	c.IsAuth = false
	c.mu.Unlock()

	// Re-Auth with Retry
	if err := c.doAuthWithRetry(); err != nil {
		c.Logger.Error("Re-Auth failed: ", err)
		return err
	}
	// WsApi usually doesn't have other persistent subscriptions
	return nil
}

func (c *WsApiClient) Auth() error {
	// Try Auth with retry
	return c.doAuthWithRetry()
}

func (c *WsApiClient) doAuthWithRetry() error {
	if c.IsAuth && c.wsClient.IsConnected() {
		return nil
	}
	err := c.performAuth()
	if err == nil {
		c.mu.Lock()
		c.IsAuth = true
		c.mu.Unlock()
		return nil
	}

	c.Logger.Warn("WsApi Auth failed, refreshing token and retrying", zap.Error(err))
	c.client.InvalidateToken()
	err = c.performAuth()
	if err == nil {
		c.mu.Lock()
		c.IsAuth = true
		c.mu.Unlock()
	}
	return err
}

func (c *WsApiClient) performAuth() error {
	c.Logger.Info("Authing in...")
	token, err := c.client.GetToken(c.ctx)
	if err != nil {
		return err
	}

	params := map[string]string{
		"token": token,
	}
	jsonParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	c.LoginRequestID = GenRequestID()
	req := WSRequest{
		SessionID: GenSessionID(),
		RequestID: c.LoginRequestID,
		Method:    "auth:login",
		Params:    string(jsonParams),
	}

	// For WS API, we don't block here?
	// Ideally we should wait for response like AccountClient does.
	// But current implementation just writes.
	// Wait, Check HandleMsg: it checks LoginRequestID.
	// But Auth() function doesn't wait in current code.
	// I should probably make it wait too for consistency, but keeping to refactor scope
	// I will just implement the retry logic assuming it returns error if write fails
	// OR if we enhance it to wait.

	// Issue: If Auth() returns nil immediately (just write success), we don't know if it failed.
	// So we can't retry based on server response here unless we block.
	// Let's implement blocking wait for Auth response here too, to match AccountClient robustness.

	// Drain
	c.mu.Lock()
	select {
	case <-c.authDone:
	default:
	}
	c.mu.Unlock()

	if err := c.wsClient.WriteJSON(req); err != nil {
		return err
	}

	// Wait for response to ensure auth is successful before returning
	select {
	case err := <-c.authDone:
		if err != nil {
			return err
		}
		// Success
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	case <-time.After(10 * time.Second):
		return fmt.Errorf("auth timeout")
	}
}

func (c *WsApiClient) CreateOrder(ctx context.Context, orderReq *CreateOrderRequest) (*WsApiResponse, error) {
	requestID := GenRequestID()
	// Standx docs: use "order:new"
	// We need to sign the payload (params)
	jsonParams, err := json.Marshal(orderReq)
	if err != nil {
		return nil, err
	}
	bodyStr := string(jsonParams)

	// Access Signer from Client
	signer := c.client.GetSigner()
	if signer == nil {
		return nil, fmt.Errorf("signer not initialized")
	}

	timestamp := time.Now().UnixMilli()
	// Sign using requestID we generated
	headers := signer.SignRequest(bodyStr, timestamp, requestID)

	req := WSRequest{
		SessionID: GenSessionID(),
		RequestID: requestID,
		Method:    "order:new",
		Header:    headers,
		Params:    bodyStr,
	}

	// Register Handler
	done := make(chan *WsApiResponse, 1)
	errCh := make(chan error, 1)

	c.RegisterRequestHandler(requestID, func(data []byte) error {
		var resp WsApiResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			errCh <- err
			return err
		}
		done <- &resp
		return nil
	})
	defer c.UnregisterRequestHandler(requestID)

	if err := c.wsClient.WriteJSON(req); err != nil {
		return nil, err
	}

	select {
	case resp := <-done:
		// Return response, caller can check Code
		return resp, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("timeout waiting for order:new response")
	}
}

func (c *WsApiClient) CancelOrder(ctx context.Context, cancelReq *CancelOrderRequest) (*WsApiResponse, error) {
	requestID := GenRequestID()
	// Method "order:cancel"
	jsonParams, err := json.Marshal(cancelReq)
	if err != nil {
		return nil, err
	}
	bodyStr := string(jsonParams)

	signer := c.client.GetSigner()
	if signer == nil {
		return nil, fmt.Errorf("signer not initialized")
	}

	timestamp := time.Now().UnixMilli()
	headers := signer.SignRequest(bodyStr, timestamp, requestID)

	req := WSRequest{
		SessionID: GenSessionID(),
		RequestID: requestID,
		Method:    "order:cancel",
		Header:    headers,
		Params:    bodyStr,
	}

	done := make(chan *WsApiResponse, 1)
	errCh := make(chan error, 1)

	c.RegisterRequestHandler(requestID, func(data []byte) error {
		var resp WsApiResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			errCh <- err
			return err
		}
		done <- &resp
		return nil
	})
	defer c.UnregisterRequestHandler(requestID)

	if err := c.wsClient.WriteJSON(req); err != nil {
		return nil, err
	}

	select {
	case resp := <-done:
		return resp, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Second): // General timeout
		return nil, fmt.Errorf("timeout waiting for order:cancel response")
	}
}

func (c *WsApiClient) RegisterRequestHandler(requestID string, handler func([]byte) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Logger.Debugw("RegisterRequestHandler", "request_id", requestID)
	c.pendingRequestIDs[requestID] = handler
}

func (c *WsApiClient) UnregisterRequestHandler(requestID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.pendingRequestIDs[requestID]; ok {
		c.Logger.Debugw("UnregisterRequestHandler", "request_id", requestID)
		delete(c.pendingRequestIDs, requestID)
	}
}

func (c *WsApiClient) HandleMsg(data []byte) {
	c.Logger.Debug("Received message: ", string(data))
	var resp WsApiResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		c.Logger.Error("Failed to unmarshal message", zap.Error(err))
		return
	}
	c.Logger.Debugw("HandleMsg", "request_id", resp.RequestID, "code", resp.Code, "msg", resp.Message, "raw", string(data))

	c.mu.Lock()
	defer c.mu.Unlock()

	if resp.RequestID == c.LoginRequestID {
		if resp.Code != 0 {
			c.Logger.Error("Auth failed: ", resp)
			select {
			case c.authDone <- fmt.Errorf("auth failed: %s", resp.Message):
			default:
			}
			return
		}
		c.Logger.Info("Auth successful")
		select {
		case c.authDone <- nil:
		default:
		}
		return
	}

	handler, ok := c.pendingRequestIDs[resp.RequestID]
	if ok {
		delete(c.pendingRequestIDs, resp.RequestID)
		c.Logger.Debugw("Found and deleted handler", "request_id", resp.RequestID)
	} else {
		c.Logger.Errorw("No handler found for requestID", "resp_req_id", resp.RequestID)
	}

	if !ok {
		return
	}
	if err := handler(data); err != nil {
		c.Logger.Error("Handler failed: ", err)
	}
}

func (c *WsApiClient) Close() {
	c.mu.Lock()
	c.IsAuth = false
	c.mu.Unlock()
	c.cancel()
	c.wsClient.Close()
}
