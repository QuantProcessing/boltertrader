package grvt

import (
	"encoding/json"
	"fmt"
)

func (c *WebsocketClient) SubscribeMiniTickerSnap(instrument string, interval MiniTickerSnapRate, callback func(WsFeeData[MiniTicker]) error) error {
	selector := fmt.Sprintf("%s@%d", instrument, interval)
	return c.Subscribe(StreamMiniTickerSnap, selector, func(data []byte) error {
		var wsData WsFeeData[MiniTicker]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeMiniTickerDelta(instrument string, interval MiniTickerDeltaRate, callback func(WsFeeData[MiniTicker]) error) error {
	selector := fmt.Sprintf("%s@%d", instrument, interval)
	return c.Subscribe(StreamMiniTickerDelta, selector, func(data []byte) error {
		var wsData WsFeeData[MiniTicker]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeTickerSnap(instrument string, interval TickerSnapRate, callback func(WsFeeData[Ticker]) error) error {
	selector := fmt.Sprintf("%s@%d", instrument, interval)
	return c.Subscribe(StreamTickerSnap, selector, func(data []byte) error {
		var wsData WsFeeData[Ticker]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeTickerDelta(instrument string, interval TickerDeltaRate, callback func(WsFeeData[Ticker]) error) error {
	selector := fmt.Sprintf("%s@%d", instrument, interval)
	return c.Subscribe(StreamTickerDelta, selector, func(data []byte) error {
		var wsData WsFeeData[Ticker]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeOrderbookSnap(instrument string, interval OrderBookSnapRate, depth OrderBookSnapDepth, callback func(WsFeeData[OrderBook]) error) error {
	selector := fmt.Sprintf("%s@%d-%d", instrument, interval, depth)
	return c.Subscribe(StreamOrderbookSnap, selector, func(data []byte) error {
		var wsData WsFeeData[OrderBook]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeOrderbookDelta(instrument string, interval OrderBookDeltaRate, callback func(WsFeeData[OrderBook]) error) error {
	selector := fmt.Sprintf("%s@%d", instrument, interval)
	return c.Subscribe(StreamOrderbookDelta, selector, func(data []byte) error {
		var wsData WsFeeData[OrderBook]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeTrade(instrument string, limit TradeLimit, callback func(WsFeeData[Trade]) error) error {
	selector := fmt.Sprintf("%s@%d", instrument, limit)
	return c.Subscribe(StreamTrade, selector, func(data []byte) error {
		var wsData WsFeeData[Trade]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeKline(instrument string, interval KlineInterval, typ KlineType, callback func(WsFeeData[KLine]) error) error {
	selector := fmt.Sprintf("%s@%s-%s", instrument, interval, typ)
	return c.Subscribe(StreamKline, selector, func(data []byte) error {
		var wsData WsFeeData[KLine]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

// Unsubscribe methods

func (c *WebsocketClient) UnsubscribeMiniTickerSnap(instrument string, interval MiniTickerSnapRate) error {
	selector := fmt.Sprintf("%s@%d", instrument, interval)
	return c.Unsubscribe(StreamMiniTickerSnap, selector)
}

func (c *WebsocketClient) UnsubscribeMiniTickerDelta(instrument string, interval MiniTickerDeltaRate) error {
	selector := fmt.Sprintf("%s@%d", instrument, interval)
	return c.Unsubscribe(StreamMiniTickerDelta, selector)
}

func (c *WebsocketClient) UnsubscribeTickerSnap(instrument string, interval TickerSnapRate) error {
	selector := fmt.Sprintf("%s@%d", instrument, interval)
	return c.Unsubscribe(StreamTickerSnap, selector)
}

func (c *WebsocketClient) UnsubscribeTickerDelta(instrument string, interval TickerDeltaRate) error {
	selector := fmt.Sprintf("%s@%d", instrument, interval)
	return c.Unsubscribe(StreamTickerDelta, selector)
}

func (c *WebsocketClient) UnsubscribeOrderbookSnap(instrument string, interval OrderBookSnapRate, depth OrderBookSnapDepth) error {
	selector := fmt.Sprintf("%s@%d-%d", instrument, interval, depth)
	return c.Unsubscribe(StreamOrderbookSnap, selector)
}

func (c *WebsocketClient) UnsubscribeOrderbookDelta(instrument string, interval OrderBookDeltaRate) error {
	selector := fmt.Sprintf("%s@%d", instrument, interval)
	return c.Unsubscribe(StreamOrderbookDelta, selector)
}

func (c *WebsocketClient) UnsubscribeTrade(instrument string, limit TradeLimit) error {
	selector := fmt.Sprintf("%s@%d", instrument, limit)
	return c.Unsubscribe(StreamTrade, selector)
}

func (c *WebsocketClient) UnsubscribeKline(instrument string, interval KlineInterval, typ KlineType) error {
	selector := fmt.Sprintf("%s@%s-%s", instrument, interval, typ)
	return c.Unsubscribe(StreamKline, selector)
}
