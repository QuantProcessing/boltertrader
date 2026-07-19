package okx

import (
	"encoding/json"
	"fmt"
)

// SubscribeOrders subscribes to orders channel.
func (c *WSClient) SubscribeOrders(instType string, instId *string, handler func(*Order)) error {
	return c.SubscribeOrdersWithError(instType, instId, handler, nil)
}

// SubscribeOrdersWithError subscribes to orders and reports malformed payloads.
func (c *WSClient) SubscribeOrdersWithError(
	instType string,
	instId *string,
	handler func(*Order),
	errorHandler func(error),
) error {
	args := WsSubscribeArgs{
		Channel:  "orders",
		InstType: instType, // SPOT, SWAP, FUTURES, OPTION, ANY
	}
	if instId != nil {
		args.InstId = *instId
	}

	return c.Subscribe(args, marketPushHandler("orders", func(value Order, _ string) {
		handler(&value)
	}, errorHandler))
}

// SubscribeAccount subscribes to the account balance channel.
func (c *WSClient) SubscribeAccount(handler func(*Balance)) error {
	return c.SubscribeAccountWithError(handler, nil)
}

// SubscribeAccountWithError subscribes to account balances and reports malformed payloads.
func (c *WSClient) SubscribeAccountWithError(handler func(*Balance), errorHandler func(error)) error {
	args := WsSubscribeArgs{Channel: "account"}
	return c.Subscribe(args, marketPushHandler("account", func(value Balance, _ string) {
		handler(&value)
	}, errorHandler))
}

// SubscribeAlgoOrders subscribes to the business websocket algo order channel.
func (c *WSClient) SubscribeAlgoOrders(instType string, handler func(*AlgoOrder)) error {
	args := WsSubscribeArgs{
		Channel:  "orders-algo",
		InstType: instType,
	}

	return c.Subscribe(args, func(msg []byte) {
		var push WsPushData[AlgoOrder]
		if err := json.Unmarshal(msg, &push); err != nil {
			fmt.Println("Error unmarshal orders-algo:", err)
			return
		}
		for _, d := range push.Data {
			val := d
			handler(&val)
		}
	})
}

// SubscribeAlgoAdvance subscribes to the business websocket advance algo channel.
func (c *WSClient) SubscribeAlgoAdvance(instType string, handler func(*AlgoOrder)) error {
	args := WsSubscribeArgs{
		Channel:  "algo-advance",
		InstType: instType,
	}

	return c.Subscribe(args, func(msg []byte) {
		var push WsPushData[AlgoOrder]
		if err := json.Unmarshal(msg, &push); err != nil {
			fmt.Println("Error unmarshal algo-advance:", err)
			return
		}
		for _, d := range push.Data {
			val := d
			handler(&val)
		}
	})
}

// SubscribeSpreadOrders subscribes to the business websocket spread order channel.
func (c *WSClient) SubscribeSpreadOrders(handler func(*SpreadOrder)) error {
	args := WsSubscribeArgs{
		Channel: "sprd-orders",
	}

	return c.Subscribe(args, func(msg []byte) {
		var push WsPushData[SpreadOrder]
		if err := json.Unmarshal(msg, &push); err != nil {
			fmt.Println("Error unmarshal sprd-orders:", err)
			return
		}
		for _, d := range push.Data {
			val := d
			handler(&val)
		}
	})
}

// SubscribePositions subscribes to positions channel.
func (c *WSClient) SubscribePositions(instType string, handler func(*Position)) error {
	return c.SubscribePositionsWithError(instType, handler, nil)
}

// SubscribePositionsWithError subscribes to positions and reports malformed payloads.
func (c *WSClient) SubscribePositionsWithError(
	instType string,
	handler func(*Position),
	errorHandler func(error),
) error {
	args := WsSubscribeArgs{
		Channel:  "positions",
		InstType: instType,
	}

	return c.Subscribe(args, marketPushHandler("positions", func(value Position, _ string) {
		handler(&value)
	}, errorHandler))
}
