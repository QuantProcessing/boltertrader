package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/catalog"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/marketdata"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/calculation"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/planning"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
	"github.com/shopspring/decimal"
)

var ErrFundingPreviewStale = errors.New("Funding preview authority is stale")

var conservativeTakerFeeRate = decimal.RequireFromString("0.001")

type FundingMarketDataAuthority interface {
	Get(context.Context, marketdata.Query) (marketdata.Fact, error)
}

type FundingAccountAuthority interface {
	Get(context.Context, string) (accounts.Account, error)
	List(context.Context) ([]accounts.Account, error)
}

type FundingReservationAuthority interface {
	Get(context.Context, string) (reservations.Reservation, error)
	List(context.Context, string, reservations.Mode) ([]reservations.Reservation, error)
}

type SharedFundingProjectionOptions struct {
	Catalog      CatalogProjection
	MarketData   FundingMarketDataAuthority
	Accounts     FundingAccountAuthority
	Reservations FundingReservationAuthority
	Admission    fundingpolicy.Admission
	Now          func() time.Time
}

type sharedFundingProjection struct {
	options  SharedFundingProjectionOptions
	mu       sync.Mutex
	previews map[string]previewAuthority
}

type previewAuthority struct {
	preview          FundingExecutionPreview
	factsRevision    string
	longAccount      accounts.Account
	shortAccount     accounts.Account
	longReservation  reservations.Reservation
	shortReservation reservations.Reservation
}

type fundingCandidate struct {
	input         calculation.Input
	plan          calculation.Plan
	factsRevision string
}

const conservativeTakerFeeAssumption = "conservative_taker_fee_rate"

func NewSharedFundingProjection(options SharedFundingProjectionOptions) (FundingProjection, error) {
	if options.Catalog == nil || options.MarketData == nil || options.Accounts == nil || options.Reservations == nil || options.Now == nil {
		return nil, configErr("Funding projection dependencies are incomplete")
	}
	return &sharedFundingProjection{options: options, previews: make(map[string]previewAuthority)}, nil
}

func (projection *sharedFundingProjection) ListOpportunities(ctx context.Context, principal, mode string) ([]FundingOpportunity, error) {
	candidates, _, err := projection.candidates(ctx, principal, mode)
	if err != nil {
		return nil, err
	}
	items := make([]FundingOpportunity, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, FundingOpportunity{
			OpportunityID: candidate.input.OpportunityID, CanonicalMarketID: candidate.plan.RouteID,
			LongVenue: candidate.input.Long.Venue, ShortVenue: candidate.input.Short.Venue,
			LongFundingRate: candidate.input.Long.Current.Rate.String(), ShortFundingRate: candidate.input.Short.Current.Rate.String(),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].OpportunityID < items[j].OpportunityID })
	return items, nil
}

func (projection *sharedFundingProjection) GetOpportunity(ctx context.Context, principal, mode, id string) (FundingOpportunity, error) {
	items, err := projection.ListOpportunities(ctx, principal, mode)
	if err != nil {
		return FundingOpportunity{}, err
	}
	for _, item := range items {
		if item.OpportunityID == id {
			return item, nil
		}
	}
	return FundingOpportunity{}, work.ErrWorkNotFound
}

func (projection *sharedFundingProjection) CreatePreview(ctx context.Context, principal string, request FundingPreviewRequest) (FundingExecutionPreview, error) {
	requested, err := decimal.NewFromString(request.NotionalPerLeg)
	if err != nil || !requested.IsPositive() {
		return FundingExecutionPreview{}, ErrFundingPreviewStale
	}
	candidates, snapshot, err := projection.candidates(ctx, principal, request.Mode)
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	available, err := projection.options.Reservations.List(ctx, principal, reservations.Mode(request.Mode))
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	for _, candidate := range candidates {
		input := candidate.input
		if input.OpportunityID != request.OpportunityID || input.Long.AccountFence.AccountID != request.LongAccountID || input.Short.AccountFence.AccountID != request.ShortAccountID || candidate.plan.RouteID != request.CanonicalMarketID {
			continue
		}
		input.RequestedNotionalPerLeg = requested
		preliminary, computeErr := calculation.ComputeWithAdmission(input, projection.options.Admission)
		if computeErr != nil || !preliminary.CanSubmit || len(preliminary.Legs) != 2 {
			return FundingExecutionPreview{}, ErrFundingPreviewStale
		}
		longAccount, shortAccount, longReservation, shortReservation, authorityErr := projection.validateAuthority(ctx, principal, request.Mode, input, preliminary, available)
		if authorityErr != nil {
			return FundingExecutionPreview{}, authorityErr
		}
		longAuthority, shortAuthority := freezeReservation(longReservation), freezeReservation(shortReservation)
		authorities := []planning.ReservationAuthority{longAuthority, shortAuthority}
		planningFact := planning.NewPlanningFact(input, principal, longReservation.ReservationID, shortReservation.ReservationID, authorities, feeAssumptions(input))
		input = planningFact.Input
		plan, computeErr := calculation.ComputeWithAdmission(input, projection.options.Admission)
		if computeErr != nil || !plan.CanSubmit {
			return FundingExecutionPreview{}, ErrFundingPreviewStale
		}
		payload, marshalErr := json.Marshal(planning.OpenIntent{PreviewID: plan.PreviewID, OpportunityID: plan.OpportunityID, LongAccountID: request.LongAccountID, ShortAccountID: request.ShortAccountID, CanonicalMarketID: plan.RouteID, NotionalPerLeg: requested.String(), MaxSlippageBps: fmt.Sprint(request.MaxSlippageBps), HoldThroughSettlement: request.HoldThroughSettlement})
		if marshalErr != nil {
			return FundingExecutionPreview{}, marshalErr
		}
		fences, marshalErr := json.Marshal([]map[string]any{{"accountId": longAccount.ID, "desiredStateRevision": longAccount.DesiredStateRevision, "runtimeGeneration": longAccount.RuntimeGeneration, "credentialVersion": longAccount.CredentialVersion}, {"accountId": shortAccount.ID, "desiredStateRevision": shortAccount.DesiredStateRevision, "runtimeGeneration": shortAccount.RuntimeGeneration, "credentialVersion": shortAccount.CredentialVersion}})
		if marshalErr != nil {
			return FundingExecutionPreview{}, marshalErr
		}
		planningPayload, marshalErr := json.Marshal(planningFact)
		if marshalErr != nil {
			return FundingExecutionPreview{}, marshalErr
		}
		frozenCursor := "funding_preview_" + plan.NormalizedHash
		frozenFact := work.Fact{FactID: "fact_" + frozenCursor, Cursor: frozenCursor, ProductID: "funding", Mode: request.Mode, Kind: planning.FactKind, Payload: planningPayload, EffectiveAt: projection.options.Now().UTC()}
		preview := FundingExecutionPreview{PreviewID: plan.PreviewID, OpportunityID: plan.OpportunityID, CanSubmit: true, Blockers: []string{}, Warnings: findingMessages(plan.Warnings), Mode: request.Mode, ExpiresAt: projection.options.Now().UTC().Add(30 * time.Second), CatalogRevision: snapshot.Revision, FactsRevision: candidate.factsRevision, Plan: plan, PrincipalID: principal, Facts: []work.Fact{frozenFact}, AccountFences: fences, WorkPayload: payload}
		projection.mu.Lock()
		projection.evictExpiredPreviewsLocked(projection.options.Now().UTC())
		projection.previews[preview.PreviewID] = previewAuthority{preview: preview, factsRevision: candidate.factsRevision, longAccount: longAccount, shortAccount: shortAccount, longReservation: longReservation, shortReservation: shortReservation}
		projection.mu.Unlock()
		return preview, nil
	}
	return FundingExecutionPreview{}, work.ErrWorkNotFound
}

func (projection *sharedFundingProjection) CreateCloseAuthority(ctx context.Context, principal, previewID, lineageID string) (FundingExecutionPreview, error) {
	authority, err := projection.currentPreviewAuthority(ctx, principal, previewID, false)
	if err != nil || lineageID == "" {
		return FundingExecutionPreview{}, ErrFundingPreviewStale
	}
	available, err := projection.options.Reservations.List(ctx, principal, reservations.Mode(authority.preview.Mode))
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	if len(authority.preview.Plan.Legs) != 2 {
		return FundingExecutionPreview{}, ErrFundingPreviewStale
	}
	selected := make([]reservations.Reservation, 0, 2)
	for _, leg := range authority.preview.Plan.Legs {
		reservation, ok := projection.findReservationForAmount(available, principal, authority.preview.Mode, leg, reservations.KindClose)
		if !ok {
			return FundingExecutionPreview{}, ErrFundingPreviewStale
		}
		selected = append(selected, reservation)
	}
	requestedAt := projection.options.Now().UTC()
	frozen := []planning.ReservationAuthority{freezeReservation(selected[0]), freezeReservation(selected[1])}
	identity := work.CanonicalRequestHash(struct {
		Principal    string
		PreviewID    string
		LineageID    string
		RequestedAt  time.Time
		Reservations []planning.ReservationAuthority
	}{principal, previewID, lineageID, requestedAt, frozen})
	cursor := "funding_close_" + identity
	facts := make([]work.Fact, 0, 2)
	for index, item := range frozen {
		payload, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			return FundingExecutionPreview{}, marshalErr
		}
		facts = append(facts, work.Fact{FactID: fmt.Sprintf("fact_%s_%d", cursor, index), Cursor: cursor, ProductID: "funding", Mode: authority.preview.Mode, Kind: "funding.unwind-reservation", AccountID: item.AccountID, Payload: payload, EffectiveAt: requestedAt})
	}
	payload, err := json.Marshal(work.FundingCloseIntent{LineageID: lineageID, Reason: "browser_request", RequestedAt: requestedAt})
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	closeAuthority := authority.preview
	closeAuthority.Facts = facts
	closeAuthority.FactsRevision = identity
	closeAuthority.WorkPayload = payload
	return closeAuthority, nil
}

func (projection *sharedFundingProjection) GetPreview(ctx context.Context, principal, id string) (FundingExecutionPreview, error) {
	authority, err := projection.currentPreviewAuthority(ctx, principal, id, true)
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	return authority.preview, nil
}

func (projection *sharedFundingProjection) currentPreviewAuthority(ctx context.Context, principal, id string, requireOpenReservations bool) (previewAuthority, error) {
	projection.mu.Lock()
	projection.evictExpiredPreviewsLocked(projection.options.Now().UTC())
	authority, ok := projection.previews[id]
	projection.mu.Unlock()
	if !ok || authority.preview.PrincipalID != principal {
		return previewAuthority{}, ErrFundingPreviewStale
	}
	candidates, snapshot, err := projection.candidates(ctx, principal, authority.preview.Mode)
	if err != nil || snapshot.Revision != authority.preview.CatalogRevision {
		projection.evictPreview(id)
		return previewAuthority{}, ErrFundingPreviewStale
	}
	current := false
	for _, candidate := range candidates {
		if candidate.input.OpportunityID == authority.preview.OpportunityID && candidate.factsRevision == authority.factsRevision && candidate.plan.RouteID == authority.preview.Plan.RouteID {
			current = true
			break
		}
	}
	reservationsCurrent := !requireOpenReservations || (projection.reservationCurrent(ctx, authority.longReservation) && projection.reservationCurrent(ctx, authority.shortReservation))
	if !current || !projection.accountCurrent(ctx, authority.longAccount) || !projection.accountCurrent(ctx, authority.shortAccount) || !reservationsCurrent {
		projection.evictPreview(id)
		return previewAuthority{}, ErrFundingPreviewStale
	}
	return authority, nil
}

func (projection *sharedFundingProjection) evictExpiredPreviewsLocked(now time.Time) {
	for id, authority := range projection.previews {
		if !now.Before(authority.preview.ExpiresAt) {
			delete(projection.previews, id)
		}
	}
}

func (projection *sharedFundingProjection) evictPreview(id string) {
	projection.mu.Lock()
	delete(projection.previews, id)
	projection.mu.Unlock()
}

func (projection *sharedFundingProjection) candidates(ctx context.Context, principal, mode string) ([]fundingCandidate, catalog.Snapshot, error) {
	if principal == "" || (mode != "test" && mode != "live") {
		return nil, catalog.Snapshot{}, ErrFundingPreviewStale
	}
	snapshot, err := projection.options.Catalog.Snapshot(ctx)
	if err != nil {
		return nil, catalog.Snapshot{}, err
	}
	availableAccounts, err := projection.options.Accounts.List(ctx)
	if err != nil {
		return nil, catalog.Snapshot{}, err
	}
	type scopedLeg struct {
		account    accounts.Account
		instrument catalog.Instrument
		fact       marketdata.Fact
		leg        calculation.LegFact
	}
	legs := []scopedLeg{}
	for _, account := range availableAccounts {
		if !accountEligible(account, mode) {
			continue
		}
		for _, instrument := range snapshot.Instruments {
			if !instrument.Ready || instrument.Product != "perp" || instrument.Venue != account.Venue || instrument.Environment != string(account.VenueEnvironment) {
				continue
			}
			query := marketdata.Query{Venue: instrument.Venue, Environment: instrument.Environment, Product: instrument.Product, InstrumentID: instrument.InstrumentID}
			fact, factErr := projection.options.MarketData.Get(ctx, query)
			if factErr != nil {
				continue
			}
			leg, legErr := fundingLeg(account, instrument, fact)
			if legErr != nil {
				continue
			}
			legs = append(legs, scopedLeg{account: account, instrument: instrument, fact: fact, leg: leg})
		}
	}
	items := []fundingCandidate{}
	for left := range legs {
		for right := range legs {
			long, short := legs[left], legs[right]
			if long.account.ID == short.account.ID || long.account.Venue == short.account.Venue || long.instrument.BaseAsset == "" || long.instrument.BaseAsset != short.instrument.BaseAsset || long.instrument.QuoteAsset != short.instrument.QuoteAsset || !short.leg.Current.Rate.GreaterThan(long.leg.Current.Rate) {
				continue
			}
			identity := struct {
				Mode                                                                      string
				LongAccount, LongVenue, LongEnvironment, LongProduct, LongInstrument      string
				ShortAccount, ShortVenue, ShortEnvironment, ShortProduct, ShortInstrument string
			}{mode, long.account.ID, long.instrument.Venue, long.instrument.Environment, long.instrument.Product, long.instrument.InstrumentID, short.account.ID, short.instrument.Venue, short.instrument.Environment, short.instrument.Product, short.instrument.InstrumentID}
			encoded, _ := json.Marshal(identity)
			sum := sha256.Sum256(encoded)
			input := calculation.Input{Mode: mode, ProductID: "funding", OpportunityID: "funding_opportunity_" + hex.EncodeToString(sum[:8]), ObservedAt: projection.options.Now().UTC(), RequestedNotionalPerLeg: decimal.NewFromInt(1), Long: long.leg, Short: short.leg}
			applyConservativeFees(&input)
			plan, computeErr := calculation.ComputeWithAdmission(input, projection.options.Admission)
			if computeErr != nil {
				continue
			}
			revision := work.CanonicalRequestHash(struct {
				Catalog string
				Long    marketdata.Fact
				Short   marketdata.Fact
			}{snapshot.Revision, long.fact, short.fact})
			items = append(items, fundingCandidate{input: input, plan: plan, factsRevision: revision})
		}
	}
	return items, snapshot, nil
}

func fundingLeg(account accounts.Account, instrument catalog.Instrument, fact marketdata.Fact) (calculation.LegFact, error) {
	if fact.Venue != instrument.Venue || fact.Environment != instrument.Environment || fact.Product != instrument.Product || fact.InstrumentID != instrument.InstrumentID || len(fact.Bids) == 0 || len(fact.Asks) == 0 || len(fact.Bids) > marketdata.MaxDepthLevels || len(fact.Asks) > marketdata.MaxDepthLevels || len(fact.FundingHistory) == 0 || len(fact.FundingHistory) > marketdata.MaxFundingHistory || fact.FundingObservedAt.IsZero() || fact.FundingTime.IsZero() || fact.NextFundingTime.IsZero() || fact.ObservedAt.IsZero() {
		return calculation.LegFact{}, ErrFundingPreviewStale
	}
	parse := func(value string, required bool) (decimal.Decimal, error) {
		if value == "" && !required {
			return decimal.Zero, nil
		}
		result, err := decimal.NewFromString(value)
		if err != nil || (required && !result.IsPositive()) {
			return decimal.Zero, ErrFundingPreviewStale
		}
		return result, nil
	}
	bid, err := parse(fact.BestBid, true)
	if err != nil {
		return calculation.LegFact{}, err
	}
	bidSize, err := parse(fact.BestBidSize, true)
	if err != nil {
		return calculation.LegFact{}, err
	}
	ask, err := parse(fact.BestAsk, true)
	if err != nil {
		return calculation.LegFact{}, err
	}
	askSize, err := parse(fact.BestAskSize, true)
	if err != nil {
		return calculation.LegFact{}, err
	}
	mark, err := parse(fact.MarkPrice, true)
	if err != nil {
		return calculation.LegFact{}, err
	}
	rate, err := decimal.NewFromString(fact.FundingRate)
	if err != nil {
		return calculation.LegFact{}, ErrFundingPreviewStale
	}
	quantityIncrement, err := parse(instrument.Rules.QuantityIncrement, true)
	if err != nil {
		return calculation.LegFact{}, err
	}
	minQuantity, err := parse(instrument.Rules.MinQuantity, true)
	if err != nil {
		return calculation.LegFact{}, err
	}
	minNotional, err := parse(instrument.Rules.MinNotional, false)
	if err != nil {
		return calculation.LegFact{}, err
	}
	depth := calculation.Depth{Bids: make([]calculation.Level, 0, len(fact.Bids)), Asks: make([]calculation.Level, 0, len(fact.Asks))}
	for _, level := range fact.Bids {
		price, priceErr := parse(level.Price, true)
		quantity, quantityErr := parse(level.Quantity, true)
		if priceErr != nil || quantityErr != nil {
			return calculation.LegFact{}, ErrFundingPreviewStale
		}
		depth.Bids = append(depth.Bids, calculation.Level{Price: price, Quantity: quantity})
	}
	for _, level := range fact.Asks {
		price, priceErr := parse(level.Price, true)
		quantity, quantityErr := parse(level.Quantity, true)
		if priceErr != nil || quantityErr != nil {
			return calculation.LegFact{}, ErrFundingPreviewStale
		}
		depth.Asks = append(depth.Asks, calculation.Level{Price: price, Quantity: quantity})
	}
	history := make([]calculation.FundingPoint, 0, len(fact.FundingHistory))
	for _, point := range fact.FundingHistory {
		historyRate, historyErr := decimal.NewFromString(point.Rate)
		if historyErr != nil || point.FundingTime.IsZero() {
			return calculation.LegFact{}, ErrFundingPreviewStale
		}
		history = append(history, calculation.FundingPoint{Rate: historyRate, ObservedAt: point.ObservedAt, FundingTime: point.FundingTime})
	}
	return calculation.LegFact{AccountFence: calculation.AccountFence{AccountID: account.ID, RuntimeGeneration: int(account.RuntimeGeneration)}, Venue: instrument.Venue, VenueEnvironment: instrument.Environment, MarketProduct: instrument.Product, InstrumentID: instrument.InstrumentID, Symbol: instrument.Symbol, Current: calculation.FundingPoint{Rate: rate, ObservedAt: fact.FundingObservedAt, FundingTime: fact.FundingTime, NextFunding: fact.NextFundingTime}, History: history, MarketObservedAt: fact.ObservedAt, MarkPrice: mark, BBO: calculation.Quote{BidPrice: bid, BidSize: bidSize, AskPrice: ask, AskSize: askSize}, Depth: depth, QuantityIncrement: quantityIncrement, MinQuantity: minQuantity, MinNotional: minNotional}, nil
}

func (projection *sharedFundingProjection) validateAuthority(ctx context.Context, principal, mode string, input calculation.Input, plan calculation.Plan, available []reservations.Reservation) (accounts.Account, accounts.Account, reservations.Reservation, reservations.Reservation, error) {
	longAccount, err := projection.options.Accounts.Get(ctx, input.Long.AccountFence.AccountID)
	if err != nil {
		return accounts.Account{}, accounts.Account{}, reservations.Reservation{}, reservations.Reservation{}, ErrFundingPreviewStale
	}
	shortAccount, err := projection.options.Accounts.Get(ctx, input.Short.AccountFence.AccountID)
	if err != nil || !validFundingAccount(longAccount, mode, input.Long) || !validFundingAccount(shortAccount, mode, input.Short) {
		return accounts.Account{}, accounts.Account{}, reservations.Reservation{}, reservations.Reservation{}, ErrFundingPreviewStale
	}
	longReservation, longOK := projection.findReservation(available, principal, mode, input.Long)
	shortReservation, shortOK := projection.findReservation(available, principal, mode, input.Short)
	if !longOK || !shortOK || !reservationSufficient(longReservation, plan.Legs[0].Quantity, plan.Legs[0].Notional) || !reservationSufficient(shortReservation, plan.Legs[1].Quantity, plan.Legs[1].Notional) {
		return accounts.Account{}, accounts.Account{}, reservations.Reservation{}, reservations.Reservation{}, ErrFundingPreviewStale
	}
	return longAccount, shortAccount, longReservation, shortReservation, nil
}

func (projection *sharedFundingProjection) findReservation(available []reservations.Reservation, principal, mode string, leg calculation.LegFact) (reservations.Reservation, bool) {
	sort.Slice(available, func(i, j int) bool { return available[i].ReservationID < available[j].ReservationID })
	for _, item := range available {
		if validFundingReservation(item, principal, mode, leg, reservations.KindOpen, projection.options.Now().UTC()) {
			return item, true
		}
	}
	return reservations.Reservation{}, false
}

func (projection *sharedFundingProjection) accountCurrent(ctx context.Context, expected accounts.Account) bool {
	current, err := projection.options.Accounts.Get(ctx, expected.ID)
	return err == nil && current.State == accounts.StateActive && current.DesiredStateRevision == expected.DesiredStateRevision && current.RuntimeGeneration == expected.RuntimeGeneration && current.CredentialVersion == expected.CredentialVersion
}

func (projection *sharedFundingProjection) reservationCurrent(ctx context.Context, expected reservations.Reservation) bool {
	item, err := projection.options.Reservations.Get(ctx, expected.ReservationID)
	return err == nil && equalReservation(item, expected) && item.Status == reservations.StatusHeld && (item.ExpiresAt == nil || projection.options.Now().UTC().Before(item.ExpiresAt.UTC()))
}

func validFundingReservation(item reservations.Reservation, principal, mode string, leg calculation.LegFact, kind reservations.Kind, now time.Time) bool {
	return item.PrincipalID == principal && string(item.Mode) == mode && item.ProductID == "funding" && item.Kind == kind && item.Status == reservations.StatusHeld && item.Scope.AccountID == leg.AccountFence.AccountID && item.Scope.Venue == leg.Venue && item.Scope.VenueEnvironment == leg.VenueEnvironment && item.Scope.InstrumentID == leg.InstrumentID && !item.UpdatedAt.IsZero() && (item.ExpiresAt == nil || now.Before(item.ExpiresAt.UTC()))
}

func (projection *sharedFundingProjection) findReservationForAmount(available []reservations.Reservation, principal, mode string, leg calculation.PlannedLeg, kind reservations.Kind) (reservations.Reservation, bool) {
	sort.Slice(available, func(i, j int) bool { return available[i].ReservationID < available[j].ReservationID })
	for _, item := range available {
		fact := calculation.LegFact{AccountFence: calculation.AccountFence{AccountID: leg.AccountID}, Venue: leg.Venue, VenueEnvironment: leg.VenueEnvironment, InstrumentID: leg.InstrumentID}
		if validFundingReservation(item, principal, mode, fact, kind, projection.options.Now().UTC()) && reservationSufficient(item, leg.Quantity, leg.Notional) {
			return item, true
		}
	}
	return reservations.Reservation{}, false
}

func reservationSufficient(item reservations.Reservation, quantity, notional decimal.Decimal) bool {
	if item.Quantity != nil {
		value, err := decimal.NewFromString(*item.Quantity)
		return err == nil && value.GreaterThanOrEqual(quantity)
	}
	if item.Notional != nil && item.Currency != nil && *item.Currency != "" {
		value, err := decimal.NewFromString(*item.Notional)
		return err == nil && value.GreaterThanOrEqual(notional)
	}
	return false
}

func equalReservation(left, right reservations.Reservation) bool {
	return left.ReservationID == right.ReservationID && left.PrincipalID == right.PrincipalID && left.Mode == right.Mode && left.ProductID == right.ProductID && left.Kind == right.Kind && left.Scope == right.Scope && equalStringPointer(left.Quantity, right.Quantity) && equalStringPointer(left.Notional, right.Notional) && equalStringPointer(left.Currency, right.Currency) && left.Status == right.Status && equalTimePointer(left.ExpiresAt, right.ExpiresAt) && left.UpdatedAt.Equal(right.UpdatedAt)
}

func equalStringPointer(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func equalTimePointer(left, right *time.Time) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && left.Equal(*right))
}

func freezeReservation(item reservations.Reservation) planning.ReservationAuthority {
	return planning.ReservationAuthority{ReservationID: item.ReservationID, Revision: item.UpdatedAt.UTC(), PrincipalID: item.PrincipalID, Mode: string(item.Mode), ProductID: item.ProductID, Kind: string(item.Kind), AccountID: item.Scope.AccountID, Venue: item.Scope.Venue, VenueEnvironment: item.Scope.VenueEnvironment, InstrumentID: item.Scope.InstrumentID, PositionMode: item.Scope.PositionMode, SettlementScope: item.Scope.SettlementScope, Quantity: item.Quantity, Notional: item.Notional, Currency: item.Currency, Status: string(item.Status), ExpiresAt: item.ExpiresAt}
}

func accountEligible(account accounts.Account, mode string) bool {
	if account.State != accounts.StateActive || account.DesiredStateRevision < 1 || account.RuntimeGeneration < 1 || account.CredentialVersion < 1 || !containsString(account.ProductIDs, "funding") {
		return false
	}
	return (mode == "test" && account.VenueEnvironment != accounts.EnvironmentLive) || (mode == "live" && account.VenueEnvironment == accounts.EnvironmentLive)
}

func validFundingAccount(account accounts.Account, mode string, leg calculation.LegFact) bool {
	return accountEligible(account, mode) && account.ID == leg.AccountFence.AccountID && account.Venue == leg.Venue && string(account.VenueEnvironment) == leg.VenueEnvironment && account.RuntimeGeneration == int64(leg.AccountFence.RuntimeGeneration)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func applyConservativeFees(input *calculation.Input) {
	if input.Long.TakerFeeRate.IsZero() {
		input.Long.TakerFeeRate = conservativeTakerFeeRate
	}
	if input.Short.TakerFeeRate.IsZero() {
		input.Short.TakerFeeRate = conservativeTakerFeeRate
	}
}

func feeAssumptions(input calculation.Input) []planning.PolicyAssumption {
	result := []planning.PolicyAssumption{}
	if input.Long.TakerFeeRate.Equal(conservativeTakerFeeRate) {
		result = append(result, planning.PolicyAssumption{Code: conservativeTakerFeeAssumption, Value: "long=" + conservativeTakerFeeRate.String()})
	}
	if input.Short.TakerFeeRate.Equal(conservativeTakerFeeRate) {
		result = append(result, planning.PolicyAssumption{Code: conservativeTakerFeeAssumption, Value: "short=" + conservativeTakerFeeRate.String()})
	}
	return result
}

func findingMessages(findings []calculation.Finding) []string {
	result := make([]string, 0, len(findings))
	for _, finding := range findings {
		result = append(result, string(finding.Code)+": "+finding.Message)
	}
	return result
}
