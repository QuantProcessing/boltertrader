package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// WsApiClient handles executes and queries via WebSocket.
// It maps responses to requests via ID.
type WsApiClient struct {
	*baseWsClient
	restClient *Client
	Signer     *Signer
	requests   sync.Map // map[string]chan *WsResponse
	subaccount string
	Logger     *zap.SugaredLogger
	ctx        context.Context
}

var gatewayRequestSeq atomic.Uint64

func NewWsApiClient(ctx context.Context, restClient *Client) (*WsApiClient, error) {
	if restClient == nil {
		return nil, fmt.Errorf("nado ws api client: rest client is required")
	}
	profile := restClient.Profile()
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	if restClient.Signer == nil {
		return nil, ErrCredentialsRequired
	}
	c := &WsApiClient{
		ctx:        ctx,
		restClient: restClient,
		Signer:     restClient.Signer,
		subaccount: restClient.subaccount,
		Logger:     zap.NewNop().Sugar().Named("nado-gateway"),
	}
	// Pass handleMessage as callback
	c.baseWsClient = newBaseWsClient(c.ctx, profile.GatewayWSURL(), c.handleMessage)
	return c, nil
}

func (c *WsApiClient) SetSubaccount(sub string) error {
	if len([]byte(sub)) > 12 {
		return fmt.Errorf("nado ws api client: subaccount name exceeds 12 bytes")
	}
	c.subaccount = sub
	return nil
}

func (c *WsApiClient) handleMessage(msg []byte) {
	var resp WsResponse
	if err := json.Unmarshal(msg, &resp); err != nil {
		c.Logger.Errorw("Error unmarshalling response", "error", err)
		return
	}

	if resp.Id != nil {
		key := strconv.FormatInt(*resp.Id, 10)
		if ch, ok := c.requests.Load(key); ok {
			ch.(chan *WsResponse) <- &resp
		} else {
			c.Logger.Warnw("No id map found for response", "id", *resp.Id, "request_type", resp.RequestType)
		}
	} else {
		c.Logger.Warnw("Received unsolicited response", "request_type", resp.RequestType)
	}
}

// Execute sends a request and waits for a response with matching ID.
func (c *WsApiClient) Execute(ctx context.Context, req map[string]interface{}, sig *string) (*WsResponse, error) {
	id, wait, err := ensureGatewayRequestID(req)
	if err != nil {
		return nil, err
	}
	var ch chan *WsResponse
	if wait {
		ch = make(chan *WsResponse, 1)
		key := strconv.FormatInt(id, 10)
		if _, loaded := c.requests.LoadOrStore(key, ch); loaded {
			return nil, fmt.Errorf("nado gateway request id collision: %d", id)
		}
		defer c.requests.Delete(key)
	}
	if err := c.SendMessage(req); err != nil {
		return nil, err
	}

	if !wait {
		return nil, nil
	}

	select {
	case resp := <-ch:
		if resp.Status != "success" {
			return nil, fmt.Errorf("gateway error (%d): %s", resp.ErrorCode, resp.Error)
		}
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(ReadTimeout):
		return nil, fmt.Errorf("timeout waiting for response")
	}
}

func ensureGatewayRequestID(req map[string]interface{}) (int64, bool, error) {
	if req == nil {
		return 0, false, nil
	}
	for _, key := range []string{"place_order", "cancel_orders", "cancel_product_orders", "cancel_and_place"} {
		body, exists := req[key]
		if !exists {
			continue
		}
		id, ok := requestBodyID(body)
		if ok {
			return id, true, nil
		}
		return 0, false, fmt.Errorf("nado gateway %s request id is required", key)
	}
	return 0, false, nil
}

func nextGatewayRequestID() int64 {
	return 1_000_000_000 + int64(gatewayRequestSeq.Add(1))
}

func requestBodyID(body interface{}) (int64, bool) {
	switch v := body.(type) {
	case map[string]interface{}:
		return numericRequestID(v["id"])
	case ExecTransaction[TxCancelOrders]:
		return v.ID, v.ID > 0
	case ExecTransaction[TxCancelProductOrders]:
		return v.ID, v.ID > 0
	}
	return 0, false
}

func numericRequestID(value interface{}) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		if v == float64(int64(v)) {
			return int64(v), true
		}
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	}
	return 0, false
}
