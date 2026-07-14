package nado

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/wsstream"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

const (
	nadoArchiveMatchesDefaultLimit = 100
	nadoArchiveMatchesMaxLimit     = 500
	nadoOrderCorrelationLimit      = 100_000
	nadoOrderCorrelationRetention  = 15 * time.Minute
)

type executionClient struct {
	rest          *sdk.Client
	provider      *instrumentProvider
	clk           clock.Clock
	productKind   enums.InstrumentKind
	accountID     string
	stream        *wsstream.Stream[contract.ExecEnvelope]
	pretrade      nadoPreTradeBackend
	reports       nadoExecutionReportBackend
	accountStream nadoAccountStreamBackend
	startMu       sync.Mutex
	started       bool
	prepared      preparedOrderCache
	correlations  nadoOrderCorrelationCache
}

func newExecutionClient(rest *sdk.Client, provider *instrumentProvider, clk clock.Clock, kind enums.InstrumentKind, accountIDs ...string) *executionClient {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	accountID := AccountIDUnified
	if len(accountIDs) > 0 && accountIDs[0] != "" {
		accountID = accountIDs[0]
	}
	c := &executionClient{
		rest:        rest,
		provider:    provider,
		clk:         clk,
		productKind: kind,
		accountID:   accountID,
		stream:      wsstream.New[contract.ExecEnvelope](256),
		prepared:    newPreparedOrderCache(128, 30*time.Second),
		correlations: newNadoOrderCorrelationCache(
			nadoOrderCorrelationLimit,
			nadoOrderCorrelationRetention,
		),
	}
	if rest != nil && rest.Signer != nil {
		c.reports = nadoSDKExecutionReportBackend{rest: rest}
		if api, err := sdk.NewWsApiClient(context.Background(), rest); err == nil {
			c.pretrade = nadoSDKPreTradeBackend{rest: rest, api: api}
		}
	}
	return c
}

func (c *executionClient) AccountID() string { return c.accountID }

func (c *executionClient) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: VenueName,
		Products: []contract.ProductCapability{{
			Kind:    selectedKind(c.productKind),
			Trading: c.pretrade != nil,
		}},
		Reports: contract.ReportCapabilities{
			SingleOrderStatus:         true,
			OpenOrders:                true,
			OpenOnlyNotFoundAmbiguous: true,
			FillHistory:               c.reports != nil,
			PositionReports:           c.reports != nil && selectedKind(c.productKind) == enums.KindPerp,
		},
		Trading:   contract.TradingCapabilities{Submit: c.pretrade != nil, Cancel: true, CancelAll: true},
		Streaming: contract.StreamCapabilities{Execution: c.accountStream != nil},
	}
}

func (c *executionClient) ValidateSubmit(req model.OrderRequest) error {
	return c.validateOrderRequest(req)
}

func (c *executionClient) validateOrderRequest(req model.OrderRequest) error {
	if req.AccountID != "" && req.AccountID != c.accountID {
		return fmt.Errorf("%w: order account %s does not match adapter account %s", ErrAccountMismatch, req.AccountID, c.accountID)
	}
	if req.InstrumentID.Kind != selectedKind(c.productKind) {
		return fmt.Errorf("nado: product %s is outside adapter scope %s: %w", req.InstrumentID.Kind, selectedKind(c.productKind), contract.ErrNotSupported)
	}
	inst, productID, err := c.instrument(req.InstrumentID)
	if err != nil {
		return err
	}
	_, err = c.orderInput(req, inst, productID)
	return err
}

func (c *executionClient) ValidatePreTrade(ctx context.Context, req model.OrderRequest, inst *model.Instrument) (contract.PreTradeLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ClientID) == "" {
		return nil, fmt.Errorf("nado: client id is required for prepared pre-trade validation")
	}
	if err := c.validateOrderRequest(req); err != nil {
		return nil, err
	}
	resolved, productID, err := c.instrument(req.InstrumentID)
	if err != nil {
		return nil, err
	}
	if inst != nil && inst.ID != resolved.ID {
		return nil, fmt.Errorf("nado: pre-trade instrument mismatch")
	}
	if c.pretrade == nil {
		return nil, fmt.Errorf("nado: pre-trade backend not configured: %w", contract.ErrNotSupported)
	}
	input, err := c.orderInput(req, resolved, productID)
	if err != nil {
		return nil, err
	}
	reservation, err := c.prepared.reserve(req.ClientID, req, c.clk.Now())
	if err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			reservation.Rollback()
		}
	}()
	sender, err := c.pretrade.Sender()
	if err != nil {
		return nil, fmt.Errorf("nado pre-trade sender: %w", err)
	}
	maxReq := sdk.MaxOrderSizeRequest{
		ProductID:    productID,
		Sender:       sender,
		PriceX18:     decimalToX18String(req.Price),
		Direction:    nadoOrderDirection(req.Side),
		ReduceOnly:   &input.ReduceOnly,
		Isolated:     &input.Isolated,
		BorrowMargin: input.BorrowMargin,
	}
	if input.SpotLeverage != nil {
		maxReq.SpotLeverage = input.SpotLeverage
	}
	maxSize, err := c.pretrade.GetMaxOrderSize(ctx, maxReq)
	if err != nil {
		return nil, fmt.Errorf("nado pre-trade max_order_size: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	maxQty, err := parseX18Required(maxSize.MaxOrderSize, "max_order_size")
	if err != nil {
		return nil, err
	}
	if req.Quantity.GreaterThan(maxQty) {
		return nil, fmt.Errorf("nado: quantity %s exceeds max_order_size %s", req.Quantity, maxQty)
	}
	prepared, err := c.pretrade.PrepareOrder(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("nado pre-trade prepare_order: %w", err)
	}
	if prepared == nil {
		return nil, fmt.Errorf("nado: prepared order is required")
	}
	if err := ctx.Err(); err != nil {
		redactPreparedOrder(prepared)
		return nil, err
	}
	lease, err := reservation.Publish(prepared, c.clk.Now())
	if err != nil {
		redactPreparedOrder(prepared)
		return nil, fmt.Errorf("nado pre-trade publish prepared order: %w", err)
	}
	published = true
	return lease, nil
}

func (c *executionClient) orderInput(req model.OrderRequest, inst *model.Instrument, productID int64) (sdk.ClientOrderInput, error) {
	input, err := orderRequestToNado(req, inst, productID)
	if err != nil {
		return sdk.ClientOrderInput{}, err
	}
	isolatedOnly, ok := c.provider.IsolatedOnly(req.InstrumentID)
	if !ok {
		return sdk.ClientOrderInput{}, fmt.Errorf("nado: missing margin capability for %s", req.InstrumentID)
	}
	if isolatedOnly {
		if req.InstrumentID.Kind != enums.KindPerp {
			return sdk.ClientOrderInput{}, fmt.Errorf("nado: isolated-only capability is invalid for %s", req.InstrumentID.Kind)
		}
		input.Isolated = true
		if !req.ReduceOnly {
			multiplier := inst.ContractMultiplier
			if !multiplier.IsPositive() {
				multiplier = decimal.NewFromInt(1)
			}
			margin := req.Price.Mul(req.Quantity).Mul(multiplier).Shift(6).Ceil().Shift(-6)
			input.IsolatedMargin, _ = margin.Float64()
			input.IsolatedMarginX6 = margin.Shift(6).BigInt()
		}
	}
	return input, nil
}

func (c *executionClient) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	return c.submit(ctx, req, true)
}

func (c *executionClient) SubmitPrepared(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	return c.submit(ctx, req, false)
}

func (c *executionClient) submit(ctx context.Context, req model.OrderRequest, allowDirectFallback bool) (*model.Order, error) {
	if err := c.validateOrderRequest(req); err != nil {
		return nil, err
	}
	if c.pretrade == nil {
		return nil, fmt.Errorf("nado: submit requires prepared pre-trade backend: %w", contract.ErrNotSupported)
	}
	if strings.TrimSpace(req.ClientID) == "" {
		return nil, fmt.Errorf("nado: client id is required for prepared submit")
	}
	entry, ok := c.prepared.consume(req.ClientID, req, c.clk.Now())
	if !ok {
		if c.prepared.isTerminal(req.ClientID, c.clk.Now()) {
			return nil, fmt.Errorf("%w: nado prepared state for client id %s is no longer usable", contract.ErrPreparedStateUnavailable, req.ClientID)
		}
		if !allowDirectFallback {
			return nil, fmt.Errorf("%w: nado prepared state for client id %s is missing or expired", contract.ErrPreparedStateUnavailable, req.ClientID)
		}
		lease, err := c.ValidatePreTrade(ctx, req, nil)
		if err != nil {
			return nil, err
		}
		defer lease.Release()
		entry, ok = c.prepared.consume(req.ClientID, req, c.clk.Now())
		if !ok {
			return nil, fmt.Errorf("nado: missing prepared state after validation")
		}
	}
	if err := ctx.Err(); err != nil {
		redactPreparedOrder(entry.order)
		return nil, err
	}
	digest := strings.TrimSpace(entry.order.Digest)
	if digest == "" {
		redactPreparedOrder(entry.order)
		return nil, fmt.Errorf("nado: prepared order digest is required")
	}
	if err := c.correlations.remember(nadoOrderCorrelation{
		accountID:    c.accountID,
		instrumentID: req.InstrumentID,
		clientID:     req.ClientID,
		venueOrderID: digest,
		request:      req,
	}, c.clk.Now()); err != nil {
		redactPreparedOrder(entry.order)
		return nil, err
	}
	resp, err := c.pretrade.ExecutePreparedOrder(ctx, entry.order)
	if err != nil {
		redactPreparedOrder(entry.order)
		return nil, fmt.Errorf("nado execute prepared order: %w", err)
	}
	if resp != nil && strings.TrimSpace(resp.Digest) != "" && !strings.EqualFold(strings.TrimSpace(resp.Digest), digest) {
		foreignDigest := strings.TrimSpace(resp.Digest)
		redactPreparedOrder(entry.order)
		return nil, fmt.Errorf("nado execute prepared order: response digest mismatch: signed=%s response=%s", digest, foreignDigest)
	}
	redactPreparedOrder(entry.order)
	return &model.Order{
		Request:      req,
		VenueOrderID: digest,
		Status:       enums.StatusNew,
		CreatedAt:    c.clk.Now(),
		UpdatedAt:    c.clk.Now(),
	}, nil
}

func (c *executionClient) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	_, productID, err := c.instrument(id)
	if err != nil {
		return err
	}
	if c.rest == nil {
		return fmt.Errorf("nado: rest client not configured: %w", contract.ErrNotSupported)
	}
	_, err = c.rest.CancelOrders(ctx, sdk.CancelOrdersInput{ProductIds: []int64{productID}, Digests: []string{venueOrderID}})
	if err == nil {
		c.correlations.markTerminalByVenueOrderID(c.accountID, id, venueOrderID, enums.StatusCanceled, c.clk.Now())
	}
	return err
}

func (c *executionClient) CancelAll(ctx context.Context, id model.InstrumentID) error {
	_, productID, err := c.instrument(id)
	if err != nil {
		return err
	}
	if c.rest == nil {
		return fmt.Errorf("nado: rest client not configured: %w", contract.ErrNotSupported)
	}
	_, err = c.rest.CancelProductOrders(ctx, []int64{productID})
	return err
}

func (c *executionClient) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	return nil, fmt.Errorf("nado: modify is not part of Story 5 adapter foundations: %w", contract.ErrNotSupported)
}

func (c *executionClient) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	inst, productID, err := c.instrument(id)
	if err != nil {
		return nil, err
	}
	if c.rest == nil {
		return nil, fmt.Errorf("nado: rest client not configured: %w", contract.ErrNotSupported)
	}
	sender, err := c.rest.Sender()
	if err != nil {
		return nil, err
	}
	orders, err := c.rest.GetAccountProductOrders(ctx, productID, sender)
	if err != nil {
		return nil, err
	}
	out := make([]model.Order, 0, len(orders.Orders))
	for _, order := range orders.Orders {
		converted, err := orderFromNadoRecord(order, inst.ID, c.accountID)
		if err != nil {
			return nil, err
		}
		if correlation, ok := c.correlations.byVenueOrderID(c.accountID, inst.ID, converted.VenueOrderID, c.clk.Now()); ok {
			converted.Request.ClientID = correlation.clientID
		}
		out = append(out, converted)
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	if query.InstrumentID.Symbol == "" {
		return nil, fmt.Errorf("nado: order status reports require an instrument in Story 5: %w", contract.ErrNotSupported)
	}
	orders, err := c.OpenOrders(ctx, query.InstrumentID)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.OrderStatusReport, 0, len(orders))
	for _, order := range orders {
		if !fillInTimeRange(order.UpdatedAt, query.Since, query.Until) {
			continue
		}
		if !model.OrderMatchesStatusQuery(order, query) {
			continue
		}
		report := model.OrderStatusReport{ReportID: model.ReportID(fmt.Sprintf("%s:%s:order:%s", VenueName, c.accountID, order.VenueOrderID)), Venue: VenueName, AccountID: c.accountID, Order: order, ReportedAt: now}
		if err := report.Validate(); err != nil {
			return nil, err
		}
		out = append(out, report)
	}
	return out, nil
}

func (c *executionClient) GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	instrumentID := query.InstrumentID
	venueOrderID := strings.TrimSpace(query.VenueOrderID)
	clientID := query.ClientID
	var correlation *nadoOrderCorrelation
	if strings.TrimSpace(clientID) != "" {
		found, ok := c.correlations.byClientID(c.accountID, instrumentID, clientID, c.clk.Now())
		if !ok {
			return nil, nil
		}
		if venueOrderID != "" && !strings.EqualFold(venueOrderID, found.venueOrderID) {
			return nil, fmt.Errorf("nado: order query client id %s maps to digest %s, not %s", clientID, found.venueOrderID, venueOrderID)
		}
		instrumentID = found.instrumentID
		venueOrderID = found.venueOrderID
		correlationCopy := found
		correlation = &correlationCopy
	}
	if venueOrderID == "" {
		reports, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{
			InstrumentID: instrumentID,
			AccountID:    query.AccountID,
			OpenOnly:     true,
		})
		if err != nil || len(reports) == 0 {
			return nil, err
		}
		return &reports[0], nil
	}
	inst, productID, err := c.instrument(instrumentID)
	if err != nil {
		return nil, err
	}
	if c.rest == nil {
		return nil, fmt.Errorf("nado: exact order status requires REST client: %w", contract.ErrNotSupported)
	}
	if correlation == nil {
		if recovered, ok := c.correlations.byVenueOrderID(c.accountID, inst.ID, venueOrderID, c.clk.Now()); ok {
			correlationCopy := recovered
			correlation = &correlationCopy
			clientID = recovered.clientID
		}
	}
	openOrders, err := c.OpenOrders(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	for _, order := range openOrders {
		if !strings.EqualFold(strings.TrimSpace(order.VenueOrderID), venueOrderID) {
			continue
		}
		if clientID != "" {
			order.Request.ClientID = clientID
		}
		return c.orderStatusReport(order)
	}
	archive, err := c.rest.GetOrdersByDigests(ctx, []string{venueOrderID})
	if err != nil {
		return nil, err
	}
	if archive == nil {
		return nil, fmt.Errorf("nado: exact archive order response is required")
	}
	if len(archive.Orders) == 0 {
		return nil, nil
	}
	if len(archive.Orders) != 1 {
		return nil, fmt.Errorf("nado: exact archive order query returned %d records for one digest", len(archive.Orders))
	}
	record := archive.Orders[0]
	if record.ProductID != productID {
		return nil, fmt.Errorf("nado: exact archive order product mismatch: got %d want %d", record.ProductID, productID)
	}
	if !strings.EqualFold(strings.TrimSpace(record.Digest), venueOrderID) {
		return nil, fmt.Errorf("nado: exact archive order digest mismatch: got %s want %s", record.Digest, venueOrderID)
	}
	expectedSender, err := c.rest.Sender()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(record.Subaccount), strings.TrimSpace(expectedSender)) {
		return nil, fmt.Errorf("nado: exact archive order subaccount mismatch")
	}
	var request *model.OrderRequest
	knownTerminal := enums.StatusUnknown
	if correlation != nil {
		requestCopy := correlation.request
		request = &requestCopy
		knownTerminal = correlation.terminalStatus
	}
	order, err := archiveOrderFromNadoRecord(record, inst.ID, c.accountID, request, knownTerminal)
	if err != nil {
		return nil, err
	}
	return c.orderStatusReport(order)
}

func (c *executionClient) orderStatusReport(order model.Order) (*model.OrderStatusReport, error) {
	report := &model.OrderStatusReport{
		ReportID:   model.ReportID(fmt.Sprintf("%s:%s:order:%s", VenueName, c.accountID, order.VenueOrderID)),
		Venue:      VenueName,
		AccountID:  c.accountID,
		Order:      order,
		ReportedAt: c.clk.Now(),
	}
	if err := report.Validate(); err != nil {
		return nil, err
	}
	if isNadoTerminalOrderStatus(order.Status) {
		c.correlations.markTerminalByVenueOrderID(c.accountID, order.Request.InstrumentID, order.VenueOrderID, order.Status, c.clk.Now())
	}
	return report, nil
}

func isNadoTerminalOrderStatus(status enums.OrderStatus) bool {
	switch status {
	case enums.StatusFilled, enums.StatusCanceled, enums.StatusRejected, enums.StatusExpired:
		return true
	default:
		return false
	}
}

func (c *executionClient) GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error) {
	reports, _, err := c.generateFillReports(ctx, query)
	return reports, err
}

func (c *executionClient) generateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, bool, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, false, nil
	}
	if c.reports == nil {
		return nil, false, fmt.Errorf("nado: fill reports require report backend: %w", contract.ErrNotSupported)
	}
	productIDs, instByProduct, err := c.reportProducts(query.InstrumentID)
	if err != nil {
		return nil, false, err
	}
	sender, err := c.reports.Sender()
	if err != nil {
		return nil, false, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = nadoArchiveMatchesDefaultLimit
	}
	matches, err := c.reports.GetMatches(ctx, sender, productIDs, limit)
	if err != nil {
		return nil, false, err
	}
	if matches == nil {
		return nil, false, fmt.Errorf("nado: archive matches response is required")
	}
	limitReached := len(matches.Matches) >= limit
	txProducts := matchTxProducts(matches.Txs)
	txTimestamps := matchTxTimestamps(matches.Txs)
	now := c.clk.Now()
	out := make([]model.FillReport, 0, len(matches.Matches))
	for _, match := range matches.Matches {
		if strings.TrimSpace(match.Timestamp) == "" {
			match.Timestamp = txTimestamps[match.SubmissionIdx]
		}
		productID, ok := productIDForMatch(match, txProducts)
		if !ok {
			return nil, limitReached, fmt.Errorf("nado: archive match %s has ambiguous product identity", match.Digest)
		}
		inst, ok := instByProduct[productID]
		if !ok {
			return nil, limitReached, fmt.Errorf("nado: archive match %s product %d outside report scope", match.Digest, productID)
		}
		fill, err := fillFromNadoMatch(match, inst.ID, c.accountID, inst.Settle)
		if err != nil {
			return nil, limitReached, err
		}
		if correlation, ok := c.correlations.byVenueOrderID(c.accountID, inst.ID, fill.VenueOrderID, c.clk.Now()); ok {
			fill.ClientID = correlation.clientID
		}
		if !fillInTimeRange(fill.Timestamp, query.Since, query.Until) || !model.FillMatchesReportQuery(fill, query) {
			continue
		}
		report := model.FillReport{ReportID: model.ReportID(fmt.Sprintf("%s:%s:%s", VenueName, c.accountID, fill.TradeID)), Venue: VenueName, AccountID: c.accountID, Fill: fill, ReportedAt: now}
		if err := report.Validate(); err != nil {
			return nil, limitReached, err
		}
		out = append(out, report)
	}
	return out, limitReached, nil
}

func (c *executionClient) GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error) {
	if selectedKind(c.productKind) != enums.KindPerp {
		return nil, fmt.Errorf("nado: position reports require Perp scope: %w", contract.ErrNotSupported)
	}
	if query.AccountID != "" && query.AccountID != c.accountID {
		return nil, nil
	}
	if c.reports == nil {
		return nil, fmt.Errorf("nado: position reports require report backend: %w", contract.ErrNotSupported)
	}
	snapshot, err := c.reports.GetAccountSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	positions, err := positionsFromNado(snapshot.Account, c.provider, c.accountID, snapshot.ReceivedAt)
	if err != nil {
		return nil, err
	}
	now := c.clk.Now()
	out := make([]model.PositionReport, 0, len(positions))
	for _, position := range positions {
		if query.InstrumentID.Symbol != "" && position.InstrumentID != query.InstrumentID {
			continue
		}
		if !fillInTimeRange(position.UpdatedAt, query.Since, query.Until) {
			continue
		}
		report := model.PositionReport{ReportID: model.ReportID(model.PositionReportKey(c.accountID, position)), Venue: VenueName, AccountID: c.accountID, Position: position, ReportedAt: now}
		if err := report.Validate(); err != nil {
			return nil, err
		}
		out = append(out, report)
	}
	return out, nil
}

func (c *executionClient) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	if query.AccountID != "" && query.AccountID != c.accountID {
		return model.NewExecutionMassStatus(VenueName, query.AccountID, c.clk.Now()), nil
	}
	mass := model.NewExecutionMassStatus(VenueName, c.accountID, c.clk.Now())
	mass.ClientID = query.ClientID
	mass.Lookback = query.Lookback
	mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_ONLY", Message: "adapter can generate open-order status only; absent closed orders are ambiguous"})
	if c.rest == nil {
		mass.Partial = true
		mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_UNAVAILABLE", Message: "open-order status requires a configured REST client"})
	} else {
		for _, inst := range c.provider.All() {
			if inst == nil || inst.ID.Kind != selectedKind(c.productKind) {
				continue
			}
			orders, err := c.GenerateOrderStatusReports(ctx, model.OrderStatusReportQuery{InstrumentID: inst.ID, AccountID: c.accountID, ClientID: query.ClientID, OpenOnly: true, Since: query.Since, Until: query.Until})
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
				mass.Partial = true
				mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "OPEN_ORDERS_PARTIAL", Message: err.Error()})
				continue
			}
			for _, report := range orders {
				if err := mass.AddOrderReport(report); err != nil {
					return nil, err
				}
			}
		}
	}
	if query.IncludeFills {
		fills, limitReached, err := c.generateFillReports(ctx, model.FillReportQuery{
			AccountID: c.accountID,
			ClientID:  query.ClientID,
			Since:     query.Since,
			Until:     query.Until,
			Limit:     nadoArchiveMatchesMaxLimit,
		})
		if err != nil {
			mass.Partial = true
			mass.Warnings = append(mass.Warnings, model.ReportWarning{Code: "FILL_REPORTS_PARTIAL", Message: err.Error()})
		} else {
			for _, report := range fills {
				if err := mass.AddFillReport(report); err != nil {
					return nil, err
				}
			}
			if limitReached {
				mass.Partial = true
				mass.Warnings = append(mass.Warnings, model.ReportWarning{
					Code:    "FILL_REPORTS_LIMIT_REACHED",
					Message: "fill-history query reached the 500-record archive limit; recovered fills may be incomplete",
				})
			}
		}
	}
	if query.IncludePositions {
		if selectedKind(c.productKind) != enums.KindPerp || c.reports == nil {
			return mass, nil
		}
		positions, err := c.GeneratePositionReports(ctx, model.PositionReportQuery{AccountID: c.accountID, Since: query.Since, Until: query.Until})
		if err != nil {
			return nil, err
		}
		for _, report := range positions {
			if err := mass.AddPositionReport(report); err != nil {
				return nil, err
			}
		}
	}
	return mass, nil
}

func (c *executionClient) Events() <-chan contract.ExecEnvelope { return c.stream.C() }
func (c *executionClient) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.accountStream == nil {
		return nil
	}
	c.startMu.Lock()
	if c.started {
		c.startMu.Unlock()
		return c.accountStream.Connect()
	}
	if err := c.accountStream.SubscribeOrders(nil, c.handleOrderUpdate); err != nil {
		c.startMu.Unlock()
		return err
	}
	if err := c.accountStream.SubscribeFills(nil, c.handleFill); err != nil {
		c.startMu.Unlock()
		return err
	}
	c.started = true
	c.startMu.Unlock()
	return c.accountStream.Connect()
}

func (c *executionClient) Close() error {
	if closer, ok := c.pretrade.(interface{ Close() }); ok {
		closer.Close()
	}
	if c.accountStream != nil {
		c.accountStream.Close()
	}
	c.stream.Close()
	return nil
}

func (c *executionClient) Connected() bool {
	return c.accountStream != nil && c.accountStream.IsConnected()
}

func (c *executionClient) Reconnect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.accountStream == nil {
		return fmt.Errorf("nado: account stream backend not configured: %w", contract.ErrNotSupported)
	}
	return c.accountStream.Connect()
}

func (c *executionClient) instrument(id model.InstrumentID) (*model.Instrument, int64, error) {
	if id.Kind != selectedKind(c.productKind) {
		return nil, 0, fmt.Errorf("nado: product %s is outside adapter scope %s: %w", id.Kind, selectedKind(c.productKind), contract.ErrNotSupported)
	}
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return nil, 0, fmt.Errorf("%w: %s", ErrUnknownInstrument, id)
	}
	productID, ok := c.provider.ProductID(id)
	if !ok {
		return nil, 0, fmt.Errorf("%w: missing product identity for %s", ErrUnknownInstrument, id)
	}
	return inst, productID, nil
}

func (c *executionClient) handleOrderUpdate(update *sdk.OrderUpdate) {
	if update == nil {
		return
	}
	if update.Digest == "" {
		return
	}
	id, ok := c.provider.ResolveProductID(update.ProductId)
	if !ok || id.Kind != selectedKind(c.productKind) {
		return
	}
	remaining, err := parseX18Required(update.Amount, "order update remaining amount")
	if err != nil {
		return
	}
	ts := timeFromString(update.Timestamp)
	if ts.IsZero() {
		return
	}
	status, ok := orderUpdateStatus(update.Reason, remaining)
	if !ok {
		return
	}
	if update.Reason == sdk.OrderReasonPlaced && remaining.IsZero() {
		return
	}
	side := enums.SideUnknown
	if remaining.IsPositive() {
		side = enums.SideBuy
	} else if remaining.IsNegative() {
		side = enums.SideSell
	}
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    c.accountID,
			InstrumentID: id,
			Side:         side,
			Quantity:     decimal.Zero,
			PositionSide: enums.PosNet,
		},
		VenueOrderID: update.Digest,
		Status:       status,
		UpdatedAt:    ts,
	}
	if isNadoTerminalOrderStatus(status) {
		c.correlations.markTerminalByVenueOrderID(c.accountID, id, update.Digest, status, c.clk.Now())
	}
	c.stream.Emit(contract.NewExecEnvelopeWithMeta(contract.OrderEvent{Order: order}, nadoEventMeta("exec", "order", c.accountID, fmt.Sprint(update.ProductId), update.Digest, update.Timestamp, string(update.Reason))))
}

func orderUpdateStatus(reason sdk.OrderUpdateReason, remaining decimal.Decimal) (enums.OrderStatus, bool) {
	switch reason {
	case sdk.OrderReasonPlaced:
		return enums.StatusNew, true
	case sdk.OrderReasonFilled:
		if remaining.IsZero() {
			return enums.StatusFilled, true
		}
		return enums.StatusPartiallyFilled, true
	case sdk.OrderReasonCancelled:
		return enums.StatusCanceled, true
	default:
		return enums.StatusUnknown, false
	}
}

func (c *executionClient) handleFill(fill *sdk.Fill) {
	if fill == nil {
		return
	}
	id, ok := c.provider.ResolveProductID(fill.ProductId)
	if !ok || id.Kind != selectedKind(c.productKind) {
		return
	}
	inst, ok := c.provider.Instrument(id)
	if !ok {
		return
	}
	converted, err := fillFromNado(*fill, id, c.accountID, inst.Settle)
	if err != nil {
		return
	}
	c.stream.Emit(contract.NewExecEnvelopeWithMeta(contract.FillEvent{Fill: converted}, nadoEventMeta("exec", "fill", c.accountID, fmt.Sprint(fill.ProductId), fill.OrderDigest, fill.SubmissionIdx, fill.Timestamp)))
}

type nadoPreTradeBackend interface {
	Sender() (string, error)
	GetMaxOrderSize(context.Context, sdk.MaxOrderSizeRequest) (*sdk.MaxOrderSizeResponse, error)
	PrepareOrder(context.Context, sdk.ClientOrderInput) (*sdk.PreparedOrder, error)
	ExecutePreparedOrder(context.Context, *sdk.PreparedOrder) (*sdk.PlaceOrderResponse, error)
}

type nadoExecutionReportBackend interface {
	Sender() (string, error)
	GetMatches(context.Context, string, []int64, int) (*sdk.ArchiveMatchesResponse, error)
	GetAccountSnapshot(context.Context) (*sdk.AccountSnapshot, error)
}

type nadoAccountStreamBackend interface {
	Connect() error
	Close()
	IsConnected() bool
	SubscribeOrders(*int64, func(*sdk.OrderUpdate)) error
	SubscribeFills(*int64, func(*sdk.Fill)) error
	SubscribePositions(*int64, func(*sdk.PositionChange)) error
}

type nadoSDKExecutionReportBackend struct {
	rest *sdk.Client
}

func (b nadoSDKExecutionReportBackend) Sender() (string, error) {
	return b.rest.Sender()
}

func (b nadoSDKExecutionReportBackend) GetMatches(ctx context.Context, subaccount string, productIDs []int64, limit int) (*sdk.ArchiveMatchesResponse, error) {
	return b.rest.GetMatches(ctx, subaccount, productIDs, limit)
}

func (b nadoSDKExecutionReportBackend) GetAccountSnapshot(ctx context.Context) (*sdk.AccountSnapshot, error) {
	return b.rest.GetAccountSnapshot(ctx)
}

type nadoSDKPreTradeBackend struct {
	rest *sdk.Client
	api  *sdk.WsApiClient
}

func (b nadoSDKPreTradeBackend) Sender() (string, error) {
	return b.rest.Sender()
}

func (b nadoSDKPreTradeBackend) Connect() error {
	return b.api.Connect()
}

func (b nadoSDKPreTradeBackend) Close() {
	b.api.Close()
}

func (b nadoSDKPreTradeBackend) GetMaxOrderSize(ctx context.Context, req sdk.MaxOrderSizeRequest) (*sdk.MaxOrderSizeResponse, error) {
	return b.rest.GetMaxOrderSize(ctx, req)
}

func (b nadoSDKPreTradeBackend) PrepareOrder(ctx context.Context, input sdk.ClientOrderInput) (*sdk.PreparedOrder, error) {
	return b.api.PrepareOrder(ctx, input)
}

func (b nadoSDKPreTradeBackend) ExecutePreparedOrder(ctx context.Context, order *sdk.PreparedOrder) (*sdk.PlaceOrderResponse, error) {
	return b.api.ExecutePreparedOrder(ctx, order)
}

type nadoOrderCorrelation struct {
	accountID      string
	instrumentID   model.InstrumentID
	clientID       string
	venueOrderID   string
	request        model.OrderRequest
	terminalStatus enums.OrderStatus
	expires        time.Time
}

type nadoOrderCorrelationCache struct {
	mu       sync.Mutex
	byClient map[string]nadoOrderCorrelation
	byVenue  map[string]string
	limit    int
	ttl      time.Duration
}

func newNadoOrderCorrelationCache(limit int, ttl time.Duration) nadoOrderCorrelationCache {
	return nadoOrderCorrelationCache{
		byClient: make(map[string]nadoOrderCorrelation),
		byVenue:  make(map[string]string),
		limit:    limit,
		ttl:      ttl,
	}
}

func (c *nadoOrderCorrelationCache) remember(correlation nadoOrderCorrelation, now time.Time) error {
	correlation.venueOrderID = strings.TrimSpace(correlation.venueOrderID)
	if correlation.accountID == "" || correlation.instrumentID.Symbol == "" || strings.TrimSpace(correlation.clientID) == "" || correlation.venueOrderID == "" {
		return fmt.Errorf("nado: complete order correlation identity is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredLocked(now)
	if existing, ok := c.byClient[correlation.clientID]; ok {
		if existing.accountID != correlation.accountID || existing.instrumentID != correlation.instrumentID || !strings.EqualFold(existing.venueOrderID, correlation.venueOrderID) {
			return fmt.Errorf("nado: client id %s already maps to a different signed order", correlation.clientID)
		}
	}
	venueKey := strings.ToLower(correlation.venueOrderID)
	if existingClientID, ok := c.byVenue[venueKey]; ok && existingClientID != correlation.clientID {
		return fmt.Errorf("nado: signed order digest %s already maps to client id %s", correlation.venueOrderID, existingClientID)
	}
	if c.limit > 0 {
		if _, replacing := c.byClient[correlation.clientID]; !replacing && len(c.byClient) >= c.limit {
			return fmt.Errorf("nado: order correlation capacity %d reached", c.limit)
		}
	}
	// Active Nado orders must retain this mapping indefinitely: the venue does
	// not echo the local client ID, so expiring it would destroy the only stable
	// client↔digest identity for long-lived GTC/GTX orders. A bounded terminal
	// retention window starts only after authoritative terminal evidence.
	correlation.expires = time.Time{}
	c.byClient[correlation.clientID] = correlation
	c.byVenue[venueKey] = correlation.clientID
	return nil
}

func (c *nadoOrderCorrelationCache) markTerminalByVenueOrderID(accountID string, instrumentID model.InstrumentID, venueOrderID string, status enums.OrderStatus, now time.Time) {
	if !isNadoTerminalOrderStatus(status) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredLocked(now)
	venueKey := strings.ToLower(strings.TrimSpace(venueOrderID))
	clientID, ok := c.byVenue[venueKey]
	if !ok {
		return
	}
	correlation, ok := c.byClient[clientID]
	if !ok || (accountID != "" && correlation.accountID != accountID) || (instrumentID.Symbol != "" && correlation.instrumentID != instrumentID) {
		return
	}
	correlation.terminalStatus = status
	correlation.expires = now.Add(c.ttl)
	c.byClient[clientID] = correlation
}

func (c *nadoOrderCorrelationCache) byClientID(accountID string, instrumentID model.InstrumentID, clientID string, now time.Time) (nadoOrderCorrelation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredLocked(now)
	correlation, ok := c.byClient[clientID]
	if !ok || (accountID != "" && correlation.accountID != accountID) || (instrumentID.Symbol != "" && correlation.instrumentID != instrumentID) {
		return nadoOrderCorrelation{}, false
	}
	return correlation, true
}

func (c *nadoOrderCorrelationCache) byVenueOrderID(accountID string, instrumentID model.InstrumentID, venueOrderID string, now time.Time) (nadoOrderCorrelation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredLocked(now)
	clientID, ok := c.byVenue[strings.ToLower(strings.TrimSpace(venueOrderID))]
	if !ok {
		return nadoOrderCorrelation{}, false
	}
	correlation, ok := c.byClient[clientID]
	if !ok || (accountID != "" && correlation.accountID != accountID) || (instrumentID.Symbol != "" && correlation.instrumentID != instrumentID) {
		return nadoOrderCorrelation{}, false
	}
	return correlation, true
}

func (c *nadoOrderCorrelationCache) evictExpiredLocked(now time.Time) {
	for clientID, correlation := range c.byClient {
		if !correlation.expires.IsZero() && !correlation.expires.After(now) {
			delete(c.byClient, clientID)
			venueKey := strings.ToLower(correlation.venueOrderID)
			if mappedClientID, ok := c.byVenue[venueKey]; ok && mappedClientID == clientID {
				delete(c.byVenue, venueKey)
			}
		}
	}
}

type preparedOrderCache struct {
	mu             sync.Mutex
	items          map[string]*preparedOrderEntry
	terminalStates map[string]preparedTerminal
	ttl            time.Duration
	maxSize        int
}

type preparedState uint8

const (
	preparedStateReserved preparedState = iota
	preparedStatePrepared
	preparedStateConsumed
	preparedStateReleased
)

type preparedOrderEntry struct {
	clientID string
	req      model.OrderRequest
	order    *sdk.PreparedOrder
	expires  time.Time
	state    preparedState
}

type preparedTerminal struct {
	state   preparedState
	expires time.Time
}

type nadoPreparedLease struct {
	cache    *preparedOrderCache
	clientID string
}

type nadoPreparedReservation struct {
	cache    *preparedOrderCache
	clientID string
}

func newPreparedOrderCache(maxSize int, ttl time.Duration) preparedOrderCache {
	return preparedOrderCache{items: make(map[string]*preparedOrderEntry), terminalStates: make(map[string]preparedTerminal), maxSize: maxSize, ttl: ttl}
}

func (c *preparedOrderCache) reserve(clientID string, req model.OrderRequest, now time.Time) (*nadoPreparedReservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked(now)
	if _, ok := c.terminalStates[clientID]; ok {
		return nil, fmt.Errorf("nado: prepared state for client id %s is terminal", clientID)
	}
	if _, exists := c.items[clientID]; exists {
		return nil, fmt.Errorf("nado: prepared state for client id %s already exists", clientID)
	}
	if c.maxSize > 0 && len(c.items)+len(c.terminalStates) >= c.maxSize && len(c.items) == 0 {
		c.evictOldestTerminalLocked()
	}
	if c.maxSize > 0 && len(c.items)+len(c.terminalStates) >= c.maxSize {
		return nil, fmt.Errorf("nado: prepared state capacity %d reached", c.maxSize)
	}
	c.items[clientID] = &preparedOrderEntry{clientID: clientID, req: req, expires: now.Add(c.ttl), state: preparedStateReserved}
	return &nadoPreparedReservation{cache: c, clientID: clientID}, nil
}

func (c *preparedOrderCache) consume(clientID string, req model.OrderRequest, now time.Time) (*preparedOrderEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked(now)
	entry := c.items[clientID]
	if entry == nil || entry.state != preparedStatePrepared || !samePreparedRequest(entry.req, req) {
		return nil, false
	}
	delete(c.items, clientID)
	entry.state = preparedStateConsumed
	c.terminalStates[clientID] = preparedTerminal{state: preparedStateConsumed, expires: entry.expires}
	c.enforceTerminalBoundLocked()
	return entry, true
}

func (c *preparedOrderCache) release(clientID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(clientID, preparedStateReleased)
}

func (c *preparedOrderCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

func (c *preparedOrderCache) isTerminal(clientID string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked(now)
	_, ok := c.terminalStates[clientID]
	return ok
}

func (c *preparedOrderCache) evictLocked(now time.Time) {
	for key, item := range c.items {
		if !item.expires.After(now) {
			c.removeLocked(key, preparedStateReleased, now.Add(c.ttl))
		}
	}
	for key, terminal := range c.terminalStates {
		if !terminal.expires.After(now) {
			delete(c.terminalStates, key)
		}
	}
}

func (c *preparedOrderCache) removeLocked(clientID string, state preparedState, terminalExpiry ...time.Time) {
	entry := c.items[clientID]
	if entry == nil {
		return
	}
	delete(c.items, clientID)
	entry.state = state
	expires := entry.expires
	if len(terminalExpiry) > 0 {
		expires = terminalExpiry[0]
	}
	c.terminalStates[clientID] = preparedTerminal{state: state, expires: expires}
	c.enforceTerminalBoundLocked()
	redactPreparedOrder(entry.order)
}

func (c *preparedOrderCache) enforceTerminalBoundLocked() {
	if c.maxSize <= 0 {
		return
	}
	for len(c.terminalStates) > c.maxSize {
		var oldestKey string
		var oldest time.Time
		for key, terminal := range c.terminalStates {
			if oldestKey == "" || terminal.expires.Before(oldest) {
				oldestKey, oldest = key, terminal.expires
			}
		}
		if oldestKey == "" {
			return
		}
		delete(c.terminalStates, oldestKey)
	}
}

func (c *preparedOrderCache) evictOldestTerminalLocked() {
	var oldestKey string
	var oldest time.Time
	for key, terminal := range c.terminalStates {
		if oldestKey == "" || terminal.expires.Before(oldest) {
			oldestKey, oldest = key, terminal.expires
		}
	}
	if oldestKey != "" {
		delete(c.terminalStates, oldestKey)
	}
}

func (r *nadoPreparedReservation) Publish(order *sdk.PreparedOrder, now time.Time) (contract.PreTradeLease, error) {
	if r == nil || r.cache == nil || r.clientID == "" {
		redactPreparedOrder(order)
		return nil, fmt.Errorf("nado: prepared reservation is required")
	}
	r.cache.mu.Lock()
	defer r.cache.mu.Unlock()
	r.cache.evictLocked(now)
	entry := r.cache.items[r.clientID]
	if entry == nil || entry.state != preparedStateReserved {
		redactPreparedOrder(order)
		return nil, fmt.Errorf("nado: prepared reservation for client id %s is not active", r.clientID)
	}
	entry.order = order
	entry.expires = now.Add(r.cache.ttl)
	entry.state = preparedStatePrepared
	return &nadoPreparedLease{cache: r.cache, clientID: r.clientID}, nil
}

func (r *nadoPreparedReservation) Rollback() {
	if r == nil || r.cache == nil || r.clientID == "" {
		return
	}
	r.cache.mu.Lock()
	defer r.cache.mu.Unlock()
	entry := r.cache.items[r.clientID]
	if entry == nil || entry.state != preparedStateReserved {
		return
	}
	delete(r.cache.items, r.clientID)
}

func (l *nadoPreparedLease) Release() {
	if l == nil || l.cache == nil || l.clientID == "" {
		return
	}
	l.cache.release(l.clientID)
}

func (c *executionClient) preparedLen() int {
	return c.prepared.len()
}

func samePreparedRequest(a, b model.OrderRequest) bool {
	return a.AccountID == b.AccountID &&
		a.InstrumentID == b.InstrumentID &&
		a.ClientID == b.ClientID &&
		a.Side == b.Side &&
		a.Type == b.Type &&
		a.TIF == b.TIF &&
		a.PositionSide == b.PositionSide &&
		a.ReduceOnly == b.ReduceOnly &&
		a.Venue == nil && b.Venue == nil &&
		a.Quantity.Equal(b.Quantity) &&
		a.Price.Equal(b.Price) &&
		a.TriggerPrice.Equal(b.TriggerPrice) &&
		a.ActivationPrice.Equal(b.ActivationPrice) &&
		a.TrailingOffsetBps.Equal(b.TrailingOffsetBps)
}

func nadoOrderDirection(side enums.OrderSide) sdk.OrderDirection {
	if side == enums.SideSell {
		return sdk.OrderDirectionShort
	}
	return sdk.OrderDirectionLong
}

func decimalToX18String(value decimal.Decimal) string {
	return value.Shift(18).StringFixed(0)
}

func redactPreparedOrder(order *sdk.PreparedOrder) {
	if order == nil {
		return
	}
	order.Signature = ""
	order.Digest = ""
	order.EncodedOrder = ""
	order.Request = nil
	order.Tx = sdk.TxOrder{}
}

func (c *executionClient) reportProducts(id model.InstrumentID) ([]int64, map[int64]*model.Instrument, error) {
	if id.Symbol != "" {
		inst, productID, err := c.instrument(id)
		if err != nil {
			return nil, nil, err
		}
		return []int64{productID}, map[int64]*model.Instrument{productID: inst}, nil
	}
	productIDs := make([]int64, 0)
	instByProduct := make(map[int64]*model.Instrument)
	for _, inst := range c.provider.All() {
		if inst == nil || inst.ID.Kind != selectedKind(c.productKind) {
			continue
		}
		productID, ok := c.provider.ProductID(inst.ID)
		if !ok {
			return nil, nil, fmt.Errorf("%w: missing product identity for %s", ErrUnknownInstrument, inst.ID)
		}
		productIDs = append(productIDs, productID)
		instByProduct[productID] = inst
	}
	if len(productIDs) == 0 {
		return nil, nil, fmt.Errorf("nado: no report products for %s", selectedKind(c.productKind))
	}
	return productIDs, instByProduct, nil
}

func fillFromNadoMatch(match sdk.Match, id model.InstrumentID, accountID, feeCurrency string) (model.Fill, error) {
	if strings.TrimSpace(match.Digest) == "" {
		return model.Fill{}, fmt.Errorf("nado: match order digest is required")
	}
	if strings.TrimSpace(match.SubmissionIdx) == "" {
		return model.Fill{}, fmt.Errorf("nado: match submission index is required")
	}
	price, err := parseX18Required(match.Order.PriceX18, "match price")
	if err != nil {
		return model.Fill{}, err
	}
	if !price.IsPositive() {
		return model.Fill{}, fmt.Errorf("nado: match price must be positive")
	}
	qty, err := parseX18Required(match.BaseFilled, "match base filled")
	if err != nil {
		return model.Fill{}, err
	}
	if qty.IsZero() {
		return model.Fill{}, fmt.Errorf("nado: match base filled must be non-zero")
	}
	qty = qty.Abs()
	fee, err := parseX18Required(match.Fee, "match fee")
	if err != nil {
		return model.Fill{}, err
	}
	ts := timeFromString(match.Timestamp)
	if ts.IsZero() {
		return model.Fill{}, fmt.Errorf("nado: match timestamp %q is invalid", match.Timestamp)
	}
	side := enums.SideBuy
	amount, err := parseDecimalRequired(match.Order.Amount, "match order amount")
	if err != nil {
		return model.Fill{}, err
	}
	if amount.IsNegative() {
		side = enums.SideSell
	}
	return model.Fill{
		AccountID:    accountID,
		InstrumentID: id,
		VenueOrderID: match.Digest,
		TradeID:      match.SubmissionIdx,
		Side:         side,
		Liquidity:    enums.LiqUnknown,
		Price:        price,
		Quantity:     qty,
		Fee:          fee,
		FeeCurrency:  feeCurrency,
		Timestamp:    ts,
	}, nil
}

func matchTxProducts(txs []sdk.Tx) map[string]int64 {
	out := make(map[string]int64, len(txs))
	for _, tx := range txs {
		if tx.SubmissionIdx != "" && tx.TxInfo.MatchOrders.ProductId != 0 {
			out[tx.SubmissionIdx] = int64(tx.TxInfo.MatchOrders.ProductId)
		}
	}
	return out
}

func matchTxTimestamps(txs []sdk.Tx) map[string]string {
	out := make(map[string]string, len(txs))
	for _, tx := range txs {
		if tx.SubmissionIdx != "" && strings.TrimSpace(tx.Timestamp) != "" {
			out[tx.SubmissionIdx] = tx.Timestamp
		}
	}
	return out
}

func productIDForMatch(match sdk.Match, txProducts map[string]int64) (int64, bool) {
	if match.PostBalance.Base.Perp != nil {
		return match.PostBalance.Base.Perp.ProductID, true
	}
	if match.PostBalance.Base.Spot != nil {
		return match.PostBalance.Base.Spot.ProductID, true
	}
	if match.PreBalance.Base.Perp != nil {
		return match.PreBalance.Base.Perp.ProductID, true
	}
	if match.PreBalance.Base.Spot != nil {
		return match.PreBalance.Base.Spot.ProductID, true
	}
	if productID, ok := txProducts[match.SubmissionIdx]; ok {
		return productID, true
	}
	return 0, false
}

func fillInTimeRange(ts, since, until time.Time) bool {
	if !since.IsZero() && ts.Before(since) {
		return false
	}
	if !until.IsZero() && ts.After(until) {
		return false
	}
	return true
}
