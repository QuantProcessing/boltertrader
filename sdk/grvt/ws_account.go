package grvt

import (
	"encoding/json"
	"fmt"
)

func (c *WebsocketClient) SubscribeOrderUpdate(instrument string, callback func(WsFeeData[Order]) error) error {
	selector := c.formatSelector(instrument)
	return c.Subscribe(StreamOrderUpdate, selector, func(data []byte) error {
		var wsData WsFeeData[Order]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeOrderState(instrument string, callback func(WsFeeData[WsOrderState]) error) error {
	selector := c.formatSelector(instrument)
	return c.Subscribe(StreamOrderState, selector, func(data []byte) error {
		var wsData WsFeeData[WsOrderState]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeOrderCancel(instrument string, callback func(WsFeeData[WsCancelOrderStatus]) error) error {
	selector := c.formatSelector(instrument)
	return c.Subscribe(StreamOrderCancel, selector, func(data []byte) error {
		var wsData WsFeeData[WsCancelOrderStatus]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeFill(instrument string, callback func(WsFeeData[WsFill]) error) error {
	selector := c.formatSelector(instrument)
	return c.Subscribe(StreamFill, selector, func(data []byte) error {
		var wsData WsFeeData[WsFill]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribePositions(instrument string, callback func(WsFeeData[Position]) error) error {
	selector := c.formatSelector(instrument)
	return c.Subscribe(StreamPositions, selector, func(data []byte) error {
		var wsData WsFeeData[Position]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeDeposit(callback func(WsFeeData[WsDeposit]) error) error {
	accountId := c.client.accountID
	// example '$GRVT_MAIN_ACCOUNT_ID'
	selector := fmt.Sprintf("%s", accountId)
	return c.Subscribe(StreamDeposit, selector, func(data []byte) error {
		var wsData WsFeeData[WsDeposit]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeTransfer(callback func(WsFeeData[WsTransfer]) error) error {
	mainAccountId := c.client.accountID
	subAccountId := c.client.SubAccountID
	// example '$GRVT_MAIN_ACCOUNT_ID-$GRVT_SUB_ACCOUNT_ID'
	selector := fmt.Sprintf("%s-%s", mainAccountId, subAccountId)
	return c.Subscribe(StreamTransfer, selector, func(data []byte) error {
		var wsData WsFeeData[WsTransfer]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) SubscribeWithdrawal(callback func(WsFeeData[WsWithdrawal]) error) error {
	mainAccountId := c.client.accountID
	// example '$GRVT_MAIN_ACCOUNT_ID'
	selector := fmt.Sprintf("%s", mainAccountId)
	return c.Subscribe(StreamWithdrawal, selector, func(data []byte) error {
		var wsData WsFeeData[WsWithdrawal]
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

func (c *WebsocketClient) formatSelector(instrument string) string {
	accountId := c.client.SubAccountID
	// example '$GRVT_SUB_ACCOUNT_ID'-BTC_USDT_Perp
	if instrument == "all" {
		return fmt.Sprintf("%s", accountId)
	}
	return fmt.Sprintf("%s-%s", accountId, instrument)
}
