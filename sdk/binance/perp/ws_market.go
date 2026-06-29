package perp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"
)

type binancePerpWSRoute string

const (
	binancePerpWSRoutePublic binancePerpWSRoute = "public"
	binancePerpWSRouteMarket binancePerpWSRoute = "market"
)

type WsMarketClient struct {
	*WsClient
	routes        map[binancePerpWSRoute]*WsClient
	managers      map[binancePerpWSRoute]*binancePerpWSRouteManager
	mu            sync.Mutex
	started       bool
	postReconnect func()
}

func NewWsMarketClient(ctx context.Context) *WsMarketClient {
	return newWsMarketClient(ctx, WSPublicBaseURL, WSMarketBaseURL, WSMarketFallbackBaseURL)
}

func NewCoinMWsMarketClient(ctx context.Context) *WsMarketClient {
	return newWsMarketClient(ctx, CoinMWSPublicBaseURL, CoinMWSMarketBaseURL, CoinMWSMarketFallbackBaseURL)
}

func newWsMarketClient(ctx context.Context, publicBaseURL string, marketBaseURL string, fallbackBaseURL string) *WsMarketClient {
	public := NewWSClient(ctx, publicBaseURL)
	market := NewWSClient(ctx, marketBaseURL)
	client := &WsMarketClient{
		WsClient: public,
		routes: map[binancePerpWSRoute]*WsClient{
			binancePerpWSRoutePublic: public,
			binancePerpWSRouteMarket: market,
		},
	}
	for route, routeClient := range client.routes {
		routeClient.Logger = zap.NewNop().Sugar().Named(fmt.Sprintf("binance-market-%s", route))
		routeClient.Handler = client.handleMessage
	}
	client.managers = map[binancePerpWSRoute]*binancePerpWSRouteManager{
		binancePerpWSRoutePublic: newBinancePerpWSRouteManagerWithFallback(ctx, binancePerpWSRoutePublic, public, client.handleMessage, fallbackBaseURL),
		binancePerpWSRouteMarket: newBinancePerpWSRouteManagerWithFallback(ctx, binancePerpWSRouteMarket, market, client.handleMessage, fallbackBaseURL),
	}
	return client
}

func (c *WsMarketClient) Connect() error {
	c.mu.Lock()
	c.started = true
	c.mu.Unlock()

	var errs []error
	for _, route := range []binancePerpWSRoute{binancePerpWSRoutePublic, binancePerpWSRouteMarket} {
		if err := c.routeManager(route).Connect(); err != nil {
			errs = append(errs, fmt.Errorf("binance %s websocket connect: %w", route, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (c *WsMarketClient) Close() {
	for _, routeClient := range c.routeClients() {
		routeClient.Close()
	}
}

func (c *WsMarketClient) IsConnected() bool {
	for _, routeClient := range c.routeClients() {
		if routeClient.IsConnected() {
			return true
		}
	}
	return false
}

func (c *WsMarketClient) SetPostReconnect(handler func()) {
	c.mu.Lock()
	c.postReconnect = handler
	c.mu.Unlock()
	for _, manager := range c.managers {
		manager.SetPostReconnect(handler)
	}
}

func (c *WsMarketClient) Subscribe(stream string, handler func([]byte) error) error {
	return c.subscribeRoute(binancePerpWSRouteForStream(stream), stream, handler)
}

func (c *WsMarketClient) Unsubscribe(stream string) error {
	return c.unsubscribeRoute(binancePerpWSRouteForStream(stream), stream)
}

func (c *WsMarketClient) subscribeRoute(route binancePerpWSRoute, stream string, handler func([]byte) error) error {
	return c.routeManager(route).Subscribe(stream, handler)
}

func (c *WsMarketClient) unsubscribeRoute(route binancePerpWSRoute, stream string) error {
	return c.routeManager(route).Unsubscribe(stream)
}

func (c *WsMarketClient) routeClient(route binancePerpWSRoute) *WsClient {
	if c.routes != nil {
		if routeClient := c.routes[route]; routeClient != nil {
			return routeClient
		}
	}
	if route == binancePerpWSRoutePublic {
		return c.WsClient
	}
	return nil
}

func (c *WsMarketClient) routeClients() []*WsClient {
	seen := make(map[*WsClient]struct{}, len(c.routes)+1)
	clients := make([]*WsClient, 0, len(c.routes)+1)
	add := func(routeClient *WsClient) {
		if routeClient == nil {
			return
		}
		if _, ok := seen[routeClient]; ok {
			return
		}
		seen[routeClient] = struct{}{}
		clients = append(clients, routeClient)
	}
	add(c.WsClient)
	for _, route := range []binancePerpWSRoute{binancePerpWSRoutePublic, binancePerpWSRouteMarket} {
		add(c.routeClient(route))
		if manager := c.routeManager(route); manager != nil {
			for _, routeClient := range manager.clientsSnapshot() {
				add(routeClient)
			}
		}
	}
	return clients
}

func (c *WsMarketClient) routeManager(route binancePerpWSRoute) *binancePerpWSRouteManager {
	if c.managers != nil {
		if manager := c.managers[route]; manager != nil {
			return manager
		}
	}
	return nil
}

func (c *WsMarketClient) setMaxSubscriptionsPerClientForTest(limit int) {
	for _, manager := range c.managers {
		manager.setMaxSubscriptionsPerClientForTest(limit)
	}
}

func streamClientKey(route binancePerpWSRoute, stream string) string {
	return string(route) + ":" + stream
}

func binancePerpWSStreamPathURLs(route binancePerpWSRoute, stream string, preferFallback bool) []string {
	primary := canonicalRouteBaseURL(route) + "/" + stream
	fallback := binancePerpWSFallbackURL(stream)
	if preferFallback {
		return []string{fallback, primary}
	}
	return []string{primary, fallback}
}

func canonicalRouteBaseURL(route binancePerpWSRoute) string {
	if route == binancePerpWSRoutePublic {
		return WSPublicBaseURL
	}
	return WSMarketBaseURL
}

func binancePerpWSFallbackURL(stream string) string {
	return WSMarketFallbackBaseURL + "/" + stream
}

func binancePerpWSRouteForStream(stream string) binancePerpWSRoute {
	stream = strings.ToLower(stream)
	if strings.Contains(stream, "@depth") || strings.Contains(stream, "@bookticker") || strings.HasPrefix(stream, "!bookticker") {
		return binancePerpWSRoutePublic
	}
	return binancePerpWSRouteMarket
}

func (c *WsMarketClient) CallSubscription(key string, message []byte) bool {
	delivered := false
	for _, routeClient := range c.routeClients() {
		if routeClient.CallSubscription(key, message) {
			delivered = true
		}
	}
	if delivered {
		return true
	}
	for _, manager := range c.managers {
		if manager.CallSubscription(key, message) {
			return true
		}
	}
	return delivered
}

func (c *WsMarketClient) handleMessage(message []byte) {
	c.Logger.Debugw("Received", "msg", string(message))

	// trim space
	message = bytes.TrimSpace(message)
	if len(message) == 0 {
		return
	}

	if stream, data, ok := unwrapBinancePerpCombinedStream(message); ok {
		if stream != "" && c.CallSubscription(stream, data) {
			return
		}
		message = data
	}

	if message[0] == '[' {
		c.handleArrayMessage(message)
	} else {
		c.handleObjectMessage(message)
	}
}

func (c *WsMarketClient) handleArrayMessage(message []byte) {
	var events []struct {
		EventType string `json:"e"`
		EventTime int64  `json:"E"`
	}
	if err := json.Unmarshal(message, &events); err != nil {
		c.Logger.Errorw("error unmarshalling array message", "error", err, "raw_msg", string(message))
		return
	}

	if len(events) == 0 {
		return
	}

	if events[0].EventType == "markPriceUpdate" {
		c.CallSubscription("!markPrice@arr", message)
		c.CallSubscription("!markPrice@arr@1s", message)
		c.CallSubscription("!markPrice@arr@3s", message)
		return
	}

	key := fmt.Sprintf("!%s@arr", events[0].EventType)
	if mappedKey, ok := ArrayEventMap[events[0].EventType]; ok {
		key = mappedKey
	}
	c.CallSubscription(key, message)
}

func (c *WsMarketClient) handleObjectMessage(message []byte) {
	var event struct {
		EventType string `json:"e"`
		EventTime int64  `json:"E"`
		Symbol    string `json:"s"`
		// kline specific
		Kline struct {
			Interval string `json:"i"`
		} `json:"k"`
	}
	if err := json.Unmarshal(message, &event); err != nil {
		c.Logger.Errorw("error unmarshalling object message", "error", err)
		return
	}

	// collect all potential keys
	var keys []string

	if event.EventType == "markPriceUpdate" && event.Symbol != "" {
		symbol := strings.ToLower(event.Symbol)
		keys = append(keys, fmt.Sprintf("%s@markPrice", symbol))
		keys = append(keys, fmt.Sprintf("%s@markPrice@1s", symbol))
		keys = append(keys, fmt.Sprintf("%s@markPrice@3s", symbol))
	} else if eventName, ok := SingleEventMap[event.EventType]; ok {
		stream := fmt.Sprintf("%s@%s", strings.ToLower(event.Symbol), eventName)
		keys = append(keys, stream)
	}

	// special handle !bookTicker
	if event.EventType == "bookTicker" {
		keys = append(keys, "!bookTicker")
	}

	// special handle kline
	if event.EventType == "kline" && event.Symbol != "" && event.Kline.Interval != "" {
		stream := fmt.Sprintf("%s@kline_%s", strings.ToLower(event.Symbol), event.Kline.Interval)
		keys = append(keys, stream)
	}

	// dispatch
	for _, key := range keys {
		c.CallSubscription(key, message)
	}

	if len(keys) == 0 {
		c.Logger.Debugw("No routing keys generated for event", "msg", string(message))
	}
}

func unwrapBinancePerpCombinedStream(message []byte) (string, []byte, bool) {
	var wrapped struct {
		Stream string          `json:"stream"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(message, &wrapped); err != nil {
		return "", nil, false
	}
	if wrapped.Stream == "" || len(wrapped.Data) == 0 {
		return "", nil, false
	}
	return wrapped.Stream, bytes.TrimSpace(wrapped.Data), true
}

// SubscribeMarkPrice latest mark price
// interval default 1s, option 3s
func (c *WsMarketClient) SubscribeMarkPrice(symbol string, interval string, callback func(*WsMarkPriceEvent) error) error {
	if interval == "" {
		interval = "1s"
	}
	channel := fmt.Sprintf("%s@markPrice@%s", symbol, interval)
	return c.subscribeRoute(binancePerpWSRouteMarket, channel, func(data []byte) error {
		var wsData WsMarkPriceEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

// SubscribeAllMarkPrice subscribes to the all-market mark price stream, which
// includes current funding rates for all perpetual symbols in one array update.
// interval defaults to 1s; Binance also supports 3s.
func (c *WsMarketClient) SubscribeAllMarkPrice(interval string, callback func([]*WsMarkPriceEvent) error) error {
	if interval == "" {
		interval = "1s"
	}
	channel := fmt.Sprintf("!markPrice@arr@%s", interval)
	return c.subscribeRoute(binancePerpWSRouteMarket, channel, func(data []byte) error {
		var wsData []*WsMarkPriceEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

// SubscribeIncrementOrderBook interval default 250ms, option 500ms 100ms
// only increment depth
func (c *WsMarketClient) SubscribeIncrementOrderBook(symbol string, interval string, callback func(*WsDepthEvent) error) error {
	if interval == "" {
		interval = "250ms"
	}
	channel := fmt.Sprintf("%s@depth@%s", symbol, interval)
	return c.subscribeRoute(binancePerpWSRoutePublic, channel, func(data []byte) error {
		var wsData WsDepthEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

// SubscribeLimitOrderBook interval default 250ms, option 500ms 100ms
// only limit depth, options: 5  10  20
func (c *WsMarketClient) SubscribeLimitOrderBook(symbol string, levels int, interval string, callback func(*WsDepthEvent) error) error {
	channel := fmt.Sprintf("%s@depth%d@%s", symbol, levels, interval)
	return c.subscribeRoute(binancePerpWSRoutePublic, channel, func(data []byte) error {
		var partial struct {
			LastUpdateID    int64      `json:"lastUpdateId"`
			EventTime       int64      `json:"E"`
			TransactionTime int64      `json:"T"`
			Bids            [][]string `json:"bids"`
			Asks            [][]string `json:"asks"`
		}
		if err := json.Unmarshal(data, &partial); err != nil {
			return err
		}
		return callback(&WsDepthEvent{
			EventTime:       partial.EventTime,
			TransactionTime: partial.TransactionTime,
			Symbol:          strings.ToUpper(symbol),
			FinalUpdateID:   partial.LastUpdateID,
			Bids:            stringLevelsToInterface(partial.Bids),
			Asks:            stringLevelsToInterface(partial.Asks),
		})
	})
}

func stringLevelsToInterface(levels [][]string) [][]interface{} {
	out := make([][]interface{}, 0, len(levels))
	for _, level := range levels {
		if len(level) < 2 {
			continue
		}
		out = append(out, []interface{}{level[0], level[1]})
	}
	return out
}

func (c *WsMarketClient) SubscribeBookTicker(symbol string, callback func(*WsBookTickerEvent) error) error {
	channel := fmt.Sprintf("%s@bookTicker", symbol)
	return c.subscribeRoute(binancePerpWSRoutePublic, channel, func(data []byte) error {
		var wsData WsBookTickerEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

func (c *WsMarketClient) SubscribeAggTrade(symbol string, callback func(*WsAggTradeEvent) error) error {
	channel := fmt.Sprintf("%s@aggTrade", symbol)
	return c.subscribeRoute(binancePerpWSRouteMarket, channel, func(data []byte) error {
		var wsData WsAggTradeEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

func (c *WsMarketClient) SubscribeKline(symbol string, interval string, callback func(*WsKlineEvent) error) error {
	channel := fmt.Sprintf("%s@kline_%s", symbol, interval)
	return c.subscribeRoute(binancePerpWSRouteMarket, channel, func(data []byte) error {
		var wsData WsKlineEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

// Unsubscribe methods

func (c *WsMarketClient) UnsubscribeMarkPrice(symbol string, interval string) error {
	if interval == "" {
		interval = "1s"
	}
	channel := fmt.Sprintf("%s@markPrice@%s", symbol, interval)
	return c.unsubscribeRoute(binancePerpWSRouteMarket, channel)
}

func (c *WsMarketClient) UnsubscribeAllMarkPrice(interval string) error {
	if interval == "" {
		interval = "1s"
	}
	channel := fmt.Sprintf("!markPrice@arr@%s", interval)
	return c.unsubscribeRoute(binancePerpWSRouteMarket, channel)
}

func (c *WsMarketClient) UnsubscribeIncrementOrderBook(symbol string, interval string) error {
	channel := fmt.Sprintf("%s@depth@%s", symbol, interval)
	return c.unsubscribeRoute(binancePerpWSRoutePublic, channel)
}

func (c *WsMarketClient) UnsubscribeLimitOrderBook(symbol string, levels int, interval string) error {
	channel := fmt.Sprintf("%s@depth%d@%s", symbol, levels, interval)
	return c.unsubscribeRoute(binancePerpWSRoutePublic, channel)
}

func (c *WsMarketClient) UnsubscribeBookTicker(symbol string) error {
	channel := fmt.Sprintf("%s@bookTicker", symbol)
	return c.unsubscribeRoute(binancePerpWSRoutePublic, channel)
}

func (c *WsMarketClient) UnsubscribeAggTrade(symbol string) error {
	channel := fmt.Sprintf("%s@aggTrade", symbol)
	return c.unsubscribeRoute(binancePerpWSRouteMarket, channel)
}

func (c *WsMarketClient) UnsubscribeKline(symbol string, interval string) error {
	channel := fmt.Sprintf("%s@kline_%s", symbol, interval)
	return c.unsubscribeRoute(binancePerpWSRouteMarket, channel)
}

func (c *WsMarketClient) SubscribeAllMiniTicker(callback func([]*WsMiniTickerEvent) error) error {
	channel := "!miniTicker@arr"
	return c.subscribeRoute(binancePerpWSRouteMarket, channel, func(data []byte) error {
		var wsData []*WsMiniTickerEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}
