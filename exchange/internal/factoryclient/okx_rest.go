package factoryclient

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

const (
	okxVenue     = exchange.VenueOKX
	okxSpotType  = "SPOT"
	okxSwapType  = "SWAP"
	okxCashMode  = "cash"
	okxCrossMode = "cross"
)

type okxContractMeta struct {
	instrument        exchange.Instrument
	contractValue     decimal.Decimal
	contractIncrement decimal.Decimal
}

func (client *okxSpotClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := okxReady(ctx, exchange.ProductSpot, "Instruments", client.sdk); err != nil {
		return nil, err
	}
	rows, err := client.sdk.GetInstruments(ctx, okxSpotType)
	if err != nil {
		return nil, okxNormalizeErr(exchange.ProductSpot, "Instruments", err)
	}
	return okxSpotInstruments(rows)
}

func (client *okxSpotClient) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := okxReady(ctx, exchange.ProductSpot, "OrderBook", client.sdk); err != nil {
		return exchange.OrderBook{}, err
	}
	if err := okxValidateSpotInstrument(req.Instrument); err != nil {
		return exchange.OrderBook{}, okxInvalid(exchange.ProductSpot, "OrderBook", err.Error())
	}
	if req.Limit < 0 {
		return exchange.OrderBook{}, okxInvalid(exchange.ProductSpot, "OrderBook", "limit must be non-negative")
	}
	rows, err := client.sdk.GetOrderBook(ctx, req.Instrument, okxLimitPtr(req.Limit))
	if err != nil {
		return exchange.OrderBook{}, okxNormalizeErr(exchange.ProductSpot, "OrderBook", err)
	}
	return okxOrderBook(exchange.ProductSpot, "OrderBook", req.Instrument, req.Limit, rows, decimal.NewFromInt(1))
}

func (client *okxSpotClient) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := okxReady(ctx, exchange.ProductSpot, "Candles", client.sdk); err != nil {
		return exchange.CandlePage{}, err
	}
	if err := okxValidateSpotInstrument(req.Instrument); err != nil {
		return exchange.CandlePage{}, okxInvalid(exchange.ProductSpot, "Candles", err.Error())
	}
	return okxCandles(ctx, client.sdk, exchange.ProductSpot, req)
}

func (client *okxSpotClient) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := okxReady(ctx, exchange.ProductSpot, "PlaceOrder", client.sdk); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := req.Validate(exchange.ProductSpot); err != nil {
		return exchange.OrderAcknowledgement{}, okxInvalid(exchange.ProductSpot, "PlaceOrder", err.Error())
	}
	tradeMode, err := client.okxSpotTradeMode(ctx, "PlaceOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ordType, px := okxOrderRequestShape(req)
	var targetCurrency *string
	if req.Type == exchange.OrderTypeMarket {
		value := "base_ccy"
		targetCurrency = &value
	}
	oid, err := client.sdk.PlaceOrder(ctx, &okx.OrderRequest{
		InstId:  req.Instrument,
		TdMode:  tradeMode,
		ClOrdId: okxStringPtr(req.ClientOrderID),
		Side:    string(req.Side),
		OrdType: ordType,
		Sz:      req.Quantity.String(),
		Px:      px,
		TgtCcy:  targetCurrency,
	})
	if err != nil {
		return okxCommandTransportAck(exchange.ProductSpot, exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, err)
	}
	ack, err := okxCommandAck(exchange.ProductSpot, "PlaceOrder", exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, oid)
	ack.OrderType = req.Type
	if err != nil {
		return ack, err
	}
	return ack, ack.Validate()
}

func (client *okxSpotClient) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := okxReady(ctx, exchange.ProductSpot, "CancelOrder", client.sdk); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := okxValidateCancel(exchange.ProductSpot, req); err != nil {
		return exchange.OrderAcknowledgement{}, okxInvalid(exchange.ProductSpot, "CancelOrder", err.Error())
	}
	oid, err := client.sdk.CancelOrder(ctx, req.Instrument, req.OrderID, "")
	if err != nil {
		return okxCommandTransportAck(exchange.ProductSpot, exchange.OrderOperationCancel, req.Instrument, req.OrderID, "", err)
	}
	return okxCommandAck(exchange.ProductSpot, "CancelOrder", exchange.OrderOperationCancel, req.Instrument, req.OrderID, "", oid)
}

func (client *okxSpotClient) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := okxReady(ctx, exchange.ProductSpot, "OpenOrders", client.sdk); err != nil {
		return exchange.OrderPage{}, err
	}
	if err := okxValidateOpenOrdersPage(exchange.ProductSpot, req.Cursor, req.Limit); err != nil {
		return exchange.OrderPage{}, err
	}
	inst, err := okxOptionalSpotInstrument(req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, okxInvalid(exchange.ProductSpot, "OpenOrders", err.Error())
	}
	instType := okxSpotType
	rows, err := client.sdk.GetOrders(ctx, &instType, inst)
	if err != nil {
		return exchange.OrderPage{}, okxNormalizeErr(exchange.ProductSpot, "OpenOrders", err)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		if row.InstType != okxSpotType || (req.Instrument != "" && row.InstId != req.Instrument) {
			return exchange.OrderPage{}, okxMalformed(exchange.ProductSpot, "OpenOrders", "mixed product or instrument row")
		}
		order, err := okxOrder(row, decimal.NewFromInt(1))
		if err != nil {
			return exchange.OrderPage{}, okxMalformed(exchange.ProductSpot, "OpenOrders", err.Error())
		}
		orders = append(orders, order)
	}
	return boundedOrderPage(orders, req.Limit, ""), nil
}

func (client *okxSpotClient) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	if err := okxReady(ctx, exchange.ProductSpot, "Fills", client.sdk); err != nil {
		return exchange.FillPage{}, err
	}
	if err := okxValidateFillsPage(exchange.ProductSpot, req); err != nil {
		return exchange.FillPage{}, err
	}
	inst, err := okxOptionalSpotInstrument(req.Instrument)
	if err != nil {
		return exchange.FillPage{}, okxInvalid(exchange.ProductSpot, "Fills", err.Error())
	}
	instType := okxSpotType
	ordID := okxStringPtr(req.OrderID)
	rows, err := client.sdk.GetFills(ctx, &instType, inst, ordID, req.Limit)
	if err != nil {
		return exchange.FillPage{}, okxNormalizeErr(exchange.ProductSpot, "Fills", err)
	}
	fills := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		if row.InstType != okxSpotType || (req.Instrument != "" && row.InstId != req.Instrument) {
			return exchange.FillPage{}, okxMalformed(exchange.ProductSpot, "Fills", "mixed product or instrument row")
		}
		fill, err := okxFill(row, decimal.NewFromInt(1))
		if err != nil {
			return exchange.FillPage{}, okxMalformed(exchange.ProductSpot, "Fills", err.Error())
		}
		fills = append(fills, fill)
	}
	return exchange.FillPage{Fills: fills, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *okxSpotClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	if err := okxReady(ctx, exchange.ProductSpot, "Balances", client.sdk); err != nil {
		return nil, err
	}
	rows, err := client.sdk.GetAccountBalance(ctx, nil)
	if err != nil {
		return nil, okxNormalizeErr(exchange.ProductSpot, "Balances", err)
	}
	return okxBalances(exchange.ProductSpot, "Balances", rows)
}

func (client *okxSpotClient) SpotAccount(ctx context.Context) (exchange.SpotAccount, error) {
	balances, err := client.Balances(ctx)
	if err != nil {
		return exchange.SpotAccount{}, withExchangeOperation(err, "SpotAccount")
	}
	return exchange.SpotAccount{Balances: balances}, nil
}

func (client *okxSpotClient) okxSpotTradeMode(ctx context.Context, operation string) (string, error) {
	if err := okxReady(ctx, exchange.ProductSpot, operation, client.sdk); err != nil {
		return "", err
	}
	for {
		client.cacheMu.Lock()
		if client.tradeMode != "" {
			cached := client.tradeMode
			client.cacheMu.Unlock()
			return cached, nil
		}
		if client.loading == nil {
			client.loading = make(chan struct{})
			loading := client.loading
			client.cacheMu.Unlock()
			return client.okxLoadSpotTradeMode(ctx, operation, loading)
		}
		loading := client.loading
		client.cacheMu.Unlock()

		select {
		case <-loading:
		case <-ctx.Done():
			return "", okxContextErr(exchange.ProductSpot, operation, ctx.Err())
		}
	}
}

func (client *okxSpotClient) okxLoadSpotTradeMode(
	ctx context.Context,
	operation string,
	loading chan struct{},
) (string, error) {
	rows, err := client.sdk.GetAccountConfig(ctx)
	if err != nil {
		client.finishSpotTradeModeLoad(loading, "")
		return "", okxNormalizeErr(exchange.ProductSpot, operation, err)
	}
	tradeMode, err := okxSpotTradeModeFromAccountConfig(rows)
	if err != nil {
		client.finishSpotTradeModeLoad(loading, "")
		return "", okxMalformed(exchange.ProductSpot, operation, err.Error())
	}
	client.finishSpotTradeModeLoad(loading, tradeMode)
	return tradeMode, nil
}

func (client *okxSpotClient) finishSpotTradeModeLoad(loading chan struct{}, tradeMode string) {
	client.cacheMu.Lock()
	if tradeMode != "" {
		client.tradeMode = tradeMode
	}
	if client.loading == loading {
		client.loading = nil
		close(loading)
	}
	client.cacheMu.Unlock()
}

func okxSpotTradeModeFromAccountConfig(rows []okx.AccountConfig) (string, error) {
	if len(rows) != 1 {
		return "", errors.New("account config response must contain exactly one row")
	}
	switch rows[0].AcctLv {
	case "1", "2":
		return okxCashMode, nil
	case "3", "4":
		return okxCrossMode, nil
	default:
		return "", errors.New("unsupported OKX account level")
	}
}

func (client *okxPerpClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "Instruments", client.sdk); err != nil {
		return nil, err
	}
	metas, err := client.okxPerpMetas(ctx, "Instruments")
	if err != nil {
		return nil, err
	}
	out := make([]exchange.Instrument, 0, len(metas))
	for _, meta := range metas {
		out = append(out, meta.instrument)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out, nil
}

func (client *okxPerpClient) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "OrderBook", client.sdk); err != nil {
		return exchange.OrderBook{}, err
	}
	if err := okxValidateSwapInstrument(req.Instrument); err != nil {
		return exchange.OrderBook{}, okxInvalid(exchange.ProductPerp, "OrderBook", err.Error())
	}
	if req.Limit < 0 {
		return exchange.OrderBook{}, okxInvalid(exchange.ProductPerp, "OrderBook", "limit must be non-negative")
	}
	meta, err := client.okxPerpMeta(ctx, "OrderBook", req.Instrument)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	rows, err := client.sdk.GetOrderBook(ctx, req.Instrument, okxLimitPtr(req.Limit))
	if err != nil {
		return exchange.OrderBook{}, okxNormalizeErr(exchange.ProductPerp, "OrderBook", err)
	}
	return okxOrderBook(exchange.ProductPerp, "OrderBook", req.Instrument, req.Limit, rows, meta.contractValue)
}

func (client *okxPerpClient) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "Candles", client.sdk); err != nil {
		return exchange.CandlePage{}, err
	}
	if err := okxValidateSwapInstrument(req.Instrument); err != nil {
		return exchange.CandlePage{}, okxInvalid(exchange.ProductPerp, "Candles", err.Error())
	}
	return okxCandles(ctx, client.sdk, exchange.ProductPerp, req)
}

func (client *okxPerpClient) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "PlaceOrder", client.sdk); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := req.Validate(exchange.ProductPerp); err != nil {
		return exchange.OrderAcknowledgement{}, okxInvalid(exchange.ProductPerp, "PlaceOrder", err.Error())
	}
	meta, err := client.okxPerpMeta(ctx, "PlaceOrder", req.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	contracts := req.Quantity.Div(meta.contractValue)
	if !contracts.Mod(meta.contractIncrement).IsZero() {
		return exchange.OrderAcknowledgement{}, okxInvalid(exchange.ProductPerp, "PlaceOrder", "quantity must align to OKX contract lot size")
	}
	ordType, px := okxOrderRequestShape(req)
	oid, err := client.sdk.PlaceOrder(ctx, &okx.OrderRequest{
		InstId:     req.Instrument,
		TdMode:     okxCrossMode,
		ClOrdId:    okxStringPtr(req.ClientOrderID),
		Side:       string(req.Side),
		OrdType:    ordType,
		Sz:         contracts.String(),
		Px:         px,
		ReduceOnly: &req.ReduceOnly,
	})
	if err != nil {
		return okxCommandTransportAck(exchange.ProductPerp, exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, err)
	}
	ack, err := okxCommandAck(exchange.ProductPerp, "PlaceOrder", exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, oid)
	ack.OrderType = req.Type
	if err != nil {
		return ack, err
	}
	return ack, ack.Validate()
}

func (client *okxPerpClient) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "CancelOrder", client.sdk); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := okxValidateCancel(exchange.ProductPerp, req); err != nil {
		return exchange.OrderAcknowledgement{}, okxInvalid(exchange.ProductPerp, "CancelOrder", err.Error())
	}
	oid, err := client.sdk.CancelOrder(ctx, req.Instrument, req.OrderID, "")
	if err != nil {
		return okxCommandTransportAck(exchange.ProductPerp, exchange.OrderOperationCancel, req.Instrument, req.OrderID, "", err)
	}
	return okxCommandAck(exchange.ProductPerp, "CancelOrder", exchange.OrderOperationCancel, req.Instrument, req.OrderID, "", oid)
}

func (client *okxPerpClient) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "OpenOrders", client.sdk); err != nil {
		return exchange.OrderPage{}, err
	}
	if err := okxValidateOpenOrdersPage(exchange.ProductPerp, req.Cursor, req.Limit); err != nil {
		return exchange.OrderPage{}, err
	}
	inst, err := okxOptionalSwapInstrument(req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, okxInvalid(exchange.ProductPerp, "OpenOrders", err.Error())
	}
	var requestedMeta okxContractMeta
	if req.Instrument != "" {
		requestedMeta, err = client.okxPerpMeta(ctx, "OpenOrders", req.Instrument)
		if err != nil {
			return exchange.OrderPage{}, err
		}
	}
	instType := okxSwapType
	rows, err := client.sdk.GetOrders(ctx, &instType, inst)
	if err != nil {
		return exchange.OrderPage{}, okxNormalizeErr(exchange.ProductPerp, "OpenOrders", err)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		if row.InstType != okxSwapType || (req.Instrument != "" && row.InstId != req.Instrument) {
			return exchange.OrderPage{}, okxMalformed(exchange.ProductPerp, "OpenOrders", "mixed product or instrument row")
		}
		meta := requestedMeta
		if req.Instrument == "" {
			meta, err = client.okxPerpMeta(ctx, "OpenOrders", row.InstId)
			if err != nil {
				return exchange.OrderPage{}, err
			}
		}
		order, err := okxOrder(row, meta.contractValue)
		if err != nil {
			return exchange.OrderPage{}, okxMalformed(exchange.ProductPerp, "OpenOrders", err.Error())
		}
		orders = append(orders, order)
	}
	return boundedOrderPage(orders, req.Limit, ""), nil
}

func (client *okxPerpClient) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "Fills", client.sdk); err != nil {
		return exchange.FillPage{}, err
	}
	if err := okxValidateFillsPage(exchange.ProductPerp, req); err != nil {
		return exchange.FillPage{}, err
	}
	inst, err := okxOptionalSwapInstrument(req.Instrument)
	if err != nil {
		return exchange.FillPage{}, okxInvalid(exchange.ProductPerp, "Fills", err.Error())
	}
	var requestedMeta okxContractMeta
	if req.Instrument != "" {
		requestedMeta, err = client.okxPerpMeta(ctx, "Fills", req.Instrument)
		if err != nil {
			return exchange.FillPage{}, err
		}
	}
	instType := okxSwapType
	rows, err := client.sdk.GetFills(ctx, &instType, inst, okxStringPtr(req.OrderID), req.Limit)
	if err != nil {
		return exchange.FillPage{}, okxNormalizeErr(exchange.ProductPerp, "Fills", err)
	}
	fills := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		if row.InstType != okxSwapType || (req.Instrument != "" && row.InstId != req.Instrument) {
			return exchange.FillPage{}, okxMalformed(exchange.ProductPerp, "Fills", "mixed product or instrument row")
		}
		meta := requestedMeta
		if req.Instrument == "" {
			meta, err = client.okxPerpMeta(ctx, "Fills", row.InstId)
			if err != nil {
				return exchange.FillPage{}, err
			}
		}
		fill, err := okxFill(row, meta.contractValue)
		if err != nil {
			return exchange.FillPage{}, okxMalformed(exchange.ProductPerp, "Fills", err.Error())
		}
		fills = append(fills, fill)
	}
	return exchange.FillPage{Fills: fills, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *okxPerpClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	account, err := client.PerpAccount(ctx)
	if err != nil {
		return nil, withExchangeOperation(err, "Balances")
	}
	return account.Balances, nil
}

func (client *okxPerpClient) PerpAccount(ctx context.Context) (exchange.PerpAccount, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "PerpAccount", client.sdk); err != nil {
		return exchange.PerpAccount{}, err
	}
	rows, err := client.sdk.GetAccountBalance(ctx, nil)
	if err != nil {
		return exchange.PerpAccount{}, okxNormalizeErr(exchange.ProductPerp, "PerpAccount", err)
	}
	balances, err := okxBalances(exchange.ProductPerp, "PerpAccount", rows)
	if err != nil {
		return exchange.PerpAccount{}, err
	}
	account := exchange.PerpAccount{Balances: balances}
	if len(rows) > 0 {
		account.Equity, err = okxOptional(rows[0].TotalEq)
		if err != nil {
			return exchange.PerpAccount{}, okxMalformed(exchange.ProductPerp, "PerpAccount", err.Error())
		}
		account.Available, err = okxOptional(rows[0].AvailEq)
		if err != nil {
			return exchange.PerpAccount{}, okxMalformed(exchange.ProductPerp, "PerpAccount", err.Error())
		}
		account.MarginUsed, err = okxOptional(rows[0].Imr)
		if err != nil {
			return exchange.PerpAccount{}, okxMalformed(exchange.ProductPerp, "PerpAccount", err.Error())
		}
		account.UnrealizedPnL, err = okxOptional(rows[0].Upl)
		if err != nil {
			return exchange.PerpAccount{}, okxMalformed(exchange.ProductPerp, "PerpAccount", err.Error())
		}
	}
	return account, nil
}

func (client *okxPerpClient) Positions(ctx context.Context, req exchange.PositionsRequest) ([]exchange.Position, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "Positions", client.sdk); err != nil {
		return nil, err
	}
	inst, err := okxOptionalSwapInstrument(req.Instrument)
	if err != nil {
		return nil, okxInvalid(exchange.ProductPerp, "Positions", err.Error())
	}
	var requestedMeta okxContractMeta
	if req.Instrument != "" {
		requestedMeta, err = client.okxPerpMeta(ctx, "Positions", req.Instrument)
		if err != nil {
			return nil, err
		}
	}
	instType := okxSwapType
	rows, err := client.sdk.GetPositions(ctx, &instType, inst)
	if err != nil {
		return nil, okxNormalizeErr(exchange.ProductPerp, "Positions", err)
	}
	positions := make([]exchange.Position, 0, len(rows))
	for _, row := range rows {
		if row.InstType != okxSwapType || (req.Instrument != "" && row.InstId != req.Instrument) {
			return nil, okxMalformed(exchange.ProductPerp, "Positions", "mixed product or instrument row")
		}
		meta := requestedMeta
		if req.Instrument == "" {
			meta, err = client.okxPerpMeta(ctx, "Positions", row.InstId)
			if err != nil {
				return nil, err
			}
		}
		pos, err := okxPosition(row, meta.contractValue)
		if err != nil {
			return nil, okxMalformed(exchange.ProductPerp, "Positions", err.Error())
		}
		if pos.Quantity.IsZero() {
			continue
		}
		positions = append(positions, pos)
	}
	return positions, nil
}

func (client *okxPerpClient) okxPerpMetas(ctx context.Context, operation string) (map[string]okxContractMeta, error) {
	if err := okxReady(ctx, exchange.ProductPerp, operation, client.sdk); err != nil {
		return nil, err
	}
	for {
		client.cacheMu.Lock()
		if client.contracts != nil {
			cached := client.contracts
			client.cacheMu.Unlock()
			return cached, nil
		}
		if client.loading == nil {
			client.loading = make(chan struct{})
			loading := client.loading
			client.cacheMu.Unlock()
			return client.okxLoadPerpMetas(ctx, operation, loading)
		}
		loading := client.loading
		client.cacheMu.Unlock()

		select {
		case <-loading:
		case <-ctx.Done():
			return nil, okxContextErr(exchange.ProductPerp, operation, ctx.Err())
		}
	}
}

func (client *okxPerpClient) okxLoadPerpMetas(ctx context.Context, operation string, loading chan struct{}) (map[string]okxContractMeta, error) {
	rows, err := client.sdk.GetInstruments(ctx, okxSwapType)
	if err != nil {
		client.cacheMu.Lock()
		if client.loading == loading {
			client.loading = nil
			close(loading)
		}
		client.cacheMu.Unlock()
		return nil, okxNormalizeErr(exchange.ProductPerp, operation, err)
	}
	metas, err := okxPerpMetasFromRows(rows)
	if err != nil {
		client.cacheMu.Lock()
		if client.loading == loading {
			client.loading = nil
			close(loading)
		}
		client.cacheMu.Unlock()
		return nil, okxMalformed(exchange.ProductPerp, operation, err.Error())
	}
	client.cacheMu.Lock()
	client.contracts = metas
	cached := client.contracts
	if client.loading == loading {
		client.loading = nil
		close(loading)
	}
	client.cacheMu.Unlock()
	return cached, nil
}

func (client *okxPerpClient) okxPerpMeta(ctx context.Context, operation, instrument string) (okxContractMeta, error) {
	if err := okxValidateSwapInstrument(instrument); err != nil {
		return okxContractMeta{}, okxInvalid(exchange.ProductPerp, operation, err.Error())
	}
	metas, err := client.okxPerpMetas(ctx, operation)
	if err != nil {
		return okxContractMeta{}, err
	}
	meta, ok := metas[instrument]
	if !ok {
		return okxContractMeta{}, okxInvalid(exchange.ProductPerp, operation, "instrument is not present in OKX SWAP metadata")
	}
	return meta, nil
}

func okxSpotInstruments(rows []okx.Instrument) ([]exchange.Instrument, error) {
	seen := make(map[string]struct{}, len(rows))
	out := make([]exchange.Instrument, 0, len(rows))
	for _, row := range rows {
		if row.InstType != okxSpotType {
			return nil, okxMalformed(exchange.ProductSpot, "Instruments", "non-SPOT row in SPOT response")
		}
		if row.State != "live" {
			continue
		}
		if err := okxValidateSpotInstrument(row.InstId); err != nil {
			return nil, okxMalformed(exchange.ProductSpot, "Instruments", err.Error())
		}
		if _, exists := seen[row.InstId]; exists {
			return nil, okxMalformed(exchange.ProductSpot, "Instruments", "duplicate instrument")
		}
		seen[row.InstId] = struct{}{}
		price, err := okxPositiveDecimal(row.TickSz)
		if err != nil {
			return nil, okxMalformed(exchange.ProductSpot, "Instruments", "invalid tick size")
		}
		qty, err := okxPositiveDecimal(row.LotSz)
		if err != nil {
			return nil, okxMalformed(exchange.ProductSpot, "Instruments", "invalid lot size")
		}
		min, err := okxPositiveDecimal(row.MinSz)
		if err != nil {
			return nil, okxMalformed(exchange.ProductSpot, "Instruments", "invalid min size")
		}
		out = append(out, exchange.Instrument{
			Symbol:            row.InstId,
			BaseAsset:         row.BaseCcy,
			QuoteAsset:        row.QuoteCcy,
			Product:           exchange.ProductSpot,
			PriceIncrement:    price,
			QuantityIncrement: qty,
			MinQuantity:       min,
		})
	}
	return out, nil
}

func okxPerpMetasFromRows(rows []okx.Instrument) (map[string]okxContractMeta, error) {
	metas := make(map[string]okxContractMeta, len(rows))
	for _, row := range rows {
		if row.InstType != okxSwapType {
			return nil, errors.New("non-SWAP row in SWAP response")
		}
		if row.State != "live" {
			continue
		}
		if row.SettleCcy != "USDT" {
			continue
		}
		if err := okxValidateSwapInstrument(row.InstId); err != nil {
			return nil, err
		}
		if row.CtValCcy != okxSwapBase(row.InstId) {
			return nil, errors.New("invalid linear USDT contract metadata")
		}
		ctVal, err := okxPositiveDecimal(row.CtVal)
		if err != nil {
			return nil, errors.New("invalid contract value")
		}
		lot, err := okxPositiveDecimal(row.LotSz)
		if err != nil {
			return nil, errors.New("invalid lot size")
		}
		min, err := okxPositiveDecimal(row.MinSz)
		if err != nil {
			return nil, errors.New("invalid min size")
		}
		price, err := okxPositiveDecimal(row.TickSz)
		if err != nil {
			return nil, errors.New("invalid tick size")
		}
		if _, exists := metas[row.InstId]; exists {
			return nil, errors.New("duplicate instrument")
		}
		metas[row.InstId] = okxContractMeta{
			instrument: exchange.Instrument{
				Symbol:            row.InstId,
				BaseAsset:         okxSwapBase(row.InstId),
				QuoteAsset:        "USDT",
				SettleAsset:       "USDT",
				Product:           exchange.ProductPerp,
				PriceIncrement:    price,
				QuantityIncrement: lot.Mul(ctVal),
				MinQuantity:       min.Mul(ctVal),
			},
			contractValue:     ctVal,
			contractIncrement: lot,
		}
	}
	return metas, nil
}

func okxOrderBook(product exchange.Product, operation, instrument string, limit int, rows []okx.OrderBook, multiplier decimal.Decimal) (exchange.OrderBook, error) {
	if len(rows) != 1 {
		return exchange.OrderBook{}, okxMalformed(product, operation, "order book response must contain exactly one row")
	}
	bids, err := okxBookLevels(rows[0].Bids, multiplier)
	if err != nil {
		return exchange.OrderBook{}, okxMalformed(product, operation, err.Error())
	}
	asks, err := okxBookLevels(rows[0].Asks, multiplier)
	if err != nil {
		return exchange.OrderBook{}, okxMalformed(product, operation, err.Error())
	}
	ts, err := okxMillis(rows[0].Ts)
	if err != nil {
		return exchange.OrderBook{}, okxMalformed(product, operation, "invalid book timestamp")
	}
	return exchange.OrderBook{Instrument: instrument, Bids: bids, Asks: asks, Time: ts, Page: exchange.PageInfo{Limit: limit}}, nil
}

func okxCandles(ctx context.Context, sdk *okx.Client, product exchange.Product, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	if strings.TrimSpace(req.Interval) == "" {
		return exchange.CandlePage{}, okxInvalid(product, "Candles", "interval is required")
	}
	if req.Limit < 0 {
		return exchange.CandlePage{}, okxInvalid(product, "Candles", "limit must be non-negative")
	}
	if req.Cursor != "" && (!req.Start.IsZero() || !req.End.IsZero()) {
		return exchange.CandlePage{}, okxInvalid(product, "Candles", "cursor and time window are mutually exclusive")
	}
	if !req.Start.IsZero() && !req.End.IsZero() && !req.Start.Before(req.End) {
		return exchange.CandlePage{}, okxInvalid(product, "Candles", "start must be before end")
	}
	interval, err := okxIntervalDuration(req.Interval)
	if err != nil {
		return exchange.CandlePage{}, okxInvalid(product, "Candles", err.Error())
	}
	after := okxStringPtr(req.Cursor)
	var before *string
	if !req.End.IsZero() {
		value := strconv.FormatInt(req.End.UnixMilli(), 10)
		after = &value
	}
	if !req.Start.IsZero() {
		value := strconv.FormatInt(req.Start.UnixMilli(), 10)
		before = &value
	}
	rows, err := sdk.GetCandles(ctx, req.Instrument, &req.Interval, after, before, okxLimitPtr(req.Limit))
	if err != nil {
		return exchange.CandlePage{}, okxNormalizeErr(product, "Candles", err)
	}
	candles := make([]exchange.Candle, 0, len(rows))
	for _, row := range rows {
		candle, err := okxCandle(row, product, interval)
		if err != nil {
			return exchange.CandlePage{}, okxMalformed(product, "Candles", err.Error())
		}
		candles = append(candles, candle)
	}
	sort.Slice(candles, func(i, j int) bool { return candles[i].OpenTime.Before(candles[j].OpenTime) })
	page := exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End}
	if len(candles) > 0 {
		if page.WindowStart.IsZero() {
			page.WindowStart = candles[0].OpenTime
		}
		if page.WindowEnd.IsZero() {
			page.WindowEnd = candles[len(candles)-1].CloseTime
		}
		page.Cursor = strconv.FormatInt(candles[0].OpenTime.UnixMilli(), 10)
	}
	return exchange.CandlePage{Candles: candles, Page: page}, nil
}

func okxCommandAck(product exchange.Product, operation string, op exchange.OrderOperation, instrument, requestOrderID, requestClientOrderID string, rows []okx.OrderId) (exchange.OrderAcknowledgement, error) {
	if len(rows) != 1 {
		return exchange.OrderAcknowledgement{}, okxMalformed(product, operation, "order command response must contain exactly one item")
	}
	row := rows[0]
	ack := okxBaseAck(product, op, instrument, row.OrdId, row.ClOrdId)
	if row.SCode == "" {
		return exchange.OrderAcknowledgement{}, okxMalformed(product, operation, "order command item missing sCode")
	}
	if row.SCode != "0" {
		ack.State = exchange.AckRejected
		ack.VenueCode = row.SCode
		ack.VenueMessage = "OKX rejected order command"
		if err := ack.Validate(); err != nil {
			return exchange.OrderAcknowledgement{}, err
		}
		return ack, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, Code: row.SCode, SafeMessage: "OKX rejected order command"})
	}
	if requestOrderID != "" && row.OrdId != requestOrderID {
		return exchange.OrderAcknowledgement{}, okxMalformed(product, operation, "order command response order id mismatch")
	}
	if requestClientOrderID != "" && row.ClOrdId != requestClientOrderID {
		return exchange.OrderAcknowledgement{}, okxMalformed(product, operation, "order command response client order id mismatch")
	}
	ack.State = exchange.AckAcceptedPending
	if err := ack.Validate(); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return ack, nil
}

func okxCommandTransportAck(product exchange.Product, op exchange.OrderOperation, instrument, orderID, clientOrderID string, err error) (exchange.OrderAcknowledgement, error) {
	ack := okxBaseAck(product, op, instrument, orderID, clientOrderID)
	ack.State = exchange.AckAmbiguous
	operation := okxCommandOperation(op)
	if errors.Is(err, okx.ErrWSPreSend) {
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: "OKX websocket command was not sent"})
	}
	if errors.Is(err, okx.ErrWSOutcomeUnknown) {
		return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: "order command outcome is unknown after possible send"})
	}
	if errors.Is(err, okx.ErrWSMalformedResponse) {
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: "OKX websocket command response was malformed"})
	}
	var sdkErr *sdkcore.ExchangeError
	if errors.As(err, &sdkErr) && errors.Is(err, sdkcore.ErrRateLimited) {
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, Code: sdkErr.Code, SafeMessage: "OKX rate limit"})
	}
	var apiErr *okx.APIError
	if errors.As(err, &apiErr) {
		if okxIsAuthCode(apiErr.Code) {
			return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, Code: apiErr.Code, SafeMessage: "OKX authentication failed"})
		}
		ack.State = exchange.AckRejected
		ack.VenueCode = apiErr.Code
		ack.VenueMessage = "OKX rejected order command"
		return ack, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, Code: apiErr.Code, SafeMessage: "OKX rejected order command"})
	}
	if okxIsCredentialError(err) {
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: "OKX credentials required"})
	}
	if okxIsHTTPAuthError(err) {
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: "OKX authentication failed"})
	}
	return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: "order command outcome is unknown after possible send"})
}

func okxCommandOperation(operation exchange.OrderOperation) string {
	switch operation {
	case exchange.OrderOperationPlace:
		return "PlaceOrder"
	case exchange.OrderOperationCancel:
		return "CancelOrder"
	default:
		return string(operation)
	}
}

func okxBaseAck(product exchange.Product, op exchange.OrderOperation, instrument, orderID, clientOrderID string) exchange.OrderAcknowledgement {
	return exchange.OrderAcknowledgement{Venue: okxVenue, Product: product, Operation: op, Instrument: instrument, OrderID: orderID, ClientOrderID: clientOrderID}
}

func okxOrder(row okx.Order, multiplier decimal.Decimal) (exchange.Order, error) {
	qty, err := okxNonNegativeDecimal(row.Sz)
	if err != nil {
		return exchange.Order{}, err
	}
	filled, err := okxNonNegativeDecimal(row.AccFillSz)
	if err != nil {
		return exchange.Order{}, err
	}
	price, err := okxNonNegativeDecimal(row.Px)
	if err != nil {
		return exchange.Order{}, err
	}
	avg := exchange.OptionalDecimal{}
	if strings.TrimSpace(row.AvgPx) != "" {
		avgValue, err := okxNonNegativeDecimal(row.AvgPx)
		if err != nil {
			return exchange.Order{}, err
		}
		if !avgValue.IsZero() {
			avg = exchange.OptionalDecimal{Value: avgValue, Valid: true}
		}
	}
	side, err := okxExchangeSide(string(row.Side))
	if err != nil {
		return exchange.Order{}, err
	}
	created, err := okxOptionalMillis(row.CTime)
	if err != nil {
		return exchange.Order{}, err
	}
	updated, err := okxOptionalMillis(row.UTime)
	if err != nil {
		return exchange.Order{}, err
	}
	orderType, limitPolicy := okxNormalizedOrderPolicy(row.OrdType)
	reduceOnly := strings.EqualFold(row.ReduceOnly, "true")
	return exchange.Order{
		Instrument:       row.InstId,
		OrderID:          row.OrdId,
		ClientOrderID:    row.ClOrdId,
		Side:             side,
		Type:             orderType,
		Quantity:         qty.Mul(multiplier),
		LimitPrice:       price,
		LimitPolicy:      limitPolicy,
		ReduceOnly:       reduceOnly,
		Filled:           filled.Mul(multiplier),
		AverageFillPrice: avg,
		Status:           string(row.State),
		CreatedAt:        created,
		UpdatedAt:        updated,
	}, nil
}

func okxFill(row okx.Fill, multiplier decimal.Decimal) (exchange.Fill, error) {
	price, err := okxPositiveDecimal(row.FillPx)
	if err != nil {
		return exchange.Fill{}, err
	}
	qty, err := okxPositiveDecimal(row.FillSz)
	if err != nil {
		return exchange.Fill{}, err
	}
	fee, err := okxDecimal(row.Fee)
	if err != nil {
		return exchange.Fill{}, err
	}
	liq := exchange.LiquidityTaker
	if row.ExecType == "M" {
		liq = exchange.LiquidityMaker
	}
	side, err := okxExchangeSide(string(row.Side))
	if err != nil {
		return exchange.Fill{}, err
	}
	ts, err := okxMillis(row.Ts)
	if err != nil {
		return exchange.Fill{}, err
	}
	return exchange.Fill{
		Instrument:    row.InstId,
		OrderID:       row.OrdId,
		ClientOrderID: row.ClOrdId,
		FillID:        row.TradeId,
		Side:          side,
		Price:         price,
		Quantity:      qty.Mul(multiplier),
		Fee:           fee,
		FeeAsset:      row.FeeCcy,
		Liquidity:     liq,
		Time:          ts,
	}, nil
}

func okxBalances(product exchange.Product, operation string, rows []okx.Balance) ([]exchange.Balance, error) {
	out := []exchange.Balance{}
	for _, row := range rows {
		for _, detail := range row.Details {
			total, err := okxNonNegativeDecimal(detail.Eq)
			if err != nil {
				return nil, okxMalformed(product, operation, err.Error())
			}
			available, err := okxNonNegativeDecimal(detail.AvailBal)
			if err != nil {
				return nil, okxMalformed(product, operation, err.Error())
			}
			locked, err := okxNonNegativeDecimal(detail.FrozenBal)
			if err != nil {
				return nil, okxMalformed(product, operation, err.Error())
			}
			out = append(out, exchange.Balance{Asset: detail.Ccy, Available: available, Locked: locked, Total: total})
		}
	}
	return out, nil
}

func okxPosition(row okx.Position, multiplier decimal.Decimal) (exchange.Position, error) {
	qty, err := okxDecimal(row.Pos)
	if err != nil {
		return exchange.Position{}, err
	}
	side, signedQty, err := okxPositionSideAndQuantity(string(row.PosSide), qty)
	if err != nil {
		return exchange.Position{}, err
	}
	entry, err := okxNonNegativeDecimal(row.AvgPx)
	if err != nil {
		return exchange.Position{}, err
	}
	mark, err := okxNonNegativeDecimal(row.MarkPx)
	if err != nil {
		return exchange.Position{}, err
	}
	pnl, err := okxDecimal(row.Upl)
	if err != nil {
		return exchange.Position{}, err
	}
	liq, err := okxOptional(row.LiqPx)
	if err != nil {
		return exchange.Position{}, err
	}
	lev, err := okxOptional(row.Lever)
	if err != nil {
		return exchange.Position{}, err
	}
	margin, err := okxOptional(row.Margin)
	if err != nil {
		return exchange.Position{}, err
	}
	return exchange.Position{Instrument: row.InstId, Side: side, Quantity: signedQty.Mul(multiplier), EntryPrice: entry, MarkPrice: mark, UnrealizedPnL: pnl, LiquidationPrice: liq, Leverage: lev, MarginUsed: margin}, nil
}

func okxCandle(row okx.Candle, product exchange.Product, interval time.Duration) (exchange.Candle, error) {
	ts, err := okxMillis(row[0])
	if err != nil {
		return exchange.Candle{}, err
	}
	open, err := okxPositiveDecimal(row[1])
	if err != nil {
		return exchange.Candle{}, err
	}
	high, err := okxPositiveDecimal(row[2])
	if err != nil {
		return exchange.Candle{}, err
	}
	low, err := okxPositiveDecimal(row[3])
	if err != nil {
		return exchange.Candle{}, err
	}
	closeValue, err := okxPositiveDecimal(row[4])
	if err != nil {
		return exchange.Candle{}, err
	}
	volumeField := row[5]
	if product == exchange.ProductPerp {
		volumeField = row[6]
	}
	volume, err := okxNonNegativeDecimal(volumeField)
	if err != nil {
		return exchange.Candle{}, err
	}
	return exchange.Candle{OpenTime: ts, CloseTime: ts.Add(interval), Open: open, High: high, Low: low, Close: closeValue, Volume: volume, Complete: row[8] == "1"}, nil
}

func okxBookLevels(rows [][]string, multiplier decimal.Decimal) ([]exchange.BookLevel, error) {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			return nil, errors.New("book level must include price and size")
		}
		price, err := okxPositiveDecimal(row[0])
		if err != nil {
			return nil, err
		}
		qty, err := okxPositiveDecimal(row[1])
		if err != nil {
			return nil, err
		}
		out = append(out, exchange.BookLevel{Price: price, Quantity: qty.Mul(multiplier)})
	}
	return out, nil
}

func okxValidateOpenOrdersPage(product exchange.Product, cursor string, limit int) error {
	if cursor != "" {
		return okxInvalid(product, "OpenOrders", "OKX open orders does not support cursor")
	}
	if limit < 0 {
		return okxInvalid(product, "OpenOrders", "limit must be non-negative")
	}
	return nil
}

func okxValidateFillsPage(product exchange.Product, req exchange.FillsRequest) error {
	if req.Cursor != "" {
		return okxInvalid(product, "Fills", "OKX fills does not support cursor")
	}
	if !req.Start.IsZero() || !req.End.IsZero() {
		return okxInvalid(product, "Fills", "OKX fills does not support exchange time windows")
	}
	if req.Limit < 0 {
		return okxInvalid(product, "Fills", "limit must be non-negative")
	}
	return nil
}

func okxValidatePlace(product exchange.Product, req exchange.PlaceOrderRequest) error {
	if product == exchange.ProductSpot {
		if err := okxValidateSpotInstrument(req.Instrument); err != nil {
			return err
		}
	} else if err := okxValidateSwapInstrument(req.Instrument); err != nil {
		return err
	}
	if req.Side != exchange.SideBuy && req.Side != exchange.SideSell {
		return errors.New("side must be buy or sell")
	}
	if !req.Quantity.IsPositive() || !req.LimitPrice.IsPositive() {
		return errors.New("quantity and limit price must be positive")
	}
	if strings.TrimSpace(req.ClientOrderID) == "" || strings.TrimSpace(req.ClientOrderID) != req.ClientOrderID {
		return errors.New("client order id is required and must not have surrounding whitespace")
	}
	return nil
}

func okxValidateCancel(product exchange.Product, req exchange.CancelOrderRequest) error {
	if product == exchange.ProductSpot {
		if err := okxValidateSpotInstrument(req.Instrument); err != nil {
			return err
		}
	} else if err := okxValidateSwapInstrument(req.Instrument); err != nil {
		return err
	}
	orderID, err := strconv.ParseInt(req.OrderID, 10, 64)
	if err != nil || orderID <= 0 || strconv.FormatInt(orderID, 10) != req.OrderID {
		return errors.New("order id must be a positive decimal int64")
	}
	return nil
}

func okxOptionalSpotInstrument(instrument string) (*string, error) {
	if instrument == "" {
		return nil, nil
	}
	if err := okxValidateSpotInstrument(instrument); err != nil {
		return nil, err
	}
	return &instrument, nil
}

func okxOptionalSwapInstrument(instrument string) (*string, error) {
	if instrument == "" {
		return nil, nil
	}
	if err := okxValidateSwapInstrument(instrument); err != nil {
		return nil, err
	}
	return &instrument, nil
}

func okxValidateSpotInstrument(instrument string) error {
	parts := strings.Split(instrument, "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ToUpper(instrument) != instrument {
		return errors.New("instrument must be exact OKX SPOT BASE-QUOTE")
	}
	return nil
}

func okxValidateSwapInstrument(instrument string) error {
	parts := strings.Split(instrument, "-")
	if len(parts) != 3 || parts[0] == "" || parts[1] != "USDT" || parts[2] != "SWAP" || strings.ToUpper(instrument) != instrument {
		return errors.New("instrument must be exact OKX USDT-linear BASE-USDT-SWAP")
	}
	return nil
}

func okxSwapBase(instrument string) string {
	parts := strings.Split(instrument, "-")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func okxReady(ctx context.Context, product exchange.Product, operation string, sdk *okx.Client) error {
	if sdk == nil {
		return okxInvalid(product, operation, "client is not initialized")
	}
	if ctx == nil {
		return okxInvalid(product, operation, "context is required")
	}
	if err := ctx.Err(); err != nil {
		return okxContextErr(product, operation, err)
	}
	return nil
}

func okxNormalizeErr(product exchange.Product, operation string, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := okxContextErr(product, operation, err); ctxErr != nil {
		return ctxErr
	}
	var sdkErr *sdkcore.ExchangeError
	if errors.As(err, &sdkErr) && errors.Is(err, sdkcore.ErrRateLimited) {
		return exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, Code: sdkErr.Code, SafeMessage: "OKX rate limit"})
	}
	var apiErr *okx.APIError
	if errors.As(err, &apiErr) {
		if okxIsAuthCode(apiErr.Code) {
			return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, Code: apiErr.Code, SafeMessage: "OKX authentication failed"})
		}
		return exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, Code: apiErr.Code, SafeMessage: "OKX rejected request"})
	}
	if okxIsCredentialError(err) {
		return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: "OKX credentials required"})
	}
	return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: "okx transport failed"})
}

func okxContextErr(product exchange.Product, operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return exchange.NewError(exchange.KindCanceled, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: "context canceled"})
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return exchange.NewError(exchange.KindDeadlineExceeded, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: "context deadline exceeded"})
	}
	return nil
}

func okxIsCredentialError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "credentials required")
}

func okxIsHTTPAuthError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "http error 401") || strings.Contains(message, "http error 403")
}

func okxIsAuthCode(code string) bool {
	switch code {
	case "50105", "50111", "50113", "50119", "50120", "50121", "50122", "50123":
		return true
	default:
		return false
	}
}

func okxPositionSideAndQuantity(posSide string, qty decimal.Decimal) (exchange.Side, decimal.Decimal, error) {
	switch posSide {
	case "long":
		if qty.IsNegative() {
			return "", decimal.Zero, errors.New("long position has negative quantity")
		}
		return exchange.SideBuy, qty, nil
	case "short":
		if qty.IsNegative() {
			return exchange.SideSell, qty, nil
		}
		return exchange.SideSell, qty.Neg(), nil
	case "net", "":
		if qty.IsNegative() {
			return exchange.SideSell, qty, nil
		}
		return exchange.SideBuy, qty, nil
	default:
		return "", decimal.Zero, errors.New("unknown position side")
	}
}

func okxInvalid(product exchange.Product, operation, message string) error {
	return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: message})
}

func okxMalformed(product exchange.Product, operation, message string) error {
	return exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: message})
}

func okxDecimal(raw string) (decimal.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "0"
	}
	return decimal.NewFromString(raw)
}

func okxPositiveDecimal(raw string) (decimal.Decimal, error) {
	value, err := okxDecimal(raw)
	if err != nil {
		return decimal.Zero, err
	}
	if !value.IsPositive() {
		return decimal.Zero, errors.New("decimal must be positive")
	}
	return value, nil
}

func okxNonNegativeDecimal(raw string) (decimal.Decimal, error) {
	value, err := okxDecimal(raw)
	if err != nil {
		return decimal.Zero, err
	}
	if value.IsNegative() {
		return decimal.Zero, errors.New("decimal must be non-negative")
	}
	return value, nil
}

func okxOptional(raw string) (exchange.OptionalDecimal, error) {
	if strings.TrimSpace(raw) == "" {
		return exchange.OptionalDecimal{}, nil
	}
	value, err := okxDecimal(raw)
	if err != nil {
		return exchange.OptionalDecimal{}, err
	}
	return exchange.OptionalDecimal{Value: value, Valid: true}, nil
}

func okxMillis(raw string) (time.Time, error) {
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms).UTC(), nil
}

func okxOptionalMillis(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	return okxMillis(raw)
}

func okxExchangeSide(raw string) (exchange.Side, error) {
	switch raw {
	case string(exchange.SideBuy):
		return exchange.SideBuy, nil
	case string(exchange.SideSell):
		return exchange.SideSell, nil
	default:
		return "", errors.New("unknown side")
	}
}

func okxIntervalDuration(interval string) (time.Duration, error) {
	switch interval {
	case "1m":
		return time.Minute, nil
	case "3m":
		return 3 * time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "15m":
		return 15 * time.Minute, nil
	case "30m":
		return 30 * time.Minute, nil
	case "1H", "1h":
		return time.Hour, nil
	case "2H", "2h":
		return 2 * time.Hour, nil
	case "4H", "4h":
		return 4 * time.Hour, nil
	case "6H", "6h":
		return 6 * time.Hour, nil
	case "12H", "12h":
		return 12 * time.Hour, nil
	case "1D", "1d":
		return 24 * time.Hour, nil
	default:
		return 0, errors.New("unsupported OKX candle interval")
	}
}

func okxLimitPtr(limit int) *int {
	if limit <= 0 {
		return nil
	}
	return &limit
}

func okxStringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
