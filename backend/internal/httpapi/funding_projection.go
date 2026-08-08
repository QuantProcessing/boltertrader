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
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/accounting"
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

type FundingPositionAuthority interface {
	ListContext(context.Context, string, portfolio.Mode, string, string) ([]portfolio.Position, error)
}

type FundingAccountingIngestor interface {
	Ingest(context.Context, accounting.IngestionRequest) error
}

type SharedFundingProjectionOptions struct {
	Catalog      CatalogProjection
	MarketData   FundingMarketDataAuthority
	Accounts     FundingAccountAuthority
	Reservations FundingReservationAuthority
	Executions   work.FundingExecutionStore
	Positions    FundingPositionAuthority
	Accounting   FundingAccountingIngestor
	Admission    fundingpolicy.Admission
	Now          func() time.Time
}

type sharedFundingProjection struct {
	options  SharedFundingProjectionOptions
	mu       sync.Mutex
	previews map[string]previewAuthority
}

type previewAuthority struct {
	preview       FundingExecutionPreview
	factsRevision string
	input         calculation.Input
	request       FundingPreviewRequest
	longAccount   accounts.Account
	shortAccount  accounts.Account
	longCurrency  string
	shortCurrency string
}

type fundingCandidate struct {
	input         calculation.Input
	plan          calculation.Plan
	factsRevision string
	longCurrency  string
	shortCurrency string
}

type fundingCloseLegAuthority struct {
	draft       work.FundingOrderDraft
	account     accounts.Account
	position    portfolio.Position
	requirement FundingReservationRequirement
}

type fundingDurableCloseAuthority struct {
	execution work.FundingExecution
	snapshot  catalog.Snapshot
	now       time.Time
	legs      []fundingCloseLegAuthority
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
			LongAccountID: candidate.input.Long.AccountFence.AccountID, ShortAccountID: candidate.input.Short.AccountFence.AccountID,
			LongInstrumentID: candidate.input.Long.InstrumentID, ShortInstrumentID: candidate.input.Short.InstrumentID,
			LongVenue: candidate.input.Long.Venue, ShortVenue: candidate.input.Short.Venue,
			LongFundingRate: candidate.input.Long.Current.Rate.String(), ShortFundingRate: candidate.input.Short.Current.Rate.String(),
			ReservationRequirements: reservationScopeRequirements(candidate.plan, reservations.KindOpen, mode),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].OpportunityID < items[j].OpportunityID })
	return items, nil
}

// RefreshAccounting identifies every owned Funding scope and ingests
// authoritative accounting wherever persisted exposure remains.
func (projection *sharedFundingProjection) RefreshAccounting(ctx context.Context, principal, mode string) (FundingReadScopes, error) {
	if principal == "" || mode != "test" || projection.options.Executions == nil || projection.options.Positions == nil || projection.options.Accounting == nil {
		return nil, fmt.Errorf("Funding accounting refresh is unavailable")
	}
	executions, err := projection.options.Executions.ListFundingExecutions(ctx, principal, orders.Mode(mode))
	if err != nil {
		return nil, err
	}
	scopes := make(FundingReadScopes, 0)
	positionsByAccount := make(map[string][]portfolio.Position)
	for _, execution := range executions {
		if execution.PrincipalID != principal || execution.Mode != orders.Mode(mode) {
			return nil, fmt.Errorf("refresh authoritative Funding accounting: invalid execution scope")
		}
		for _, executionLeg := range execution.Legs {
			scopes = append(scopes, FundingReadScope{LineageID: execution.LineageID, AccountID: executionLeg.Draft.AccountID, InstrumentID: executionLeg.Draft.InstrumentID})
			positions, loaded := positionsByAccount[executionLeg.Draft.AccountID]
			if !loaded {
				positions, err = projection.options.Positions.ListContext(ctx, principal, portfolio.Mode(mode), executionLeg.Draft.AccountID, "funding")
				if err != nil {
					return nil, err
				}
				positionsByAccount[executionLeg.Draft.AccountID] = positions
			}
			position, ok := persistedFundingPosition(positions, mode, execution.LineageID, executionLeg.Draft)
			if !ok {
				if execution.State == work.FundingExecutionClosed {
					return nil, fmt.Errorf("refresh authoritative Funding accounting: %w", ErrFundingPreviewStale)
				}
				continue
			}
			quantity, parseErr := decimal.NewFromString(position.Quantity)
			if parseErr != nil {
				return nil, fmt.Errorf("refresh authoritative Funding accounting: invalid position quantity")
			}
			if quantity.IsZero() {
				continue
			}
			if position.SettlementCurrency == "" {
				return nil, fmt.Errorf("refresh authoritative Funding accounting: %w", ErrFundingPreviewStale)
			}
			if err := projection.options.Accounting.Ingest(ctx, accounting.IngestionRequest{
				PrincipalID: principal, RequestID: "funding-accounting:" + execution.ExecutionID,
				ProductID: "funding", Mode: mode, LineageID: execution.LineageID, PositionID: execution.ExecutionID,
				AccountID: executionLeg.Draft.AccountID, InstrumentID: executionLeg.Draft.InstrumentID, SettlementAsset: position.SettlementCurrency,
				Start: execution.CreatedAt.UTC(), End: projection.options.Now().UTC(),
			}); err != nil {
				return nil, fmt.Errorf("refresh authoritative Funding accounting: %w", err)
			}
		}
	}
	return scopes, nil
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
		longAccount, shortAccount, authorityErr := projection.validateAccounts(ctx, request.Mode, input)
		if authorityErr != nil {
			return FundingExecutionPreview{}, authorityErr
		}
		fences, marshalErr := json.Marshal([]map[string]any{{"accountId": longAccount.ID, "desiredStateRevision": longAccount.DesiredStateRevision, "runtimeGeneration": longAccount.RuntimeGeneration, "credentialVersion": longAccount.CredentialVersion}, {"accountId": shortAccount.ID, "desiredStateRevision": shortAccount.DesiredStateRevision, "runtimeGeneration": shortAccount.RuntimeGeneration, "credentialVersion": shortAccount.CredentialVersion}})
		if marshalErr != nil {
			return FundingExecutionPreview{}, marshalErr
		}
		previewID := "funding_preview_" + work.CanonicalRequestHash(struct {
			Principal      string
			NormalizedHash string
		}{Principal: principal, NormalizedHash: preliminary.NormalizedHash})[:16]
		preview := FundingExecutionPreview{PreviewID: previewID, OpportunityID: preliminary.OpportunityID, CanSubmit: true, Blockers: []string{}, Warnings: findingMessages(preliminary.Warnings), Mode: request.Mode, ExpiresAt: projection.options.Now().UTC().Add(30 * time.Second), CatalogRevision: snapshot.Revision, FactsRevision: candidate.factsRevision, Plan: preliminary, ReservationRequirements: reservationRequirements(input, preliminary, reservations.KindOpen, request.Mode, candidate.longCurrency, candidate.shortCurrency), PrincipalID: principal, AccountFences: fences}
		projection.mu.Lock()
		projection.evictExpiredPreviewsLocked(projection.options.Now().UTC())
		projection.previews[preview.PreviewID] = previewAuthority{preview: preview, factsRevision: candidate.factsRevision, input: input, request: request, longAccount: longAccount, shortAccount: shortAccount, longCurrency: candidate.longCurrency, shortCurrency: candidate.shortCurrency}
		projection.mu.Unlock()
		return preview, nil
	}
	return FundingExecutionPreview{}, work.ErrWorkNotFound
}

func (projection *sharedFundingProjection) CreateCloseAuthority(ctx context.Context, principal, mode, previewID, lineageID string) (FundingExecutionPreview, error) {
	if projection.options.Executions != nil && projection.options.Positions != nil {
		return projection.createDurableCloseAuthority(ctx, principal, mode, lineageID)
	}
	authority, err := projection.currentPreviewAuthority(ctx, principal, previewID)
	if err != nil || lineageID == "" || authority.preview.Mode != mode {
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

func (projection *sharedFundingProjection) GetCloseReservationRequirements(ctx context.Context, principal, mode, lineageID string) ([]FundingReservationRequirement, error) {
	if projection.options.Executions == nil || projection.options.Positions == nil {
		return nil, ErrFundingPreviewStale
	}
	authority, err := projection.currentDurableCloseAuthority(ctx, principal, mode, lineageID)
	if err != nil {
		return nil, err
	}
	requirements := make([]FundingReservationRequirement, 0, len(authority.legs))
	for _, leg := range authority.legs {
		requirements = append(requirements, leg.requirement)
	}
	return requirements, nil
}

func (projection *sharedFundingProjection) createDurableCloseAuthority(ctx context.Context, principal, mode, lineageID string) (FundingExecutionPreview, error) {
	authority, err := projection.currentDurableCloseAuthority(ctx, principal, mode, lineageID)
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	available, err := projection.options.Reservations.List(ctx, principal, reservations.Mode(mode))
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	selected := make([]reservations.Reservation, 0, 2)
	frozen := make([]planning.ReservationAuthority, 0, 2)
	facts := make([]work.Fact, 0, 2)
	fences := make([]map[string]any, 0, 2)
	requirements := make([]FundingReservationRequirement, 0, 2)
	for _, leg := range authority.legs {
		reservation, ok := projection.findReservationForDraft(available, principal, mode, leg.draft, reservations.KindClose, leg.requirement.Currency)
		if !ok {
			return FundingExecutionPreview{}, ErrFundingPreviewStale
		}
		selected = append(selected, reservation)
		frozen = append(frozen, freezeReservation(reservation))
		fences = append(fences, map[string]any{"accountId": leg.account.ID, "desiredStateRevision": leg.account.DesiredStateRevision, "runtimeGeneration": leg.account.RuntimeGeneration, "credentialVersion": leg.account.CredentialVersion})
		requirements = append(requirements, leg.requirement)
	}
	if len(selected) != 2 {
		return FundingExecutionPreview{}, ErrFundingPreviewStale
	}
	requestedAt := authority.now
	identity := work.CanonicalRequestHash(struct {
		Principal, Mode, LineageID string
		ExecutionVersion           int64
		RequestedAt                time.Time
		Reservations               []planning.ReservationAuthority
	}{principal, mode, lineageID, authority.execution.Version, requestedAt, frozen})
	cursor := "funding_close_" + identity
	for index, item := range frozen {
		payload, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			return FundingExecutionPreview{}, marshalErr
		}
		facts = append(facts, work.Fact{FactID: fmt.Sprintf("fact_%s_%d", cursor, index), Cursor: cursor, ProductID: "funding", Mode: mode, Kind: "funding.unwind-reservation", AccountID: item.AccountID, Payload: payload, EffectiveAt: requestedAt})
	}
	payload, err := json.Marshal(work.FundingCloseIntent{LineageID: lineageID, Reason: "browser_request", RequestedAt: requestedAt})
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	encodedFences, err := json.Marshal(fences)
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	return FundingExecutionPreview{PreviewID: "funding_close_preview_" + identity[:16], CanSubmit: true, Blockers: []string{}, Warnings: []string{}, Mode: mode, ExpiresAt: authority.now.Add(30 * time.Second), CatalogRevision: authority.snapshot.Revision, FactsRevision: identity, ReservationRequirements: requirements, PrincipalID: principal, Facts: facts, AccountFences: encodedFences, WorkPayload: payload}, nil
}

func (projection *sharedFundingProjection) currentDurableCloseAuthority(ctx context.Context, principal, mode, lineageID string) (fundingDurableCloseAuthority, error) {
	if principal == "" || lineageID == "" || (mode != "test" && mode != "live") || projection.options.Executions == nil || projection.options.Positions == nil {
		return fundingDurableCloseAuthority{}, ErrFundingPreviewStale
	}
	execution, err := projection.options.Executions.GetFundingExecution(ctx, principal, orders.Mode(mode), lineageID)
	if err != nil || execution.PrincipalID != principal || string(execution.Mode) != mode || execution.LineageID != lineageID || execution.State != work.FundingExecutionHedged || len(execution.Legs) != 2 {
		return fundingDurableCloseAuthority{}, ErrFundingPreviewStale
	}
	positions, err := projection.options.Positions.ListContext(ctx, principal, portfolio.Mode(mode), "", "funding")
	if err != nil {
		return fundingDurableCloseAuthority{}, err
	}
	if projection.options.Accounting != nil {
		for _, executionLeg := range execution.Legs {
			position, ok := currentFundingPosition(positions, mode, lineageID, executionLeg.Draft)
			if !ok || position.SettlementCurrency == "" {
				return fundingDurableCloseAuthority{}, ErrFundingPreviewStale
			}
			if err := projection.options.Accounting.Ingest(ctx, accounting.IngestionRequest{
				PrincipalID: principal, RequestID: "funding-accounting:" + execution.ExecutionID,
				ProductID: "funding", Mode: mode, LineageID: lineageID, PositionID: execution.ExecutionID,
				AccountID: executionLeg.Draft.AccountID, InstrumentID: executionLeg.Draft.InstrumentID, SettlementAsset: position.SettlementCurrency,
				Start: execution.CreatedAt.UTC(), End: projection.options.Now().UTC(),
			}); err != nil {
				return fundingDurableCloseAuthority{}, fmt.Errorf("refresh authoritative Funding accounting: %w", err)
			}
		}
		positions, err = projection.options.Positions.ListContext(ctx, principal, portfolio.Mode(mode), "", "funding")
		if err != nil {
			return fundingDurableCloseAuthority{}, err
		}
	}
	snapshot, err := projection.options.Catalog.Snapshot(ctx)
	if err != nil {
		return fundingDurableCloseAuthority{}, err
	}
	authority := fundingDurableCloseAuthority{execution: execution, snapshot: snapshot, now: projection.options.Now().UTC(), legs: make([]fundingCloseLegAuthority, 0, 2)}
	seen := make(map[string]struct{}, 2)
	for _, executionLeg := range execution.Legs {
		draft := executionLeg.Draft
		identity := draft.AccountID + "\x00" + draft.InstrumentID
		if draft.AccountID == "" || draft.InstrumentID == "" {
			return fundingDurableCloseAuthority{}, ErrFundingPreviewStale
		}
		if _, duplicate := seen[identity]; duplicate {
			return fundingDurableCloseAuthority{}, ErrFundingPreviewStale
		}
		seen[identity] = struct{}{}
		account, accountErr := projection.options.Accounts.Get(ctx, draft.AccountID)
		if accountErr != nil || account.State != accounts.StateActive || account.DesiredStateRevision != draft.DesiredStateRevision || int(account.RuntimeGeneration) != draft.RuntimeGeneration || account.CredentialVersion != draft.CredentialVersion {
			return fundingDurableCloseAuthority{}, ErrFundingPreviewStale
		}
		position, ok := currentFundingPosition(positions, mode, lineageID, draft)
		if !ok {
			return fundingDurableCloseAuthority{}, ErrFundingPreviewStale
		}
		positionQuantity, positionErr := decimal.NewFromString(position.Quantity)
		draftQuantity, quantityErr := decimal.NewFromString(draft.Quantity)
		draftNotional, notionalErr := decimal.NewFromString(draft.Notional)
		directionMatches := (draft.Side == "buy" && positionQuantity.IsPositive()) || (draft.Side == "sell" && positionQuantity.IsNegative())
		if positionErr != nil || quantityErr != nil || notionalErr != nil || !draftQuantity.IsPositive() || !draftNotional.IsPositive() || !positionQuantity.Abs().Equal(draftQuantity) || !directionMatches || position.SettlementCurrency == "" {
			return fundingDurableCloseAuthority{}, ErrFundingPreviewStale
		}
		if !currentFundingInstrument(snapshot, draft) {
			return fundingDurableCloseAuthority{}, ErrFundingPreviewStale
		}
		market, marketErr := projection.options.MarketData.Get(ctx, marketdata.Query{Venue: draft.Venue, Environment: draft.VenueEnvironment, Product: "perp", InstrumentID: draft.InstrumentID})
		if marketErr != nil || market.Venue != draft.Venue || market.Environment != draft.VenueEnvironment || market.Product != "perp" || market.InstrumentID != draft.InstrumentID || market.ObservedAt.IsZero() {
			return fundingDurableCloseAuthority{}, ErrFundingPreviewStale
		}
		authority.legs = append(authority.legs, fundingCloseLegAuthority{draft: draft, account: account, position: position, requirement: reservationRequirement(draft.AccountID, draft.Venue, draft.VenueEnvironment, draft.InstrumentID, reservations.KindClose, mode, draft.Quantity, draft.Notional, position.SettlementCurrency)})
	}
	if len(authority.legs) != 2 {
		return fundingDurableCloseAuthority{}, ErrFundingPreviewStale
	}
	return authority, nil
}

func currentFundingPosition(positions []portfolio.Position, mode, lineageID string, draft work.FundingOrderDraft) (portfolio.Position, bool) {
	position, ok := persistedFundingPosition(positions, mode, lineageID, draft)
	return position, ok && position.Quantity != "0"
}

func persistedFundingPosition(positions []portfolio.Position, mode, lineageID string, draft work.FundingOrderDraft) (portfolio.Position, bool) {
	for _, position := range positions {
		if position.LineageID == lineageID && position.ProductID == "funding" && string(position.Mode) == mode && position.AccountID == draft.AccountID && position.InstrumentID == draft.InstrumentID {
			return position, true
		}
	}
	return portfolio.Position{}, false
}

func currentFundingInstrument(snapshot catalog.Snapshot, draft work.FundingOrderDraft) bool {
	for _, instrument := range snapshot.Instruments {
		if instrument.Ready && instrument.Product == "perp" && instrument.Venue == draft.Venue && instrument.Environment == draft.VenueEnvironment && instrument.InstrumentID == draft.InstrumentID {
			return true
		}
	}
	return false
}

func (projection *sharedFundingProjection) GetPreview(ctx context.Context, principal, id string) (FundingExecutionPreview, error) {
	authority, err := projection.currentPreviewAuthority(ctx, principal, id)
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	return projection.finalizeOpenPreview(ctx, principal, authority)
}

func (projection *sharedFundingProjection) finalizeOpenPreview(ctx context.Context, principal string, authority previewAuthority) (FundingExecutionPreview, error) {
	available, err := projection.options.Reservations.List(ctx, principal, reservations.Mode(authority.preview.Mode))
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	if len(authority.preview.Plan.Legs) != 2 {
		return FundingExecutionPreview{}, ErrFundingPreviewStale
	}
	longReservation, longOK := projection.findExactOpenReservation(available, principal, authority.preview.Mode, authority.preview.Plan.Legs[0], authority.longCurrency)
	shortReservation, shortOK := projection.findExactOpenReservation(available, principal, authority.preview.Mode, authority.preview.Plan.Legs[1], authority.shortCurrency)
	if !longOK || !shortOK {
		return FundingExecutionPreview{}, ErrFundingPreviewStale
	}
	authorities := []planning.ReservationAuthority{freezeReservation(longReservation), freezeReservation(shortReservation)}
	planningFact := planning.NewPlanningFact(authority.input, principal, longReservation.ReservationID, shortReservation.ReservationID, authorities, feeAssumptions(authority.input))
	plan, err := calculation.ComputeWithAdmission(planningFact.Input, projection.options.Admission)
	if err != nil || !plan.CanSubmit || len(plan.Legs) != 2 {
		return FundingExecutionPreview{}, ErrFundingPreviewStale
	}
	payload, err := json.Marshal(planning.OpenIntent{PreviewID: plan.PreviewID, OpportunityID: plan.OpportunityID, LongAccountID: authority.request.LongAccountID, ShortAccountID: authority.request.ShortAccountID, CanonicalMarketID: plan.RouteID, NotionalPerLeg: authority.input.RequestedNotionalPerLeg.String(), MaxSlippageBps: fmt.Sprint(authority.request.MaxSlippageBps), HoldThroughSettlement: authority.request.HoldThroughSettlement})
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	planningPayload, err := json.Marshal(planningFact)
	if err != nil {
		return FundingExecutionPreview{}, err
	}
	frozenCursor := "funding_preview_" + plan.NormalizedHash
	preview := authority.preview
	preview.Plan = plan
	preview.Facts = []work.Fact{{FactID: "fact_" + frozenCursor, Cursor: frozenCursor, ProductID: "funding", Mode: preview.Mode, Kind: planning.FactKind, Payload: planningPayload, EffectiveAt: preview.Plan.ObservedAt.UTC()}}
	preview.WorkPayload = payload
	return preview, nil
}

func (projection *sharedFundingProjection) currentPreviewAuthority(ctx context.Context, principal, id string) (previewAuthority, error) {
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
			input := candidate.input
			input.RequestedNotionalPerLeg = authority.input.RequestedNotionalPerLeg
			plan, computeErr := calculation.ComputeWithAdmission(input, projection.options.Admission)
			if computeErr != nil || !plan.CanSubmit || plan.NormalizedHash != authority.preview.Plan.NormalizedHash || len(plan.Legs) != 2 || plan.Legs[0].AccountID != authority.request.LongAccountID || plan.Legs[1].AccountID != authority.request.ShortAccountID {
				break
			}
			authority.input = input
			authority.preview.Plan = plan
			authority.longCurrency = candidate.longCurrency
			authority.shortCurrency = candidate.shortCurrency
			current = true
			break
		}
	}
	if !current || !projection.accountCurrent(ctx, authority.longAccount) || !projection.accountCurrent(ctx, authority.shortAccount) {
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
			items = append(items, fundingCandidate{input: input, plan: plan, factsRevision: revision, longCurrency: long.instrument.QuoteAsset, shortCurrency: short.instrument.QuoteAsset})
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

func (projection *sharedFundingProjection) validateAccounts(ctx context.Context, mode string, input calculation.Input) (accounts.Account, accounts.Account, error) {
	longAccount, err := projection.options.Accounts.Get(ctx, input.Long.AccountFence.AccountID)
	if err != nil {
		return accounts.Account{}, accounts.Account{}, ErrFundingPreviewStale
	}
	shortAccount, err := projection.options.Accounts.Get(ctx, input.Short.AccountFence.AccountID)
	if err != nil || !validFundingAccount(longAccount, mode, input.Long) || !validFundingAccount(shortAccount, mode, input.Short) {
		return accounts.Account{}, accounts.Account{}, ErrFundingPreviewStale
	}
	return longAccount, shortAccount, nil
}

func (projection *sharedFundingProjection) findExactOpenReservation(available []reservations.Reservation, principal, mode string, leg calculation.PlannedLeg, currency string) (reservations.Reservation, bool) {
	expectedScope := reservations.NettingScope{AccountID: leg.AccountID, Venue: leg.Venue, InstrumentID: leg.InstrumentID, VenueEnvironment: leg.VenueEnvironment}
	fact := calculation.LegFact{AccountFence: calculation.AccountFence{AccountID: leg.AccountID}, Venue: leg.Venue, VenueEnvironment: leg.VenueEnvironment, InstrumentID: leg.InstrumentID}
	sort.Slice(available, func(i, j int) bool { return available[i].ReservationID < available[j].ReservationID })
	for _, item := range available {
		if !validFundingReservation(item, principal, mode, fact, reservations.KindOpen, projection.options.Now().UTC()) || item.Scope != expectedScope || item.Quantity == nil || item.Notional == nil || item.Currency == nil || *item.Currency != currency {
			continue
		}
		quantity, quantityErr := decimal.NewFromString(*item.Quantity)
		notional, notionalErr := decimal.NewFromString(*item.Notional)
		if quantityErr == nil && notionalErr == nil && quantity.Equal(leg.Quantity) && notional.Equal(leg.Notional) {
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

func (projection *sharedFundingProjection) findReservationForDraft(available []reservations.Reservation, principal, mode string, draft work.FundingOrderDraft, kind reservations.Kind, currency string) (reservations.Reservation, bool) {
	quantity, quantityErr := decimal.NewFromString(draft.Quantity)
	notional, notionalErr := decimal.NewFromString(draft.Notional)
	if quantityErr != nil || notionalErr != nil || !quantity.IsPositive() || !notional.IsPositive() || currency == "" {
		return reservations.Reservation{}, false
	}
	expectedScope := reservations.NettingScope{AccountID: draft.AccountID, Venue: draft.Venue, InstrumentID: draft.InstrumentID, VenueEnvironment: draft.VenueEnvironment}
	fact := calculation.LegFact{AccountFence: calculation.AccountFence{AccountID: draft.AccountID}, Venue: draft.Venue, VenueEnvironment: draft.VenueEnvironment, InstrumentID: draft.InstrumentID}
	sort.Slice(available, func(i, j int) bool { return available[i].ReservationID < available[j].ReservationID })
	for _, item := range available {
		if !validFundingReservation(item, principal, mode, fact, kind, projection.options.Now().UTC()) || item.Scope != expectedScope || item.Quantity == nil || item.Notional == nil || item.Currency == nil || *item.Currency != currency {
			continue
		}
		reservedQuantity, reservedQuantityErr := decimal.NewFromString(*item.Quantity)
		reservedNotional, reservedNotionalErr := decimal.NewFromString(*item.Notional)
		if reservedQuantityErr == nil && reservedNotionalErr == nil && reservedQuantity.Equal(quantity) && reservedNotional.Equal(notional) {
			return item, true
		}
	}
	return reservations.Reservation{}, false
}

func reservationScopeRequirements(plan calculation.Plan, kind reservations.Kind, mode string) []FundingReservationScopeRequirement {
	if len(plan.Legs) != 2 {
		return []FundingReservationScopeRequirement{}
	}
	result := make([]FundingReservationScopeRequirement, 0, 2)
	for _, leg := range plan.Legs {
		result = append(result, FundingReservationScopeRequirement{
			AccountID: leg.AccountID, ProductID: "funding", ReservationKind: kind, Mode: mode,
			NettingScope: reservations.NettingScope{AccountID: leg.AccountID, Venue: leg.Venue, InstrumentID: leg.InstrumentID, VenueEnvironment: leg.VenueEnvironment},
		})
	}
	return result
}

func reservationRequirements(_ calculation.Input, plan calculation.Plan, kind reservations.Kind, mode string, currencies ...string) []FundingReservationRequirement {
	if len(plan.Legs) != 2 {
		return []FundingReservationRequirement{}
	}
	result := make([]FundingReservationRequirement, 0, 2)
	for index, leg := range plan.Legs {
		currency := ""
		if index < len(currencies) {
			currency = currencies[index]
		}
		requirement := reservationRequirement(leg.AccountID, leg.Venue, leg.VenueEnvironment, leg.InstrumentID, kind, mode, leg.Quantity.String(), leg.Notional.String(), currency)
		result = append(result, requirement)
	}
	return result
}

func reservationRequirement(accountID, venue, environment, instrumentID string, kind reservations.Kind, mode, quantity, notional, currency string) FundingReservationRequirement {
	return FundingReservationRequirement{
		AccountID: accountID, ProductID: "funding", ReservationKind: kind, Mode: mode, Quantity: quantity, Notional: notional, Currency: currency,
		NettingScope: reservations.NettingScope{AccountID: accountID, Venue: venue, InstrumentID: instrumentID, VenueEnvironment: environment},
	}
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
