package lighter

import (
	"context"
	"fmt"
	"hash/fnv"
	"strconv"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdk "github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

type executionClient struct {
	rest         *sdk.Client
	provider     *registry
	clk          clock.Clock
	accountIndex int64
	accountID    string
	stream       *wsstream.Stream[contract.ExecEnvelope]

	mu              sync.RWMutex
	clientIndexToID map[int64]string
	ordersByVenueID map[string]model.Order
}

func newExecutionClient(rest *sdk.Client, provider *registry, clk clock.Clock, accountIndex int64, accountIDs ...string) *executionClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	accountID := model.AccountIDLighterDefault
	if len(accountIDs) > 0 && accountIDs[0] != "" {
		accountID = accountIDs[0]
	}
	return &executionClient{
		rest:            rest,
		provider:        provider,
		clk:             clk,
		accountIndex:    accountIndex,
		accountID:       accountID,
		stream:          wsstream.New[contract.ExecEnvelope](256),
		clientIndexToID: make(map[int64]string),
		ordersByVenueID: make(map[string]model.Order),
	}
}

func (c *executionClient) AccountID() string { return c.accountID }

func (c *executionClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: venueName,
		Products: []contract.ProductCapability{
			{Kind: enums.KindSpot, Trading: true},
			{Kind: enums.KindPerp, Trading: true},
		},
		Reports: contract.ReportCapabilities{
			OpenOrders:                true,
			OpenOnlyNotFoundAmbiguous: true,
			PositionReports:           true,
		},
		Trading: contract.TradingCapabilities{Submit: true, Cancel: true, CancelAll: true, Modify: true},
	}
}

func (c *executionClient) ValidateSubmit(req model.OrderRequest) error {
	_, _, err := c.placeOrderRequest(req)
	return err
}

func (c *executionClient) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	wire, clientIndex, err := c.placeOrderRequest(req)
	if err != nil {
		return nil, err
	}
	if req.AccountID == "" {
		req.AccountID = c.accountID
	}
	c.rememberClientIndex(clientIndex, req.ClientID)
	res, err := c.rest.PlaceOrder(ctx, wire)
	if err != nil {
		return nil, err
	}
	if res.Code != 0 && res.Code != 200 {
		return &model.Order{Request: req, Status: enums.StatusRejected, RejectReason: res.Message, UpdatedAt: c.clk.Now()}, fmt.Errorf("lighter: place order rejected code=%d message=%s", res.Code, res.Message)
	}
	var order *model.Order
	if wire.TimeInForce == sdk.OrderTimeInForceImmediateOrCancel {
		order, err = c.waitForInactiveOrder(ctx, req, wire.MarketId, clientIndex)
	} else {
		order, err = c.waitForOpenOrder(ctx, req, wire.MarketId, clientIndex)
	}
	if err != nil {
		return nil, err
	}
	c.emitSubmitFill(*order)
	return order, nil
}

func (c *executionClient) placeOrderRequest(req model.OrderRequest) (sdk.CreateOrderRequest, int64, error) {
	if req.AccountID != "" && req.AccountID != c.accountID {
		return sdk.CreateOrderRequest{}, 0, fmt.Errorf("lighter: order account %s does not match adapter account %s", req.AccountID, c.accountID)
	}
	inst, ok := c.provider.Instrument(req.InstrumentID)
	if !ok || inst.AssetIndex == nil {
		return sdk.CreateOrderRequest{}, 0, fmt.Errorf("lighter: unknown instrument %s: %w", req.InstrumentID, errs.ErrSymbolNotFound)
	}
	if req.PositionSide != enums.PosNet {
		return sdk.CreateOrderRequest{}, 0, fmt.Errorf("lighter: hedge position side is not supported: %w", errs.ErrNotSupported)
	}
	if req.InstrumentID.Kind == enums.KindSpot && req.ReduceOnly {
		return sdk.CreateOrderRequest{}, 0, fmt.Errorf("lighter: reduce-only spot orders are not supported: %w", errs.ErrNotSupported)
	}
	if req.Type != enums.TypeLimit {
		return sdk.CreateOrderRequest{}, 0, fmt.Errorf("lighter: only limit orders are implemented: %w", errs.ErrNotSupported)
	}
	tif, err := timeInForceToLighter(req.TIF)
	if err != nil {
		return sdk.CreateOrderRequest{}, 0, err
	}
	priceTicks, err := decimalToTicks(req.Price, inst.PriceTick, "price")
	if err != nil {
		return sdk.CreateOrderRequest{}, 0, err
	}
	if priceTicks > int64(^uint32(0)) {
		return sdk.CreateOrderRequest{}, 0, fmt.Errorf("lighter: price ticks %d overflow uint32", priceTicks)
	}
	baseTicks, err := decimalToTicks(req.Quantity, inst.SizeStep, "quantity")
	if err != nil {
		return sdk.CreateOrderRequest{}, 0, err
	}
	if inst.MinQty.IsPositive() && req.Quantity.LessThan(inst.MinQty) {
		return sdk.CreateOrderRequest{}, 0, fmt.Errorf("lighter: quantity %s below min %s: %w", req.Quantity, inst.MinQty, errs.ErrNotSupported)
	}
	notional := req.Quantity.Mul(req.Price)
	if inst.MinNotional.IsPositive() && notional.LessThan(inst.MinNotional) {
		return sdk.CreateOrderRequest{}, 0, fmt.Errorf("lighter: notional %s below min %s: %w", notional, inst.MinNotional, errs.ErrNotSupported)
	}
	isAsk, err := sideToLighter(req.Side)
	if err != nil {
		return sdk.CreateOrderRequest{}, 0, err
	}
	clientIndex := clientOrderIndex(req.ClientID)
	reduceOnly := uint32(0)
	if req.ReduceOnly {
		reduceOnly = 1
	}
	return sdk.CreateOrderRequest{
		MarketId:      *inst.AssetIndex,
		Price:         uint32(priceTicks),
		BaseAmount:    baseTicks,
		IsAsk:         isAsk,
		OrderType:     sdk.OrderTypeLimit,
		ClientOrderId: clientIndex,
		TimeInForce:   tif,
		ReduceOnly:    reduceOnly,
		TriggerPrice:  sdk.NilTriggerPrice,
		OrderExpiry:   orderExpiryForLighter(req.TIF, c.clk.Now()),
	}, clientIndex, nil
}

func sideToLighter(side enums.OrderSide) (uint32, error) {
	switch side {
	case enums.SideBuy:
		return 0, nil
	case enums.SideSell:
		return 1, nil
	default:
		return 0, fmt.Errorf("lighter: unsupported side %v: %w", side, errs.ErrNotSupported)
	}
}

func sideFromLighter(o *sdk.Order) enums.OrderSide {
	if o == nil {
		return enums.SideUnknown
	}
	if o.IsAsk || o.Side == "sell" || o.Side == "SELL" {
		return enums.SideSell
	}
	return enums.SideBuy
}

func timeInForceToLighter(tif enums.TimeInForce) (uint32, error) {
	switch tif {
	case enums.TifUnknown, enums.TifGTC:
		return sdk.OrderTimeInForceGoodTillTime, nil
	case enums.TifIOC:
		return sdk.OrderTimeInForceImmediateOrCancel, nil
	case enums.TifGTX:
		return sdk.OrderTimeInForcePostOnly, nil
	default:
		return 0, fmt.Errorf("lighter: unsupported TIF %v: %w", tif, errs.ErrNotSupported)
	}
}

func orderExpiryForLighter(tif enums.TimeInForce, now time.Time) int64 {
	switch tif {
	case enums.TifIOC:
		return 0
	default:
		return now.Add(28 * 24 * time.Hour).UnixMilli()
	}
}

func decimalToTicks(value, step decimal.Decimal, label string) (int64, error) {
	if !value.IsPositive() {
		return 0, fmt.Errorf("lighter: %s must be positive", label)
	}
	if !step.IsPositive() {
		return 0, fmt.Errorf("lighter: %s step is not positive", label)
	}
	ticks := value.Div(step)
	if !ticks.Equal(ticks.Truncate(0)) {
		return 0, fmt.Errorf("lighter: %s %s is not aligned to step %s: %w", label, value, step, errs.ErrNotSupported)
	}
	return ticks.IntPart(), nil
}

func clientOrderIndex(clientID string) int64 {
	if clientID == "" {
		clientID = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(clientID))
	v := int64(uint32(h.Sum64()) & 0x7fff_ffff)
	if v == 0 {
		return 1
	}
	return v
}

func (c *executionClient) waitForOpenOrder(ctx context.Context, req model.OrderRequest, marketID int, clientIndex int64) (*model.Order, error) {
	deadline, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		callCtx, cancelCall := context.WithTimeout(deadline, 15*time.Second)
		orders, err := c.openOrdersForMarket(callCtx, marketID)
		cancelCall()
		if err != nil {
			lastErr = err
		}
		for i := range orders {
			if orders[i].ClientOrderIndex != clientIndex {
				continue
			}
			order := c.orderFromLighter(&orders[i])
			if order.Request.ClientID == "" {
				order.Request.ClientID = req.ClientID
			}
			order.Request.AccountID = c.accountID
			order.Request.PositionSide = enums.PosNet
			order = c.rememberOrder(order)
			return &order, nil
		}
		select {
		case <-deadline.Done():
			if order, err := c.findInactiveOrder(ctx, req, marketID, clientIndex); err == nil {
				return order, nil
			}
			if lastErr != nil {
				return nil, fmt.Errorf("lighter: submitted order not observed in active orders before timeout: %w", lastErr)
			}
			return nil, fmt.Errorf("lighter: submitted order client_index=%d not observed in active orders before timeout", clientIndex)
		case <-ticker.C:
		}
	}
}

func (c *executionClient) waitForInactiveOrder(ctx context.Context, req model.OrderRequest, marketID int, clientIndex int64) (*model.Order, error) {
	deadline, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		order, err := c.findInactiveOrder(deadline, req, marketID, clientIndex)
		if err == nil {
			return order, nil
		}
		lastErr = err
		select {
		case <-deadline.Done():
			return nil, fmt.Errorf("lighter: submitted order not observed in inactive orders before timeout: %w", lastErr)
		case <-ticker.C:
		}
	}
}

func (c *executionClient) findInactiveOrder(ctx context.Context, req model.OrderRequest, marketID int, clientIndex int64) (*model.Order, error) {
	callCtx, cancelCall := context.WithTimeout(ctx, 15*time.Second)
	defer cancelCall()
	orders, err := c.rest.GetInactiveOrders(callCtx, &marketID, 100)
	if err != nil {
		return nil, err
	}
	for _, raw := range orders.Orders {
		if raw == nil || raw.ClientOrderIndex != clientIndex {
			continue
		}
		order := c.orderFromLighter(raw)
		if order.Request.ClientID == "" {
			order.Request.ClientID = req.ClientID
		}
		order.Request.AccountID = c.accountID
		order.Request.PositionSide = enums.PosNet
		order = c.rememberOrder(order)
		return &order, nil
	}
	return nil, fmt.Errorf("lighter: inactive order client_index=%d not found", clientIndex)
}

func (c *executionClient) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	inst, ok := c.provider.Instrument(id)
	if !ok || inst.AssetIndex == nil {
		return fmt.Errorf("lighter: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	orderID, err := strconv.ParseInt(venueOrderID, 10, 64)
	if err != nil {
		return fmt.Errorf("lighter: invalid venue order id %q: %w", venueOrderID, err)
	}
	res, err := c.rest.CancelOrder(ctx, sdk.CancelOrderRequest{MarketId: *inst.AssetIndex, OrderId: orderID})
	if err != nil {
		return err
	}
	if res.Code != 0 && res.Code != 200 {
		return fmt.Errorf("lighter: cancel rejected code=%d message=%s", res.Code, res.Message)
	}
	c.emitCanceled(id, venueOrderID)
	return nil
}

func (c *executionClient) CancelAll(ctx context.Context, id model.InstrumentID) error {
	orders, err := c.OpenOrders(ctx, id)
	if err != nil {
		return err
	}
	for _, order := range orders {
		if err := c.Cancel(ctx, id, order.VenueOrderID); err != nil {
			return err
		}
	}
	return nil
}

func (c *executionClient) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok || inst.AssetIndex == nil {
		return nil, fmt.Errorf("lighter: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	orderID, err := strconv.ParseInt(venueOrderID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("lighter: invalid venue order id %q: %w", venueOrderID, err)
	}
	if newPrice.IsZero() && newQty.IsZero() {
		return nil, fmt.Errorf("lighter: modify requires price or quantity: %w", errs.ErrNotSupported)
	}
	order := c.snapshotOrder(venueOrderID)
	price := order.Request.Price
	qty := order.Request.Quantity
	if !newPrice.IsZero() {
		price = newPrice
	}
	if !newQty.IsZero() {
		qty = newQty
	}
	priceTicks, err := decimalToTicks(price, inst.PriceTick, "price")
	if err != nil {
		return nil, err
	}
	baseTicks, err := decimalToTicks(qty, inst.SizeStep, "quantity")
	if err != nil {
		return nil, err
	}
	res, err := c.rest.ModifyOrder(ctx, sdk.ModifyOrderRequest{
		MarketId:   *inst.AssetIndex,
		OrderIndex: orderID,
		BaseAmount: baseTicks,
		Price:      uint32(priceTicks),
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 && res.Code != 200 {
		return nil, fmt.Errorf("lighter: modify rejected code=%d message=%s", res.Code, res.Message)
	}
	order.Request.Price = price
	order.Request.Quantity = qty
	order.UpdatedAt = c.clk.Now()
	order.Status = enums.StatusNew
	order = c.rememberOrder(order)
	return &order, nil
}

func (c *executionClient) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	inst, ok := c.provider.Instrument(id)
	if !ok || inst.AssetIndex == nil {
		return nil, fmt.Errorf("lighter: unknown instrument %s: %w", id, errs.ErrSymbolNotFound)
	}
	orders, err := c.openOrdersForMarket(ctx, *inst.AssetIndex)
	if err != nil {
		return nil, err
	}
	out := make([]model.Order, 0, len(orders))
	for i := range orders {
		order := c.orderFromLighter(&orders[i])
		order = c.rememberOrder(order)
		out = append(out, order)
	}
	return out, nil
}

func (c *executionClient) openOrdersForMarket(ctx context.Context, marketID int) ([]sdk.Order, error) {
	res, err := c.rest.GetAccountActiveOrders(ctx, marketID)
	if err != nil {
		return nil, err
	}
	out := make([]sdk.Order, 0, len(res.Orders))
	for _, order := range res.Orders {
		if order == nil {
			continue
		}
		out = append(out, *order)
	}
	return out, nil
}

func (c *executionClient) orderFromLighter(o *sdk.Order) model.Order {
	if o == nil {
		return model.Order{}
	}
	inst, _ := c.provider.byMarket(o.MarketIndex)
	clientID := ""
	if o.ClientOrderIndex != 0 {
		clientID = c.lookupClientID(o.ClientOrderIndex)
	}
	if clientID == "" {
		clientID = o.ClientOrderId
	}
	if clientID == "" && o.ClientOrderIndex != 0 {
		clientID = strconv.FormatInt(o.ClientOrderIndex, 10)
	}
	venueOrderID := strconv.FormatInt(o.OrderIndex, 10)
	if venueOrderID == "0" && o.OrderId != "" {
		venueOrderID = o.OrderId
	}
	created := parseMillisOrMicros(firstNonZeroInt64(o.CreatedAt, o.Timestamp, o.TransactionTime))
	updated := parseMillisOrMicros(firstNonZeroInt64(o.UpdatedAt, o.TransactionTime, o.Timestamp))
	return model.Order{
		Request: model.OrderRequest{
			AccountID:    c.accountID,
			InstrumentID: inst.ID,
			ClientID:     clientID,
			Side:         sideFromLighter(o),
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     dec(o.InitialBaseAmount),
			Price:        dec(o.Price),
			PositionSide: enums.PosNet,
			ReduceOnly:   o.ReduceOnly,
		},
		VenueOrderID: venueOrderID,
		Status:       statusFromLighter(o.Status),
		FilledQty:    dec(o.FilledBaseAmount),
		AvgFillPrice: avgFillPrice(o),
		CreatedAt:    created,
		UpdatedAt:    updated,
	}
}

func statusFromLighter(status sdk.OrderStatus) enums.OrderStatus {
	switch status {
	case sdk.OrderStatusOpen, sdk.OrderStatusPending, sdk.OrderStatusInProgress:
		return enums.StatusNew
	case sdk.OrderStatusFilled:
		return enums.StatusFilled
	case sdk.OrderStatusCanceled, sdk.OrderStatusCanceledExpired, sdk.OrderStatusCanceledReduceOnly, sdk.OrderStatusCanceledLiquidation:
		return enums.StatusCanceled
	case sdk.OrderStatusCanceledPostOnly, sdk.OrderStatusCanceledInvalidBalance, sdk.OrderStatusRejected:
		return enums.StatusRejected
	case sdk.OrderStatusPartiallyFilled:
		return enums.StatusPartiallyFilled
	default:
		return enums.StatusUnknown
	}
}

func avgFillPrice(o *sdk.Order) decimal.Decimal {
	filledBase := dec(o.FilledBaseAmount)
	if !filledBase.IsPositive() {
		return decimal.Zero
	}
	return dec(o.FilledQuoteAmount).Div(filledBase)
}

func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	accountID, ok := c.scopedReportAccountID(query.AccountID)
	if !ok {
		return nil, nil
	}
	query.AccountID = accountID
	marketIDs := c.marketIDsForQuery(query.InstrumentID)
	now := c.clk.Now()
	out := make([]model.OrderStatusReport, 0)
	for _, marketID := range marketIDs {
		orders, err := c.openOrdersForMarket(ctx, marketID)
		if err != nil {
			return nil, err
		}
		for i := range orders {
			order := c.orderFromLighter(&orders[i])
			if !model.OrderMatchesStatusQuery(order, query) {
				continue
			}
			order = c.rememberOrder(order)
			out = append(out, model.OrderStatusReport{Venue: venueName, AccountID: accountID, Order: order, ReportedAt: now})
		}
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{
		InstrumentID: query.InstrumentID,
		AccountID:    query.AccountID,
		ClientID:     query.ClientID,
		VenueOrderID: query.VenueOrderID,
		OpenOnly:     true,
	})
	if err != nil || len(reports) == 0 {
		return nil, err
	}
	return &reports[0], nil
}

func (c *executionClient) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	if _, ok := c.scopedReportAccountID(query.AccountID); !ok {
		return nil, nil
	}
	return nil, fmt.Errorf("lighter: fill report history is not implemented: %w", errs.ErrNotSupported)
}

func (c *executionClient) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	accountID, ok := c.scopedReportAccountID(query.AccountID)
	if !ok {
		return nil, nil
	}
	acct := newAccountClient(c.rest, c.provider, c.clk, c.accountIndex, c.accountID)
	positions, err := acct.Positions(ctx)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.PositionReport, 0, len(positions))
	for _, pos := range positions {
		if query.InstrumentID.Symbol != "" && pos.InstrumentID != query.InstrumentID {
			continue
		}
		out = append(out, model.PositionReport{Venue: venueName, AccountID: accountID, Position: pos, ReportedAt: now})
	}
	return out, nil
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	accountID, ok := c.scopedReportAccountID(query.AccountID)
	if !ok {
		return model.NewExecutionMassStatus(venueName, query.AccountID, c.clk.Now()), nil
	}
	mass := model.NewExecutionMassStatus(venueName, accountID, c.clk.Now())
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	mass.Partial = true
	mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_ONLY", Message: "adapter can generate open-order status only; absent closed orders are ambiguous"})
	reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{AccountID: accountID, ClientID: query.ClientID, OpenOnly: true})
	if err != nil {
		return nil, err
	}
	for _, report := range reports {
		if err := mass.AddOrderReport(report); err != nil {
			return nil, err
		}
	}
	return mass, nil
}

func (c *executionClient) scopedReportAccountID(accountID string) (string, bool) {
	if accountID == "" {
		return c.accountID, true
	}
	return c.accountID, accountID == c.accountID
}

func (c *executionClient) marketIDsForQuery(id model.InstrumentID) []int {
	if id.Symbol != "" {
		if inst, ok := c.provider.Instrument(id); ok && inst.AssetIndex != nil {
			return []int{*inst.AssetIndex}
		}
		return nil
	}
	out := make([]int, 0, len(c.provider.byMarketID))
	for marketID := range c.provider.byMarketID {
		out = append(out, marketID)
	}
	return out
}

func (c *executionClient) rememberClientIndex(index int64, clientID string) {
	if index == 0 || clientID == "" {
		return
	}
	c.mu.Lock()
	c.clientIndexToID[index] = clientID
	c.mu.Unlock()
}

func (c *executionClient) lookupClientID(index int64) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.clientIndexToID[index]
}

func (c *executionClient) rememberOrder(order model.Order) model.Order {
	if order.VenueOrderID == "" {
		return order
	}
	c.mu.Lock()
	c.ordersByVenueID[order.VenueOrderID] = order
	c.mu.Unlock()
	return order
}

func (c *executionClient) snapshotOrder(venueOrderID string) model.Order {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ordersByVenueID[venueOrderID]
}

func (c *executionClient) emitCanceled(id model.InstrumentID, venueOrderID string) {
	order := c.snapshotOrder(venueOrderID)
	if order.VenueOrderID == "" {
		order = model.Order{Request: model.OrderRequest{AccountID: c.accountID, InstrumentID: id, PositionSide: enums.PosNet}, VenueOrderID: venueOrderID}
	}
	order.Status = enums.StatusCanceled
	order.UpdatedAt = c.clk.Now()
	order = c.rememberOrder(order)
	c.stream.Emit(contract.NewExecEnvelopeWithMeta(contract.OrderEvent{Order: order}, contract.EventMeta{
		Source: contract.SourceAdapterREST,
		Flags:  contract.EventFlagSynthetic,
	}))
}

// Lighter private order/fill streaming is not wired into this adapter and fill
// history is unsupported. A submit response that already contains executions
// is therefore the sole fill source and must enter the normal event path once.
func (c *executionClient) emitSubmitFill(order model.Order) {
	if !order.FilledQty.IsPositive() || !order.AvgFillPrice.IsPositive() {
		return
	}
	timestamp := order.UpdatedAt
	if timestamp.IsZero() {
		timestamp = order.CreatedAt
	}
	if timestamp.IsZero() {
		timestamp = c.clk.Now()
	}
	fill := model.Fill{
		AccountID:    order.Request.AccountID,
		InstrumentID: order.Request.InstrumentID,
		VenueOrderID: order.VenueOrderID,
		ClientID:     order.Request.ClientID,
		TradeID: fmt.Sprintf(
			"inferred-submit:%s:%s:%s:%s:%s:%s:%s",
			venueName,
			order.Request.AccountID,
			order.Request.InstrumentID.String(),
			order.Request.ClientID,
			order.VenueOrderID,
			order.FilledQty.String(),
			order.AvgFillPrice.String(),
		),
		Side:      order.Request.Side,
		Price:     order.AvgFillPrice,
		Quantity:  order.FilledQty,
		Timestamp: timestamp,
	}
	c.stream.Emit(contract.NewExecEnvelopeWithMeta(contract.FillEvent{Fill: fill}, contract.EventMeta{
		Source: contract.SourceAdapterREST,
		Flags:  contract.EventFlagSynthetic,
	}))
}

func parseMillisOrMicros(ts int64) time.Time {
	if ts == 0 {
		return time.Time{}
	}
	if ts > 9_999_999_999_999 {
		return time.UnixMicro(ts)
	}
	return time.UnixMilli(ts)
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func (c *executionClient) Events() <-chan contract.ExecEnvelope { return c.stream.C() }

func (c *executionClient) Close() error {
	c.stream.Close()
	return nil
}
