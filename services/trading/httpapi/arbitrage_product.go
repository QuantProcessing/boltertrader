package httpapi

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/httpx"
	"github.com/QuantProcessing/boltertrader/services/trading/arbitrage"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/QuantProcessing/boltertrader/services/trading/reservations"
	"github.com/shopspring/decimal"
)

/* ---------------------------------------------------------------------------
 * The arbitrage product's browser surface.
 *
 * Four routes rather than manual's five: a leg has no screen of its own, so
 * there is no per-order detail route. Everything an operator sees about a hedge
 * is the pair, and the pair is keyed by LineageID.
 *
 * The shape follows manual deliberately — preview, seal, spend the seal — and
 * departs from it in exactly one place: opening emits two commands, and the
 * second one failing is not an error the first one gets rolled back for. A
 * half-open pair is reported as half-open and left for a human, because the
 * alternative is this handler placing an order nobody previewed.
 * ------------------------------------------------------------------------- */

type arbitrageProductHandler struct {
	trading *tradingHandler
}

// arbitrageLegRequest is one side of the hedge as the operator names it. The
// limit price is per leg because the two venues quote independently; a single
// price for both would only ever be right by accident.
type arbitrageLegRequest struct {
	AccountID    string `json:"accountId"`
	InstrumentID string `json:"instrumentId"`
	LimitPrice   string `json:"limitPrice,omitempty"`
}

type arbitragePreviewRequest struct {
	Long        arbitrageLegRequest `json:"long"`
	Short       arbitrageLegRequest `json:"short"`
	Quantity    string              `json:"quantity"`
	OrderType   string              `json:"orderType"`
	TimeInForce string              `json:"timeInForce,omitempty"`
}

type arbitrageCommandRequest struct {
	PreviewID      string `json:"previewId,omitempty"`
	Action         string `json:"action"`
	LineageID      string `json:"lineageId"`
	OrderID        string `json:"orderId,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// arbitrageLegAuthority is the per-leg half of what the preview seals: where the
// order goes, which route it was authorized through, and the account fence it
// was authorized against. Two of these travel in one token because both legs
// have to still be true at submission — a hedge authorized against an account
// that has since rotated its credential is not authorized any more.
type arbitrageLegAuthority struct {
	AccountID        string             `json:"accountId"`
	InstrumentID     string             `json:"instrumentId"`
	LimitPrice       string             `json:"limitPrice,omitempty"`
	Venue            string             `json:"venue"`
	VenueEnvironment string             `json:"venueEnvironment"`
	Product          string             `json:"product"`
	RouteID          string             `json:"routeId"`
	Fence            manualAccountFence `json:"fence"`
}

type arbitragePreviewAuthority struct {
	PrincipalID     string                  `json:"principalId"`
	Request         arbitragePreviewRequest `json:"request"`
	Long            arbitrageLegAuthority   `json:"long"`
	Short           arbitrageLegAuthority   `json:"short"`
	CatalogRevision string                  `json:"catalogRevision"`
	IssuedAt        time.Time               `json:"issuedAt"`
	ExpiresAt       time.Time               `json:"expiresAt"`
}

// arbitragePreviewLeg is what the preview shows for one side: where it lands and
// what it earns or pays. The rate and interval are reported raw beside the
// derived daily figure so the interface can re-express them in whatever unit the
// operator is reading in without asking the server again.
type arbitragePreviewLeg struct {
	AccountID       string    `json:"accountId"`
	InstrumentID    string    `json:"instrumentId"`
	Venue           string    `json:"venue"`
	Environment     string    `json:"environment"`
	Symbol          string    `json:"symbol,omitempty"`
	Side            string    `json:"side"`
	LimitPrice      string    `json:"limitPrice,omitempty"`
	Rate            string    `json:"rate,omitempty"`
	IntervalHours   int64     `json:"intervalHours,omitempty"`
	NextFundingTime time.Time `json:"nextFundingTime,omitzero"`
	MarkPrice       string    `json:"markPrice,omitempty"`
	DailyRate       string    `json:"dailyRate,omitempty"`
	Stale           bool      `json:"stale,omitempty"`
}

type arbitragePreview struct {
	PreviewID    string                  `json:"previewId"`
	Request      arbitragePreviewRequest `json:"request"`
	Long         arbitragePreviewLeg     `json:"long"`
	Short        arbitragePreviewLeg     `json:"short"`
	Quantity     string                  `json:"quantity"`
	Notional     string                  `json:"notional,omitempty"`
	SpreadDaily  string                  `json:"spreadDaily,omitempty"`
	SpreadApr    string                  `json:"spreadApr,omitempty"`
	DailyFunding string                  `json:"dailyFunding,omitempty"`
	Blockers     []string                `json:"blockers"`
	Warnings     []string                `json:"warnings"`
}

type arbitrageOpportunityLeg struct {
	Venue                string    `json:"venue"`
	Environment          string    `json:"environment"`
	RouteProfileID       string    `json:"routeProfileId"`
	Settlement           string    `json:"settlement,omitempty"`
	AccountID            string    `json:"accountId,omitempty"`
	InstrumentID         string    `json:"instrumentId,omitempty"`
	Rate                 string    `json:"rate"`
	IntervalHours        int64     `json:"intervalHours,omitempty"`
	NextFundingTime      time.Time `json:"nextFundingTime,omitzero"`
	MarkPrice            string    `json:"markPrice,omitempty"`
	OpenInterest         string    `json:"openInterest,omitempty"`
	OpenInterestNotional string    `json:"openInterestNotional,omitempty"`
	DailyRate            string    `json:"dailyRate,omitempty"`
	Stale                bool      `json:"stale,omitempty"`
}

type arbitrageOpportunity struct {
	Symbol      string                  `json:"symbol"`
	BaseAsset   string                  `json:"baseAsset"`
	QuoteAsset  string                  `json:"quoteAsset"`
	Long        arbitrageOpportunityLeg `json:"long"`
	Short       arbitrageOpportunityLeg `json:"short"`
	SpreadDaily string                  `json:"spreadDaily,omitempty"`
	SpreadApr   string                  `json:"spreadApr,omitempty"`
	Executable  bool                    `json:"executable"`
	Blockers    []string                `json:"blockers"`
}

type arbitrageAmount struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type arbitragePair struct {
	LineageID string               `json:"lineageId"`
	State     arbitrage.PairState  `json:"state"`
	Legs      []arbitrage.LegView  `json:"legs"`
	Positions []portfolio.Position `json:"positions"`
	Funding   []arbitrageAmount    `json:"funding"`
	Blockers  []string             `json:"blockers"`
	UpdatedAt time.Time            `json:"updatedAt"`
}

type arbitrageLegFailure struct {
	Leg   arbitrage.Leg `json:"leg"`
	Stage string        `json:"stage"`
	Code  string        `json:"code"`
}

type arbitrageExecution struct {
	LineageID string                `json:"lineageId"`
	State     arbitrage.PairState   `json:"state"`
	Legs      []arbitrage.LegView   `json:"legs"`
	Failures  []arbitrageLegFailure `json:"failures"`
}

// arbitrageLegOrder is one leg on its way to the venue. It carries its
// counterpart because the intent records who the sibling is, and the sibling has
// to be known before either command is minted.
type arbitrageLegOrder struct {
	Leg         arbitrage.Leg
	Authority   arbitrageLegAuthority
	Counter     arbitrageLegAuthority
	Quantity    string
	OrderType   string
	LimitPrice  string
	TimeInForce string
	ReduceOnly  bool
}

// arbitragePreviewCodec is structurally manual's codec, named separately so the
// two products' seals stay distinguishable in the reading. They are not
// interchangeable in fact either: arbitrage's MAC covers an extra domain label,
// so a token sealed by one opener is refused by the other.
type arbitragePreviewCodec interface {
	SealPreview(principal string, payload []byte) (string, error)
	OpenPreview(principal string, token string) ([]byte, error)
	PreviewNow() time.Time
}

const arbitragePreviewTTL = 30 * time.Second

// arbitrageMaxAccounts bounds the reachability lookup on the opportunities
// route. Each named account costs one authority read, and the board only ever
// spans MaxBoardVenues venues, so nothing useful lies past this.
const arbitrageMaxAccounts = 32

// arbitrageProduct is the perp product the hedge is built from. A funding hedge
// is two perpetuals by construction: spot has no funding leg to arbitrage.
const arbitrageProduct = "perp"

func registerArbitrageProductRoutes(mux *http.ServeMux, trading *tradingHandler) {
	handler := &arbitrageProductHandler{trading: trading}
	mux.HandleFunc("GET /api/v1/products/arbitrage/opportunities", handler.opportunities)
	mux.HandleFunc("POST /api/v1/products/arbitrage/execution-previews", handler.createPreview)
	mux.HandleFunc("POST /api/v1/products/arbitrage/commands", handler.createCommand)
	mux.HandleFunc("GET /api/v1/products/arbitrage/positions", handler.positions)
}

/* --- Opportunities -------------------------------------------------------- */

// opportunities ranks the board's cross-venue funding spreads.
//
// Reachability is opt-in through repeated `accountId` parameters rather than
// enumerated here, because trading's account authority answers for one account
// at a time and deliberately has no list: the caller already holds its own
// account list from the gateway plane. Naming an account that is not the
// caller's simply fails the principal-scoped read and leaves the venue
// unreachable, so the parameter cannot be used to discover anything.
func (handler *arbitrageProductHandler) opportunities(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.trading.identity(w, r)
	if !ok {
		return
	}
	if handler.trading.options.FundingBoard == nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	board, err := handler.trading.options.FundingBoard.Get(r.Context())
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "PUBLIC_DATA_NOT_READY")
		return
	}
	reachable := handler.reachableProfiles(r, principal)

	// Group the board by underlying, then pick the extremes: the venue paying
	// the most is the one to be short on, the venue paying the least is the one
	// to be long on. Any other pairing on the same underlying earns strictly
	// less, so there is exactly one interesting pair per row.
	grouped := map[boardKey][]arbitrageOpportunityLeg{}
	for _, venue := range board.Venues {
		if venue.Unavailable || venue.Product != arbitrageProduct {
			continue
		}
		for _, row := range venue.Rows {
			if row.BaseAsset == "" || row.QuoteAsset == "" || row.Rate == "" {
				continue
			}
			key := boardKey{base: row.BaseAsset, quote: row.QuoteAsset}
			leg := arbitrageOpportunityLeg{
				Venue: venue.Venue, Environment: venue.Environment,
				RouteProfileID: venue.RouteProfileID, Settlement: venue.Settlement,
				InstrumentID: row.InstrumentID, Rate: row.Rate,
				IntervalHours: row.IntervalHours, NextFundingTime: row.NextFundingTime,
				MarkPrice: row.MarkPrice, OpenInterest: row.OpenInterest,
				OpenInterestNotional: row.OpenInterestNotional, Stale: venue.Stale,
			}
			if daily, ok := dailyFundingRate(row.Rate, row.IntervalHours); ok {
				leg.DailyRate = daily.String()
			}
			if accountID, found := reachable[venue.RouteProfileID]; found {
				leg.AccountID = accountID
			}
			grouped[key] = append(grouped[key], leg)
		}
	}

	items := make([]arbitrageOpportunity, 0, len(grouped))
	for key, legs := range grouped {
		opportunity, ok := buildOpportunity(key, legs)
		if !ok {
			continue
		}
		items = append(items, opportunity)
	}
	// Comparable spreads first and largest first within them; an underlying no
	// venue published an interval for still appears, at the bottom, rather than
	// being dropped for being unrankable.
	slices.SortStableFunc(items, func(left, right arbitrageOpportunity) int {
		leftValue, leftOK := decimal.NewFromString(left.SpreadDaily)
		rightValue, rightOK := decimal.NewFromString(right.SpreadDaily)
		switch {
		case leftOK != nil && rightOK != nil:
			return strings.Compare(left.Symbol, right.Symbol)
		case leftOK != nil:
			return 1
		case rightOK != nil:
			return -1
		case !leftValue.Equal(rightValue):
			return rightValue.Cmp(leftValue)
		default:
			return strings.Compare(left.Symbol, right.Symbol)
		}
	})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"meta": httpx.Meta(r),
		"data": map[string]any{"items": items, "observedAt": board.ObservedAt},
	})
}

func buildOpportunity(key boardKey, legs []arbitrageOpportunityLeg) (arbitrageOpportunity, bool) {
	// One quote per exchange, so the two extremes below cannot land on the same
	// one. A hedge with both legs on one exchange is not a hedge — it is
	// delta-neutral against nothing and pays no spread.
	//
	// This used to be a filter as much as an invariant: a venue the registry
	// listed under two native environments arrived as two columns. The board is
	// now keyed by a profile ID of venue, product and settlement, so it cannot
	// hand over two columns naming one market. The reduction stays because the
	// fallback at the bottom rotates off the first extreme to find the second,
	// and only reaches a different exchange if these legs are already distinct.
	venues := make([]arbitrageOpportunityLeg, 0, len(legs))
	seen := make(map[string]struct{}, len(legs))
	for _, leg := range legs {
		if _, taken := seen[leg.Venue]; taken {
			continue
		}
		seen[leg.Venue] = struct{}{}
		venues = append(venues, leg)
	}
	if len(venues) < 2 {
		return arbitrageOpportunity{}, false
	}

	// Indices, because the two extremes can otherwise coincide: `compareRates`
	// declines to order legs that published no interval, so an underlying only
	// one exchange dated leaves both on `venues[0]` and reports a zero spread
	// between a venue and itself. When nothing separates them any two exchanges
	// will do — the pair is unranked, and says so through the blocker below.
	lowest, highest := 0, 0
	for index := 1; index < len(venues); index++ {
		if compareRates(venues[index], venues[lowest]) < 0 {
			lowest = index
		}
		if compareRates(venues[index], venues[highest]) > 0 {
			highest = index
		}
	}
	if lowest == highest {
		highest = (lowest + 1) % len(venues)
	}
	long, short := venues[lowest], venues[highest]

	opportunity := arbitrageOpportunity{
		Symbol: key.base + "-" + key.quote, BaseAsset: key.base, QuoteAsset: key.quote,
		Long: long, Short: short, Blockers: []string{},
	}
	longDaily, longOK := decimal.NewFromString(long.DailyRate)
	shortDaily, shortOK := decimal.NewFromString(short.DailyRate)
	if longOK == nil && shortOK == nil {
		spread := shortDaily.Sub(longDaily)
		opportunity.SpreadDaily = spread.String()
		opportunity.SpreadApr = spread.Mul(decimal.NewFromInt(365)).String()
	} else {
		opportunity.Blockers = append(opportunity.Blockers, "funding_interval_unknown")
	}
	if long.AccountID == "" || short.AccountID == "" {
		opportunity.Blockers = append(opportunity.Blockers, "account_unreachable")
	}
	if long.InstrumentID == "" || short.InstrumentID == "" {
		opportunity.Blockers = append(opportunity.Blockers, "instrument_identity_unresolved")
	}
	if long.Stale || short.Stale {
		opportunity.Blockers = append(opportunity.Blockers, "board_stale")
	}
	opportunity.Executable = len(opportunity.Blockers) == 0
	return opportunity, true
}

// compareRates orders two venues by the daily rate when both publish an
// interval, and refuses to order them otherwise. Comparing raw per-interval
// rates across venues on different schedules is the mistake this exists to
// prevent: an hourly 0.0001 pays eight times what an eight-hourly 0.0001 does.
func compareRates(left, right arbitrageOpportunityLeg) int {
	leftValue, leftErr := decimal.NewFromString(left.DailyRate)
	rightValue, rightErr := decimal.NewFromString(right.DailyRate)
	if leftErr != nil || rightErr != nil {
		return 0
	}
	return leftValue.Cmp(rightValue)
}

type boardKey struct{ base, quote string }

// reachableProfiles resolves the caller's named accounts into exact route
// profiles they can trade. Unknown, foreign or
// not-ready accounts drop out silently here — the route reports them as
// unreachable rather than as errors, because an operator with three accounts
// asking about a twelve-venue board is the normal case, not a fault.
func (handler *arbitrageProductHandler) reachableProfiles(r *http.Request, principal string) map[string]string {
	reachable := map[string]string{}
	authority := handler.trading.options.ManualAccounts
	if authority == nil {
		return reachable
	}
	identifiers := r.URL.Query()["accountId"]
	if len(identifiers) > arbitrageMaxAccounts {
		identifiers = identifiers[:arbitrageMaxAccounts]
	}
	for _, identifier := range identifiers {
		account, err := authority.Get(r.Context(), principal, identifier)
		if err != nil || account.ID != identifier || !arbitrageAccountReady(account) {
			continue
		}
		for _, route := range account.Routes {
			if route.RequestedState != manualAccountActive || route.Product != arbitrageProduct ||
				strings.TrimSpace(route.RouteProfileID) == "" {
				continue
			}
			if _, exists := reachable[route.RouteProfileID]; !exists {
				reachable[route.RouteProfileID] = account.ID
			}
		}
	}
	return reachable
}

/* --- Preview -------------------------------------------------------------- */

func (handler *arbitrageProductHandler) createPreview(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.trading.identity(w, r)
	if !ok || !handler.trading.authorizeMutation(w, r) {
		return
	}
	var request arbitragePreviewRequest
	if !decodeTrading(w, r, &request) {
		return
	}
	if validateArbitragePreview(request) != nil {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	authority, snapshot, resolved := handler.resolveAuthority(w, r, principal, request)
	if !resolved {
		return
	}
	codec, ok := handler.trading.options.Arbitrage.(arbitragePreviewCodec)
	if !ok {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	now := codec.PreviewNow()
	if now.IsZero() {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	authority.PrincipalID = principal
	authority.Request = request
	authority.CatalogRevision = snapshot.Revision
	authority.IssuedAt = now
	authority.ExpiresAt = now.Add(arbitragePreviewTTL)
	canonical, _ := json.Marshal(authority)
	previewID, err := codec.SealPreview(principal, canonical)
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	preview, err := handler.priceHedge(r, previewID, request, authority, snapshot)
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "PUBLIC_DATA_NOT_READY")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"meta": httpx.Meta(r), "data": preview})
}

func validateArbitragePreview(request arbitragePreviewRequest) error {
	quantity, err := decimal.NewFromString(request.Quantity)
	if err != nil || !quantity.IsPositive() {
		return orders.ErrNotFound
	}
	if request.Long.AccountID == "" || request.Long.InstrumentID == "" || request.Short.AccountID == "" || request.Short.InstrumentID == "" {
		return orders.ErrNotFound
	}
	// Two legs on one account is not a hedge — it is the same position opened
	// twice, and the reservation netting scope would collide on it anyway.
	if request.Long.AccountID == request.Short.AccountID || request.Long.InstrumentID == request.Short.InstrumentID {
		return orders.ErrNotFound
	}
	switch strings.ToLower(request.OrderType) {
	case "market":
		if request.Long.LimitPrice != "" || request.Short.LimitPrice != "" {
			return orders.ErrNotFound
		}
	case "limit":
		for _, text := range []string{request.Long.LimitPrice, request.Short.LimitPrice} {
			price, priceErr := decimal.NewFromString(text)
			if priceErr != nil || !price.IsPositive() {
				return orders.ErrNotFound
			}
		}
	default:
		return orders.ErrNotFound
	}
	return nil
}

// resolveAuthority establishes both legs against the accounts and the catalog as
// they stand right now. It runs on the preview and again on the command: the
// preview seals what it found, and the command refuses to spend a seal whose
// facts have moved.
func (handler *arbitrageProductHandler) resolveAuthority(w http.ResponseWriter, r *http.Request, principal string, request arbitragePreviewRequest) (arbitragePreviewAuthority, catalog.Snapshot, bool) {
	if handler.trading.options.ManualAccounts == nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return arbitragePreviewAuthority{}, catalog.Snapshot{}, false
	}
	snapshot, err := handler.trading.options.Catalog.Snapshot(r.Context())
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return arbitragePreviewAuthority{}, catalog.Snapshot{}, false
	}
	var authority arbitragePreviewAuthority
	for _, side := range []struct {
		leg     arbitrage.Leg
		request arbitrageLegRequest
		target  *arbitrageLegAuthority
	}{
		{arbitrage.LegLong, request.Long, &authority.Long},
		{arbitrage.LegShort, request.Short, &authority.Short},
	} {
		account, accountErr := handler.trading.options.ManualAccounts.Get(r.Context(), principal, side.request.AccountID)
		if accountErr != nil || account.ID != side.request.AccountID || !arbitrageAccountReady(account) {
			tradingError(w, http.StatusNotFound, "NOT_FOUND")
			return arbitragePreviewAuthority{}, catalog.Snapshot{}, false
		}
		route, routed := manualCatalogAuthority(snapshot, account, side.request.InstrumentID)
		if !routed || route.Product != arbitrageProduct {
			tradingError(w, http.StatusNotFound, "NOT_FOUND")
			return arbitragePreviewAuthority{}, catalog.Snapshot{}, false
		}
		*side.target = arbitrageLegAuthority{
			AccountID: account.ID, InstrumentID: side.request.InstrumentID, LimitPrice: side.request.LimitPrice,
			Venue: route.Venue, VenueEnvironment: route.VenueEnvironment, Product: route.Product, RouteID: route.RouteID,
			Fence: manualAccountFence{
				AccountID: account.ID, RuntimeID: manualRuntimeID(account),
			},
		}
	}
	// Same venue means no cross-venue spread to collect: the two legs would net
	// to flat on one book and pay two sets of fees for the privilege.
	if authority.Long.Venue == authority.Short.Venue && authority.Long.VenueEnvironment == authority.Short.VenueEnvironment {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return arbitragePreviewAuthority{}, catalog.Snapshot{}, false
	}
	// And different underlyings mean the pair is not delta-neutral at all — it
	// is two directional bets wearing a hedge's clothes.
	longBase, longQuote, longFound := arbitrageUnderlying(snapshot, authority.Long.InstrumentID)
	shortBase, shortQuote, shortFound := arbitrageUnderlying(snapshot, authority.Short.InstrumentID)
	if !longFound || !shortFound || longBase != shortBase || longQuote != shortQuote {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return arbitragePreviewAuthority{}, catalog.Snapshot{}, false
	}
	return authority, snapshot, true
}

func arbitrageUnderlying(snapshot catalog.Snapshot, instrumentID string) (string, string, bool) {
	instrument, found := snapshot.Instrument(instrumentID)
	if found && instrument.Ready {
		return instrument.BaseAsset, instrument.QuoteAsset, instrument.BaseAsset != "" && instrument.QuoteAsset != ""
	}
	return "", "", false
}

// priceHedge annotates a sealed preview with what it is expected to earn.
//
// The rates come from the funding board rather than from two per-instrument
// market reads, so the numbers on the confirmation are the same numbers the
// operator just read off the matrix. A board that cannot answer fails the
// preview: a hedge priced at an unknown spread is not a decision anybody can
// make.
func (handler *arbitrageProductHandler) priceHedge(r *http.Request, previewID string, request arbitragePreviewRequest, authority arbitragePreviewAuthority, snapshot catalog.Snapshot) (arbitragePreview, error) {
	board, err := handler.trading.options.FundingBoard.Get(r.Context())
	if err != nil {
		return arbitragePreview{}, err
	}
	quotes := indexBoard(board)
	preview := arbitragePreview{
		PreviewID: previewID, Request: request, Quantity: request.Quantity,
		Blockers: []string{}, Warnings: []string{},
	}
	base, quote, _ := arbitrageUnderlying(snapshot, authority.Long.InstrumentID)
	marks := []decimal.Decimal{}
	dailies := map[arbitrage.Leg]decimal.Decimal{}
	for _, side := range []struct {
		leg       arbitrage.Leg
		authority arbitrageLegAuthority
		target    *arbitragePreviewLeg
	}{
		{arbitrage.LegLong, authority.Long, &preview.Long},
		{arbitrage.LegShort, authority.Short, &preview.Short},
	} {
		*side.target = arbitragePreviewLeg{
			AccountID: side.authority.AccountID, InstrumentID: side.authority.InstrumentID,
			Venue: side.authority.Venue, Environment: side.authority.VenueEnvironment,
			Side: arbitrage.OpenSide(side.leg), LimitPrice: side.authority.LimitPrice,
		}
		found, ok := quotes[quoteKey{
			venue: side.authority.Venue, environment: side.authority.VenueEnvironment,
			base: base, quote: quote,
		}]
		if !ok {
			preview.Warnings = append(preview.Warnings, "funding_rate_unavailable")
			continue
		}
		side.target.Symbol = found.row.Symbol
		side.target.Rate = found.row.Rate
		side.target.IntervalHours = found.row.IntervalHours
		side.target.NextFundingTime = found.row.NextFundingTime
		side.target.MarkPrice = found.row.MarkPrice
		side.target.Stale = found.stale
		if found.stale {
			preview.Warnings = append(preview.Warnings, "board_stale")
		}
		if mark, markErr := decimal.NewFromString(found.row.MarkPrice); markErr == nil && mark.IsPositive() {
			marks = append(marks, mark)
		}
		daily, ok := dailyFundingRate(found.row.Rate, found.row.IntervalHours)
		if !ok {
			preview.Warnings = append(preview.Warnings, "funding_interval_unknown")
			continue
		}
		side.target.DailyRate = daily.String()
		dailies[side.leg] = daily
	}
	quantity, quantityErr := decimal.NewFromString(request.Quantity)
	longDaily, longOK := dailies[arbitrage.LegLong]
	shortDaily, shortOK := dailies[arbitrage.LegShort]
	if longOK && shortOK {
		spread := shortDaily.Sub(longDaily)
		preview.SpreadDaily = spread.String()
		preview.SpreadApr = spread.Mul(decimal.NewFromInt(365)).String()
		if spread.IsNegative() {
			preview.Warnings = append(preview.Warnings, "negative_spread")
		}
		if len(marks) > 0 && quantityErr == nil {
			average := decimal.Sum(marks[0], marks[1:]...).Div(decimal.NewFromInt(int64(len(marks))))
			notional := quantity.Mul(average)
			preview.Notional = notional.Round(8).String()
			preview.DailyFunding = notional.Mul(spread).Round(8).String()
		}
	}
	// Venue fees are not in trading's reach — the gateway prices them per route
	// at execution time — so the figures above are gross. Saying so is cheaper
	// than an operator discovering it from a settled ledger.
	preview.Warnings = append(preview.Warnings, "fees_excluded")
	return preview, nil
}

type quoteKey struct{ venue, environment, base, quote string }

type boardQuote struct {
	row   marketdata.BoardRow
	stale bool
}

func indexBoard(board marketdata.BoardSnapshot) map[quoteKey]boardQuote {
	quotes := map[quoteKey]boardQuote{}
	for _, venue := range board.Venues {
		if venue.Unavailable || venue.Product != arbitrageProduct {
			continue
		}
		for _, row := range venue.Rows {
			if row.BaseAsset == "" || row.QuoteAsset == "" {
				continue
			}
			key := quoteKey{venue: venue.Venue, environment: venue.Environment, base: row.BaseAsset, quote: row.QuoteAsset}
			quotes[key] = boardQuote{row: row, stale: venue.Stale}
		}
	}
	return quotes
}

// dailyFundingRate puts every venue on one clock.
//
// Funding intervals differ across venues — eight hours on most, one on some —
// so per-interval rates are not comparable and must never be subtracted from
// each other directly. A missing or nonsensical interval yields no rate at all
// rather than a plausible wrong one.
func dailyFundingRate(rate string, intervalHours int64) (decimal.Decimal, bool) {
	value, err := decimal.NewFromString(rate)
	if err != nil || intervalHours <= 0 {
		return decimal.Zero, false
	}
	return value.Mul(decimal.NewFromInt(24)).Div(decimal.NewFromInt(intervalHours)), true
}

/* --- Commands ------------------------------------------------------------- */

func (handler *arbitrageProductHandler) createCommand(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.trading.identity(w, r)
	if !ok || !handler.trading.authorizeMutation(w, r) {
		return
	}
	var request arbitrageCommandRequest
	if !decodeTrading(w, r, &request) {
		return
	}
	if request.LineageID == "" || request.IdempotencyKey == "" || request.IdempotencyKey != strings.TrimSpace(r.Header.Get("Idempotency-Key")) {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	switch request.Action {
	case "cancel":
		if request.PreviewID != "" || request.OrderID == "" {
			tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
			return
		}
		handler.cancelLeg(w, r, principal, request)
		return
	case "close":
		if request.PreviewID != "" || request.OrderID != "" {
			tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
			return
		}
		handler.closePair(w, r, principal, request)
		return
	case "open":
		if request.PreviewID == "" || request.OrderID != "" {
			tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
			return
		}
		handler.openPair(w, r, principal, request)
		return
	default:
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
	}
}

func (handler *arbitrageProductHandler) openPair(w http.ResponseWriter, r *http.Request, principal string, request arbitrageCommandRequest) {
	if !handler.trading.options.Arbitrage.Ready() {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	codec, ok := handler.trading.options.Arbitrage.(arbitragePreviewCodec)
	if !ok {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	payload, err := codec.OpenPreview(principal, request.PreviewID)
	if err != nil {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	var sealed arbitragePreviewAuthority
	now := codec.PreviewNow()
	if json.Unmarshal(payload, &sealed) != nil || sealed.PrincipalID != principal ||
		validateArbitragePreview(sealed.Request) != nil || !arbitrageLegSealed(sealed.Long) || !arbitrageLegSealed(sealed.Short) ||
		sealed.CatalogRevision == "" || sealed.IssuedAt.IsZero() || sealed.ExpiresAt.IsZero() ||
		sealed.ExpiresAt.Sub(sealed.IssuedAt) != arbitragePreviewTTL || now.Before(sealed.IssuedAt) || !now.Before(sealed.ExpiresAt) {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	// Re-establish both legs from scratch and require the seal to still describe
	// them. A rotated credential, a withdrawn route or a re-published catalog
	// between preview and confirmation all land here as a refusal rather than as
	// a fence violation at the venue.
	current, snapshot, resolved := handler.resolveAuthority(w, r, principal, sealed.Request)
	if !resolved {
		return
	}
	if snapshot.Revision != sealed.CatalogRevision ||
		current.Long != sealed.Long || current.Short != sealed.Short {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	orderType := strings.ToLower(sealed.Request.OrderType)
	legs := []arbitrageLegOrder{
		{
			Leg: arbitrage.LegLong, Authority: sealed.Long, Counter: sealed.Short, Quantity: sealed.Request.Quantity,
			OrderType: orderType, LimitPrice: sealed.Long.LimitPrice, TimeInForce: sealed.Request.TimeInForce,
		},
		{
			Leg: arbitrage.LegShort, Authority: sealed.Short, Counter: sealed.Long, Quantity: sealed.Request.Quantity,
			OrderType: orderType, LimitPrice: sealed.Short.LimitPrice, TimeInForce: sealed.Request.TimeInForce,
		},
	}
	handler.submitLegs(w, r, principal, request, legs)
}

func arbitrageLegSealed(leg arbitrageLegAuthority) bool {
	return leg.AccountID != "" && leg.InstrumentID != "" && leg.Venue != "" && leg.VenueEnvironment != "" &&
		leg.Product == arbitrageProduct && leg.RouteID != "" && leg.Fence.AccountID == leg.AccountID &&
		strings.TrimSpace(leg.Fence.RuntimeID) != ""
}

// submitLegs places the legs in order and stops at the first one that fails.
//
// This is the half-open branch the operator asked for in writing. Once a leg has
// a durable command row, the request has really happened, so the response is an
// accepted execution document describing what exists — not an error, which would
// read as "nothing was placed". Nothing here unwinds a leg: a filled leg whose
// sibling never made it is a directional position, and deciding what to do with
// one is a person's job.
func (handler *arbitrageProductHandler) submitLegs(w http.ResponseWriter, r *http.Request, principal string, request arbitrageCommandRequest, legs []arbitrageLegOrder) {
	execution := arbitrageExecution{LineageID: request.LineageID, Legs: []arbitrage.LegView{}, Failures: []arbitrageLegFailure{}}
	replayed := 0
	for index, order := range legs {
		command, replay, err := handler.placeLeg(r, principal, request.LineageID, request.IdempotencyKey, order)
		if err != nil {
			// Nothing durable exists for the first leg yet, so the whole request
			// can still be reported as refused with its real cause.
			if index == 0 && command.CommandID == "" {
				tradingFailure(w, r, "arbitrage.open", err)
				return
			}
			execution.Failures = append(execution.Failures, arbitrageLegFailure{
				Leg: order.Leg, Stage: "execute", Code: codeForTradingError(err),
			})
			if command.CommandID != "" {
				if view, viewErr := arbitrage.DescribeLeg(command); viewErr == nil {
					execution.Legs = append(execution.Legs, view)
				}
			}
			break
		}
		if replay {
			replayed++
		}
		view, viewErr := arbitrage.DescribeLeg(command)
		if viewErr != nil {
			execution.Failures = append(execution.Failures, arbitrageLegFailure{
				Leg: order.Leg, Stage: "describe", Code: "INTERNAL_ERROR",
			})
			break
		}
		execution.Legs = append(execution.Legs, view)
	}
	execution.State = arbitrage.ClassifyPair(execution.Legs)
	status := http.StatusAccepted
	if replayed == len(legs) {
		status = http.StatusOK
	}
	httpx.WriteJSON(w, status, map[string]any{"meta": httpx.Meta(r), "data": execution})
}

// placeLeg mints and dispatches one leg.
//
// The idempotency key is suffixed per leg for both the reservation and the
// command. Without the suffix the second leg would replay the first one's
// command — the order service keys idempotency by principal and key alone — and
// a two-leg hedge would quietly become one leg placed twice.
func (handler *arbitrageProductHandler) placeLeg(r *http.Request, principal, lineageID, idempotencyKey string, order arbitrageLegOrder) (orders.Command, bool, error) {
	side := arbitrage.OpenSide(order.Leg)
	if order.ReduceOnly {
		side = arbitrage.ReduceSide(order.Leg)
	}
	payload, err := arbitrage.EncodeIntent(
		order.Leg, order.Authority.InstrumentID, side, order.Quantity, order.OrderType,
		order.LimitPrice, order.TimeInForce, order.Authority.RouteID,
		order.Counter.AccountID, order.Counter.InstrumentID, order.ReduceOnly,
		order.Authority.Fence.RuntimeID,
	)
	if err != nil {
		return orders.Command{}, false, err
	}
	kind := reservations.KindOpen
	if order.ReduceOnly {
		kind = reservations.KindClose
	}
	suffix := ":" + string(order.Leg)
	reservation, err := handler.trading.options.Reservations.Create(r.Context(), reservations.CreateRequest{
		PrincipalID: principal, ProductID: arbitrage.ProductID, Kind: kind,
		Scope: reservations.NettingScope{
			AccountID: order.Authority.AccountID, Venue: order.Authority.Venue,
			InstrumentID: order.Authority.InstrumentID, VenueEnvironment: order.Authority.VenueEnvironment,
		},
		Quantity: order.Quantity, IdempotencyKey: idempotencyKey + suffix + ":reservation",
	})
	if err != nil {
		return orders.Command{}, false, err
	}
	command, replay, err := handler.trading.options.Commands.Create(r.Context(), orders.CreateRequest{
		PrincipalID: principal, AccountID: order.Authority.AccountID, ProductID: arbitrage.ProductID, LineageID: lineageID,
		ReservationID: reservation.ReservationID, IdempotencyKey: idempotencyKey + suffix, Method: r.Method, Path: r.URL.Path,
		CommandKind: arbitrage.CommandKindPlace, Payload: payload,
	})
	if err != nil {
		return orders.Command{}, false, err
	}
	if !replay || resumableManualState(command.State) {
		command, err = handler.trading.options.Arbitrage.Execute(r.Context(), command)
		if err != nil {
			return command, replay, err
		}
		publishCommandActivity(handler.trading.options.Activity, command)
	}
	return command, replay, nil
}

// closePair sends a reverse reduce-only market order for every leg still holding
// quantity.
//
// It closes whatever is open rather than insisting on two legs, which is also
// the remedy for a partially open pair: the operator closes the leg that filled
// and the request is complete. Fewer than two legs is therefore a normal
// outcome, not a degraded one.
func (handler *arbitrageProductHandler) closePair(w http.ResponseWriter, r *http.Request, principal string, request arbitrageCommandRequest) {
	if !handler.trading.options.Arbitrage.Ready() {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	commands, err := handler.trading.options.Commands.ListOwned(r.Context(), principal, arbitrage.ProductID)
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	if replayed, conflict, handled := handler.replayClose(commands, request); handled {
		if conflict {
			tradingError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
			return
		}
		handler.resumeClose(w, r, request, replayed)
		return
	}
	positions, err := handler.trading.options.Portfolio.ListContext(r.Context(), principal, "", arbitrage.ProductID)
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	openings := map[string]arbitrage.LegView{}
	for _, command := range commands {
		if command.LineageID != request.LineageID {
			continue
		}
		view, viewErr := arbitrage.DescribeLeg(command)
		if viewErr != nil || view.ReduceOnly {
			continue
		}
		openings[command.AccountID+"\x00"+view.InstrumentID] = view
	}
	snapshot, err := handler.trading.options.Catalog.Snapshot(r.Context())
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	legs := []arbitrageLegOrder{}
	for _, position := range positions {
		if position.LineageID != request.LineageID {
			continue
		}
		quantity, quantityErr := decimal.NewFromString(position.Quantity)
		if quantityErr != nil || quantity.IsZero() {
			continue
		}
		opening, owned := openings[position.AccountID+"\x00"+position.InstrumentID]
		if !owned {
			continue
		}
		authority, ready := handler.closeAuthority(r, principal, position, snapshot)
		if !ready {
			tradingError(w, http.StatusNotFound, "NOT_FOUND")
			return
		}
		legs = append(legs, arbitrageLegOrder{
			Leg: opening.Leg, Authority: authority, Counter: arbitrageCounterOf(openings, opening),
			Quantity: quantity.Abs().String(), OrderType: "market", ReduceOnly: true,
		})
	}
	if len(legs) == 0 {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	// Deterministic order so a retried close reuses the same per-leg keys.
	slices.SortStableFunc(legs, func(left, right arbitrageLegOrder) int {
		return strings.Compare(string(left.Leg), string(right.Leg))
	})
	handler.submitLegs(w, r, principal, request, legs)
}

// arbitrageCounterOf names the sibling for a closing leg. The intent requires
// one, and a pair whose other half was never commanded still has to close the
// half that exists — so a leg with no sibling names itself rather than being
// refused for the sake of a field.
func arbitrageCounterOf(openings map[string]arbitrage.LegView, leg arbitrage.LegView) arbitrageLegAuthority {
	for _, candidate := range openings {
		if candidate.Leg != leg.Leg {
			return arbitrageLegAuthority{AccountID: candidate.AccountID, InstrumentID: candidate.InstrumentID}
		}
	}
	return arbitrageLegAuthority{AccountID: leg.AccountID, InstrumentID: leg.InstrumentID}
}

func (handler *arbitrageProductHandler) closeAuthority(r *http.Request, principal string, position portfolio.Position, snapshot catalog.Snapshot) (arbitrageLegAuthority, bool) {
	if handler.trading.options.ManualAccounts == nil {
		return arbitrageLegAuthority{}, false
	}
	account, err := handler.trading.options.ManualAccounts.Get(r.Context(), principal, position.AccountID)
	if err != nil || !arbitrageAccountReady(account) {
		return arbitrageLegAuthority{}, false
	}
	route, routed := manualCatalogAuthority(snapshot, account, position.InstrumentID)
	if !routed || route.Product != arbitrageProduct {
		return arbitrageLegAuthority{}, false
	}
	return arbitrageLegAuthority{
		AccountID: account.ID, InstrumentID: position.InstrumentID,
		Venue: route.Venue, VenueEnvironment: route.VenueEnvironment, Product: route.Product, RouteID: route.RouteID,
		Fence: manualAccountFence{
			AccountID: account.ID, RuntimeID: manualRuntimeID(account),
		},
	}, true
}

// replayClose finds the commands a previous attempt with this idempotency key
// already minted. A key that produced something other than reduce-only market
// orders on this lineage is a genuine conflict: the caller reused a key across
// two different intentions, and answering it with either outcome would be a lie.
func (handler *arbitrageProductHandler) replayClose(commands []orders.Command, request arbitrageCommandRequest) ([]orders.Command, bool, bool) {
	prefix := request.IdempotencyKey + ":"
	matched := []orders.Command{}
	for _, command := range commands {
		if !strings.HasPrefix(command.IdempotencyKey, prefix) {
			continue
		}
		view, err := arbitrage.DescribeLeg(command)
		if command.LineageID != request.LineageID || err != nil || view.OrderType != "market" || !view.ReduceOnly {
			return nil, true, true
		}
		matched = append(matched, command)
	}
	return matched, false, len(matched) > 0
}

func (handler *arbitrageProductHandler) resumeClose(w http.ResponseWriter, r *http.Request, request arbitrageCommandRequest, commands []orders.Command) {
	execution := arbitrageExecution{LineageID: request.LineageID, Legs: []arbitrage.LegView{}, Failures: []arbitrageLegFailure{}}
	for _, command := range commands {
		view, err := arbitrage.DescribeLeg(command)
		if err != nil {
			continue
		}
		if resumableManualState(command.State) {
			resumed, executeErr := handler.trading.options.Arbitrage.Execute(r.Context(), command)
			if executeErr != nil {
				execution.Failures = append(execution.Failures, arbitrageLegFailure{
					Leg: view.Leg, Stage: "execute", Code: codeForTradingError(executeErr),
				})
			}
			command = resumed
			if resumedView, viewErr := arbitrage.DescribeLeg(command); viewErr == nil {
				view = resumedView
			}
		}
		execution.Legs = append(execution.Legs, view)
	}
	execution.State = arbitrage.ClassifyPair(execution.Legs)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": execution})
}

// cancelLeg withdraws one resting leg.
//
// A resting leg cannot be closed: a reduce-only order against a position that
// has not opened yet is refused by the venue. Cancel is therefore not a
// duplicate of close but the other half of the remedy for a stuck pair.
func (handler *arbitrageProductHandler) cancelLeg(w http.ResponseWriter, r *http.Request, principal string, request arbitrageCommandRequest) {
	if !handler.trading.options.Arbitrage.Ready() {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	command, err := handler.trading.options.Commands.GetOwned(r.Context(), principal, request.OrderID)
	if err != nil || command.ProductID != arbitrage.ProductID || command.LineageID != request.LineageID {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	status := http.StatusOK
	if resumableManualState(command.State) {
		command, err = handler.trading.options.Arbitrage.Cancel(r.Context(), command)
		if err != nil {
			tradingFailure(w, r, "arbitrage.cancel", err)
			return
		}
		publishCommandActivityAs(handler.trading.options.Activity, command, "command.cancel_accepted")
		status = http.StatusAccepted
	}
	httpx.WriteJSON(w, status, map[string]any{"meta": httpx.Meta(r), "data": command})
}

/* --- Positions ------------------------------------------------------------ */

// positions reports one row per lineage: both legs, whatever the pair still
// holds, and the funding it has accrued.
//
// Funding is summed straight from the ledger's funding entries rather than read
// off a PnL snapshot, because a snapshot demands all four attribution fields and
// a hedge legitimately spans two accounts and two instruments.
func (handler *arbitrageProductHandler) positions(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.trading.identity(w, r)
	if !ok {
		return
	}
	commands, err := handler.trading.options.Commands.ListOwned(r.Context(), principal, arbitrage.ProductID)
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	held, err := handler.trading.options.Portfolio.ListContext(r.Context(), principal, "", arbitrage.ProductID)
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	entries, err := handler.trading.options.Ledger.List(r.Context(), ledger.Query{
		PrincipalID: principal, ProductID: arbitrage.ProductID,
	})
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}

	pairs := map[string]*arbitragePair{}
	order := []string{}
	pairFor := func(lineageID string) *arbitragePair {
		pair, exists := pairs[lineageID]
		if !exists {
			pair = &arbitragePair{
				LineageID: lineageID, Legs: []arbitrage.LegView{},
				Positions: []portfolio.Position{}, Funding: []arbitrageAmount{}, Blockers: []string{},
			}
			pairs[lineageID] = pair
			order = append(order, lineageID)
		}
		return pair
	}
	for _, command := range commands {
		pair := pairFor(command.LineageID)
		view, viewErr := arbitrage.DescribeLeg(command)
		if viewErr != nil {
			// A payload this product wrote must decode; one that does not is a
			// defect, and hiding the leg would understate the pair's exposure.
			if !slices.Contains(pair.Blockers, "leg_payload_unreadable") {
				pair.Blockers = append(pair.Blockers, "leg_payload_unreadable")
			}
			continue
		}
		pair.Legs = append(pair.Legs, view)
		if command.UpdatedAt.After(pair.UpdatedAt) {
			pair.UpdatedAt = command.UpdatedAt
		}
	}
	for _, position := range held {
		pair, exists := pairs[position.LineageID]
		if !exists {
			continue
		}
		pair.Positions = append(pair.Positions, position)
	}
	funding := map[string]map[string]decimal.Decimal{}
	for _, entry := range entries {
		if entry.Kind != ledger.KindFunding {
			continue
		}
		if _, exists := pairs[entry.LineageID]; !exists {
			continue
		}
		amount, amountErr := decimal.NewFromString(entry.Amount)
		if amountErr != nil {
			continue
		}
		if funding[entry.LineageID] == nil {
			funding[entry.LineageID] = map[string]decimal.Decimal{}
		}
		funding[entry.LineageID][entry.Currency] = funding[entry.LineageID][entry.Currency].Add(amount)
	}
	items := make([]arbitragePair, 0, len(order))
	for _, lineageID := range order {
		pair := pairs[lineageID]
		pair.State = arbitrage.ClassifyPair(pair.Legs)
		for _, currency := range sortedCurrencies(funding[lineageID]) {
			pair.Funding = append(pair.Funding, arbitrageAmount{Currency: currency, Amount: funding[lineageID][currency].String()})
		}
		items = append(items, *pair)
	}
	slices.SortStableFunc(items, func(left, right arbitragePair) int {
		return right.UpdatedAt.Compare(left.UpdatedAt)
	})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": items}})
}

// sortedCurrencies orders a lineage's accumulated funding by currency so the
// same pair renders its settlement columns in the same order on every poll. Map
// iteration would reshuffle them, which reads as data churn in a table the
// operator is watching for real movement.
func sortedCurrencies(values map[string]decimal.Decimal) []string {
	currencies := make([]string, 0, len(values))
	for currency := range values {
		currencies = append(currencies, currency)
	}
	slices.Sort(currencies)
	return currencies
}

/* --- Account readiness ---------------------------------------------------- */

// arbitrageAccountReady is manualAccountReady with a different product name.
// They share a body rather than a signature because manualAccountReady is
// asserted on directly by manual's tests and must keep taking exactly the one
// argument it takes today.
func arbitrageAccountReady(account ManualAccount) bool {
	return accountReadyFor(account, arbitrage.ProductID)
}

// accountReadyFor no longer reads mode or environment, for the reason
// manualAccountReady records: the two were only ever cross-checked against each
// other, and with one environment left the check admitted nothing in test mode.
func accountReadyFor(account ManualAccount, productID string) bool {
	if account.ID == "" || manualRuntimeID(account) == "" || account.State != manualAccountActive {
		return false
	}
	for _, candidate := range account.ProductIDs {
		if candidate == productID {
			return true
		}
	}
	return false
}
