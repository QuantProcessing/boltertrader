package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// WsApiClient handles executes and queries via WebSocket.
// It maps responses to requests via ID.
type WsApiClient struct {
	*BaseWsClient
	privateKey string
	Signer     *Signer
	requests   sync.Map // map[string]chan *WsResponse
	subaccount string
	Logger     *zap.SugaredLogger
	ctx        context.Context
}

func NewWsApiClient(ctx context.Context, privateKey string) (*WsApiClient, error) {
	signer, err := NewSigner(privateKey)
	if err != nil {
		return nil, err
	}
	c := &WsApiClient{
		ctx:        ctx,
		privateKey: privateKey,
		Signer:     signer,
		subaccount: "default", // Default subaccount
		Logger:     zap.NewNop().Sugar().Named("nado-gateway"),
	}
	// Pass handleMessage as callback
	c.BaseWsClient = NewBaseWsClient(c.ctx, WsURL, c.handleMessage)
	return c, nil
}

func (c *WsApiClient) SetSubaccount(sub string) {
	c.subaccount = sub
}

func (c *WsApiClient) handleMessage(msg []byte) {
	c.Logger.Debugw("Received response", "msg", string(msg))
	var resp WsResponse
	if err := json.Unmarshal(msg, &resp); err != nil {
		c.Logger.Errorw("Error unmarshalling response", "msg", string(msg), "error", err)
		return
	}

	// Check if it's a response with ID
	if resp.Signature != nil {
		if ch, ok := c.requests.Load(*resp.Signature); ok {
			ch.(chan *WsResponse) <- &resp
		} else {
			c.Logger.Warnw("No signature map found for response", "resp", resp)
		}
	} else {
		c.Logger.Warnw("Received unsolicited response", "resp", resp)
	}
}

// Execute sends a request and waits for a response with matching ID.
func (c *WsApiClient) Execute(ctx context.Context, req map[string]interface{}, sig *string) (*WsResponse, error) {
	c.Logger.Debugw("Sending request", "req", req)

	var ch chan *WsResponse
	if sig != nil {
		ch = make(chan *WsResponse, 1)
		c.requests.Store(*sig, ch)
		defer c.requests.Delete(*sig)
	}
	if err := c.SendMessage(req); err != nil {
		return nil, err
	}

	if sig == nil {
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
