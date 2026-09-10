// Package gatewayv1 defines Gateway wire contracts without service or SDK dependencies.
package gatewayv1

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	MaxActiveTopics         = 64
	MaxInternalActiveTopics = 1024
	MaxControlBytes         = 64 << 10
	OutboundBuffer          = 256
)

type Operation string

const (
	OperationSubscribe   Operation = "subscribe"
	OperationUnsubscribe Operation = "unsubscribe"
	OperationPing        Operation = "ping"
)

type Topic string

const (
	TopicOrderBook         Topic = "market.order_book"
	TopicBBO               Topic = "market.bbo"
	TopicCandles           Topic = "market.candles"
	TopicMarkPrice         Topic = "market.mark_price"
	TopicIndexPrice        Topic = "market.index_price"
	TopicFundingBoard      Topic = "funding.board"
	TopicVenueConnectivity Topic = "system.venue_connectivity"
	TopicAccountEvents     Topic = "account.events"
	TopicTradingOrders     Topic = "trading.orders"
)

type TopicRequest struct {
	SubscriptionID string `json:"subscriptionId,omitempty"`
	Topic          Topic  `json:"topic"`
	InstrumentID   string `json:"instrumentId,omitempty"`
	Depth          int    `json:"depth,omitempty"`
	Interval       string `json:"interval,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
	// InternalPurpose is set only on an authenticated Gateway service hop. The
	// browser handler overwrites it, so clients cannot raise their hotset
	// priority. It does not define a second protocol or a second connection.
	InternalPurpose string `json:"internalPurpose,omitempty"`
}

func (request TopicRequest) Validate() error {
	switch request.InternalPurpose {
	case "", "browser", "position", "open_order", "execution", "funding_history":
	default:
		return fmt.Errorf("topic purpose is invalid")
	}
	switch request.Topic {
	case TopicOrderBook:
		if strings.TrimSpace(request.InstrumentID) == "" || request.Depth <= 0 || request.Depth > 100 || request.Interval != "" {
			return fmt.Errorf("order-book topic is invalid")
		}
	case TopicBBO, TopicMarkPrice, TopicIndexPrice:
		if strings.TrimSpace(request.InstrumentID) == "" || request.Depth != 0 || request.Interval != "" {
			return fmt.Errorf("instrument topic is invalid")
		}
	case TopicCandles:
		if strings.TrimSpace(request.InstrumentID) == "" || strings.TrimSpace(request.Interval) == "" || request.Depth != 0 {
			return fmt.Errorf("candle topic is invalid")
		}
	case TopicFundingBoard, TopicVenueConnectivity, TopicAccountEvents, TopicTradingOrders:
		if request.InstrumentID != "" || request.Depth != 0 || request.Interval != "" {
			return fmt.Errorf("global topic is invalid")
		}
	default:
		return fmt.Errorf("topic is unknown")
	}
	return nil
}

type Control struct {
	Operation Operation      `json:"op"`
	RequestID string         `json:"requestId"`
	Topics    []TopicRequest `json:"topics,omitempty"`
}

func (control Control) Validate() error {
	if strings.TrimSpace(control.RequestID) == "" {
		return fmt.Errorf("requestId is required")
	}
	switch control.Operation {
	case OperationPing:
		if len(control.Topics) != 0 {
			return fmt.Errorf("ping carries no topics")
		}
	case OperationSubscribe:
		if len(control.Topics) == 0 || len(control.Topics) > MaxActiveTopics {
			return fmt.Errorf("subscribe topic count is invalid")
		}
		for _, topic := range control.Topics {
			if err := topic.Validate(); err != nil {
				return err
			}
		}
	case OperationUnsubscribe:
		if len(control.Topics) == 0 || len(control.Topics) > MaxActiveTopics {
			return fmt.Errorf("unsubscribe topic count is invalid")
		}
		for _, topic := range control.Topics {
			if strings.TrimSpace(topic.SubscriptionID) == "" {
				return fmt.Errorf("unsubscribe subscriptionId is required")
			}
		}
	default:
		return fmt.Errorf("operation is unknown")
	}
	return nil
}

type MessageType string

const (
	MessageAck            MessageType = "ack"
	MessageSnapshot       MessageType = "snapshot"
	MessageDelta          MessageType = "delta"
	MessageFull           MessageType = "full"
	MessageResyncRequired MessageType = "resync_required"
	MessageError          MessageType = "error"
	MessagePong           MessageType = "pong"
)

type Message struct {
	Type           MessageType `json:"type"`
	RequestID      string      `json:"requestId,omitempty"`
	SubscriptionID string      `json:"subscriptionId,omitempty"`
	Topic          Topic       `json:"topic,omitempty"`
	Generation     uint64      `json:"generation,omitempty"`
	// Sequence increases within a generation. BBO fanout may skip sequences
	// because each update is a replaceable full quote; book deltas must be contiguous.
	Sequence       uint64          `json:"sequence,omitempty"`
	SourceSequence string          `json:"sourceSequence,omitempty"`
	ObservedAt     time.Time       `json:"observedAt,omitzero"`
	Data           json.RawMessage `json:"data,omitempty"`
	Code           string          `json:"code,omitempty"`
	Reason         string          `json:"reason,omitempty"`
}

type Subscription interface {
	Events() <-chan Message
	Close()
}

func EncodeData(value any) json.RawMessage { payload, _ := json.Marshal(value); return payload }
