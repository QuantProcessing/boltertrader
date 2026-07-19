package factoryclient

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

func (client *gateSpotClient) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := gateContext(ctx, client.meta, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := request.Validate(exchange.ProductSpot); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	order := gateSpotPlaceOrder(request)
	if request.Type == exchange.OrderTypeMarket && request.Side == exchange.SideBuy {
		book, err := client.sdk.GetSpotOrderBook(ctx, request.Instrument, 20, false)
		if err != nil {
			return exchange.OrderAcknowledgement{}, gateNormalizeErr(client.meta, "PlaceOrder", err)
		}
		if err := gateValidateSpotOrderBook(client.meta, "PlaceOrder", *book); err != nil {
			return exchange.OrderAcknowledgement{}, err
		}
		order.Amount = request.Quantity.Mul(gateDecimal(string(book.Asks[0][0]))).String()
	}
	row, err := client.sdk.CreateSpotOrder(ctx, order)
	if err != nil {
		return gateCommandErr(client.meta, exchange.OrderOperationPlace, request.Instrument, "", request.ClientOrderID, err)
	}
	if err := gateValidateSpotOrders(client.meta, "PlaceOrder", []gate.Order{*row}); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return gateAckValidate(gateSpotAck(client.meta, exchange.OrderOperationPlace, request.Type, *row))
}

func (client *gatePerpClient) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := gateContext(ctx, client.meta, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := request.Validate(exchange.ProductPerp); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	multiplier, err := client.gateContractMultiplier(ctx, "PlaceOrder", request.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	contracts := request.Quantity.Div(multiplier)
	if !contracts.IsPositive() || !contracts.Equal(contracts.Truncate(0)) {
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: "PlaceOrder", SafeMessage: "gate futures quantity must align with the contract multiplier"})
	}
	order := gateFuturesPlaceOrder(request, contracts.IntPart())
	row, err := client.sdk.CreateFuturesOrder(ctx, client.settle, order)
	if err != nil {
		return gateCommandErr(client.meta, exchange.OrderOperationPlace, request.Instrument, "", request.ClientOrderID, err)
	}
	if err := gateValidateFuturesOrders(client.meta, "PlaceOrder", []gate.FuturesOrder{*row}); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return gateAckValidate(gateFuturesAck(client.meta, exchange.OrderOperationPlace, request.Type, *row, multiplier))
}

func (client *gateSpotClient) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := gateContext(ctx, client.meta, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := validateGateCancel(client.meta, request); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	row, err := client.sdk.CancelSpotOrder(ctx, request.OrderID, request.Instrument)
	if err != nil {
		return gateCommandErr(client.meta, exchange.OrderOperationCancel, request.Instrument, request.OrderID, "", err)
	}
	if err := gateValidateSpotOrders(client.meta, "CancelOrder", []gate.Order{*row}); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return gateAckValidate(gateSpotAck(client.meta, exchange.OrderOperationCancel, gateOrderType(*row), *row))
}

func (client *gatePerpClient) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := gateContext(ctx, client.meta, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := validateGateCancel(client.meta, request); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	id, err := parseGateOrderID(client.meta, "CancelOrder", request.OrderID)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	multiplier, err := client.gateContractMultiplier(ctx, "CancelOrder", request.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	row, err := client.sdk.CancelFuturesOrder(ctx, client.settle, id)
	if err != nil {
		return gateCommandErr(client.meta, exchange.OrderOperationCancel, request.Instrument, request.OrderID, "", err)
	}
	if err := gateValidateFuturesOrders(client.meta, "CancelOrder", []gate.FuturesOrder{*row}); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return gateAckValidate(gateFuturesAck(client.meta, exchange.OrderOperationCancel, gateFuturesOrderType(*row), *row, multiplier))
}

func (client *gateSpotClient) OpenOrders(ctx context.Context, request exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := gateContext(ctx, client.meta, "OpenOrders"); err != nil {
		return exchange.OrderPage{}, err
	}
	if err := gateRejectCursor(client.meta, "OpenOrders", request.Cursor); err != nil {
		return exchange.OrderPage{}, err
	}
	rows, err := client.sdk.ListSpotOpenOrders(ctx, request.Instrument)
	if err != nil {
		return exchange.OrderPage{}, gateNormalizeErr(client.meta, "OpenOrders", err)
	}
	if err := gateValidateSpotOrders(client.meta, "OpenOrders", rows); err != nil {
		return exchange.OrderPage{}, err
	}
	return boundedOrderPage(gateSpotOrders(rows), request.Limit, request.Cursor), nil
}

func (client *gatePerpClient) OpenOrders(ctx context.Context, request exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := gateContext(ctx, client.meta, "OpenOrders"); err != nil {
		return exchange.OrderPage{}, err
	}
	if err := gateRejectCursor(client.meta, "OpenOrders", request.Cursor); err != nil {
		return exchange.OrderPage{}, err
	}
	rows, err := client.sdk.ListFuturesOpenOrders(ctx, client.settle, request.Instrument)
	if err != nil {
		return exchange.OrderPage{}, gateNormalizeErr(client.meta, "OpenOrders", err)
	}
	if err := gateValidateFuturesOrders(client.meta, "OpenOrders", rows); err != nil {
		return exchange.OrderPage{}, err
	}
	orders, err := client.gateFuturesOrders(ctx, "OpenOrders", rows)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return boundedOrderPage(orders, request.Limit, request.Cursor), nil
}

func (client *gateSpotClient) OrderHistory(ctx context.Context, request exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := gateContext(ctx, client.meta, "OrderHistory"); err != nil {
		return exchange.OrderPage{}, err
	}
	if err := gateValidateHistoryRequest(client.meta, "OrderHistory", request.Instrument, request.Cursor, request.Start, request.End); err != nil {
		return exchange.OrderPage{}, err
	}
	rows, err := client.sdk.ListSpotOrderHistory(ctx, request.Instrument, request.Start, request.End, request.Limit)
	if err != nil {
		return exchange.OrderPage{}, gateNormalizeErr(client.meta, "OrderHistory", err)
	}
	if err := gateValidateSpotOrders(client.meta, "OrderHistory", rows); err != nil {
		return exchange.OrderPage{}, err
	}
	return boundedOrderPage(gateSpotOrders(rows), request.Limit, request.Cursor), nil
}

func (client *gatePerpClient) OrderHistory(ctx context.Context, request exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := gateContext(ctx, client.meta, "OrderHistory"); err != nil {
		return exchange.OrderPage{}, err
	}
	if err := gateValidateHistoryRequest(client.meta, "OrderHistory", request.Instrument, request.Cursor, request.Start, request.End); err != nil {
		return exchange.OrderPage{}, err
	}
	rows, err := client.sdk.ListFuturesOrderHistory(ctx, client.settle, request.Instrument, request.Start, request.End, request.Limit)
	if err != nil {
		return exchange.OrderPage{}, gateNormalizeErr(client.meta, "OrderHistory", err)
	}
	if err := gateValidateFuturesOrders(client.meta, "OrderHistory", rows); err != nil {
		return exchange.OrderPage{}, err
	}
	orders, err := client.gateFuturesOrders(ctx, "OrderHistory", rows)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return boundedOrderPage(orders, request.Limit, request.Cursor), nil
}

func (client *gateSpotClient) Fills(ctx context.Context, request exchange.FillsRequest) (exchange.FillPage, error) {
	if err := gateContext(ctx, client.meta, "Fills"); err != nil {
		return exchange.FillPage{}, err
	}
	if err := gateValidateFillsRequest(client.meta, request); err != nil {
		return exchange.FillPage{}, err
	}
	rows, err := client.sdk.ListSpotMyTrades(ctx, request.Instrument, request.OrderID, request.Limit)
	if err != nil {
		return exchange.FillPage{}, gateNormalizeErr(client.meta, "Fills", err)
	}
	if err := gateValidateSpotFills(client.meta, "Fills", rows); err != nil {
		return exchange.FillPage{}, err
	}
	return exchange.FillPage{Fills: gateSpotFills(rows), Page: exchange.PageInfo{Cursor: request.Cursor, Limit: request.Limit, WindowStart: request.Start, WindowEnd: request.End}}, nil
}

func (client *gatePerpClient) Fills(ctx context.Context, request exchange.FillsRequest) (exchange.FillPage, error) {
	if err := gateContext(ctx, client.meta, "Fills"); err != nil {
		return exchange.FillPage{}, err
	}
	if err := gateValidateFillsRequest(client.meta, request); err != nil {
		return exchange.FillPage{}, err
	}
	rows, err := client.sdk.ListMyFuturesTradesFiltered(ctx, client.settle, request.Instrument, request.OrderID, request.Limit)
	if err != nil {
		return exchange.FillPage{}, gateNormalizeErr(client.meta, "Fills", err)
	}
	if err := gateValidateFuturesFills(client.meta, "Fills", rows); err != nil {
		return exchange.FillPage{}, err
	}
	fills, err := client.gateFuturesFills(ctx, "Fills", rows)
	if err != nil {
		return exchange.FillPage{}, err
	}
	return exchange.FillPage{Fills: fills, Page: exchange.PageInfo{Cursor: request.Cursor, Limit: request.Limit, WindowStart: request.Start, WindowEnd: request.End}}, nil
}

func (client *gateSpotClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	if err := gateContext(ctx, client.meta, "Balances"); err != nil {
		return nil, err
	}
	mode, err := client.sdk.GetUnifiedMode(ctx)
	if err != nil {
		return nil, gateNormalizeErr(client.meta, "Balances", err)
	}
	switch strings.ToLower(strings.TrimSpace(mode.Mode)) {
	case "multi_currency", "portfolio", "single_currency":
		account, err := client.sdk.GetUnifiedAccount(ctx, "")
		if err != nil {
			return nil, gateNormalizeErr(client.meta, "Balances", err)
		}
		if err := gateValidateUnifiedBalances(client.meta, "Balances", account.Balances); err != nil {
			return nil, err
		}
		return gateUnifiedBalances(account.Balances), nil
	case "classic":
	default:
		return nil, gateMalformed(client.meta, "Balances", "unknown gate account mode")
	}
	rows, err := client.sdk.ListSpotAccounts(ctx, "")
	if err != nil {
		return nil, gateNormalizeErr(client.meta, "Balances", err)
	}
	if err := gateValidateSpotAccounts(client.meta, "Balances", rows); err != nil {
		return nil, err
	}
	return gateSpotBalances(rows), nil
}

func (client *gateSpotClient) SpotAccount(ctx context.Context) (exchange.SpotAccount, error) {
	balances, err := client.Balances(ctx)
	if err != nil {
		return exchange.SpotAccount{}, err
	}
	return exchange.SpotAccount{Balances: balances}, nil
}

func (client *gatePerpClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	account, err := client.PerpAccount(ctx)
	if err != nil {
		return nil, err
	}
	return account.Balances, nil
}

func (client *gatePerpClient) PerpAccount(ctx context.Context) (exchange.PerpAccount, error) {
	if err := gateContext(ctx, client.meta, "PerpAccount"); err != nil {
		return exchange.PerpAccount{}, err
	}
	row, err := client.sdk.GetFuturesAccount(ctx, client.settle)
	if err != nil {
		return exchange.PerpAccount{}, gateNormalizeErr(client.meta, "PerpAccount", err)
	}
	if err := gateValidateFuturesAccount(client.meta, "PerpAccount", *row); err != nil {
		return exchange.PerpAccount{}, err
	}
	total := gateDecimal(row.Total)
	available := gateDecimal(row.Available)
	return exchange.PerpAccount{
		Balances:      []exchange.Balance{{Asset: row.Currency, Available: available, Locked: total.Sub(available), Total: total}},
		Equity:        exchange.OptionalDecimal{Value: total, Valid: row.Total != ""},
		Available:     exchange.OptionalDecimal{Value: available, Valid: row.Available != ""},
		MarginUsed:    gateOptionalDecimal(row.PositionMargin),
		UnrealizedPnL: gateOptionalDecimal(row.UnrealisedPNL),
	}, nil
}

func (client *gatePerpClient) Positions(ctx context.Context, request exchange.PositionsRequest) ([]exchange.Position, error) {
	if err := gateContext(ctx, client.meta, "Positions"); err != nil {
		return nil, err
	}
	rows, err := client.sdk.ListPositions(ctx, client.settle, false)
	if err != nil {
		return nil, gateNormalizeErr(client.meta, "Positions", err)
	}
	if err := gateValidatePositions(client.meta, "Positions", rows); err != nil {
		return nil, err
	}
	out := make([]exchange.Position, 0, len(rows))
	for _, row := range rows {
		if request.Instrument != "" && row.Contract != request.Instrument {
			continue
		}
		side := exchange.SideBuy
		size := row.Size
		if size < 0 {
			side = exchange.SideSell
			size = -size
		}
		multiplier, err := client.gateContractMultiplier(ctx, "Positions", row.Contract)
		if err != nil {
			return nil, err
		}
		out = append(out, exchange.Position{
			Instrument:       row.Contract,
			Side:             side,
			Quantity:         decimal.NewFromInt(size).Mul(multiplier),
			EntryPrice:       gateDecimal(row.EntryPrice),
			MarkPrice:        gateDecimal(row.MarkPrice),
			UnrealizedPnL:    gateDecimal(row.UnrealisedPNL),
			LiquidationPrice: gateOptionalDecimal(row.LiqPrice),
			Leverage:         gateOptionalDecimal(row.Leverage),
			MarginUsed:       gateOptionalDecimal(row.Margin),
		})
	}
	return out, nil
}

func (client *gatePerpClient) FundingRate(ctx context.Context, request exchange.FundingRateRequest) (exchange.FundingRate, error) {
	if err := gateContext(ctx, client.meta, "FundingRate"); err != nil {
		return exchange.FundingRate{}, err
	}
	contract, err := client.sdk.GetFuturesContract(ctx, client.settle, request.Instrument)
	if err != nil {
		return exchange.FundingRate{}, gateNormalizeErr(client.meta, "FundingRate", err)
	}
	if err := gateValidateDecimal(client.meta, "FundingRate", "funding rate", contract.FundingRate); err != nil {
		return exchange.FundingRate{}, err
	}
	if err := gateValidateDecimal(client.meta, "FundingRate", "mark price", contract.MarkPrice); err != nil {
		return exchange.FundingRate{}, err
	}
	next := time.Unix(int64(contract.FundingNextApply), 0).UTC()
	return exchange.FundingRate{Instrument: contract.Name, Rate: gateDecimal(contract.FundingRate), MarkPrice: gateOptionalDecimal(contract.MarkPrice), NextFundingTime: next}, nil
}

func (client *gatePerpClient) FundingRateHistory(ctx context.Context, request exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	if err := gateContext(ctx, client.meta, "FundingRateHistory"); err != nil {
		return exchange.FundingRatePage{}, err
	}
	if err := gateValidateHistoryRequest(client.meta, "FundingRateHistory", request.Instrument, request.Cursor, request.Start, request.End); err != nil {
		return exchange.FundingRatePage{}, err
	}
	rows, err := client.sdk.ListFuturesFundingRateHistory(ctx, client.settle, request.Instrument, request.Start, request.End, request.Limit)
	if err != nil {
		return exchange.FundingRatePage{}, gateNormalizeErr(client.meta, "FundingRateHistory", err)
	}
	out := make([]exchange.FundingRate, 0, len(rows))
	for _, row := range rows {
		if err := gateValidateDecimal(client.meta, "FundingRateHistory", "funding rate", row.Rate); err != nil {
			return exchange.FundingRatePage{}, err
		}
		out = append(out, exchange.FundingRate{Instrument: request.Instrument, Rate: gateDecimal(row.Rate), FundingTime: time.Unix(row.Time, 0).UTC()})
	}
	return exchange.FundingRatePage{Rates: out, Page: exchange.PageInfo{Cursor: request.Cursor, Limit: request.Limit, WindowStart: request.Start, WindowEnd: request.End}}, nil
}

func (client *gatePerpClient) SetLeverage(ctx context.Context, request exchange.SetLeverageRequest) (exchange.Leverage, error) {
	if err := gateContext(ctx, client.meta, "SetLeverage"); err != nil {
		return exchange.Leverage{}, err
	}
	if strings.TrimSpace(request.Instrument) == "" || request.Leverage <= 0 {
		return exchange.Leverage{}, exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: "SetLeverage", SafeMessage: "instrument and positive leverage are required"})
	}
	row, err := client.sdk.SetFuturesLeverage(ctx, client.settle, request.Instrument, request.Leverage)
	if err != nil {
		return exchange.Leverage{}, gateWriteErr(client.meta, "SetLeverage", err)
	}
	effective, err := strconv.Atoi(row.Leverage)
	if err != nil {
		return exchange.Leverage{}, gateBadResponse(client.meta, "SetLeverage", err)
	}
	if effective != request.Leverage {
		return exchange.Leverage{}, gateMalformed(client.meta, "SetLeverage", "gate leverage response did not match request")
	}
	return exchange.Leverage{Instrument: request.Instrument, Effective: effective}, nil
}

func gateSpotPlaceOrder(request exchange.PlaceOrderRequest) gate.Order {
	order := gate.Order{CurrencyPair: request.Instrument, Text: gateClientOrderID(request.ClientOrderID), Side: string(request.Side), Amount: request.Quantity.String(), Account: "spot"}
	if request.Type == exchange.OrderTypeMarket {
		order.Type = "market"
		order.TimeInForce = "ioc"
		return order
	}
	order.Type = "limit"
	order.Price = request.LimitPrice.String()
	switch request.LimitPolicy {
	case exchange.LimitPolicyIOC:
		order.TimeInForce = "ioc"
	case exchange.LimitPolicyPostOnly:
		order.TimeInForce = "poc"
	default:
		order.TimeInForce = "gtc"
	}
	return order
}

func gateFuturesPlaceOrder(request exchange.PlaceOrderRequest, contracts int64) gate.FuturesOrder {
	size := contracts
	if request.Side == exchange.SideSell {
		size = -size
	}
	order := gate.FuturesOrder{Contract: request.Instrument, Text: gateClientOrderID(request.ClientOrderID), Size: size, ReduceOnly: request.ReduceOnly}
	if request.Type == exchange.OrderTypeMarket {
		order.Price = "0"
		order.TIF = "ioc"
		return order
	}
	order.Price = request.LimitPrice.String()
	switch request.LimitPolicy {
	case exchange.LimitPolicyIOC:
		order.TIF = "ioc"
	case exchange.LimitPolicyPostOnly:
		order.TIF = "poc"
	default:
		order.TIF = "gtc"
	}
	return order
}

func gateSpotOrders(rows []gate.Order) []exchange.Order {
	out := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		out = append(out, gateSpotOrder(row))
	}
	return out
}

func gateValidateHistoryRequest(meta clientMeta, operation, instrument, cursor string, start, end time.Time) error {
	if strings.TrimSpace(instrument) == "" {
		return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "instrument is required"})
	}
	if strings.TrimSpace(cursor) != "" {
		return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "cursor is not supported"})
	}
	if !start.IsZero() && !end.IsZero() && !start.Before(end) {
		return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "start must be before end"})
	}
	return nil
}

func gateRejectCursor(meta clientMeta, operation, cursor string) error {
	if strings.TrimSpace(cursor) == "" {
		return nil
	}
	return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "cursor is not supported"})
}

func gateValidateFillsRequest(meta clientMeta, request exchange.FillsRequest) error {
	if err := gateRejectCursor(meta, "Fills", request.Cursor); err != nil {
		return err
	}
	if !request.Start.IsZero() || !request.End.IsZero() {
		return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: "Fills", SafeMessage: "time windows are not supported"})
	}
	return nil
}

func gateValidateSpotOrders(meta clientMeta, operation string, rows []gate.Order) error {
	for _, row := range rows {
		if err := gateValidateSide(meta, operation, row.Side); err != nil {
			return err
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{"amount", row.Amount},
			{"price", row.Price},
			{"left", row.Left},
			{"filled amount", row.FilledAmount},
			{"average deal price", row.AvgDealPrice},
		} {
			if err := gateValidateDecimal(meta, operation, "order "+field.name, field.value); err != nil {
				return err
			}
		}
		if err := gateValidateStatus(meta, operation, row.Status, row.FinishAs); err != nil {
			return err
		}
	}
	return nil
}

func gateValidateFuturesOrders(meta clientMeta, operation string, rows []gate.FuturesOrder) error {
	for _, row := range rows {
		for _, field := range []struct {
			name  string
			value string
		}{
			{"price", row.Price},
			{"fill price", row.FillPrice},
		} {
			if err := gateValidateDecimal(meta, operation, "order "+field.name, field.value); err != nil {
				return err
			}
		}
		if err := gateValidateStatus(meta, operation, row.Status, row.FinishAs); err != nil {
			return err
		}
	}
	return nil
}

func gateValidateSpotFills(meta clientMeta, operation string, rows []gate.SpotUserTrade) error {
	for _, row := range rows {
		if err := gateValidateSide(meta, operation, row.Side); err != nil {
			return err
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{"fill amount", row.Amount},
			{"fill price", row.Price},
			{"fill fee", row.Fee},
		} {
			if err := gateValidateDecimal(meta, operation, field.name, field.value); err != nil {
				return err
			}
		}
		if err := gateValidateLiquidity(meta, operation, row.Role); err != nil {
			return err
		}
	}
	return nil
}

func gateValidateFuturesFills(meta clientMeta, operation string, rows []gate.MyFuturesTrade) error {
	for _, row := range rows {
		for _, field := range []struct {
			name  string
			value string
		}{
			{"fill price", row.Price},
			{"fill fee", row.Fee},
		} {
			if err := gateValidateDecimal(meta, operation, field.name, field.value); err != nil {
				return err
			}
		}
		if err := gateValidateLiquidity(meta, operation, row.Role); err != nil {
			return err
		}
	}
	return nil
}

func gateValidateSpotAccounts(meta clientMeta, operation string, rows []gate.SpotAccount) error {
	for _, row := range rows {
		if err := gateValidateDecimal(meta, operation, "available balance", row.Available); err != nil {
			return err
		}
		if err := gateValidateDecimal(meta, operation, "locked balance", row.Locked); err != nil {
			return err
		}
	}
	return nil
}

func gateValidateUnifiedBalances(meta clientMeta, operation string, rows map[string]gate.UnifiedBalance) error {
	for asset, row := range rows {
		if strings.TrimSpace(asset) == "" {
			return gateMalformed(meta, operation, "unified balance asset is missing")
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{"available balance", row.Available},
			{"locked balance", row.Freeze},
			{"equity balance", row.Equity},
		} {
			if err := gateValidateDecimal(meta, operation, field.name, field.value); err != nil {
				return err
			}
		}
	}
	return nil
}

func gateValidateFuturesAccount(meta clientMeta, operation string, row gate.FuturesAccount) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"total balance", row.Total},
		{"available balance", row.Available},
		{"position margin", row.PositionMargin},
		{"unrealised pnl", row.UnrealisedPNL},
	} {
		if err := gateValidateDecimal(meta, operation, field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

func gateValidatePositions(meta clientMeta, operation string, rows []gate.Position) error {
	for _, row := range rows {
		for _, field := range []struct {
			name  string
			value string
		}{
			{"leverage", row.Leverage},
			{"margin", row.Margin},
			{"entry price", row.EntryPrice},
			{"liq price", row.LiqPrice},
			{"mark price", row.MarkPrice},
			{"unrealised pnl", row.UnrealisedPNL},
		} {
			if err := gateValidateDecimal(meta, operation, field.name, field.value); err != nil {
				return err
			}
		}
	}
	return nil
}

func gateValidateLiquidity(meta clientMeta, operation, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	switch strings.ToLower(value) {
	case "maker", "taker":
		return nil
	default:
		return gateMalformed(meta, operation, "invalid gate liquidity")
	}
}

func gateSpotOrder(row gate.Order) exchange.Order {
	quantity := gateDecimal(row.Amount)
	filled := gateDecimal(row.FilledAmount)
	if filled.IsZero() && row.Left != "" {
		filled = quantity.Sub(gateDecimal(row.Left))
	}
	return exchange.Order{Instrument: row.CurrencyPair, OrderID: row.ID, ClientOrderID: gatePortableClientOrderID(row.Text), Side: gateSide(row.Side), Type: gateOrderType(row), Quantity: quantity, LimitPrice: gateDecimal(row.Price), LimitPolicy: gateLimitPolicy(row.TimeInForce), Filled: filled, AverageFillPrice: gateOptionalDecimal(row.AvgDealPrice), Status: gateOrderStatus(row.Status, row.FinishAs), CreatedAt: gateTimeMS(string(row.CreateTimeMS)), UpdatedAt: gateTimeMS(string(row.UpdateTimeMS))}
}

func gateFuturesOrder(row gate.FuturesOrder, multiplier decimal.Decimal) exchange.Order {
	size := row.Size
	side := exchange.SideBuy
	if size < 0 {
		side = exchange.SideSell
		size = -size
	}
	quantity := decimal.NewFromInt(size).Mul(multiplier)
	filled := quantity.Sub(decimal.NewFromInt(abs64(row.Left)).Mul(multiplier))
	return exchange.Order{Instrument: row.Contract, OrderID: gateOrderID(row.ID), ClientOrderID: gatePortableClientOrderID(row.Text), Side: side, Type: gateFuturesOrderType(row), Quantity: quantity, LimitPrice: gateDecimal(row.Price), LimitPolicy: gateLimitPolicy(row.TIF), ReduceOnly: row.ReduceOnly || row.IsReduceOnly, Filled: filled, AverageFillPrice: gateOptionalDecimal(row.FillPrice), Status: gateOrderStatus(row.Status, row.FinishAs), CreatedAt: gateTimeMS(string(row.CreateTimeMS)), UpdatedAt: gateTimeMS(string(row.UpdateTime))}
}

func gateSpotAck(meta clientMeta, operation exchange.OrderOperation, orderType exchange.OrderType, row gate.Order) exchange.OrderAcknowledgement {
	order := gateSpotOrder(row)
	return gateAckFromOrder(meta, operation, orderType, order)
}

func gateFuturesAck(meta clientMeta, operation exchange.OrderOperation, orderType exchange.OrderType, row gate.FuturesOrder, multiplier decimal.Decimal) exchange.OrderAcknowledgement {
	order := gateFuturesOrder(row, multiplier)
	return gateAckFromOrder(meta, operation, orderType, order)
}

func gateAckFromOrder(meta clientMeta, operation exchange.OrderOperation, orderType exchange.OrderType, order exchange.Order) exchange.OrderAcknowledgement {
	state := exchange.AckResting
	if operation == exchange.OrderOperationCancel {
		state = exchange.AckCanceled
	} else if order.Filled.IsPositive() && order.Filled.LessThan(order.Quantity) {
		state = exchange.AckPartiallyFilled
	} else if order.Filled.IsPositive() && !order.Filled.LessThan(order.Quantity) {
		state = exchange.AckImmediatelyFilled
	} else if orderType == exchange.OrderTypeMarket {
		state = exchange.AckAcceptedPending
	}
	return exchange.OrderAcknowledgement{Venue: meta.venue, Product: meta.product, Operation: operation, State: state, Instrument: order.Instrument, OrderType: orderType, OrderID: order.OrderID, ClientOrderID: order.ClientOrderID, FilledQuantity: order.Filled, AverageFillPrice: order.AverageFillPrice}
}

func gateOrderType(row gate.Order) exchange.OrderType {
	if strings.EqualFold(row.Type, "market") {
		return exchange.OrderTypeMarket
	}
	return exchange.OrderTypeLimit
}

func gateFuturesOrderType(row gate.FuturesOrder) exchange.OrderType {
	if row.Price == "" || row.Price == "0" {
		return exchange.OrderTypeMarket
	}
	return exchange.OrderTypeLimit
}

func gateLimitPolicy(value string) exchange.LimitPolicy {
	switch strings.ToLower(value) {
	case "ioc":
		return exchange.LimitPolicyIOC
	case "poc":
		return exchange.LimitPolicyPostOnly
	default:
		return exchange.LimitPolicyResting
	}
}

func gateOrderStatus(status, finishAs string) string {
	if finishAs != "" {
		return finishAs
	}
	return status
}

func gateSpotFills(rows []gate.SpotUserTrade) []exchange.Fill {
	out := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		out = append(out, exchange.Fill{Instrument: row.CurrencyPair, OrderID: row.OrderID, ClientOrderID: gatePortableClientOrderID(row.Text), FillID: row.ID, Side: gateSide(row.Side), Price: gateDecimal(row.Price), Quantity: gateDecimal(row.Amount), Fee: gateDecimal(row.Fee), FeeAsset: row.FeeCurrency, Liquidity: gateLiquidity(row.Role), Time: gateTimeMS(row.CreateTimeMS)})
	}
	return out
}

func gateFuturesFills(rows []gate.MyFuturesTrade, multiplier decimal.Decimal) []exchange.Fill {
	out := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		side := exchange.SideBuy
		size := row.Size
		if size < 0 {
			side = exchange.SideSell
			size = -size
		}
		out = append(out, exchange.Fill{Instrument: row.Contract, OrderID: gateOrderID(row.OrderID), ClientOrderID: gatePortableClientOrderID(row.Text), FillID: gateOrderID(row.ID), Side: side, Price: gateDecimal(row.Price), Quantity: decimal.NewFromInt(size).Mul(multiplier), Fee: gateDecimal(row.Fee), FeeAsset: strings.ToUpper(gate.SettleUSDT), Liquidity: gateLiquidity(row.Role), Time: gateUnixSecondString(string(row.CreateTime))})
	}
	return out
}

func (client *gatePerpClient) gateFuturesOrders(ctx context.Context, operation string, rows []gate.FuturesOrder) ([]exchange.Order, error) {
	out := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		multiplier, err := client.gateContractMultiplier(ctx, operation, row.Contract)
		if err != nil {
			return nil, err
		}
		out = append(out, gateFuturesOrder(row, multiplier))
	}
	return out, nil
}

func (client *gatePerpClient) gateFuturesFills(ctx context.Context, operation string, rows []gate.MyFuturesTrade) ([]exchange.Fill, error) {
	out := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		multiplier, err := client.gateContractMultiplier(ctx, operation, row.Contract)
		if err != nil {
			return nil, err
		}
		out = append(out, gateFuturesFills([]gate.MyFuturesTrade{row}, multiplier)...)
	}
	return out, nil
}

func gateClientOrderID(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "t-" + value
}

func gatePortableClientOrderID(value string) string {
	return strings.TrimPrefix(value, "t-")
}

func gateSpotBalances(rows []gate.SpotAccount) []exchange.Balance {
	out := make([]exchange.Balance, 0, len(rows))
	for _, row := range rows {
		available := gateDecimal(row.Available)
		locked := gateDecimal(row.Locked)
		out = append(out, exchange.Balance{Asset: row.Currency, Available: available, Locked: locked, Total: available.Add(locked)})
	}
	return out
}

func gateUnifiedBalances(rows map[string]gate.UnifiedBalance) []exchange.Balance {
	assets := make([]string, 0, len(rows))
	for asset := range rows {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	out := make([]exchange.Balance, 0, len(rows))
	for _, asset := range assets {
		row := rows[asset]
		available := gateDecimal(row.Available)
		locked := gateDecimal(row.Freeze)
		total := available.Add(locked)
		if strings.TrimSpace(row.Equity) != "" {
			total = gateDecimal(row.Equity)
		}
		out = append(out, exchange.Balance{Asset: asset, Available: available, Locked: locked, Total: total})
	}
	return out
}

func gateLiquidity(value string) exchange.Liquidity {
	if strings.EqualFold(value, "maker") {
		return exchange.LiquidityMaker
	}
	return exchange.LiquidityTaker
}
