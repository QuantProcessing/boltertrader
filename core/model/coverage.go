package model

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
)

// CoverageState describes how authoritative one mass-status domain is. The
// zero value is intentionally unsafe for absence-based conclusions.
type CoverageState uint8

const (
	CoverageUnknown CoverageState = iota
	CoverageNotRequested
	CoverageComplete
	CoveragePartial
	CoverageUnavailable
)

func (s CoverageState) String() string {
	switch s {
	case CoverageUnknown:
		return "UNKNOWN"
	case CoverageNotRequested:
		return "NOT_REQUESTED"
	case CoverageComplete:
		return "COMPLETE"
	case CoveragePartial:
		return "PARTIAL"
	case CoverageUnavailable:
		return "UNAVAILABLE"
	default:
		return fmt.Sprintf("CoverageState(%d)", s)
	}
}

// CoverageScope is the self-contained scope owned by one mass-status domain.
// InstrumentIDs is nil when no selector was captured; a non-nil empty slice is
// a proven empty selector. Snapshot domains use only Through as the local
// request-start observation watermark. Fill domains use From/Through as the
// exact effective history interval.
type CoverageScope struct {
	AccountID     string
	ClientID      string
	InstrumentIDs []InstrumentID
	From          time.Time
	Through       time.Time
}

func (s CoverageScope) Clone() CoverageScope {
	out := s
	if s.InstrumentIDs != nil {
		out.InstrumentIDs = append([]InstrumentID{}, s.InstrumentIDs...)
	}
	return out
}

func (s CoverageScope) IsZero() bool {
	return s.AccountID == "" && s.ClientID == "" && s.InstrumentIDs == nil && s.From.IsZero() && s.Through.IsZero()
}

// ContainsInstrument reports membership using only the response-owned frozen
// selector. It never consults a mutable instrument provider.
func (s CoverageScope) ContainsInstrument(id InstrumentID) bool {
	_, ok := slices.BinarySearchFunc(s.InstrumentIDs, id, compareInstrumentID)
	return ok
}

// ReportCoverage couples an authority state with its exact evidence/attempt
// scope. Unknown and NotRequested carry a zero scope. Unavailable may carry a
// zero scope (no request started) or a fully formed attempted scope.
type ReportCoverage struct {
	State CoverageState
	Scope CoverageScope
}

func (c ReportCoverage) Clone() ReportCoverage {
	c.Scope = c.Scope.Clone()
	return c
}

func (c ReportCoverage) Complete() bool { return c.State == CoverageComplete }

func NewSnapshotCoverage(state CoverageState, accountID, clientID string, ids []InstrumentID, through time.Time) ReportCoverage {
	return ReportCoverage{
		State: state,
		Scope: CoverageScope{
			AccountID:     strings.TrimSpace(accountID),
			ClientID:      clientID,
			InstrumentIDs: NormalizeInstrumentIDs(ids),
			Through:       through,
		},
	}
}

func NewFillCoverage(state CoverageState, accountID, clientID string, ids []InstrumentID, from, through time.Time) ReportCoverage {
	return ReportCoverage{
		State: state,
		Scope: CoverageScope{
			AccountID:     strings.TrimSpace(accountID),
			ClientID:      clientID,
			InstrumentIDs: NormalizeInstrumentIDs(ids),
			From:          from,
			Through:       through,
		},
	}
}

// NormalizeInstrumentIDs copies, trims the neutral identity fields, sorts, and
// deduplicates a selector. Case is preserved because neutral symbols may carry
// case-sensitive venue namespaces (for example Hyperliquid HIP-3 prefixes).
func NormalizeInstrumentIDs(ids []InstrumentID) []InstrumentID {
	if ids == nil {
		return nil
	}
	out := make([]InstrumentID, len(ids))
	for i, id := range ids {
		id.Venue = strings.TrimSpace(id.Venue)
		id.Symbol = strings.TrimSpace(id.Symbol)
		out[i] = id
	}
	sort.Slice(out, func(i, j int) bool { return compareInstrumentID(out[i], out[j]) < 0 })
	out = slices.Compact(out)
	return out
}

func compareInstrumentID(left, right InstrumentID) int {
	if cmp := strings.Compare(left.Venue, right.Venue); cmp != 0 {
		return cmp
	}
	if cmp := strings.Compare(left.Symbol, right.Symbol); cmp != 0 {
		return cmp
	}
	return int(left.Kind) - int(right.Kind)
}

type coverageDomain uint8

const (
	coverageOpenOrders coverageDomain = iota
	coverageFills
	coveragePositions
)

func (d coverageDomain) String() string {
	switch d {
	case coverageOpenOrders:
		return "open orders"
	case coverageFills:
		return "fills"
	case coveragePositions:
		return "positions"
	default:
		return "unknown"
	}
}

// ValidateFor validates both report payloads and the three coverage domains
// against the effective query. Every successful mass-status response must
// satisfy this contract before runtime consumes any report rows.
func (s ExecutionMassStatus) ValidateFor(query MassStatusQuery) error {
	if err := s.Validate(); err != nil {
		return err
	}
	venue := strings.TrimSpace(s.Venue)
	if venue == "" || venue != s.Venue {
		return fmt.Errorf("mass status coverage: normalized response venue required")
	}
	queryVenue := strings.TrimSpace(query.Venue)
	if queryVenue != "" && queryVenue != venue {
		return fmt.Errorf("mass status coverage: response venue %q does not match query venue %q", venue, queryVenue)
	}
	accountID := strings.TrimSpace(query.AccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(s.AccountID)
	}
	if accountID == "" {
		return fmt.Errorf("mass status coverage: account id required")
	}
	if strings.TrimSpace(s.AccountID) != s.AccountID || s.AccountID != accountID {
		return fmt.Errorf("mass status coverage: response account %q does not match query account %q", s.AccountID, accountID)
	}
	if s.ClientID != query.ClientID {
		return fmt.Errorf("mass status coverage: response client filter %q does not match query filter %q", s.ClientID, query.ClientID)
	}
	queryIDs := NormalizeInstrumentIDs(query.InstrumentIDs)
	for _, id := range queryIDs {
		if id.Venue != venue || id.Symbol == "" || id.Kind == enums.KindUnknown {
			return fmt.Errorf("mass status coverage: invalid query instrument %s for venue %q", id, venue)
		}
	}
	if err := validateCoverageFor(coverageOpenOrders, s.OpenOrdersCoverage, true, venue, accountID, query.ClientID, queryIDs, query); err != nil {
		return err
	}
	if err := validateCoverageFor(coverageFills, s.FillsCoverage, query.IncludeFills, venue, accountID, query.ClientID, queryIDs, query); err != nil {
		return err
	}
	if err := validateCoverageFor(coveragePositions, s.PositionsCoverage, query.IncludePositions, venue, accountID, query.ClientID, queryIDs, query); err != nil {
		return err
	}
	if err := s.validateReportsWithinCoverage(); err != nil {
		return err
	}
	return nil
}

func validateCoverageFor(domain coverageDomain, coverage ReportCoverage, requested bool, venue, accountID, clientID string, queryIDs []InstrumentID, query MassStatusQuery) error {
	if coverage.State > CoverageUnavailable {
		return fmt.Errorf("mass status %s coverage: invalid state %d", domain, coverage.State)
	}
	if !requested {
		if coverage.State != CoverageNotRequested || !coverage.Scope.IsZero() {
			return fmt.Errorf("mass status %s coverage: omitted domain must be NotRequested with zero scope", domain)
		}
		return nil
	}
	if domain == coverageOpenOrders && coverage.State == CoverageNotRequested {
		return fmt.Errorf("mass status open orders coverage: domain is always requested")
	}
	switch coverage.State {
	case CoverageUnknown:
		if !coverage.Scope.IsZero() {
			return fmt.Errorf("mass status %s coverage: Unknown must have zero scope", domain)
		}
		return fmt.Errorf("mass status %s coverage: requested domain cannot be Unknown", domain)
	case CoverageNotRequested:
		return fmt.Errorf("mass status %s coverage: requested domain cannot be NotRequested", domain)
	case CoverageUnavailable:
		if coverage.Scope.IsZero() {
			return nil
		}
		if err := validateFullCoverageScope(domain, coverage.Scope, venue, accountID, clientID, queryIDs, query); err != nil {
			return fmt.Errorf("mass status %s coverage: unavailable scope: %w", domain, err)
		}
		return nil
	case CoverageComplete, CoveragePartial:
		if err := validateFullCoverageScope(domain, coverage.Scope, venue, accountID, clientID, queryIDs, query); err != nil {
			return fmt.Errorf("mass status %s coverage: %w", domain, err)
		}
		return nil
	default:
		return fmt.Errorf("mass status %s coverage: invalid state %d", domain, coverage.State)
	}
}

func validateFullCoverageScope(domain coverageDomain, scope CoverageScope, venue, accountID, clientID string, queryIDs []InstrumentID, query MassStatusQuery) error {
	if strings.TrimSpace(scope.AccountID) == "" || scope.AccountID != strings.TrimSpace(scope.AccountID) {
		return fmt.Errorf("normalized account id required")
	}
	if scope.AccountID != accountID {
		return fmt.Errorf("account %q does not match effective query account %q", scope.AccountID, accountID)
	}
	if scope.ClientID != clientID {
		return fmt.Errorf("client filter %q does not match effective query filter %q", scope.ClientID, clientID)
	}
	if scope.InstrumentIDs == nil {
		return fmt.Errorf("frozen instrument selector required")
	}
	normalized := NormalizeInstrumentIDs(scope.InstrumentIDs)
	if !slices.Equal(normalized, scope.InstrumentIDs) {
		return fmt.Errorf("instrument selector must be normalized, sorted, and deduplicated")
	}
	for _, id := range scope.InstrumentIDs {
		if id.Venue == "" || id.Symbol == "" || id.Kind == enums.KindUnknown {
			return fmt.Errorf("invalid instrument selector entry %s", id)
		}
		if id.Venue != venue {
			return fmt.Errorf("instrument %s does not match response venue %q", id, venue)
		}
	}
	if queryIDs != nil {
		for _, id := range scope.InstrumentIDs {
			if _, ok := slices.BinarySearchFunc(queryIDs, id, compareInstrumentID); !ok {
				return fmt.Errorf("instrument %s is outside effective query selector", id)
			}
		}
	}
	if domain == coverageFills {
		if scope.Through.IsZero() || scope.From.After(scope.Through) {
			return fmt.Errorf("valid fill interval required")
		}
		expectedFrom := query.Since
		if expectedFrom.IsZero() && query.Lookback > 0 && !query.Until.IsZero() {
			expectedFrom = query.Until.Add(-query.Lookback)
		}
		if !scope.From.Equal(expectedFrom) {
			return fmt.Errorf("fill interval start %s does not match effective query start %s", scope.From, expectedFrom)
		}
		if !query.Until.IsZero() && !scope.Through.Equal(query.Until) {
			return fmt.Errorf("fill interval end %s does not match effective query end %s", scope.Through, query.Until)
		}
		return nil
	}
	if !scope.From.IsZero() || scope.Through.IsZero() {
		return fmt.Errorf("snapshot scope requires zero From and nonzero Through observation watermark")
	}
	return nil
}

func (s ExecutionMassStatus) validateReportsWithinCoverage() error {
	for _, report := range s.OrderReports {
		if err := validateReportIdentityWithinCoverage(
			"order",
			s.OpenOrdersCoverage,
			s.Venue,
			report.Venue,
			[]string{report.AccountID, report.Order.Request.AccountID},
			report.Order.Request.ClientID,
			report.Order.Request.InstrumentID,
		); err != nil {
			return err
		}
	}
	for _, reports := range s.FillReports {
		for _, report := range reports {
			if err := validateReportIdentityWithinCoverage(
				"fill",
				s.FillsCoverage,
				s.Venue,
				report.Venue,
				[]string{report.AccountID, report.Fill.AccountID},
				report.Fill.ClientID,
				report.Fill.InstrumentID,
			); err != nil {
				return err
			}
			if report.Fill.Timestamp.IsZero() ||
				report.Fill.Timestamp.Before(s.FillsCoverage.Scope.From) ||
				report.Fill.Timestamp.After(s.FillsCoverage.Scope.Through) {
				return fmt.Errorf(
					"mass status fill report: event time %s outside coverage interval [%s,%s]",
					report.Fill.Timestamp,
					s.FillsCoverage.Scope.From,
					s.FillsCoverage.Scope.Through,
				)
			}
		}
	}
	for _, reports := range s.PositionReports {
		for _, report := range reports {
			if err := validateReportIdentityWithinCoverage(
				"position",
				s.PositionsCoverage,
				s.Venue,
				report.Venue,
				[]string{report.AccountID, report.Position.AccountID},
				"",
				report.Position.InstrumentID,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateReportIdentityWithinCoverage(
	kind string,
	coverage ReportCoverage,
	venue string,
	reportVenue string,
	accountIDs []string,
	clientID string,
	instrumentID InstrumentID,
) error {
	if coverage.State != CoverageComplete && coverage.State != CoveragePartial {
		return fmt.Errorf("mass status %s report: coverage state %s cannot carry reports", kind, coverage.State)
	}
	if reportVenue != "" && reportVenue != venue {
		return fmt.Errorf("mass status %s report: venue %q outside coverage venue %q", kind, reportVenue, venue)
	}
	for _, accountID := range accountIDs {
		if accountID != "" && accountID != coverage.Scope.AccountID {
			return fmt.Errorf("mass status %s report: account %q outside coverage account %q", kind, accountID, coverage.Scope.AccountID)
		}
	}
	if kind != "position" && coverage.Scope.ClientID != "" && clientID != coverage.Scope.ClientID {
		return fmt.Errorf("mass status %s report: client %q outside coverage filter %q", kind, clientID, coverage.Scope.ClientID)
	}
	if !coverage.Scope.ContainsInstrument(instrumentID) {
		return fmt.Errorf("mass status %s report: instrument %s outside frozen selector", kind, instrumentID)
	}
	return nil
}
