package perp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

type demoPerpSpec struct {
	VenueSymbol    string
	BaseCurrency   string
	QuoteCurrency  string
	SettleCurrency string
	PriceTick      decimal.Decimal
	SizeStep       decimal.Decimal
	MinQty         decimal.Decimal
	CtVal          decimal.Decimal
	CtValCcy       string
}

func demoPerpSpecFromOKX(in *okx.Instrument) (demoPerpSpec, error) {
	if in == nil {
		return demoPerpSpec{}, fmt.Errorf("missing instrument")
	}
	settle := in.SettleCcy
	if settle == "" {
		settle = in.SettCcy
	}
	spec := demoPerpSpec{
		VenueSymbol:    in.InstId,
		BaseCurrency:   in.BaseCcy,
		QuoteCurrency:  in.QuoteCcy,
		SettleCurrency: settle,
		PriceTick:      dec(in.TickSz),
		SizeStep:       dec(in.LotSz),
		MinQty:         dec(in.MinSz),
		CtVal:          dec(in.CtVal),
		CtValCcy:       in.CtValCcy,
	}
	if spec.PriceTick.IsZero() || spec.SizeStep.IsZero() || spec.MinQty.IsZero() || spec.CtVal.IsZero() {
		return demoPerpSpec{}, fmt.Errorf("incomplete OKX Perp instrument metadata: %+v", spec)
	}
	return spec, nil
}

func okxDemoEndpoints(t testHelper, cfg testenv.OKXDemoConfig) okx.EndpointURLs {
	t.Helper()
	if cfg.HostProfile == testenv.OKXDemoHostProfileCustom {
		return okxDemoCustomEndpoints(cfg)
	}
	endpoints, err := okx.DefaultEndpointURLs(okx.Simulated, okx.DemoHostProfile(cfg.HostProfile))
	if err != nil {
		t.Fatalf("OKX Demo endpoints: %v", err)
	}
	if cfg.RESTBaseURL != "" {
		endpoints.REST = cfg.RESTBaseURL
	}
	if cfg.WSBaseURL != "" {
		base := strings.TrimRight(cfg.WSBaseURL, "/")
		endpoints.WSPublic = base + "/ws/v5/public"
		endpoints.WSPrivate = base + "/ws/v5/private"
		endpoints.WSBusiness = base + "/ws/v5/business"
	}
	return endpoints
}

func okxDemoCustomEndpoints(cfg testenv.OKXDemoConfig) okx.EndpointURLs {
	endpoints := okx.EndpointURLs{REST: cfg.RESTBaseURL}
	if cfg.WSBaseURL != "" {
		base := strings.TrimRight(cfg.WSBaseURL, "/")
		endpoints.WSPublic = base + "/ws/v5/public"
		endpoints.WSPrivate = base + "/ws/v5/private"
		endpoints.WSBusiness = base + "/ws/v5/business"
	}
	return endpoints
}

func validateDemoPerpAccountMode(ctx context.Context, rest *okx.Client) error {
	configs, err := rest.GetAccountConfig(ctx)
	if err != nil {
		return fmt.Errorf("load OKX Demo account config: %w", err)
	}
	if len(configs) == 0 {
		return fmt.Errorf("load OKX Demo account config: empty response")
	}
	cfg := configs[0]
	summary := fmt.Sprintf(
		"acctLv=%q(%s) posMode=%q type=%q",
		cfg.AcctLv,
		cfg.AccountLevel(),
		cfg.PosMode,
		cfg.Type,
	)
	if cfg.AccountLevel() == okx.AccountLevelSimple {
		return fmt.Errorf("%s does not support OKX SWAP demo acceptance; switch the OKX Demo account to a margin/futures-capable account mode in OKX Web/App before running this gate", summary)
	}
	return nil
}

type testHelper interface {
	Helper()
	Fatalf(format string, args ...any)
}

func selectDemoPerpQuantity(spec demoPerpSpec, maxNotional, refPrice decimal.Decimal) (decimal.Decimal, error) {
	perContract := demoPerpNotionalPerContract(spec, refPrice)
	if maxNotional.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("max notional must be positive")
	}
	if perContract.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("invalid per-contract notional %s", perContract)
	}
	maxQty := floorDecimalToStep(maxNotional.Div(perContract), spec.SizeStep)
	qty := ceilDecimalToStep(spec.MinQty, spec.SizeStep)
	if qty.GreaterThan(maxQty) {
		return decimal.Zero, fmt.Errorf("min contracts %s exceed max notional %s at price %s with per-contract notional %s", qty, maxNotional, refPrice, perContract)
	}
	target := maxNotional.Div(perContract).Div(decimal.NewFromInt(2))
	qty = floorDecimalToStep(maxDecimal(qty, target), spec.SizeStep)
	if qty.LessThan(spec.MinQty) {
		qty = ceilDecimalToStep(spec.MinQty, spec.SizeStep)
	}
	if qty.GreaterThan(maxQty) {
		qty = maxQty
	}
	if qty.IsZero() {
		return decimal.Zero, fmt.Errorf("selected zero contracts")
	}
	return qty, nil
}

func demoPerpNotionalPerContract(spec demoPerpSpec, price decimal.Decimal) decimal.Decimal {
	if strings.EqualFold(spec.CtValCcy, spec.QuoteCurrency) || strings.EqualFold(spec.CtValCcy, spec.SettleCurrency) {
		return spec.CtVal
	}
	return spec.CtVal.Mul(price)
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

func demoClientOrderID(kind string) string {
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	prefix := "btdop"
	kind = demoClientOrderIDKind(kind, 32-len(prefix)-len(suffix))
	return prefix + kind + suffix
}

func demoClientOrderIDKind(kind string, maxLen int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(kind) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if maxLen > 0 && b.Len() >= maxLen {
			break
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

func collectDemoExecEvents(events <-chan contract.ExecEnvelope) chan contract.ExecEvent {
	out := make(chan contract.ExecEvent, 64)
	go func() {
		for envelope := range events {
			select {
			case out <- envelope.Payload:
			default:
			}
		}
		close(out)
	}()
	return out
}

func waitForDemoOrderStatus(ctx context.Context, rest *okx.Client, instID, clientID string, statuses ...string) (*okx.Order, error) {
	want := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		want[strings.ToLower(status)] = struct{}{}
	}
	var lastErr error
	var lastStatus string
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		orders, err := rest.GetOrder(ctx, instID, "", clientID)
		if err == nil && len(orders) > 0 {
			order := orders[0]
			lastStatus = string(order.State)
			if _, ok := want[strings.ToLower(string(order.State))]; ok {
				return &order, nil
			}
		} else if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out waiting for %s to reach %v; lastStatus=%q lastErr=%v: %w", clientID, statuses, lastStatus, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForDemoExecObservation(ctx context.Context, events <-chan contract.ExecEvent, clientID, venueOrderID string) error {
	timeout, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	for {
		select {
		case <-timeout.Done():
			return timeout.Err()
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("execution event stream closed")
			}
			switch ev := event.(type) {
			case contract.FillEvent:
				if ev.Fill.ClientID == clientID || ev.Fill.VenueOrderID == venueOrderID {
					return nil
				}
			case contract.OrderEvent:
				if ev.Order.Request.ClientID == clientID || ev.Order.VenueOrderID == venueOrderID {
					if ev.Order.Status == enums.StatusFilled || !ev.Order.FilledQty.IsZero() {
						return nil
					}
				}
			}
		}
	}
}

func demoCurrentExposure(ctx context.Context, adapter *Adapter, id model.InstrumentID) (decimal.Decimal, error) {
	positions, err := adapter.Account.Positions(ctx)
	if err != nil {
		return decimal.Zero, err
	}
	for _, position := range positions {
		if position.InstrumentID == id {
			return position.Quantity, nil
		}
	}
	return decimal.Zero, nil
}

func waitForDemoExposure(ctx context.Context, adapter *Adapter, id model.InstrumentID, minAbs decimal.Decimal) (decimal.Decimal, error) {
	var lastErr error
	var last decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		exposure, err := demoCurrentExposure(ctx, adapter, id)
		if err == nil {
			last = exposure
			if exposure.Abs().GreaterThanOrEqual(minAbs.Abs()) {
				return exposure, nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return decimal.Zero, fmt.Errorf("timed out waiting for exposure >= %s; last=%s lastErr=%v: %w", minAbs, last, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForDemoFlat(ctx context.Context, adapter *Adapter, id model.InstrumentID) error {
	var lastErr error
	var last decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		exposure, err := demoCurrentExposure(ctx, adapter, id)
		if err == nil {
			last = exposure
			if exposure.IsZero() {
				return nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for flat position; last=%s lastErr=%v: %w", last, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

type demoPerpCleanupState struct {
	needed         bool
	spec           demoPerpSpec
	qty            decimal.Decimal
	exposure       decimal.Decimal
	venueOrderIDs  []string
	clientOrderIDs []string
}

func newDemoPerpCleanupState(spec demoPerpSpec, qty decimal.Decimal) demoPerpCleanupState {
	return demoPerpCleanupState{spec: spec, qty: qty}
}

func (s *demoPerpCleanupState) Arm(clientID string) {
	s.needed = true
	if clientID != "" {
		s.clientOrderIDs = append(s.clientOrderIDs, clientID)
	}
}

func (s *demoPerpCleanupState) RecordVenueOrderID(id string) {
	if id != "" {
		s.venueOrderIDs = append(s.venueOrderIDs, id)
	}
}

func (s *demoPerpCleanupState) SetExposure(exposure decimal.Decimal) { s.exposure = exposure }

func (s *demoPerpCleanupState) MarkClean() {
	s.needed = false
	s.exposure = decimal.Zero
}

func (s demoPerpCleanupState) Remediation() string {
	return fmt.Sprintf(
		"OKX Perp Demo cleanup failed: symbol=%s quantity=%s exposure=%s venueOrderIDs=%s clientOrderIDs=%s. Manually cancel open Demo SWAP orders and flatten remaining exposure.",
		s.spec.VenueSymbol,
		s.qty,
		s.exposure,
		strings.Join(s.venueOrderIDs, ","),
		strings.Join(s.clientOrderIDs, ","),
	)
}

func cleanupOKXPerpDemo(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoPerpSpec, state *demoPerpCleanupState) error {
	cancelErr := adapter.Execution.CancelAll(ctx, id)
	if err := closeOKXPerpDemoExposure(ctx, adapter, id, spec); err != nil {
		return err
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, id); err != nil {
		return err
	}
	if cancelErr != nil {
		return fmt.Errorf("cancel all OKX Perp Demo open orders: %w", cancelErr)
	}
	if err := waitForDemoFlat(ctx, adapter, id); err != nil {
		exposure, _ := demoCurrentExposure(ctx, adapter, id)
		state.SetExposure(exposure)
		return err
	}
	state.SetExposure(decimal.Zero)
	return nil
}

func closeOKXPerpDemoExposure(ctx context.Context, adapter *Adapter, id model.InstrumentID, spec demoPerpSpec) error {
	for attempt := 0; attempt < 3; attempt++ {
		exposure, err := demoCurrentExposure(ctx, adapter, id)
		if err != nil {
			return err
		}
		if exposure.IsZero() {
			return nil
		}
		book, err := adapter.Market.OrderBook(ctx, id, 5)
		if err != nil {
			return err
		}
		side := enums.SideSell
		price := decimal.Zero
		if exposure.IsPositive() {
			if len(book.Bids) == 0 {
				return fmt.Errorf("cannot close long exposure: empty bid book")
			}
			price = floorDecimalToStep(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), spec.PriceTick)
		} else {
			side = enums.SideBuy
			if len(book.Asks) == 0 {
				return fmt.Errorf("cannot close short exposure: empty ask book")
			}
			price = ceilDecimalToStep(book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), spec.PriceTick)
		}
		_, err = adapter.Execution.Submit(ctx, model.OrderRequest{
			InstrumentID: id,
			ClientID:     demoClientOrderID("close"),
			Side:         side,
			Type:         enums.TypeLimit,
			TIF:          enums.TifIOC,
			Quantity:     exposure.Abs(),
			Price:        price,
			PositionSide: enums.PosNet,
			ReduceOnly:   true,
		})
		if err != nil {
			return err
		}
		if err := waitForDemoFlat(ctx, adapter, id); err == nil {
			return nil
		}
	}
	return waitForDemoFlat(ctx, adapter, id)
}

func waitForNoDemoOpenOrders(ctx context.Context, adapter *Adapter, id model.InstrumentID) error {
	var lastErr error
	var lastOpen int
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		open, err := adapter.Execution.OpenOrders(ctx, id)
		if err == nil && len(open) == 0 {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastOpen = len(open)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for no open orders; lastOpen=%d lastErr=%v: %w", lastOpen, lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}
