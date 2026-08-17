package exchange_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type externalAcceptanceManifest struct {
	ProductRows    []externalManifestProductRow    `json:"product_rows"`
	ParameterCases []externalManifestParameterCase `json:"parameter_cases"`
}

type externalManifestProductRow struct {
	Code             string   `json:"code"`
	RESTMethods      []string `json:"rest_methods"`
	WebSocketMethods []string `json:"websocket_methods"`
}

type externalManifestParameterCase struct {
	ID            string   `json:"id"`
	ExternalCells []string `json:"external_cells"`
}

type externalOperationCell struct {
	RowCode   string
	Transport string
	Method    string
}

type externalParameterCaseCell struct {
	RowCode string
	CaseID  string
}

type externalCoverageMark struct {
	Kind      string
	RowCode   string
	Transport string
	Method    string
	CaseID    string
}

type externalAcceptanceLedger struct {
	operations     map[externalOperationCell]struct{}
	parameterCases map[externalParameterCaseCell]struct{}
}

func loadExternalAcceptanceManifest(t *testing.T) externalAcceptanceManifest {
	t.Helper()

	payload, err := os.ReadFile("testdata/public_surface_manifest.json")
	if err != nil {
		t.Fatalf("load public surface manifest: %v", err)
	}
	var manifest externalAcceptanceManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatalf("decode public surface manifest: %v", err)
	}
	return manifest
}

func buildExternalAcceptanceLedger(t *testing.T, manifest externalAcceptanceManifest) externalAcceptanceLedger {
	t.Helper()

	ledger := externalAcceptanceLedger{
		operations:     make(map[externalOperationCell]struct{}),
		parameterCases: make(map[externalParameterCaseCell]struct{}),
	}
	for _, row := range manifest.ProductRows {
		if strings.TrimSpace(row.Code) == "" {
			t.Fatal("manifest product row has empty code")
		}
		for _, method := range row.RESTMethods {
			ledger.operations[externalOperationCell{RowCode: row.Code, Transport: "rest", Method: method}] = struct{}{}
		}
		for _, method := range row.WebSocketMethods {
			ledger.operations[externalOperationCell{RowCode: row.Code, Transport: "websocket", Method: method}] = struct{}{}
		}
	}
	for _, parameterCase := range manifest.ParameterCases {
		if strings.TrimSpace(parameterCase.ID) == "" {
			t.Fatal("manifest parameter case has empty id")
		}
		for _, rowCode := range parameterCase.ExternalCells {
			ledger.parameterCases[externalParameterCaseCell{RowCode: rowCode, CaseID: parameterCase.ID}] = struct{}{}
		}
	}
	return ledger
}

func (ledger externalAcceptanceLedger) PublicAPICellCount() int {
	return len(ledger.operations)
}

func (ledger externalAcceptanceLedger) ParameterCaseCellCount() int {
	return len(ledger.parameterCases)
}

func (ledger externalAcceptanceLedger) ExpectOperation(cell externalOperationCell) bool {
	_, ok := ledger.operations[cell]
	return ok
}

func (ledger externalAcceptanceLedger) ExpectParameterCase(cell externalParameterCaseCell) bool {
	_, ok := ledger.parameterCases[cell]
	return ok
}

func (ledger externalAcceptanceLedger) CompleteMarks() []externalCoverageMark {
	marks := make([]externalCoverageMark, 0, len(ledger.operations)+len(ledger.parameterCases))
	for cell := range ledger.operations {
		marks = append(marks, externalCoverageMark{
			Kind:      "operation",
			RowCode:   cell.RowCode,
			Transport: cell.Transport,
			Method:    cell.Method,
		})
	}
	for cell := range ledger.parameterCases {
		marks = append(marks, externalCoverageMark{
			Kind:    "parameter_case",
			RowCode: cell.RowCode,
			CaseID:  cell.CaseID,
		})
	}
	sort.Slice(marks, func(i, j int) bool {
		return coverageMarkKey(marks[i]) < coverageMarkKey(marks[j])
	})
	return marks
}

func (ledger externalAcceptanceLedger) CompleteMarksForRow(rowCode string) ([]externalCoverageMark, error) {
	marks := make([]externalCoverageMark, 0)
	for _, mark := range ledger.CompleteMarks() {
		if mark.RowCode == rowCode {
			marks = append(marks, mark)
		}
	}
	if len(marks) == 0 {
		return nil, fmt.Errorf("unknown external acceptance row %s", rowCode)
	}
	return marks, nil
}

func (ledger externalAcceptanceLedger) ValidateMarks(marks []externalCoverageMark) error {
	return validateCoverageMarks(ledger.CompleteMarks(), marks)
}

func (ledger externalAcceptanceLedger) ValidateRowMarks(rowCode string, marks []externalCoverageMark) error {
	want, err := ledger.CompleteMarksForRow(rowCode)
	if err != nil {
		return err
	}
	return validateCoverageMarks(want, marks)
}

func validateCoverageMarks(expected, marks []externalCoverageMark) error {
	want := make(map[string]struct{}, len(expected))
	for _, mark := range expected {
		want[coverageMarkKey(mark)] = struct{}{}
	}

	seen := make(map[string]struct{}, len(marks))
	for _, mark := range marks {
		key := coverageMarkKey(mark)
		if _, ok := want[key]; !ok {
			return fmt.Errorf("unknown external coverage mark %s", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate external coverage mark %s", key)
		}
		seen[key] = struct{}{}
	}
	if len(seen) != len(want) {
		return fmt.Errorf("missing external coverage marks: got %d want %d", len(seen), len(want))
	}
	return nil
}

func coverageMarkKey(mark externalCoverageMark) string {
	switch mark.Kind {
	case "operation":
		return "operation|" + mark.RowCode + "|" + mark.Transport + "|" + mark.Method
	case "parameter_case":
		return "parameter_case|" + mark.RowCode + "|" + mark.CaseID
	default:
		return "unknown|" + mark.Kind
	}
}

type externalRowCoverage struct {
	t       *testing.T
	ledger  externalAcceptanceLedger
	rowCode string
	marks   map[string]externalCoverageMark
}

type externalCoverageWaiver struct {
	reason string
	marks  map[string]struct{}
}

func newExternalCoverageWaiver(reason string, marks ...externalCoverageMark) externalCoverageWaiver {
	waiver := externalCoverageWaiver{
		reason: strings.TrimSpace(reason),
		marks:  make(map[string]struct{}, len(marks)),
	}
	for _, mark := range marks {
		waiver.marks[coverageMarkKey(mark)] = struct{}{}
	}
	return waiver
}

func (waiver externalCoverageWaiver) Allows(mark externalCoverageMark) bool {
	_, ok := waiver.marks[coverageMarkKey(mark)]
	return ok
}

func newExternalRowCoverage(t *testing.T, ledger externalAcceptanceLedger, rowCode string) *externalRowCoverage {
	t.Helper()
	if _, err := ledger.CompleteMarksForRow(rowCode); err != nil {
		t.Fatal(err)
	}
	return &externalRowCoverage{
		t:       t,
		ledger:  ledger,
		rowCode: rowCode,
		marks:   make(map[string]externalCoverageMark),
	}
}

func (coverage *externalRowCoverage) MarkOperation(transport, method string) {
	coverage.t.Helper()
	mark := externalCoverageMark{
		Kind:      "operation",
		RowCode:   coverage.rowCode,
		Transport: transport,
		Method:    method,
	}
	cell := externalOperationCell{RowCode: coverage.rowCode, Transport: transport, Method: method}
	if !coverage.ledger.ExpectOperation(cell) {
		coverage.t.Fatalf("unexpected operation coverage mark %s", coverageMarkKey(mark))
	}
	coverage.marks[coverageMarkKey(mark)] = mark
}

func (coverage *externalRowCoverage) MarkParameterCase(caseID string) {
	coverage.t.Helper()
	mark := externalCoverageMark{Kind: "parameter_case", RowCode: coverage.rowCode, CaseID: caseID}
	cell := externalParameterCaseCell{RowCode: coverage.rowCode, CaseID: caseID}
	if !coverage.ledger.ExpectParameterCase(cell) {
		coverage.t.Fatalf("unexpected parameter-case coverage mark %s", coverageMarkKey(mark))
	}
	coverage.marks[coverageMarkKey(mark)] = mark
}

func (coverage *externalRowCoverage) MarkCount() int {
	return len(coverage.marks)
}

func (coverage *externalRowCoverage) Validate() error {
	marks := make([]externalCoverageMark, 0, len(coverage.marks))
	for _, mark := range coverage.marks {
		marks = append(marks, mark)
	}
	return coverage.ledger.ValidateRowMarks(coverage.rowCode, marks)
}

func (coverage *externalRowCoverage) ValidateWithWaiver(waiver externalCoverageWaiver) error {
	if waiver.reason == "" || len(waiver.marks) == 0 {
		return errors.New("external coverage waiver requires a reason and at least one mark")
	}
	expected, err := coverage.ledger.CompleteMarksForRow(coverage.rowCode)
	if err != nil {
		return err
	}
	expectedKeys := make(map[string]struct{}, len(expected))
	for _, mark := range expected {
		expectedKeys[coverageMarkKey(mark)] = struct{}{}
	}
	for key := range waiver.marks {
		if _, ok := expectedKeys[key]; !ok {
			return fmt.Errorf("waiver references unknown external coverage mark %s", key)
		}
		if _, exercised := coverage.marks[key]; exercised {
			return fmt.Errorf("waiver references exercised external coverage mark %s", key)
		}
	}
	var missing []string
	for key := range expectedKeys {
		if _, exercised := coverage.marks[key]; exercised {
			continue
		}
		if _, waived := waiver.marks[key]; waived {
			continue
		}
		missing = append(missing, key)
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing non-waived external coverage marks: %s", strings.Join(missing, ", "))
	}
	return nil
}

type ownedOrderJournal struct {
	orderIDs       map[string]struct{}
	clientOrderIDs map[string]struct{}
}

func newOwnedOrderJournal() *ownedOrderJournal {
	return &ownedOrderJournal{
		orderIDs:       make(map[string]struct{}),
		clientOrderIDs: make(map[string]struct{}),
	}
}

func (journal *ownedOrderJournal) TrackPlacement(ack exchange.OrderAcknowledgement) {
	if isTerminalAckState(ack.State) {
		return
	}
	journal.TrackOrderID(ack.OrderID)
	journal.TrackClientOrderID(ack.ClientOrderID)
}

func (journal *ownedOrderJournal) TrackOrderID(orderID string) {
	if isPortableNativeOrderID(orderID) {
		journal.orderIDs[orderID] = struct{}{}
	}
}

func (journal *ownedOrderJournal) TrackClientOrderID(clientOrderID string) {
	if isPositivePortableOrderID(clientOrderID) {
		journal.clientOrderIDs[clientOrderID] = struct{}{}
	}
}

func (journal *ownedOrderJournal) MarkTerminal(orderID string) {
	delete(journal.orderIDs, orderID)
}

func (journal *ownedOrderJournal) Cleanup(ctx context.Context, client orderCleanupClient, instrument exchange.Instrument) error {
	page, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: &instrument, Limit: 100})
	if err != nil {
		return err
	}
	for _, order := range page.Orders {
		if journal.Owns(order) {
			if !isPortableNativeOrderID(order.OrderID) {
				return fmt.Errorf("owned open order has nonportable native id %q", order.OrderID)
			}
			journal.TrackOrderID(order.OrderID)
		}
	}

	ids := make([]string, 0, len(journal.orderIDs))
	for id := range journal.orderIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		_, _ = client.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: instrument, OrderID: id})
	}

	var lastErr error
	for {
		page, err = client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: &instrument, Limit: 100})
		if err == nil {
			residualOrderID := ""
			for _, order := range page.Orders {
				if journal.Owns(order) {
					residualOrderID = order.OrderID
					break
				}
			}
			if residualOrderID == "" {
				return nil
			}
			lastErr = fmt.Errorf("owned order %s remains open after cleanup", residualOrderID)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return errors.Join(lastErr, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (journal *ownedOrderJournal) Owns(order exchange.Order) bool {
	if _, owned := journal.orderIDs[order.OrderID]; owned {
		return true
	}
	_, owned := journal.clientOrderIDs[order.ClientOrderID]
	return owned
}

type orderCleanupClient interface {
	CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error)
	OpenOrders(context.Context, exchange.OpenOrdersRequest) (exchange.OrderPage, error)
}

func isPositivePortableOrderID(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0
}

func isPortableNativeOrderID(value string) bool {
	if isPositivePortableOrderID(value) {
		return true
	}
	if len(value) == 36 {
		for index, char := range value {
			if index == 8 || index == 13 || index == 18 || index == 23 {
				if char != '-' {
					return false
				}
				continue
			}
			if !isHexCharacter(char) {
				return false
			}
		}
		return true
	}
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return false
	}
	for _, char := range value[2:] {
		if !isHexCharacter(char) {
			return false
		}
	}
	return true
}

func isHexCharacter(char rune) bool {
	return (char >= '0' && char <= '9') ||
		(char >= 'a' && char <= 'f') ||
		(char >= 'A' && char <= 'F')
}

func isTerminalAckState(state exchange.OrderAckState) bool {
	return state == exchange.AckImmediatelyFilled || state == exchange.AckCanceled || state == exchange.AckRejected
}

func orderPageContainsOrderID(page exchange.OrderPage, orderID string) bool {
	for _, order := range page.Orders {
		if order.OrderID == orderID {
			return true
		}
	}
	return false
}

type fakeOrderCleanupClient struct {
	open            map[string]exchange.Order
	canceledIDs     []string
	failCancel      bool
	cancelAllCalled bool
}

func (client *fakeOrderCleanupClient) CancelOrder(_ context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	client.canceledIDs = append(client.canceledIDs, request.OrderID)
	if client.failCancel {
		return exchange.OrderAcknowledgement{}, errors.New("cancel failed")
	}
	delete(client.open, request.OrderID)
	return exchange.OrderAcknowledgement{OrderID: request.OrderID, State: exchange.AckCanceled}, nil
}

func (client *fakeOrderCleanupClient) OpenOrders(context.Context, exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	orders := make([]exchange.Order, 0, len(client.open))
	for _, order := range client.open {
		orders = append(orders, order)
	}
	return exchange.OrderPage{Orders: orders}, nil
}

func (client *fakeOrderCleanupClient) openOrderIDs() map[string]bool {
	ids := make(map[string]bool, len(client.open))
	for id := range client.open {
		ids[id] = true
	}
	return ids
}

func authorizeFillOffset(ack exchange.OrderAcknowledgement) error {
	if ack.State != exchange.AckImmediatelyFilled && ack.State != exchange.AckPartiallyFilled {
		return fmt.Errorf("fill offset requires confirmed fill state, got %s", ack.State)
	}
	if !ack.FilledQuantity.IsPositive() {
		return errors.New("fill offset requires positive filled quantity")
	}
	return nil
}

type approxOrderSize struct {
	Price    decimal.Decimal
	Quantity decimal.Decimal
}

func sizeApproxFiftyQuoteOrder(instrument exchange.Instrument, book exchange.OrderBook, side exchange.Side) (approxOrderSize, error) {
	return sizeApproxQuoteOrder(instrument, book, side, decimal.RequireFromString("50"))
}

func sizeApproxQuoteOrder(
	instrument exchange.Instrument,
	book exchange.OrderBook,
	side exchange.Side,
	target decimal.Decimal,
) (approxOrderSize, error) {
	return sizeAcceptanceQuoteOrder(instrument, book, side, target, decimal.Zero)
}

func sizeAcceptanceQuoteOrder(
	instrument exchange.Instrument,
	book exchange.OrderBook,
	side exchange.Side,
	target decimal.Decimal,
	maxNotional decimal.Decimal,
) (approxOrderSize, error) {
	price, err := executablePrice(book, side)
	if err != nil {
		return approxOrderSize{}, err
	}
	price = roundPriceToTick(price, instrument.Rules.PriceIncrement, side)
	if !price.IsPositive() {
		return approxOrderSize{}, errors.New("rounded price is not positive")
	}
	return sizeAcceptanceQuoteOrderAtPrice(instrument, price, target, maxNotional)
}

func sizeAcceptanceQuoteOrderAtPrice(
	instrument exchange.Instrument,
	price decimal.Decimal,
	target decimal.Decimal,
	maxNotional decimal.Decimal,
) (approxOrderSize, error) {
	if !price.IsPositive() {
		return approxOrderSize{}, errors.New("order price must be positive")
	}
	effectiveTarget := target
	useVenueMinimum := instrument.Rules.MinNotional.Valid && instrument.Rules.MinNotional.Value.GreaterThan(target)
	if useVenueMinimum {
		if !maxNotional.IsPositive() {
			return approxOrderSize{}, fmt.Errorf("approximately %s quote notional is below min notional %s", target, instrument.Rules.MinNotional.Value)
		}
		effectiveTarget = instrument.Rules.MinNotional.Value
	}

	quantity := roundDownToStep(effectiveTarget.Div(price), instrument.Rules.QuantityIncrement)
	if useVenueMinimum {
		quantity = roundUpToStep(effectiveTarget.Div(price), instrument.Rules.QuantityIncrement)
	} else if maxNotional.IsPositive() && price.Mul(quantity).LessThan(target.Mul(decimal.RequireFromString("0.90"))) {
		nextQuantity := roundUpToStep(effectiveTarget.Div(price), instrument.Rules.QuantityIncrement)
		if price.Mul(nextQuantity).LessThanOrEqual(maxNotional) {
			quantity = nextQuantity
		}
	}
	if quantity.LessThan(instrument.Rules.MinQuantity) {
		quantity = instrument.Rules.MinQuantity
	}
	quantity = roundDownToStep(quantity, instrument.Rules.QuantityIncrement)
	if !quantity.IsPositive() {
		return approxOrderSize{}, errors.New("rounded quantity is not positive")
	}

	notional := price.Mul(quantity)
	if instrument.Rules.MinNotional.Valid && notional.LessThan(instrument.Rules.MinNotional.Value) {
		return approxOrderSize{}, fmt.Errorf("approximately %s quote notional is below min notional %s", target, instrument.Rules.MinNotional.Value)
	}
	if notional.LessThan(target.Mul(decimal.RequireFromString("0.90"))) {
		return approxOrderSize{}, fmt.Errorf("rounded notional %s is too far below approximately %s", notional, target)
	}
	if !maxNotional.IsPositive() && notional.GreaterThan(target.Mul(decimal.RequireFromString("1.10"))) {
		return approxOrderSize{}, fmt.Errorf("minimum tradable notional %s is too far above approximately %s", notional, target)
	}
	if maxNotional.IsPositive() {
		roundingAllowance := price.Mul(instrument.Rules.QuantityIncrement)
		if notional.GreaterThan(maxNotional.Add(roundingAllowance)) {
			return approxOrderSize{}, fmt.Errorf("minimum tradable notional %s exceeds configured maximum %s", notional, maxNotional)
		}
	}
	return approxOrderSize{Price: price, Quantity: quantity}, nil
}

func executablePrice(book exchange.OrderBook, side exchange.Side) (decimal.Decimal, error) {
	switch side {
	case exchange.SideBuy:
		if len(book.Asks) == 0 {
			return decimal.Zero, errors.New("buy sizing requires an ask")
		}
		return book.Asks[0].Price, nil
	case exchange.SideSell:
		if len(book.Bids) == 0 {
			return decimal.Zero, errors.New("sell sizing requires a bid")
		}
		return book.Bids[0].Price, nil
	default:
		return decimal.Zero, errors.New("side must be buy or sell")
	}
}

func roundPriceToTick(price, tick decimal.Decimal, side exchange.Side) decimal.Decimal {
	if !tick.IsPositive() {
		return price
	}
	switch side {
	case exchange.SideBuy:
		return roundUpToStep(price, tick)
	default:
		return roundDownToStep(price, tick)
	}
}

func roundDownToStep(value, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() {
		return value
	}
	return value.Div(step).Floor().Mul(step)
}

func roundUpToStep(value, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() {
		return value
	}
	return value.Div(step).Ceil().Mul(step)
}

func spotDeltaRequiresCleanup(
	delta decimal.Decimal,
	step decimal.Decimal,
	minNotional exchange.OptionalDecimal,
	price decimal.Decimal,
) bool {
	if !delta.IsPositive() || delta.LessThanOrEqual(step.Mul(decimal.NewFromInt(2))) {
		return false
	}
	if price.IsPositive() && delta.Mul(price).LessThanOrEqual(decimal.RequireFromString("0.50")) {
		return false
	}
	if minNotional.Valid && price.IsPositive() && delta.Mul(price).LessThan(minNotional.Value) {
		return false
	}
	return true
}

func sameStrings(got, want []string) bool {
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

type acceptanceOrderCase struct {
	ID   string
	Kind string
}

func acceptanceOrderCases(product exchange.Product, transport string) []acceptanceOrderCase {
	prefix := "place_order." + transport + "."
	cases := []acceptanceOrderCase{
		{ID: prefix + "market", Kind: "market"},
		{ID: prefix + "limit_resting", Kind: "limit_resting"},
		{ID: prefix + "limit_ioc", Kind: "limit_ioc"},
		{ID: prefix + "limit_post_only", Kind: "limit_post_only"},
		{ID: prefix + "client_order_id", Kind: "client_order_id"},
	}
	if product == exchange.ProductPerp {
		cases = append(cases, acceptanceOrderCase{ID: prefix + "perp_reduce_only", Kind: "perp_reduce_only"})
	}
	return cases
}

func acceptanceCaseIDs(cases []acceptanceOrderCase) []string {
	ids := make([]string, 0, len(cases))
	for _, orderCase := range cases {
		ids = append(ids, orderCase.ID)
	}
	return ids
}

type exchangeAcceptanceRow struct {
	code                      string
	product                   exchange.Product
	instrumentHint            string
	notional                  decimal.Decimal
	maxNotional               decimal.Decimal
	perpReference             exchangeAcceptancePerpReference
	perpPublicWS              exchangeAcceptancePerpPublicWebSocket
	watchMarkPriceUnsupported bool
	oneSidedSpotWaiver        bool
}

type exchangeAcceptancePerpReference interface {
	FundingRate(context.Context, exchange.FundingRateRequest) (exchange.FundingRate, error)
	FundingRateHistory(context.Context, exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error)
}

type exchangeAcceptancePerpPublicWebSocket interface {
	WatchMarkPrice(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.MarkPriceEvent], error)
	WatchFundingRate(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.FundingRateEvent], error)
}

type exchangeAcceptanceREST interface {
	exchange.MarketREST
	exchange.OrderREST
	Balances(context.Context) ([]exchange.Balance, error)
}

type exchangeAcceptanceCatalog interface {
	ResolveInstrument(context.Context, string) (exchange.Instrument, error)
	RefreshInstruments(context.Context) ([]exchange.Instrument, error)
}

type spotExposureCleanupClient interface {
	Balances(context.Context) ([]exchange.Balance, error)
	OrderBook(context.Context, exchange.OrderBookRequest) (exchange.OrderBook, error)
	PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error)
}

type perpExposureCleanupClient interface {
	Positions(context.Context, exchange.PositionsRequest) ([]exchange.Position, error)
	PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error)
}

type spotAcceptanceCleanupClient interface {
	orderCleanupClient
	spotExposureCleanupClient
}

type perpAcceptanceCleanupClient interface {
	orderCleanupClient
	perpExposureCleanupClient
}

type exchangeAcceptanceOrderTransport interface {
	PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error)
	CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error)
}

type acceptanceOrderTransport struct {
	caseName       string
	coverageName   string
	orderTransport exchangeAcceptanceOrderTransport
}

type acceptanceSubscriptionWitness struct {
	operation string
	arm       func()
	waitEvent func(context.Context) error
	close     func() error
}

func (witness *acceptanceSubscriptionWitness) Arm() {
	witness.arm()
}

func (witness *acceptanceSubscriptionWitness) WaitEvent(ctx context.Context) error {
	return witness.waitEvent(ctx)
}

func (witness *acceptanceSubscriptionWitness) Close() error {
	return witness.close()
}

var exchangeAcceptanceClientID atomic.Int64
var exchangeAcceptanceClientIDSeed = uint64(time.Now().UnixNano()) & (1<<48 - 1)

func validateAcceptanceTopOfBook(
	row exchangeAcceptanceRow,
	instrument exchange.Instrument,
	book exchange.OrderBook,
) error {
	hasBid := len(book.Bids) != 0 && book.Bids[0].Price.IsPositive()
	hasAsk := len(book.Asks) != 0 && book.Asks[0].Price.IsPositive()
	if hasBid && hasAsk {
		return nil
	}
	if row.oneSidedSpotWaiver && row.code == "LIS" && (hasBid || hasAsk) {
		return nil
	}
	return fmt.Errorf("%s OrderBook has no positive two-sided top of book for %s", row.code, instrument.Identity.Symbol)
}

func acceptanceSubscriptionSkipped(row exchangeAcceptanceRow, operation string) bool {
	return row.oneSidedSpotWaiver && row.code == "LIS" && operation == "WatchBBO"
}

func lighterSpotOneSidedCoverageWaiver() externalCoverageWaiver {
	// The reason used to name Testnet, which is where the one-sided book was
	// first seen. It reads as live now, and not because the environment was
	// find-and-replaced out of it: capability/matrix.go holds LIS back from
	// write promotion pending live evidence of two-sided liquidity, so the
	// premise this waiver rests on is the platform's live-facing position and
	// not an artefact of a sandbox that no longer exists.
	const reason = "Lighter Spot is structurally one-sided; user authorized skipping live order mutations"
	operation := func(transport, method string) externalCoverageMark {
		return externalCoverageMark{Kind: "operation", RowCode: "LIS", Transport: transport, Method: method}
	}
	parameter := func(caseID string) externalCoverageMark {
		return externalCoverageMark{Kind: "parameter_case", RowCode: "LIS", CaseID: caseID}
	}
	return newExternalCoverageWaiver(
		reason,
		operation("rest", "PlaceOrder"),
		operation("rest", "CancelOrder"),
		operation("rest", "LookupExactOrder"),
		operation("websocket", "PlaceOrder"),
		operation("websocket", "CancelOrder"),
		operation("websocket", "WatchBBO"),
		parameter("place_order.rest.market"),
		parameter("place_order.rest.limit_resting"),
		parameter("place_order.rest.limit_ioc"),
		parameter("place_order.rest.limit_post_only"),
		parameter("place_order.rest.client_order_id"),
		parameter("place_order.ws.market"),
		parameter("place_order.ws.limit_resting"),
		parameter("place_order.ws.limit_ioc"),
		parameter("place_order.ws.limit_post_only"),
		parameter("place_order.ws.client_order_id"),
		parameter("cancel_order.rest.order_id"),
		parameter("cancel_order.ws.order_id"),
	)
}

func refreshAcceptanceInstrument(
	ctx context.Context,
	client exchangeAcceptanceCatalog,
	selected exchange.Instrument,
) (exchange.Instrument, error) {
	resolved, err := client.ResolveInstrument(ctx, selected.Identity.Symbol)
	if err != nil {
		return exchange.Instrument{}, fmt.Errorf("resolve %s: %w", selected.Identity.Symbol, err)
	}
	if resolved.Identity != selected.Identity {
		return exchange.Instrument{}, fmt.Errorf(
			"resolved identity mismatch for %s: got %+v want %+v",
			selected.Identity.Symbol,
			resolved.Identity,
			selected.Identity,
		)
	}
	refreshed, err := client.RefreshInstruments(ctx)
	if err != nil {
		return exchange.Instrument{}, fmt.Errorf("refresh instruments: %w", err)
	}
	for _, instrument := range refreshed {
		if instrument.Identity == selected.Identity {
			return instrument, nil
		}
	}
	return exchange.Instrument{}, fmt.Errorf(
		"refreshed catalog no longer contains %s (%s)",
		selected.Identity.Symbol,
		selected.Identity.NativeSymbol,
	)
}

func placeTrackedAcceptanceOrder(
	ctx context.Context,
	transport exchangeAcceptanceOrderTransport,
	request exchange.PlaceOrderRequest,
	journal *ownedOrderJournal,
) (exchange.OrderAcknowledgement, error) {
	journal.TrackClientOrderID(request.ClientOrderID)
	ack, err := transport.PlaceOrder(ctx, request)
	if err == nil {
		journal.TrackPlacement(ack)
	}
	return ack, err
}

func nextAcceptanceClientOrderID() string {
	const maxPortableClientOrderID = uint64(1<<48 - 1)
	sequence := uint64(exchangeAcceptanceClientID.Add(1))
	value := (exchangeAcceptanceClientIDSeed + sequence) & maxPortableClientOrderID
	if value == 0 {
		value = 1
	}
	return fmt.Sprintf("%d", value)
}

func acceptanceRestingBuyPrice(instrument exchange.Instrument, book exchange.OrderBook) decimal.Decimal {
	price := book.Bids[0].Price.Mul(decimal.RequireFromString("0.98"))
	return acceptanceLimitPrice(instrument, price, false)
}

func acceptanceIOCPrice(instrument exchange.Instrument, book exchange.OrderBook, side exchange.Side) decimal.Decimal {
	if instrument.Identity.Venue == exchange.VenueBybit && instrument.Identity.Product == exchange.ProductSpot {
		if side == exchange.SideBuy {
			return acceptanceLimitPrice(instrument, book.Asks[0].Price, true)
		}
		return acceptanceLimitPrice(instrument, book.Bids[0].Price, false)
	}
	if side == exchange.SideBuy {
		return acceptanceLimitPrice(instrument, book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), true)
	}
	return acceptanceLimitPrice(instrument, book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), false)
}

func acceptanceLimitPrice(instrument exchange.Instrument, desired decimal.Decimal, roundUp bool) decimal.Decimal {
	step := instrument.Rules.PriceIncrement
	if instrument.Identity.Venue == exchange.VenueHyperliquid {
		significantStep := hyperliquidAcceptanceSignificantStep(desired)
		if significantStep.GreaterThan(step) {
			step = significantStep
		}
	}
	if roundUp {
		return roundUpToStep(desired, step)
	}
	return roundDownToStep(desired, step)
}

func hyperliquidAcceptanceSignificantStep(value decimal.Decimal) decimal.Decimal {
	value = value.Abs()
	if value.IsZero() || value.Equal(value.Truncate(0)) {
		return decimal.Zero
	}
	parts := strings.SplitN(value.String(), ".", 2)
	integerDigits := len(strings.TrimLeft(parts[0], "0"))
	if integerDigits > 0 {
		return decimal.New(1, int32(integerDigits-5))
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	leadingZeros := len(fraction) - len(strings.TrimLeft(fraction, "0"))
	return decimal.New(1, int32(-(leadingZeros + 5)))
}

func perpRoundTripDirection(baseline decimal.Decimal) (exchange.Side, exchange.Side, decimal.Decimal) {
	if baseline.IsNegative() {
		return exchange.SideSell, exchange.SideBuy, decimal.NewFromInt(-1)
	}
	return exchange.SideBuy, exchange.SideSell, decimal.NewFromInt(1)
}

func positionMovedInDirection(
	current decimal.Decimal,
	baseline decimal.Decimal,
	direction decimal.Decimal,
	step decimal.Decimal,
) bool {
	return current.Sub(baseline).Mul(direction).GreaterThan(step.Div(decimal.NewFromInt(2)))
}

func acceptanceBalanceTotal(balances []exchange.Balance, asset string) decimal.Decimal {
	for _, balance := range balances {
		if strings.EqualFold(balance.Asset, asset) {
			return balance.Total
		}
	}
	return decimal.Zero
}

func signedAcceptancePosition(positions []exchange.Position, instrument exchange.Instrument) decimal.Decimal {
	total := decimal.Zero
	for _, position := range positions {
		if !acceptancePositionMatchesInstrument(position, instrument) {
			continue
		}
		switch position.Side {
		case exchange.SideBuy:
			total = total.Add(position.Quantity)
		case exchange.SideSell:
			total = total.Sub(position.Quantity)
		}
	}
	return total
}

func acceptancePositionMatchesInstrument(position exchange.Position, instrument exchange.Instrument) bool {
	if position.NativeID != "" && instrument.Identity.NativeID != "" {
		return position.NativeID == instrument.Identity.NativeID
	}
	if position.NativeSymbol != "" && instrument.Identity.NativeSymbol != "" {
		return normalizeAcceptanceSymbol(position.NativeSymbol) == normalizeAcceptanceSymbol(instrument.Identity.NativeSymbol)
	}
	return false
}

func cleanupSpotAcceptanceExposure(
	ctx context.Context,
	row exchangeAcceptanceRow,
	rest spotExposureCleanupClient,
	instrument exchange.Instrument,
	baseline []exchange.Balance,
	journal *ownedOrderJournal,
) error {
	balances, err := rest.Balances(ctx)
	if err != nil {
		return fmt.Errorf("%s cleanup Balances: %w", row.code, err)
	}
	delta := acceptanceBalanceTotal(balances, instrument.Identity.BaseAsset).Sub(acceptanceBalanceTotal(baseline, instrument.Identity.BaseAsset))
	if spotBalanceWithinFeeTolerance(delta, instrument.Rules.QuantityIncrement, decimal.Zero) {
		return nil
	}
	book, err := rest.OrderBook(ctx, exchange.OrderBookRequest{Instrument: instrument, Limit: 1})
	if err != nil {
		return fmt.Errorf("%s cleanup OrderBook: %w", row.code, err)
	}
	side := exchange.SideSell
	price, err := executablePrice(book, side)
	if delta.IsNegative() {
		side = exchange.SideBuy
		price, err = executablePrice(book, side)
	}
	if err != nil {
		return fmt.Errorf("%s cleanup executable price: %w", row.code, err)
	}
	if spotBalanceWithinFeeTolerance(delta, instrument.Rules.QuantityIncrement, price) {
		return nil
	}
	quantity := roundDownToStep(delta.Abs(), instrument.Rules.QuantityIncrement)
	if !quantity.IsPositive() {
		return fmt.Errorf("%s cleanup delta %s cannot be represented by quantity step %s", row.code, delta, instrument.Rules.QuantityIncrement)
	}
	if instrument.Rules.MinNotional.Valid && quantity.Mul(price).LessThan(instrument.Rules.MinNotional.Value) {
		return nil
	}
	request := exchange.PlaceOrderRequest{
		Instrument:    instrument,
		ClientOrderID: nextAcceptanceClientOrderID(),
		Side:          side,
		Type:          exchange.OrderTypeMarket,
		Quantity:      quantity,
	}
	journal.TrackClientOrderID(request.ClientOrderID)
	ack, err := rest.PlaceOrder(ctx, request)
	if err != nil {
		return fmt.Errorf("%s cleanup spot delta %s %s: %w", row.code, quantity, instrument.Identity.BaseAsset, err)
	}
	journal.TrackPlacement(ack)
	if err := validateAcceptanceCleanupAck(ack); err != nil {
		return fmt.Errorf("%s cleanup spot acknowledgement: %w", row.code, err)
	}
	return waitSpotAcceptanceBaseline(ctx, row.code, rest, instrument, baseline, price)
}

func cleanupPerpAcceptanceExposure(
	ctx context.Context,
	row exchangeAcceptanceRow,
	rest perpExposureCleanupClient,
	instrument exchange.Instrument,
	baseline decimal.Decimal,
	journal *ownedOrderJournal,
) error {
	positions, err := rest.Positions(ctx, exchange.PositionsRequest{Instrument: &instrument})
	if err != nil {
		return fmt.Errorf("%s cleanup Positions: %w", row.code, err)
	}
	current := signedAcceptancePosition(positions, instrument)
	delta := current.Sub(baseline)
	if delta.Abs().LessThanOrEqual(instrument.Rules.QuantityIncrement.Div(decimal.NewFromInt(2))) {
		return nil
	}
	quantity := roundDownToStep(delta.Abs(), instrument.Rules.QuantityIncrement)
	if !quantity.IsPositive() {
		return fmt.Errorf("%s cleanup position delta %s cannot be represented by quantity step %s", row.code, delta, instrument.Rules.QuantityIncrement)
	}
	side := exchange.SideSell
	if delta.IsNegative() {
		side = exchange.SideBuy
	}
	reduceOnly := baseline.IsZero() ||
		current.Sign() == baseline.Sign() && current.Abs().GreaterThan(baseline.Abs())
	request := exchange.PlaceOrderRequest{
		Instrument:    instrument,
		ClientOrderID: nextAcceptanceClientOrderID(),
		Side:          side,
		Type:          exchange.OrderTypeMarket,
		Quantity:      quantity,
		ReduceOnly:    reduceOnly,
	}
	journal.TrackClientOrderID(request.ClientOrderID)
	ack, err := rest.PlaceOrder(ctx, request)
	if err != nil {
		return fmt.Errorf("%s cleanup perp delta %s: %w", row.code, delta, err)
	}
	journal.TrackPlacement(ack)
	if err := validateAcceptanceCleanupAck(ack); err != nil {
		return fmt.Errorf("%s cleanup perp acknowledgement: %w", row.code, err)
	}
	return waitPerpAcceptanceBaseline(ctx, row.code, rest, instrument, baseline)
}

func validateAcceptanceCleanupAck(ack exchange.OrderAcknowledgement) error {
	if err := ack.Validate(); err != nil {
		return err
	}
	if ack.State == exchange.AckRejected || ack.State == exchange.AckAmbiguous {
		return fmt.Errorf("unsafe state %s: %s %s", ack.State, ack.VenueCode, ack.VenueMessage)
	}
	return nil
}

func spotBalanceWithinFeeTolerance(delta, step, price decimal.Decimal) bool {
	delta = delta.Abs()
	if delta.LessThanOrEqual(step.Mul(decimal.NewFromInt(2))) {
		return true
	}
	return price.IsPositive() && delta.Mul(price).LessThanOrEqual(decimal.RequireFromString("0.50"))
}

func waitSpotAcceptanceBaseline(
	ctx context.Context,
	rowCode string,
	rest interface {
		Balances(context.Context) ([]exchange.Balance, error)
	},
	instrument exchange.Instrument,
	baseline []exchange.Balance,
	price decimal.Decimal,
) error {
	for {
		balances, err := rest.Balances(ctx)
		if err == nil {
			delta := acceptanceBalanceTotal(balances, instrument.Identity.BaseAsset).
				Sub(acceptanceBalanceTotal(baseline, instrument.Identity.BaseAsset))
			if spotBalanceWithinFeeTolerance(delta, instrument.Rules.QuantityIncrement, price) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s %s balance did not return to baseline: %w", rowCode, instrument.Identity.BaseAsset, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func waitPerpAcceptanceBaseline(
	ctx context.Context,
	rowCode string,
	rest interface {
		Positions(context.Context, exchange.PositionsRequest) ([]exchange.Position, error)
	},
	instrument exchange.Instrument,
	baseline decimal.Decimal,
) error {
	tolerance := instrument.Rules.QuantityIncrement.Div(decimal.NewFromInt(2))
	for {
		positions, err := rest.Positions(ctx, exchange.PositionsRequest{Instrument: &instrument})
		if err == nil {
			current := signedAcceptancePosition(positions, instrument)
			if current.Sub(baseline).Abs().LessThanOrEqual(tolerance) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s position did not return to baseline %s: %w", rowCode, baseline, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func finalizeSpotAcceptance(
	row exchangeAcceptanceRow,
	rest spotAcceptanceCleanupClient,
	instrument exchange.Instrument,
	baseline []exchange.Balance,
	journal *ownedOrderJournal,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var cleanupErrors []error
	if err := journal.Cleanup(ctx, rest, instrument); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("cancel exact-owned orders: %w", err))
	}
	if err := cleanupSpotAcceptanceExposure(ctx, row, rest, instrument, baseline, journal); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := journal.Cleanup(ctx, rest, instrument); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("verify exact-owned orders: %w", err))
	}
	return errors.Join(cleanupErrors...)
}

func acceptanceSubscriptionTimeout(operation string) time.Duration {
	if strings.Contains(operation, "WatchCandles") {
		return 75 * time.Second
	}
	if strings.HasPrefix(operation, "BG") && strings.Contains(operation, "WatchOrderBook") {
		return 120 * time.Second
	}
	return 45 * time.Second
}

func startAcceptanceSubscriptionWitness[T any](
	parent context.Context,
	operation string,
	start func(context.Context) (exchange.Subscription[T], error),
) (*acceptanceSubscriptionWitness, error) {
	subscription, err := start(parent)
	if err != nil {
		return nil, err
	}
	if subscription == nil || subscription.ID() == "" {
		return nil, errors.New("returned an empty subscription")
	}

	activationCtx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	statuses := subscription.Status()
	streamErrors := subscription.Errors()
	pendingEvent := false
	for {
		select {
		case status, ok := <-statuses:
			if !ok {
				return nil, errors.New("status channel closed before activation")
			}
			if status.State == exchange.SubscriptionActive {
				goto active
			}
		case _, ok := <-subscription.Events():
			if !ok {
				return nil, errors.New("event channel closed before activation")
			}
			pendingEvent = true
		case streamErr, ok := <-streamErrors:
			if !ok {
				streamErrors = nil
				continue
			}
			if streamErr != nil {
				return nil, fmt.Errorf("stream error: %w", streamErr)
			}
		case <-activationCtx.Done():
			return nil, fmt.Errorf("did not activate: %w", activationCtx.Err())
		}
	}

active:
	events := subscription.Events()
	statuses = subscription.Status()
	streamErrors = subscription.Errors()
	return &acceptanceSubscriptionWitness{
		operation: operation,
		arm: func() {
			pendingEvent = false
			for {
				select {
				case _, ok := <-events:
					if !ok {
						return
					}
				default:
					return
				}
			}
		},
		waitEvent: func(ctx context.Context) error {
			if pendingEvent {
				pendingEvent = false
				return nil
			}
			for {
				select {
				case _, ok := <-events:
					if !ok {
						return errors.New("event channel closed before evidence")
					}
					return nil
				case streamErr, ok := <-streamErrors:
					if !ok {
						streamErrors = nil
						continue
					}
					if streamErr != nil {
						return fmt.Errorf("stream error: %w", streamErr)
					}
				case _, ok := <-statuses:
					if !ok {
						statuses = nil
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		},
		close: subscription.Close,
	}, nil
}

func selectAcceptanceInstrument(
	instruments []exchange.Instrument,
	product exchange.Product,
	hint string,
) (exchange.Instrument, error) {
	want := normalizeAcceptanceSymbol(hint)
	for _, instrument := range instruments {
		if instrument.Identity.Product != product {
			continue
		}
		if normalizeAcceptanceSymbol(instrument.Identity.Symbol) == want {
			return instrument, nil
		}
		if product == exchange.ProductPerp &&
			strings.EqualFold(instrument.Identity.QuoteAsset, "USDC") &&
			normalizeAcceptanceSymbol(instrument.Identity.BaseAsset+"PERP") == want {
			return instrument, nil
		}
	}
	return exchange.Instrument{}, fmt.Errorf(
		"configured %s instrument %q is not admitted by the exchange (%d instruments returned)",
		product,
		hint,
		len(instruments),
	)
}

func normalizeAcceptanceSymbol(symbol string) string {
	replacer := strings.NewReplacer("-", "", "_", "", "/", "", " ", "")
	return strings.ToUpper(replacer.Replace(symbol))
}
