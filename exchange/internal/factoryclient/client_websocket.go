package factoryclient

import "github.com/QuantProcessing/boltertrader/exchange"

func (client *binanceSpotClient) WebSocket() exchange.SpotWebSocket     { return client.ws }
func (client *binancePerpClient) WebSocket() exchange.PerpWebSocket     { return client.ws }
func (client *okxSpotClient) WebSocket() exchange.SpotWebSocket         { return client.ws }
func (client *okxPerpClient) WebSocket() exchange.PerpWebSocket         { return client.ws }
func (client *lighterSpotClient) WebSocket() exchange.SpotWebSocket     { return client.ws }
func (client *lighterPerpClient) WebSocket() exchange.PerpWebSocket     { return client.ws }
func (client *hyperliquidSpotClient) WebSocket() exchange.SpotWebSocket { return client.ws }
func (client *hyperliquidPerpClient) WebSocket() exchange.PerpWebSocket { return client.ws }

func closeSpotWebSocket(socket exchange.SpotWebSocket) error {
	if socket == nil {
		return nil
	}
	return socket.Close()
}

func closePerpWebSocket(socket exchange.PerpWebSocket) error {
	if socket == nil {
		return nil
	}
	return socket.Close()
}

func (client *binanceSpotClient) Close() error     { return closeSpotWebSocket(client.ws) }
func (client *binancePerpClient) Close() error     { return closePerpWebSocket(client.ws) }
func (client *okxSpotClient) Close() error         { return closeSpotWebSocket(client.ws) }
func (client *okxPerpClient) Close() error         { return closePerpWebSocket(client.ws) }
func (client *lighterSpotClient) Close() error     { return closeSpotWebSocket(client.ws) }
func (client *lighterPerpClient) Close() error     { return closePerpWebSocket(client.ws) }
func (client *hyperliquidSpotClient) Close() error { return closeSpotWebSocket(client.ws) }
func (client *hyperliquidPerpClient) Close() error { return closePerpWebSocket(client.ws) }
