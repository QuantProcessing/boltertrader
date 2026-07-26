package factoryclient

import "github.com/QuantProcessing/boltertrader/exchange"

func (client *binanceSpotClient) WebSocket() exchange.SpotWebSocket     { return client.ws }
func (client *binancePerpClient) WebSocket() exchange.PerpWebSocket     { return client.ws }
func (client *hyperliquidSpotClient) WebSocket() exchange.SpotWebSocket { return client.ws }
func (client *hyperliquidPerpClient) WebSocket() exchange.PerpWebSocket { return client.ws }

func (client *binanceSpotClient) Close() error     { return closeSpotWebSocket(client.ws) }
func (client *binancePerpClient) Close() error     { return closePerpWebSocket(client.ws) }
func (client *hyperliquidSpotClient) Close() error { return closeSpotWebSocket(client.ws) }
func (client *hyperliquidPerpClient) Close() error { return closePerpWebSocket(client.ws) }
