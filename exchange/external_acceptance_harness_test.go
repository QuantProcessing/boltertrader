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
	if isPositivePortableOrderID(orderID) {
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

func (journal *ownedOrderJournal) Cleanup(ctx context.Context, client orderCleanupClient, instrument string) error {
	page, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: instrument, Limit: 100})
	if err != nil {
		return err
	}
	for _, order := range page.Orders {
		if journal.Owns(order) {
			if !isPositivePortableOrderID(order.OrderID) {
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
		page, err = client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: instrument, Limit: 100})
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
	price, err := executablePrice(book, side)
	if err != nil {
		return approxOrderSize{}, err
	}
	price = roundPriceToTick(price, instrument.PriceIncrement, side)
	if !price.IsPositive() {
		return approxOrderSize{}, errors.New("rounded price is not positive")
	}

	quantity := roundDownToStep(target.Div(price), instrument.QuantityIncrement)
	if quantity.LessThan(instrument.MinQuantity) {
		quantity = instrument.MinQuantity
	}
	quantity = roundDownToStep(quantity, instrument.QuantityIncrement)
	if !quantity.IsPositive() {
		return approxOrderSize{}, errors.New("rounded quantity is not positive")
	}

	notional := price.Mul(quantity)
	if instrument.MinNotional.Valid && notional.LessThan(instrument.MinNotional.Value) {
		return approxOrderSize{}, fmt.Errorf("approximately %s quote notional is below min notional %s", target, instrument.MinNotional.Value)
	}
	if notional.LessThan(target.Mul(decimal.RequireFromString("0.90"))) {
		return approxOrderSize{}, fmt.Errorf("rounded notional %s is too far below approximately %s", notional, target)
	}
	if notional.GreaterThan(target.Mul(decimal.RequireFromString("1.10"))) {
		return approxOrderSize{}, fmt.Errorf("minimum tradable notional %s is too far above approximately %s", notional, target)
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
