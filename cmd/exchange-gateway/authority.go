package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/fundingboard"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

type gatewayAuthority struct {
	manager *account.Manager
	reads   authorityReadSource
}

type validationSnapshotAuthority struct{}

func (validationSnapshotAuthority) Execute(_ context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	if request.Operation == httpapi.AuthorityRuntimeReadiness {
		return httpapi.AuthorityResponse{Body: httpapi.RuntimeReadiness{Ready: false}}, nil
	}
	switch request.Operation {
	case httpapi.AuthorityCatalogVenues:
		return httpapi.AuthorityResponse{Body: profileAuthorityVenues(nil)}, nil
	case httpapi.AuthorityCatalogInstruments:
		revision := sha256.Sum256([]byte("[]"))
		return httpapi.AuthorityResponse{Body: authorityCatalogInstruments{Revision: fmt.Sprintf("gateway-%x", revision[:16]), ObservedAt: time.Now().UTC(), Instruments: []authorityInstrument{}}}, nil
	}
	return httpapi.AuthorityResponse{Body: []any{}}, nil
}

type authorityVenue = wire.CatalogVenue

type authorityInstrument = wire.CatalogInstrument

type authorityCatalogInstruments = wire.CatalogInstruments

type authorityRuntimeStatus struct {
	Accounts []authorityPublicRuntimeAccount `json:"accounts"`
}

type authorityPublicRuntimeAccount struct {
	AccountID        domain.AccountID `json:"accountId"`
	Venue            string           `json:"venue"`
	VenueEnvironment string           `json:"venueEnvironment"`
	Ready            bool             `json:"ready"`
	Reason           string           `json:"reason,omitempty"`
	ObservedAt       time.Time        `json:"observedAt"`
}

type authorityTradeRules = wire.TradeRules

type authorityMarketData struct {
	Venue             string                  `json:"venue"`
	Environment       string                  `json:"environment"`
	Product           string                  `json:"product"`
	InstrumentID      string                  `json:"instrumentId"`
	BestBid           string                  `json:"bestBid"`
	BestBidSize       string                  `json:"bestBidSize"`
	BestAsk           string                  `json:"bestAsk"`
	BestAskSize       string                  `json:"bestAskSize"`
	Bids              []authorityLevel        `json:"bids"`
	Asks              []authorityLevel        `json:"asks"`
	MarkPrice         string                  `json:"markPrice,omitempty"`
	FundingRate       string                  `json:"fundingRate,omitempty"`
	FundingObservedAt time.Time               `json:"fundingObservedAt,omitzero"`
	FundingTime       time.Time               `json:"fundingTime,omitzero"`
	NextFundingTime   time.Time               `json:"nextFundingTime,omitzero"`
	FundingHistory    []authorityFundingPoint `json:"fundingHistory,omitempty"`
	OpenInterest      string                  `json:"openInterest,omitempty"`
	SourceSequence    string                  `json:"sourceSequence,omitempty"`
	ObservedAt        time.Time               `json:"observedAt"`
}

type authorityLevel struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
}

// authorityCandlesQuery is a struct rather than the positional arguments
// MarketData takes because it carries six fields, five of them strings, and
// four of those are venue/environment/product/instrumentId in an order nothing
// in the type system enforces.
type authorityCandlesQuery struct {
	Venue        string
	Environment  string
	Product      string
	InstrumentID string
	Interval     string
	Limit        int
}

type authorityCandles struct {
	Venue        string            `json:"venue"`
	Environment  string            `json:"environment"`
	Product      string            `json:"product"`
	InstrumentID string            `json:"instrumentId"`
	Interval     string            `json:"interval"`
	Candles      []authorityCandle `json:"candles"`
	ObservedAt   time.Time         `json:"observedAt"`
}

type authorityCandle struct {
	OpenTime  time.Time `json:"openTime"`
	CloseTime time.Time `json:"closeTime"`
	Open      string    `json:"open"`
	High      string    `json:"high"`
	Low       string    `json:"low"`
	Close     string    `json:"close"`
	Volume    string    `json:"volume"`
	Complete  bool      `json:"complete"`
}

type authorityFundingPoint struct {
	Rate        string    `json:"rate"`
	ObservedAt  time.Time `json:"observedAt,omitzero"`
	FundingTime time.Time `json:"fundingTime"`
}

// authorityFundingBoard is the whole cross-venue rate table, one column per
// funding-capable route profile.
//
// It is a separate projection from authorityMarketData rather than a list of
// them because it is a cross-venue bulk read, while MarketData is one selected
// instrument. Both are credential-free public data.
type authorityFundingBoard struct {
	ObservedAt time.Time               `json:"observedAt"`
	Venues     []authorityFundingVenue `json:"venues"`
}

// authorityFundingVenue is one column.
//
// Stale and Unavailable are both on the wire because they are different answers
// to "may I compare this number against the next column": stale means these
// rates are the venue's last known ones and older than the rest of the table,
// unavailable means there is nothing recent enough to put beside another venue
// at all. An unavailable column carries no rows by construction upstream.
//
// ObservedAt carries omitzero, not omitempty: omitempty does not consult
// time.Time.IsZero, so an unavailable column — which has no observation time by
// definition — would ship "0001-01-01T00:00:00Z" and render as an age of two
// thousand years rather than as no reading at all.
type authorityFundingVenue struct {
	Venue          string                `json:"venue"`
	Environment    string                `json:"environment"`
	Product        string                `json:"product"`
	Settlement     string                `json:"settlement,omitempty"`
	RouteProfileID string                `json:"routeProfileId"`
	ObservedAt     time.Time             `json:"observedAt,omitzero"`
	Stale          bool                  `json:"stale"`
	Unavailable    bool                  `json:"unavailable"`
	Status         string                `json:"status"`
	LastSuccessAt  time.Time             `json:"lastSuccessAt,omitzero"`
	Reconnects     uint64                `json:"reconnects,omitempty"`
	Rows           []authorityFundingRow `json:"rows"`
}

// authorityFundingRow is one instrument's rate on one venue.
//
// Every optional cell is a plain string carrying omitempty rather than
// exchange.OptionalDecimal, which serialises as {"value":…,"valid":…} and would
// put "0" on the wire for a venue that published nothing. Seven of the ten
// venues leave open interest empty in their bulk call, so an absent cell has to
// stay absent rather than become a number the venue never quoted.
//
// NextFundingTime is the same argument in the time domain, and needs omitzero
// to make it: omitempty ignores time.Time entirely, so a zero time reaches the
// browser as "0001-01-01T00:00:00Z" and renders as a settlement countdown that
// has already elapsed. Hyperliquid and lighter publish a bare rate with no
// settlement clock, and binance, okx, and nado leave it out whenever the venue
// itself omits it, so the zero is reachable on a third of the table.
type authorityFundingRow struct {
	InstrumentID              string    `json:"instrumentId,omitempty"`
	Symbol                    string    `json:"symbol"`
	BaseAsset                 string    `json:"baseAsset,omitempty"`
	QuoteAsset                string    `json:"quoteAsset,omitempty"`
	MarketFamily              string    `json:"marketFamily"`
	AssetClass                string    `json:"assetClass"`
	ClassificationSource      string    `json:"classificationSource,omitempty"`
	RawAssetType              string    `json:"rawAssetType,omitempty"`
	RawCategories             []string  `json:"rawCategories,omitempty"`
	Rate                      string    `json:"rate,omitempty"`
	IntervalHours             int64     `json:"intervalHours,omitempty"`
	NextFundingTime           time.Time `json:"nextFundingTime,omitzero"`
	MarkPrice                 string    `json:"markPrice,omitempty"`
	IndexPrice                string    `json:"indexPrice,omitempty"`
	OpenInterest              string    `json:"openInterest,omitempty"`
	OpenInterestNotional      string    `json:"openInterestNotional,omitempty"`
	OpenInterestNotionalAsset string    `json:"openInterestNotionalAsset,omitempty"`
	FundingObservedAt         time.Time `json:"fundingObservedAt,omitzero"`
	MarkObservedAt            time.Time `json:"markObservedAt,omitzero"`
	IndexObservedAt           time.Time `json:"indexObservedAt,omitzero"`
	PriceObservedAt           time.Time `json:"priceObservedAt,omitzero"`
	OpenInterestObservedAt    time.Time `json:"openInterestObservedAt,omitzero"`
	StaleFields               []string  `json:"staleFields,omitempty"`
	MissingFields             []string  `json:"missingFields,omitempty"`
}

// authorityFundingHistoryQuery is a struct for the same reason
// authorityCandlesQuery is: three adjacent strings that the type system cannot
// tell apart, in an order nothing enforces.
//
// The window is a pair of instants rather than a preset name because the
// gateway is the capability layer. Which two windows the product offers is a
// product decision that belongs to the service above; what the gateway knows,
// and the service cannot, is how far back a single venue call actually reaches.
type authorityFundingHistoryQuery struct {
	InstrumentID string
	Since        time.Time
	Until        time.Time
}

// authorityFundingSeries is one instrument's settled funding on one venue.
//
// It echoes the requested identity back for the same reason authorityCandles
// does: two of these are drawn on one chart as the legs of a spread, and a
// response that arrived under the wrong label would render as a real number
// against the wrong venue rather than as a gap.
type authorityFundingSeries struct {
	InstrumentID string                        `json:"instrumentId"`
	Venue        string                        `json:"venue"`
	Since        time.Time                     `json:"since"`
	Until        time.Time                     `json:"until"`
	ObservedAt   time.Time                     `json:"observedAt"`
	Points       []authorityFundingSeriesPoint `json:"points"`
}

// authorityFundingSeriesPoint is one settlement.
//
// Separate from authorityFundingPoint, which carries the venue's own
// observedAt for a single live rate embedded in a market-data fact. A settled
// point has no such stamp — it has the interval it accrued over, which is what
// makes two venues on different clocks comparable at all.
type authorityFundingSeriesPoint struct {
	FundingTime   time.Time `json:"fundingTime"`
	Rate          string    `json:"rate"`
	IntervalHours int64     `json:"intervalHours,omitempty"`
}

func authorityPublicRuntimes(runtimes []account.RuntimeAccount) []authorityPublicRuntimeAccount {
	result := make([]authorityPublicRuntimeAccount, 0, len(runtimes))
	for _, runtime := range runtimes {
		result = append(result, authorityPublicRuntimeAccount{
			AccountID: runtime.AccountID, Venue: runtime.Venue, VenueEnvironment: runtime.VenueEnvironment,
			Ready: runtime.Ready, Reason: runtime.Reason,
			ObservedAt: runtime.ObservedAt,
		})
	}
	return result
}

const (
	authorityDepthLimit = 20
	// Matches the public plane's maxListLimit. It is a screenful of 1m bars with
	// room to scroll, and every supported venue serves 500 in one call.
	authorityCandleLimitMax = 500
)

type authorityReadSource interface {
	Venues() []authorityVenue
	Instruments(context.Context) (authorityCatalogInstruments, error)
	MarketData(context.Context, string, string, string, string) (authorityMarketData, error)
	Candles(context.Context, authorityCandlesQuery) (authorityCandles, error)
	FundingBoard(context.Context) (authorityFundingBoard, error)
	FundingHistory(context.Context, authorityFundingHistoryQuery) (authorityFundingSeries, error)
}

type authorityAccountSource interface {
	Accounts() []account.Resource
	Instruments(domain.AccountID) (catalog.Snapshot, error)
	Resolve(context.Context, domain.AccountID, domain.InstrumentID) (exchange.Instrument, error)
	Spot(domain.AccountID) (exchange.SpotClient, error)
	Perp(domain.AccountID) (exchange.PerpClient, error)
	// The route-scoped pair is what every reader below actually wants. These
	// readers walk Accounts(), which emits one Resource per route, so they have
	// already chosen a route by the time they need a client; asking for one by
	// bare account id throws that choice away and cannot be answered at all for
	// an account running two routes of the same product.
	SpotRoute(domain.AccountID, string) (exchange.SpotClient, error)
	PerpRoute(domain.AccountID, string) (exchange.PerpClient, error)
}

type managerAuthorityReads struct {
	manager  authorityAccountSource
	profiles *routeprofile.Registry
	// markets owns the credential-free catalog, order-book and candle plane.
	// It is deliberately independent of manager: public facts exist before an
	// account is configured and remain readable after all accounts are deleted.
	markets authorityPublicMarketSource
	// board serves the bulk funding matrix from its own public endpoint loaders.
	// It stays separate from markets because it refreshes every venue as one
	// comparable snapshot rather than resolving a selected instrument.
	board fundingboard.Board
	now   func() time.Time
}

type authorityPublicMarketSource interface {
	Venues() []string
	Snapshot(context.Context) (publicmarket.Snapshot, error)
	MarketData(context.Context, publicmarket.Query, int) (publicmarket.Fact, error)
	Candles(context.Context, publicmarket.Query, string, int) (publicmarket.CandleResult, error)
}

// clock reports the read's notion of now, in UTC. Tests inject now to make
// observation times deterministic; production leaves it nil.
func (reads managerAuthorityReads) clock() time.Time {
	if reads.now == nil {
		return time.Now().UTC()
	}
	return reads.now().UTC()
}

// authorityOption configures immutable read dependencies before requests begin.
type authorityOption func(*managerAuthorityReads)

// withFundingBoard supplies the credential-free source behind the funding
// matrix. A nil source leaves the board answering ErrBoardUnavailable, which is
// what a test-injected account manager should see — an honest gap rather than a
// board that silently has no venues.
func withFundingBoard(board fundingboard.Board) authorityOption {
	return func(reads *managerAuthorityReads) { reads.board = board }
}

func withPublicMarkets(markets authorityPublicMarketSource) authorityOption {
	return func(reads *managerAuthorityReads) { reads.markets = markets }
}

func newGatewayAuthority(ctx context.Context, manager *account.Manager, options ...authorityOption) (*gatewayAuthority, error) {
	if ctx == nil || manager == nil {
		return nil, errors.New("gateway authority dependencies are incomplete")
	}
	reads := managerAuthorityReads{manager: manager, profiles: routeprofile.DefaultRegistry(), now: time.Now}
	for _, option := range options {
		if option != nil {
			option(&reads)
		}
	}
	authority := &gatewayAuthority{
		manager: manager,
		reads:   reads,
	}
	return authority, nil
}

func (authority *gatewayAuthority) Execute(ctx context.Context, request httpapi.AuthorityRequest) (httpapi.AuthorityResponse, error) {
	if ctx == nil {
		return httpapi.AuthorityResponse{}, authorityError(errors.New("context is required"))
	}
	var response httpapi.AuthorityResponse
	var err error
	switch request.Operation {
	case httpapi.AuthorityListRuntimeAccounts:
		response.Body = authorityPublicRuntimes(authority.manager.Runtimes())
	case httpapi.AuthorityGetRuntimeAccount:
		response.Body, err = authority.connectionView(request.AccountID)
	case httpapi.AuthorityRuntimeStatus:
		response.Body = authorityRuntimeStatus{Accounts: authorityPublicRuntimes(authority.manager.Runtimes())}
	case httpapi.AuthorityRuntimeReadiness:
		response.Body = httpapi.RuntimeReadiness{Ready: authority.manager.RuntimeReady()}
	case httpapi.AuthorityCatalogVenues:
		response.Body = authority.reads.Venues()
	case httpapi.AuthorityCatalogInstruments:
		response.Body, err = authority.reads.Instruments(ctx)
	case httpapi.AuthorityMarketData:
		venue, environment, product, instrumentID := request.Query.Get("venue"), request.Query.Get("environment"), request.Query.Get("product"), request.Query.Get("instrumentId")
		if strings.TrimSpace(venue) == "" || strings.TrimSpace(environment) == "" || strings.TrimSpace(product) == "" || strings.TrimSpace(instrumentID) == "" {
			err = errors.New("market-data composite identity is required")
			break
		}
		var fact authorityMarketData
		fact, err = authority.reads.MarketData(ctx, venue, environment, product, instrumentID)
		if err == nil && (fact.Venue != venue || fact.Environment != environment || fact.Product != product || fact.InstrumentID != instrumentID) {
			err = errors.New("market-data composite identity mismatch")
		} else {
			response.Body = fact
		}
	case httpapi.AuthorityCandles:
		query := authorityCandlesQuery{
			Venue: request.Query.Get("venue"), Environment: request.Query.Get("environment"),
			Product: request.Query.Get("product"), InstrumentID: request.Query.Get("instrumentId"),
			Interval: request.Query.Get("interval"),
		}
		if strings.TrimSpace(query.Venue) == "" || strings.TrimSpace(query.Environment) == "" ||
			strings.TrimSpace(query.Product) == "" || strings.TrimSpace(query.InstrumentID) == "" {
			err = errors.New("candles composite identity is required")
			break
		}
		if query.Interval, err = httpapi.PortableCandleInterval(strings.TrimSpace(query.Interval)); err != nil {
			break
		}
		if query.Limit, err = authorityCandleLimit(request.Query.Get("limit")); err != nil {
			break
		}
		var bars authorityCandles
		bars, err = authority.reads.Candles(ctx, query)
		if err == nil && (bars.Venue != query.Venue || bars.Environment != query.Environment ||
			bars.Product != query.Product || bars.InstrumentID != query.InstrumentID || bars.Interval != query.Interval) {
			err = errors.New("candles composite identity mismatch")
		} else {
			response.Body = bars
		}
	case httpapi.AuthorityFundingBoard:
		// No query to validate and no identity to echo back: the board is the
		// whole table this gateway can reach, so there is nothing a caller could
		// ask for that would make one column right and another wrong.
		if len(request.Query) != 0 {
			err = errors.New("funding board query is not allowed")
		} else {
			response.Body, err = authority.reads.FundingBoard(ctx)
		}
	case httpapi.AuthorityFundingHistory:
		if !authorityQueryExactly(request.Query, "instrumentId", "since", "until") {
			err = errors.New("funding history query is invalid")
			break
		}
		query := authorityFundingHistoryQuery{
			InstrumentID: strings.TrimSpace(request.Query.Get("instrumentId")),
		}
		if query.InstrumentID == "" {
			err = errors.New("funding history instrumentId is required")
			break
		}
		if query.Since, err = authorityHistoryBound(request.Query.Get("since")); err != nil {
			break
		}
		if query.Until, err = authorityHistoryBound(request.Query.Get("until")); err != nil {
			break
		}
		var series authorityFundingSeries
		series, err = authority.reads.FundingHistory(ctx, query)
		if err == nil && series.InstrumentID != query.InstrumentID {
			err = errors.New("funding history instrument identity mismatch")
		} else {
			response.Body = series
		}
	case httpapi.AuthorityRuntimeOpenOrders, httpapi.AuthorityRuntimePositions,
		httpapi.AuthorityRuntimeUncertain, httpapi.AuthorityRuntimeEvents,
		httpapi.AuthorityPrivateEvents:
		response.Body = []any{}
	default:
		err = errors.New("unsupported authority operation")
	}
	if err != nil {
		// This is the one place every internal-plane failure passes through, and
		// the last place its cause still exists: authorityError maps the error to
		// a status and a stable code, and the HTTP layer above renders anything
		// unmapped as a bare INTERNAL_ERROR. Without this line a caller sees
		// "request denied" and nothing — not which operation, not which account,
		// not why — which is exactly what made the account-promotion and
		// market-data failures each take a full diagnostic session to place.
		//
		// The cause is safe to log here because it is ours: these errors are
		// built from store sentinels, manager state, and venue responses, none of
		// which carry credential material. The request is not logged, only its
		// operation and account, because request bodies on this plane do.
		slog.Error("gateway internal authority operation failed",
			"operation", request.Operation, "accountId", request.AccountID,
			"cause", err)
		return httpapi.AuthorityResponse{}, authorityError(err)
	}
	return response, nil
}

func (reads managerAuthorityReads) Venues() []authorityVenue {
	if reads.markets != nil {
		venues := profileAuthorityVenues(reads.profiles)
		ready := make(map[string]struct{})
		for _, venue := range reads.markets.Venues() {
			ready[venue] = struct{}{}
		}
		for index := range venues {
			_, venues[index].Ready = ready[venues[index].Venue]
		}
		return venues
	}
	resources := reads.manager.Accounts()
	venues := profileAuthorityVenues(reads.profiles)
	indexes := make(map[string]int, len(venues))
	for index, venue := range venues {
		indexes[venue.Venue+"\x00"+venue.Environment] = index
	}
	for _, resource := range resources {
		key := string(resource.Venue) + "\x00" + string(resource.Environment)
		if index, ok := indexes[key]; ok {
			venues[index].Ready = venues[index].Ready || resource.ReadReady
		}
	}
	return venues
}

// profileAuthorityVenues enumerates the venues the registry knows, one entry
// each.
//
// A venue used to appear once per environment it stood endpoints in, so the
// dedup key carried the environment and the ordering broke ties on it. Only
// live endpoints exist now, which makes a venue name the whole identity. The
// environment stays on the emitted record because the readiness join below is
// keyed on it and the resources it matches against still carry one.
func profileAuthorityVenues(registry *routeprofile.Registry) []authorityVenue {
	if registry == nil {
		registry = routeprofile.DefaultRegistry()
	}
	profiles := registry.Profiles()
	venues := make([]authorityVenue, 0, len(profiles))
	indexes := make(map[string]int, len(profiles))
	for _, profile := range profiles {
		if _, exists := indexes[profile.Venue]; exists {
			continue
		}
		indexes[profile.Venue] = len(venues)
		venues = append(venues, authorityVenue{Venue: profile.Venue, Environment: string(exchange.EnvironmentLive)})
	}
	sort.Slice(venues, func(i, j int) bool { return venues[i].Venue < venues[j].Venue })
	return venues
}

func (reads managerAuthorityReads) Instruments(ctx context.Context) (authorityCatalogInstruments, error) {
	if reads.markets != nil {
		snapshot, err := reads.markets.Snapshot(ctx)
		if err != nil {
			return authorityCatalogInstruments{}, fmt.Errorf("read public market catalog: %w", err)
		}
		instruments := make([]authorityInstrument, 0, len(snapshot.Instruments))
		for _, row := range snapshot.Instruments {
			instrument := row.Catalog
			minNotional := ""
			if instrument.Rules.MinNotional.Valid {
				minNotional = instrument.Rules.MinNotional.Value.String()
			}
			instruments = append(instruments, authorityInstrument{
				InstrumentID: string(instrument.ID), Venue: string(instrument.Venue), Environment: string(instrument.Environment), Product: string(instrument.Product),
				Settlement: instrument.Settlement, Symbol: instrument.Symbol, BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset,
				MarketFamily: string(instrument.MarketFamily), AssetClass: string(instrument.AssetClass), ClassificationSource: instrument.ClassificationSource,
				RawAssetType: instrument.RawAssetType, RawCategories: append([]string(nil), instrument.RawCategories...),
				Rules: authorityTradeRules{PriceIncrement: instrument.Rules.PriceIncrement.String(), QuantityIncrement: instrument.Rules.QuantityIncrement.String(), MinQuantity: instrument.Rules.MinQuantity.String(), MinNotional: minNotional},
				Ready: row.Ready,
			})
		}
		return authorityCatalogInstruments{Revision: snapshot.Revision, ObservedAt: snapshot.ObservedAt.UTC(), Instruments: instruments}, nil
	}
	resources := reads.manager.Accounts()
	instruments := make([]authorityInstrument, 0)
	indexes := make(map[string]int)
	contributors := make([]string, 0, len(resources))
	var observedAt time.Time
	for _, resource := range resources {
		snapshot, err := reads.manager.Instruments(resource.AccountID)
		if err != nil {
			return authorityCatalogInstruments{}, fmt.Errorf("read configured account catalog: %w", err)
		}
		if strings.TrimSpace(snapshot.Revision) == "" || snapshot.LoadedAt.IsZero() {
			return authorityCatalogInstruments{}, errors.New("configured account catalog authority is incomplete")
		}
		contributors = append(contributors, strings.Join([]string{string(resource.Venue), string(resource.Environment), string(resource.Product), resource.Settlement, snapshot.Revision}, "\x00"))
		if observedAt.IsZero() || snapshot.LoadedAt.Before(observedAt) {
			observedAt = snapshot.LoadedAt.UTC()
		}
		for _, instrument := range snapshot.Instruments {
			key := strings.Join([]string{string(instrument.Venue), string(instrument.Environment), string(instrument.Product), string(instrument.ID)}, "\x00")
			if index, ok := indexes[key]; ok {
				instruments[index].Ready = instruments[index].Ready || resource.ReadReady
				continue
			}
			minNotional := ""
			if instrument.Rules.MinNotional.Valid {
				minNotional = instrument.Rules.MinNotional.Value.String()
			}
			indexes[key] = len(instruments)
			instruments = append(instruments, authorityInstrument{
				InstrumentID: string(instrument.ID), Venue: string(instrument.Venue), Environment: string(instrument.Environment), Product: string(instrument.Product),
				Settlement: instrument.Settlement, Symbol: instrument.Symbol, BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset,
				MarketFamily: string(instrument.MarketFamily), AssetClass: string(instrument.AssetClass), ClassificationSource: instrument.ClassificationSource,
				RawAssetType: instrument.RawAssetType, RawCategories: append([]string(nil), instrument.RawCategories...),
				Rules: authorityTradeRules{PriceIncrement: instrument.Rules.PriceIncrement.String(), QuantityIncrement: instrument.Rules.QuantityIncrement.String(), MinQuantity: instrument.Rules.MinQuantity.String(), MinNotional: minNotional},
				Ready: resource.ReadReady,
			})
		}
	}
	if observedAt.IsZero() {
		observedAt = reads.clock()
	}
	sort.Slice(instruments, func(i, j int) bool {
		if instruments[i].Venue == instruments[j].Venue {
			if instruments[i].Environment == instruments[j].Environment {
				if instruments[i].Product == instruments[j].Product {
					return instruments[i].InstrumentID < instruments[j].InstrumentID
				}
				return instruments[i].Product < instruments[j].Product
			}
			return instruments[i].Environment < instruments[j].Environment
		}
		return instruments[i].Venue < instruments[j].Venue
	})
	sort.Strings(contributors)
	fingerprint, err := json.Marshal(contributors)
	if err != nil {
		return authorityCatalogInstruments{}, errors.New("catalog revision aggregation failed")
	}
	revision := sha256.Sum256(fingerprint)
	return authorityCatalogInstruments{Revision: fmt.Sprintf("gateway-%x", revision[:16]), ObservedAt: observedAt, Instruments: instruments}, nil
}

// FundingBoard serves the cross-venue funding matrix from public endpoints.
//
// It is the one read on this type that never touches reads.manager: the board
// is what an operator looks at before configuring anything, so binding it to a
// ReadReady route would make a fresh install render an empty market.
//
// A nil source is ErrBoardUnavailable rather than an empty board. The two are
// indistinguishable on the wire otherwise, and "every venue is down" is a
// conclusion an operator would act on.
func (reads managerAuthorityReads) FundingBoard(ctx context.Context) (authorityFundingBoard, error) {
	if reads.board == nil {
		return authorityFundingBoard{}, fundingboard.ErrBoardUnavailable
	}
	snapshot, err := reads.board.Snapshot(ctx)
	if err != nil {
		return authorityFundingBoard{}, err
	}
	var instruments publicmarket.Snapshot
	if reads.markets != nil {
		// The rate surface and instrument directory are independently public.
		// A partial directory must not hide an otherwise useful board, so rows
		// that cannot be joined stay visible with no InstrumentID and are
		// explicitly non-executable above this boundary.
		instruments, _ = reads.markets.Snapshot(ctx)
	}
	board := authorityFundingBoard{
		ObservedAt: snapshot.ObservedAt,
		Venues:     make([]authorityFundingVenue, 0, len(snapshot.Venues)),
	}
	for _, venue := range snapshot.Venues {
		column := authorityFundingVenue{
			Venue: venue.Venue, Environment: venue.Environment, Product: venue.Product,
			Settlement: venue.Settlement, RouteProfileID: venue.RouteProfileID,
			ObservedAt: venue.ObservedAt, Stale: venue.Stale, Unavailable: venue.Unavailable,
			Status: string(venue.Status), LastSuccessAt: venue.LastSuccessAt, Reconnects: venue.Reconnects,
			Rows: make([]authorityFundingRow, 0, len(venue.Entries)),
		}
		for _, entry := range venue.Entries {
			row := authorityFundingRow{
				Symbol:                    entry.Symbol,
				BaseAsset:                 entry.BaseAsset,
				QuoteAsset:                entry.QuoteAsset,
				MarketFamily:              string(exchange.MarketFamilyVenuePerpetual),
				AssetClass:                string(exchange.AssetClassUnknown),
				NextFundingTime:           entry.NextFundingTime,
				OpenInterestNotionalAsset: entry.OpenInterestNotionalAsset,
				FundingObservedAt:         entry.FundingObservedAt, MarkObservedAt: entry.MarkObservedAt,
				IndexObservedAt: entry.IndexObservedAt, PriceObservedAt: entry.PriceObservedAt,
				OpenInterestObservedAt: entry.OpenInterestObservedAt,
				StaleFields:            append([]string(nil), entry.StaleFields...), MissingFields: append([]string(nil), entry.MissingFields...),
			}
			if entry.RateValid {
				row.Rate = entry.Rate.String()
			}
			if instrument, ok := instruments.MatchFundingEntry(venue.RouteProfileID, entry); ok {
				row.InstrumentID = string(instrument.Catalog.ID)
				row.Symbol = instrument.Catalog.Symbol
				row.BaseAsset = instrument.Catalog.BaseAsset
				row.QuoteAsset = instrument.Catalog.QuoteAsset
				if instrument.Catalog.MarketFamily != "" {
					row.MarketFamily = string(instrument.Catalog.MarketFamily)
				}
				if instrument.Catalog.AssetClass != "" {
					row.AssetClass = string(instrument.Catalog.AssetClass)
				}
				row.ClassificationSource = instrument.Catalog.ClassificationSource
				row.RawAssetType = instrument.Catalog.RawAssetType
				row.RawCategories = append([]string(nil), instrument.Catalog.RawCategories...)
			}
			if entry.IntervalHours.Valid {
				row.IntervalHours = entry.IntervalHours.Value
			}
			if entry.MarkPrice.Valid {
				row.MarkPrice = entry.MarkPrice.Value.String()
			}
			if entry.IndexPrice.Valid {
				row.IndexPrice = entry.IndexPrice.Value.String()
			}
			if entry.OpenInterest.Valid {
				row.OpenInterest = entry.OpenInterest.Value.String()
			}
			if entry.OpenInterestNotional.Valid {
				row.OpenInterestNotional = entry.OpenInterestNotional.Value.String()
			}
			column.Rows = append(column.Rows, row)
		}
		board.Venues = append(board.Venues, column)
	}
	return board, nil
}

// FundingHistory serves one instrument's settled series from the same public
// source the board is served from.
//
// It reads the same credential-free source as the board, so a chart draws on a
// gateway with no accounts at all — the state an operator comparing venues
// before funding one is actually in.
func (reads managerAuthorityReads) FundingHistory(ctx context.Context, query authorityFundingHistoryQuery) (authorityFundingSeries, error) {
	if reads.board == nil || reads.markets == nil {
		return authorityFundingSeries{}, fundingboard.ErrBoardUnavailable
	}
	publicSnapshot, err := reads.markets.Snapshot(ctx)
	if err != nil {
		return authorityFundingSeries{}, err
	}
	instrument, ok := publicSnapshot.Find(query.InstrumentID)
	if !ok || instrument.Catalog.Product != exchange.ProductPerp {
		return authorityFundingSeries{}, fundingboard.ErrHistoryUnknownInstrument
	}
	snapshot, err := reads.board.History(ctx, fundingboard.HistoryQuery{
		RouteProfileID: instrument.RouteProfileID,
		NativeSymbol:   instrument.Canonical.Identity.NativeSymbol,
		NativeID:       instrument.Canonical.Identity.NativeID,
		Since:          query.Since,
		Until:          query.Until,
	})
	if err != nil {
		return authorityFundingSeries{}, err
	}
	series := authorityFundingSeries{
		InstrumentID: query.InstrumentID, Venue: string(instrument.Catalog.Venue),
		Since: query.Since, Until: query.Until, ObservedAt: snapshot.ObservedAt,
		Points: make([]authorityFundingSeriesPoint, 0, len(snapshot.Points)),
	}
	for _, point := range snapshot.Points {
		converted := authorityFundingSeriesPoint{FundingTime: point.FundingTime, Rate: point.Rate.String()}
		if point.IntervalHours.Valid {
			converted.IntervalHours = point.IntervalHours.Value
		}
		series.Points = append(series.Points, converted)
	}
	return series, nil
}

func (reads managerAuthorityReads) MarketData(ctx context.Context, venue, environment, product, instrumentID string) (authorityMarketData, error) {
	venue = strings.TrimSpace(venue)
	environment = strings.TrimSpace(environment)
	product = strings.TrimSpace(product)
	instrumentID = strings.TrimSpace(instrumentID)
	if venue == "" || environment == "" || instrumentID == "" || (product != string(exchange.ProductSpot) && product != string(exchange.ProductPerp)) {
		return authorityMarketData{}, errors.New("market-data venue, environment, product, and instrumentId are required")
	}
	if reads.markets == nil {
		return authorityMarketData{}, errors.New("public market-data source is unavailable")
	}
	return reads.publicMarketData(ctx, publicmarket.Query{
		Venue: venue, Environment: environment, Product: product, InstrumentID: instrumentID,
	})
}

func (reads managerAuthorityReads) publicMarketData(ctx context.Context, query publicmarket.Query) (authorityMarketData, error) {
	fact, err := reads.markets.MarketData(ctx, query, authorityDepthLimit)
	if err != nil {
		return authorityMarketData{}, fmt.Errorf("read public market-data capability: %w", err)
	}
	result := authorityMarketData{
		Venue: query.Venue, Environment: query.Environment, Product: query.Product, InstrumentID: query.InstrumentID,
	}
	if fact.HasMark {
		result.MarkPrice = fact.Mark.Price.String()
		result.ObservedAt = fact.Mark.Time.UTC()
	}
	// Production MarketData reads depth and mark from Market Hub. Funding and
	// OI are independent resident derivative facts; joining their current
	// values here is memory-only and deliberately excludes settled history.
	if query.Product == string(exchange.ProductPerp) {
		if board, boardErr := reads.FundingBoard(ctx); boardErr == nil {
			for _, venue := range board.Venues {
				if venue.Venue != query.Venue || venue.Product != query.Product {
					continue
				}
				for _, row := range venue.Rows {
					if row.InstrumentID != query.InstrumentID {
						continue
					}
					if !containsField(row.StaleFields, "funding") && !containsField(row.MissingFields, "funding") && row.Rate != "" {
						result.FundingRate, result.FundingObservedAt = row.Rate, row.FundingObservedAt.UTC()
						result.NextFundingTime = row.NextFundingTime.UTC()
					}
					if result.MarkPrice == "" && !containsField(row.StaleFields, "mark") && !containsField(row.MissingFields, "mark") {
						result.MarkPrice = row.MarkPrice
						if row.MarkObservedAt.After(result.ObservedAt) {
							result.ObservedAt = row.MarkObservedAt.UTC()
						}
					}
					if !containsField(row.StaleFields, "open_interest") && !containsField(row.MissingFields, "open_interest") {
						result.OpenInterest = row.OpenInterest
					}
					break
				}
			}
		}
	}
	book := fact.Book
	if len(book.Bids) == 0 || len(book.Asks) == 0 || len(book.Bids) > authorityDepthLimit || len(book.Asks) > authorityDepthLimit {
		return authorityMarketData{}, errors.New("bounded two-sided order book is unavailable")
	}
	result.Bids, err = projectAuthorityLevels(book.Bids)
	if err != nil {
		return authorityMarketData{}, err
	}
	result.Asks, err = projectAuthorityLevels(book.Asks)
	if err != nil {
		return authorityMarketData{}, err
	}
	result.BestBid, result.BestBidSize = result.Bids[0].Price, result.Bids[0].Quantity
	result.BestAsk, result.BestAskSize = result.Asks[0].Price, result.Asks[0].Quantity
	result.SourceSequence = book.Sequence
	if book.Time.IsZero() {
		if result.ObservedAt.IsZero() {
			result.ObservedAt = reads.clock()
		}
	} else if result.ObservedAt.IsZero() || book.Time.After(result.ObservedAt) {
		result.ObservedAt = book.Time.UTC()
	}
	if result.ObservedAt.IsZero() {
		return authorityMarketData{}, errors.New("market-data observation time is unavailable")
	}
	return result, nil
}

func containsField(fields []string, target string) bool {
	for _, field := range fields {
		if field == target {
			return true
		}
	}
	return false
}

// Candles answers the chart's credential-free public read. It always resolves
// through the public market source; account routes are private execution state
// and must never become a hidden market-data fallback.
func (reads managerAuthorityReads) Candles(ctx context.Context, query authorityCandlesQuery) (authorityCandles, error) {
	query.Venue = strings.TrimSpace(query.Venue)
	query.Environment = strings.TrimSpace(query.Environment)
	query.Product = strings.TrimSpace(query.Product)
	query.InstrumentID = strings.TrimSpace(query.InstrumentID)
	query.Interval = strings.TrimSpace(query.Interval)
	if query.Venue == "" || query.Environment == "" || query.InstrumentID == "" || query.Interval == "" ||
		(query.Product != string(exchange.ProductSpot) && query.Product != string(exchange.ProductPerp)) {
		return authorityCandles{}, errors.New("candles venue, environment, product, instrumentId, and interval are required")
	}
	if query.Limit <= 0 || query.Limit > authorityCandleLimitMax {
		query.Limit = authorityCandleLimitMax
	}
	if reads.markets == nil {
		return authorityCandles{}, errors.New("public candle source is unavailable")
	}
	return reads.publicCandles(ctx, query)
}

func (reads managerAuthorityReads) publicCandles(ctx context.Context, query authorityCandlesQuery) (authorityCandles, error) {
	page, err := reads.markets.Candles(ctx, publicmarket.Query{
		Venue: query.Venue, Environment: query.Environment, Product: query.Product, InstrumentID: query.InstrumentID,
	}, query.Interval, query.Limit)
	if err != nil {
		return authorityCandles{}, fmt.Errorf("read public candles capability: %w", err)
	}
	result := authorityCandles{
		Venue: query.Venue, Environment: query.Environment, Product: query.Product,
		InstrumentID: query.InstrumentID, Interval: query.Interval,
		Candles: make([]authorityCandle, len(page.Page.Candles)),
	}
	for index, candle := range page.Page.Candles {
		if err := httpapi.ValidCandle(candle); err != nil {
			return authorityCandles{}, fmt.Errorf("read candles: %w", err)
		}
		if candle.OpenTime.After(result.ObservedAt) {
			result.ObservedAt = candle.OpenTime.UTC()
		}
		result.Candles[index] = authorityCandle{
			OpenTime: candle.OpenTime.UTC(), CloseTime: candle.CloseTime.UTC(),
			Open: candle.Open.String(), High: candle.High.String(), Low: candle.Low.String(),
			Close: candle.Close.String(), Volume: candle.Volume.String(), Complete: candle.Complete,
		}
	}
	if result.ObservedAt.IsZero() {
		result.ObservedAt = reads.clock()
	}
	return result, nil
}

// authorityCandleLimit parses the caller's bound. An absent limit is the
// default rather than an error: the chart's first paint wants "a screenful",
// and naming a number is the caller narrowing that, not supplying something
// missing.
func authorityCandleLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return authorityCandleLimitMax, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > authorityCandleLimitMax {
		return 0, errors.New("candles limit is invalid")
	}
	return limit, nil
}

// authorityHistoryBound parses one end of a funding-history window.
//
// RFC 3339 with an explicit offset, not epoch milliseconds, because this bound
// is written by a browser, narrowed by a product service, and finally handed to
// a venue in whatever unit that venue wants. A bare integer that lost its unit
// somewhere along the way still parses — into a different decade — and the only
// symptom is an empty chart.
func authorityHistoryBound(raw string) (time.Time, error) {
	bound, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, errors.New("funding history since and until must be RFC 3339 timestamps")
	}
	return bound.UTC(), nil
}

func projectAuthorityLevels(levels []exchange.BookLevel) ([]authorityLevel, error) {
	result := make([]authorityLevel, len(levels))
	for index, level := range levels {
		if !level.Price.IsPositive() || !level.Quantity.IsPositive() {
			return nil, errors.New("order-book level is invalid")
		}
		result[index] = authorityLevel{Price: level.Price.String(), Quantity: level.Quantity.String()}
	}
	return result, nil
}

func authorityError(err error) error {
	switch {
	case errors.Is(err, account.ErrUnknownAccount):
		return httpapi.AuthorityError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND"}
	case errors.Is(err, account.ErrStaleWriteFence):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "STALE_RUNTIME_FENCE", Retryable: true}
	case errors.Is(err, account.ErrRuntimeBlocked):
		return httpapi.AuthorityError{Status: http.StatusConflict, Code: "ACCOUNT_RUNTIME_BLOCKED"}
	case errors.Is(err, account.ErrLiveWriteDisabled):
		return httpapi.AuthorityError{Status: http.StatusForbidden, Code: "LIVE_PRIVATE_WRITE_DISABLED"}
	case errors.Is(err, fundingboard.ErrBoardUnavailable):
		// A table where every venue failed is a snapshot, not an error, so
		// reaching here means the source itself could not be built or has been
		// closed. That is a gateway configuration fault the caller may retry
		// through, not a 500 the caller caused.
		return httpapi.AuthorityError{Status: http.StatusServiceUnavailable, Code: "FUNDING_BOARD_UNAVAILABLE", Retryable: true}
	case errors.Is(err, fundingboard.ErrHistoryUnsupported):
		// Not retryable and not a 503: nado publishes no settled series and
		// never will, and neither does a venue the board does not carry. A
		// retryable code here would have the caller poll forever for a chart
		// that cannot exist, instead of saying so beside the leg that drew.
		return httpapi.AuthorityError{Status: http.StatusNotFound, Code: "FUNDING_HISTORY_UNSUPPORTED"}
	case errors.Is(err, fundingboard.ErrHistoryUnknownInstrument):
		return httpapi.AuthorityError{Status: http.StatusNotFound, Code: "FUNDING_HISTORY_INSTRUMENT_UNKNOWN"}
	case errors.Is(err, fundingboard.ErrHistoryWindow):
		return httpapi.AuthorityError{Status: http.StatusBadRequest, Code: "FUNDING_HISTORY_WINDOW_UNSUPPORTED"}
	default:
		return httpapi.AuthorityError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Retryable: true}
	}
}

// connectionView deliberately does not predict the permissions of the API key.
func (authority *gatewayAuthority) connectionView(id string) (any, error) {
	runtime, err := authority.manager.Runtime(domain.AccountID(id))
	if err != nil {
		return nil, err
	}
	return struct {
		AccountID  string    `json:"accountId"`
		RuntimeID  string    `json:"runtimeId"`
		Connected  bool      `json:"connected"`
		ObservedAt time.Time `json:"observedAt"`
	}{id, runtime.RuntimeID, runtime.Ready, runtime.ObservedAt}, nil
}

func authorityQueryExactly(query map[string][]string, fields ...string) bool {
	if len(query) != len(fields) {
		return false
	}
	for _, field := range fields {
		if values, ok := query[field]; !ok || len(values) != 1 {
			return false
		}
	}
	return true
}
