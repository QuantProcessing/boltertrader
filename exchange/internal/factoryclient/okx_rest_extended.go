package factoryclient

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

func (client *okxSpotClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := okxReady(ctx, exchange.ProductSpot, "PublicTrades", client.sdk); err != nil {
		return exchange.PublicTradePage{}, err
	}
	if err := okxValidateSpotInstrument(req.Instrument); err != nil {
		return exchange.PublicTradePage{}, okxInvalid(exchange.ProductSpot, "PublicTrades", err.Error())
	}
	return okxPublicTrades(ctx, client.sdk, exchange.ProductSpot, req, decimal.NewFromInt(1))
}

func (client *okxPerpClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "PublicTrades", client.sdk); err != nil {
		return exchange.PublicTradePage{}, err
	}
	if err := okxValidateSwapInstrument(req.Instrument); err != nil {
		return exchange.PublicTradePage{}, okxInvalid(exchange.ProductPerp, "PublicTrades", err.Error())
	}
	meta, err := client.okxPerpMeta(ctx, "PublicTrades", req.Instrument)
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	return okxPublicTrades(ctx, client.sdk, exchange.ProductPerp, req, meta.contractValue)
}

func (client *okxSpotClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := okxReady(ctx, exchange.ProductSpot, "OrderHistory", client.sdk); err != nil {
		return exchange.OrderPage{}, err
	}
	inst, err := okxOptionalSpotInstrument(req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, okxInvalid(exchange.ProductSpot, "OrderHistory", err.Error())
	}
	return okxOrderHistory(ctx, client.sdk, exchange.ProductSpot, req, inst, func(string) (decimal.Decimal, error) {
		return decimal.NewFromInt(1), nil
	})
}

func (client *okxPerpClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "OrderHistory", client.sdk); err != nil {
		return exchange.OrderPage{}, err
	}
	inst, err := okxOptionalSwapInstrument(req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, okxInvalid(exchange.ProductPerp, "OrderHistory", err.Error())
	}
	return okxOrderHistory(ctx, client.sdk, exchange.ProductPerp, req, inst, func(instrument string) (decimal.Decimal, error) {
		meta, err := client.okxPerpMeta(ctx, "OrderHistory", instrument)
		return meta.contractValue, err
	})
}

func (client *okxPerpClient) FundingRate(ctx context.Context, req exchange.FundingRateRequest) (exchange.FundingRate, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "FundingRate", client.sdk); err != nil {
		return exchange.FundingRate{}, err
	}
	if err := okxValidateSwapInstrument(req.Instrument); err != nil {
		return exchange.FundingRate{}, okxInvalid(exchange.ProductPerp, "FundingRate", err.Error())
	}
	row, err := client.sdk.GetFundingRate(ctx, req.Instrument)
	if err != nil {
		return exchange.FundingRate{}, okxNormalizeErr(exchange.ProductPerp, "FundingRate", err)
	}
	if row.InstrumentID != req.Instrument {
		return exchange.FundingRate{}, okxMalformed(exchange.ProductPerp, "FundingRate", "response instrument does not match request")
	}
	rate, err := okxDecimal(row.FundingRate)
	if err != nil {
		return exchange.FundingRate{}, okxMalformed(exchange.ProductPerp, "FundingRate", "invalid funding rate")
	}
	observed, err := okxOptionalMillis(row.Ts)
	if err != nil {
		return exchange.FundingRate{}, okxMalformed(exchange.ProductPerp, "FundingRate", "invalid observed timestamp")
	}
	fundingTime, err := okxOptionalMillis(row.FundingTime)
	if err != nil {
		return exchange.FundingRate{}, okxMalformed(exchange.ProductPerp, "FundingRate", "invalid funding timestamp")
	}
	next, err := okxOptionalMillis(row.NextFundingTime)
	if err != nil {
		return exchange.FundingRate{}, okxMalformed(exchange.ProductPerp, "FundingRate", "invalid next funding timestamp")
	}
	return exchange.FundingRate{
		Instrument:      req.Instrument,
		Rate:            rate,
		ObservedAt:      observed,
		FundingTime:     fundingTime,
		NextFundingTime: next,
	}, nil
}

func (client *okxPerpClient) FundingRateHistory(ctx context.Context, req exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "FundingRateHistory", client.sdk); err != nil {
		return exchange.FundingRatePage{}, err
	}
	if err := okxValidateSwapInstrument(req.Instrument); err != nil {
		return exchange.FundingRatePage{}, okxInvalid(exchange.ProductPerp, "FundingRateHistory", err.Error())
	}
	if err := validateBoundedHistory(req.Cursor, req.Limit, req.Start, req.End); err != nil {
		return exchange.FundingRatePage{}, okxInvalid(exchange.ProductPerp, "FundingRateHistory", "invalid history bounds")
	}
	if req.Cursor != "" {
		return exchange.FundingRatePage{}, okxInvalid(exchange.ProductPerp, "FundingRateHistory", "cursor is not supported")
	}
	rows, err := client.sdk.GetFundingRateHistory(ctx, req.Instrument, optionalMillis(req.Start), optionalMillis(req.End), req.Limit)
	if err != nil {
		return exchange.FundingRatePage{}, okxNormalizeErr(exchange.ProductPerp, "FundingRateHistory", err)
	}
	rates := make([]exchange.FundingRate, 0, len(rows))
	for _, row := range rows {
		if row.InstId != req.Instrument {
			return exchange.FundingRatePage{}, okxMalformed(exchange.ProductPerp, "FundingRateHistory", "response instrument does not match request")
		}
		rate, err := okxDecimal(row.FundingRate)
		if err != nil {
			return exchange.FundingRatePage{}, okxMalformed(exchange.ProductPerp, "FundingRateHistory", "invalid funding rate")
		}
		at, err := okxMillis(row.FundingTime)
		if err != nil {
			return exchange.FundingRatePage{}, okxMalformed(exchange.ProductPerp, "FundingRateHistory", "invalid funding timestamp")
		}
		rates = append(rates, exchange.FundingRate{Instrument: req.Instrument, Rate: rate, FundingTime: at})
	}
	return exchange.FundingRatePage{Rates: rates, Page: historyPage("", req.Limit, req.Start, req.End)}, nil
}

func (client *okxPerpClient) SetLeverage(ctx context.Context, req exchange.SetLeverageRequest) (exchange.Leverage, error) {
	if err := okxReady(ctx, exchange.ProductPerp, "SetLeverage", client.sdk); err != nil {
		return exchange.Leverage{}, err
	}
	if err := okxValidateSwapInstrument(req.Instrument); err != nil {
		return exchange.Leverage{}, okxInvalid(exchange.ProductPerp, "SetLeverage", err.Error())
	}
	if req.Leverage <= 0 || req.Leverage > 125 {
		return exchange.Leverage{}, okxInvalid(exchange.ProductPerp, "SetLeverage", "leverage must be between 1 and 125")
	}
	marginMode, positionSide, err := client.okxCurrentLeverageScope(ctx, req.Instrument)
	if err != nil {
		return exchange.Leverage{}, err
	}
	rows, err := client.sdk.SetLeverage(ctx, okx.SetLeverage{
		InstId:  req.Instrument,
		Lever:   req.Leverage,
		MgnMode: marginMode,
		PosSide: positionSide,
	})
	if err != nil {
		return exchange.Leverage{}, okxNormalizeErr(exchange.ProductPerp, "SetLeverage", err)
	}
	if len(rows) != 1 || rows[0].InstId != req.Instrument || rows[0].Lever <= 0 {
		return exchange.Leverage{}, okxMalformed(exchange.ProductPerp, "SetLeverage", "response does not match request")
	}
	if rows[0].MgnMode != marginMode || !okxPositionSidesEquivalent(positionSide, rows[0].PosSide) {
		return exchange.Leverage{}, okxMalformed(
			exchange.ProductPerp,
			"SetLeverage",
			fmt.Sprintf(
				"response margin scope does not match request: requested mgnMode=%q posSide=%q, received mgnMode=%q posSide=%q",
				marginMode,
				positionSide,
				rows[0].MgnMode,
				rows[0].PosSide,
			),
		)
	}
	return exchange.Leverage{Instrument: req.Instrument, Effective: rows[0].Lever}, nil
}

func okxPositionSidesEquivalent(left, right string) bool {
	if left == right {
		return true
	}
	leftIsNet := left == "" || left == string(okx.PosSideNet)
	rightIsNet := right == "" || right == string(okx.PosSideNet)
	return leftIsNet && rightIsNet
}

func (client *okxPerpClient) okxCurrentLeverageScope(ctx context.Context, instrument string) (string, string, error) {
	instType := okxSwapType
	rows, err := client.sdk.GetPositions(ctx, &instType, &instrument)
	if err != nil {
		return "", "", okxNormalizeErr(exchange.ProductPerp, "SetLeverage", err)
	}
	if len(rows) == 0 {
		return okxCrossMode, "", nil
	}
	if len(rows) != 1 {
		return "", "", okxInvalid(exchange.ProductPerp, "SetLeverage", "multiple position scopes are not portable")
	}
	row := rows[0]
	if row.InstType != okxSwapType || row.InstId != instrument {
		return "", "", okxMalformed(exchange.ProductPerp, "SetLeverage", "position response does not match request")
	}
	switch row.MgnMode {
	case okx.MgnModeCross, okx.MgnModeIsolated:
	default:
		return "", "", okxMalformed(exchange.ProductPerp, "SetLeverage", "unknown current margin mode")
	}
	switch row.PosSide {
	case "", okx.PosSideNet, okx.PosSideLong, okx.PosSideShort:
	default:
		return "", "", okxMalformed(exchange.ProductPerp, "SetLeverage", "unknown current position side")
	}
	return string(row.MgnMode), string(row.PosSide), nil
}

func okxPublicTrades(ctx context.Context, sdk *okx.Client, product exchange.Product, req exchange.PublicTradesRequest, multiplier decimal.Decimal) (exchange.PublicTradePage, error) {
	if req.Limit < 0 {
		return exchange.PublicTradePage{}, okxInvalid(product, "PublicTrades", "limit must be non-negative")
	}
	rows, err := sdk.GetTrades(ctx, req.Instrument, okxLimitPtr(req.Limit))
	if err != nil {
		return exchange.PublicTradePage{}, okxNormalizeErr(product, "PublicTrades", err)
	}
	trades := make([]exchange.PublicTrade, 0, len(rows))
	for _, row := range rows {
		if row.InstId != req.Instrument {
			return exchange.PublicTradePage{}, okxMalformed(product, "PublicTrades", "response instrument does not match request")
		}
		price, err := okxPositiveDecimal(row.Px)
		if err != nil {
			return exchange.PublicTradePage{}, okxMalformed(product, "PublicTrades", "invalid trade price")
		}
		quantity, err := okxPositiveDecimal(row.Sz)
		if err != nil {
			return exchange.PublicTradePage{}, okxMalformed(product, "PublicTrades", "invalid trade quantity")
		}
		side, err := okxExchangeSide(row.Side)
		if err != nil {
			return exchange.PublicTradePage{}, okxMalformed(product, "PublicTrades", "invalid trade side")
		}
		at, err := okxMillis(row.Ts)
		if err != nil {
			return exchange.PublicTradePage{}, okxMalformed(product, "PublicTrades", "invalid trade timestamp")
		}
		trades = append(trades, exchange.PublicTrade{
			Instrument: row.InstId,
			TradeID:    row.TradeId,
			Side:       side,
			Price:      price,
			Quantity:   quantity.Mul(multiplier),
			Time:       at,
		})
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func okxOrderHistory(
	ctx context.Context,
	sdk *okx.Client,
	product exchange.Product,
	req exchange.OrderHistoryRequest,
	instrument *string,
	multiplier func(string) (decimal.Decimal, error),
) (exchange.OrderPage, error) {
	if req.Limit < 0 {
		return exchange.OrderPage{}, okxInvalid(product, "OrderHistory", "limit must be non-negative")
	}
	if !req.Start.IsZero() || !req.End.IsZero() {
		return exchange.OrderPage{}, okxInvalid(product, "OrderHistory", "OKX order history does not support exchange time windows")
	}
	instType := okxSpotType
	if product == exchange.ProductPerp {
		instType = okxSwapType
	}
	rows, err := sdk.GetOrderHistory(ctx, instType, instrument, req.Cursor, "", req.Limit)
	if err != nil {
		return exchange.OrderPage{}, okxNormalizeErr(product, "OrderHistory", err)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		if row.InstType != instType || (instrument != nil && row.InstId != *instrument) {
			return exchange.OrderPage{}, okxMalformed(product, "OrderHistory", "mixed product or instrument row")
		}
		factor, err := multiplier(row.InstId)
		if err != nil {
			return exchange.OrderPage{}, err
		}
		order, err := okxOrder(row, factor)
		if err != nil {
			return exchange.OrderPage{}, okxMalformed(product, "OrderHistory", err.Error())
		}
		orders = append(orders, order)
	}
	return exchange.OrderPage{Orders: orders, Page: exchange.PageInfo{Cursor: req.Cursor, Limit: req.Limit}}, nil
}

func okxTimeCursor(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return strconv.FormatInt(value.UnixMilli(), 10)
}

func okxNormalizedOrderPolicy(value okx.OrderType) (exchange.OrderType, exchange.LimitPolicy) {
	switch strings.ToLower(string(value)) {
	case "market":
		return exchange.OrderTypeMarket, ""
	case "ioc":
		return exchange.OrderTypeLimit, exchange.LimitPolicyIOC
	case "post_only":
		return exchange.OrderTypeLimit, exchange.LimitPolicyPostOnly
	default:
		return exchange.OrderTypeLimit, exchange.LimitPolicyResting
	}
}

func okxOrderRequestShape(req exchange.PlaceOrderRequest) (string, *string) {
	if req.Type == exchange.OrderTypeMarket {
		return "market", nil
	}
	price := req.LimitPrice.String()
	switch req.LimitPolicy {
	case exchange.LimitPolicyIOC:
		return "ioc", &price
	case exchange.LimitPolicyPostOnly:
		return "post_only", &price
	default:
		return "limit", &price
	}
}
