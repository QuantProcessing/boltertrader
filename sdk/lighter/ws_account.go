package lighter

import "fmt"

// Private Channel Subscriptions (require authentication)

// SubscribeAccountAll subscribes to all account data
func (c *WebsocketClient) SubscribeAccountAll(accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("account_all/%d", accountId)
	return c.Subscribe(channel, &authToken, cb)
}

// SubscribeAccountMarket subscribes to account data for a specific market
func (c *WebsocketClient) SubscribeAccountMarket(marketId int, accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("account_market/%d/%d", marketId, accountId)
	return c.Subscribe(channel, &authToken, cb)
}

// SubscribeUserStats subscribes to user statistics
func (c *WebsocketClient) SubscribeUserStats(accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("user_stats/%d", accountId)
	return c.Subscribe(channel, &authToken, cb)
}

// SubscribeAccountTx subscribes to account transactions
func (c *WebsocketClient) SubscribeAccountTx(accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("account_tx/%d", accountId)
	return c.Subscribe(channel, &authToken, cb)
}

// SubscribeAccountAllOrders subscribes to all account orders
func (c *WebsocketClient) SubscribeAccountAllOrders(accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("account_all_orders/%d", accountId)
	return c.Subscribe(channel, &authToken, cb)
}

// SubscribeAccountOrders subscribes to account orders for a specific market
func (c *WebsocketClient) SubscribeAccountOrders(marketId int, accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("account_orders/%d/%d", marketId, accountId)
	return c.Subscribe(channel, &authToken, cb)
}

// SubscribeNotification subscribes to account notifications
func (c *WebsocketClient) SubscribeNotification(accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("notification/%d", accountId)
	return c.Subscribe(channel, &authToken, cb)
}

// SubscribeAccountAllTrades subscribes to all account trades
func (c *WebsocketClient) SubscribeAccountAllTrades(accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("account_all_trades/%d", accountId)
	return c.Subscribe(channel, &authToken, cb)
}

// SubscribeAccountAllPositions subscribes to all account positions
func (c *WebsocketClient) SubscribeAccountAllPositions(accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("account_all_positions/%d", accountId)
	return c.Subscribe(channel, &authToken, cb)
}

// SubscribeAccountAllAssets subscribes to spot asset balances for a specific account.
func (c *WebsocketClient) SubscribeAccountAllAssets(accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("account_all_assets/%d", accountId)
	return c.Subscribe(channel, &authToken, cb)
}

// SubscribeAccountSpotAvgEntryPrices subscribes to spot average entry price updates.
func (c *WebsocketClient) SubscribeAccountSpotAvgEntryPrices(accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("account_spot_avg_entry_prices/%d", accountId)
	return c.Subscribe(channel, &authToken, cb)
}

// SubscribePoolData subscribes to pool activity data for a public pool account.
func (c *WebsocketClient) SubscribePoolData(accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("pool_data/%d", accountId)
	return c.Subscribe(channel, &authToken, cb)
}

// SubscribePoolInfo subscribes to pool metadata updates for a public pool account.
func (c *WebsocketClient) SubscribePoolInfo(accountId int64, authToken string, cb func([]byte)) error {
	channel := fmt.Sprintf("pool_info/%d", accountId)
	return c.Subscribe(channel, &authToken, cb)
}

func (c *WebsocketClient) UnsubscribeAccountAll(accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("account_all/%d", accountId))
}

func (c *WebsocketClient) UnsubscribeAccountMarket(marketId int, accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("account_market/%d/%d", marketId, accountId))
}

func (c *WebsocketClient) UnsubscribeUserStats(accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("user_stats/%d", accountId))
}

func (c *WebsocketClient) UnsubscribeAccountTx(accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("account_tx/%d", accountId))
}

func (c *WebsocketClient) UnsubscribeAccountAllOrders(accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("account_all_orders/%d", accountId))
}

func (c *WebsocketClient) UnsubscribeAccountOrders(marketId int, accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("account_orders/%d/%d", marketId, accountId))
}

func (c *WebsocketClient) UnsubscribeNotification(accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("notification/%d", accountId))
}

func (c *WebsocketClient) UnsubscribeAccountAllTrades(accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("account_all_trades/%d", accountId))
}

func (c *WebsocketClient) UnsubscribeAccountAllPositions(accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("account_all_positions/%d", accountId))
}

func (c *WebsocketClient) UnsubscribeAccountAllAssets(accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("account_all_assets/%d", accountId))
}

func (c *WebsocketClient) UnsubscribeAccountSpotAvgEntryPrices(accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("account_spot_avg_entry_prices/%d", accountId))
}

func (c *WebsocketClient) UnsubscribePoolData(accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("pool_data/%d", accountId))
}

func (c *WebsocketClient) UnsubscribePoolInfo(accountId int64) error {
	return c.Unsubscribe(fmt.Sprintf("pool_info/%d", accountId))
}
