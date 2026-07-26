package factoryclient

import (
	"context"
	"errors"
	"strconv"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
)

const binanceExactLookupOperation = "LookupExactOrder"

func (client *binanceSpotClient) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	if err := binanceSpotContextErr(ctx, binanceExactLookupOperation); err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	if err := request.Validate(); err != nil {
		return exchange.ExactOrderLookupResult{}, withExchangeOperation(err, binanceExactLookupOperation)
	}
	if request.Instrument == nil {
		return exchange.ExactOrderLookupResult{}, binanceSpotInvalid(binanceExactLookupOperation, "instrument is required")
	}
	nativeSymbol, instrument, err := binanceSpotSymbols(*request.Instrument, binanceExactLookupOperation)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	orderID, err := binanceExactLookupVenueOrderID(request.VenueOrderID, binanceSpotInvalid)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	resp, err := client.sdk.GetOrder(ctx, nativeSymbol, orderID, request.ClientOrderID)
	evidence := binanceExactLookupEvidence(instrument, request)
	if binanceSpotExactLookupNotFound(err) {
		return binanceExactLookupResult(exchange.ExactOrderNotFound, exchange.Order{}, evidence)
	}
	if err != nil {
		return exchange.ExactOrderLookupResult{}, binanceSpotNormalizeErr(err, binanceExactLookupOperation)
	}
	if resp == nil {
		return exchange.ExactOrderLookupResult{}, binanceSpotMalformed(binanceExactLookupOperation, "missing get order response")
	}
	order, err := binanceSpotOrder(*resp, nativeSymbol, nativeSymbol)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, binanceSpotMalformed(binanceExactLookupOperation, err.Error())
	}
	order.Status, err = exactPortableOrderStatus(order.Status)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, binanceSpotMalformed(binanceExactLookupOperation, err.Error())
	}
	if err := binanceExactLookupMatch(order, request); err != nil {
		return exchange.ExactOrderLookupResult{}, binanceSpotMalformed(binanceExactLookupOperation, err.Error())
	}
	return binanceExactLookupResult(exchange.ExactOrderFound, order, evidence)
}

func (client *binancePerpClient) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	if err := client.binancePerpReady(ctx, binanceExactLookupOperation); err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	if err := request.Validate(); err != nil {
		return exchange.ExactOrderLookupResult{}, withExchangeOperation(err, binanceExactLookupOperation)
	}
	if request.Instrument == nil {
		return exchange.ExactOrderLookupResult{}, binancePerpInvalidRequest(binanceExactLookupOperation, "instrument is required")
	}
	instrument, nativeSymbol, err := binancePerpRequestSymbols(*request.Instrument)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, binancePerpInvalidRequest(binanceExactLookupOperation, err.Error())
	}
	orderID, err := binanceExactLookupVenueOrderID(request.VenueOrderID, binancePerpInvalidRequest)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	resp, err := client.sdk.GetOrder(ctx, nativeSymbol, orderID, request.ClientOrderID)
	evidence := binanceExactLookupEvidence(instrument, request)
	if binancePerpExactLookupNotFound(err) {
		return binanceExactLookupResult(exchange.ExactOrderNotFound, exchange.Order{}, evidence)
	}
	if err != nil {
		return exchange.ExactOrderLookupResult{}, binancePerpNormalizeError(binanceExactLookupOperation, err)
	}
	if resp == nil {
		return exchange.ExactOrderLookupResult{}, binancePerpMalformed(binanceExactLookupOperation, "missing get order response")
	}
	order, err := binancePerpOrder(*resp)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, binancePerpMalformed(binanceExactLookupOperation, err.Error())
	}
	if order.NativeSymbol != nativeSymbol {
		return exchange.ExactOrderLookupResult{}, binancePerpMalformed(binanceExactLookupOperation, "response symbol does not match requested instrument")
	}
	order.Status, err = exactPortableOrderStatus(order.Status)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, binancePerpMalformed(binanceExactLookupOperation, err.Error())
	}
	if err := binanceExactLookupMatch(order, request); err != nil {
		return exchange.ExactOrderLookupResult{}, binancePerpMalformed(binanceExactLookupOperation, err.Error())
	}
	return binanceExactLookupResult(exchange.ExactOrderFound, order, evidence)
}

func binanceExactLookupVenueOrderID(value string, invalid func(string, string) error) (int64, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, invalid(binanceExactLookupOperation, "venue order id must be a positive decimal int64")
	}
	return parsed, nil
}

func binanceExactLookupEvidence(instrument exchange.Instrument, request exchange.ExactOrderLookupRequest) exchange.ExactOrderEvidence {
	evidence := exchange.ExactOrderEvidence{
		Scope: exchange.ExactOrderScope{
			Venue:      exchange.VenueBinance,
			Product:    instrument.Identity.Product,
			Instrument: &instrument,
		},
		Authoritative: true,
		Complete:      true,
	}
	if request.ClientOrderID != "" {
		evidence.Method = exchange.ExactOrderEvidenceByClientOrderID
		evidence.ObservedClientOrderID = request.ClientOrderID
		return evidence
	}
	evidence.Method = exchange.ExactOrderEvidenceByVenueOrderID
	evidence.ObservedVenueOrderID = request.VenueOrderID
	return evidence
}

func binanceExactLookupResult(status exchange.ExactOrderLookupStatus, order exchange.Order, evidence exchange.ExactOrderEvidence) (exchange.ExactOrderLookupResult, error) {
	result := exchange.ExactOrderLookupResult{Status: status, Order: order, Evidence: evidence}
	if err := result.Validate(); err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	return result, nil
}

func binanceExactLookupMatch(order exchange.Order, request exchange.ExactOrderLookupRequest) error {
	if request.ClientOrderID != "" && order.ClientOrderID != request.ClientOrderID {
		return errors.New("response client order id does not match requested identity")
	}
	if request.VenueOrderID != "" && order.OrderID != request.VenueOrderID {
		return errors.New("response venue order id does not match requested identity")
	}
	return nil
}

func binanceSpotExactLookupNotFound(err error) bool {
	if err == nil {
		return false
	}
	apiErr, ok := binanceSpotAPIError(err)
	return ok && apiErr.Code == -2013
}

func binancePerpExactLookupNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *binanceperp.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return apiErr.Code == -2013
	}
	return false
}

var (
	_ exchange.ExactOrderLookup = (*binanceSpotClient)(nil)
	_ exchange.ExactOrderLookup = (*binancePerpClient)(nil)
)
