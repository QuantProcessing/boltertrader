package httpapi

import (
	"context"
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
	Strategy    string              `json:"strategy,omitempty"`
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
	Strategy        string                  `json:"strategy"`
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
	Product         string    `json:"product"`
	Symbol          string    `json:"symbol,omitempty"`
	Side            string    `json:"side"`
	LimitPrice      string    `json:"limitPrice,omitempty"`
	Rate            string    `json:"rate,omitempty"`
	IntervalHours   int64     `json:"intervalHours,omitempty"`
	NextFundingTime time.Time `json:"nextFundingTime,omitzero"`
	MarkPrice       string    `json:"markPrice,omitempty"`
	BestBid         string    `json:"bestBid,omitempty"`
	BestAsk         string    `json:"bestAsk,omitempty"`
	ExecutionPrice  string    `json:"executionPrice,omitempty"`
	DailyRate       string    `json:"dailyRate,omitempty"`
	Stale           bool      `json:"stale,omitempty"`
}

type arbitragePreview struct {
	PreviewID    string                  `json:"previewId"`
	Strategy     string                  `json:"strategy"`
	Request      arbitragePreviewRequest `json:"request"`
	Long         arbitragePreviewLeg     `json:"long"`
	Short        arbitragePreviewLeg     `json:"short"`
	Quantity     string                  `json:"quantity"`
	Notional     string                  `json:"notional,omitempty"`
	SpreadDaily  string                  `json:"spreadDaily,omitempty"`
	SpreadApr    string                  `json:"spreadApr,omitempty"`
	BasisRate    string                  `json:"basisRate,omitempty"`
	PriceSpread  string                  `json:"priceSpread,omitempty"`
	DailyFunding string                  `json:"dailyFunding,omitempty"`
	Blockers     []string                `json:"blockers"`
	Warnings     []string                `json:"warnings"`
}

type arbitrageOpportunityLeg struct {
	Venue                     string    `json:"venue"`
	Environment               string    `json:"environment"`
	Product                   string    `json:"product"`
	RouteProfileID            string    `json:"routeProfileId"`
	Settlement                string    `json:"settlement,omitempty"`
	MarketFamily              string    `json:"marketFamily"`
	AssetClass                string    `json:"assetClass"`
	ClassificationSource      string    `json:"classificationSource,omitempty"`
	AccountID                 string    `json:"accountId,omitempty"`
	InstrumentID              string    `json:"instrumentId"`
	Rate                      string    `json:"rate,omitempty"`
	IntervalHours             int64     `json:"intervalHours,omitempty"`
	NextFundingTime           time.Time `json:"nextFundingTime,omitzero"`
	MarkPrice                 string    `json:"markPrice,omitempty"`
	IndexPrice                string    `json:"indexPrice,omitempty"`
	ReferencePrice            string    `json:"referencePrice,omitempty"`
	OpenInterest              string    `json:"openInterest,omitempty"`
	OpenInterestNotional      string    `json:"openInterestNotional,omitempty"`
	OpenInterestNotionalAsset string    `json:"openInterestNotionalAsset,omitempty"`
	FundingObservedAt         time.Time `json:"fundingObservedAt,omitzero"`
	PriceObservedAt           time.Time `json:"priceObservedAt,omitzero"`
	OpenInterestObservedAt    time.Time `json:"openInterestObservedAt,omitzero"`
	VenueStatus               string    `json:"venueStatus,omitempty"`
	DailyRate                 string    `json:"dailyRate,omitempty"`
	Stale                     bool      `json:"stale,omitempty"`
}

type arbitrageOpportunity struct {
	ID             string                  `json:"id"`
	Symbol         string                  `json:"symbol"`
	BaseAsset      string                  `json:"baseAsset"`
	QuoteAsset     string                  `json:"quoteAsset"`
	AssetClass     string                  `json:"assetClass"`
	StrategyFamily string                  `json:"strategyFamily"`
	Strategy       string                  `json:"strategy"`
	Long           arbitrageOpportunityLeg `json:"long"`
	Short          arbitrageOpportunityLeg `json:"short"`
	SpreadDaily    string                  `json:"spreadDaily,omitempty"`
	SpreadApr      string                  `json:"spreadApr,omitempty"`
	BasisRate      string                  `json:"basisRate,omitempty"`
	PriceSpread    string                  `json:"priceSpread,omitempty"`
	Score          string                  `json:"score,omitempty"`
	Executable     bool                    `json:"executable"`
	Blockers       []string                `json:"blockers"`
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
	Closing     bool
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

const (
	arbitrageProduct               = "perp"
	arbitrageStrategyFunding       = "cross_perp_funding"
	arbitrageStrategySpotPerpBasis = "spot_perp_basis"
	arbitrageStrategyPerpSpread    = "cross_perp_spread"
)

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
	principal, _ := handler.trading.options.Principal(r)
	if len(r.URL.Query()["accountId"]) > 0 && strings.TrimSpace(principal) == "" {
		tradingError(w, http.StatusUnauthorized, "AUTH_REQUIRED")
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
	snapshot, err := handler.trading.options.Catalog.Snapshot(r.Context())
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
			instrument, listed := canonicalBoardPerp(snapshot, venue, row)
			if !listed {
				// The funding board is intentionally diagnostic and may retain a
				// venue fact while its catalog column is unavailable. An arbitrage
				// opportunity is a stronger promise: both legs must already resolve
				// to canonical, trade-addressable instruments so history, preview and
				// execution all use the same identity.
				continue
			}
			// Group public facts before applying a strategy's field requirements.
			// Funding carry needs a rate and interval, whereas a TradFi
			// cross-perpetual spread only needs current prices. Requiring a rate
			// here made a valid price opportunity disappear merely because the
			// venue published funding through a different channel.
			key := boardKey{base: instrument.BaseAsset, quote: instrument.QuoteAsset}
			leg := arbitrageOpportunityLeg{
				Venue: venue.Venue, Environment: venue.Environment, Product: venue.Product,
				RouteProfileID: venue.RouteProfileID, Settlement: instrument.Settlement,
				MarketFamily: instrument.MarketFamily, AssetClass: instrument.AssetClass,
				ClassificationSource: instrument.ClassificationSource,
				InstrumentID:         instrument.InstrumentID, Rate: row.Rate,
				IntervalHours: row.IntervalHours, NextFundingTime: row.NextFundingTime,
				MarkPrice: row.MarkPrice, IndexPrice: row.IndexPrice, ReferencePrice: row.MarkPrice,
				OpenInterest:              row.OpenInterest,
				OpenInterestNotional:      row.OpenInterestNotional,
				OpenInterestNotionalAsset: row.OpenInterestNotionalAsset,
				FundingObservedAt:         row.FundingObservedAt, PriceObservedAt: row.PriceObservedAt,
				OpenInterestObservedAt: row.OpenInterestObservedAt, VenueStatus: venue.Status,
				Stale: observationStale(board.ObservedAt, row.FundingObservedAt, time.Minute),
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
	items = append(items, buildTradFiPerpetualSpreads(grouped, board.ObservedAt)...)
	items = append(items, buildCryptoSpotPerpBasis(snapshot, board, reachable)...)
	// Comparable spreads first and largest first within them. Incomplete market
	// facts remain visible on the funding board but never enter this strategy
	// list, so every item here has an honest score and canonical identities.
	slices.SortStableFunc(items, func(left, right arbitrageOpportunity) int {
		leftValue, leftOK := decimal.NewFromString(left.Score)
		rightValue, rightOK := decimal.NewFromString(right.Score)
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
	fundingLegs := make([]arbitrageOpportunityLeg, 0, len(legs))
	for _, leg := range legs {
		if strings.TrimSpace(leg.InstrumentID) == "" || strings.TrimSpace(leg.Rate) == "" {
			continue
		}
		if _, err := decimal.NewFromString(leg.Rate); err != nil {
			continue
		}
		if _, err := decimal.NewFromString(leg.DailyRate); err != nil {
			// Comparing raw rates from different settlement clocks creates a
			// plausible but dimensionally invalid spread. Until the venue's quote
			// period is known this is a board fact, not an opportunity.
			continue
		}
		fundingLegs = append(fundingLegs, leg)
	}
	legs = fundingLegs
	assetClass, compatible := opportunityAssetClass(legs)
	if !compatible {
		// The same ticker can name unrelated economic assets. Once two venues
		// explicitly disagree, pairing them would manufacture an arbitrage out
		// of a naming collision; unknown legs never overrule a known class.
		return arbitrageOpportunity{}, false
	}
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

	// Indices keep the direction explicit even when two venues publish the same
	// daily rate. Every leg admitted above has a comparable daily value.
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
		ID:     arbitrageOpportunityID(arbitrageStrategyFunding, long, short),
		Symbol: key.base + "-" + key.quote, BaseAsset: key.base, QuoteAsset: key.quote,
		AssetClass: assetClass, StrategyFamily: strategyFamily(assetClass), Strategy: arbitrageStrategyFunding,
		Long: long, Short: short, Blockers: []string{},
	}
	longDaily, _ := decimal.NewFromString(long.DailyRate)
	shortDaily, _ := decimal.NewFromString(short.DailyRate)
	spread := shortDaily.Sub(longDaily)
	opportunity.SpreadDaily = spread.String()
	opportunity.SpreadApr = spread.Mul(decimal.NewFromInt(365)).String()
	opportunity.Score = spread.Abs().String()
	if long.AccountID == "" || short.AccountID == "" {
		opportunity.Blockers = append(opportunity.Blockers, "account_unreachable")
	}
	if long.Stale || short.Stale {
		opportunity.Blockers = append(opportunity.Blockers, "board_stale")
	}
	opportunity.Executable = len(opportunity.Blockers) == 0
	return opportunity, true
}

// buildTradFiPerpetualSpreads is deliberately separate from the funding
// reducer above. It ranks price dislocation, not carry: the cheapest mark is
// bought and the dearest is sold, while funding remains visible only as leg
// context. The executable preview replaces both marks with live BBO prices.
func buildTradFiPerpetualSpreads(grouped map[boardKey][]arbitrageOpportunityLeg, observedAt time.Time) []arbitrageOpportunity {
	items := make([]arbitrageOpportunity, 0, len(grouped))
	for key, all := range grouped {
		legs := make([]arbitrageOpportunityLeg, 0, len(all))
		seen := map[string]struct{}{}
		for _, leg := range all {
			if strings.TrimSpace(leg.InstrumentID) == "" || !tradFiAssetClass(leg.AssetClass) {
				continue
			}
			leg.Stale = observationStale(observedAt, leg.PriceObservedAt, 15*time.Second)
			mark, err := decimal.NewFromString(leg.MarkPrice)
			if err != nil || !mark.IsPositive() {
				continue
			}
			identity := leg.Venue + "\x00" + leg.Environment
			if _, duplicate := seen[identity]; duplicate {
				continue
			}
			seen[identity] = struct{}{}
			legs = append(legs, leg)
		}
		assetClass, compatible := opportunityAssetClass(legs)
		if !compatible || !tradFiAssetClass(assetClass) || len(legs) < 2 {
			continue
		}
		lowest, highest := 0, 0
		for index := 1; index < len(legs); index++ {
			value, _ := decimal.NewFromString(legs[index].MarkPrice)
			low, _ := decimal.NewFromString(legs[lowest].MarkPrice)
			high, _ := decimal.NewFromString(legs[highest].MarkPrice)
			if value.LessThan(low) {
				lowest = index
			}
			if value.GreaterThan(high) {
				highest = index
			}
		}
		if lowest == highest {
			continue
		}
		long, short := legs[lowest], legs[highest]
		low, _ := decimal.NewFromString(long.MarkPrice)
		high, _ := decimal.NewFromString(short.MarkPrice)
		mid := high.Add(low).Div(decimal.NewFromInt(2))
		spread := high.Sub(low).Div(mid)
		candidate := arbitrageOpportunity{
			ID:     arbitrageOpportunityID(arbitrageStrategyPerpSpread, long, short),
			Symbol: key.base + "-" + key.quote, BaseAsset: key.base, QuoteAsset: key.quote,
			AssetClass: assetClass, StrategyFamily: "tradfi_perpetual", Strategy: arbitrageStrategyPerpSpread,
			Long: long, Short: short, PriceSpread: spread.String(), Score: spread.Abs().String(), Blockers: []string{},
		}
		candidate.Long.ReferencePrice = candidate.Long.MarkPrice
		candidate.Short.ReferencePrice = candidate.Short.MarkPrice
		candidate.Blockers = appendOpportunityBlockers(candidate.Blockers, long, short)
		candidate.Executable = len(candidate.Blockers) == 0
		items = append(items, candidate)
	}
	return items
}

// buildCryptoSpotPerpBasis discovers cash-and-carry pairs without issuing an
// order-book request per catalog row. Perpetual mark/index prices are bulk
// venue facts and are used only for ranking. The spot leg is an exact catalog
// instrument; preview time reads both books and seals the actual buy/sell edge.
func buildCryptoSpotPerpBasis(snapshot catalog.Snapshot, board marketdata.BoardSnapshot, reachable map[string]string) []arbitrageOpportunity {
	spots := map[boardKey][]arbitrageOpportunityLeg{}
	for _, instrument := range snapshot.Instruments {
		if !instrument.Ready || instrument.Product != "spot" || instrument.BaseAsset == "" || instrument.QuoteAsset == "" {
			continue
		}
		profile := routeProfileForInstrument(snapshot, instrument)
		if profile == "" {
			continue
		}
		leg := arbitrageOpportunityLeg{
			Venue: instrument.Venue, Environment: instrument.Environment, Product: instrument.Product,
			RouteProfileID: profile, Settlement: instrument.Settlement,
			MarketFamily: instrument.MarketFamily, AssetClass: instrument.AssetClass,
			ClassificationSource: instrument.ClassificationSource, InstrumentID: instrument.InstrumentID,
		}
		if accountID, ok := reachable[profile]; ok {
			leg.AccountID = accountID
		}
		key := boardKey{base: instrument.BaseAsset, quote: instrument.QuoteAsset}
		spots[key] = append(spots[key], leg)
	}

	best := map[boardKey]arbitrageOpportunity{}
	for _, venue := range board.Venues {
		if venue.Unavailable || venue.Product != arbitrageProduct {
			continue
		}
		for _, row := range venue.Rows {
			instrument, listed := canonicalBoardPerp(snapshot, venue, row)
			if !listed {
				continue
			}
			key := boardKey{base: instrument.BaseAsset, quote: instrument.QuoteAsset}
			if key.base == "" || key.quote == "" || len(spots[key]) == 0 {
				continue
			}
			mark, markErr := decimal.NewFromString(row.MarkPrice)
			index, indexErr := decimal.NewFromString(row.IndexPrice)
			if markErr != nil || indexErr != nil || !mark.IsPositive() || !index.IsPositive() {
				continue
			}
			perp := arbitrageOpportunityLeg{
				Venue: venue.Venue, Environment: venue.Environment, Product: venue.Product,
				RouteProfileID: venue.RouteProfileID, Settlement: instrument.Settlement,
				MarketFamily: instrument.MarketFamily, AssetClass: instrument.AssetClass,
				ClassificationSource: instrument.ClassificationSource, InstrumentID: instrument.InstrumentID,
				Rate: row.Rate, IntervalHours: row.IntervalHours, NextFundingTime: row.NextFundingTime,
				MarkPrice: row.MarkPrice, IndexPrice: row.IndexPrice, ReferencePrice: row.MarkPrice,
				OpenInterest: row.OpenInterest, OpenInterestNotional: row.OpenInterestNotional,
				OpenInterestNotionalAsset: row.OpenInterestNotionalAsset,
				FundingObservedAt:         row.FundingObservedAt, PriceObservedAt: row.PriceObservedAt,
				OpenInterestObservedAt: row.OpenInterestObservedAt, VenueStatus: venue.Status,
			}
			perp.Stale = observationStale(board.ObservedAt, row.PriceObservedAt, 15*time.Second)
			if daily, ok := dailyFundingRate(row.Rate, row.IntervalHours); ok {
				perp.DailyRate = daily.String()
			}
			if accountID, ok := reachable[venue.RouteProfileID]; ok {
				perp.AccountID = accountID
			}
			basis := mark.Sub(index).Div(index)
			for _, spot := range spots[key] {
				assetClass, compatible := spotPerpAssetClass(spot.AssetClass, perp.AssetClass)
				if !compatible {
					continue
				}
				spot.ReferencePrice = row.IndexPrice
				candidate := arbitrageOpportunity{
					ID:     arbitrageOpportunityID(arbitrageStrategySpotPerpBasis, spot, perp),
					Symbol: key.base + "-" + key.quote, BaseAsset: key.base, QuoteAsset: key.quote,
					AssetClass: assetClass, StrategyFamily: "crypto_continuous", Strategy: arbitrageStrategySpotPerpBasis,
					Long: spot, Short: perp, BasisRate: basis.String(), Score: basis.Abs().String(), Blockers: []string{},
				}
				candidate.Blockers = appendOpportunityBlockers(candidate.Blockers, spot, perp)
				if !basis.IsPositive() {
					candidate.Blockers = append(candidate.Blockers, "reverse_spot_borrow_unsupported")
				}
				candidate.Executable = len(candidate.Blockers) == 0
				current, exists := best[key]
				if !exists || betterBasisCandidate(candidate, current) {
					best[key] = candidate
				}
			}
		}
	}
	items := make([]arbitrageOpportunity, 0, len(best))
	for _, candidate := range best {
		items = append(items, candidate)
	}
	return items
}

func betterBasisCandidate(candidate, current arbitrageOpportunity) bool {
	if candidate.Executable != current.Executable {
		return candidate.Executable
	}
	left, leftErr := decimal.NewFromString(candidate.BasisRate)
	right, rightErr := decimal.NewFromString(current.BasisRate)
	if leftErr == nil && rightErr == nil && !left.Equal(right) {
		return left.GreaterThan(right)
	}
	// Prefer same-venue cash and perp when the edge is identical: it removes a
	// transfer dependency without inventing a price advantage.
	if (candidate.Long.Venue == candidate.Short.Venue) != (current.Long.Venue == current.Short.Venue) {
		return candidate.Long.Venue == candidate.Short.Venue
	}
	return candidate.ID < current.ID
}

func appendOpportunityBlockers(blockers []string, long, short arbitrageOpportunityLeg) []string {
	if long.AccountID == "" || short.AccountID == "" {
		blockers = append(blockers, "account_unreachable")
	}
	if long.InstrumentID == "" || short.InstrumentID == "" {
		blockers = append(blockers, "instrument_identity_unresolved")
	}
	if long.Stale || short.Stale {
		blockers = append(blockers, "board_stale")
	}
	return blockers
}

func observationStale(now, observedAt time.Time, freshness time.Duration) bool {
	return observedAt.IsZero() || now.Sub(observedAt) > freshness
}

func routeProfileForInstrument(snapshot catalog.Snapshot, instrument catalog.Instrument) string {
	for _, venue := range snapshot.Venues {
		if venue.Venue != instrument.Venue || venue.Environment != instrument.Environment {
			continue
		}
		for _, profile := range venue.RouteProfiles {
			if profile.Product == instrument.Product && profile.SettlementAsset == instrument.Settlement {
				return profile.RouteProfileID
			}
		}
	}
	return ""
}

// canonicalBoardPerp upgrades one diagnostic board row into a strategy leg.
// The row owns volatile values; the public catalog owns identity and
// classification. Requiring both sources to agree here prevents an orphaned
// ticker from reaching history or execution merely because a venue still emits
// a quote for it.
func canonicalBoardPerp(snapshot catalog.Snapshot, venue marketdata.BoardVenue, row marketdata.BoardRow) (catalog.Instrument, bool) {
	instrument, ok := snapshot.Instrument(row.InstrumentID)
	if !ok || !instrument.Ready || instrument.Product != arbitrageProduct ||
		instrument.Venue != venue.Venue || instrument.Environment != venue.Environment ||
		instrument.BaseAsset == "" || instrument.QuoteAsset == "" {
		return catalog.Instrument{}, false
	}
	if row.BaseAsset != "" && row.BaseAsset != instrument.BaseAsset {
		return catalog.Instrument{}, false
	}
	if row.QuoteAsset != "" && row.QuoteAsset != instrument.QuoteAsset {
		return catalog.Instrument{}, false
	}
	if venue.Settlement != "" && instrument.Settlement != "" && venue.Settlement != instrument.Settlement {
		return catalog.Instrument{}, false
	}
	if profile := routeProfileForInstrument(snapshot, instrument); profile != "" && profile != venue.RouteProfileID {
		return catalog.Instrument{}, false
	}
	return instrument, true
}

// spotPerpAssetClass accepts an exact canonical spot/perpetual topology when
// neither venue publishes an economic classification. This does not label the
// asset as crypto: unknown stays unknown in the response. It only recognizes
// the structural cash-and-carry strategy. Any explicit non-crypto class still
// rejects the pair, as does a conflict between official classifications.
func spotPerpAssetClass(spot, perp string) (string, bool) {
	assetClass, compatible := opportunityAssetClass([]arbitrageOpportunityLeg{{AssetClass: spot}, {AssetClass: perp}})
	if !compatible || (assetClass != "unknown" && assetClass != "crypto") {
		return "unknown", false
	}
	return assetClass, true
}

func tradFiAssetClass(assetClass string) bool {
	switch assetClass {
	case "equity", "commodity", "index", "fx", "rates", "pre_ipo", "rwa_other":
		return true
	default:
		return false
	}
}

func arbitrageOpportunityID(strategy string, long, short arbitrageOpportunityLeg) string {
	return strings.Join([]string{strategy, long.RouteProfileID, long.InstrumentID, short.RouteProfileID, short.InstrumentID}, ":")
}

// opportunityAssetClass returns the one venue-published class shared by the
// legs. Unknown is absence of evidence, so it may coexist with one known class;
// two different known classes are evidence of a ticker collision and make the
// candidate unsafe to construct.
func opportunityAssetClass(legs []arbitrageOpportunityLeg) (string, bool) {
	class := "unknown"
	for _, leg := range legs {
		candidate := strings.TrimSpace(leg.AssetClass)
		if candidate == "" || candidate == "unknown" {
			continue
		}
		if class != "unknown" && class != candidate {
			return "unknown", false
		}
		class = candidate
	}
	return class, true
}

func strategyFamily(assetClass string) string {
	switch assetClass {
	case "crypto":
		return "crypto_continuous"
	case "equity", "commodity", "index", "fx", "rates", "pre_ipo", "rwa_other":
		return "tradfi_perpetual"
	default:
		return "unclassified"
	}
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
			if route.RequestedState != manualAccountActive ||
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
	request.Strategy = arbitrageStrategy(request.Strategy)
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
	if !knownArbitrageStrategy(arbitrageStrategy(request.Strategy)) {
		return orders.ErrNotFound
	}
	quantity, err := decimal.NewFromString(request.Quantity)
	if err != nil || !quantity.IsPositive() {
		return orders.ErrNotFound
	}
	if request.Long.AccountID == "" || request.Long.InstrumentID == "" || request.Short.AccountID == "" || request.Short.InstrumentID == "" {
		return orders.ErrNotFound
	}
	// Two legs on one account is not a hedge — it is the same position opened
	// twice, and the reservation netting scope would collide on it anyway.
	if request.Long.InstrumentID == request.Short.InstrumentID {
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
	strategy := arbitrageStrategy(request.Strategy)
	var authority arbitragePreviewAuthority
	authority.Strategy = strategy
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
		if !routed || (route.Product != "spot" && route.Product != arbitrageProduct) {
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
	if strategy != arbitrageStrategySpotPerpBasis && authority.Long.Venue == authority.Short.Venue && authority.Long.VenueEnvironment == authority.Short.VenueEnvironment {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return arbitragePreviewAuthority{}, catalog.Snapshot{}, false
	}
	// And different underlyings mean the pair is not delta-neutral at all — it
	// is two directional bets wearing a hedge's clothes.
	longInstrument, longFound := snapshot.Instrument(authority.Long.InstrumentID)
	shortInstrument, shortFound := snapshot.Instrument(authority.Short.InstrumentID)
	longBase, longQuote := longInstrument.BaseAsset, longInstrument.QuoteAsset
	shortBase, shortQuote := shortInstrument.BaseAsset, shortInstrument.QuoteAsset
	if !longFound || !shortFound || longBase != shortBase || longQuote != shortQuote {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return arbitragePreviewAuthority{}, catalog.Snapshot{}, false
	}
	if !validArbitrageShape(strategy, longInstrument, shortInstrument) {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return arbitragePreviewAuthority{}, catalog.Snapshot{}, false
	}
	return authority, snapshot, true
}

func arbitrageStrategy(value string) string {
	if strings.TrimSpace(value) == "" {
		return arbitrageStrategyFunding
	}
	return strings.TrimSpace(value)
}

func knownArbitrageStrategy(value string) bool {
	switch value {
	case arbitrageStrategyFunding, arbitrageStrategySpotPerpBasis, arbitrageStrategyPerpSpread:
		return true
	default:
		return false
	}
}

func validArbitrageShape(strategy string, long, short catalog.Instrument) bool {
	assetClass, compatible := opportunityAssetClass([]arbitrageOpportunityLeg{{AssetClass: long.AssetClass}, {AssetClass: short.AssetClass}})
	if !compatible {
		return false
	}
	switch strategy {
	case arbitrageStrategyFunding:
		return long.Product == "perp" && short.Product == "perp"
	case arbitrageStrategySpotPerpBasis:
		_, basisCompatible := spotPerpAssetClass(long.AssetClass, short.AssetClass)
		return long.Product == "spot" && short.Product == "perp" && basisCompatible
	case arbitrageStrategyPerpSpread:
		return long.Product == "perp" && short.Product == "perp" && tradFiAssetClass(assetClass)
	default:
		return false
	}
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
	if authority.Strategy != arbitrageStrategyFunding {
		return handler.priceQuotedHedge(r, previewID, request, authority, snapshot)
	}
	board, err := handler.trading.options.FundingBoard.Get(r.Context())
	if err != nil {
		return arbitragePreview{}, err
	}
	now := time.Now().UTC()
	if codec, ok := handler.trading.options.Arbitrage.(arbitragePreviewCodec); ok {
		now = codec.PreviewNow().UTC()
	}
	quotes := indexBoard(board, now)
	preview := arbitragePreview{
		PreviewID: previewID, Strategy: authority.Strategy, Request: request, Quantity: request.Quantity,
		Blockers: []string{}, Warnings: []string{},
	}
	base, quote, _ := arbitrageUnderlying(snapshot, authority.Long.InstrumentID)
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
			Product: side.authority.Product,
			Side:    arbitrage.OpenSide(side.leg), LimitPrice: side.authority.LimitPrice,
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
		daily, ok := dailyFundingRate(found.row.Rate, found.row.IntervalHours)
		if !ok {
			preview.Warnings = append(preview.Warnings, "funding_interval_unknown")
			continue
		}
		side.target.DailyRate = daily.String()
		dailies[side.leg] = daily
	}
	quantity, quantityErr := decimal.NewFromString(request.Quantity)
	prices, quoteErr := handler.freshHedgePrices(r.Context(), snapshot, authority, &preview)
	if quoteErr != nil {
		return arbitragePreview{}, quoteErr
	}
	longDaily, longOK := dailies[arbitrage.LegLong]
	shortDaily, shortOK := dailies[arbitrage.LegShort]
	if longOK && shortOK {
		spread := shortDaily.Sub(longDaily)
		preview.SpreadDaily = spread.String()
		preview.SpreadApr = spread.Mul(decimal.NewFromInt(365)).String()
		if spread.IsNegative() {
			preview.Warnings = append(preview.Warnings, "negative_spread")
		}
		if quantityErr == nil {
			average := prices[arbitrage.LegLong].Add(prices[arbitrage.LegShort]).Div(decimal.NewFromInt(2))
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

// priceQuotedHedge turns a bulk-discovered price strategy into a tradeable
// preview. Long is priced at the best ask and short at the best bid; using two
// marks here would report an edge the operator cannot actually cross.
func (handler *arbitrageProductHandler) priceQuotedHedge(r *http.Request, previewID string, request arbitragePreviewRequest, authority arbitragePreviewAuthority, snapshot catalog.Snapshot) (arbitragePreview, error) {
	preview := arbitragePreview{
		PreviewID: previewID, Strategy: authority.Strategy, Request: request, Quantity: request.Quantity,
		Blockers: []string{}, Warnings: []string{},
	}
	prices, err := handler.freshHedgePrices(r.Context(), snapshot, authority, &preview)
	if err != nil {
		return arbitragePreview{}, err
	}
	longPrice, shortPrice := prices[arbitrage.LegLong], prices[arbitrage.LegShort]
	edge := shortPrice.Sub(longPrice)
	switch authority.Strategy {
	case arbitrageStrategySpotPerpBasis:
		preview.BasisRate = edge.Div(longPrice).String()
		if !edge.IsPositive() {
			preview.Warnings = append(preview.Warnings, "negative_basis")
		}
	case arbitrageStrategyPerpSpread:
		mid := shortPrice.Add(longPrice).Div(decimal.NewFromInt(2))
		preview.PriceSpread = edge.Div(mid).String()
		if !edge.IsPositive() {
			preview.Warnings = append(preview.Warnings, "negative_spread")
		}
	}
	quantity, err := decimal.NewFromString(request.Quantity)
	if err == nil {
		average := longPrice.Add(shortPrice).Div(decimal.NewFromInt(2))
		preview.Notional = quantity.Mul(average).Round(8).String()
	}
	preview.Warnings = append(preview.Warnings, "fees_excluded")
	return preview, nil
}

func (handler *arbitrageProductHandler) freshHedgePrices(ctx context.Context, snapshot catalog.Snapshot, authority arbitragePreviewAuthority, preview *arbitragePreview) (map[arbitrage.Leg]decimal.Decimal, error) {
	if handler.trading.options.FreshMarketData == nil {
		return nil, marketdata.ErrRateLimited
	}
	now := time.Now().UTC()
	if codec, ok := handler.trading.options.Arbitrage.(arbitragePreviewCodec); ok {
		now = codec.PreviewNow().UTC()
	}
	prices := map[arbitrage.Leg]decimal.Decimal{}
	for _, side := range []struct {
		leg       arbitrage.Leg
		authority arbitrageLegAuthority
		target    *arbitragePreviewLeg
	}{
		{arbitrage.LegLong, authority.Long, &preview.Long},
		{arbitrage.LegShort, authority.Short, &preview.Short},
	} {
		instrument, found := snapshot.Instrument(side.authority.InstrumentID)
		if !found {
			return nil, orders.ErrNotFound
		}
		fact, err := handler.trading.options.FreshMarketData.Get(ctx, marketdata.Query{
			Venue: instrument.Venue, Environment: instrument.Environment,
			Product: instrument.Product, InstrumentID: instrument.InstrumentID,
		})
		if err != nil {
			return nil, err
		}
		if fact.ObservedAt.IsZero() || now.Sub(fact.ObservedAt.UTC()) > 2*time.Second || fact.ObservedAt.UTC().After(now.Add(time.Second)) {
			return nil, orders.ErrNotFound
		}
		priceText := fact.BestAsk
		if side.leg == arbitrage.LegShort {
			priceText = fact.BestBid
		}
		price, err := decimal.NewFromString(priceText)
		if err != nil || !price.IsPositive() {
			return nil, orders.ErrNotFound
		}
		prices[side.leg] = price
		target := side.target
		target.AccountID, target.InstrumentID = side.authority.AccountID, side.authority.InstrumentID
		target.Venue, target.Environment, target.Product = side.authority.Venue, side.authority.VenueEnvironment, side.authority.Product
		target.Symbol, target.Side, target.LimitPrice = instrument.Symbol, arbitrage.OpenSide(side.leg), side.authority.LimitPrice
		target.BestBid, target.BestAsk, target.ExecutionPrice, target.MarkPrice = fact.BestBid, fact.BestAsk, priceText, fact.MarkPrice
	}
	return prices, nil
}

type quoteKey struct{ venue, environment, base, quote string }

type boardQuote struct {
	row   marketdata.BoardRow
	stale bool
}

func indexBoard(board marketdata.BoardSnapshot, now time.Time) map[quoteKey]boardQuote {
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
			quotes[key] = boardQuote{row: row, stale: observationStale(now, row.FundingObservedAt, time.Minute)}
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
		sealed.Strategy != arbitrageStrategy(sealed.Request.Strategy) ||
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
		current.Strategy != sealed.Strategy || current.Long != sealed.Long || current.Short != sealed.Short {
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
		(leg.Product == "spot" || leg.Product == arbitrageProduct) && leg.RouteID != "" && leg.Fence.AccountID == leg.AccountID &&
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
	if order.Closing {
		side = arbitrage.ReduceSide(order.Leg)
	}
	payload, err := arbitrage.EncodeIntentWithClose(
		order.Leg, order.Authority.InstrumentID, side, order.Quantity, order.OrderType,
		order.LimitPrice, order.TimeInForce, order.Authority.RouteID,
		order.Counter.AccountID, order.Counter.InstrumentID, order.ReduceOnly, order.Closing,
		order.Authority.Fence.RuntimeID,
	)
	if err != nil {
		return orders.Command{}, false, err
	}
	kind := reservations.KindOpen
	if order.Closing {
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
		if viewErr != nil || view.Closing {
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
			Quantity: quantity.Abs().String(), OrderType: "market", Closing: true,
			ReduceOnly: authority.Product == arbitrageProduct,
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
	if !routed || (route.Product != "spot" && route.Product != arbitrageProduct) {
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
		if command.LineageID != request.LineageID || err != nil || view.OrderType != "market" || !view.Closing {
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
