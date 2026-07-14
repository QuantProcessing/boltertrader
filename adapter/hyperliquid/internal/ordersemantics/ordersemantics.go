package ordersemantics

import (
	"strings"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// FromWire reconstructs the immutable order semantics exposed by
// Hyperliquid's orderStatus/orderUpdates schemas. Unknown or ambiguous values
// remain Unknown so callers can fail closed instead of silently coercing them.
func FromWire(orderType, tif string, isTrigger bool, triggerPx string) (enums.OrderType, enums.TimeInForce, decimal.Decimal) {
	normalizedType := strings.ToLower(strings.TrimSpace(orderType))
	typeValue := enums.TypeUnknown
	switch normalizedType {
	case "limit":
		typeValue = enums.TypeLimit
	case "market":
		typeValue = enums.TypeMarket
	case "stop market":
		typeValue = enums.TypeStopMarket
	case "stop limit":
		typeValue = enums.TypeStopLimit
	case "take profit market":
		typeValue = enums.TypeMarketIfTouched
	case "take profit limit":
		typeValue = enums.TypeLimitIfTouched
	}

	tifValue := enums.TifUnknown
	switch strings.ToLower(strings.TrimSpace(tif)) {
	case "gtc":
		tifValue = enums.TifGTC
	case "ioc":
		tifValue = enums.TifIOC
	case "alo":
		tifValue = enums.TifGTX
	case "fok":
		tifValue = enums.TifFOK
	}
	if typeValue == enums.TypeUnknown && !isTrigger && tifValue != enums.TifUnknown {
		typeValue = enums.TypeLimit
	}
	if typeValue == enums.TypeMarket && !isTrigger && tifValue == enums.TifUnknown {
		tifValue = enums.TifIOC
	}
	if isTrigger && typeValue != enums.TypeStopMarket && typeValue != enums.TypeStopLimit && typeValue != enums.TypeMarketIfTouched && typeValue != enums.TypeLimitIfTouched {
		typeValue = enums.TypeUnknown
	}

	trigger := decimal.Zero
	if triggerPx != "" {
		if value, err := decimal.NewFromString(triggerPx); err == nil {
			trigger = value
		}
	}
	return typeValue, tifValue, trigger
}

// MergeKnownRequest preserves immutable semantics from a request that this
// process submitted when a later venue update omits or contradicts those
// fields. Price and quantity remain venue-authoritative because modify can
// change them.
func MergeKnownRequest(known, update model.OrderRequest) model.OrderRequest {
	if update.AccountID == "" {
		update.AccountID = known.AccountID
	}
	if update.InstrumentID.Symbol == "" {
		update.InstrumentID = known.InstrumentID
	}
	if update.ClientID == "" {
		update.ClientID = known.ClientID
	}
	if known.Side != enums.SideUnknown {
		update.Side = known.Side
	}
	if known.Type != enums.TypeUnknown {
		update.Type = known.Type
		update.TIF = known.TIF
		update.TriggerPrice = known.TriggerPrice
		update.ActivationPrice = known.ActivationPrice
		update.TrailingOffsetBps = known.TrailingOffsetBps
		update.ReduceOnly = known.ReduceOnly
	}
	if known.ReduceOnly {
		update.ReduceOnly = true
	}
	if update.TriggerPrice.IsZero() {
		update.TriggerPrice = known.TriggerPrice
	}
	update.PositionSide = known.PositionSide
	if update.Quantity.IsZero() {
		update.Quantity = known.Quantity
	}
	if update.Price.IsZero() {
		update.Price = known.Price
	}
	if update.Venue == nil {
		update.Venue = known.Venue
	}
	return update
}
