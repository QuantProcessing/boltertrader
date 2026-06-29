package okx

import (
	"encoding/json"
	"fmt"
)

// SubscribeTicker subscribes to ticker channel.
func (c *WSClient) SubscribeTicker(instId string, handler func(*Ticker)) error {
	args := WsSubscribeArgs{
		Channel: "tickers",
		InstId:  instId,
	}

	return c.Subscribe(args, func(msg []byte) {
		var push WsPushData[Ticker]
		if err := json.Unmarshal(msg, &push); err != nil {
			fmt.Println("Error unmarshal ticker:", err)
			return
		}
		for _, d := range push.Data {
			val := d
			handler(&val)
		}
	})
}

// SubscribeOrderBook subscribes to books channel.
// Default depth is 400
func (c *WSClient) SubscribeOrderBook(instId string, handler func(*OrderBook, string)) error {
	return c.SubscribeOrderBookDepth(instId, 0, handler)
}

// SubscribeOrderBookDepth subscribes to an OKX order book channel for the requested depth.
func (c *WSClient) SubscribeOrderBookDepth(instId string, depth int, handler func(*OrderBook, string)) error {
	channel, err := OrderBookChannel(depth)
	if err != nil {
		return err
	}
	args := WsSubscribeArgs{
		Channel: channel,
		InstId:  instId,
	}

	return c.Subscribe(args, func(msg []byte) {
		var push WsPushData[OrderBook]
		if err := json.Unmarshal(msg, &push); err != nil {
			fmt.Println("Error unmarshal book:", err)
			return
		}
		for _, d := range push.Data {
			val := d
			handler(&val, push.Action)
		}
	})
}

func OrderBookChannel(depth int) (string, error) {
	switch depth {
	case 0:
		return "books", nil
	case 50:
		return "books50-l2-tbt", nil
	case 400:
		return "books-l2-tbt", nil
	default:
		return "", fmt.Errorf("unsupported OKX order book depth %d", depth)
	}
}

// SubscribeTrades subscribes to public trades channel.
func (c *WSClient) SubscribeTrades(instId string, handler func(*PublicTrade)) error {
	args := WsSubscribeArgs{
		Channel: "trades",
		InstId:  instId,
	}

	return c.Subscribe(args, func(msg []byte) {
		var push WsPushData[PublicTrade]
		if err := json.Unmarshal(msg, &push); err != nil {
			fmt.Println("Error unmarshal trades:", err)
			return
		}
		for _, d := range push.Data {
			val := d
			handler(&val)
		}
	})
}

// SubscribeCandles subscribes to a public candle channel such as candle1m.
func (c *WSClient) SubscribeCandles(instId string, channel string, handler func(Candle)) error {
	args := WsSubscribeArgs{
		Channel: channel,
		InstId:  instId,
	}

	return c.Subscribe(args, func(msg []byte) {
		var push WsPushData[Candle]
		if err := json.Unmarshal(msg, &push); err != nil {
			fmt.Println("Error unmarshal candles:", err)
			return
		}
		for _, d := range push.Data {
			handler(d)
		}
	})
}

// SubscribeFundingRate subscribes to the public funding-rate channel.
func (c *WSClient) SubscribeFundingRate(instId string, handler func(*FundingRate)) error {
	args := WsSubscribeArgs{
		Channel: "funding-rate",
		InstId:  instId,
	}

	return c.Subscribe(args, func(msg []byte) {
		var push WsPushData[FundingRate]
		if err := json.Unmarshal(msg, &push); err != nil {
			fmt.Println("Error unmarshal funding-rate:", err)
			return
		}
		for _, d := range push.Data {
			val := d
			handler(&val)
		}
	})
}

// SubscribeMarkPrice subscribes to the public mark-price channel.
func (c *WSClient) SubscribeMarkPrice(instId string, handler func(*MarkPrice)) error {
	args := WsSubscribeArgs{
		Channel: "mark-price",
		InstId:  instId,
	}

	return c.Subscribe(args, func(msg []byte) {
		var push WsPushData[MarkPrice]
		if err := json.Unmarshal(msg, &push); err != nil {
			fmt.Println("Error unmarshal mark-price:", err)
			return
		}
		for _, d := range push.Data {
			val := d
			handler(&val)
		}
	})
}

// SubscribeIndexTicker subscribes to the public index-tickers channel.
func (c *WSClient) SubscribeIndexTicker(instId string, handler func(*IndexTicker)) error {
	args := WsSubscribeArgs{
		Channel: "index-tickers",
		InstId:  instId,
	}

	return c.Subscribe(args, func(msg []byte) {
		var push WsPushData[IndexTicker]
		if err := json.Unmarshal(msg, &push); err != nil {
			fmt.Println("Error unmarshal index-tickers:", err)
			return
		}
		for _, d := range push.Data {
			val := d
			handler(&val)
		}
	})
}

// SubscribeOptionSummary subscribes to the public opt-summary channel.
func (c *WSClient) SubscribeOptionSummary(instFamily string, handler func(*OptionSummary)) error {
	args := WsSubscribeArgs{
		Channel:    "opt-summary",
		InstFamily: instFamily,
	}

	return c.Subscribe(args, func(msg []byte) {
		var push WsPushData[OptionSummary]
		if err := json.Unmarshal(msg, &push); err != nil {
			fmt.Println("Error unmarshal opt-summary:", err)
			return
		}
		for _, d := range push.Data {
			val := d
			handler(&val)
		}
	})
}
