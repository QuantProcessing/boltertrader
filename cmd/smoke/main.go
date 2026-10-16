// Command smoke checks a running gateway's public surface on every market:
// ticker, order book and candles over REST, and book/trades/candle(/ticker)
// over WebSocket. It places no orders and needs no exchange credentials.
//
//	go run ./cmd/smoke -url http://127.0.0.1:8081 -token-file data/gateway/token
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type instrument struct {
	ID      string `json:"id"`
	Venue   string `json:"venue"`
	Product string `json:"product"`
	Base    string `json:"base"`
	Quote   string `json:"quote"`
	Settle  string `json:"settle"`
	Symbol  string `json:"symbol"`
}

type result struct {
	market string
	inst   string
	checks map[string]string // name -> "ok 123ms" | error
}

func main() {
	base := flag.String("url", "http://127.0.0.1:8081", "gateway base URL")
	tokenFile := flag.String("token-file", "data/gateway/token", "gateway token file")
	timeout := flag.Duration("timeout", 20*time.Second, "per-check timeout")
	flag.Parse()
	raw, err := os.ReadFile(*tokenFile)
	if err != nil {
		fail(err)
	}
	token := strings.TrimSpace(string(raw))
	client := &http.Client{Timeout: *timeout}
	get := func(path string, out any) (time.Duration, error) {
		request, _ := http.NewRequest(http.MethodGet, *base+path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		started := time.Now()
		response, err := client.Do(request)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != 200 {
			return 0, fmt.Errorf("HTTP %d %s", response.StatusCode, strings.TrimSpace(string(body)))
		}
		if out != nil {
			if err := json.Unmarshal(body, out); err != nil {
				return 0, err
			}
		}
		return time.Since(started), nil
	}

	var instruments []instrument
	if _, err := get("/v1/instruments", &instruments); err != nil {
		fail(err)
	}
	// One instrument per served market: spot is one market per venue; perps
	// are one market per settlement asset.
	picked := map[string]instrument{}
	for _, item := range instruments {
		market := item.Venue + ":" + item.Product
		if item.Product == "perp" {
			market += ":" + item.Settle
		}
		current, seen := picked[market]
		if !seen || rank(item) < rank(current) {
			picked[market] = item
		}
	}
	markets := make([]string, 0, len(picked))
	for market := range picked {
		markets = append(markets, market)
	}
	sort.Strings(markets)

	results := make([]result, len(markets))
	var wg sync.WaitGroup
	for index, market := range markets {
		wg.Add(1)
		go func(index int, market string, item instrument) {
			defer wg.Done()
			r := result{market: market, inst: item.ID, checks: map[string]string{}}
			record := func(name string, elapsed time.Duration, err error) {
				if err != nil {
					r.checks[name] = "FAIL " + truncate(err.Error(), 120)
				} else {
					r.checks[name] = fmt.Sprintf("ok %dms", elapsed.Milliseconds())
				}
			}
			inst := url.QueryEscape(item.ID)
			var stats struct{ Last string }
			elapsed, err := get("/v1/ticker?inst="+inst, &stats)
			if err == nil && stats.Last == "" {
				err = fmt.Errorf("no last price")
			}
			record("rest.ticker", elapsed, err)
			var book struct{ Bids, Asks [][2]string }
			elapsed, err = get("/v1/orderbook?inst="+inst, &book)
			if err == nil && (len(book.Bids) == 0 || len(book.Asks) == 0) {
				err = fmt.Errorf("empty book")
			}
			record("rest.book", elapsed, err)
			var candles []json.RawMessage
			elapsed, err = get("/v1/candles?inst="+inst+"&interval=1h&limit=10", &candles)
			if err == nil && len(candles) == 0 {
				err = fmt.Errorf("no candles")
			}
			record("rest.candles", elapsed, err)

			channels := []map[string]string{{"ch": "book"}, {"ch": "trades"}, {"ch": "candle", "interval": "1m"}}
			if item.Product == "perp" {
				channels = append(channels, map[string]string{"ch": "ticker"})
			}
			for _, channel := range channels {
				channel["op"], channel["inst"] = "sub", item.ID
				// Trades on quiet markets may legitimately take long.
				limit := *timeout
				if channel["ch"] == "trades" || channel["ch"] == "candle" {
					limit = 2 * *timeout
				}
				elapsed, err := firstMessage(*base, token, channel, limit)
				record("ws."+channel["ch"], elapsed, err)
			}
			results[index] = r
		}(index, market, picked[market])
	}
	wg.Wait()

	failures := 0
	for _, r := range results {
		fmt.Printf("%-24s %s\n", r.market, r.inst)
		names := make([]string, 0, len(r.checks))
		for name := range r.checks {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if strings.HasPrefix(r.checks[name], "FAIL") {
				failures++
			}
			fmt.Printf("    %-14s %s\n", name, r.checks[name])
		}
	}
	fmt.Printf("\n%d markets, %d failed checks\n", len(results), failures)
	if failures > 0 {
		os.Exit(1)
	}
}

func firstMessage(base, token string, subscription map[string]string, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	wsURL := strings.Replace(base, "http", "ws", 1) + "/v1/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		return 0, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	payload, _ := json.Marshal(subscription)
	started := time.Now()
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return 0, err
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return 0, fmt.Errorf("no %s message within %s", subscription["ch"], timeout)
		}
		var message struct {
			Channel string          `json:"ch"`
			Data    json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(data, &message)
		switch message.Channel {
		case subscription["ch"]:
			return time.Since(started), nil
		case "error":
			return 0, fmt.Errorf("%s", message.Data)
		}
	}
}

// rank prefers BTC, then ETH, quoted in a stablecoin, without a builder-dex prefix.
func rank(item instrument) int {
	score := 100
	switch strings.ToUpper(item.Base) {
	case "BTC", "UBTC":
		score = 0
	case "ETH", "UETH":
		score = 10
	}
	switch strings.ToUpper(item.Quote) {
	case "USDT":
	case "USDC":
		score += 1 // USDT books are the liquid ones on most CEX spot markets
	default:
		score += 200
	}
	if strings.Contains(item.Symbol, ":") {
		score += 50
	}
	return score
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "smoke:", err)
	os.Exit(2)
}
