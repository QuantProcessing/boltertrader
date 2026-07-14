package gate

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

func productForInstrument(inst *model.Instrument) (string, string, error) {
	if inst == nil {
		return "", "", fmt.Errorf("gate: instrument required: %w", errs.ErrSymbolNotFound)
	}
	switch inst.ID.Kind {
	case enums.KindSpot:
		return gatesdk.ProductSpot, "", nil
	case enums.KindPerp:
		if strings.EqualFold(inst.Settle, "USDT") {
			return gatesdk.ProductFuturesUSDT, gatesdk.SettleUSDT, nil
		}
		return "", "", fmt.Errorf("gate: unsupported settlement %q: %w", inst.Settle, errs.ErrNotSupported)
	default:
		return "", "", fmt.Errorf("gate: unsupported instrument kind %s: %w", inst.ID.Kind, errs.ErrNotSupported)
	}
}

func sideToGate(side enums.OrderSide) (string, error) {
	switch side {
	case enums.SideBuy:
		return "buy", nil
	case enums.SideSell:
		return "sell", nil
	default:
		return "", fmt.Errorf("gate: unsupported side %s: %w", side, errs.ErrNotSupported)
	}
}

func sideFromGate(side string) enums.OrderSide {
	switch strings.ToLower(side) {
	case "buy":
		return enums.SideBuy
	case "sell":
		return enums.SideSell
	default:
		return enums.SideUnknown
	}
}

func orderTypeToGate(t enums.OrderType) (string, error) {
	switch t {
	case enums.TypeMarket:
		return "market", nil
	case enums.TypeLimit:
		return "limit", nil
	default:
		return "", fmt.Errorf("gate: unsupported order type %s: %w", t, errs.ErrNotSupported)
	}
}

func orderTypeFromGate(value string) enums.OrderType {
	switch strings.ToLower(value) {
	case "market":
		return enums.TypeMarket
	case "limit":
		return enums.TypeLimit
	default:
		return enums.TypeUnknown
	}
}

func tifToGate(tif enums.TimeInForce) (string, error) {
	switch tif {
	case enums.TifUnknown, enums.TifGTC:
		return "gtc", nil
	case enums.TifIOC:
		return "ioc", nil
	case enums.TifFOK:
		return "fok", nil
	case enums.TifGTX:
		return "poc", nil
	default:
		return "", fmt.Errorf("gate: unsupported TIF %s: %w", tif, errs.ErrNotSupported)
	}
}

func tifFromGate(value string) enums.TimeInForce {
	switch strings.ToLower(value) {
	case "gtc":
		return enums.TifGTC
	case "ioc":
		return enums.TifIOC
	case "fok":
		return enums.TifFOK
	case "poc":
		return enums.TifGTX
	default:
		return enums.TifUnknown
	}
}

func statusFromGate(value string) enums.OrderStatus {
	switch strings.ToLower(value) {
	case "open":
		return enums.StatusNew
	case "closed", "finished":
		return enums.StatusFilled
	case "cancelled", "canceled":
		return enums.StatusCanceled
	case "expired":
		return enums.StatusExpired
	case "failed":
		return enums.StatusRejected
	default:
		return enums.StatusUnknown
	}
}

func orderStatusFromGate(status, finishAs string) enums.OrderStatus {
	if strings.EqualFold(status, "closed") || strings.EqualFold(status, "finished") {
		switch strings.ToLower(finishAs) {
		case "cancelled", "canceled":
			return enums.StatusCanceled
		case "expired":
			return enums.StatusExpired
		case "filled":
			return enums.StatusFilled
		}
	}
	return statusFromGate(status)
}

func positionSideFromGate(size int64) enums.PositionSide {
	switch {
	case size > 0:
		return enums.PosLong
	case size < 0:
		return enums.PosShort
	default:
		return enums.PosNet
	}
}

func sideFromSignedSize(size int64) enums.OrderSide {
	if size < 0 {
		return enums.SideSell
	}
	if size > 0 {
		return enums.SideBuy
	}
	return enums.SideUnknown
}

func timeFromSecondsString(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	wholeText, fracText, _ := strings.Cut(value, ".")
	seconds, err := strconv.ParseInt(wholeText, 10, 64)
	if err != nil {
		return time.Time{}
	}
	if len(fracText) > 9 {
		fracText = fracText[:9]
	}
	for len(fracText) < 9 {
		fracText += "0"
	}
	nanos := int64(0)
	if fracText != "" {
		var err error
		nanos, err = strconv.ParseInt(fracText, 10, 64)
		if err != nil {
			return time.Time{}
		}
	}
	return time.Unix(seconds, nanos)
}

func timeFromMillis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value)
}

func orderRequestToGateSpot(req model.OrderRequest, inst *model.Instrument) (gatesdk.Order, error) {
	product, _, err := productForInstrument(inst)
	if err != nil {
		return gatesdk.Order{}, err
	}
	if product != gatesdk.ProductSpot {
		return gatesdk.Order{}, fmt.Errorf("gate: spot order conversion cannot use %s: %w", inst.ID.Kind, errs.ErrNotSupported)
	}
	side, err := sideToGate(req.Side)
	if err != nil {
		return gatesdk.Order{}, err
	}
	orderType, err := orderTypeToGate(req.Type)
	if err != nil {
		return gatesdk.Order{}, err
	}
	tif := ""
	if req.Type == enums.TypeLimit {
		tif, err = tifToGate(req.TIF)
		if err != nil {
			return gatesdk.Order{}, err
		}
	}
	return gatesdk.Order{
		CurrencyPair: inst.VenueSymbol,
		Type:         orderType,
		Side:         side,
		Amount:       req.Quantity.String(),
		Price:        decimalStringOrEmpty(req.Price),
		TimeInForce:  tif,
		Text:         clientTextToGate(req.ClientID),
	}, nil
}

func orderFromGateSpotAction(resp *gatesdk.Order, req model.OrderRequest, now time.Time) model.Order {
	if req.AccountID == "" {
		req.AccountID = AccountIDUnified
	}
	if req.ClientID == "" && resp != nil {
		req.ClientID = clientIDFromGateText(resp.Text)
	}
	order := model.Order{Request: req, Status: enums.StatusNew, CreatedAt: now, UpdatedAt: now}
	if resp != nil {
		order.VenueOrderID = resp.ID
		if status := orderStatusFromGate(resp.Status, resp.FinishAs); status != enums.StatusUnknown {
			order.Status = status
		}
		order.FilledQty = dec(resp.FilledAmount)
		order.AvgFillPrice = dec(resp.AvgDealPrice)
	}
	return order
}

func orderFromGateSpotRecord(record gatesdk.Order, id model.InstrumentID, accountID string) model.Order {
	req := model.OrderRequest{
		AccountID:    accountID,
		InstrumentID: id,
		ClientID:     clientIDFromGateText(record.Text),
		Side:         sideFromGate(record.Side),
		Type:         orderTypeFromGate(record.Type),
		TIF:          tifFromGate(record.TimeInForce),
		Quantity:     dec(record.Amount),
		Price:        dec(record.Price),
		PositionSide: enums.PosNet,
	}
	return model.Order{
		Request:      req,
		VenueOrderID: record.ID,
		Status:       orderStatusFromGate(record.Status, record.FinishAs),
		FilledQty:    dec(record.FilledAmount),
		AvgFillPrice: dec(record.AvgDealPrice),
		CreatedAt:    firstNonZeroTime(timeFromMillisString(string(record.CreateTimeMS)), timeFromSecondsString(string(record.CreateTime))),
		UpdatedAt:    timeFromMillisString(string(record.UpdateTimeMS)),
	}
}

func fillFromGateSpotTrade(record gatesdk.SpotUserTrade, id model.InstrumentID, accountID string) model.Fill {
	return model.Fill{
		AccountID:    accountID,
		InstrumentID: id,
		VenueOrderID: record.OrderID,
		ClientID:     clientIDFromGateText(record.Text),
		TradeID:      record.ID,
		Side:         sideFromGate(record.Side),
		Liquidity:    liquidityFromGateRole(record.Role),
		Price:        dec(record.Price),
		Quantity:     dec(record.Amount),
		Fee:          dec(record.Fee).Abs(),
		FeeCurrency:  record.FeeCurrency,
		Timestamp:    firstNonZeroTime(timeFromMillisString(record.CreateTimeMS), timeFromSecondsString(record.CreateTime)),
	}
}

func orderRequestToGateFutures(req model.OrderRequest, inst *model.Instrument) (gatesdk.FuturesOrder, error) {
	product, settle, err := productForInstrument(inst)
	if err != nil {
		return gatesdk.FuturesOrder{}, err
	}
	if product != gatesdk.ProductFuturesUSDT || settle != gatesdk.SettleUSDT {
		return gatesdk.FuturesOrder{}, fmt.Errorf("gate: futures order conversion cannot use %s/%s: %w", inst.ID.Kind, inst.Settle, errs.ErrNotSupported)
	}
	if _, err := sideToGate(req.Side); err != nil {
		return gatesdk.FuturesOrder{}, err
	}
	tif := "gtc"
	if req.Type == enums.TypeLimit {
		tif, err = tifToGate(req.TIF)
		if err != nil {
			return gatesdk.FuturesOrder{}, err
		}
	} else if req.Type != enums.TypeMarket {
		return gatesdk.FuturesOrder{}, fmt.Errorf("gate: unsupported futures order type %s: %w", req.Type, errs.ErrNotSupported)
	}
	size := req.Quantity.Round(0).IntPart()
	if req.Side == enums.SideSell {
		size = -size
	}
	return gatesdk.FuturesOrder{
		Contract:   inst.VenueSymbol,
		Size:       size,
		Price:      decimalStringOrEmpty(req.Price),
		TIF:        tif,
		Text:       clientTextToGate(req.ClientID),
		ReduceOnly: req.ReduceOnly,
	}, nil
}

func orderFromGateFuturesAction(resp *gatesdk.FuturesOrder, req model.OrderRequest, now time.Time) model.Order {
	if req.AccountID == "" {
		req.AccountID = AccountIDUnified
	}
	if req.ClientID == "" && resp != nil {
		req.ClientID = clientIDFromGateText(resp.Text)
	}
	order := model.Order{Request: req, Status: enums.StatusNew, CreatedAt: now, UpdatedAt: now}
	if resp != nil {
		order.VenueOrderID = strconv.FormatInt(resp.ID, 10)
		if status := orderStatusFromGate(resp.Status, resp.FinishAs); status != enums.StatusUnknown {
			order.Status = status
		}
		order.FilledQty = filledFuturesQty(resp.Size, resp.Left)
		order.AvgFillPrice = dec(resp.FillPrice)
	}
	return order
}

func orderFromGateFuturesRecord(record gatesdk.FuturesOrder, id model.InstrumentID, accountID string, positionSides ...enums.PositionSide) model.Order {
	positionSide := positionSideFromGate(record.Size)
	if len(positionSides) > 0 {
		positionSide = positionSides[0]
	}
	req := model.OrderRequest{
		AccountID:    accountID,
		InstrumentID: id,
		ClientID:     clientIDFromGateText(record.Text),
		Side:         sideFromSignedSize(record.Size),
		Type:         futuresOrderTypeFromGate(record),
		TIF:          tifFromGate(record.TIF),
		Quantity:     decimal.NewFromInt(record.Size).Abs(),
		Price:        dec(record.Price),
		PositionSide: positionSide,
		ReduceOnly:   record.ReduceOnly || record.IsReduceOnly,
	}
	return model.Order{
		Request:      req,
		VenueOrderID: strconv.FormatInt(record.ID, 10),
		Status:       orderStatusFromGate(record.Status, record.FinishAs),
		FilledQty:    filledFuturesQty(record.Size, record.Left),
		AvgFillPrice: dec(record.FillPrice),
		CreatedAt:    timeFromSecondsString(string(record.CreateTime)),
		UpdatedAt:    timeFromSecondsString(string(record.UpdateTime)),
	}
}

func futuresOrderTypeFromGate(record gatesdk.FuturesOrder) enums.OrderType {
	if record.Price == "" || record.Price == "0" {
		return enums.TypeMarket
	}
	return enums.TypeLimit
}

func filledFuturesQty(size, left int64) decimal.Decimal {
	filled := absInt64(size) - absInt64(left)
	if filled < 0 {
		filled = 0
	}
	return decimal.NewFromInt(filled)
}

func fillFromGateFuturesTrade(record gatesdk.MyFuturesTrade, id model.InstrumentID, accountID string) model.Fill {
	return model.Fill{
		AccountID:    accountID,
		InstrumentID: id,
		VenueOrderID: strconv.FormatInt(record.OrderID, 10),
		ClientID:     clientIDFromGateText(record.Text),
		TradeID:      strconv.FormatInt(record.ID, 10),
		Side:         sideFromSignedSize(record.Size),
		Liquidity:    liquidityFromGateRole(record.Role),
		Price:        dec(record.Price),
		Quantity:     decimal.NewFromInt(record.Size).Abs(),
		Fee:          dec(record.Fee).Abs(),
		FeeCurrency:  "USDT",
		Timestamp:    timeFromSecondsString(string(record.CreateTime)),
	}
}

func positionFromGate(record gatesdk.Position, resolve func(string) model.InstrumentID, accountID string, now time.Time) model.Position {
	positionSide, quantity, ok := gateFuturesPositionSideAndQuantity(record)
	if !ok {
		return model.Position{}
	}
	return model.Position{
		AccountID:     accountID,
		InstrumentID:  resolve(record.Contract),
		Side:          positionSide,
		Quantity:      quantity,
		EntryPrice:    dec(record.EntryPrice),
		MarkPrice:     dec(record.MarkPrice),
		UnrealizedPnL: dec(record.UnrealisedPNL),
		Leverage:      dec(record.Leverage),
		UpdatedAt:     firstNonZeroTime(timeFromSeconds(record.UpdateTime), now),
	}
}

func gateFuturesPositionSideAndQuantity(record gatesdk.Position) (enums.PositionSide, decimal.Decimal, bool) {
	quantity := decimal.NewFromInt(record.Size)
	switch strings.ToLower(strings.TrimSpace(record.Mode)) {
	case "single":
		return enums.PosNet, quantity, true
	case "dual_long":
		return enums.PosLong, quantity.Abs(), true
	case "dual_short":
		return enums.PosShort, quantity.Abs().Neg(), true
	default:
		return enums.PosNet, decimal.Zero, false
	}
}

func liquidityFromGateRole(role string) enums.LiquiditySide {
	switch strings.ToLower(role) {
	case "maker":
		return enums.LiqMaker
	case "taker":
		return enums.LiqTaker
	default:
		return enums.LiqUnknown
	}
}

func clientTextToGate(clientID string) string {
	if clientID == "" {
		return ""
	}
	if strings.HasPrefix(clientID, "t-") {
		return clientID
	}
	return "t-" + clientID
}

func clientIDFromGateText(text string) string {
	return strings.TrimPrefix(text, "t-")
}

func firstNonZero(values ...decimal.Decimal) decimal.Decimal {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return decimal.Zero
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func firstPositiveIntInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func parseGateOrderID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("gate: invalid futures order id %q: %w", value, err)
	}
	return id, nil
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func decimalStringOrEmpty(value decimal.Decimal) string {
	if value.IsZero() {
		return ""
	}
	return value.String()
}

func gateKinds(scope []enums.InstrumentKind) []enums.InstrumentKind {
	if len(scope) > 0 {
		return append([]enums.InstrumentKind(nil), scope...)
	}
	return []enums.InstrumentKind{enums.KindSpot}
}

func gateTradingKinds() []enums.InstrumentKind {
	return []enums.InstrumentKind{enums.KindSpot, enums.KindPerp}
}

func hasKind(scope []enums.InstrumentKind, want enums.InstrumentKind) bool {
	for _, kind := range scope {
		if kind == want {
			return true
		}
	}
	return false
}

func kindsForProducts(products []string) []enums.InstrumentKind {
	out := make([]enums.InstrumentKind, 0, len(products))
	seen := map[enums.InstrumentKind]bool{}
	for _, product := range products {
		var kind enums.InstrumentKind
		switch product {
		case gatesdk.ProductSpot:
			kind = enums.KindSpot
		case gatesdk.ProductFuturesUSDT:
			kind = enums.KindPerp
		default:
			continue
		}
		if !seen[kind] {
			seen[kind] = true
			out = append(out, kind)
		}
	}
	return gateKinds(out)
}
