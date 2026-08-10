package ledgerprojection

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/shopspring/decimal"
)

type ConversionMetadata struct {
	ConversionSource string    `json:"conversionSource"`
	ConversionAsOf   time.Time `json:"conversionAsOf"`
	RoundingMode     string    `json:"roundingMode"`
	Scale            int       `json:"scale"`
	FormulaVersion   string    `json:"formulaVersion"`
}

func (m ConversionMetadata) MarshalJSON() ([]byte, error) {
	type alias ConversionMetadata
	return json.Marshal(alias(m))
}

func (m *ConversionMetadata) UnmarshalJSON(body []byte) error {
	type alias ConversionMetadata
	var decoded alias
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}
	*m = ConversionMetadata(decoded)
	return nil
}

type Residual struct {
	LegQuantity            decimal.Decimal `json:"legQuantity"`
	ReportingAssetNotional decimal.Decimal `json:"reportingAssetNotional"`
	OpenOrderCount         int             `json:"openOrderCount"`
	PendingCompensation    int             `json:"pendingCompensation"`
	BlockerClass           string          `json:"blockerClass"`
}

type Watermark struct {
	Status          ledgerfacts.CompletenessStatus `json:"status"`
	Verified        bool                           `json:"verified"`
	CompleteThrough time.Time                      `json:"completeThrough"`
}

type factSet struct {
	Scope          ledgerfacts.Scope
	PositionID     string
	ReportingAsset string
	Closed         bool
	Verified       bool
	AsOf           time.Time
	TradingPnl     ledgerfacts.Money
	UnrealizedPnl  ledgerfacts.Money
	Funding        ledgerfacts.Money
	Fees           ledgerfacts.Money
	Residual       Residual
	Watermark      Watermark
	Conversion     ConversionMetadata
	Revised        bool
}

type AuthoritativePortfolioReplay struct {
	PrincipalID    string
	Mode           string
	ReportingAsset string
	Scopes         []ledgerfacts.Scope
	Entries        []ledgerfacts.ReplayEntry
}

type ClosedPositionProjection struct {
	PositionID            string
	Scope                 ledgerfacts.Scope
	ReportingAsset        string
	AsOf                  time.Time
	RealizedTradingPnl    ledgerfacts.Money
	UnrealizedTradingPnl  ledgerfacts.Money
	ActualFundingCashFlow ledgerfacts.Money
	Fees                  ledgerfacts.Money
	NetPnl                ledgerfacts.Money
	ClosedTradingUPnL     ledgerfacts.Money
	Verified              bool
	VerificationStatus    string
	Residual              Residual
	Watermark             Watermark
	Conversion            ConversionMetadata
}

type PortfolioProjection struct {
	PrincipalID              string
	Mode                     string
	ReportingAsset           string
	AsOf                     time.Time
	CumulativeRealizedNetPnl ledgerfacts.Money
	CurrentOpenUnrealizedPnl ledgerfacts.Money
	TotalNetPnl              ledgerfacts.Money
	FundingTotal             ledgerfacts.Money
	FeeTotal                 ledgerfacts.Money
	OpenPositionCount        int
	ClosedPositionCount      int
	VerificationStatus       string
	Residual                 Residual
	Watermark                Watermark
	Conversion               ConversionMetadata
}

type Store interface {
	ReplaceClosedPosition(context.Context, ClosedPositionProjection) error
	ReplacePortfolio(context.Context, PortfolioProjection) error
	ClosedPositions(string, string, string) []ClosedPositionProjection
	ClosedPosition(string) (ClosedPositionProjection, bool)
	Portfolio(string, string, string) (PortfolioProjection, bool)
}

type Projector struct {
	store Store
}

func NewProjector(store Store) *Projector {
	return &Projector{store: store}
}

func FeeExpense(value ledgerfacts.Money) ledgerfacts.Money {
	if value.Amount.IsNegative() {
		return value
	}
	value.Amount = value.Amount.Neg()
	return value
}

func RebateIncome(value ledgerfacts.Money) ledgerfacts.Money {
	if value.Amount.IsNegative() {
		value.Amount = value.Amount.Neg()
	}
	return value
}

func AlreadySigned(value ledgerfacts.Money) ledgerfacts.Money {
	return value
}

func (p *Projector) RebuildPortfolioFromReplay(ctx context.Context, bundle AuthoritativePortfolioReplay) ([]ClosedPositionProjection, PortfolioProjection, error) {
	if p == nil || p.store == nil {
		return nil, PortfolioProjection{}, errors.New("ledger projection store is required")
	}
	if err := validateAuthoritativeBundle(bundle); err != nil {
		return nil, PortfolioProjection{}, err
	}
	return p.rebuildValidatedPortfolio(ctx, bundle)
}

func (p *Projector) rebuildValidatedPortfolio(ctx context.Context, bundle AuthoritativePortfolioReplay) ([]ClosedPositionProjection, PortfolioProjection, error) {
	entries := append([]ledgerfacts.ReplayEntry(nil), bundle.Entries...)
	sortReplayEntries(entries)
	heads := supersessionHeads(entries)

	positionScopes := map[string]map[string]ledgerfacts.PositionSnapshotFact{}
	lineagesByFactID := map[string]ledgerfacts.LineageFact{}
	lineagesByPosition := map[string][]ledgerfacts.LineageFact{}
	revisedByPosition := map[string]bool{}
	watermarksByScope := map[string][]ledgerfacts.SyncWatermark{}
	watermarksByID := map[string]ledgerfacts.SyncWatermark{}

	for _, entry := range entries {
		if id := entryFactID(entry); id != "" && !heads[id] {
			continue
		}
		switch {
		case entry.Position != nil:
			item := *entry.Position
			if item.PositionID == "" {
				return nil, PortfolioProjection{}, errors.New("position id is required")
			}
			if positionScopes[item.PositionID] == nil {
				positionScopes[item.PositionID] = map[string]ledgerfacts.PositionSnapshotFact{}
			}
			positionScopes[item.PositionID][exactScopeKey(item.Scope)] = item
		case entry.Watermark != nil:
			item := *entry.Watermark
			watermarksByScope[exactScopeKey(item.Scope)] = append(watermarksByScope[exactScopeKey(item.Scope)], item)
			if item.FactID != "" {
				if prior, ok := watermarksByID[item.FactID]; ok && !sameExactScope(prior.Scope, item.Scope) {
					return nil, PortfolioProjection{}, errors.New("watermark fact id was replayed for multiple scopes")
				}
				watermarksByID[item.FactID] = item
			}
		case entry.Lineage != nil:
			if entry.Lineage.FactID == "" || entry.Lineage.LineageID == "" || entry.Lineage.PositionID == "" {
				return nil, PortfolioProjection{}, errors.New("lineage fact identity is required")
			}
			lineagesByFactID[entry.Lineage.FactID] = *entry.Lineage
		}
	}
	if len(positionScopes) == 0 {
		return nil, PortfolioProjection{}, errors.New("position snapshot fact is required")
	}
	for _, lineage := range lineagesByFactID {
		if _, ok := positionScopes[lineage.PositionID]; !ok {
			return nil, PortfolioProjection{}, errors.New("lineage position attribution does not match replay positions")
		}
		lineagesByPosition[lineage.PositionID] = append(lineagesByPosition[lineage.PositionID], lineage)
	}
	for positionID := range positionScopes {
		if len(lineagesByPosition[positionID]) == 0 {
			return nil, PortfolioProjection{}, errors.New("position snapshot requires lineage evidence")
		}
	}
	for _, entry := range entries {
		if entry.RevisionFact == nil || (entryFactID(entry) != "" && !heads[entryFactID(entry)]) {
			continue
		}
		if _, ok := lineagesByFactID[entry.RevisionFact.LineageFactID]; !ok {
			return nil, PortfolioProjection{}, errors.New("lineage revision references undeclared lineage")
		}
	}

	targetWatermarks := map[string]map[string]resolvedWatermark{}
	for positionID, scopes := range positionScopes {
		targetWatermarks[positionID] = map[string]resolvedWatermark{}
		for scopeKey, snapshot := range scopes {
			watermark, err := resolvePositionWatermark(snapshot, watermarksByID, watermarksByScope)
			if err != nil {
				return nil, PortfolioProjection{}, err
			}
			targetWatermarks[positionID][scopeKey] = watermark
		}
	}
	collapsedScopes, err := collapseLegacyEquivalentCloseRetries(entries, heads, positionScopes, lineagesByFactID, lineagesByPosition, targetWatermarks)
	if err != nil {
		return nil, PortfolioProjection{}, err
	}
	revisedByPosition = replayRevisedPositions(entries, lineagesByFactID, collapsedScopes)

	fundingByPosition := map[string]decimal.Decimal{}
	feesByPosition := map[string]decimal.Decimal{}
	fundingEvidence := map[string]map[string]factEvidence{}
	fillEvidence := map[string]map[string]factEvidence{}
	for _, entry := range entries {
		if id := entryFactID(entry); id != "" && !heads[id] {
			continue
		}
		switch {
		case entry.Funding != nil:
			fact := *entry.Funding
			positionID, err := attributedGroupedPositionID(fact.PositionID, positionScopes)
			if err != nil {
				return nil, PortfolioProjection{}, err
			}
			if fact.SupersedesFactID != "" {
				revisedByPosition[positionID] = true
			}
			scopeKey := exactScopeKey(fact.Scope)
			snapshot, ok := positionScopes[positionID][scopeKey]
			if !ok {
				if collapsedScopes[positionID][scopeKey] {
					continue
				}
				return nil, PortfolioProjection{}, errors.New("funding fact scope has no position snapshot")
			}
			if fact.Payment.Asset != snapshot.ReportingAsset {
				return nil, PortfolioProjection{}, errors.New("funding payment asset does not match reporting asset")
			}
			bound, err := factUsesResolvedWatermark(fact.SyncWatermarkID, fact.Scope, targetWatermarks[positionID][scopeKey], watermarksByID)
			if err != nil {
				return nil, PortfolioProjection{}, err
			}
			fundingEvidence[positionID] = addFactEvidence(fundingEvidence[positionID], scopeKey, bound)
			fundingByPosition[positionID] = fundingByPosition[positionID].Add(fact.Payment.Amount)
		case entry.Fill != nil:
			fact := *entry.Fill
			positionID, err := attributedGroupedPositionID(fact.PositionID, positionScopes)
			if err != nil {
				return nil, PortfolioProjection{}, err
			}
			if fact.SupersedesFactID != "" {
				revisedByPosition[positionID] = true
			}
			scopeKey := exactScopeKey(fact.Scope)
			snapshot, ok := positionScopes[positionID][scopeKey]
			if !ok {
				if collapsedScopes[positionID][scopeKey] {
					continue
				}
				return nil, PortfolioProjection{}, errors.New("fill fact scope has no position snapshot")
			}
			if err := validateFillReportingAsset(fact, snapshot.ReportingAsset); err != nil {
				return nil, PortfolioProjection{}, err
			}
			bound, err := factUsesResolvedWatermark(fact.SyncWatermarkID, fact.Scope, targetWatermarks[positionID][scopeKey], watermarksByID)
			if err != nil {
				return nil, PortfolioProjection{}, err
			}
			fillEvidence[positionID] = addFactEvidence(fillEvidence[positionID], scopeKey, bound)
			fee := FeeExpense(fact.Fee).Amount.Add(RebateIncome(fact.Rebate).Amount)
			if fact.AlreadySigned {
				fee = fact.Fee.Amount.Add(fact.Rebate.Amount)
			}
			feesByPosition[positionID] = feesByPosition[positionID].Add(fee)
		}
	}

	portfolio := newPortfolioProjection(bundle)
	positionIDs := make([]string, 0, len(positionScopes))
	for positionID := range positionScopes {
		positionIDs = append(positionIDs, positionID)
	}
	sort.Strings(positionIDs)
	closedItems := make([]ClosedPositionProjection, 0, len(positionIDs))
	for _, positionID := range positionIDs {
		facts, err := aggregatePositionFacts(
			bundle,
			positionID,
			positionScopes[positionID],
			lineagesByPosition[positionID],
			targetWatermarks[positionID],
			fundingEvidence[positionID],
			fillEvidence[positionID],
			fundingByPosition[positionID],
			feesByPosition[positionID],
			revisedByPosition[positionID],
		)
		if err != nil {
			return nil, PortfolioProjection{}, err
		}
		portfolio.Conversion, err = mergePortfolioConversion(portfolio.Conversion, facts.Conversion)
		if err != nil {
			return nil, PortfolioProjection{}, err
		}
		portfolio.FundingTotal.Amount = portfolio.FundingTotal.Amount.Add(facts.Funding.Amount)
		portfolio.FeeTotal.Amount = portfolio.FeeTotal.Amount.Add(facts.Fees.Amount)
		portfolio.Residual = mergeResidual(portfolio.Residual, facts.Residual)
		portfolio.Watermark = mergeWatermark(portfolio.Watermark, facts.Watermark)
		if facts.AsOf.After(portfolio.AsOf) {
			portfolio.AsOf = facts.AsOf
		}
		if facts.Closed {
			closed, err := buildClosedProjection(facts)
			if err != nil {
				return nil, PortfolioProjection{}, err
			}
			closedItems = append(closedItems, closed)
			portfolio.CumulativeRealizedNetPnl.Amount = portfolio.CumulativeRealizedNetPnl.Amount.Add(closed.NetPnl.Amount)
			portfolio.VerificationStatus = mergeStatus(portfolio.VerificationStatus, closed.VerificationStatus)
			continue
		}
		portfolio.CurrentOpenUnrealizedPnl.Amount = portfolio.CurrentOpenUnrealizedPnl.Amount.Add(facts.UnrealizedPnl.Amount)
		portfolio.VerificationStatus = mergeStatus(portfolio.VerificationStatus, verificationStatus(facts))
	}
	portfolio.OpenPositionCount, portfolio.ClosedPositionCount = logicalLineageCounts(lineagesByFactID)
	portfolio.TotalNetPnl.Amount = portfolio.CumulativeRealizedNetPnl.Amount.Add(portfolio.CurrentOpenUnrealizedPnl.Amount)
	for _, closed := range closedItems {
		if err := p.store.ReplaceClosedPosition(ctx, closed); err != nil {
			return nil, PortfolioProjection{}, err
		}
	}
	if err := p.store.ReplacePortfolio(ctx, portfolio); err != nil {
		return nil, PortfolioProjection{}, err
	}
	return closedItems, portfolio, nil
}

type resolvedWatermark struct {
	FactID    string
	Scope     ledgerfacts.Scope
	SourceRef string
	Cursor    string
	Revision  string
	Watermark Watermark
}

type factEvidence struct {
	Seen     bool
	AllBound bool
}

type legacyCloseRetryBundle struct {
	positionID    string
	scopeKey      string
	groupKey      string
	lineageFactID string
	sequenceID    int64
	signature     legacyCloseRetrySignature
}

type legacyCloseRetrySignature struct {
	Position  []string
	Lineage   []string
	Watermark []string
	Fills     []string
	Funding   []string
	Revisions []string
}

func collapseLegacyEquivalentCloseRetries(
	entries []ledgerfacts.ReplayEntry,
	heads map[string]bool,
	positionScopes map[string]map[string]ledgerfacts.PositionSnapshotFact,
	lineagesByFactID map[string]ledgerfacts.LineageFact,
	lineagesByPosition map[string][]ledgerfacts.LineageFact,
	watermarks map[string]map[string]resolvedWatermark,
) (map[string]map[string]bool, error) {
	fillsByPositionScope, fundingByPositionScope := legacyCloseRetryEvidence(entries, heads)
	revisionsByLineageFactID := legacyCloseRetryRevisions(entries, heads)
	candidatesByGroup := map[string][]legacyCloseRetryBundle{}
	for positionID, scopes := range positionScopes {
		lineagesBySnapshot := map[string][]ledgerfacts.LineageFact{}
		for _, lineage := range lineagesByPosition[positionID] {
			key := legacyCloseRetryPairingKey(lineage.Scope, lineage.PositionID, lineage.Provenance.SourceRef, lineage.Provenance.SourceRevision)
			lineagesBySnapshot[key] = append(lineagesBySnapshot[key], lineage)
		}
		for scopeKey, snapshot := range scopes {
			if !snapshot.Closed || snapshot.Provenance.SourceRef == "" {
				continue
			}
			lineages := lineagesBySnapshot[legacyCloseRetryPairingKey(snapshot.Scope, positionID, snapshot.Provenance.SourceRef, snapshot.Provenance.SourceRevision)]
			if len(lineages) != 1 {
				continue
			}
			lineage := lineages[0]
			if lineage.Active || lineage.ClosedAt.IsZero() || lineage.PositionID != positionID ||
				lineage.LineageID == "" || lineage.Provenance.SourceRef != snapshot.Provenance.SourceRef ||
				!sameIdentityScope(lineage.Scope, snapshot.Scope) {
				continue
			}
			groupKey := identityScopeKey(snapshot.Scope) + "\x00" + positionID + "\x00" + snapshot.Provenance.SourceRef
			candidatesByGroup[groupKey] = append(candidatesByGroup[groupKey], legacyCloseRetryBundle{
				positionID:    positionID,
				scopeKey:      scopeKey,
				groupKey:      groupKey,
				lineageFactID: lineage.FactID,
				sequenceID:    legacyCloseRetrySequence(snapshot.SequenceID, lineage.SequenceID),
				signature: legacyCloseRetrySignature{
					Position:  legacyCloseRetryPositionSignature(snapshot),
					Lineage:   legacyCloseRetryLineageSignature(lineage),
					Watermark: legacyCloseRetryWatermarkSignature(watermarks[positionID][scopeKey]),
					Fills:     fillsByPositionScope[positionID][scopeKey],
					Funding:   fundingByPositionScope[positionID][scopeKey],
					Revisions: revisionsByLineageFactID[lineage.FactID],
				},
			})
		}
	}

	collapsed := map[string]map[string]bool{}
	collapsedLineages := map[string]bool{}
	for _, candidates := range candidatesByGroup {
		if len(candidates) < 2 {
			continue
		}
		keeper := candidates[0]
		for _, candidate := range candidates[1:] {
			if candidate.sequenceID < keeper.sequenceID ||
				(candidate.sequenceID == keeper.sequenceID && candidate.scopeKey < keeper.scopeKey) {
				keeper = candidate
			}
		}
		for _, candidate := range candidates {
			if !sameLegacyCloseRetrySignature(keeper.signature, candidate.signature) {
				return nil, errors.New("legacy close retry bundle differs economically")
			}
		}
		for _, candidate := range candidates {
			if candidate.scopeKey == keeper.scopeKey {
				continue
			}
			if collapsed[candidate.positionID] == nil {
				collapsed[candidate.positionID] = map[string]bool{}
			}
			collapsed[candidate.positionID][candidate.scopeKey] = true
			delete(positionScopes[candidate.positionID], candidate.scopeKey)
			delete(watermarks[candidate.positionID], candidate.scopeKey)
			delete(lineagesByFactID, candidate.lineageFactID)
			collapsedLineages[candidate.lineageFactID] = true
		}
	}
	for positionID, collapsedByScope := range collapsed {
		lineages := lineagesByPosition[positionID]
		filtered := lineages[:0]
		for _, lineage := range lineages {
			if !collapsedByScope[exactScopeKey(lineage.Scope)] && !collapsedLineages[lineage.FactID] {
				filtered = append(filtered, lineage)
			}
		}
		lineagesByPosition[positionID] = filtered
	}
	return collapsed, nil
}

func legacyCloseRetryEvidence(entries []ledgerfacts.ReplayEntry, heads map[string]bool) (map[string]map[string][]string, map[string]map[string][]string) {
	fillsByPositionScope := map[string]map[string][]string{}
	fundingByPositionScope := map[string]map[string][]string{}
	for _, entry := range entries {
		if id := entryFactID(entry); id != "" && !heads[id] {
			continue
		}
		switch {
		case entry.Fill != nil:
			fact := *entry.Fill
			if fact.PositionID == "" {
				continue
			}
			addLegacyCloseRetryEvidence(fillsByPositionScope, fact.PositionID, exactScopeKey(fact.Scope), legacyCloseRetryFillSignature(fact))
		case entry.Funding != nil:
			fact := *entry.Funding
			if fact.PositionID == "" {
				continue
			}
			addLegacyCloseRetryEvidence(fundingByPositionScope, fact.PositionID, exactScopeKey(fact.Scope), legacyCloseRetryFundingSignature(fact))
		}
	}
	for _, byScope := range fillsByPositionScope {
		for scopeKey := range byScope {
			sort.Strings(byScope[scopeKey])
		}
	}
	for _, byScope := range fundingByPositionScope {
		for scopeKey := range byScope {
			sort.Strings(byScope[scopeKey])
		}
	}
	return fillsByPositionScope, fundingByPositionScope
}

func legacyCloseRetryRevisions(entries []ledgerfacts.ReplayEntry, heads map[string]bool) map[string][]string {
	byLineageFactID := map[string][]string{}
	for _, entry := range entries {
		if entry.RevisionFact == nil || (entryFactID(entry) != "" && !heads[entryFactID(entry)]) {
			continue
		}
		fact := *entry.RevisionFact
		byLineageFactID[fact.LineageFactID] = append(byLineageFactID[fact.LineageFactID], strings.Join(legacyCloseRetryRevisionSignature(fact), "\x1f"))
	}
	for lineageFactID := range byLineageFactID {
		sort.Strings(byLineageFactID[lineageFactID])
	}
	return byLineageFactID
}

func addLegacyCloseRetryEvidence(byPositionScope map[string]map[string][]string, positionID, scopeKey string, signature []string) {
	if byPositionScope[positionID] == nil {
		byPositionScope[positionID] = map[string][]string{}
	}
	byPositionScope[positionID][scopeKey] = append(byPositionScope[positionID][scopeKey], strings.Join(signature, "\x1f"))
}

func legacyCloseRetrySequence(values ...int64) int64 {
	lowest := int64(0)
	for _, value := range values {
		if value == 0 {
			continue
		}
		if lowest == 0 || value < lowest {
			lowest = value
		}
	}
	return lowest
}

func sameLegacyCloseRetrySignature(left, right legacyCloseRetrySignature) bool {
	return sameStringSlice(left.Position, right.Position) &&
		sameStringSlice(left.Lineage, right.Lineage) &&
		sameStringSlice(left.Watermark, right.Watermark) &&
		sameStringSlice(left.Fills, right.Fills) &&
		sameStringSlice(left.Funding, right.Funding) &&
		sameStringSlice(left.Revisions, right.Revisions)
}

func replayRevisedPositions(entries []ledgerfacts.ReplayEntry, lineagesByFactID map[string]ledgerfacts.LineageFact, collapsedScopes map[string]map[string]bool) map[string]bool {
	revised := map[string]bool{}
	for _, entry := range entries {
		if entrySupersedes(entry) == "" && (entry.RevisionFact == nil || entry.RevisionFact.Status != "revised") {
			continue
		}
		switch {
		case entry.Position != nil:
			positionID := entry.Position.PositionID
			if collapsedScopes[positionID][exactScopeKey(entry.Position.Scope)] {
				continue
			}
			revised[positionID] = true
		case entry.RevisionFact != nil:
			lineage, ok := lineagesByFactID[entry.RevisionFact.LineageFactID]
			if !ok {
				continue
			}
			revised[lineage.PositionID] = true
		}
	}
	return revised
}

func sortReplayEntries(entries []ledgerfacts.ReplayEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		left := entries[i]
		right := entries[j]
		if comparison := compareReplayRevision(left.Revision, right.Revision); comparison != 0 {
			return comparison < 0
		}
		if left.Marker != right.Marker {
			return left.Marker < right.Marker
		}
		if left.SequenceID != right.SequenceID {
			return left.SequenceID < right.SequenceID
		}
		if leftID, rightID := entryFactID(left), entryFactID(right); leftID != rightID {
			return leftID < rightID
		}
		leftScope, _ := replayEntryScope(left)
		rightScope, _ := replayEntryScope(right)
		return exactScopeKey(leftScope) < exactScopeKey(rightScope)
	})
}

func compareReplayRevision(left, right string) int {
	leftNumber, leftErr := strconv.ParseInt(strings.TrimSpace(left), 10, 64)
	rightNumber, rightErr := strconv.ParseInt(strings.TrimSpace(right), 10, 64)
	if leftErr == nil && rightErr == nil {
		switch {
		case leftNumber < rightNumber:
			return -1
		case leftNumber > rightNumber:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(left, right)
}

func newPortfolioProjection(bundle AuthoritativePortfolioReplay) PortfolioProjection {
	return PortfolioProjection{
		PrincipalID:              bundle.PrincipalID,
		Mode:                     bundle.Mode,
		ReportingAsset:           bundle.ReportingAsset,
		CumulativeRealizedNetPnl: ledgerfacts.Money{Asset: bundle.ReportingAsset},
		CurrentOpenUnrealizedPnl: ledgerfacts.Money{Asset: bundle.ReportingAsset},
		TotalNetPnl:              ledgerfacts.Money{Asset: bundle.ReportingAsset},
		FundingTotal:             ledgerfacts.Money{Asset: bundle.ReportingAsset},
		FeeTotal:                 ledgerfacts.Money{Asset: bundle.ReportingAsset},
		VerificationStatus:       "verified",
	}
}

func attributedGroupedPositionID(positionID string, positions map[string]map[string]ledgerfacts.PositionSnapshotFact) (string, error) {
	if positionID != "" {
		if _, ok := positions[positionID]; ok {
			return positionID, nil
		}
		return "", errors.New("fact position attribution does not match replay positions")
	}
	if len(positions) != 1 {
		return "", errors.New("fact position attribution is required")
	}
	for id := range positions {
		return id, nil
	}
	return "", errors.New("position snapshot fact is required")
}

func addFactEvidence(byScope map[string]factEvidence, scopeKey string, bound bool) map[string]factEvidence {
	if byScope == nil {
		byScope = map[string]factEvidence{}
	}
	evidence := byScope[scopeKey]
	if !evidence.Seen {
		evidence.AllBound = bound
	} else {
		evidence.AllBound = evidence.AllBound && bound
	}
	evidence.Seen = true
	byScope[scopeKey] = evidence
	return byScope
}

func factUsesResolvedWatermark(syncWatermarkID string, scope ledgerfacts.Scope, target resolvedWatermark, byID map[string]ledgerfacts.SyncWatermark) (bool, error) {
	if syncWatermarkID == "" {
		return false, nil
	}
	watermark, ok := byID[syncWatermarkID]
	if !ok {
		return false, errors.New("fact sync watermark id was not replayed")
	}
	if !sameExactScope(scope, watermark.Scope) {
		return false, errors.New("fact sync watermark scope mismatch")
	}
	return target.FactID != "" && syncWatermarkID == target.FactID, nil
}

func aggregatePositionFacts(
	bundle AuthoritativePortfolioReplay,
	positionID string,
	snapshots map[string]ledgerfacts.PositionSnapshotFact,
	lineages []ledgerfacts.LineageFact,
	watermarks map[string]resolvedWatermark,
	fundingByScope map[string]factEvidence,
	fillByScope map[string]factEvidence,
	funding decimal.Decimal,
	fees decimal.Decimal,
	revised bool,
) (factSet, error) {
	lineagesByIdentity := map[string]ledgerfacts.LineageFact{}
	lineageID := ""
	for _, lineage := range lineages {
		if lineageID == "" {
			lineageID = lineage.LineageID
		} else if lineage.LineageID != lineageID {
			return factSet{}, errors.New("position is attributed to multiple logical lineages")
		}
		key := identityScopeKey(lineage.Scope)
		if prior, ok := lineagesByIdentity[key]; ok && prior.FactID != lineage.FactID {
			return factSet{}, errors.New("position has multiple lineage facts for one leg scope")
		}
		lineagesByIdentity[key] = lineage
	}
	if len(lineagesByIdentity) != len(snapshots) {
		return factSet{}, errors.New("position lineage and snapshot scope sets differ")
	}

	scopeKeys := make([]string, 0, len(snapshots))
	for scopeKey := range snapshots {
		scopeKeys = append(scopeKeys, scopeKey)
	}
	sort.Strings(scopeKeys)
	facts := factSet{
		PositionID:     positionID,
		ReportingAsset: bundle.ReportingAsset,
		Closed:         true,
		Verified:       true,
		TradingPnl:     ledgerfacts.Money{Asset: bundle.ReportingAsset},
		UnrealizedPnl:  ledgerfacts.Money{Asset: bundle.ReportingAsset},
		Funding:        ledgerfacts.Money{Asset: bundle.ReportingAsset, Amount: funding},
		Fees:           ledgerfacts.Money{Asset: bundle.ReportingAsset, Amount: fees},
		Revised:        revised,
	}
	asOfAligned := true
	lifecycleAligned := true
	for index, scopeKey := range scopeKeys {
		snapshot := snapshots[scopeKey]
		if snapshot.ReportingAsset != bundle.ReportingAsset {
			return factSet{}, errors.New("mixed reporting assets")
		}
		lineage, ok := lineagesByIdentity[identityScopeKey(snapshot.Scope)]
		if !ok {
			return factSet{}, errors.New("position snapshot has no lineage for its leg scope")
		}
		if index == 0 {
			facts.Scope = snapshot.Scope
			facts.AsOf = snapshot.AsOf
			facts.Conversion = projectionConversion(snapshot.Conversion)
		} else {
			if !snapshot.AsOf.Equal(facts.AsOf) {
				asOfAligned = false
			}
			if snapshot.AsOf.After(facts.AsOf) {
				facts.AsOf = snapshot.AsOf
			}
			if !sameConversion(facts.Conversion, projectionConversion(snapshot.Conversion)) {
				return factSet{}, errors.New("position leg conversion metadata differs")
			}
		}
		facts.Closed = facts.Closed && snapshot.Closed
		facts.Verified = facts.Verified && snapshot.Verified
		facts.TradingPnl.Amount = facts.TradingPnl.Amount.Add(snapshot.RealizedTradingPnl)
		facts.UnrealizedPnl.Amount = facts.UnrealizedPnl.Amount.Add(snapshot.UnrealizedTradingPnl)
		facts.Residual = mergeResidual(facts.Residual, projectionResidual(snapshot.Residual))
		facts.Watermark = mergeWatermark(facts.Watermark, watermarks[scopeKey].Watermark)
		active := lineage.Active && lineage.ClosedAt.IsZero()
		if snapshot.Closed {
			if active || lineage.ClosedAt.IsZero() || lineage.ClosedAt.After(snapshot.AsOf) {
				lifecycleAligned = false
			}
		} else if !active {
			lifecycleAligned = false
		}
	}
	for _, scopeKey := range scopeKeys {
		if !scopeEvidenceVerified(snapshots[scopeKey], watermarks[scopeKey], fundingByScope[scopeKey], fillByScope[scopeKey], facts.AsOf) {
			facts.Verified = false
		}
	}
	if !asOfAligned {
		facts.Verified = false
	}
	if !lifecycleAligned {
		facts.Verified = false
		if facts.Residual.BlockerClass == "" {
			facts.Residual.BlockerClass = "lineage_state_mismatch"
		}
	}
	return facts, nil
}

func scopeEvidenceVerified(snapshot ledgerfacts.PositionSnapshotFact, target resolvedWatermark, funding, fills factEvidence, positionAsOf time.Time) bool {
	watermark := target.Watermark
	if target.FactID == "" || !snapshot.Verified || snapshot.AsOf.IsZero() || !snapshot.AsOf.Equal(positionAsOf) ||
		!watermark.Verified || watermark.CompleteThrough.Before(positionAsOf) || !fills.Seen || !fills.AllBound {
		return false
	}
	switch watermark.Status {
	case ledgerfacts.Complete:
		return funding.Seen && funding.AllBound
	case ledgerfacts.CompleteEmpty:
		return !funding.Seen
	default:
		return false
	}
}

func logicalLineageCounts(lineages map[string]ledgerfacts.LineageFact) (int, int) {
	activeByLineageID := map[string]bool{}
	for _, lineage := range lineages {
		active := lineage.Active && lineage.ClosedAt.IsZero()
		activeByLineageID[lineage.LineageID] = activeByLineageID[lineage.LineageID] || active
	}
	open := 0
	closed := 0
	for _, active := range activeByLineageID {
		if active {
			open++
		} else {
			closed++
		}
	}
	return open, closed
}

func identityScopeKey(scope ledgerfacts.Scope) string {
	return scope.PrincipalID + "\x00" + scope.Mode + "\x00" + scope.AccountID + "\x00" + scope.InstrumentID
}

func sameConversion(left, right ConversionMetadata) bool {
	return left.ConversionSource == right.ConversionSource &&
		left.ConversionAsOf.Equal(right.ConversionAsOf) &&
		left.RoundingMode == right.RoundingMode &&
		left.Scale == right.Scale &&
		left.FormulaVersion == right.FormulaVersion
}

func legacyCloseRetryPositionSignature(fact ledgerfacts.PositionSnapshotFact) []string {
	return []string{
		legacyCloseRetryScopeSignature(fact.Scope),
		fact.PositionID,
		fact.Provenance.SourceRef,
		fact.Provenance.SourceVersion,
		strconv.FormatBool(fact.Closed),
		fact.ReportingAsset,
		fact.RealizedTradingPnl.String(),
		fact.UnrealizedTradingPnl.String(),
		fact.ActualFundingCashFlow.String(),
		fact.Fees.String(),
		strconv.FormatBool(fact.Verified),
		legacyCloseRetryResidualSignature(projectionResidual(fact.Residual)),
		legacyCloseRetryConversionSignature(projectionConversion(fact.Conversion)),
	}
}

func legacyCloseRetryLineageSignature(fact ledgerfacts.LineageFact) []string {
	return []string{
		legacyCloseRetryScopeSignature(fact.Scope),
		fact.LineageID,
		fact.PositionID,
		fact.VenueEnvironment,
		fact.Product,
		fact.Provenance.SourceRef,
		fact.Provenance.SourceVersion,
		strconv.FormatBool(fact.Active),
		strconv.FormatBool(!fact.ClosedAt.IsZero()),
	}
}

func legacyCloseRetryRevisionSignature(fact ledgerfacts.LineageRevisionFact) []string {
	return []string{
		legacyCloseRetryScopeSignature(fact.Scope),
		fact.Revision,
		fact.Status,
		fact.Reason,
		fact.Provenance.SourceRef,
		fact.Provenance.SourceVersion,
	}
}

func legacyCloseRetryWatermarkSignature(watermark resolvedWatermark) []string {
	return []string{
		legacyCloseRetryScopeSignature(watermark.Scope),
		watermark.SourceRef,
		watermark.Cursor,
		watermark.Revision,
		string(watermark.Watermark.Status),
		strconv.FormatBool(watermark.Watermark.Verified),
	}
}

func legacyCloseRetryFillSignature(fact ledgerfacts.FillCashflowFact) []string {
	return []string{
		legacyCloseRetryScopeSignature(fact.Scope),
		fact.PositionID,
		fact.LineageID,
		fact.FillID,
		fact.Provenance.SourceRef,
		fact.Provenance.SourceVersion,
		fact.Provenance.EffectiveAt.UTC().Format(time.RFC3339Nano),
		legacyCloseRetryMoneySignature(fact.Fee),
		legacyCloseRetryMoneySignature(fact.Rebate),
		fact.SourceAsset,
		fact.ReportingAsset,
		legacyCloseRetryConversionSignature(projectionConversion(fact.Conversion)),
		strconv.FormatBool(fact.AlreadySigned),
	}
}

func legacyCloseRetryFundingSignature(fact ledgerfacts.FundingPaymentFact) []string {
	return []string{
		legacyCloseRetryScopeSignature(fact.Scope),
		fact.PaymentID,
		fact.PositionID,
		fact.Venue,
		fact.VenueEnvironment,
		fact.Product,
		fact.FormulaVersion,
		fact.Provenance.SourceRef,
		fact.Provenance.SourceVersion,
		fact.Provenance.EffectiveAt.UTC().Format(time.RFC3339Nano),
		legacyCloseRetryMoneySignature(fact.Payment),
	}
}

func legacyCloseRetryScopeSignature(scope ledgerfacts.Scope) string {
	return scope.PrincipalID + "\x1e" +
		scope.Mode + "\x1e" +
		scope.AccountID + "\x1e" +
		scope.InstrumentID + "\x1e" +
		scope.WindowStart.UTC().Format(time.RFC3339Nano) + "\x1e" +
		scope.PaginationTraversal
}

func legacyCloseRetryPairingKey(scope ledgerfacts.Scope, positionID, sourceRef, sourceRevision string) string {
	return identityScopeKey(scope) + "\x1e" + positionID + "\x1e" + sourceRef + "\x1e" + sourceRevision
}

func legacyCloseRetryMoneySignature(money ledgerfacts.Money) string {
	return money.Asset + "\x1e" + money.Amount.String()
}

func legacyCloseRetryResidualSignature(residual Residual) string {
	return strings.Join([]string{
		residual.LegQuantity.String(),
		residual.ReportingAssetNotional.String(),
		strconv.Itoa(residual.OpenOrderCount),
		strconv.Itoa(residual.PendingCompensation),
		residual.BlockerClass,
	}, "\x1e")
}

func legacyCloseRetryConversionSignature(conversion ConversionMetadata) string {
	return strings.Join([]string{
		conversion.ConversionSource,
		conversion.RoundingMode,
		strconv.Itoa(conversion.Scale),
		conversion.FormulaVersion,
	}, "\x1e")
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func mergePortfolioConversion(current, next ConversionMetadata) (ConversionMetadata, error) {
	if current == (ConversionMetadata{}) {
		return next, nil
	}
	if current.ConversionSource != next.ConversionSource ||
		current.RoundingMode != next.RoundingMode ||
		current.Scale != next.Scale ||
		current.FormulaVersion != next.FormulaVersion {
		return ConversionMetadata{}, errors.New("mixed portfolio conversion metadata")
	}
	if next.ConversionAsOf.After(current.ConversionAsOf) {
		current.ConversionAsOf = next.ConversionAsOf
	}
	return current, nil
}

func buildClosedProjection(facts factSet) (ClosedPositionProjection, error) {
	if facts.Verified && facts.Closed && !facts.UnrealizedPnl.Amount.IsZero() {
		return ClosedPositionProjection{}, errors.New("verified closed position requires zero unrealized trading pnl")
	}
	net := facts.TradingPnl.Amount.Add(facts.UnrealizedPnl.Amount).Add(facts.Funding.Amount).Add(facts.Fees.Amount)
	status := verificationStatus(facts)
	return ClosedPositionProjection{
		PositionID:            facts.PositionID,
		Scope:                 facts.Scope,
		ReportingAsset:        facts.ReportingAsset,
		AsOf:                  facts.AsOf,
		RealizedTradingPnl:    ledgerfacts.Money{Asset: facts.ReportingAsset, Amount: facts.TradingPnl.Amount},
		UnrealizedTradingPnl:  ledgerfacts.Money{Asset: facts.ReportingAsset, Amount: facts.UnrealizedPnl.Amount},
		ActualFundingCashFlow: ledgerfacts.Money{Asset: facts.ReportingAsset, Amount: facts.Funding.Amount},
		Fees:                  ledgerfacts.Money{Asset: facts.ReportingAsset, Amount: facts.Fees.Amount},
		NetPnl:                ledgerfacts.Money{Asset: facts.ReportingAsset, Amount: net},
		ClosedTradingUPnL:     ledgerfacts.Money{Asset: facts.ReportingAsset, Amount: facts.UnrealizedPnl.Amount},
		Verified:              facts.Verified,
		VerificationStatus:    status,
		Residual:              facts.Residual,
		Watermark:             facts.Watermark,
		Conversion:            facts.Conversion,
	}, nil
}

func verificationStatus(facts factSet) string {
	if hasResidual(facts.Residual) {
		return "blocked"
	}
	switch facts.Watermark.Status {
	case ledgerfacts.Unsupported:
		return "unsupported"
	case ledgerfacts.TemporarilyUnavailable:
		return "temporarily_unavailable"
	case ledgerfacts.Incomplete:
		return "incomplete"
	}
	if facts.Verified && facts.Revised {
		return "revised"
	}
	if facts.Verified {
		return "verified"
	}
	return "provisional"
}

func mergeStatus(left, right string) string {
	if left == "" {
		return right
	}
	if statusRank(right) > statusRank(left) {
		return right
	}
	return left
}

func statusRank(status string) int {
	switch status {
	case "blocked":
		return 6
	case "unsupported":
		return 5
	case "temporarily_unavailable":
		return 4
	case "incomplete":
		return 3
	case "provisional":
		return 2
	case "revised":
		return 1
	case "verified":
		return 0
	default:
		return 2
	}
}

func validateAuthoritativeBundle(bundle AuthoritativePortfolioReplay) error {
	if bundle.PrincipalID == "" || (bundle.Mode != "test" && bundle.Mode != "live") || bundle.ReportingAsset == "" {
		return errors.New("authoritative portfolio target is required")
	}
	if len(bundle.Scopes) == 0 {
		return errors.New("authoritative replay scopes are required")
	}
	declared := map[string]ledgerfacts.Scope{}
	used := map[string]bool{}
	facts := map[string]ledgerfacts.ReplayEntry{}
	for _, scope := range bundle.Scopes {
		if scope.PrincipalID != bundle.PrincipalID || scope.Mode != bundle.Mode {
			return errors.New("authoritative scope principal or mode mismatch")
		}
		key := exactScopeKey(scope)
		if _, ok := declared[key]; ok {
			return errors.New("duplicate authoritative replay scope")
		}
		declared[key] = scope
	}
	for _, entry := range bundle.Entries {
		kind, factID, factCount := replayEntryIdentity(entry)
		if factCount != 1 {
			return errors.New("replay entry must contain exactly one fact")
		}
		if entry.Kind != kind {
			return errors.New("replay entry kind does not match fact")
		}
		if strings.TrimSpace(factID) == "" {
			return errors.New("replay fact stable identity is required")
		}
		if entry.SequenceID <= 0 || strings.TrimSpace(entry.Revision) == "" {
			return errors.New("replay ordering provenance is required")
		}
		factKey := kind + "\x00" + factID
		if _, exists := facts[factKey]; exists {
			return errors.New("authoritative replay contains duplicate fact")
		}
		facts[factKey] = entry
		scope, ok := replayEntryScope(entry)
		if !ok {
			return errors.New("replay entry has no scoped fact")
		}
		if scope.PrincipalID != bundle.PrincipalID || scope.Mode != bundle.Mode {
			return errors.New("replay entry principal or mode mismatch")
		}
		if entry.Lineage != nil || entry.RevisionFact != nil {
			matched := false
			for key, declaredScope := range declared {
				if sameIdentityScope(scope, declaredScope) {
					used[key] = true
					matched = true
				}
			}
			if !matched {
				return errors.New("lineage replay identity scope is not declared")
			}
		} else {
			key := exactScopeKey(scope)
			if _, ok := declared[key]; !ok {
				return errors.New("replay entry scope is not declared")
			}
			used[key] = true
		}
		if err := validateReplayEntryAsset(bundle.ReportingAsset, entry); err != nil {
			return err
		}
	}
	successors := map[string]string{}
	for factKey, entry := range facts {
		supersedes := entrySupersedes(entry)
		if supersedes == "" {
			continue
		}
		targetKey := entry.Kind + "\x00" + supersedes
		target, ok := facts[targetKey]
		if !ok {
			return errors.New("replay supersession target is missing")
		}
		if targetKey == factKey {
			return errors.New("replay fact cannot supersede itself")
		}
		if prior, exists := successors[targetKey]; exists && prior != factKey {
			return errors.New("replay supersession target has multiple successors")
		}
		if compareReplayRevision(entry.Revision, target.Revision) <= 0 {
			return errors.New("replay supersession revision must increase")
		}
		successors[targetKey] = factKey
	}
	for key := range declared {
		if !used[key] {
			return errors.New("authoritative replay scope has no entries")
		}
	}
	return nil
}

func validateReplayEntryAsset(asset string, entry ledgerfacts.ReplayEntry) error {
	switch {
	case entry.Position != nil:
		if entry.Position.ReportingAsset != asset {
			return errors.New("position reporting asset does not match portfolio target")
		}
	case entry.Funding != nil:
		if entry.Funding.Payment.Asset != asset {
			return errors.New("funding payment asset does not match portfolio target")
		}
	case entry.Fill != nil:
		if err := validateFillReportingAsset(*entry.Fill, asset); err != nil {
			return err
		}
	}
	return nil
}

func replayEntryScope(entry ledgerfacts.ReplayEntry) (ledgerfacts.Scope, bool) {
	switch {
	case entry.Funding != nil:
		return entry.Funding.Scope, true
	case entry.Fill != nil:
		return entry.Fill.Scope, true
	case entry.Position != nil:
		return entry.Position.Scope, true
	case entry.Watermark != nil:
		return entry.Watermark.Scope, true
	case entry.Lineage != nil:
		return entry.Lineage.Scope, true
	case entry.RevisionFact != nil:
		return entry.RevisionFact.Scope, true
	default:
		return ledgerfacts.Scope{}, false
	}
}

func exactScopeKey(scope ledgerfacts.Scope) string {
	return scope.PrincipalID + "\x00" +
		scope.Mode + "\x00" +
		scope.AccountID + "\x00" +
		scope.InstrumentID + "\x00" +
		scope.WindowStart.UTC().Format(time.RFC3339Nano) + "\x00" +
		scope.WindowEnd.UTC().Format(time.RFC3339Nano) + "\x00" +
		scope.PaginationTraversal
}

func resolvePositionWatermark(position ledgerfacts.PositionSnapshotFact, byID map[string]ledgerfacts.SyncWatermark, byScope map[string][]ledgerfacts.SyncWatermark) (resolvedWatermark, error) {
	var source ledgerfacts.SyncWatermark
	if position.SyncWatermarkID != "" {
		item, ok := byID[position.SyncWatermarkID]
		if !ok {
			return resolvedWatermark{}, errors.New("position sync watermark id was not replayed")
		}
		if !sameExactScope(position.Scope, item.Scope) {
			return resolvedWatermark{}, errors.New("position sync watermark scope mismatch")
		}
		source = item
	} else {
		items := byScope[exactScopeKey(position.Scope)]
		if len(items) == 0 {
			return resolvedWatermark{}, nil
		}
		if len(items) != 1 {
			return resolvedWatermark{}, errors.New("position replay has ambiguous exact-scope watermarks")
		}
		source = items[0]
	}
	return resolvedWatermark{
		FactID:    source.FactID,
		Scope:     source.Scope,
		SourceRef: source.SourceRef,
		Cursor:    source.Cursor,
		Revision:  source.Revision,
		Watermark: Watermark{
			Status:          source.Status,
			Verified:        source.Verified,
			CompleteThrough: source.CompleteThrough,
		},
	}, nil
}

func mergeResidual(left, right Residual) Residual {
	left.LegQuantity = left.LegQuantity.Add(right.LegQuantity)
	left.ReportingAssetNotional = left.ReportingAssetNotional.Add(right.ReportingAssetNotional)
	left.OpenOrderCount += right.OpenOrderCount
	left.PendingCompensation += right.PendingCompensation
	if left.BlockerClass == "" {
		left.BlockerClass = right.BlockerClass
	}
	if left.BlockerClass == "" && hasResidual(left) {
		left.BlockerClass = "residual"
	}
	return left
}

func hasResidual(residual Residual) bool {
	return !residual.LegQuantity.IsZero() ||
		!residual.ReportingAssetNotional.IsZero() ||
		residual.OpenOrderCount != 0 ||
		residual.PendingCompensation != 0 ||
		residual.BlockerClass != ""
}

func mergeWatermark(left, right Watermark) Watermark {
	if left.Status == "" {
		return right
	}
	if statusRank(watermarkStatus(right)) > statusRank(watermarkStatus(left)) {
		return right
	}
	if right.CompleteThrough.Before(left.CompleteThrough) {
		return right
	}
	return left
}

func watermarkStatus(watermark Watermark) string {
	return verificationStatus(factSet{Verified: watermark.Verified, Watermark: watermark})
}

func sameExactScope(left, right ledgerfacts.Scope) bool {
	return sameIdentityScope(left, right) &&
		left.WindowStart.Equal(right.WindowStart) &&
		left.WindowEnd.Equal(right.WindowEnd) &&
		left.PaginationTraversal == right.PaginationTraversal
}

func sameIdentityScope(left, right ledgerfacts.Scope) bool {
	return left.PrincipalID == right.PrincipalID &&
		left.Mode == right.Mode &&
		left.AccountID == right.AccountID &&
		left.InstrumentID == right.InstrumentID
}

func validateFillReportingAsset(fact ledgerfacts.FillCashflowFact, reportingAsset string) error {
	if fact.ReportingAsset != "" && fact.ReportingAsset != reportingAsset {
		return errors.New("fill reporting asset does not match position reporting asset")
	}
	for _, money := range []ledgerfacts.Money{fact.Fee, fact.Rebate} {
		if money.Asset == "" {
			continue
		}
		if money.Asset != reportingAsset {
			return errors.New("fill cashflow asset does not match reporting asset")
		}
	}
	return nil
}

func projectionResidual(residual ledgerfacts.Residual) Residual {
	return Residual{
		LegQuantity:            residual.LegQuantity,
		ReportingAssetNotional: residual.ReportingAssetNotional,
		OpenOrderCount:         residual.OpenOrderCount,
		PendingCompensation:    residual.PendingCompensation,
		BlockerClass:           residual.BlockerClass,
	}
}

func projectionConversion(conversion ledgerfacts.ConversionMetadata) ConversionMetadata {
	return ConversionMetadata{
		ConversionSource: conversion.Source,
		ConversionAsOf:   conversion.AsOf,
		RoundingMode:     conversion.RoundingMode,
		Scale:            conversion.Scale,
		FormulaVersion:   conversion.FormulaVersion,
	}
}

func supersessionHeads(entries []ledgerfacts.ReplayEntry) map[string]bool {
	heads := map[string]bool{}
	for _, entry := range entries {
		id := entryFactID(entry)
		if id != "" {
			heads[id] = true
		}
	}
	for _, entry := range entries {
		if supersedes := entrySupersedes(entry); supersedes != "" {
			heads[supersedes] = false
		}
	}
	return heads
}

func entryFactID(entry ledgerfacts.ReplayEntry) string {
	_, factID, count := replayEntryIdentity(entry)
	if count != 1 {
		return ""
	}
	return factID
}

func replayEntryIdentity(entry ledgerfacts.ReplayEntry) (kind string, factID string, count int) {
	if entry.Funding != nil {
		kind, factID, count = "funding", entry.Funding.FactID, count+1
	}
	if entry.Fill != nil {
		kind, factID, count = "fill", entry.Fill.FactID, count+1
	}
	if entry.Position != nil {
		kind, factID, count = "position", entry.Position.FactID, count+1
	}
	if entry.Lineage != nil {
		kind, factID, count = "lineage", entry.Lineage.FactID, count+1
	}
	if entry.RevisionFact != nil {
		kind, factID, count = "lineage_revision", entry.RevisionFact.FactID, count+1
	}
	if entry.Watermark != nil {
		kind, factID, count = "watermark", entry.Watermark.FactID, count+1
	}
	return kind, factID, count
}

func entrySupersedes(entry ledgerfacts.ReplayEntry) string {
	switch {
	case entry.Funding != nil:
		return entry.Funding.SupersedesFactID
	case entry.Fill != nil:
		return entry.Fill.SupersedesFactID
	case entry.Position != nil:
		return entry.Position.SupersedesFactID
	case entry.RevisionFact != nil:
		return entry.RevisionFact.SupersedesFactID
	default:
		return ""
	}
}

type MemoryStore struct {
	mu        sync.Mutex
	closed    map[string]ClosedPositionProjection
	portfolio map[string]PortfolioProjection
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		closed:    map[string]ClosedPositionProjection{},
		portfolio: map[string]PortfolioProjection{},
	}
}

func (s *MemoryStore) ReplaceClosedPosition(_ context.Context, projection ClosedPositionProjection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed[projection.PositionID] = projection
	return nil
}

func (s *MemoryStore) ReplacePortfolio(_ context.Context, projection PortfolioProjection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.portfolio[portfolioKey(projection.PrincipalID, projection.Mode, projection.ReportingAsset)] = projection
	return nil
}

func (s *MemoryStore) ClosedPositions(principalID, mode, asset string) []ClosedPositionProjection {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ClosedPositionProjection, 0, len(s.closed))
	for _, projection := range s.closed {
		if projection.Scope.PrincipalID == principalID &&
			projection.Scope.Mode == mode &&
			projection.ReportingAsset == asset {
			out = append(out, projection)
		}
	}
	return out
}

func (s *MemoryStore) ClosedPosition(positionID string) (ClosedPositionProjection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	projection, ok := s.closed[positionID]
	return projection, ok
}

func (s *MemoryStore) Portfolio(principalID, mode, asset string) (PortfolioProjection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	projection, ok := s.portfolio[portfolioKey(principalID, mode, asset)]
	return projection, ok
}

func portfolioKey(principalID, mode, asset string) string {
	return principalID + "\x00" + mode + "\x00" + asset
}
