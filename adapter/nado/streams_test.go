package nado

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func TestNadoPublicStreamsEmitScopedEventsAndReconnect(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 7, 0, 0, 0, time.UTC))
	market := newMarketDataClient(nil, nadoTestProvider(), clk, enums.KindPerp)
	backend := &recordingMarketStreamBackend{}
	snapshots := &recordingMarketSnapshotBackend{snapshots: []*sdk.MarketLiquidity{{
		ProductID: 2,
		Timestamp: "1695081920633150999",
		Bids:      [][2]string{{"2490000000000000000000", "300000000000000000"}},
		Asks:      [][2]string{{"2510000000000000000000", "200000000000000000"}},
	}}}
	market.streamBackend = backend
	market.snapshotBackend = snapshots
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}

	if err := market.SubscribeBook(context.Background(), id); err != nil {
		t.Fatalf("SubscribeBook: %v", err)
	}
	if err := market.SubscribeQuotes(context.Background(), id); err != nil {
		t.Fatalf("SubscribeQuotes: %v", err)
	}
	if err := market.SubscribeTrades(context.Background(), id); err != nil {
		t.Fatalf("SubscribeTrades: %v", err)
	}
	if !market.Capabilities().Streaming.Market {
		t.Fatalf("market stream capability false: %+v", market.Capabilities())
	}
	<-market.Events()

	backend.book(&sdk.OrderBook{
		Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151000", MaxTimestamp: "1695081920633151000", LastMaxTimestamp: "1695081920633150999",
		Bids: [][2]string{{"2500000000000000000000", "100000000000000000"}},
		Asks: [][2]string{{"2510000000000000000000", "200000000000000000"}},
	})
	backend.quote(&sdk.Ticker{Type: "best_bid_offer", ProductId: 2, Timestamp: "1695081920633151001", BidPrice: "2500000000000000000000", BidQty: "100000000000000000", AskPrice: "2510000000000000000000", AskQty: "200000000000000000"})
	backend.trade(&sdk.Trade{Type: "trade", ProductId: 2, Timestamp: "1695081920633151002", Price: "2505000000000000000000", TakerQty: "100000000000000000", MakerQty: "-100000000000000000", IsTakerBuyer: true})

	book := (<-market.Events()).Payload.(contract.BookEvent).Book
	wantTS := time.Unix(0, 1695081920633151000)
	if book.InstrumentID != id || len(book.Bids) != 2 || !book.Bids[0].Price.Equal(decimal.RequireFromString("2500")) || !book.Bids[1].Price.Equal(decimal.RequireFromString("2490")) || !book.Timestamp.Equal(wantTS) {
		t.Fatalf("book event mismatch: %+v", book)
	}
	quote := (<-market.Events()).Payload.(contract.QuoteEvent).Quote
	if quote.InstrumentID != id || !quote.BidPrice.Equal(decimal.RequireFromString("2500")) || !quote.AskSize.Equal(decimal.RequireFromString("0.2")) {
		t.Fatalf("quote event mismatch: %+v", quote)
	}
	trade := (<-market.Events()).Payload.(contract.TradeEvent).Trade
	if trade.InstrumentID != id || trade.AggressorSide != enums.SideBuy || !trade.Quantity.Equal(decimal.RequireFromString("0.1")) {
		t.Fatalf("trade event mismatch: %+v", trade)
	}

	backend.connected = true
	if !market.Connected() {
		t.Fatal("market Connected false")
	}
	if err := market.Reconnect(context.Background()); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if backend.connectCalls != 1 {
		t.Fatalf("connectCalls=%d", backend.connectCalls)
	}
}

func TestNadoBookDepthRebuilderContinuityDeletionDuplicateAndGapRepair(t *testing.T) {
	market := newMarketDataClient(nil, nadoTestProvider(), clock.NewSimulatedClock(time.Date(2026, 7, 10, 7, 0, 0, 0, time.UTC)), enums.KindPerp)
	backend := &recordingMarketStreamBackend{}
	snapshots := &recordingMarketSnapshotBackend{snapshots: []*sdk.MarketLiquidity{
		{ProductID: 2, Timestamp: "1695081920633151000", Bids: [][2]string{{"2500000000000000000000", "100000000000000000"}}, Asks: [][2]string{{"2510000000000000000000", "200000000000000000"}}},
		{ProductID: 2, Timestamp: "1695081920633151005", Bids: [][2]string{{"2520000000000000000000", "300000000000000000"}}, Asks: [][2]string{{"2530000000000000000000", "400000000000000000"}}},
	}}
	market.streamBackend = backend
	market.snapshotBackend = snapshots
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
	if err := market.SubscribeBook(context.Background(), id); err != nil {
		t.Fatalf("SubscribeBook: %v", err)
	}
	<-market.Events()

	backend.book(&sdk.OrderBook{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151001", MaxTimestamp: "1695081920633151001", LastMaxTimestamp: "1695081920633151000", Bids: [][2]string{{"2500000000000000000000", "0"}}, Asks: [][2]string{{"2505000000000000000000", "100000000000000000"}}})
	deleted := (<-market.Events()).Payload.(contract.BookEvent).Book
	if len(deleted.Bids) != 0 || len(deleted.Asks) != 2 || !deleted.Asks[0].Price.Equal(decimal.RequireFromString("2505")) {
		t.Fatalf("delete/apply mismatch: %+v", deleted)
	}

	backend.book(&sdk.OrderBook{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151001", MaxTimestamp: "1695081920633151001", LastMaxTimestamp: "1695081920633151000", Bids: [][2]string{{"2499000000000000000000", "100000000000000000"}}})
	select {
	case ev := <-market.Events():
		t.Fatalf("duplicate/reordered diff emitted event: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}

	backend.book(&sdk.OrderBook{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151004", MaxTimestamp: "1695081920633151004", LastMaxTimestamp: "1695081920633151003", Bids: [][2]string{{"2600000000000000000000", "100000000000000000"}}})
	repaired := (<-market.Events()).Payload.(contract.BookEvent).Book
	if snapshots.calls != 2 || len(repaired.Bids) != 1 || !repaired.Bids[0].Price.Equal(decimal.RequireFromString("2520")) || !repaired.Timestamp.Equal(time.Unix(0, 1695081920633151005)) {
		t.Fatalf("gap repair mismatch calls=%d book=%+v", snapshots.calls, repaired)
	}
	if ch := market.Events(); ch != market.Events() {
		t.Fatal("reconnect/gap repair changed stable event channel")
	}
}

func TestNadoBookDepthBootstrapQueuesDiffsAndGapRecoveryAdvances(t *testing.T) {
	market := newMarketDataClient(nil, nadoTestProvider(), clock.NewSimulatedClock(time.Date(2026, 7, 10, 7, 0, 0, 0, time.UTC)), enums.KindPerp)
	backend := &recordingMarketStreamBackend{}
	snapshots := &recordingMarketSnapshotBackend{snapshots: []*sdk.MarketLiquidity{
		{ProductID: 2, Timestamp: "1695081920633151005", Bids: [][2]string{{"2500000000000000000000", "100000000000000000"}}, Asks: [][2]string{{"2510000000000000000000", "200000000000000000"}}},
		{ProductID: 2, Timestamp: "1695081920633151012", Bids: [][2]string{{"2520000000000000000000", "100000000000000000"}}, Asks: [][2]string{{"2530000000000000000000", "100000000000000000"}}},
	}}
	market.streamBackend = backend
	market.snapshotBackend = snapshots
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
	backend.onSubscribeBook = func() {
		backend.book(&sdk.OrderBook{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151004", MaxTimestamp: "1695081920633151004", LastMaxTimestamp: "official-prev-before-snapshot", Bids: [][2]string{{"2490000000000000000000", "100000000000000000"}}})
		backend.book(&sdk.OrderBook{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151006", MaxTimestamp: "1695081920633151006", LastMaxTimestamp: "1695081920633151004", Bids: [][2]string{{"2505000000000000000000", "100000000000000000"}}})
	}
	if err := market.SubscribeBook(context.Background(), id); err != nil {
		t.Fatalf("SubscribeBook: %v", err)
	}
	bootstrapped := (<-market.Events()).Payload.(contract.BookEvent).Book
	if snapshots.calls != 1 || len(bootstrapped.Bids) != 2 || !bootstrapped.Bids[0].Price.Equal(decimal.RequireFromString("2505")) {
		t.Fatalf("bootstrap queue mismatch calls=%d book=%+v", snapshots.calls, bootstrapped)
	}

	backend.book(&sdk.OrderBook{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151010", MaxTimestamp: "1695081920633151010", LastMaxTimestamp: "gap-before", Bids: [][2]string{{"2600000000000000000000", "100000000000000000"}}})
	repaired := (<-market.Events()).Payload.(contract.BookEvent).Book
	if snapshots.calls != 2 || !repaired.Bids[0].Price.Equal(decimal.RequireFromString("2520")) {
		t.Fatalf("gap snapshot mismatch calls=%d book=%+v", snapshots.calls, repaired)
	}
	backend.book(&sdk.OrderBook{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151013", MaxTimestamp: "1695081920633151013", LastMaxTimestamp: "1695081920633151012", Bids: [][2]string{{"2525000000000000000000", "100000000000000000"}}})
	advanced := (<-market.Events()).Payload.(contract.BookEvent).Book
	if snapshots.calls != 2 || !advanced.Bids[0].Price.Equal(decimal.RequireFromString("2525")) {
		t.Fatalf("post-gap continuity did not advance calls=%d book=%+v", snapshots.calls, advanced)
	}
}

func TestNadoSubscribeBookConnectsBeforeBootstrapSoDiffsQueue(t *testing.T) {
	market := newMarketDataClient(nil, nadoTestProvider(), clock.NewSimulatedClock(time.Date(2026, 7, 10, 7, 0, 0, 0, time.UTC)), enums.KindPerp)
	backend := &connectedOnlyMarketStreamBackend{}
	snapshots := &recordingMarketSnapshotBackend{snapshots: []*sdk.MarketLiquidity{
		{ProductID: 2, Timestamp: "1695081920633151005", Bids: [][2]string{{"2500000000000000000000", "100000000000000000"}}, Asks: [][2]string{{"2510000000000000000000", "100000000000000000"}}},
	}}
	market.streamBackend = backend
	market.snapshotBackend = snapshots
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}

	if err := market.SubscribeBook(context.Background(), id); err != nil {
		t.Fatalf("SubscribeBook: %v", err)
	}
	if backend.connectCalls != 1 {
		t.Fatalf("SubscribeBook did not connect before bootstrap: calls=%d", backend.connectCalls)
	}
	book := (<-market.Events()).Payload.(contract.BookEvent).Book
	if len(book.Bids) != 2 || !book.Bids[0].Price.Equal(decimal.RequireFromString("2505")) {
		t.Fatalf("connected diff did not queue/apply during bootstrap: %+v", book)
	}
}

func TestNadoBookBootstrapOverflowAndStaleGapTakeFreshSnapshot(t *testing.T) {
	market := newMarketDataClient(nil, nadoTestProvider(), clock.NewSimulatedClock(time.Date(2026, 7, 10, 7, 0, 0, 0, time.UTC)), enums.KindPerp)
	snapshots := &recordingMarketSnapshotBackend{snapshots: []*sdk.MarketLiquidity{
		{ProductID: 2, Timestamp: "1695081920633151005", Bids: [][2]string{{"2500000000000000000000", "100000000000000000"}}, Asks: [][2]string{{"2510000000000000000000", "100000000000000000"}}},
		{ProductID: 2, Timestamp: "1695081920633151005", Bids: [][2]string{{"2501000000000000000000", "100000000000000000"}}, Asks: [][2]string{{"2511000000000000000000", "100000000000000000"}}},
		{ProductID: 2, Timestamp: "1695081920633151010", Bids: [][2]string{{"2520000000000000000000", "100000000000000000"}}, Asks: [][2]string{{"2530000000000000000000", "100000000000000000"}}},
	}}
	market.snapshotBackend = snapshots
	rebuilder := market.bookRebuilder(model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}, 2, 100)
	rebuilder.maxQueue = 2
	for _, diff := range []*sdk.OrderBook{
		{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151001", MaxTimestamp: "1695081920633151001", LastMaxTimestamp: "prev", Bids: [][2]string{{"2490000000000000000000", "100000000000000000"}}},
		{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151002", MaxTimestamp: "1695081920633151002", LastMaxTimestamp: "1695081920633151001", Bids: [][2]string{{"2491000000000000000000", "100000000000000000"}}},
		{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151003", MaxTimestamp: "1695081920633151003", LastMaxTimestamp: "1695081920633151002", Bids: [][2]string{{"2492000000000000000000", "100000000000000000"}}},
	} {
		_, _ = rebuilder.Apply(context.Background(), diff)
	}
	overflowed := (<-mustBootstrapBook(t, rebuilder)).Payload.(contract.BookEvent).Book
	if snapshots.calls != 1 || !overflowed.Bids[0].Price.Equal(decimal.RequireFromString("2500")) || len(overflowed.Bids) != 1 {
		t.Fatalf("overflow bootstrap should discard queue and use fresh snapshot calls=%d book=%+v", snapshots.calls, overflowed)
	}

	rebuilder = market.bookRebuilder(model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}, 2, 100)
	rebuilder.mu.Lock()
	rebuilder.ready = false
	rebuilder.queue = nil
	rebuilder.overflow = false
	rebuilder.lastMax = ""
	rebuilder.lastTS = time.Time{}
	rebuilder.mu.Unlock()
	_, _ = rebuilder.Apply(context.Background(), &sdk.OrderBook{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151004", MaxTimestamp: "1695081920633151004", LastMaxTimestamp: "prev", Bids: [][2]string{{"2495000000000000000000", "100000000000000000"}}})
	_, _ = rebuilder.Apply(context.Background(), &sdk.OrderBook{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151006", MaxTimestamp: "1695081920633151006", LastMaxTimestamp: "gap-not-1004", Bids: [][2]string{{"2506000000000000000000", "100000000000000000"}}})
	repaired := (<-mustBootstrapBook(t, rebuilder)).Payload.(contract.BookEvent).Book
	if snapshots.calls != 3 || !repaired.Bids[0].Price.Equal(decimal.RequireFromString("2520")) {
		t.Fatalf("stale-established lastMax gap was accepted instead of repaired calls=%d book=%+v", snapshots.calls, repaired)
	}
}

func mustBootstrapBook(t *testing.T, rebuilder *nadoBookRebuilder) <-chan contract.MarketEnvelope {
	t.Helper()
	ch := make(chan contract.MarketEnvelope, 1)
	event, err := rebuilder.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	ch <- event
	return ch
}

func TestNadoPublicStreamsRejectInvalidOfficialPayloads(t *testing.T) {
	market := newMarketDataClient(nil, nadoTestProvider(), clock.NewSimulatedClock(time.Date(2026, 7, 10, 7, 0, 0, 0, time.UTC)), enums.KindPerp)
	backend := &recordingMarketStreamBackend{}
	market.streamBackend = backend
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
	if err := market.SubscribeQuotes(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := market.SubscribeTrades(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	backend.quote(&sdk.Ticker{Type: "best_bid_offer", ProductId: 2, Timestamp: "1695081920633151000", BidPrice: "-1", BidQty: "100000000000000000", AskPrice: "2510000000000000000000", AskQty: "200000000000000000"})
	backend.trade(&sdk.Trade{Type: "trade", ProductId: 2, Timestamp: "1695081920633151000", Price: "0", TakerQty: "100000000000000000", IsTakerBuyer: true})
	backend.trade(&sdk.Trade{Type: "trade", ProductId: 2, Timestamp: "1695081920633151000", Price: "2500000000000000000000", TakerQty: "-100000000000000000", IsTakerBuyer: true})
	select {
	case ev := <-market.Events():
		t.Fatalf("invalid public payload emitted event: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestNadoPrivateStreamsEmitExecutionAndAccountEvents(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 7, 0, 0, 0, time.UTC))
	provider := nadoTestProvider()
	exec := newExecutionClient(nil, provider, clk, enums.KindPerp, AccountIDUnified)
	acct := newAccountClient(nil, provider, clk, enums.KindPerp, AccountIDUnified)
	backend := &recordingAccountStreamBackend{connected: true}
	exec.accountStream = backend
	acct.streamBackend = backend

	if err := exec.Start(context.Background()); err != nil {
		t.Fatalf("exec Start: %v", err)
	}
	if err := acct.Start(context.Background()); err != nil {
		t.Fatalf("acct Start: %v", err)
	}
	if err := exec.Start(context.Background()); err != nil {
		t.Fatalf("second exec Start: %v", err)
	}
	if err := acct.Start(context.Background()); err != nil {
		t.Fatalf("second acct Start: %v", err)
	}
	if backend.orderSubs != 1 || backend.fillSubs != 1 || backend.positionSubs != 1 {
		t.Fatalf("duplicate private subscriptions order=%d fill=%d position=%d", backend.orderSubs, backend.fillSubs, backend.positionSubs)
	}
	if !exec.Capabilities().Streaming.Execution || !acct.Capabilities().Streaming.Account {
		t.Fatalf("stream capabilities false exec=%+v acct=%+v", exec.Capabilities(), acct.Capabilities())
	}

	backend.order(&sdk.OrderUpdate{Type: "order_update", ProductId: 2, Timestamp: "1695081920633151003", Digest: "digest-order", Amount: "100000000000000000", Reason: sdk.OrderReasonPlaced})
	backend.fill(&sdk.Fill{Type: "fill", ProductId: 2, Timestamp: "1695081920633151004", OrderDigest: "digest-order", FilledQty: "100000000000000000", Price: "2500000000000000000000", IsBid: true, IsTaker: true, Fee: "-200000000000000", SubmissionIdx: "42"})
	backend.position(&sdk.PositionChange{Type: "position_change", ProductId: 2, Timestamp: "1695081920633151005", Amount: "100000000000000000", Reason: sdk.PositionReasonMatchOrders})
	backend.position(&sdk.PositionChange{Type: "position_change", ProductId: 1, Timestamp: "1695081920633151006", Amount: "-2000000000000000000", Reason: sdk.PositionReasonTransferQuote})

	orderEnv := <-exec.Events()
	if orderEnv.EventID == "" || orderEnv.Payload.(contract.OrderEvent).Order.VenueOrderID != "digest-order" {
		t.Fatalf("order event mismatch: %+v", orderEnv)
	}
	fillEnv := <-exec.Events()
	fill := fillEnv.Payload.(contract.FillEvent).Fill
	if fillEnv.EventID == "" || fill.TradeID != "42" || !fill.Price.Equal(decimal.RequireFromString("2500")) || !fill.Fee.Equal(decimal.RequireFromString("-0.0002")) || !fill.Timestamp.Equal(time.Unix(0, 1695081920633151004)) {
		t.Fatalf("fill event mismatch: %+v", fillEnv)
	}
	posEnv := <-acct.Events()
	if posEnv.EventID == "" || !posEnv.Payload.(contract.PositionEvent).Position.Quantity.Equal(decimal.RequireFromString("0.1")) {
		t.Fatalf("position event mismatch: %+v", posEnv)
	}

	backend.position(&sdk.PositionChange{Type: "position_change", ProductId: 1, Timestamp: "1695081920633151006", Amount: "-2000000000000000000", Reason: sdk.PositionReasonTransferQuote})
	select {
	case ev := <-acct.Events():
		t.Fatalf("perp-scoped account emitted spot event: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}

	spotAcct := newAccountClient(nil, provider, clk, enums.KindSpot, AccountIDUnified)
	spotBackend := &recordingAccountStreamBackend{connected: true}
	spotAcct.streamBackend = spotBackend
	if err := spotAcct.Start(context.Background()); err != nil {
		t.Fatalf("spot acct Start: %v", err)
	}
	spotBackend.position(&sdk.PositionChange{Type: "position_change", ProductId: 1, Timestamp: "1695081920633151006", Amount: "-2000000000000000000", Reason: sdk.PositionReasonTransferQuote})
	balEnv := <-spotAcct.Events()
	bal := balEnv.Payload.(contract.BalanceEvent).Balance
	if bal.Currency != "ETH" || !bal.Total.Equal(decimal.RequireFromString("-2")) || !bal.Borrowed.Equal(decimal.RequireFromString("2")) || bal.Free.IsPositive() || bal.Available.IsPositive() {
		t.Fatalf("balance event mismatch: %+v", bal)
	}

	firstID := balEnv.EventID
	spotBackend.position(&sdk.PositionChange{Type: "position_change", ProductId: 1, Timestamp: "1695081920633151006", Amount: "-2000000000000000000", Reason: sdk.PositionReasonTransferQuote})
	if replay := <-spotAcct.Events(); replay.EventID != firstID {
		t.Fatalf("replayed event id changed: first=%s replay=%s", firstID, replay.EventID)
	}

	spotBackend.position(&sdk.PositionChange{Type: "position_change", ProductId: 1, Timestamp: "1695081920633151007", Amount: "1000000000000000000", Reason: sdk.PositionReasonMatchOrders, Isolated: true})
	select {
	case ev := <-spotAcct.Events():
		t.Fatalf("isolated position change emitted first-phase unified account event: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestNadoPrivateStreamRejectsInvalidOfficialPayloads(t *testing.T) {
	exec := newExecutionClient(nil, nadoTestProvider(), clock.NewSimulatedClock(time.Date(2026, 7, 10, 7, 0, 0, 0, time.UTC)), enums.KindPerp, AccountIDUnified)
	backend := &recordingAccountStreamBackend{connected: true}
	exec.accountStream = backend
	if err := exec.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.order(&sdk.OrderUpdate{Type: "order_update", ProductId: 2, Timestamp: "1695081920633151000", Amount: "0", Reason: sdk.OrderReasonFilled})
	backend.order(&sdk.OrderUpdate{Type: "order_update", ProductId: 2, Timestamp: "1695081920633151000", Digest: "digest", Amount: "0", Reason: "unknown"})
	backend.fill(&sdk.Fill{Type: "fill", ProductId: 2, Timestamp: "1695081920633151000", OrderDigest: "", FilledQty: "100000000000000000", Price: "2500000000000000000000", Fee: "0", SubmissionIdx: "42"})
	backend.fill(&sdk.Fill{Type: "fill", ProductId: 2, Timestamp: "1695081920633151000", OrderDigest: "digest", FilledQty: "-100000000000000000", Price: "2500000000000000000000", Fee: "0", SubmissionIdx: "42"})
	select {
	case ev := <-exec.Events():
		t.Fatalf("invalid private payload emitted event: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestNadoOrderUpdateRemainingAmountSemantics(t *testing.T) {
	exec := newExecutionClient(nil, nadoTestProvider(), clock.NewSimulatedClock(time.Date(2026, 7, 10, 7, 0, 0, 0, time.UTC)), enums.KindPerp, AccountIDUnified)
	backend := &recordingAccountStreamBackend{connected: true}
	exec.accountStream = backend
	if err := exec.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.order(&sdk.OrderUpdate{Type: "order_update", ProductId: 2, Timestamp: "1695081920633151000", Digest: "placed", Amount: "80000000000000000000", Reason: sdk.OrderReasonPlaced})
	placed := (<-exec.Events()).Payload.(contract.OrderEvent).Order
	if placed.Status != enums.StatusNew || !placed.Request.Quantity.IsZero() {
		t.Fatalf("placed remaining amount must not be treated as original quantity: %+v", placed)
	}
	backend.order(&sdk.OrderUpdate{Type: "order_update", ProductId: 2, Timestamp: "1695081920633151001", Digest: "partial-90", Amount: "90000000000000000000", Reason: sdk.OrderReasonFilled})
	partial := (<-exec.Events()).Payload.(contract.OrderEvent).Order
	if partial.Status != enums.StatusPartiallyFilled || !partial.Request.Quantity.IsZero() {
		t.Fatalf("partial fill remaining amount was treated as original quantity: %+v", partial)
	}
	backend.order(&sdk.OrderUpdate{Type: "order_update", ProductId: 2, Timestamp: "1695081920633151002", Digest: "partial-80", Amount: "80000000000000000000", Reason: sdk.OrderReasonFilled})
	secondPartial := (<-exec.Events()).Payload.(contract.OrderEvent).Order
	if secondPartial.Status != enums.StatusPartiallyFilled || !secondPartial.Request.Quantity.IsZero() {
		t.Fatalf("second partial fill remaining amount was treated as original quantity: %+v", secondPartial)
	}
	backend.order(&sdk.OrderUpdate{Type: "order_update", ProductId: 2, Timestamp: "1695081920633151003", Digest: "filled", Amount: "0", Reason: sdk.OrderReasonFilled})
	filled := (<-exec.Events()).Payload.(contract.OrderEvent).Order
	if filled.Status != enums.StatusFilled || !filled.Request.Quantity.IsZero() || filled.Request.Side != enums.SideUnknown {
		t.Fatalf("filled order semantics mismatch: %+v", filled)
	}
	backend.order(&sdk.OrderUpdate{Type: "order_update", ProductId: 2, Timestamp: "1695081920633151004", Digest: "cancelled", Amount: "0", Reason: sdk.OrderReasonCancelled})
	cancelled := (<-exec.Events()).Payload.(contract.OrderEvent).Order
	if cancelled.Status != enums.StatusCanceled || cancelled.Request.Side != enums.SideUnknown {
		t.Fatalf("zero remaining cancelled order side must be unknown: %+v", cancelled)
	}
	backend.order(&sdk.OrderUpdate{Type: "order_update", ProductId: 2, Timestamp: "1695081920633151005", Digest: "bad-placed-zero", Amount: "0", Reason: sdk.OrderReasonPlaced})
	select {
	case ev := <-exec.Events():
		t.Fatalf("placed zero remaining order emitted event: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestNadoFillDecodesNumericSubmissionIdxWithoutClientID(t *testing.T) {
	var fill sdk.Fill
	err := json.Unmarshal([]byte(`{"type":"fill","timestamp":"1695081920633151000","product_id":2,"order_digest":"digest","filled_qty":"100000000000000000","remaining_qty":"0","original_qty":"100000000000000000","price":"2500000000000000000000","is_bid":true,"is_taker":false,"fee":"-200000000000000","submission_idx":42,"id":"official-fill-id"}`), &fill)
	if err != nil {
		t.Fatalf("numeric submission_idx decode: %v", err)
	}
	if fill.SubmissionIdx != "42" || fill.Id != "official-fill-id" {
		t.Fatalf("fill official ids mismatch: %+v", fill)
	}
	converted, err := fillFromNado(fill, model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}, AccountIDUnified, "USDT0")
	if err != nil {
		t.Fatalf("fill conversion: %v", err)
	}
	if converted.ClientID != "" || converted.TradeID != "42" || !converted.Fee.Equal(decimal.RequireFromString("-0.0002")) {
		t.Fatalf("fill conversion fabricated client id or lost rebate: %+v", converted)
	}
	var quoted sdk.Fill
	if err := json.Unmarshal([]byte(`{"submission_idx":"43"}`), &quoted); err != nil {
		t.Fatalf("quoted submission_idx decode: %v", err)
	}
	if quoted.SubmissionIdx != "43" {
		t.Fatalf("quoted submission_idx mismatch: %+v", quoted)
	}
}

func TestNadoTimeFromStringRejectsNonPositiveNumericTimestamps(t *testing.T) {
	for _, value := range []string{"0", "-1", "42"} {
		if got := timeFromString(value); !got.IsZero() {
			t.Fatalf("timeFromString(%q)=%s, want zero", value, got)
		}
	}
	if got := timeFromString("1695081920633151000"); !got.Equal(time.Unix(0, 1695081920633151000)) {
		t.Fatalf("nanosecond timestamp parse mismatch: %s", got)
	}
}

type recordingMarketStreamBackend struct {
	connected            bool
	connectCalls         int
	book                 func(*sdk.OrderBook)
	quote                func(*sdk.Ticker)
	trade                func(*sdk.Trade)
	fundingRate          func(*sdk.FundingRate)
	fundingRateProductID *int64
	onSubscribeBook      func()
}

type recordingMarketSnapshotBackend struct {
	calls     int
	snapshots []*sdk.MarketLiquidity
}

type connectedOnlyMarketStreamBackend struct {
	recordingMarketStreamBackend
}

func (b *connectedOnlyMarketStreamBackend) Connect() error {
	if b.connected {
		return nil
	}
	b.connectCalls++
	b.connected = true
	if b.book != nil {
		b.book(&sdk.OrderBook{Type: "book_depth", ProductId: 2, MinTimestamp: "1695081920633151006", MaxTimestamp: "1695081920633151006", LastMaxTimestamp: "1695081920633151004", Bids: [][2]string{{"2505000000000000000000", "100000000000000000"}}})
	}
	return nil
}

func (b *recordingMarketSnapshotBackend) GetMarketLiquidity(ctx context.Context, productID int64, depth int) (*sdk.MarketLiquidity, error) {
	b.calls++
	if len(b.snapshots) == 0 {
		return nil, context.Canceled
	}
	snapshot := b.snapshots[0]
	b.snapshots = b.snapshots[1:]
	return snapshot, nil
}

func (b *recordingMarketStreamBackend) Connect() error {
	if b.connected {
		return nil
	}
	b.connectCalls++
	b.connected = true
	return nil
}
func (b *recordingMarketStreamBackend) Close() { b.connected = false }
func (b *recordingMarketStreamBackend) IsConnected() bool {
	return b.connected
}
func (b *recordingMarketStreamBackend) SubscribeOrderBook(productID int64, cb func(*sdk.OrderBook)) error {
	b.book = cb
	if b.onSubscribeBook != nil {
		b.onSubscribeBook()
	}
	return nil
}
func (b *recordingMarketStreamBackend) SubscribeTicker(productID int64, cb func(*sdk.Ticker)) error {
	b.quote = cb
	return nil
}
func (b *recordingMarketStreamBackend) SubscribeTrades(productID int64, cb func(*sdk.Trade)) error {
	b.trade = cb
	return nil
}
func (b *recordingMarketStreamBackend) SubscribeFundingRate(productID *int64, cb func(*sdk.FundingRate)) error {
	b.fundingRateProductID = productID
	b.fundingRate = cb
	return nil
}

type recordingAccountStreamBackend struct {
	connected    bool
	orderSubs    int
	fillSubs     int
	positionSubs int
	order        func(*sdk.OrderUpdate)
	fill         func(*sdk.Fill)
	position     func(*sdk.PositionChange)
}

func (b *recordingAccountStreamBackend) Connect() error { b.connected = true; return nil }
func (b *recordingAccountStreamBackend) Close()         { b.connected = false }
func (b *recordingAccountStreamBackend) IsConnected() bool {
	return b.connected
}
func (b *recordingAccountStreamBackend) SubscribeOrders(productID *int64, cb func(*sdk.OrderUpdate)) error {
	b.orderSubs++
	b.order = cb
	return nil
}
func (b *recordingAccountStreamBackend) SubscribeFills(productID *int64, cb func(*sdk.Fill)) error {
	b.fillSubs++
	b.fill = cb
	return nil
}
func (b *recordingAccountStreamBackend) SubscribePositions(productID *int64, cb func(*sdk.PositionChange)) error {
	b.positionSubs++
	b.position = cb
	return nil
}
