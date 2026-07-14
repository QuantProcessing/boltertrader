package perp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

var binanceDemoTerminalStatuses = []string{"FILLED", "CANCELED", "EXPIRED", "EXPIRED_IN_MATCH", "REJECTED"}

func isBinanceDemoTerminalStatus(status string) bool {
	switch strings.ToUpper(status) {
	case "FILLED", "CANCELED", "EXPIRED", "EXPIRED_IN_MATCH", "REJECTED":
		return true
	default:
		return false
	}
}

type demoAcceptanceSymbolSpec struct {
	VenueSymbol string
	PriceTick   decimal.Decimal
	SizeStep    decimal.Decimal
	MinQty      decimal.Decimal
	MinNotional decimal.Decimal
}

func normalizeDemoAcceptanceSymbol(symbol string) string {
	replacer := strings.NewReplacer("-", "", "_", "", "/", "", " ", "")
	return strings.ToUpper(replacer.Replace(symbol))
}

func demoAcceptanceSymbolSpecFromExchangeInfo(info *sdkperp.ExchangeInfoResponse, symbol string) (demoAcceptanceSymbolSpec, error) {
	if info == nil {
		return demoAcceptanceSymbolSpec{}, fmt.Errorf("missing exchange info")
	}
	want := normalizeDemoAcceptanceSymbol(symbol)
	for _, candidate := range info.Symbols {
		if normalizeDemoAcceptanceSymbol(candidate.Symbol) != want {
			continue
		}
		tick, step, minQty, minNotional := extractFilters(candidate.Filters)
		spec := demoAcceptanceSymbolSpec{
			VenueSymbol: candidate.Symbol,
			PriceTick:   tick,
			SizeStep:    step,
			MinQty:      minQty,
			MinNotional: minNotional,
		}
		if spec.PriceTick.IsZero() || spec.SizeStep.IsZero() || spec.MinQty.IsZero() || spec.MinNotional.IsZero() {
			return demoAcceptanceSymbolSpec{}, fmt.Errorf("symbol %s has incomplete exchange filters: %+v", candidate.Symbol, spec)
		}
		return spec, nil
	}
	return demoAcceptanceSymbolSpec{}, fmt.Errorf("symbol %s not found in exchange info", want)
}

func selectDemoAcceptanceOrderQuantity(spec demoAcceptanceSymbolSpec, configuredQty, maxNotional, refPrice decimal.Decimal) (decimal.Decimal, error) {
	return selectDemoAcceptanceOrderQuantityForPriceBand(spec, configuredQty, maxNotional, refPrice, refPrice)
}

func selectDemoAcceptanceOrderQuantityForPriceBand(spec demoAcceptanceSymbolSpec, configuredQty, maxNotional, minNotionalPrice, maxNotionalPrice decimal.Decimal) (decimal.Decimal, error) {
	if spec.SizeStep.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("symbol %s has invalid size step %s", spec.VenueSymbol, spec.SizeStep)
	}
	if maxNotional.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("max notional must be positive")
	}
	if minNotionalPrice.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("min-notional reference price must be positive")
	}
	if maxNotionalPrice.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("max-notional reference price must be positive")
	}

	qty := configuredQty
	if qty.IsZero() {
		minByNotional := spec.MinNotional.Div(minNotionalPrice)
		qty = ceilDecimalToStep(maxDecimal(spec.MinQty, minByNotional), spec.SizeStep)
	} else {
		if qty.LessThan(spec.MinQty) {
			return decimal.Zero, fmt.Errorf("configured quantity %s below min quantity %s for %s", qty, spec.MinQty, spec.VenueSymbol)
		}
		if !qty.Equal(ceilDecimalToStep(qty, spec.SizeStep)) {
			return decimal.Zero, fmt.Errorf("configured quantity %s is not aligned to step %s for %s", qty, spec.SizeStep, spec.VenueSymbol)
		}
	}

	minCheckedNotional := qty.Mul(minNotionalPrice)
	if minCheckedNotional.LessThan(spec.MinNotional) {
		return decimal.Zero, fmt.Errorf("quantity %s notional %s below min notional %s for %s", qty, minCheckedNotional, spec.MinNotional, spec.VenueSymbol)
	}
	maxCheckedNotional := qty.Mul(maxNotionalPrice)
	if maxCheckedNotional.GreaterThan(maxNotional) {
		return decimal.Zero, fmt.Errorf("quantity %s notional %s exceeds max notional %s for %s", qty, maxCheckedNotional, maxNotional, spec.VenueSymbol)
	}
	return qty, nil
}

func ceilDecimalToStep(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Ceil().Mul(step)
}

func floorDecimalToStep(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Floor().Mul(step)
}

func maxDecimal(a, b decimal.Decimal) decimal.Decimal {
	if a.GreaterThan(b) {
		return a
	}
	return b
}

type demoAcceptanceCleanupMetadata struct {
	Symbol         string
	Side           string
	Quantity       decimal.Decimal
	VenueOrderIDs  []string
	ClientOrderIDs []string
	Exposure       decimal.Decimal
}

func (m demoAcceptanceCleanupMetadata) Remediation() string {
	return fmt.Sprintf(
		"Binance Demo cleanup failed: symbol=%s side=%s quantity=%s exposure=%s venueOrderIDs=%s clientOrderIDs=%s. Manually cancel open orders and flatten remaining exposure in Binance Futures Demo Trading.",
		m.Symbol,
		m.Side,
		m.Quantity,
		m.Exposure,
		strings.Join(m.VenueOrderIDs, ","),
		strings.Join(m.ClientOrderIDs, ","),
	)
}

type demoAcceptanceCleanupState struct {
	needed                 bool
	meta                   demoAcceptanceCleanupMetadata
	openVenueOrders        map[string]struct{}
	clientIDByVenueOrderID map[string]string
	unresolvedClientIDs    map[string]struct{}
	armedClientID          string
	confirmedFill          decimal.Decimal
	closeAttempted         bool
}

type demoAcceptanceTrackedOrder struct {
	VenueOrderID string
	ClientID     string
}

func newDemoAcceptanceCleanupState(symbol string, qty decimal.Decimal) demoAcceptanceCleanupState {
	return demoAcceptanceCleanupState{
		meta: demoAcceptanceCleanupMetadata{
			Symbol:   symbol,
			Quantity: qty,
		},
		openVenueOrders:        make(map[string]struct{}),
		clientIDByVenueOrderID: make(map[string]string),
		unresolvedClientIDs:    make(map[string]struct{}),
	}
}

func (s *demoAcceptanceCleanupState) Arm(side enums.OrderSide, clientID string) {
	s.needed = true
	s.meta.Side = side.String()
	if clientID != "" {
		s.meta.ClientOrderIDs = append(s.meta.ClientOrderIDs, clientID)
		s.armedClientID = clientID
		s.unresolvedClientIDs[clientID] = struct{}{}
	}
}

func (s *demoAcceptanceCleanupState) RecordVenueOrderID(venueOrderID string) {
	s.ResolveClientOrder(s.armedClientID, venueOrderID)
}

func (s *demoAcceptanceCleanupState) ResolveClientOrder(clientID, venueOrderID string) {
	if venueOrderID == "" {
		return
	}
	if _, exists := s.clientIDByVenueOrderID[venueOrderID]; !exists {
		s.meta.VenueOrderIDs = append(s.meta.VenueOrderIDs, venueOrderID)
	}
	s.openVenueOrders[venueOrderID] = struct{}{}
	s.clientIDByVenueOrderID[venueOrderID] = clientID
	delete(s.unresolvedClientIDs, clientID)
}

func (s *demoAcceptanceCleanupState) MarkOrderTerminal(venueOrderID string) {
	delete(s.unresolvedClientIDs, s.clientIDByVenueOrderID[venueOrderID])
	delete(s.openVenueOrders, venueOrderID)
}

func (s *demoAcceptanceCleanupState) MarkClientOrderTerminal(clientID string) {
	delete(s.unresolvedClientIDs, clientID)
}

func (s *demoAcceptanceCleanupState) ConfirmFill(qty decimal.Decimal) {
	if qty.IsPositive() {
		if qty.GreaterThan(s.confirmedFill) {
			s.confirmedFill = qty
		}
		s.needed = true
	}
}

func (s demoAcceptanceCleanupState) CloseAuthorized() bool {
	return s.confirmedFill.IsPositive() && !s.closeAttempted
}

func (s demoAcceptanceCleanupState) CloseLimit() decimal.Decimal {
	return s.confirmedFill
}

func (s *demoAcceptanceCleanupState) BeginCloseAttempt() {
	s.closeAttempted = true
}

func (s demoAcceptanceCleanupState) CancellableVenueOrderIDs() []string {
	ids := make([]string, 0, len(s.openVenueOrders))
	for id := range s.openVenueOrders {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s demoAcceptanceCleanupState) TrackedOpenOrders() []demoAcceptanceTrackedOrder {
	orders := s.ResolvedOpenOrders()
	orders = append(orders, s.UnresolvedClientOrders()...)
	return orders
}

func (s demoAcceptanceCleanupState) ResolvedOpenOrders() []demoAcceptanceTrackedOrder {
	venueOrderIDs := s.CancellableVenueOrderIDs()
	orders := make([]demoAcceptanceTrackedOrder, 0, len(venueOrderIDs))
	for _, venueOrderID := range venueOrderIDs {
		orders = append(orders, demoAcceptanceTrackedOrder{
			VenueOrderID: venueOrderID,
			ClientID:     s.clientIDByVenueOrderID[venueOrderID],
		})
	}
	return orders
}

func (s demoAcceptanceCleanupState) UnresolvedClientOrders() []demoAcceptanceTrackedOrder {
	clientIDs := make([]string, 0, len(s.unresolvedClientIDs))
	for clientID := range s.unresolvedClientIDs {
		clientIDs = append(clientIDs, clientID)
	}
	sort.Strings(clientIDs)
	orders := make([]demoAcceptanceTrackedOrder, 0, len(clientIDs))
	for _, clientID := range clientIDs {
		orders = append(orders, demoAcceptanceTrackedOrder{ClientID: clientID})
	}
	return orders
}

func (s *demoAcceptanceCleanupState) SetExposure(exposure decimal.Decimal) {
	s.meta.Exposure = exposure
}

func (s *demoAcceptanceCleanupState) MarkClean() {
	s.needed = false
	s.meta.Exposure = decimal.Zero
	s.confirmedFill = decimal.Zero
	s.closeAttempted = false
	s.armedClientID = ""
	clear(s.openVenueOrders)
	clear(s.clientIDByVenueOrderID)
	clear(s.unresolvedClientIDs)
}

func (s demoAcceptanceCleanupState) Needed() bool {
	return s.needed
}

func (s demoAcceptanceCleanupState) Metadata() demoAcceptanceCleanupMetadata {
	return s.meta
}

func demoExposureFromPositions(positions []model.Position, id model.InstrumentID) (decimal.Decimal, error) {
	var exposure decimal.Decimal
	nonZero := 0
	var reports []string
	for _, position := range positions {
		if position.InstrumentID != id || position.Quantity.IsZero() {
			continue
		}
		nonZero++
		exposure = position.Quantity
		reports = append(reports, fmt.Sprintf("side=%s qty=%s", position.Side, position.Quantity))
	}
	if nonZero > 1 {
		return decimal.Zero, fmt.Errorf("%s has %d non-zero position reports (%s); offsetting hedge legs are not flat", id, nonZero, strings.Join(reports, ", "))
	}
	return exposure, nil
}

func validateBinanceDemoFill(resp *sdkperp.OrderResponse, maxNotional decimal.Decimal) (decimal.Decimal, error) {
	if resp == nil {
		return decimal.Zero, fmt.Errorf("missing Binance Demo fill response")
	}
	if maxNotional.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("max notional must be positive")
	}
	qty, err := decimal.NewFromString(resp.ExecutedQty)
	if err != nil || !qty.IsPositive() {
		return decimal.Zero, fmt.Errorf("invalid executed quantity %q", resp.ExecutedQty)
	}
	cumQuote, err := decimal.NewFromString(resp.CumQuote)
	if err != nil || !cumQuote.IsPositive() {
		return qty, fmt.Errorf("invalid cumulative quote %q", resp.CumQuote)
	}
	if cumQuote.GreaterThan(maxNotional) {
		return qty, fmt.Errorf("executed cumulative quote %s exceeds configured Demo cap %s", cumQuote, maxNotional)
	}
	return qty, nil
}

func demoFillOrderRequest(id model.InstrumentID, clientID string, qty, maxPrice decimal.Decimal) model.OrderRequest {
	return model.OrderRequest{
		InstrumentID: id,
		ClientID:     clientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     qty,
		Price:        maxPrice,
		PositionSide: enums.PosNet,
	}
}
