package exchange_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type fakeAcceptanceSubscription[T any] struct {
	id     string
	events chan T
	status chan exchange.StreamStatusEvent
	errors chan error
	closed bool
}

func newFakeAcceptanceSubscription[T any](id string) *fakeAcceptanceSubscription[T] {
	return &fakeAcceptanceSubscription[T]{
		id:     id,
		events: make(chan T, 8),
		status: make(chan exchange.StreamStatusEvent, 8),
		errors: make(chan error, 8),
	}
}

func (subscription *fakeAcceptanceSubscription[T]) ID() string {
	return subscription.id
}

func (subscription *fakeAcceptanceSubscription[T]) Events() <-chan T {
	return subscription.events
}

func (subscription *fakeAcceptanceSubscription[T]) Status() <-chan exchange.StreamStatusEvent {
	return subscription.status
}

func (subscription *fakeAcceptanceSubscription[T]) Errors() <-chan error {
	return subscription.errors
}

func (subscription *fakeAcceptanceSubscription[T]) Close() error {
	subscription.closed = true
	return nil
}

type fakeSpotAcceptanceCleanupClient struct {
	balances     []exchange.Balance
	target       []exchange.Balance
	book         exchange.OrderBook
	open         map[string]exchange.Order
	calls        []string
	placeRequest exchange.PlaceOrderRequest
	placeState   exchange.OrderAckState
}

func (client *fakeSpotAcceptanceCleanupClient) Balances(context.Context) ([]exchange.Balance, error) {
	client.calls = append(client.calls, "balances")
	return append([]exchange.Balance(nil), client.balances...), nil
}

func (client *fakeSpotAcceptanceCleanupClient) OrderBook(context.Context, exchange.OrderBookRequest) (exchange.OrderBook, error) {
	client.calls = append(client.calls, "order_book")
	return client.book, nil
}

func (client *fakeSpotAcceptanceCleanupClient) PlaceOrder(
	_ context.Context,
	request exchange.PlaceOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	client.calls = append(client.calls, "place")
	client.placeRequest = request
	state := client.placeState
	if state == "" {
		state = exchange.AckImmediatelyFilled
	}
	if state != exchange.AckRejected {
		client.balances = append([]exchange.Balance(nil), client.target...)
	}
	ack := validCleanupAck(exchange.ProductSpot, request, state)
	if state == exchange.AckRejected {
		ack.OrderID = ""
		ack.VenueCode = "rejected"
		ack.VenueMessage = "rejected by fake"
	}
	return ack, nil
}

func (client *fakeSpotAcceptanceCleanupClient) OpenOrders(
	context.Context,
	exchange.OpenOrdersRequest,
) (exchange.OrderPage, error) {
	client.calls = append(client.calls, "open_orders")
	orders := make([]exchange.Order, 0, len(client.open))
	for _, order := range client.open {
		orders = append(orders, order)
	}
	return exchange.OrderPage{Orders: orders}, nil
}

func (client *fakeSpotAcceptanceCleanupClient) CancelOrder(
	_ context.Context,
	request exchange.CancelOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	client.calls = append(client.calls, "cancel")
	delete(client.open, request.OrderID)
	return exchange.OrderAcknowledgement{OrderID: request.OrderID, State: exchange.AckCanceled}, nil
}

type fakePerpAcceptanceCleanupClient struct {
	position     decimal.Decimal
	target       decimal.Decimal
	calls        []string
	placeRequest exchange.PlaceOrderRequest
	placeState   exchange.OrderAckState
}

type fakeAcceptanceOrderTransport struct {
	ack exchange.OrderAcknowledgement
	err error
}

type delayedOrderCleanupClient struct {
	openReadsAfterCancel int
	canceled             bool
	openCalls            int
}

func (client *delayedOrderCleanupClient) CancelOrder(
	context.Context,
	exchange.CancelOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	client.canceled = true
	return exchange.OrderAcknowledgement{OrderID: "101", State: exchange.AckCanceled}, nil
}

func (client *delayedOrderCleanupClient) OpenOrders(
	context.Context,
	exchange.OpenOrdersRequest,
) (exchange.OrderPage, error) {
	client.openCalls++
	if client.canceled {
		if client.openReadsAfterCancel == 0 {
			return exchange.OrderPage{}, nil
		}
		client.openReadsAfterCancel--
	}
	return exchange.OrderPage{Orders: []exchange.Order{{OrderID: "101", Status: "live"}}}, nil
}

func (transport fakeAcceptanceOrderTransport) PlaceOrder(
	context.Context,
	exchange.PlaceOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	return transport.ack, transport.err
}

func (transport fakeAcceptanceOrderTransport) CancelOrder(
	context.Context,
	exchange.CancelOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	return exchange.OrderAcknowledgement{}, nil
}

func (client *fakePerpAcceptanceCleanupClient) Positions(
	_ context.Context,
	request exchange.PositionsRequest,
) ([]exchange.Position, error) {
	client.calls = append(client.calls, "positions")
	if client.position.IsZero() {
		return nil, nil
	}
	side := exchange.SideBuy
	quantity := client.position
	if quantity.IsNegative() {
		side = exchange.SideSell
		quantity = quantity.Abs()
	}
	return []exchange.Position{{Instrument: request.Instrument, Side: side, Quantity: quantity}}, nil
}

func (client *fakePerpAcceptanceCleanupClient) PlaceOrder(
	_ context.Context,
	request exchange.PlaceOrderRequest,
) (exchange.OrderAcknowledgement, error) {
	client.calls = append(client.calls, "place")
	client.placeRequest = request
	state := client.placeState
	if state == "" {
		state = exchange.AckImmediatelyFilled
	}
	if state != exchange.AckRejected {
		client.position = client.target
	}
	ack := validCleanupAck(exchange.ProductPerp, request, state)
	if state == exchange.AckRejected {
		ack.OrderID = ""
		ack.VenueCode = "rejected"
		ack.VenueMessage = "rejected by fake"
	}
	return ack, nil
}

func validCleanupAck(
	product exchange.Product,
	request exchange.PlaceOrderRequest,
	state exchange.OrderAckState,
) exchange.OrderAcknowledgement {
	return exchange.OrderAcknowledgement{
		Venue:          exchange.VenueBinance,
		Product:        product,
		Operation:      exchange.OrderOperationPlace,
		State:          state,
		Instrument:     request.Instrument,
		OrderType:      request.Type,
		OrderID:        "777",
		ClientOrderID:  request.ClientOrderID,
		FilledQuantity: request.Quantity,
	}
}

func TestExternalAcceptanceLedgerBuildsExpectedCellTotals(t *testing.T) {
	manifest := loadExternalAcceptanceManifest(t)
	ledger := buildExternalAcceptanceLedger(t, manifest)

	if got := ledger.PublicAPICellCount(); got != 497 {
		t.Fatalf("public API cell count = %d, want 497", got)
	}
	if got := ledger.ParameterCaseCellCount(); got != 451 {
		t.Fatalf("parameter-case cell count = %d, want 451", got)
	}

	for _, cell := range []externalOperationCell{
		{RowCode: "BNS", Transport: "rest", Method: "PlaceOrder"},
		{RowCode: "BNP", Transport: "websocket", Method: "WatchFundingRate"},
		{RowCode: "HLS", Transport: "rest", Method: "SpotAccount"},
		{RowCode: "HLP", Transport: "websocket", Method: "WatchPositions"},
		{RowCode: "BYS", Transport: "websocket", Method: "PlaceOrder"},
		{RowCode: "BGC", Transport: "rest", Method: "FundingRateHistory"},
		{RowCode: "GTU", Transport: "rest", Method: "SetLeverage"},
		{RowCode: "ATS", Transport: "websocket", Method: "WatchBalances"},
		{RowCode: "NDP", Transport: "websocket", Method: "WatchFundingRate"},
	} {
		if !ledger.ExpectOperation(cell) {
			t.Fatalf("ledger does not expect operation cell %#v", cell)
		}
	}
	if !ledger.ExpectParameterCase(externalParameterCaseCell{RowCode: "OXP", CaseID: "place_order.rest.perp_reduce_only"}) {
		t.Fatal("ledger does not expect perp-only parameter case on OXP")
	}
	if ledger.ExpectParameterCase(externalParameterCaseCell{RowCode: "OXS", CaseID: "place_order.rest.perp_reduce_only"}) {
		t.Fatal("ledger expected perp-only parameter case on OXS spot row")
	}
}

func TestExternalAcceptanceLedgerRejectsMissingDuplicateAndUnknownMarks(t *testing.T) {
	ledger := buildExternalAcceptanceLedger(t, loadExternalAcceptanceManifest(t))

	complete := ledger.CompleteMarks()
	if err := ledger.ValidateMarks(complete); err != nil {
		t.Fatalf("complete marks rejected: %v", err)
	}

	missing := complete[1:]
	if err := ledger.ValidateMarks(missing); err == nil {
		t.Fatal("missing operation mark accepted")
	}

	duplicate := append([]externalCoverageMark(nil), complete...)
	duplicate = append(duplicate, complete[0])
	if err := ledger.ValidateMarks(duplicate); err == nil {
		t.Fatal("duplicate mark accepted")
	}

	unknown := append([]externalCoverageMark(nil), complete...)
	unknown = append(unknown, externalCoverageMark{Kind: "operation", RowCode: "BNS", Transport: "rest", Method: "NotPublic"})
	if err := ledger.ValidateMarks(unknown); err == nil {
		t.Fatal("unknown mark accepted")
	}
}

func TestExternalAcceptanceLedgerValidatesOneProductRow(t *testing.T) {
	ledger := buildExternalAcceptanceLedger(t, loadExternalAcceptanceManifest(t))

	rowMarks, err := ledger.CompleteMarksForRow("BNS")
	if err != nil {
		t.Fatalf("complete row marks: %v", err)
	}
	if got, want := len(rowMarks), 43; got != want {
		t.Fatalf("BNS row mark count = %d, want %d", got, want)
	}
	if err := ledger.ValidateRowMarks("BNS", rowMarks); err != nil {
		t.Fatalf("complete BNS marks rejected: %v", err)
	}
	if err := ledger.ValidateRowMarks("BNS", rowMarks[1:]); err == nil {
		t.Fatal("missing BNS mark accepted")
	}
	if _, err := ledger.CompleteMarksForRow("UNKNOWN"); err == nil {
		t.Fatal("unknown row accepted")
	}
}

func TestExternalRowCoverageMarksOperationsAndParameterCasesIdempotently(t *testing.T) {
	ledger := buildExternalAcceptanceLedger(t, loadExternalAcceptanceManifest(t))
	coverage := newExternalRowCoverage(t, ledger, "BNS")

	coverage.MarkOperation("rest", "PlaceOrder")
	coverage.MarkOperation("rest", "PlaceOrder")
	coverage.MarkParameterCase("place_order.rest.market")
	coverage.MarkParameterCase("place_order.rest.market")

	if got, want := coverage.MarkCount(), 2; got != want {
		t.Fatalf("coverage mark count = %d, want %d", got, want)
	}
	if err := coverage.Validate(); err == nil {
		t.Fatal("partial row coverage validated as complete")
	}

	complete, err := ledger.CompleteMarksForRow("BNS")
	if err != nil {
		t.Fatal(err)
	}
	for _, mark := range complete {
		switch mark.Kind {
		case "operation":
			coverage.MarkOperation(mark.Transport, mark.Method)
		case "parameter_case":
			coverage.MarkParameterCase(mark.CaseID)
		}
	}
	if err := coverage.Validate(); err != nil {
		t.Fatalf("complete row coverage rejected: %v", err)
	}
}

func TestOwnedOrderJournalCleansUpOnlyTrackedPortableNativeOrderIDs(t *testing.T) {
	const nadoDigest = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const bybitUUID = "cf55eb56-0853-4d3f-945e-17ddd6059a89"
	journal := newOwnedOrderJournal()
	journal.TrackPlacement(exchange.OrderAcknowledgement{OrderID: "101", State: exchange.AckResting})
	journal.TrackPlacement(exchange.OrderAcknowledgement{OrderID: nadoDigest, State: exchange.AckResting})
	journal.TrackPlacement(exchange.OrderAcknowledgement{OrderID: bybitUUID, State: exchange.AckResting})
	journal.TrackPlacement(exchange.OrderAcknowledgement{OrderID: "client-202", State: exchange.AckResting})
	journal.TrackPlacement(exchange.OrderAcknowledgement{OrderID: "0", State: exchange.AckResting})
	journal.TrackPlacement(exchange.OrderAcknowledgement{OrderID: "", ClientOrderID: "303", State: exchange.AckResting})
	journal.TrackPlacement(exchange.OrderAcknowledgement{OrderID: "404", State: exchange.AckImmediatelyFilled})

	client := &fakeOrderCleanupClient{
		open: map[string]exchange.Order{
			"101":      {OrderID: "101", Status: "open"},
			nadoDigest: {OrderID: nadoDigest, Status: "open"},
			bybitUUID:  {OrderID: bybitUUID, Status: "open"},
			"999":      {OrderID: "999", Status: "open"},
		},
	}
	if err := journal.Cleanup(context.Background(), client, "BTC-USDT"); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
	if got, want := client.canceledIDs, []string{"101", nadoDigest, bybitUUID}; !sameStrings(got, want) {
		t.Fatalf("canceled order IDs = %v, want %v", got, want)
	}
	if !client.openOrderIDs()["999"] {
		t.Fatal("cleanup touched unrelated open order 999")
	}
	if client.cancelAllCalled {
		t.Fatal("cleanup used cancel-all or symbol-wide recovery")
	}
}

func TestOwnedOrderJournalFailsFinalVerificationWhenOwnedOrderRemainsOpen(t *testing.T) {
	journal := newOwnedOrderJournal()
	journal.TrackPlacement(exchange.OrderAcknowledgement{OrderID: "101", State: exchange.AckResting})

	client := &fakeOrderCleanupClient{
		failCancel: true,
		open: map[string]exchange.Order{
			"101": {OrderID: "101", Status: "open"},
			"999": {OrderID: "999", Status: "open"},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := journal.Cleanup(ctx, client, "BTC-USDT"); err == nil {
		t.Fatal("cleanup accepted owned remaining open order")
	}
	if !client.openOrderIDs()["999"] {
		t.Fatal("cleanup touched unrelated open order 999")
	}
}

func TestOwnedOrderJournalWaitsForEventuallyConsistentCancelVisibility(t *testing.T) {
	journal := newOwnedOrderJournal()
	journal.TrackOrderID("101")
	client := &delayedOrderCleanupClient{openReadsAfterCancel: 2}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := journal.Cleanup(ctx, client, "BTC-USDT"); err != nil {
		t.Fatalf("cleanup rejected an exact-owned order that disappeared after cancel: %v", err)
	}
	if client.openCalls < 4 {
		t.Fatalf("OpenOrders calls = %d; cleanup trusted cancel acknowledgement before disappearance", client.openCalls)
	}
}

func TestOwnedOrderJournalTreatsEveryOwnedOpenOrdersRowAsResidual(t *testing.T) {
	journal := newOwnedOrderJournal()
	journal.TrackOrderID("101")

	client := &fakeOrderCleanupClient{
		failCancel: true,
		open: map[string]exchange.Order{
			"101": {OrderID: "101", Status: "live"},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := journal.Cleanup(ctx, client, "BTC-USDT"); err == nil {
		t.Fatal("owned OpenOrders row with venue-specific live status was treated as closed")
	}
}

func TestOwnedOrderJournalResolvesTrackedClientOrderIDWithoutTouchingUnrelatedOrders(t *testing.T) {
	journal := newOwnedOrderJournal()
	journal.TrackClientOrderID("202")

	client := &fakeOrderCleanupClient{
		open: map[string]exchange.Order{
			"101": {OrderID: "101", ClientOrderID: "202", Status: "open"},
			"999": {OrderID: "999", ClientOrderID: "998", Status: "open"},
		},
	}
	if err := journal.Cleanup(context.Background(), client, "BTC-USDT"); err != nil {
		t.Fatalf("cleanup by exact client order id: %v", err)
	}
	if got, want := client.canceledIDs, []string{"101"}; !sameStrings(got, want) {
		t.Fatalf("canceled order IDs = %v, want %v", got, want)
	}
	if !client.openOrderIDs()["999"] {
		t.Fatal("cleanup touched unrelated order")
	}
}

func TestOrderPageContainsOrderIDRegardlessOfVenueStatus(t *testing.T) {
	page := exchange.OrderPage{Orders: []exchange.Order{
		{OrderID: "101", Status: "live"},
		{OrderID: "202", Status: "venue_terminal_but_still_open"},
	}}

	if !orderPageContainsOrderID(page, "101") {
		t.Fatal("OpenOrders row with venue-specific live status was treated as closed")
	}
	if !orderPageContainsOrderID(page, "202") {
		t.Fatal("OpenOrders row status overrode the authoritative presence of the order")
	}
	if orderPageContainsOrderID(page, "303") {
		t.Fatal("missing order ID was reported as open")
	}
}

func TestPerpRoundTripDirectionMovesAwayFromAnyBaseline(t *testing.T) {
	tests := []struct {
		name      string
		baseline  decimal.Decimal
		primary   exchange.Side
		offset    exchange.Side
		direction decimal.Decimal
	}{
		{
			name:      "flat or long baseline",
			baseline:  decimal.RequireFromString("2"),
			primary:   exchange.SideBuy,
			offset:    exchange.SideSell,
			direction: decimal.NewFromInt(1),
		},
		{
			name:      "short baseline",
			baseline:  decimal.RequireFromString("-2"),
			primary:   exchange.SideSell,
			offset:    exchange.SideBuy,
			direction: decimal.NewFromInt(-1),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary, offset, direction := perpRoundTripDirection(test.baseline)
			if primary != test.primary || offset != test.offset || !direction.Equal(test.direction) {
				t.Fatalf(
					"perpRoundTripDirection(%s) = (%s, %s, %s), want (%s, %s, %s)",
					test.baseline,
					primary,
					offset,
					direction,
					test.primary,
					test.offset,
					test.direction,
				)
			}
		})
	}
}

func TestPositionMovedInExpectedSignedDirection(t *testing.T) {
	step := decimal.RequireFromString("0.01")
	baseline := decimal.RequireFromString("-2")

	if !positionMovedInDirection(decimal.RequireFromString("-2.02"), baseline, decimal.NewFromInt(-1), step) {
		t.Fatal("larger short exposure was not recognized as the expected movement")
	}
	if positionMovedInDirection(decimal.RequireFromString("-1.98"), baseline, decimal.NewFromInt(-1), step) {
		t.Fatal("movement toward zero was accepted as larger short exposure")
	}
	if !positionMovedInDirection(decimal.RequireFromString("-1.98"), baseline, decimal.NewFromInt(1), step) {
		t.Fatal("positive signed movement was not recognized")
	}
}

func TestAcceptanceSubscriptionWitnessRequiresPostArmEvent(t *testing.T) {
	subscription := newFakeAcceptanceSubscription[int]("orders")
	subscription.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	subscription.events <- 1

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	witness, err := startAcceptanceSubscriptionWitness(ctx, "orders", func(context.Context) (exchange.Subscription[int], error) {
		return subscription, nil
	})
	if err != nil {
		t.Fatalf("start witness: %v", err)
	}
	witness.Arm()

	eventCtx, eventCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer eventCancel()
	if err := witness.WaitEvent(eventCtx); err == nil {
		t.Fatal("pre-arm snapshot was accepted as post-arm event evidence")
	}

	subscription.events <- 2
	if err := witness.WaitEvent(ctx); err != nil {
		t.Fatalf("post-arm event was not observed: %v", err)
	}
	if err := witness.Close(); err != nil {
		t.Fatalf("close witness: %v", err)
	}
	if err := witness.Close(); err != nil {
		t.Fatalf("idempotent close witness: %v", err)
	}
}

func TestAcceptanceSubscriptionWitnessRetainsPublicEventObservedBeforeActive(t *testing.T) {
	subscription := newFakeAcceptanceSubscription[int]("book")
	subscription.events <- 1
	subscription.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	witness, err := startAcceptanceSubscriptionWitness(ctx, "book", func(context.Context) (exchange.Subscription[int], error) {
		return subscription, nil
	})
	if err != nil {
		t.Fatalf("start witness: %v", err)
	}
	if err := witness.WaitEvent(ctx); err != nil {
		t.Fatalf("valid pre-Active public snapshot was lost: %v", err)
	}
}

func TestSpotExposureCleanupTradesExactOwnedDeltaAndVerifiesBaseline(t *testing.T) {
	baseline := []exchange.Balance{{Asset: "ETH", Total: decimal.NewFromInt(1)}}
	client := &fakeSpotAcceptanceCleanupClient{
		balances: []exchange.Balance{{Asset: "ETH", Total: decimal.RequireFromString("1.02")}},
		target:   baseline,
		book: exchange.OrderBook{
			Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(2500)}},
			Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(2501)}},
		},
	}
	instrument := exchange.Instrument{
		Symbol:            "ETH-USDT",
		BaseAsset:         "ETH",
		QuantityIncrement: decimal.RequireFromString("0.001"),
	}
	journal := newOwnedOrderJournal()

	if err := cleanupSpotAcceptanceExposure(
		context.Background(),
		exchangeAcceptanceRow{code: "BNS", product: exchange.ProductSpot},
		client,
		instrument,
		baseline,
		journal,
	); err != nil {
		t.Fatalf("spot cleanup: %v", err)
	}
	if client.placeRequest.Side != exchange.SideSell ||
		!client.placeRequest.Quantity.Equal(decimal.RequireFromString("0.02")) {
		t.Fatalf("spot cleanup request = %+v", client.placeRequest)
	}
	if _, tracked := journal.clientOrderIDs[client.placeRequest.ClientOrderID]; !tracked {
		t.Fatal("spot cleanup order was not exact-owned before/after submission")
	}
}

func TestSpotExposureCleanupRejectsRejectedAcknowledgement(t *testing.T) {
	baseline := []exchange.Balance{{Asset: "ETH", Total: decimal.NewFromInt(1)}}
	client := &fakeSpotAcceptanceCleanupClient{
		balances:   []exchange.Balance{{Asset: "ETH", Total: decimal.RequireFromString("1.02")}},
		target:     baseline,
		placeState: exchange.AckRejected,
		book: exchange.OrderBook{
			Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(2500)}},
			Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(2501)}},
		},
	}
	instrument := exchange.Instrument{
		Symbol:            "ETH-USDT",
		BaseAsset:         "ETH",
		QuantityIncrement: decimal.RequireFromString("0.001"),
	}
	if err := cleanupSpotAcceptanceExposure(
		context.Background(),
		exchangeAcceptanceRow{code: "BNS", product: exchange.ProductSpot},
		client,
		instrument,
		baseline,
		newOwnedOrderJournal(),
	); err == nil {
		t.Fatal("spot cleanup accepted rejected acknowledgement")
	}
}

func TestSpotExposureCleanupAcceptsUntradableGateCommissionDust(t *testing.T) {
	baseline := []exchange.Balance{{Asset: "ETH", Total: decimal.NewFromInt(1)}}
	client := &fakeSpotAcceptanceCleanupClient{
		balances: []exchange.Balance{{Asset: "ETH", Total: decimal.RequireFromString("1.0002934")}},
		book: exchange.OrderBook{
			Bids: []exchange.BookLevel{{Price: decimal.RequireFromString("1272.70")}},
			Asks: []exchange.BookLevel{{Price: decimal.RequireFromString("1272.80")}},
		},
	}
	instrument := exchange.Instrument{
		Symbol:            "ETH-USDT",
		BaseAsset:         "ETH",
		QuantityIncrement: decimal.RequireFromString("0.0001"),
		MinNotional: exchange.OptionalDecimal{
			Value: decimal.NewFromInt(3),
			Valid: true,
		},
	}

	if err := cleanupSpotAcceptanceExposure(
		context.Background(),
		exchangeAcceptanceRow{code: "GTS", product: exchange.ProductSpot},
		client,
		instrument,
		baseline,
		newOwnedOrderJournal(),
	); err != nil {
		t.Fatalf("commission dust cleanup: %v", err)
	}
	if client.placeRequest.Instrument != "" {
		t.Fatalf("untradable commission dust produced cleanup order %+v", client.placeRequest)
	}
}

func TestSpotExposureCleanupSkipsAnyResidualBelowVenueMinimum(t *testing.T) {
	baseline := []exchange.Balance{{Asset: "ETH", Total: decimal.NewFromInt(1)}}
	client := &fakeSpotAcceptanceCleanupClient{
		balances: []exchange.Balance{{Asset: "ETH", Total: decimal.RequireFromString("1.001")}},
		book: exchange.OrderBook{
			Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(2500)}},
			Asks: []exchange.BookLevel{{Price: decimal.RequireFromString("2500.10")}},
		},
	}
	instrument := exchange.Instrument{
		Symbol:            "ETH-USDT",
		BaseAsset:         "ETH",
		QuantityIncrement: decimal.RequireFromString("0.0001"),
		MinNotional: exchange.OptionalDecimal{
			Value: decimal.NewFromInt(5),
			Valid: true,
		},
	}

	if err := cleanupSpotAcceptanceExposure(
		context.Background(),
		exchangeAcceptanceRow{code: "GTS", product: exchange.ProductSpot},
		client,
		instrument,
		baseline,
		newOwnedOrderJournal(),
	); err != nil {
		t.Fatalf("untradable residual cleanup: %v", err)
	}
	if client.placeRequest.Instrument != "" {
		t.Fatalf("untradable residual produced cleanup order %+v", client.placeRequest)
	}
}

func TestPerpExposureCleanupRestoresShortBaselineWithCorrectReduceOnlyPolicy(t *testing.T) {
	instrument := exchange.Instrument{
		Symbol:            "ETH-USDT",
		QuantityIncrement: decimal.RequireFromString("0.001"),
	}
	tests := []struct {
		name       string
		current    decimal.Decimal
		baseline   decimal.Decimal
		side       exchange.Side
		reduceOnly bool
	}{
		{
			name:       "remove excess short",
			current:    decimal.RequireFromString("-2.1"),
			baseline:   decimal.RequireFromString("-2"),
			side:       exchange.SideBuy,
			reduceOnly: true,
		},
		{
			name:       "restore missing short",
			current:    decimal.RequireFromString("-1.9"),
			baseline:   decimal.RequireFromString("-2"),
			side:       exchange.SideSell,
			reduceOnly: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakePerpAcceptanceCleanupClient{position: test.current, target: test.baseline}
			journal := newOwnedOrderJournal()
			if err := cleanupPerpAcceptanceExposure(
				context.Background(),
				exchangeAcceptanceRow{code: "BNP", product: exchange.ProductPerp},
				client,
				instrument,
				test.baseline,
				journal,
			); err != nil {
				t.Fatalf("perp cleanup: %v", err)
			}
			if client.placeRequest.Side != test.side || client.placeRequest.ReduceOnly != test.reduceOnly {
				t.Fatalf("perp cleanup request = %+v", client.placeRequest)
			}
			if _, tracked := journal.clientOrderIDs[client.placeRequest.ClientOrderID]; !tracked {
				t.Fatal("perp cleanup order was not exact-owned")
			}
		})
	}
}

func TestSpotFinalizerCancelsOwnedOrdersBeforeExposureRepair(t *testing.T) {
	baseline := []exchange.Balance{{Asset: "ETH", Total: decimal.NewFromInt(1)}}
	client := &fakeSpotAcceptanceCleanupClient{
		balances: []exchange.Balance{{Asset: "ETH", Total: decimal.RequireFromString("1.02")}},
		target:   baseline,
		book: exchange.OrderBook{
			Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(2500)}},
			Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(2501)}},
		},
		open: map[string]exchange.Order{
			"101": {OrderID: "101", ClientOrderID: "202", Status: "live"},
		},
	}
	journal := newOwnedOrderJournal()
	journal.TrackClientOrderID("202")
	instrument := exchange.Instrument{
		Symbol:            "ETH-USDT",
		BaseAsset:         "ETH",
		QuantityIncrement: decimal.RequireFromString("0.001"),
	}

	if err := finalizeSpotAcceptance(
		exchangeAcceptanceRow{code: "BNS", product: exchange.ProductSpot},
		client,
		instrument,
		baseline,
		journal,
	); err != nil {
		t.Fatalf("spot finalizer: %v", err)
	}
	cancelIndex, placeIndex := -1, -1
	for index, call := range client.calls {
		if call == "cancel" && cancelIndex == -1 {
			cancelIndex = index
		}
		if call == "place" && placeIndex == -1 {
			placeIndex = index
		}
	}
	if cancelIndex == -1 || placeIndex == -1 || cancelIndex > placeIndex {
		t.Fatalf("cleanup call order = %v; owned orders must be canceled before exposure repair", client.calls)
	}
}

func TestTrackedAcceptancePlacementOwnsRequestBeforeTransportOutcome(t *testing.T) {
	request := exchange.PlaceOrderRequest{
		Instrument:    "ETH-USDT",
		ClientOrderID: "202",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeMarket,
		Quantity:      decimal.RequireFromString("0.02"),
	}
	journal := newOwnedOrderJournal()
	transport := fakeAcceptanceOrderTransport{err: context.DeadlineExceeded}

	if _, err := placeTrackedAcceptanceOrder(context.Background(), transport, request, journal); err == nil {
		t.Fatal("transport failure was not returned")
	}
	if _, tracked := journal.clientOrderIDs[request.ClientOrderID]; !tracked {
		t.Fatal("request client order ID was not owned before the ambiguous transport outcome")
	}

	transport = fakeAcceptanceOrderTransport{ack: validCleanupAck(exchange.ProductSpot, request, exchange.AckResting)}
	transport.ack.OrderType = exchange.OrderTypeLimit
	transport.ack.OrderID = "303"
	if _, err := placeTrackedAcceptanceOrder(context.Background(), transport, request, journal); err != nil {
		t.Fatalf("tracked placement: %v", err)
	}
	if _, tracked := journal.orderIDs["303"]; !tracked {
		t.Fatal("native order ID from acknowledgement was not journaled")
	}
}

func TestAcceptanceClientOrderIDsFitEveryVenuePortableRange(t *testing.T) {
	const maxUint48 = int64(1<<48 - 1)
	seen := make(map[string]struct{}, 256)
	for range 256 {
		clientOrderID := nextAcceptanceClientOrderID()
		parsed, err := strconv.ParseInt(clientOrderID, 10, 64)
		if err != nil {
			t.Fatalf("client order ID %q is not a portable positive decimal: %v", clientOrderID, err)
		}
		if parsed <= 0 || parsed > maxUint48 {
			t.Fatalf("client order ID %d is outside common venue range [1,%d]", parsed, maxUint48)
		}
		if _, duplicate := seen[clientOrderID]; duplicate {
			t.Fatalf("duplicate client order ID %q", clientOrderID)
		}
		seen[clientOrderID] = struct{}{}
	}
}

func TestFillOffsetAuthorizationRequiresConfirmedPositiveFilledQuantity(t *testing.T) {
	positive := exchange.OrderAcknowledgement{
		State:          exchange.AckImmediatelyFilled,
		OrderID:        "11",
		FilledQuantity: decimal.NewFromInt(2),
	}
	if err := authorizeFillOffset(positive); err != nil {
		t.Fatalf("positive fill rejected: %v", err)
	}

	for name, ack := range map[string]exchange.OrderAcknowledgement{
		"zero fill":      {State: exchange.AckImmediatelyFilled, OrderID: "11", FilledQuantity: decimal.Zero},
		"ambiguous ack":  {State: exchange.AckAmbiguous, OrderID: "11", FilledQuantity: decimal.NewFromInt(2)},
		"accepted ack":   {State: exchange.AckAcceptedPending, OrderID: "11", FilledQuantity: decimal.NewFromInt(2)},
		"partial no qty": {State: exchange.AckPartiallyFilled, OrderID: "11", FilledQuantity: decimal.Zero},
	} {
		t.Run(name, func(t *testing.T) {
			if err := authorizeFillOffset(ack); err == nil {
				t.Fatal("ambiguous or zero fill evidence was accepted")
			}
		})
	}
}

func TestApproxFiftyQuoteSizingUsesDecimalMarketConstraints(t *testing.T) {
	instrument := exchange.Instrument{
		Symbol:            "BTC-USDT",
		PriceIncrement:    decimal.RequireFromString("0.10"),
		QuantityIncrement: decimal.RequireFromString("0.0001"),
		MinQuantity:       decimal.RequireFromString("0.0002"),
		MinNotional: exchange.OptionalDecimal{
			Value: decimal.RequireFromString("10"),
			Valid: true,
		},
	}
	book := exchange.OrderBook{
		Asks: []exchange.BookLevel{{Price: decimal.RequireFromString("20000.03"), Quantity: decimal.RequireFromString("1")}},
		Bids: []exchange.BookLevel{{Price: decimal.RequireFromString("19999.97"), Quantity: decimal.RequireFromString("1")}},
	}

	size, err := sizeApproxFiftyQuoteOrder(instrument, book, exchange.SideBuy)
	if err != nil {
		t.Fatalf("sizing failed: %v", err)
	}
	if !size.Price.Equal(decimal.RequireFromString("20000.10")) {
		t.Fatalf("price = %s, want 20000.10", size.Price)
	}
	if !size.Quantity.Equal(decimal.RequireFromString("0.0024")) {
		t.Fatalf("quantity = %s, want 0.0024", size.Quantity)
	}
	if notional := size.Price.Mul(size.Quantity); notional.LessThan(instrument.MinNotional.Value) {
		t.Fatalf("notional = %s below min notional %s", notional, instrument.MinNotional.Value)
	}
}

func TestApproxFiftyQuoteSizingRejectsTooSmallRoundedNotional(t *testing.T) {
	instrument := exchange.Instrument{
		Symbol:            "BTC-USDT",
		PriceIncrement:    decimal.RequireFromString("0.10"),
		QuantityIncrement: decimal.RequireFromString("0.001"),
		MinQuantity:       decimal.RequireFromString("0.001"),
	}
	book := exchange.OrderBook{
		Asks: []exchange.BookLevel{{Price: decimal.RequireFromString("20000"), Quantity: decimal.RequireFromString("1")}},
	}

	if _, err := sizeApproxFiftyQuoteOrder(instrument, book, exchange.SideBuy); err == nil {
		t.Fatal("sizing accepted rounded notional 20% below the approximately 50 target")
	}
}

func TestApproxFiftyQuoteSizingFailsWhenFiftyQuoteNotTradable(t *testing.T) {
	instrument := exchange.Instrument{
		Symbol:            "ETH-USDT",
		PriceIncrement:    decimal.RequireFromString("0.01"),
		QuantityIncrement: decimal.RequireFromString("0.001"),
		MinQuantity:       decimal.RequireFromString("0.001"),
		MinNotional: exchange.OptionalDecimal{
			Value: decimal.RequireFromString("75"),
			Valid: true,
		},
	}
	book := exchange.OrderBook{
		Asks: []exchange.BookLevel{{Price: decimal.RequireFromString("2500"), Quantity: decimal.RequireFromString("1")}},
	}

	if _, err := sizeApproxFiftyQuoteOrder(instrument, book, exchange.SideBuy); err == nil {
		t.Fatal("sizing succeeded even though about 50 quote notional is below venue minimum")
	}
}

func TestAcceptanceSizingUsesVenueMinimumWithinExplicitSafetyCap(t *testing.T) {
	instrument := exchange.Instrument{
		Symbol:            "USDC-USDT0",
		PriceIncrement:    decimal.RequireFromString("0.0001"),
		QuantityIncrement: decimal.RequireFromString("0.0001"),
		MinQuantity:       decimal.RequireFromString("0.0001"),
		MinNotional: exchange.OptionalDecimal{
			Value: decimal.RequireFromString("100"),
			Valid: true,
		},
	}
	book := exchange.OrderBook{
		Asks: []exchange.BookLevel{{Price: decimal.RequireFromString("1.0006"), Quantity: decimal.RequireFromString("1000")}},
	}

	size, err := sizeAcceptanceQuoteOrder(
		instrument,
		book,
		exchange.SideBuy,
		decimal.RequireFromString("50"),
		decimal.RequireFromString("100"),
	)
	if err != nil {
		t.Fatalf("venue-minimum sizing failed: %v", err)
	}
	notional := size.Price.Mul(size.Quantity)
	if notional.LessThan(instrument.MinNotional.Value) {
		t.Fatalf("notional = %s below venue minimum %s", notional, instrument.MinNotional.Value)
	}
	if notional.GreaterThan(decimal.RequireFromString("100").Add(size.Price.Mul(instrument.QuantityIncrement))) {
		t.Fatalf("notional = %s exceeds the explicit cap plus one rounding step", notional)
	}
}

func TestAcceptanceRestingSizingUsesSubmittedLimitPriceForVenueMinimum(t *testing.T) {
	instrument := exchange.Instrument{
		Symbol:            "USDC-USDT0",
		PriceIncrement:    decimal.RequireFromString("0.0001"),
		QuantityIncrement: decimal.RequireFromString("0.0001"),
		MinQuantity:       decimal.RequireFromString("0.0001"),
		MinNotional: exchange.OptionalDecimal{
			Value: decimal.RequireFromString("100"),
			Valid: true,
		},
	}
	price := decimal.RequireFromString("0.98")
	size, err := sizeAcceptanceQuoteOrderAtPrice(
		instrument,
		price,
		decimal.RequireFromString("50"),
		decimal.RequireFromString("100"),
	)
	if err != nil {
		t.Fatalf("resting venue-minimum sizing failed: %v", err)
	}
	if got := price.Mul(size.Quantity); got.LessThan(instrument.MinNotional.Value) {
		t.Fatalf("submitted notional = %s below venue minimum %s", got, instrument.MinNotional.Value)
	}
}

func TestAcceptanceSizingUsesNextContractWithinExplicitSafetyCap(t *testing.T) {
	instrument := exchange.Instrument{
		Symbol:            "BTC-USDT",
		PriceIncrement:    decimal.RequireFromString("0.1"),
		QuantityIncrement: decimal.NewFromInt(1),
		MinQuantity:       decimal.NewFromInt(1),
	}
	price := decimal.RequireFromString("44.20836")
	size, err := sizeAcceptanceQuoteOrderAtPrice(
		instrument,
		price,
		decimal.NewFromInt(50),
		decimal.NewFromInt(100),
	)
	if err != nil {
		t.Fatalf("coarse-contract sizing failed: %v", err)
	}
	if !size.Quantity.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("quantity = %s, want 2 contracts", size.Quantity)
	}
	if notional := price.Mul(size.Quantity); notional.GreaterThan(decimal.NewFromInt(100)) {
		t.Fatalf("notional = %s exceeds explicit cap", notional)
	}
}

func TestSelectAcceptanceInstrumentAcceptsNativeUSDCPerpAlias(t *testing.T) {
	instrument := exchange.Instrument{
		Symbol:      "BTC-USDC",
		BaseAsset:   "BTC",
		QuoteAsset:  "USDC",
		SettleAsset: "USDC",
		Product:     exchange.ProductPerp,
	}
	got, err := selectAcceptanceInstrument(
		[]exchange.Instrument{instrument},
		exchange.ProductPerp,
		"BTCPERP",
	)
	if err != nil {
		t.Fatalf("select native USDC perp alias: %v", err)
	}
	if got.Symbol != instrument.Symbol {
		t.Fatalf("selected instrument=%+v, want %+v", got, instrument)
	}
}

func TestAcceptanceOrderCasePlanCoversEveryValidManifestBranch(t *testing.T) {
	for _, transport := range []string{"rest", "ws"} {
		spot := acceptanceOrderCases(exchange.ProductSpot, transport)
		wantSpot := []string{
			"place_order." + transport + ".market",
			"place_order." + transport + ".limit_resting",
			"place_order." + transport + ".limit_ioc",
			"place_order." + transport + ".limit_post_only",
			"place_order." + transport + ".client_order_id",
		}
		if got := acceptanceCaseIDs(spot); !sameStrings(got, wantSpot) {
			t.Fatalf("%s spot cases = %v, want %v", transport, got, wantSpot)
		}

		perp := acceptanceOrderCases(exchange.ProductPerp, transport)
		wantPerp := append(append([]string(nil), wantSpot...), "place_order."+transport+".perp_reduce_only")
		if got := acceptanceCaseIDs(perp); !sameStrings(got, wantPerp) {
			t.Fatalf("%s perp cases = %v, want %v", transport, got, wantPerp)
		}
	}
}

func TestSpotCleanupSkipsFeeDustAndUntradableResiduals(t *testing.T) {
	step := decimal.RequireFromString("0.0001")
	minNotional := exchange.OptionalDecimal{Value: decimal.RequireFromString("5"), Valid: true}
	price := decimal.RequireFromString("2500")

	if spotDeltaRequiresCleanup(decimal.RequireFromString("0.0002"), step, minNotional, price) {
		t.Fatal("two-step fee dust should remain inside cleanup tolerance")
	}
	if spotDeltaRequiresCleanup(decimal.RequireFromString("0.001"), step, minNotional, price) {
		t.Fatal("residual below the venue minimum notional should not trigger a rejected cleanup order")
	}
	if spotDeltaRequiresCleanup(decimal.RequireFromString("0.000003"), decimal.RequireFromString("0.000001"), exchange.OptionalDecimal{}, price) {
		t.Fatal("sub-cent residual should remain inside the generic fee tolerance when venue min notional is unavailable")
	}
	if !spotDeltaRequiresCleanup(decimal.RequireFromString("0.003"), step, minNotional, price) {
		t.Fatal("tradable residual above tolerance should be cleaned")
	}
}

func TestSpotFeeToleranceAcceptsGateUntradableCommissionDust(t *testing.T) {
	if !spotBalanceWithinFeeTolerance(
		decimal.RequireFromString("0.0002934"),
		decimal.RequireFromString("0.0001"),
		decimal.RequireFromString("1272.70"),
	) {
		t.Fatal("Gate commission dust worth less than 0.50 quote was rejected")
	}
}

func TestAcceptanceSubscriptionTimeoutAllowsBitgetDemoDepthRecovery(t *testing.T) {
	if got := acceptanceSubscriptionTimeout("BGS/WatchOrderBook"); got != 120*time.Second {
		t.Fatalf("Bitget order book timeout = %s, want 120s", got)
	}
	if got := acceptanceSubscriptionTimeout("GTU/WatchOrderBook"); got != 45*time.Second {
		t.Fatalf("default order book timeout = %s, want 45s", got)
	}
	if got := acceptanceSubscriptionTimeout("BGS/WatchCandles"); got != 75*time.Second {
		t.Fatalf("candle timeout = %s, want 75s", got)
	}
}

func TestAcceptanceIOCPriceUsesBoundedCrossingOffset(t *testing.T) {
	instrument := exchange.Instrument{PriceIncrement: decimal.RequireFromString("0.1")}
	book := exchange.OrderBook{
		Bids: []exchange.BookLevel{{Price: decimal.RequireFromString("99.0"), Quantity: decimal.RequireFromString("1")}},
		Asks: []exchange.BookLevel{{Price: decimal.RequireFromString("101.0"), Quantity: decimal.RequireFromString("1")}},
	}
	if got := acceptanceIOCPrice(instrument, book, exchange.SideBuy); !got.Equal(decimal.RequireFromString("102.1")) {
		t.Fatalf("buy IOC price = %s", got)
	}
	if got := acceptanceIOCPrice(instrument, book, exchange.SideSell); !got.Equal(decimal.RequireFromString("98.0")) {
		t.Fatalf("sell IOC price = %s", got)
	}
}
