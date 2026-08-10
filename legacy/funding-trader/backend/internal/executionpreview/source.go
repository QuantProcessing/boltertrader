package executionpreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	boltertrader "github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/shopspring/decimal"
)

const closeBookLimit = 500

var (
	tenThousand = decimal.NewFromInt(10_000)
	two         = decimal.NewFromInt(2)
)

type Reader interface {
	Opportunity(context.Context, readmodel.Scope, string) (json.RawMessage, error)
	Position(context.Context, readmodel.Scope, string) (json.RawMessage, error)
}

type Refresher interface {
	Refresh(context.Context) error
	FreshOpportunity(context.Context, string, string, string, string) (json.RawMessage, error)
}

type Gateway interface {
	DiscoverAccounts(context.Context) (boltertrader.AccountDiscovery, error)
	ListInstruments(context.Context, string) ([]boltertrader.InstrumentResource, error)
	Balances(context.Context, string) ([]boltertrader.BalanceResource, error)
	RiskLimits(context.Context, string) (boltertrader.RiskLimitReference, error)
	Positions(context.Context, string, string) ([]boltertrader.PositionResource, error)
}

type Market interface {
	Instruments(context.Context) ([]marketdata.Instrument, error)
	MarkPrice(context.Context, marketdata.Instrument) (marketdata.MarkPrice, error)
	BBO(context.Context, marketdata.Instrument) (marketdata.BBO, error)
	OrderBook(context.Context, marketdata.OrderBookRequest) (marketdata.OrderBook, error)
}

type SourceOptions struct {
	Reader    Reader
	Refresher Refresher
	Gateway   Gateway
	Market    Market
	Now       func() time.Time
}

type Source struct {
	reader    Reader
	refresher Refresher
	gateway   Gateway
	market    Market
	now       func() time.Time
}

func NewSource(options SourceOptions) (*Source, error) {
	if options.Reader == nil || options.Refresher == nil || options.Gateway == nil || options.Market == nil {
		return nil, errors.New("execution preview source dependencies are required")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Source{
		reader: options.Reader, refresher: options.Refresher,
		gateway: options.Gateway, market: options.Market, now: now,
	}, nil
}

func (source *Source) FreshPreview(
	ctx context.Context,
	scope executionservice.Scope,
	request executionservice.PreviewRequest,
) (execution.PreviewInput, error) {
	switch request.Operation {
	case execution.OperationOpen:
		return source.freshOpen(ctx, scope, request)
	case execution.OperationClose:
		return source.freshClose(ctx, scope, request)
	default:
		return execution.PreviewInput{}, executionservice.ErrPreviewMismatch
	}
}

func (source *Source) freshOpen(
	ctx context.Context,
	scope executionservice.Scope,
	request executionservice.PreviewRequest,
) (execution.PreviewInput, error) {
	raw, err := source.reader.Opportunity(ctx, readScope(scope), request.OpportunityID)
	if err != nil {
		return execution.PreviewInput{}, mapReadError(err)
	}
	var opportunity opportunityRecord
	if err := json.Unmarshal(raw, &opportunity); err != nil {
		return execution.PreviewInput{}, executionservice.ErrServiceUnavailable
	}
	if request.OpportunityID == "" || opportunity.OpportunityID != request.OpportunityID {
		return execution.PreviewInput{}, executionservice.ErrPreviewMismatch
	}
	if !opportunityRouteConsistent(opportunity) {
		return execution.PreviewInput{}, executionservice.ErrPreviewMismatch
	}

	storedOpportunity := opportunity
	mode := config.Mode(scope.Mode)
	longIdentity := legIdentity{
		LegIdentity: execution.LegIdentity{
			AccountID:        opportunity.LongAccountID,
			InstrumentID:     opportunity.LongInstrumentID,
			Venue:            opportunity.LongVenue,
			VenueEnvironment: opportunity.CalculationInputs.LongLeg.VenueEnvironment,
		},
		Symbol: opportunity.Symbol,
	}
	shortIdentity := legIdentity{
		LegIdentity: execution.LegIdentity{
			AccountID:        opportunity.ShortAccountID,
			InstrumentID:     opportunity.ShortInstrumentID,
			Venue:            opportunity.ShortVenue,
			VenueEnvironment: opportunity.CalculationInputs.ShortLeg.VenueEnvironment,
		},
		Symbol: opportunity.Symbol,
	}
	accountOK, rulesOK, longInstrument, shortInstrument := source.referenceEvidence(
		ctx,
		mode,
		longIdentity,
		shortIdentity,
		true,
	)
	availableCapital, balancesOK := source.availableCapital(
		ctx,
		opportunity.LongAccountID,
		opportunity.ShortAccountID,
		quoteAsset(longInstrument),
		quoteAsset(shortInstrument),
	)
	accountOK = accountOK && balancesOK

	// Gateway V1 deliberately exposes no normalized risk-limit fields. Even if
	// the reference endpoint is reachable, an empty reference cannot prove that
	// a proposed increase is safe.
	_, longRiskErr := source.gateway.RiskLimits(ctx, opportunity.LongAccountID)
	_, shortRiskErr := source.gateway.RiskLimits(ctx, opportunity.ShortAccountID)
	riskVerifiable := false
	if longRiskErr != nil || shortRiskErr != nil {
		riskVerifiable = false
	}

	freshRaw, refreshErr := source.refresher.FreshOpportunity(
		ctx,
		storedOpportunity.LongAccountID,
		storedOpportunity.LongInstrumentID,
		storedOpportunity.ShortAccountID,
		storedOpportunity.ShortInstrumentID,
	)
	refreshFailed := refreshErr != nil
	if refreshErr == nil {
		var fresh opportunityRecord
		if err := json.Unmarshal(freshRaw, &fresh); err != nil {
			return execution.PreviewInput{}, executionservice.ErrServiceUnavailable
		}
		if !opportunityRouteConsistent(fresh) ||
			!sameOpportunityRoute(storedOpportunity, fresh) {
			return execution.PreviewInput{}, executionservice.ErrPreviewMismatch
		}
		opportunity = fresh
	}

	originalBlockers := findings(opportunity.Blockers)
	if refreshFailed {
		originalBlockers = append(originalBlockers, execution.Finding{
			Code: execution.ReasonOriginalBlocker, Message: "opportunity refresh is unavailable",
		})
	}
	input := execution.PreviewInput{
		PreviewID: request.PreviewID, PrincipalID: scope.PrincipalID, Mode: scope.Mode,
		Operation: execution.OperationOpen, OpportunityID: opportunity.OpportunityID,
		AvailableCapitalUSDT: availableCapital,
		CapitalUSDT:          request.CapitalUSDT,
		Leverage:             request.Leverage,
		GrossAprPct:          opportunity.GrossAPRPct,
		DataObservedAt: oldestObservation(
			opportunity.ObservedAt,
			opportunity.CalculationInputs.LongLeg.MarketObservedAt,
			opportunity.CalculationInputs.ShortLeg.MarketObservedAt,
		),
		PreviewedAt:          source.now().UTC(),
		CapacityNotional:     opportunity.CapacityUSD,
		LiquidationBufferPct: opportunity.CalculationInputs.LiquidationBufferPct,
		StabilityPct:         opportunity.StabilityPct,
		RecentReversal:       hasRecentReversal(opportunity.Warnings),
		RiskVerifiable:       riskVerifiable,
		AccountVerifiable:    accountOK,
		RulesVerifiable:      rulesOK,
		OriginalBlockers:     originalBlockers,
		Long: previewLegFromReference(
			longIdentity, opportunity.CalculationInputs.LongLeg.MarkPrice, longInstrument,
		),
		Short: previewLegFromReference(
			shortIdentity, opportunity.CalculationInputs.ShortLeg.MarkPrice, shortInstrument,
		),
	}
	singleLegNotional := input.CapitalUSDT.Mul(decimal.NewFromInt(int64(input.Leverage)))
	input.FeesUSDT = singleLegNotional.Mul(opportunity.RoundTripFeeBPS).Div(tenThousand)
	input.SlippageUSDT = singleLegNotional.Mul(opportunity.EstimatedSlippageBPS).Div(tenThousand)
	input.SnapshotID = snapshotID(input)
	return input, nil
}

func (source *Source) freshClose(
	ctx context.Context,
	scope executionservice.Scope,
	request executionservice.PreviewRequest,
) (execution.PreviewInput, error) {
	raw, err := source.reader.Position(ctx, readScope(scope), request.PositionID)
	if err != nil {
		return execution.PreviewInput{}, mapReadError(err)
	}
	var position positionRecord
	if err := json.Unmarshal(raw, &position); err != nil {
		return execution.PreviewInput{}, executionservice.ErrServiceUnavailable
	}
	if request.PositionID == "" || position.PositionID != request.PositionID {
		return execution.PreviewInput{}, executionservice.ErrPreviewMismatch
	}
	positionStatus := strings.TrimSpace(position.Status)
	if positionStatus == "" {
		positionStatus = strings.TrimSpace(position.State)
	}
	capital := position.Capital
	if !capital.IsPositive() {
		capital = position.CapitalUSDT
	}
	netAPRPct := position.NetAPRPct
	if netAPRPct.IsZero() && !position.ExpectedNetAPRPct.IsZero() {
		netAPRPct = position.ExpectedNetAPRPct
	}

	mode := config.Mode(scope.Mode)
	longIdentity := legIdentity{
		LegIdentity: execution.LegIdentity{
			AccountID:        position.LongLeg.AccountID,
			InstrumentID:     position.LongLeg.InstrumentID,
			Venue:            position.LongLeg.Venue,
			VenueEnvironment: position.LongLeg.VenueEnvironment,
		},
		Symbol: position.LongLeg.Symbol,
	}
	shortIdentity := legIdentity{
		LegIdentity: execution.LegIdentity{
			AccountID:        position.ShortLeg.AccountID,
			InstrumentID:     position.ShortLeg.InstrumentID,
			Venue:            position.ShortLeg.Venue,
			VenueEnvironment: position.ShortLeg.VenueEnvironment,
		},
		Symbol: position.ShortLeg.Symbol,
	}
	accountOK, rulesOK, longReference, shortReference := source.referenceEvidence(
		ctx, mode, longIdentity, shortIdentity, false,
	)
	longQuantity, longPositionOK := source.currentQuantity(
		ctx, longIdentity, exchange.SideBuy,
	)
	shortQuantity, shortPositionOK := source.currentQuantity(
		ctx, shortIdentity, exchange.SideSell,
	)
	positionVerifiable := longPositionOK && shortPositionOK

	publicInstruments, marketListErr := source.market.Instruments(ctx)
	longMarket, longMarketOK := exactMarketInstrument(publicInstruments, mode, longIdentity)
	shortMarket, shortMarketOK := exactMarketInstrument(publicInstruments, mode, shortIdentity)
	if marketListErr != nil {
		longMarketOK = false
		shortMarketOK = false
	}
	rulesOK = rulesOK &&
		longMarketOK && shortMarketOK &&
		rulesAgree(longReference, longMarket) &&
		rulesAgree(shortReference, shortMarket)

	longEvidence, shortEvidence := source.twoLegMarketEvidence(ctx, longMarket, longMarketOK, shortMarket, shortMarketOK)
	longMark, longBBO, longBook, longMarketDataOK := longEvidence.mark, longEvidence.bbo, longEvidence.book, longEvidence.ok
	shortMark, shortBBO, shortBook, shortMarketDataOK := shortEvidence.mark, shortEvidence.bbo, shortEvidence.book, shortEvidence.ok
	rulesOK = rulesOK && longMarketDataOK && shortMarketDataOK

	originalBlockers := make([]execution.Finding, 0, 2)
	if positionStatus != "open" && positionStatus != "attention" {
		originalBlockers = append(originalBlockers, execution.Finding{
			Code: execution.ReasonOriginalBlocker, Message: "position is not open for close",
		})
	}
	expectedNotional := capital.Mul(decimal.NewFromInt(int64(position.Leverage)))
	if !position.SingleLegNotional.IsPositive() || !expectedNotional.Equal(position.SingleLegNotional) {
		originalBlockers = append(originalBlockers, execution.Finding{
			Code: execution.ReasonOriginalBlocker, Message: "position economics cannot be verified",
		})
	}

	input := execution.PreviewInput{
		PreviewID: request.PreviewID, PrincipalID: scope.PrincipalID, Mode: scope.Mode,
		Operation: execution.OperationClose, OpportunityID: position.OpportunityID,
		PositionID: request.PositionID, ParentExecutionID: position.OpeningExecutionID,
		LineageID:   position.LineageID,
		CapitalUSDT: capital, Leverage: position.Leverage,
		GrossAprPct: netAPRPct,
		DataObservedAt: oldestObservation(
			longMark.ObservedAt, longBBO.ObservedAt, longBook.ObservedAt,
			shortMark.ObservedAt, shortBBO.ObservedAt, shortBook.ObservedAt,
		),
		PreviewedAt:               source.now().UTC(),
		CapacityNotional:          closeCapacity(longBook, shortBook),
		StabilityPct:              position.StabilityPct,
		RiskVerifiable:            positionVerifiable,
		AccountVerifiable:         accountOK,
		RulesVerifiable:           rulesOK,
		OriginalBlockers:          originalBlockers,
		CurrentPositionVerifiable: positionVerifiable,
		CurrentLongQuantity:       longQuantity,
		CurrentShortQuantity:      shortQuantity,
		Long:                      closePreviewLeg(position.LongLeg, longReference, longMark),
		Short:                     closePreviewLeg(position.ShortLeg, shortReference, shortMark),
	}
	input.FeesUSDT = position.SingleLegNotional.
		Mul(position.RoundTripFeeBPS).
		Div(tenThousand).
		Div(two)
	input.SlippageUSDT = closeSlippage(longMark, longBBO, longQuantity, shortMark, shortBBO, shortQuantity)
	input.SnapshotID = snapshotID(input)
	return input, nil
}

func (source *Source) referenceEvidence(
	ctx context.Context,
	mode config.Mode,
	long legIdentity,
	short legIdentity,
	requireOpenCompatible bool,
) (bool, bool, boltertrader.InstrumentResource, boltertrader.InstrumentResource) {
	discovery, err := source.gateway.DiscoverAccounts(ctx)
	if err != nil || discovery.Mode != mode {
		return false, false, boltertrader.InstrumentResource{}, boltertrader.InstrumentResource{}
	}
	longAccountOK := exactReadyAccount(discovery.Accounts, mode, long)
	shortAccountOK := exactReadyAccount(discovery.Accounts, mode, short)
	if requireOpenCompatible {
		compatibleAccounts := compatibleWriteAccountSet(
			boltertrader.EvaluateTwoLegWriteAcceptance(discovery),
		)
		longAccountOK = longAccountOK && compatibleAccounts[long.AccountID]
		shortAccountOK = shortAccountOK && compatibleAccounts[short.AccountID]
	}
	longInstruments, longErr := source.gateway.ListInstruments(ctx, long.AccountID)
	shortInstruments, shortErr := source.gateway.ListInstruments(ctx, short.AccountID)
	longInstrument, longRulesOK := exactReferenceInstrument(longInstruments, mode, long)
	shortInstrument, shortRulesOK := exactReferenceInstrument(shortInstruments, mode, short)
	if longErr != nil {
		longRulesOK = false
	}
	if shortErr != nil {
		shortRulesOK = false
	}
	return longAccountOK && shortAccountOK,
		longRulesOK && shortRulesOK,
		longInstrument,
		shortInstrument
}

func (source *Source) availableCapital(
	ctx context.Context,
	longAccountID string,
	shortAccountID string,
	longAsset string,
	shortAsset string,
) (decimal.Decimal, bool) {
	longBalances, longErr := source.gateway.Balances(ctx, longAccountID)
	shortBalances, shortErr := source.gateway.Balances(ctx, shortAccountID)
	if longErr != nil || shortErr != nil || longAsset == "" || shortAsset == "" {
		return decimal.Zero, false
	}
	longAvailable, longOK := exactAvailableBalance(longBalances, longAccountID, longAsset)
	shortAvailable, shortOK := exactAvailableBalance(shortBalances, shortAccountID, shortAsset)
	if !longOK || !shortOK {
		return decimal.Zero, false
	}
	return decimal.Min(longAvailable, shortAvailable), true
}

func (source *Source) marketEvidence(
	ctx context.Context,
	instrument marketdata.Instrument,
	instrumentOK bool,
) (marketdata.MarkPrice, marketdata.BBO, marketdata.OrderBook, bool) {
	if !instrumentOK {
		return marketdata.MarkPrice{}, marketdata.BBO{}, marketdata.OrderBook{}, false
	}
	var (
		mark    marketdata.MarkPrice
		bbo     marketdata.BBO
		book    marketdata.OrderBook
		markErr error
		bboErr  error
		bookErr error
		group   sync.WaitGroup
	)
	group.Add(3)
	go func() {
		defer group.Done()
		mark, markErr = source.market.MarkPrice(ctx, instrument)
	}()
	go func() {
		defer group.Done()
		bbo, bboErr = source.market.BBO(ctx, instrument)
	}()
	go func() {
		defer group.Done()
		book, bookErr = source.market.OrderBook(ctx, marketdata.OrderBookRequest{
			Instrument: instrument, Limit: closeBookLimit,
		})
	}()
	group.Wait()
	valid := markErr == nil && bboErr == nil && bookErr == nil &&
		mark.Instrument.Key() == instrument.Key() &&
		bbo.Instrument.Key() == instrument.Key() &&
		book.Instrument.Key() == instrument.Key() &&
		mark.Price.IsPositive() &&
		bbo.Bid.Price.IsPositive() && bbo.Bid.Quantity.IsPositive() &&
		bbo.Ask.Price.IsPositive() && bbo.Ask.Quantity.IsPositive() &&
		!oldestObservation(mark.ObservedAt, bbo.ObservedAt, book.ObservedAt).IsZero()
	return mark, bbo, book, valid
}

type marketEvidenceResult struct {
	mark marketdata.MarkPrice
	bbo  marketdata.BBO
	book marketdata.OrderBook
	ok   bool
}

func (source *Source) twoLegMarketEvidence(
	ctx context.Context,
	long marketdata.Instrument,
	longOK bool,
	short marketdata.Instrument,
	shortOK bool,
) (marketEvidenceResult, marketEvidenceResult) {
	var longResult, shortResult marketEvidenceResult
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		longResult.mark, longResult.bbo, longResult.book, longResult.ok =
			source.marketEvidence(ctx, long, longOK)
	}()
	go func() {
		defer group.Done()
		shortResult.mark, shortResult.bbo, shortResult.book, shortResult.ok =
			source.marketEvidence(ctx, short, shortOK)
	}()
	group.Wait()
	return longResult, shortResult
}

func (source *Source) currentQuantity(
	ctx context.Context,
	identity legIdentity,
	side exchange.Side,
) (decimal.Decimal, bool) {
	positions, err := source.gateway.Positions(ctx, identity.AccountID, identity.InstrumentID)
	if err != nil {
		return decimal.Zero, false
	}
	total := decimal.Zero
	found := false
	for _, resource := range positions {
		if resource.AccountID != identity.AccountID || resource.InstrumentID != identity.InstrumentID {
			return decimal.Zero, false
		}
		if !resource.Position.Quantity.IsPositive() {
			continue
		}
		if resource.Position.Side != side {
			return decimal.Zero, false
		}
		total = total.Add(resource.Position.Quantity)
		found = true
	}
	return total, found
}

type findingRecord struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type opportunityRecord struct {
	OpportunityID        string          `json:"opportunityId"`
	Symbol               string          `json:"symbol"`
	LongVenue            string          `json:"longVenue"`
	ShortVenue           string          `json:"shortVenue"`
	LongAccountID        string          `json:"longAccountId"`
	ShortAccountID       string          `json:"shortAccountId"`
	LongInstrumentID     string          `json:"longInstrumentId"`
	ShortInstrumentID    string          `json:"shortInstrumentId"`
	GrossAPRPct          decimal.Decimal `json:"grossAprPct"`
	NetAPRPct            decimal.Decimal `json:"netAprPct"`
	StabilityPct         decimal.Decimal `json:"stabilityPct"`
	CapacityUSD          decimal.Decimal `json:"capacityUsd"`
	RoundTripFeeBPS      decimal.Decimal `json:"roundTripFeeBps"`
	EstimatedSlippageBPS decimal.Decimal `json:"estimatedSlippageBps"`
	ObservedAt           time.Time       `json:"observedAt"`
	Blockers             []findingRecord `json:"blockers"`
	Warnings             []findingRecord `json:"warnings"`
	CalculationInputs    struct {
		LiquidationBufferPct decimal.Decimal `json:"liquidationBufferPct"`
		LongLeg              opportunityLeg  `json:"longLeg"`
		ShortLeg             opportunityLeg  `json:"shortLeg"`
	} `json:"calculationInputs"`
}

type opportunityLeg struct {
	AccountID        string          `json:"accountId"`
	InstrumentID     string          `json:"instrumentId"`
	Venue            string          `json:"venue"`
	VenueEnvironment string          `json:"venueEnvironment"`
	VenueSymbol      string          `json:"venueSymbol"`
	MarkPrice        decimal.Decimal `json:"markPrice"`
	MarketObservedAt time.Time       `json:"marketObservedAt"`
}

type positionRecord struct {
	PositionID         string          `json:"positionId"`
	OpeningExecutionID string          `json:"openingExecutionId"`
	LineageID          string          `json:"lineageId"`
	OpportunityID      string          `json:"opportunityId"`
	Symbol             string          `json:"symbol"`
	Status             string          `json:"status"`
	Capital            decimal.Decimal `json:"capital"`
	CapitalUSDT        decimal.Decimal `json:"capitalUsdt"`
	SingleLegNotional  decimal.Decimal `json:"singleLegNotional"`
	Leverage           int             `json:"leverage"`
	NetAPRPct          decimal.Decimal `json:"netAprPct"`
	ExpectedNetAPRPct  decimal.Decimal `json:"expectedNetAprPct"`
	StabilityPct       decimal.Decimal `json:"stabilityPct"`
	RoundTripFeeBPS    decimal.Decimal `json:"roundTripFeeBps"`
	State              string          `json:"state"`
	LongLeg            positionLeg     `json:"longLeg"`
	ShortLeg           positionLeg     `json:"shortLeg"`
}

type positionLeg struct {
	AccountID        string `json:"accountId"`
	InstrumentID     string `json:"instrumentId"`
	Venue            string `json:"venue"`
	VenueEnvironment string `json:"venueEnvironment"`
	Symbol           string `json:"symbol"`
}

type legIdentity struct {
	execution.LegIdentity
	Symbol string
}

func exactReadyAccount(
	accounts []boltertrader.DiscoveredAccount,
	mode config.Mode,
	identity legIdentity,
) bool {
	matches := 0
	for _, account := range accounts {
		if account.AccountID != identity.AccountID {
			continue
		}
		matches++
		operations := account.Capability.Operations
		if account.Venue != identity.Venue ||
			account.Product != "perp" ||
			account.VenueEnvironment != boltertrader.VenueEnvironment(identity.VenueEnvironment) ||
			!mode.AllowsVenueEnvironment(string(account.VenueEnvironment)) ||
			!account.ReadReady ||
			!account.WriteReady ||
			!operations.Place ||
			!operations.Cancel ||
			!operations.OrderHistory ||
			!operations.OpenOrders ||
			!operations.Fills ||
			!operations.Balances ||
			!operations.Positions ||
			!operations.Account ||
			!operations.Funding ||
			!operations.AccountFundingPayments ||
			!operations.PrivateStream {
			return false
		}
	}
	return matches == 1
}

func compatibleWriteAccountSet(acceptance boltertrader.TwoLegWriteAcceptance) map[string]bool {
	accounts := make(map[string]bool, len(acceptance.CompatibleAccountIDs))
	if !acceptance.Ready {
		return accounts
	}
	for _, accountID := range acceptance.CompatibleAccountIDs {
		if accountID = strings.TrimSpace(accountID); accountID != "" {
			accounts[accountID] = true
		}
	}
	return accounts
}

func exactReferenceInstrument(
	instruments []boltertrader.InstrumentResource,
	mode config.Mode,
	identity legIdentity,
) (boltertrader.InstrumentResource, bool) {
	var match boltertrader.InstrumentResource
	matches := 0
	for _, instrument := range instruments {
		if instrument.InstrumentID != identity.InstrumentID {
			continue
		}
		matches++
		match = instrument
	}
	valid := matches == 1 &&
		match.Venue == identity.Venue &&
		match.VenueEnvironment == boltertrader.VenueEnvironment(identity.VenueEnvironment) &&
		mode.AllowsVenueEnvironment(string(match.VenueEnvironment)) &&
		match.Product == "perp" &&
		match.Symbol == identity.Symbol &&
		match.Rules.QuantityIncrement.IsPositive() &&
		!match.Rules.MinQuantity.IsNegative() &&
		(!match.Rules.MinNotional.Valid || !match.Rules.MinNotional.Value.IsNegative())
	return match, valid
}

func exactMarketInstrument(
	instruments []marketdata.Instrument,
	mode config.Mode,
	identity legIdentity,
) (marketdata.Instrument, bool) {
	var match marketdata.Instrument
	matches := 0
	for _, instrument := range instruments {
		if instrument.AccountID != identity.AccountID || instrument.InstrumentID != identity.InstrumentID {
			continue
		}
		matches++
		match = instrument
	}
	valid := matches == 1 &&
		match.Venue == identity.Venue &&
		match.VenueEnvironment == identity.VenueEnvironment &&
		mode.AllowsVenueEnvironment(match.VenueEnvironment) &&
		match.Product == "perp" &&
		match.Symbol == identity.Symbol &&
		match.Rules.QuantityIncrement.IsPositive() &&
		!match.Rules.MinQuantity.IsNegative() &&
		(!match.Rules.MinNotional.Valid || !match.Rules.MinNotional.Value.IsNegative())
	return match, valid
}

func exactAvailableBalance(
	balances []boltertrader.BalanceResource,
	accountID string,
	asset string,
) (decimal.Decimal, bool) {
	total := decimal.Zero
	matches := 0
	for _, resource := range balances {
		if resource.AccountID != accountID {
			return decimal.Zero, false
		}
		if resource.Balance.Asset != asset {
			continue
		}
		if resource.Balance.Available.IsNegative() {
			return decimal.Zero, false
		}
		matches++
		total = total.Add(resource.Balance.Available)
	}
	return total, matches == 1
}

func previewLegFromReference(
	identity legIdentity,
	markPrice decimal.Decimal,
	reference boltertrader.InstrumentResource,
) execution.PreviewLegInput {
	return execution.PreviewLegInput{
		LegIdentity: identity.LegIdentity, Symbol: identity.Symbol,
		GatewayInstrumentID: identity.InstrumentID,
		MarkPrice:           markPrice, QuantityIncrement: reference.Rules.QuantityIncrement,
		MinQuantity: reference.Rules.MinQuantity, MinNotional: minNotional(reference),
	}
}

func closePreviewLeg(
	leg positionLeg,
	reference boltertrader.InstrumentResource,
	mark marketdata.MarkPrice,
) execution.PreviewLegInput {
	return execution.PreviewLegInput{
		LegIdentity: execution.LegIdentity{
			AccountID:        leg.AccountID,
			Venue:            leg.Venue,
			InstrumentID:     leg.InstrumentID,
			VenueEnvironment: leg.VenueEnvironment,
		},
		Symbol: leg.Symbol, GatewayInstrumentID: leg.InstrumentID,
		MarkPrice: mark.Price, QuantityIncrement: reference.Rules.QuantityIncrement,
		MinQuantity: reference.Rules.MinQuantity, MinNotional: minNotional(reference),
	}
}

func minNotional(instrument boltertrader.InstrumentResource) decimal.Decimal {
	if !instrument.Rules.MinNotional.Valid {
		return decimal.Zero
	}
	return instrument.Rules.MinNotional.Value
}

func quoteAsset(instrument boltertrader.InstrumentResource) string {
	if instrument.QuoteAsset != "" {
		return instrument.QuoteAsset
	}
	return instrument.Settlement
}

func rulesAgree(reference boltertrader.InstrumentResource, public marketdata.Instrument) bool {
	if !reference.Rules.QuantityIncrement.Equal(public.Rules.QuantityIncrement) ||
		!reference.Rules.MinQuantity.Equal(public.Rules.MinQuantity) ||
		reference.Rules.MinNotional.Valid != public.Rules.MinNotional.Valid {
		return false
	}
	return !reference.Rules.MinNotional.Valid ||
		reference.Rules.MinNotional.Value.Equal(public.Rules.MinNotional.Value)
}

func closeCapacity(long marketdata.OrderBook, short marketdata.OrderBook) decimal.Decimal {
	longCapacity := levelNotional(long.Bids)
	shortCapacity := levelNotional(short.Asks)
	if !longCapacity.IsPositive() || !shortCapacity.IsPositive() {
		return decimal.Zero
	}
	return decimal.Min(longCapacity, shortCapacity)
}

func levelNotional(levels []marketdata.Level) decimal.Decimal {
	total := decimal.Zero
	for _, level := range levels {
		if !level.Price.IsPositive() || !level.Quantity.IsPositive() {
			return decimal.Zero
		}
		total = total.Add(level.Price.Mul(level.Quantity))
	}
	return total
}

func closeSlippage(
	longMark marketdata.MarkPrice,
	longBBO marketdata.BBO,
	longQuantity decimal.Decimal,
	shortMark marketdata.MarkPrice,
	shortBBO marketdata.BBO,
	shortQuantity decimal.Decimal,
) decimal.Decimal {
	longLoss := decimal.Max(longMark.Price.Sub(longBBO.Bid.Price), decimal.Zero).Mul(longQuantity)
	shortLoss := decimal.Max(shortBBO.Ask.Price.Sub(shortMark.Price), decimal.Zero).Mul(shortQuantity)
	return longLoss.Add(shortLoss)
}

func findings(records []findingRecord) []execution.Finding {
	result := make([]execution.Finding, 0, len(records))
	for _, record := range records {
		result = append(result, execution.Finding{
			Code: execution.ReasonCode(record.Code), Message: record.Message,
		})
	}
	return result
}

func hasRecentReversal(records []findingRecord) bool {
	for _, record := range records {
		if strings.Contains(strings.ToLower(record.Code), "reversal") {
			return true
		}
	}
	return false
}

func opportunityRouteConsistent(opportunity opportunityRecord) bool {
	return opportunity.CalculationInputs.LongLeg.AccountID == opportunity.LongAccountID &&
		opportunity.CalculationInputs.LongLeg.InstrumentID == opportunity.LongInstrumentID &&
		opportunity.CalculationInputs.LongLeg.Venue == opportunity.LongVenue &&
		opportunity.CalculationInputs.LongLeg.VenueEnvironment != "" &&
		opportunity.CalculationInputs.LongLeg.VenueSymbol == opportunity.Symbol &&
		opportunity.CalculationInputs.ShortLeg.AccountID == opportunity.ShortAccountID &&
		opportunity.CalculationInputs.ShortLeg.InstrumentID == opportunity.ShortInstrumentID &&
		opportunity.CalculationInputs.ShortLeg.Venue == opportunity.ShortVenue &&
		opportunity.CalculationInputs.ShortLeg.VenueEnvironment != "" &&
		opportunity.CalculationInputs.ShortLeg.VenueSymbol == opportunity.Symbol
}

func sameOpportunityRoute(stored, fresh opportunityRecord) bool {
	return fresh.OpportunityID == stored.OpportunityID &&
		fresh.Symbol == stored.Symbol &&
		fresh.LongAccountID == stored.LongAccountID &&
		fresh.LongInstrumentID == stored.LongInstrumentID &&
		fresh.LongVenue == stored.LongVenue &&
		fresh.ShortAccountID == stored.ShortAccountID &&
		fresh.ShortInstrumentID == stored.ShortInstrumentID &&
		fresh.ShortVenue == stored.ShortVenue &&
		fresh.CalculationInputs.LongLeg.AccountID == stored.CalculationInputs.LongLeg.AccountID &&
		fresh.CalculationInputs.LongLeg.InstrumentID == stored.CalculationInputs.LongLeg.InstrumentID &&
		fresh.CalculationInputs.LongLeg.Venue == stored.CalculationInputs.LongLeg.Venue &&
		fresh.CalculationInputs.LongLeg.VenueEnvironment == stored.CalculationInputs.LongLeg.VenueEnvironment &&
		fresh.CalculationInputs.LongLeg.VenueSymbol == stored.CalculationInputs.LongLeg.VenueSymbol &&
		fresh.CalculationInputs.ShortLeg.AccountID == stored.CalculationInputs.ShortLeg.AccountID &&
		fresh.CalculationInputs.ShortLeg.InstrumentID == stored.CalculationInputs.ShortLeg.InstrumentID &&
		fresh.CalculationInputs.ShortLeg.Venue == stored.CalculationInputs.ShortLeg.Venue &&
		fresh.CalculationInputs.ShortLeg.VenueEnvironment == stored.CalculationInputs.ShortLeg.VenueEnvironment &&
		fresh.CalculationInputs.ShortLeg.VenueSymbol == stored.CalculationInputs.ShortLeg.VenueSymbol
}

func oldestObservation(values ...time.Time) time.Time {
	var oldest time.Time
	for _, value := range values {
		if value.IsZero() {
			return time.Time{}
		}
		value = value.UTC()
		if oldest.IsZero() || value.Before(oldest) {
			oldest = value
		}
	}
	return oldest
}

func snapshotID(input execution.PreviewInput) string {
	input.PreviewID = ""
	input.SnapshotID = ""
	input.PreviewedAt = time.Time{}
	body, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readScope(scope executionservice.Scope) readmodel.Scope {
	return readmodel.Scope{PrincipalID: scope.PrincipalID, Mode: scope.Mode}
}

func mapReadError(err error) error {
	if errors.Is(err, readmodel.ErrNotFound) {
		return executionservice.ErrNotFound
	}
	return executionservice.ErrServiceUnavailable
}
