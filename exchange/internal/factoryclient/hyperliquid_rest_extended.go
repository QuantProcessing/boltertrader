package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/shopspring/decimal"
)

func (client *hyperliquidSpotClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := hlRequireCtx(ctx, exchange.ProductSpot, "PublicTrades"); err != nil {
		return exchange.PublicTradePage{}, err
	}
	meta, err := client.spotMeta(ctx, "PublicTrades", req.Instrument)
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	return hlPublicTrades(ctx, client.sdk.Client, exchange.ProductSpot, meta, req)
}

func (client *hyperliquidPerpClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "PublicTrades"); err != nil {
		return exchange.PublicTradePage{}, err
	}
	meta, err := client.perpMeta(ctx, "PublicTrades", req.Instrument)
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	return hlPublicTrades(ctx, client.sdk.Client, exchange.ProductPerp, meta, req)
}

func (client *hyperliquidSpotClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := hlRequireCtx(ctx, exchange.ProductSpot, "OrderHistory"); err != nil {
		return exchange.OrderPage{}, err
	}
	metas, requested, err := client.spotMetasForOptional(ctx, "OrderHistory", req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return hlOrderHistory(ctx, client.sdk.Client, exchange.ProductSpot, metas, requested, req)
}

func (client *hyperliquidPerpClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "OrderHistory"); err != nil {
		return exchange.OrderPage{}, err
	}
	metas, requested, err := client.perpMetasForOptional(ctx, "OrderHistory", req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return hlOrderHistory(ctx, client.sdk.Client, exchange.ProductPerp, metas, requested, req)
}

func (client *hyperliquidPerpClient) FundingRate(ctx context.Context, req exchange.FundingRateRequest) (exchange.FundingRate, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "FundingRate"); err != nil {
		return exchange.FundingRate{}, err
	}
	meta, err := client.perpMeta(ctx, "FundingRate", req.Instrument)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	row, err := client.sdk.GetFundingRate(requestCtx, meta.nativeCoin)
	if err != nil {
		return exchange.FundingRate{}, hlNormalizeQueryErr(exchange.ProductPerp, "FundingRate", err, tracker)
	}
	if row.Coin != meta.nativeCoin {
		return exchange.FundingRate{}, hlMalformed(exchange.ProductPerp, "FundingRate", "response instrument does not match request")
	}
	rate, err := hlDecimal(row.Funding)
	if err != nil {
		return exchange.FundingRate{}, hlMalformed(exchange.ProductPerp, "FundingRate", "invalid funding rate")
	}
	mark, err := hlPositiveDecimal(row.MarkPx)
	if err != nil {
		return exchange.FundingRate{}, hlMalformed(exchange.ProductPerp, "FundingRate", "invalid mark price")
	}
	return exchange.FundingRate{
		Instrument: req.Instrument,
		Rate:       rate,
		MarkPrice:  exchange.OptionalDecimal{Value: mark, Valid: true},
		ObservedAt: time.Now().UTC(),
	}, nil
}

func (client *hyperliquidPerpClient) FundingRateHistory(ctx context.Context, req exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "FundingRateHistory"); err != nil {
		return exchange.FundingRatePage{}, err
	}
	meta, err := client.perpMeta(ctx, "FundingRateHistory", req.Instrument)
	if err != nil {
		return exchange.FundingRatePage{}, err
	}
	if req.Cursor != "" {
		return exchange.FundingRatePage{}, hlInvalid(exchange.ProductPerp, "FundingRateHistory", "cursor is not supported")
	}
	if req.Limit < 0 || (!req.Start.IsZero() && !req.End.IsZero() && !req.Start.Before(req.End)) {
		return exchange.FundingRatePage{}, hlInvalid(exchange.ProductPerp, "FundingRateHistory", "history bounds must be valid")
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	rows, err := client.sdk.GetFundingRateHistory(requestCtx, meta.nativeCoin, optionalMillis(req.Start), optionalMillis(req.End))
	if err != nil {
		return exchange.FundingRatePage{}, hlNormalizeQueryErr(exchange.ProductPerp, "FundingRateHistory", err, tracker)
	}
	if req.Limit > 0 && len(rows) > req.Limit {
		rows = rows[len(rows)-req.Limit:]
	}
	rates := make([]exchange.FundingRate, 0, len(rows))
	for _, row := range rows {
		if row.Coin != meta.nativeCoin {
			return exchange.FundingRatePage{}, hlMalformed(exchange.ProductPerp, "FundingRateHistory", "mixed instrument row")
		}
		rate, err := hlDecimal(row.FundingRate)
		if err != nil {
			return exchange.FundingRatePage{}, hlMalformed(exchange.ProductPerp, "FundingRateHistory", "invalid funding rate")
		}
		rates = append(rates, exchange.FundingRate{
			Instrument:  req.Instrument,
			Rate:        rate,
			FundingTime: time.UnixMilli(row.Time).UTC(),
		})
	}
	return exchange.FundingRatePage{Rates: rates, Page: historyPage("", req.Limit, req.Start, req.End)}, nil
}

func (client *hyperliquidPerpClient) SetLeverage(ctx context.Context, req exchange.SetLeverageRequest) (exchange.Leverage, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "SetLeverage"); err != nil {
		return exchange.Leverage{}, err
	}
	meta, err := client.perpMeta(ctx, "SetLeverage", req.Instrument)
	if err != nil {
		return exchange.Leverage{}, err
	}
	if req.Leverage <= 0 {
		return exchange.Leverage{}, hlInvalid(exchange.ProductPerp, "SetLeverage", "leverage must be positive")
	}
	isCross, maxLeverage, err := client.hlCurrentLeverageMode(ctx, meta)
	if err != nil {
		return exchange.Leverage{}, err
	}
	if maxLeverage > 0 && req.Leverage > maxLeverage {
		return exchange.Leverage{}, hlInvalid(exchange.ProductPerp, "SetLeverage", "leverage exceeds instrument maximum")
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	err = client.sdk.UpdateLeverage(requestCtx, hyperliquidperp.UpdateLeverageRequest{
		AssetID:  meta.assetID,
		IsCross:  isCross,
		Leverage: req.Leverage,
	})
	if err != nil {
		return exchange.Leverage{}, hlLeverageMutationErr(err, tracker)
	}
	return exchange.Leverage{Instrument: req.Instrument, Effective: req.Leverage}, nil
}

func hlLeverageMutationErr(err error, tracker *hyperliquidRequestTracker) error {
	const operation = "SetLeverage"
	details := exchange.ErrorDetails{
		Venue:       hyperliquidVenue,
		Product:     exchange.ProductPerp,
		Operation:   operation,
		SafeMessage: "leverage update outcome is unknown after possible send",
	}
	if errors.Is(err, hyperliquid.ErrCredentialsRequired) {
		details.SafeMessage = "Hyperliquid credentials required"
		return exchange.NewError(exchange.KindAuthentication, details)
	}
	status := tracker.responseStatus()
	if status > 0 {
		details.Code = strconv.Itoa(status)
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		details.SafeMessage = "Hyperliquid authentication failed"
		return exchange.NewError(exchange.KindAuthentication, details)
	case status == http.StatusTooManyRequests:
		details.SafeMessage = "Hyperliquid rate limit"
		return exchange.NewError(exchange.KindRateLimit, details)
	case status == http.StatusNotFound:
		details.SafeMessage = "Hyperliquid leverage target not found"
		return exchange.NewError(exchange.KindNotFound, details)
	case status >= 400 && status < 500:
		details.SafeMessage = "Hyperliquid rejected the leverage update request"
		return exchange.NewError(exchange.KindInvalidRequest, details)
	case status >= 500:
		return exchange.NewError(exchange.KindAmbiguousOutcome, details)
	}
	var sdkErr *sdkcore.ExchangeError
	if errors.As(err, &sdkErr) && errors.Is(err, sdkcore.ErrRateLimited) {
		details.Code = sdkErr.Code
		details.SafeMessage = "Hyperliquid rate limit"
		return exchange.NewError(exchange.KindRateLimit, details)
	}
	var apiErr *hyperliquid.APIError
	if errors.As(err, &apiErr) {
		details.Code = strconv.Itoa(apiErr.Code)
		switch {
		case apiErr.Code == http.StatusUnauthorized || apiErr.Code == http.StatusForbidden:
			details.SafeMessage = "Hyperliquid authentication failed"
			return exchange.NewError(exchange.KindAuthentication, details)
		case apiErr.Code == http.StatusTooManyRequests:
			details.SafeMessage = "Hyperliquid rate limit"
			return exchange.NewError(exchange.KindRateLimit, details)
		case apiErr.Code == http.StatusNotFound:
			details.SafeMessage = "Hyperliquid leverage target not found"
			return exchange.NewError(exchange.KindNotFound, details)
		case apiErr.Code >= 400 && apiErr.Code < 500:
			details.SafeMessage = "Hyperliquid rejected the leverage update request"
			return exchange.NewError(exchange.KindInvalidRequest, details)
		}
	}
	return exchange.NewError(exchange.KindAmbiguousOutcome, details)
}

func (client *hyperliquidPerpClient) hlCurrentLeverageMode(ctx context.Context, meta hyperliquidMarketMeta) (bool, int, error) {
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	state, err := client.sdk.GetPerpPosition(requestCtx)
	if err != nil {
		return false, 0, hlNormalizeQueryErr(exchange.ProductPerp, "SetLeverage", err, tracker)
	}
	for _, row := range state.AssetPositions {
		if row.Position.Coin != meta.nativeCoin {
			continue
		}
		switch strings.ToLower(row.Position.Leverage.Type) {
		case "cross":
			return true, row.Position.MaxLeverage, nil
		case "isolated":
			return false, row.Position.MaxLeverage, nil
		default:
			return false, 0, hlMalformed(exchange.ProductPerp, "SetLeverage", "unknown current margin mode")
		}
	}
	return true, 0, nil
}

func hlPublicTrades(ctx context.Context, sdk *hyperliquid.Client, product exchange.Product, meta hyperliquidMarketMeta, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if req.Limit < 0 {
		return exchange.PublicTradePage{}, hlInvalid(product, "PublicTrades", "limit must be non-negative")
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	rows, err := sdk.RecentTrades(requestCtx, meta.nativeCoin)
	if err != nil {
		return exchange.PublicTradePage{}, hlNormalizeQueryErr(product, "PublicTrades", err, tracker)
	}
	if req.Limit > 0 && len(rows) > req.Limit {
		rows = rows[:req.Limit]
	}
	trades := make([]exchange.PublicTrade, 0, len(rows))
	for _, row := range rows {
		if row.Coin != meta.nativeCoin || row.TradeID <= 0 {
			return exchange.PublicTradePage{}, hlMalformed(product, "PublicTrades", "response identity does not match request")
		}
		side, err := hlSide(string(row.Side))
		if err != nil {
			return exchange.PublicTradePage{}, hlMalformed(product, "PublicTrades", "invalid trade side")
		}
		price, err := hlPositiveDecimal(row.Price)
		if err != nil {
			return exchange.PublicTradePage{}, hlMalformed(product, "PublicTrades", "invalid trade price")
		}
		quantity, err := hlPositiveDecimal(row.Size)
		if err != nil {
			return exchange.PublicTradePage{}, hlMalformed(product, "PublicTrades", "invalid trade quantity")
		}
		trades = append(trades, exchange.PublicTrade{
			Instrument: req.Instrument,
			TradeID:    strconv.FormatInt(row.TradeID, 10),
			Side:       side,
			Price:      price,
			Quantity:   quantity,
			Time:       time.UnixMilli(row.Time).UTC(),
		})
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func hlOrderHistory(
	ctx context.Context,
	sdk *hyperliquid.Client,
	product exchange.Product,
	metas map[string]hyperliquidMarketMeta,
	requested *hyperliquidMarketMeta,
	req exchange.OrderHistoryRequest,
) (exchange.OrderPage, error) {
	if req.Cursor != "" {
		return exchange.OrderPage{}, hlInvalid(product, "OrderHistory", "cursor is not supported")
	}
	if req.Limit < 0 || (!req.Start.IsZero() && !req.End.IsZero() && !req.Start.Before(req.End)) {
		return exchange.OrderPage{}, hlInvalid(product, "OrderHistory", "invalid history bounds")
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	rows, err := sdk.HistoricalOrders(requestCtx, sdk.AccountAddr)
	if err != nil {
		return exchange.OrderPage{}, hlNormalizeQueryErr(product, "OrderHistory", err, tracker)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		if strings.EqualFold(row.Status, "open") {
			continue
		}
		details := row.Order
		meta, ok := metas[details.Coin]
		if !ok || hlRejectCoin(details.Coin) {
			continue
		}
		if requested != nil && details.Coin != requested.nativeCoin {
			continue
		}
		if !req.Start.IsZero() && row.StatusTimestamp < req.Start.UnixMilli() {
			continue
		}
		if !req.End.IsZero() && row.StatusTimestamp >= req.End.UnixMilli() {
			continue
		}
		order, err := hlHistoricalOrder(meta, row)
		if err != nil {
			return exchange.OrderPage{}, hlMalformed(product, "OrderHistory", err.Error())
		}
		orders = append(orders, order)
		if req.Limit > 0 && len(orders) == req.Limit {
			break
		}
	}
	return exchange.OrderPage{Orders: orders, Page: historyPage("", req.Limit, req.Start, req.End)}, nil
}

func hlHistoricalOrder(meta hyperliquidMarketMeta, row hyperliquid.HistoricalOrder) (exchange.Order, error) {
	details := row.Order
	quantity, err := hlPositiveDecimal(details.OriginalSize)
	if err != nil {
		return exchange.Order{}, err
	}
	remaining, err := hlNonNegativeDecimal(details.RemainingSize)
	if err != nil || remaining.GreaterThan(quantity) {
		return exchange.Order{}, strconv.ErrSyntax
	}
	price, err := hlNonNegativeDecimal(details.LimitPrice)
	if err != nil {
		return exchange.Order{}, err
	}
	side, err := hlSide(string(details.Side))
	if err != nil {
		return exchange.Order{}, err
	}
	if details.IsTrigger {
		return exchange.Order{}, errors.New("trigger orders are outside the exchange order-history contract")
	}
	var orderType exchange.OrderType
	var policy exchange.LimitPolicy
	switch strings.ToLower(strings.TrimSpace(details.OrderType)) {
	case "market":
		orderType = exchange.OrderTypeMarket
		price = decimal.Zero
	case "limit":
		orderType = exchange.OrderTypeLimit
		switch strings.ToLower(strings.TrimSpace(details.TimeInForce)) {
		case "", "gtc":
			policy = exchange.LimitPolicyResting
		case "ioc":
			policy = exchange.LimitPolicyIOC
		case "alo":
			policy = exchange.LimitPolicyPostOnly
		default:
			return exchange.Order{}, errors.New("unsupported historical limit policy")
		}
	default:
		return exchange.Order{}, errors.New("unsupported historical order type")
	}
	clientID := ""
	if details.ClientOrderID != nil {
		clientID = hlPortableClientOrderID(*details.ClientOrderID)
	}
	return exchange.Order{
		Instrument:    meta.instrument.Symbol,
		OrderID:       strconv.FormatInt(details.OrderID, 10),
		ClientOrderID: clientID,
		Side:          side,
		Type:          orderType,
		Quantity:      quantity,
		LimitPrice:    price,
		LimitPolicy:   policy,
		ReduceOnly:    details.ReduceOnly,
		Filled:        quantity.Sub(remaining),
		Status:        row.Status,
		CreatedAt:     time.UnixMilli(details.Timestamp).UTC(),
		UpdatedAt:     time.UnixMilli(row.StatusTimestamp).UTC(),
	}, nil
}

func hlPortableClientOrderID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if !strings.HasPrefix(value, "0x") {
		return value
	}
	parsed := new(big.Int)
	if _, ok := parsed.SetString(strings.TrimPrefix(value, "0x"), 16); !ok || !parsed.IsInt64() || parsed.Sign() <= 0 {
		return ""
	}
	return parsed.String()
}

func hlNativeClientOrderID(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return ""
	}
	return fmt.Sprintf("0x%032x", parsed)
}

func hlOptionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func hlLimitTIF(policy exchange.LimitPolicy) hyperliquid.Tif {
	switch policy {
	case exchange.LimitPolicyIOC:
		return hyperliquid.TifIoc
	case exchange.LimitPolicyPostOnly:
		return hyperliquid.TifAlo
	default:
		return hyperliquid.TifGtc
	}
}
