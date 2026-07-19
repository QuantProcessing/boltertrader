package sdk

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWSPrivateSubscribeRequestUsesGateSignature(t *testing.T) {
	client := MustNewWSClient(ProductSpot).
		WithCredentials("key", "secret").
		WithClock(func() time.Time { return time.Unix(123, 0) })

	req, err := client.subscribeRequest(ChannelSpotOrder, "subscribe", []string{"BTC_USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if req.Auth == nil {
		t.Fatal("expected auth")
	}
	if got, want := req.Auth.Method, "api_key"; got != want {
		t.Fatalf("method=%q want %q", got, want)
	}
	if got, want := req.Auth.Key, "key"; got != want {
		t.Fatalf("key=%q want %q", got, want)
	}
	wantSign := sign("secret", "channel=spot.orders&event=subscribe&time=123")
	if req.Auth.Sign != wantSign {
		t.Fatalf("sign=%q want %q", req.Auth.Sign, wantSign)
	}
}

func TestWSPublicSubscribeDoesNotRequireCredentials(t *testing.T) {
	client := MustNewWSClient(ProductFuturesUSDT).WithClock(func() time.Time { return time.Unix(123, 0) })
	req, err := client.subscribeRequest(ChannelFuturesTrade, "subscribe", []string{"BTC_USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if req.Auth != nil {
		t.Fatalf("unexpected auth: %+v", req.Auth)
	}
	if got, want := req.Channel, ChannelFuturesTrade; got != want {
		t.Fatalf("channel=%q want %q", got, want)
	}
}

func TestWSPrivateSubscribeRequiresCredentials(t *testing.T) {
	client := MustNewWSClient(ProductFuturesUSDT)
	_, err := client.subscribeRequest(ChannelFuturesOrder, "subscribe", []string{"BTC_USDT"})
	if err == nil || !strings.Contains(err.Error(), "credentials required") {
		t.Fatalf("expected credentials error, got %v", err)
	}
}

func TestDecodeWSMessages(t *testing.T) {
	spotPayload := []byte(`{"time":1,"time_ms":1000,"channel":"spot.orders","event":"update","result":[{"id":"1","currency_pair":"BTC_USDT","side":"buy","amount":"0.01","event":"put","finish_as":"open"}]}`)
	spot, err := DecodeSpotOrderMessage(spotPayload)
	if err != nil {
		t.Fatal(err)
	}
	if spot.Channel != ChannelSpotOrder || len(spot.Orders) != 1 || spot.Orders[0].ID != "1" || spot.Orders[0].Event != "put" || spot.Orders[0].FinishAs != "open" {
		t.Fatalf("unexpected spot message: %+v", spot)
	}

	futuresPayload := []byte(`{"time":1,"channel":"futures.positions","event":"update","result":[{"contract":"BTC_USDT","size":2,"entry_price":"100"}]}`)
	futures, err := DecodeFuturesPositionMessage(futuresPayload)
	if err != nil {
		t.Fatal(err)
	}
	if futures.Channel != ChannelFuturesPosition || len(futures.Positions) != 1 || futures.Positions[0].Size != 2 {
		t.Fatalf("unexpected futures message: %+v", futures)
	}
}

func TestDecodeSpotUserTradeMessageAcceptsNumericTradeID(t *testing.T) {
	payload := []byte(`{"time":1,"channel":"spot.usertrades","event":"update","result":[{"id":12345,"order_id":"67890","currency_pair":"ETH_USDT","side":"buy","role":"taker","amount":"0.001","price":"2000","create_time":1700000000,"create_time_ms":1700000000123}]}`)
	msg, err := DecodeSpotUserTradeMessage(payload)
	if err != nil {
		t.Fatalf("DecodeSpotUserTradeMessage: %v", err)
	}
	if len(msg.Trades) != 1 || msg.Trades[0].ID != "12345" || msg.Trades[0].OrderID != "67890" || msg.Trades[0].CurrencyPair != "ETH_USDT" || msg.Trades[0].Amount != "0.001" || msg.Trades[0].CreateTime != "1700000000" || msg.Trades[0].CreateTimeMS != "1700000000123" {
		t.Fatalf("trades=%+v, want numeric trade id and remaining fields preserved", msg.Trades)
	}
}

func TestSpotPublicTradeAcceptsNumericTradeIDAndTimestamps(t *testing.T) {
	var trade Trade
	if err := json.Unmarshal([]byte(`{"id":12345,"currency_pair":"ETH_USDT","side":"buy","amount":"0.001","price":"2000","create_time":1700000000,"create_time_ms":1700000000123}`), &trade); err != nil {
		t.Fatalf("Unmarshal Trade: %v", err)
	}
	if trade.ID != "12345" || trade.CreateTime != "1700000000" || trade.CreateTimeMS != "1700000000123" {
		t.Fatalf("trade=%+v, want flexible public trade identifiers and timestamps", trade)
	}
}

func TestDecodeSpotBalanceMessageAcceptsStringTimestamp(t *testing.T) {
	payload := []byte(`{"time":1,"channel":"spot.balances","event":"update","result":[{"timestamp":"1700000000","timestamp_ms":1700000000123,"user":"42","currency":"ETH","total":"1","available":"1"}]}`)
	msg, err := DecodeSpotBalanceMessage(payload)
	if err != nil {
		t.Fatalf("DecodeSpotBalanceMessage: %v", err)
	}
	if len(msg.Balances) != 1 || msg.Balances[0].Timestamp != 1700000000 || msg.Balances[0].TimestampMS != "1700000000123" || msg.Balances[0].User != 42 {
		t.Fatalf("balances=%+v, want flexible timestamps preserved", msg.Balances)
	}
}

func TestMyFuturesTradeAcceptsStringEncodedIntegers(t *testing.T) {
	var trade MyFuturesTrade
	if err := json.Unmarshal([]byte(`{"id":"99","order_id":"456","size":"2","close_size":"1","contract":"BTC_USDT","price":50000.5,"fee":-0.02}`), &trade); err != nil {
		t.Fatalf("Unmarshal MyFuturesTrade: %v", err)
	}
	if trade.ID != 99 || trade.OrderID != 456 || trade.Size != 2 || trade.CloseSize != 1 || trade.Price != "50000.5" || trade.Fee != "-0.02" {
		t.Fatalf("trade=%+v, want flexible integer fields", trade)
	}
}

func TestDecodeFuturesPrivateMessagesAcceptsTestnetNumberEncodings(t *testing.T) {
	order, err := DecodeFuturesOrderMessage([]byte(`{"channel":"futures.orders","event":"update","result":[{"id":"1","user":"42","contract":"BTC_USDT","size":"1","left":"0","price":50000.5,"fill_price":50001.5,"mkfr":-0.0001,"tkfr":0.0005,"create_time":1628736847,"create_time_ms":1628736847325,"update_time":1541505434123}]}`))
	if err != nil {
		t.Fatalf("DecodeFuturesOrderMessage: %v", err)
	}
	if len(order.Orders) != 1 || order.Orders[0].ID != 1 || order.Orders[0].User != 42 || order.Orders[0].FillPrice != "50001.5" || order.Orders[0].MKFR != "-0.0001" || order.Orders[0].CreateTime != "1628736847" || order.Orders[0].CreateTimeMS != "1628736847325" || order.Orders[0].UpdateTime != "1541505434123" {
		t.Fatalf("orders=%+v, want flexible number encodings", order.Orders)
	}

	balance, err := DecodeFuturesBalanceMessage([]byte(`{"channel":"futures.balances","event":"update","result":[{"time":"1700000000","time_ms":"1700000000123","user":"42","currency":"USDT","change":-0.02,"total":99.98}]}`))
	if err != nil {
		t.Fatalf("DecodeFuturesBalanceMessage: %v", err)
	}
	if len(balance.Balances) != 1 || balance.Balances[0].User != 42 || balance.Balances[0].Change != "-0.02" || balance.Balances[0].Total != "99.98" {
		t.Fatalf("balances=%+v, want flexible number encodings", balance.Balances)
	}

	position, err := DecodeFuturesPositionMessage([]byte(`{"channel":"futures.positions","event":"update","result":[{"user":"42","contract":"BTC_USDT","size":"1","entry_price":50000.5,"mark_price":50001.5,"unrealised_pnl":1,"update_time":"1700000000"}]}`))
	if err != nil {
		t.Fatalf("DecodeFuturesPositionMessage: %v", err)
	}
	if len(position.Positions) != 1 || position.Positions[0].User != 42 || position.Positions[0].Size != 1 || position.Positions[0].EntryPrice != "50000.5" || position.Positions[0].UpdateTime != 1700000000 {
		t.Fatalf("positions=%+v, want flexible number encodings", position.Positions)
	}
}

func TestWSSubscribeRequestOmitsEmptyPayload(t *testing.T) {
	client := MustNewWSClient(ProductSpot).
		WithCredentials("key", "secret").
		WithClock(func() time.Time { return time.Unix(123, 0) })
	req, err := client.subscribeRequest(ChannelSpotBalance, "subscribe", nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"payload"`) {
		t.Fatalf("empty private subscription payload must be omitted: %s", payload)
	}
}

func TestWSKeyKeepsPayloadSpecificSubscriptionsDistinct(t *testing.T) {
	if got, want := wsKey(ChannelSpotOrderBook, []string{"BTC_USDT", "5", "100ms"}), "spot.order_book|BTC_USDT,5,100ms"; got != want {
		t.Fatalf("key=%q want %q", got, want)
	}
	if got, want := wsKey(ChannelSpotOrderBook, nil), ChannelSpotOrderBook; got != want {
		t.Fatalf("key=%q want %q", got, want)
	}
}

func TestWSUnsupportedProduct(t *testing.T) {
	if _, err := NewWSClient("option"); err == nil {
		t.Fatal("expected unsupported product error")
	}
}

func TestWSUnsubscribeWithoutConnectionIsNoop(t *testing.T) {
	client := MustNewWSClient(ProductSpot)
	if err := client.Unsubscribe(context.Background(), ChannelSpotTrade, []string{"BTC_USDT"}); err != nil {
		t.Fatal(err)
	}
}

func TestWSRequestMarshalsGateAuthKeys(t *testing.T) {
	req := wsRequest{
		Time:    123,
		Channel: ChannelSpotOrder,
		Event:   "subscribe",
		Payload: []string{"BTC_USDT"},
		Auth:    &WSAuth{Method: "api_key", Key: "key", Sign: "sig"},
	}
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, want := range []string{`"KEY":"key"`, `"SIGN":"sig"`, `"method":"api_key"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s missing %s", text, want)
		}
	}
}
