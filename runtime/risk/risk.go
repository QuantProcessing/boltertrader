// Package risk provides pre-trade risk checks — the safety net that sits in
// front of order submission. The engine is venue-neutral and reads only the
// Cache + Instrument metadata, so it behaves consistently across adapters.
package risk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/accounting"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/orderstate"
	"github.com/shopspring/decimal"
)

// ErrRiskRejected is the sentinel wrapped by every risk rejection so callers can
// errors.Is against it.
var ErrRiskRejected = errors.New("risk: order rejected")

// Limits configures the pre-trade checks. A zero value disables that check.
type Limits struct {
	// MaxOrderQty caps the quantity of any single order.
	MaxOrderQty decimal.Decimal
	// MaxOrderNotional caps price*qty*contract-multiplier of any single order
	// (uses order price; market orders require a caller-supplied reference price).
	MaxOrderNotional decimal.Decimal
	// MaxPositionQty caps the absolute resulting net position quantity per
	// instrument/side after the order would fully fill.
	MaxPositionQty decimal.Decimal
}

// Engine performs pre-trade checks against configured limits and a kill switch.
type Engine struct {
	mu                  sync.RWMutex
	limits              Limits
	cache               *cache.Cache
	tripped             bool // kill switch
	seen                map[string]struct{}
	seenOrder           []string
	clientIDLimit       int
	reservationSeq      uint64
	reservations        map[uint64]submissionReservation
	instrumentProvider  model.InstrumentProvider
	requireAccountState bool
	allowLegacyBalance  bool
	now                 func() time.Time
	productSupport      map[enums.InstrumentKind]riskProductSupport
	productSupportReady bool
	venueValidators     map[enums.InstrumentKind]contract.VenuePreTradeValidator
}

// defaultClientIDRetentionLimit is the bounded local idempotency horizon for
// completed/inactive client IDs. IDs attached to nonterminal or UNKNOWN orders
// remain protected even when that temporarily exceeds the configured limit.
const defaultClientIDRetentionLimit = 100_000

// New builds a risk Engine reading positions from c.
func New(limits Limits, c *cache.Cache) *Engine {
	return &Engine{
		limits:          limits,
		cache:           c,
		seen:            make(map[string]struct{}),
		clientIDLimit:   defaultClientIDRetentionLimit,
		reservations:    make(map[uint64]submissionReservation),
		now:             time.Now,
		venueValidators: make(map[enums.InstrumentKind]contract.VenuePreTradeValidator),
	}
}

type submissionReservation struct {
	request    model.OrderRequest
	instrument *model.Instrument
}

// SetInstrumentProvider installs the registry used to resolve working spot
// orders across instruments that reserve the same base or quote asset.
func (e *Engine) SetInstrumentProvider(provider model.InstrumentProvider) {
	e.mu.Lock()
	e.instrumentProvider = provider
	e.mu.Unlock()
}

// WithInstrumentProvider is the fluent form of SetInstrumentProvider.
func (e *Engine) WithInstrumentProvider(provider model.InstrumentProvider) *Engine {
	e.SetInstrumentProvider(provider)
	return e
}

// WithClientIDRetentionLimit bounds inactive duplicate-client-ID history to
// the most recent limit IDs. Nonterminal and UNKNOWN orders are recovery state,
// so their IDs are never evicted merely to meet the limit. Once an inactive ID
// leaves the window it may be submitted again. A non-positive limit leaves the
// current conservative default unchanged.
func (e *Engine) WithClientIDRetentionLimit(limit int) *Engine {
	if limit <= 0 {
		return e
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clientIDLimit = limit
	e.trimClientIDsLocked()
	return e
}

type riskProductSupport struct {
	trading      bool
	account      bool
	accountState bool
}

// SetRuntimeCapabilities installs the product-support contract provided by the
// runtime clients. When configured, every order must target an advertised
// trading product, and account-state-backed risk also requires an account-state
// capable account client for that product.
func (e *Engine) SetRuntimeCapabilities(caps ...contract.Capabilities) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.productSupportReady = true
	e.productSupport = make(map[enums.InstrumentKind]riskProductSupport)
	for _, cap := range caps {
		for _, product := range cap.Products {
			support := e.productSupport[product.Kind]
			if product.Trading && cap.Trading.Submit {
				support.trading = true
			}
			if product.Account {
				support.account = true
				if cap.Reports.AccountStateSnapshots || cap.Streaming.AccountState {
					support.accountState = true
				}
			}
			e.productSupport[product.Kind] = support
		}
	}
}

// RequireAccountState makes pre-trade checks fail closed when no fresh account
// state can be selected for the order's venue/product.
func (e *Engine) RequireAccountState() *Engine {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requireAccountState = true
	return e
}

// AllowLegacyBalanceFallback explicitly enables the pre-account-state spot
// balance path. It exists for adapters/tests that have not migrated to
// AccountStateReporter yet; RequireAccountState still takes precedence.
func (e *Engine) AllowLegacyBalanceFallback() *Engine {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.allowLegacyBalance = true
	return e
}

// WithClock injects a clock for deterministic freshness tests.
func (e *Engine) WithClock(now func() time.Time) *Engine {
	if now == nil {
		return e
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = now
	return e
}

// WithVenuePreTradeValidator registers the venue's authoritative capacity and
// prepared-payload validator for the supplied product kinds. A nil validator
// removes those registrations.
func (e *Engine) WithVenuePreTradeValidator(validator contract.VenuePreTradeValidator, kinds ...enums.InstrumentKind) *Engine {
	e.SetVenuePreTradeValidator(validator, kinds...)
	return e
}

// SetVenuePreTradeValidator is the non-fluent registration surface used by
// venue-neutral runtime wiring.
func (e *Engine) SetVenuePreTradeValidator(validator contract.VenuePreTradeValidator, kinds ...enums.InstrumentKind) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.venueValidators == nil {
		e.venueValidators = make(map[enums.InstrumentKind]contract.VenuePreTradeValidator)
	}
	for _, kind := range kinds {
		if validator == nil {
			delete(e.venueValidators, kind)
			continue
		}
		e.venueValidators[kind] = validator
	}
}

// Trip activates the kill switch: all subsequent orders are rejected.
func (e *Engine) Trip() {
	e.mu.Lock()
	e.tripped = true
	e.mu.Unlock()
}

// Reset clears the kill switch.
func (e *Engine) Reset() {
	e.mu.Lock()
	e.tripped = false
	e.mu.Unlock()
}

// Tripped reports whether the kill switch is active.
func (e *Engine) Tripped() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.tripped
}

// Check validates an order request against the limits, kill switch, instrument
// minimums, and duplicate-client-id protection. It returns a wrapped
// ErrRiskRejected on failure. inst may be nil (instrument minimums skipped).
func (e *Engine) Check(req model.OrderRequest, inst *model.Instrument) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, configured := e.venueValidators[req.InstrumentID.Kind]; configured {
		return fmt.Errorf("%w: product %s requires context-aware venue pre-trade validation", ErrRiskRejected, req.InstrumentID.Kind)
	}
	_, err := e.checkLocked(req, inst, false)
	return err
}

// CheckContext performs the same local checks as Check and, for product kinds
// with a registered venue validator, invokes that validator only after local
// checks pass. The risk mutex is never held across validator I/O.
func (e *Engine) CheckContext(ctx context.Context, req model.OrderRequest, inst *model.Instrument) (contract.PreTradeLease, error) {
	lease, _, err := e.checkContext(ctx, req, inst, false)
	return lease, err
}

// CheckSubmission performs context-aware risk validation and atomically holds
// the accepted request's local exposure until release is called. Exec uses this
// additive surface to close the gap between the risk check and PendingNew cache
// insertion without changing the legacy Check or CheckContext contracts.
func (e *Engine) CheckSubmission(
	ctx context.Context,
	req model.OrderRequest,
	inst *model.Instrument,
) (contract.PreTradeLease, func(), error) {
	return e.checkContext(ctx, req, inst, true)
}

func (e *Engine) checkContext(
	ctx context.Context,
	req model.OrderRequest,
	inst *model.Instrument,
	reserveSubmission bool,
) (contract.PreTradeLease, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	e.mu.Lock()
	validator, configured := e.venueValidators[req.InstrumentID.Kind]
	reserved, err := e.checkLocked(req, inst, configured)
	var release func()
	if err == nil && (reserveSubmission || configured) {
		release = e.reserveSubmissionLocked(req, inst)
	}
	e.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	if !configured {
		return nil, release, nil
	}
	if !reserveSubmission && release != nil {
		deferredRelease := release
		defer deferredRelease()
		release = nil
	}

	lease, validationErr := validator.ValidatePreTrade(ctx, req, inst)
	if validationErr != nil {
		if lease != nil {
			lease.Release()
		}
		if release != nil {
			release()
		}
		e.rollbackClientID(req.ClientID, reserved)
		return nil, nil, validationErr
	}
	if err := ctx.Err(); err != nil {
		if lease != nil {
			lease.Release()
		}
		if release != nil {
			release()
		}
		e.rollbackClientID(req.ClientID, reserved)
		return nil, nil, err
	}

	// A kill switch tripped while the validator was in I/O still denies handoff.
	e.mu.RLock()
	tripped := e.tripped
	e.mu.RUnlock()
	if tripped {
		if lease != nil {
			lease.Release()
		}
		if release != nil {
			release()
		}
		e.rollbackClientID(req.ClientID, reserved)
		return nil, nil, fmt.Errorf("%w: kill switch active", ErrRiskRejected)
	}
	return lease, release, nil
}

func (e *Engine) reserveSubmissionLocked(req model.OrderRequest, inst *model.Instrument) func() {
	e.reservationSeq++
	id := e.reservationSeq
	var instrument *model.Instrument
	if inst != nil {
		cloned := *inst
		instrument = &cloned
	}
	e.reservations[id] = submissionReservation{request: req, instrument: instrument}
	var once sync.Once
	return func() {
		once.Do(func() {
			e.mu.Lock()
			delete(e.reservations, id)
			e.trimClientIDsLocked()
			e.mu.Unlock()
		})
	}
}

func (e *Engine) rollbackClientID(clientID string, reserved bool) {
	if clientID == "" || !reserved {
		return
	}
	e.mu.Lock()
	delete(e.seen, clientID)
	for i := 0; i < len(e.seenOrder); {
		if e.seenOrder[i] == clientID {
			e.seenOrder = append(e.seenOrder[:i], e.seenOrder[i+1:]...)
			continue
		}
		i++
	}
	e.mu.Unlock()
}

// checkLocked performs only local/cache-backed checks. The caller must hold
// e.mu for writing so duplicate client IDs can be reserved atomically.
func (e *Engine) checkLocked(req model.OrderRequest, inst *model.Instrument, venueValidated bool) (bool, error) {

	if e.tripped {
		return false, fmt.Errorf("%w: kill switch active", ErrRiskRejected)
	}

	if req.Quantity.IsZero() || req.Quantity.IsNegative() {
		return false, fmt.Errorf("%w: non-positive quantity %s", ErrRiskRejected, req.Quantity)
	}

	// Duplicate client id (idempotency guard) — only when one is provided.
	if req.ClientID != "" {
		if _, dup := e.seen[req.ClientID]; dup {
			return false, fmt.Errorf("%w: duplicate client id %q", ErrRiskRejected, req.ClientID)
		}
	}

	if lim := e.limits.MaxOrderQty; !lim.IsZero() && req.Quantity.GreaterThan(lim) {
		return false, fmt.Errorf("%w: order qty %s exceeds max %s", ErrRiskRejected, req.Quantity, lim)
	}

	if lim := e.limits.MaxOrderNotional; !lim.IsZero() {
		if req.Price.IsZero() {
			return false, fmt.Errorf("%w: reference price required to evaluate max order notional", ErrRiskRejected)
		}
		notional := orderNotional(req, inst)
		if notional.GreaterThan(lim) {
			return false, fmt.Errorf("%w: order notional %s exceeds max %s", ErrRiskRejected, notional, lim)
		}
	}
	if err := e.ensureProductSupported(req.InstrumentID.Kind); err != nil {
		return false, err
	}

	// Instrument minimums.
	if inst != nil {
		if !inst.MinQty.IsZero() && req.Quantity.LessThan(inst.MinQty) {
			return false, fmt.Errorf("%w: qty %s below instrument min %s", ErrRiskRejected, req.Quantity, inst.MinQty)
		}
		if !inst.MinNotional.IsZero() {
			if req.Price.IsZero() {
				return false, fmt.Errorf("%w: reference price required to evaluate instrument min notional", ErrRiskRejected)
			}
			if n := orderNotional(req, inst); n.LessThan(inst.MinNotional) {
				return false, fmt.Errorf("%w: notional %s below instrument min %s", ErrRiskRejected, n, inst.MinNotional)
			}
		}
	}

	if venueValidated {
		if err := e.checkVenueValidatedAccount(req, inst); err != nil {
			return false, err
		}
	} else if req.InstrumentID.Kind == enums.KindSpot {
		if inst == nil {
			return false, fmt.Errorf("%w: spot instrument metadata required for cash risk check", ErrRiskRejected)
		}
		if err := e.checkSpotBalance(req, inst); err != nil {
			return false, err
		}
	} else if e.requireAccountState && !req.ReduceOnly {
		if err := e.checkMarginAccount(req, inst); err != nil {
			return false, err
		}
	}

	// Resulting-position cap: current signed qty plus every potentially live
	// order. Buys and sells are evaluated independently so opposing working
	// orders cannot optimistically net one another before either fills.
	if lim := e.limits.MaxPositionQty; req.InstrumentID.Kind != enums.KindSpot && !lim.IsZero() {
		if err := e.checkMaxPositionQty(req, lim); err != nil {
			return false, err
		}
	}

	// Record the client id only after all checks pass.
	reserved := false
	if req.ClientID != "" {
		e.seen[req.ClientID] = struct{}{}
		e.seenOrder = append(e.seenOrder, req.ClientID)
		e.trimClientIDsLocked()
		reserved = true
	}
	return reserved, nil
}

func (e *Engine) trimClientIDsLocked() {
	for len(e.seen) > e.clientIDLimit {
		evicted := false
		for i, clientID := range e.seenOrder {
			if _, present := e.seen[clientID]; !present {
				e.seenOrder = append(e.seenOrder[:i], e.seenOrder[i+1:]...)
				evicted = true
				break
			}
			// The newest entry is the reservation being returned from the
			// current check. It may not have reached the order cache yet, so
			// treating cache absence as inactivity would create an immediate
			// duplicate-submit hole while venue validation is still in flight.
			if i == len(e.seenOrder)-1 {
				continue
			}
			if e.clientIDHasActiveOrder(clientID) {
				continue
			}
			delete(e.seen, clientID)
			e.seenOrder = append(e.seenOrder[:i], e.seenOrder[i+1:]...)
			evicted = true
			break
		}
		if !evicted {
			return
		}
	}
}

func (e *Engine) clientIDHasActiveOrder(clientID string) bool {
	for _, reservation := range e.reservations {
		if reservation.request.ClientID == clientID {
			return true
		}
	}
	if e.cache == nil {
		return false
	}
	for _, order := range e.cache.Orders() {
		if order.Request.ClientID != clientID {
			continue
		}
		if order.Status == enums.StatusUnknown || !orderstate.IsTerminal(order.Status) {
			return true
		}
	}
	return false
}

func (e *Engine) checkVenueValidatedAccount(req model.OrderRequest, inst *model.Instrument) error {
	if req.InstrumentID.Kind == enums.KindSpot && inst == nil {
		return fmt.Errorf("%w: spot instrument metadata required for cash risk check", ErrRiskRejected)
	}
	acct, ok, err := e.accountForRequest(req)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: no account state for venue %s", ErrRiskRejected, req.InstrumentID.Venue)
	}
	if req.InstrumentID.Kind == enums.KindSpot {
		if acct.Type() != model.AccountCash && acct.Type() != model.AccountMargin {
			return fmt.Errorf("%w: unsupported account type %s for spot order", ErrRiskRejected, acct.Type())
		}
	} else if acct.Type() != model.AccountMargin {
		return fmt.Errorf("%w: unsupported account type %s for %s order", ErrRiskRejected, acct.Type(), req.InstrumentID.Kind)
	}
	return e.ensureFreshAccount(acct)
}

func (e *Engine) checkSpotBalance(req model.OrderRequest, inst *model.Instrument) error {
	if acct, ok, err := e.accountForRequest(req); err != nil {
		return err
	} else if ok {
		return e.checkSpotAccountBalance(req, inst, acct)
	}
	if e.requireAccountState || !e.allowLegacyBalance {
		return fmt.Errorf("%w: no account state for venue %s", ErrRiskRejected, req.InstrumentID.Venue)
	}
	return e.checkLegacySpotBalance(req, inst)
}

func (e *Engine) checkLegacySpotBalance(req model.OrderRequest, inst *model.Instrument) error {
	mult := inst.ContractMultiplier
	if !mult.IsPositive() {
		mult = decimal.NewFromInt(1)
	}
	switch req.Side {
	case enums.SideBuy:
		if inst.Quote == "" {
			return fmt.Errorf("%w: spot buy requires quote currency metadata for cash risk check", ErrRiskRejected)
		}
		if req.Price.IsZero() {
			return fmt.Errorf("%w: spot buy requires a reference price for cash risk check", ErrRiskRejected)
		}
		reserved, err := e.workingSpotReservation(req, inst, "", time.Time{})
		if err != nil {
			return err
		}
		required := orderNotional(req, inst).Add(reserved)
		available := decimal.Zero
		if bal, ok := e.cache.Balance(inst.Quote); ok {
			available = bal.FreeOrAvailable()
		}
		if required.GreaterThan(available) {
			return fmt.Errorf("%w: insufficient %s cash: need %s, available %s", ErrRiskRejected, inst.Quote, required, available)
		}
	case enums.SideSell:
		if inst.Base == "" {
			return fmt.Errorf("%w: spot sell requires base currency metadata for cash risk check", ErrRiskRejected)
		}
		reserved, err := e.workingSpotReservation(req, inst, "", time.Time{})
		if err != nil {
			return err
		}
		required := req.Quantity.Mul(mult).Add(reserved)
		available := decimal.Zero
		if bal, ok := e.cache.Balance(inst.Base); ok {
			available = bal.FreeOrAvailable()
		}
		if required.GreaterThan(available) {
			return fmt.Errorf("%w: insufficient %s inventory: need %s, available %s", ErrRiskRejected, inst.Base, required, available)
		}
	}
	return nil
}

func (e *Engine) checkSpotAccountBalance(req model.OrderRequest, inst *model.Instrument, acct accounting.Account) error {
	if acct.Type() != model.AccountCash && acct.Type() != model.AccountMargin {
		return fmt.Errorf("%w: unsupported account type %s for spot order", ErrRiskRejected, acct.Type())
	}
	if err := e.ensureFreshAccount(acct); err != nil {
		return err
	}
	switch req.Side {
	case enums.SideBuy:
		if inst.Quote == "" {
			return fmt.Errorf("%w: spot buy requires quote currency metadata for cash risk check", ErrRiskRejected)
		}
		if req.Price.IsZero() {
			return fmt.Errorf("%w: spot buy requires a reference price for cash risk check", ErrRiskRejected)
		}
		balance, ok := acct.Balance(inst.Quote)
		if !ok {
			return fmt.Errorf("%w: missing free balance for %s on account %s", ErrRiskRejected, inst.Quote, acct.ID())
		}
		reserved, err := e.workingSpotReservation(req, inst, acct.ID(), accountBalanceAsOf(acct, balance))
		if err != nil {
			return err
		}
		required := orderNotional(req, inst).Add(reserved)
		free := balance.FreeOrAvailable()
		if required.GreaterThan(free) {
			return fmt.Errorf("%w: insufficient %s cash on account %s: need %s, free %s", ErrRiskRejected, inst.Quote, acct.ID(), required, free)
		}
	case enums.SideSell:
		if inst.Base == "" {
			return fmt.Errorf("%w: spot sell requires base currency metadata for cash risk check", ErrRiskRejected)
		}
		balance, ok := acct.Balance(inst.Base)
		if !ok {
			return fmt.Errorf("%w: missing free balance for %s on account %s", ErrRiskRejected, inst.Base, acct.ID())
		}
		reserved, err := e.workingSpotReservation(req, inst, acct.ID(), accountBalanceAsOf(acct, balance))
		if err != nil {
			return err
		}
		required := req.Quantity.Mul(contractMultiplier(inst)).Add(reserved)
		free := balance.FreeOrAvailable()
		if required.GreaterThan(free) {
			return fmt.Errorf("%w: insufficient %s inventory on account %s: need %s, free %s", ErrRiskRejected, inst.Base, acct.ID(), required, free)
		}
	}
	return nil
}

func (e *Engine) checkMarginAccount(req model.OrderRequest, inst *model.Instrument) error {
	acct, ok, err := e.accountForRequest(req)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: no account state for venue %s", ErrRiskRejected, req.InstrumentID.Venue)
	}
	if acct.Type() != model.AccountMargin {
		return fmt.Errorf("%w: unsupported account type %s for %s order", ErrRiskRejected, acct.Type(), req.InstrumentID.Kind)
	}
	if err := e.ensureFreshAccount(acct); err != nil {
		return err
	}
	if req.Price.IsZero() {
		return fmt.Errorf("%w: %s order requires a reference price for account risk check", ErrRiskRejected, req.InstrumentID.Kind)
	}
	ccy := marginCurrency(req.InstrumentID, inst)
	if ccy == "" {
		return fmt.Errorf("%w: missing margin currency metadata for %s", ErrRiskRejected, req.InstrumentID)
	}
	required := orderNotional(req, inst)
	free, ok := acct.BalanceFree(ccy)
	if ok && required.LessThanOrEqual(free) {
		return nil
	}
	if !ok {
		return fmt.Errorf("%w: missing free balance for %s on account %s", ErrRiskRejected, ccy, acct.ID())
	}
	if required.GreaterThan(free) {
		return fmt.Errorf("%w: insufficient %s margin on account %s: need %s, free %s", ErrRiskRejected, ccy, acct.ID(), required, free)
	}
	return nil
}

func (e *Engine) accountForRequest(req model.OrderRequest) (accounting.Account, bool, error) {
	if e.cache == nil {
		return nil, false, nil
	}
	if req.AccountID != "" {
		acct, ok := e.cache.Account(req.AccountID)
		if !ok {
			return nil, false, fmt.Errorf("%w: no account state for account %s", ErrRiskRejected, req.AccountID)
		}
		return acct, true, nil
	}
	ids := e.cache.AccountIDsForVenue(req.InstrumentID.Venue)
	if len(ids) > 1 {
		return nil, false, fmt.Errorf("%w: ambiguous account state for venue %s; account id required", ErrRiskRejected, req.InstrumentID.Venue)
	}
	acct, ok := e.cache.AccountForVenue(req.InstrumentID.Venue)
	return acct, ok, nil
}

func (e *Engine) positionForRequest(req model.OrderRequest) (model.Position, bool) {
	if e.cache == nil {
		return model.Position{}, false
	}
	if req.AccountID != "" {
		return e.cache.PositionForAccount(req.AccountID, req.InstrumentID, req.PositionSide)
	}
	return e.cache.Position(req.InstrumentID, req.PositionSide)
}

func (e *Engine) checkMaxPositionQty(req model.OrderRequest, limit decimal.Decimal) error {
	current := decimal.Zero
	if position, ok := e.positionForRequest(req); ok {
		current = position.Quantity
	}
	worstBuy := current
	worstSell := current
	accountID := e.riskScopeAccountID(req)
	if e.cache != nil {
		for _, order := range e.cache.Orders() {
			if !riskBearingOrderStatus(order.Status) || !workingOrderMatches(req, order.Request, accountID) {
				continue
			}
			remaining := remainingOrderQuantity(order)
			if !remaining.IsPositive() {
				continue
			}
			switch order.Request.Side {
			case enums.SideBuy:
				worstBuy = worstBuy.Add(remaining)
			case enums.SideSell:
				worstSell = worstSell.Sub(remaining)
			}
		}
	}
	for _, reservation := range e.reservations {
		if e.reservationReflectedInCache(reservation) || !workingOrderMatches(req, reservation.request, accountID) {
			continue
		}
		switch reservation.request.Side {
		case enums.SideBuy:
			worstBuy = worstBuy.Add(reservation.request.Quantity)
		case enums.SideSell:
			worstSell = worstSell.Sub(reservation.request.Quantity)
		}
	}
	if req.Side == enums.SideSell {
		worstSell = worstSell.Sub(req.Quantity)
	} else {
		worstBuy = worstBuy.Add(req.Quantity)
	}
	worst := worstBuy.Abs()
	if sell := worstSell.Abs(); sell.GreaterThan(worst) {
		worst = sell
	}
	if worst.GreaterThan(limit) {
		return fmt.Errorf("%w: worst-case resulting position %s exceeds max %s", ErrRiskRejected, worst, limit)
	}
	return nil
}

func (e *Engine) workingSpotReservation(req model.OrderRequest, inst *model.Instrument, accountID string, balanceAsOf time.Time) (decimal.Decimal, error) {
	if accountID == "" {
		accountID = e.riskScopeAccountID(req)
	}
	asset, err := spotReservationAsset(req, inst)
	if err != nil {
		return decimal.Zero, err
	}
	reserved := decimal.Zero
	if e.cache != nil {
		for _, order := range e.cache.Orders() {
			if !riskBearingOrderStatus(order.Status) || !spotOrderInRiskScope(req, order.Request, accountID) || orderReflectedByBalance(order, balanceAsOf) {
				continue
			}
			workingInst, resolveErr := e.spotInstrument(order.Request.InstrumentID, req.InstrumentID, inst)
			if resolveErr != nil {
				return decimal.Zero, resolveErr
			}
			workingAsset, assetErr := spotReservationAsset(order.Request, workingInst)
			if assetErr != nil {
				return decimal.Zero, assetErr
			}
			if workingAsset != asset {
				continue
			}
			amount, amountErr := spotOrderReservation(order.Request, remainingOrderQuantity(order), workingInst)
			if amountErr != nil {
				return decimal.Zero, amountErr
			}
			reserved = reserved.Add(amount)
		}
	}
	for _, reservation := range e.reservations {
		if e.reservationReflectedInCache(reservation) || !spotOrderInRiskScope(req, reservation.request, accountID) {
			continue
		}
		workingInst := reservation.instrument
		var resolveErr error
		if workingInst == nil {
			workingInst, resolveErr = e.spotInstrument(reservation.request.InstrumentID, req.InstrumentID, inst)
		}
		if resolveErr != nil {
			return decimal.Zero, resolveErr
		}
		workingAsset, assetErr := spotReservationAsset(reservation.request, workingInst)
		if assetErr != nil {
			return decimal.Zero, assetErr
		}
		if workingAsset != asset {
			continue
		}
		amount, amountErr := spotOrderReservation(reservation.request, reservation.request.Quantity, workingInst)
		if amountErr != nil {
			return decimal.Zero, amountErr
		}
		reserved = reserved.Add(amount)
	}
	return reserved, nil
}

func (e *Engine) spotInstrument(id, requestID model.InstrumentID, requestInst *model.Instrument) (*model.Instrument, error) {
	if id == requestID && requestInst != nil {
		return requestInst, nil
	}
	if e.instrumentProvider != nil {
		if instrument, ok := e.instrumentProvider.Instrument(id); ok && instrument != nil {
			return instrument, nil
		}
	}
	parts := strings.Split(id.Symbol, "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("%w: instrument metadata required to reserve working spot order %s", ErrRiskRejected, id)
	}
	return &model.Instrument{ID: id, Base: parts[0], Quote: parts[1]}, nil
}

func spotReservationAsset(req model.OrderRequest, inst *model.Instrument) (string, error) {
	if inst == nil {
		return "", fmt.Errorf("%w: spot instrument metadata required for cash reservation", ErrRiskRejected)
	}
	switch req.Side {
	case enums.SideBuy:
		if inst.Quote == "" {
			return "", fmt.Errorf("%w: spot buy requires quote currency metadata for cash reservation", ErrRiskRejected)
		}
		return inst.Quote, nil
	case enums.SideSell:
		if inst.Base == "" {
			return "", fmt.Errorf("%w: spot sell requires base currency metadata for cash reservation", ErrRiskRejected)
		}
		return inst.Base, nil
	default:
		return "", fmt.Errorf("%w: unsupported spot side %s", ErrRiskRejected, req.Side)
	}
}

func spotOrderReservation(req model.OrderRequest, remaining decimal.Decimal, inst *model.Instrument) (decimal.Decimal, error) {
	if !remaining.IsPositive() {
		return decimal.Zero, nil
	}
	switch req.Side {
	case enums.SideBuy:
		if req.Price.IsZero() {
			return decimal.Zero, fmt.Errorf("%w: working spot buy %q requires a reference price for cash reservation", ErrRiskRejected, req.ClientID)
		}
		working := req
		working.Quantity = remaining
		return orderNotional(working, inst), nil
	case enums.SideSell:
		return remaining.Mul(contractMultiplier(inst)), nil
	default:
		return decimal.Zero, fmt.Errorf("%w: unsupported spot side %s", ErrRiskRejected, req.Side)
	}
}

func spotOrderInRiskScope(req, working model.OrderRequest, accountID string) bool {
	if working.InstrumentID.Kind != enums.KindSpot || working.InstrumentID.Venue != req.InstrumentID.Venue {
		return false
	}
	return accountID == "" || working.AccountID == "" || working.AccountID == accountID
}

func orderReflectedByBalance(order model.Order, balanceAsOf time.Time) bool {
	if balanceAsOf.IsZero() {
		return false
	}
	updatedAt := order.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = order.CreatedAt
	}
	return !updatedAt.IsZero() && !updatedAt.After(balanceAsOf)
}

func accountBalanceAsOf(acct accounting.Account, balance model.AccountBalance) time.Time {
	if !balance.UpdatedAt.IsZero() {
		return balance.UpdatedAt
	}
	return acct.LastEvent().TsEvent
}

func (e *Engine) reservationReflectedInCache(reservation submissionReservation) bool {
	if e.cache == nil || reservation.request.ClientID == "" {
		return false
	}
	for _, order := range e.cache.Orders() {
		if order.Request.ClientID != reservation.request.ClientID || order.Request.InstrumentID != reservation.request.InstrumentID {
			continue
		}
		if reservation.request.AccountID != "" && order.Request.AccountID != "" && order.Request.AccountID != reservation.request.AccountID {
			continue
		}
		return riskBearingOrderStatus(order.Status)
	}
	return false
}

func (e *Engine) riskScopeAccountID(req model.OrderRequest) string {
	if req.AccountID != "" || e.cache == nil {
		return req.AccountID
	}
	accountIDs := e.cache.AccountIDsForVenue(req.InstrumentID.Venue)
	if len(accountIDs) == 1 {
		return accountIDs[0]
	}
	return ""
}

func workingOrderMatches(req, working model.OrderRequest, accountID string) bool {
	if working.InstrumentID != req.InstrumentID || working.PositionSide != req.PositionSide {
		return false
	}
	return accountID == "" || working.AccountID == "" || working.AccountID == accountID
}

func remainingOrderQuantity(order model.Order) decimal.Decimal {
	remaining := order.Request.Quantity.Sub(order.FilledQty)
	if remaining.IsPositive() {
		return remaining
	}
	return decimal.Zero
}

func riskBearingOrderStatus(status enums.OrderStatus) bool {
	switch status {
	case enums.StatusFilled, enums.StatusCanceled, enums.StatusRejected, enums.StatusExpired:
		return false
	default:
		return true
	}
}

func (e *Engine) ensureProductSupported(kind enums.InstrumentKind) error {
	if !e.productSupportReady {
		return nil
	}
	support, ok := e.productSupport[kind]
	if !ok || !support.trading {
		return fmt.Errorf("%w: unsupported product %s for trading", ErrRiskRejected, kind)
	}
	if e.requireAccountState && (!support.account || !support.accountState) {
		return fmt.Errorf("%w: unsupported product %s for account-state-backed risk", ErrRiskRejected, kind)
	}
	return nil
}

func (e *Engine) ensureFreshAccount(acct accounting.Account) error {
	now := time.Now
	if e.now != nil {
		now = e.now
	}
	if acct.IsFresh(now()) {
		return nil
	}
	f := acct.Freshness()
	return fmt.Errorf("%w: stale account state for %s age %s exceeds %s", ErrRiskRejected, acct.ID(), f.Age(now()), f.StaleAfter)
}

func orderNotional(req model.OrderRequest, inst *model.Instrument) decimal.Decimal {
	return req.Price.Mul(req.Quantity).Mul(contractMultiplier(inst))
}

func contractMultiplier(inst *model.Instrument) decimal.Decimal {
	if inst != nil && inst.ContractMultiplier.IsPositive() {
		return inst.ContractMultiplier
	}
	return decimal.NewFromInt(1)
}

func marginCurrency(id model.InstrumentID, inst *model.Instrument) string {
	if inst != nil {
		if inst.Settle != "" {
			return inst.Settle
		}
		if inst.Quote != "" {
			return inst.Quote
		}
	}
	quote := ""
	for i := len(id.Symbol) - 1; i >= 0; i-- {
		if id.Symbol[i] == '-' {
			quote = id.Symbol[i+1:]
			break
		}
	}
	ok := quote != ""
	if !ok {
		return ""
	}
	return strings.ToUpper(quote)
}
