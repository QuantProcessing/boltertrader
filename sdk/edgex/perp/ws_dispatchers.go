package perp

import (
	"encoding/json"
	"fmt"
)

// MsgDispatcher interface for handling WebSocket messages
type MsgDispatcher interface {
	Dispatch(data []byte) error
}

// MsgDispatcherRegistry manages dispatchers for different channels
type MsgDispatcherRegistry struct {
	dispatchers map[string]MsgDispatcher
}

func NewMsgDispatcherRegistry() *MsgDispatcherRegistry {
	return &MsgDispatcherRegistry{
		dispatchers: make(map[string]MsgDispatcher),
	}
}

func (r *MsgDispatcherRegistry) Register(channel string, d MsgDispatcher) {
	r.dispatchers[channel] = d
}

func (r *MsgDispatcherRegistry) Dispatch(channel string, data []byte) error {
	if d, ok := r.dispatchers[channel]; ok {
		return d.Dispatch(data)
	}
	return fmt.Errorf("no dispatcher for channel: %s", channel)
}

// Specific Dispatchers

// DepthDispatcher handles depth events
type DepthDispatcher struct {
	Callback func(event *WsDepthEvent)
}

func (d *DepthDispatcher) Dispatch(data []byte) error {
	var event WsDepthEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	if d.Callback != nil {
		d.Callback(&event)
	}
	return nil
}

// TradeDispatcher handles trade events
type TradeDispatcher struct {
	Callback func(event *WsTradeEvent)
}

func (d *TradeDispatcher) Dispatch(data []byte) error {
	var event WsTradeEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	if d.Callback != nil {
		d.Callback(&event)
	}
	return nil
}

// TickerDispatcher handles ticker events
type TickerDispatcher struct {
	Callback func(event *WsTickerEvent)
}

func (d *TickerDispatcher) Dispatch(data []byte) error {
	var event WsTickerEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	if d.Callback != nil {
		d.Callback(&event)
	}
	return nil
}

// KlineDispatcher handles kline events
type KlineDispatcher struct {
	Callback func(event *WsKlineEvent)
}

func (d *KlineDispatcher) Dispatch(data []byte) error {
	var event WsKlineEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	if d.Callback != nil {
		d.Callback(&event)
	}
	return nil
}

// MetadataDispatcher handles metadata events
type MetadataDispatcher struct {
	Callback func(event *WsMetadataEvent)
}

func (d *MetadataDispatcher) Dispatch(data []byte) error {
	var event WsMetadataEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	if d.Callback != nil {
		d.Callback(&event)
	}
	return nil
}
