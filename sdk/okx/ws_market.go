package okx

import (
	"encoding/json"
	"fmt"
)

// SubscribeTicker subscribes to ticker channel.
func (c *WSClient) SubscribeTicker(instId string, handler func(*Ticker)) error {
	return c.SubscribeTickerWithError(instId, handler, nil)
}

// SubscribeTickerWithError subscribes to ticker channel and reports malformed payloads.
func (c *WSClient) SubscribeTickerWithError(instId string, handler func(*Ticker), errorHandler func(error)) error {
	args := WsSubscribeArgs{
		Channel: "tickers",
		InstId:  instId,
	}

	return c.Subscribe(args, marketPushHandler("ticker", func(value Ticker, _ string) {
		handler(&value)
	}, errorHandler))
}

// SubscribeOrderBook subscribes to books channel.
// Default depth is 400
func (c *WSClient) SubscribeOrderBook(instId string, handler func(*OrderBook, string)) error {
	return c.SubscribeOrderBookDepth(instId, 0, handler)
}

// SubscribeOrderBookDepth subscribes to an OKX order book channel for the requested depth.
func (c *WSClient) SubscribeOrderBookDepth(instId string, depth int, handler func(*OrderBook, string)) error {
	return c.SubscribeOrderBookDepthWithError(instId, depth, handler, nil)
}

// SubscribeOrderBookDepthWithError subscribes to an OKX order book channel and reports malformed payloads.
func (c *WSClient) SubscribeOrderBookDepthWithError(
	instId string,
	depth int,
	handler func(*OrderBook, string),
	errorHandler func(error),
) error {
	channel, err := OrderBookChannel(depth)
	if err != nil {
		return err
	}
	args := WsSubscribeArgs{
		Channel: channel,
		InstId:  instId,
	}

	return c.Subscribe(args, marketPushHandler("order book", func(value OrderBook, action string) {
		handler(&value, action)
	}, errorHandler))
}

func OrderBookChannel(depth int) (string, error) {
	switch depth {
	case 0:
		return "books", nil
	case 5:
		return "books5", nil
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
	return c.SubscribeTradesWithError(instId, handler, nil)
}

// SubscribeTradesWithError subscribes to public trades and reports malformed payloads.
func (c *WSClient) SubscribeTradesWithError(instId string, handler func(*PublicTrade), errorHandler func(error)) error {
	args := WsSubscribeArgs{
		Channel: "trades",
		InstId:  instId,
	}

	return c.Subscribe(args, marketPushHandler("trades", func(value PublicTrade, _ string) {
		handler(&value)
	}, errorHandler))
}

// SubscribeCandles subscribes to a public candle channel such as candle1m.
func (c *WSClient) SubscribeCandles(instId string, channel string, handler func(Candle)) error {
	return c.SubscribeCandlesWithError(instId, channel, handler, nil)
}

// SubscribeCandlesWithError subscribes to a public candle channel and reports
// malformed candle payloads through errorHandler.
func (c *WSClient) SubscribeCandlesWithError(instId string, channel string, handler func(Candle), errorHandler func(error)) error {
	args := WsSubscribeArgs{
		Channel: channel,
		InstId:  instId,
	}

	return c.Subscribe(args, candlePushHandler(handler, errorHandler))
}

func candlePushHandler(handler func(Candle), errorHandler func(error)) func([]byte) {
	return func(msg []byte) {
		var push WsPushData[Candle]
		if err := json.Unmarshal(msg, &push); err != nil {
			if errorHandler != nil {
				errorHandler(fmt.Errorf("unmarshal candles: %w", err))
			}
			return
		}
		for _, d := range push.Data {
			handler(d)
		}
	}
}

// SubscribeFundingRate subscribes to the public funding-rate channel.
func (c *WSClient) SubscribeFundingRate(instId string, handler func(*FundingRate)) error {
	return c.SubscribeFundingRateWithError(instId, handler, nil)
}

// SubscribeFundingRateWithError subscribes to the public funding-rate channel and reports malformed payloads.
func (c *WSClient) SubscribeFundingRateWithError(instId string, handler func(*FundingRate), errorHandler func(error)) error {
	args := WsSubscribeArgs{
		Channel: "funding-rate",
		InstId:  instId,
	}

	return c.Subscribe(args, marketPushHandler("funding rate", func(value FundingRate, _ string) {
		handler(&value)
	}, errorHandler))
}

// SubscribeMarkPrice subscribes to the public mark-price channel.
func (c *WSClient) SubscribeMarkPrice(instId string, handler func(*MarkPrice)) error {
	return c.SubscribeMarkPriceWithError(instId, handler, nil)
}

// SubscribeMarkPriceWithError subscribes to the public mark-price channel and reports malformed payloads.
func (c *WSClient) SubscribeMarkPriceWithError(instId string, handler func(*MarkPrice), errorHandler func(error)) error {
	args := WsSubscribeArgs{
		Channel: "mark-price",
		InstId:  instId,
	}

	return c.Subscribe(args, marketPushHandler("mark price", func(value MarkPrice, _ string) {
		handler(&value)
	}, errorHandler))
}

func marketPushHandler[T any](
	label string,
	handler func(T, string),
	errorHandler func(error),
) func([]byte) {
	return func(msg []byte) {
		var push WsPushData[T]
		if err := json.Unmarshal(msg, &push); err != nil {
			if errorHandler != nil {
				errorHandler(fmt.Errorf("unmarshal %s: %w", label, err))
			}
			return
		}
		for _, value := range push.Data {
			handler(value, push.Action)
		}
	}
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
