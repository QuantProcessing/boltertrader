package ledgerreconcile

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgersync"
	"github.com/shopspring/decimal"
)

var (
	ErrInvalidExecution    = errors.New("ledger reconcile execution is invalid")
	ErrLineageMismatch     = errors.New("ledger reconcile lineage mismatch")
	ErrLegIdentityDrift    = errors.New("ledger reconcile leg identity drift")
	ErrTimeOrder           = errors.New("ledger reconcile time order is invalid")
	ErrIncompleteClose     = errors.New("ledger reconcile close quantity is incomplete")
	ErrResidualExposure    = errors.New("ledger reconcile residual exposure remains")
	ErrReportingAsset      = errors.New("ledger reconcile reporting asset mismatch")
	ErrClosedUnrealizedPnL = errors.New("ledger reconcile closed unrealized pnl must be zero")
	ErrNegativeFee         = errors.New("ledger reconcile fee must be non-negative")
)

type BuildOptions struct {
	ReportingAsset       string
	UnrealizedTradingPnl decimal.Decimal
}

type Bridge struct {
	PrincipalID                  string
	Mode                         string
	PositionID                   string
	LineageID                    string
	ReportingAsset               string
	VerificationPrerequisitesMet bool
	Legs                         []LegBridge
}

func (b Bridge) Leg(role execution.LegRole) *LegBridge {
	for i := range b.Legs {
		if b.Legs[i].Role == role {
			return &b.Legs[i]
		}
	}
	return nil
}

type LegBridge struct {
	Role             execution.LegRole
	Identity         execution.LegIdentity
	Scope            ledgerfacts.Scope
	SyncTarget       ledgersync.SyncTarget
	FillCashflows    []ledgerfacts.FillCashflowFact
	PositionSnapshot ledgerfacts.PositionSnapshotFact
	Lineage          ledgerfacts.LineageFact
	LineageRevision  ledgerfacts.LineageRevisionFact
}

func BuildClosedPositionBridge(open, close execution.Execution, options BuildOptions) (Bridge, error) {
	reportingAsset := strings.TrimSpace(options.ReportingAsset)
	if reportingAsset == "" {
		return Bridge{}, fmt.Errorf("%w: reporting asset is required", ErrReportingAsset)
	}
	if !options.UnrealizedTradingPnl.IsZero() {
		return Bridge{}, ErrClosedUnrealizedPnL
	}
	if err := validateExecutions(open, close); err != nil {
		return Bridge{}, err
	}
	closedAt, err := closeEffectiveAt(close)
	if err != nil {
		return Bridge{}, err
	}
	ledgerClose := close
	ledgerClose.UpdatedAt = closedAt

	openLegs, err := indexedLegs(open)
	if err != nil {
		return Bridge{}, err
	}
	closeLegs, err := indexedLegs(close)
	if err != nil {
		return Bridge{}, err
	}
	openFills := indexedFills(open)
	closeFills := indexedFills(close)

	positionID := strings.TrimSpace(close.PositionID)
	lineageID := stableLineageID(open)
	bridge := Bridge{
		PrincipalID:                  open.PrincipalID,
		Mode:                         open.Mode,
		PositionID:                   positionID,
		LineageID:                    lineageID,
		ReportingAsset:               reportingAsset,
		VerificationPrerequisitesMet: true,
		Legs:                         make([]LegBridge, 0, 2),
	}
	for _, role := range []execution.LegRole{execution.LegRoleLong, execution.LegRoleShort} {
		openLeg := openLegs[role]
		closeLeg := closeLegs[role]
		if !sameIdentity(openLeg.LegIdentity, closeLeg.LegIdentity) {
			return Bridge{}, ErrLegIdentityDrift
		}
		roleOpenFills := openFills[role]
		roleCloseFills := closeFills[role]
		if err := validateFillIdentities(openLeg.LegIdentity, roleOpenFills, roleCloseFills); err != nil {
			return Bridge{}, err
		}
		closedQty := fillQuantity(roleCloseFills)
		if !closedQty.Equal(openLeg.Filled) || !closedQty.Equal(closeLeg.Filled) {
			return Bridge{}, ErrIncompleteClose
		}
		realized, err := realizedTradingPnL(role, openLeg.Side, closeLeg.Side, roleOpenFills, roleCloseFills)
		if err != nil {
			return Bridge{}, err
		}
		fillFacts, feeTotal, err := fillCashflowTemplates(open, ledgerClose, openLeg.LegIdentity, role, reportingAsset, roleOpenFills, roleCloseFills)
		if err != nil {
			return Bridge{}, err
		}
		scope := ledgerScope(open, ledgerClose, openLeg.LegIdentity)
		bridge.Legs = append(bridge.Legs, LegBridge{
			Role:          role,
			Identity:      openLeg.LegIdentity,
			Scope:         scope,
			SyncTarget:    syncTarget(open, ledgerClose, openLeg.LegIdentity, reportingAsset),
			FillCashflows: fillFacts,
			PositionSnapshot: ledgerfacts.PositionSnapshotFact{
				Scope:                 scope,
				PositionID:            positionID,
				Provenance:            provenance(open, ledgerClose, role),
				Closed:                true,
				AsOf:                  closedAt,
				ReportingAsset:        reportingAsset,
				RealizedTradingPnl:    realized,
				UnrealizedTradingPnl:  decimal.Zero,
				ActualFundingCashFlow: decimal.Zero,
				Fees:                  feeTotal,
				Verified:              false,
				Residual:              ledgerfacts.Residual{},
				Conversion:            passthroughConversion(closedAt),
			},
			Lineage: ledgerfacts.LineageFact{
				Scope:            scope,
				LineageID:        lineageID,
				PositionID:       positionID,
				VenueEnvironment: openLeg.VenueEnvironment,
				Product:          openLeg.InstrumentID,
				Provenance:       provenance(open, ledgerClose, role),
				Active:           false,
				ClosedAt:         closedAt,
			},
			LineageRevision: ledgerfacts.LineageRevisionFact{
				Scope:      scope,
				Revision:   close.ID,
				Status:     "closed",
				Reason:     "execution_closed_flat",
				Provenance: provenance(open, ledgerClose, role),
			},
		})
	}
	return bridge, nil
}

func closeEffectiveAt(close execution.Execution) (time.Time, error) {
	var closedAt time.Time
	for _, fill := range close.Fills {
		at := fill.OccurredAt
		if at.IsZero() {
			at = fill.At
		}
		if at.After(closedAt) {
			closedAt = at.UTC()
		}
	}
	if closedAt.IsZero() || closedAt.Before(close.CreatedAt.UTC()) {
		return time.Time{}, ErrTimeOrder
	}
	return closedAt, nil
}

func validateExecutions(open, close execution.Execution) error {
	if open.Operation != execution.OperationOpen || open.State != execution.StateHedged ||
		close.Operation != execution.OperationClose || close.State != execution.StateClosed {
		return ErrInvalidExecution
	}
	if open.PrincipalID == "" || open.PrincipalID != close.PrincipalID ||
		(open.Mode != "test" && open.Mode != "live") || open.Mode != close.Mode {
		return ErrInvalidExecution
	}
	openID := strings.TrimSpace(open.ID)
	expectedPositionID := strings.TrimSpace(open.PositionID)
	if expectedPositionID == "" && openID != "" {
		expectedPositionID = "position-" + openID
	}
	if strings.TrimSpace(close.ParentExecutionID) != openID ||
		strings.TrimSpace(close.PositionID) == "" ||
		expectedPositionID != strings.TrimSpace(close.PositionID) ||
		stableLineageID(open) == "" ||
		strings.TrimSpace(close.LineageID) != stableLineageID(open) {
		return ErrLineageMismatch
	}
	if open.CreatedAt.IsZero() || close.CreatedAt.IsZero() || close.UpdatedAt.IsZero() ||
		close.CreatedAt.Before(open.CreatedAt) || close.UpdatedAt.Before(close.CreatedAt) {
		return ErrTimeOrder
	}
	if !open.DirectionalExposure.IsZero() || !close.DirectionalExposure.IsZero() {
		return ErrResidualExposure
	}
	if hasCompensation(open) || hasCompensation(close) {
		return ErrResidualExposure
	}
	return nil
}

func indexedLegs(item execution.Execution) (map[execution.LegRole]execution.Leg, error) {
	legs := make(map[execution.LegRole]execution.Leg, 2)
	for _, leg := range item.Legs {
		if leg.Role != execution.LegRoleLong && leg.Role != execution.LegRoleShort {
			return nil, ErrResidualExposure
		}
		if _, exists := legs[leg.Role]; exists {
			return nil, ErrInvalidExecution
		}
		if !leg.LegIdentity.Valid() || leg.Status != execution.LegStatusFilled || leg.State != execution.LegStatusFilled || !leg.Filled.IsPositive() {
			return nil, ErrInvalidExecution
		}
		legs[leg.Role] = leg
	}
	if len(legs) != 2 {
		return nil, ErrInvalidExecution
	}
	return legs, nil
}

func indexedFills(item execution.Execution) map[execution.LegRole][]execution.Fill {
	fills := map[execution.LegRole][]execution.Fill{}
	for _, fill := range item.Fills {
		fills[fill.Role] = append(fills[fill.Role], fill)
	}
	return fills
}

func validateFillIdentities(identity execution.LegIdentity, openFills, closeFills []execution.Fill) error {
	if len(openFills) == 0 || len(closeFills) == 0 {
		return ErrIncompleteClose
	}
	for _, fill := range append(append([]execution.Fill(nil), openFills...), closeFills...) {
		if !fill.LegIdentity.Valid() || !sameIdentity(identity, fill.LegIdentity) {
			return ErrLegIdentityDrift
		}
		if !fill.Quantity.IsPositive() || !fill.Price.IsPositive() {
			return ErrInvalidExecution
		}
	}
	return nil
}

func realizedTradingPnL(role execution.LegRole, openSide, closeSide execution.Side, openFills, closeFills []execution.Fill) (decimal.Decimal, error) {
	openQty := fillQuantity(openFills)
	closeQty := fillQuantity(closeFills)
	if !openQty.Equal(closeQty) {
		return decimal.Zero, ErrIncompleteClose
	}
	openNotional := fillNotional(openFills)
	closeNotional := fillNotional(closeFills)
	switch role {
	case execution.LegRoleLong:
		if openSide != execution.SideBuy || closeSide != execution.SideSell {
			return decimal.Zero, ErrInvalidExecution
		}
		return closeNotional.Sub(openNotional), nil
	case execution.LegRoleShort:
		if openSide != execution.SideSell || closeSide != execution.SideBuy {
			return decimal.Zero, ErrInvalidExecution
		}
		return openNotional.Sub(closeNotional), nil
	default:
		return decimal.Zero, ErrInvalidExecution
	}
}

func fillCashflowTemplates(open, close execution.Execution, identity execution.LegIdentity, role execution.LegRole, reportingAsset string, openFills, closeFills []execution.Fill) ([]ledgerfacts.FillCashflowFact, decimal.Decimal, error) {
	all := append(append([]execution.Fill(nil), openFills...), closeFills...)
	facts := make([]ledgerfacts.FillCashflowFact, 0, len(all))
	fees := decimal.Zero
	scope := ledgerScope(open, close, identity)
	for _, fill := range all {
		if fill.Fee.Currency != reportingAsset {
			return nil, decimal.Zero, ErrReportingAsset
		}
		if fill.Fee.Amount.IsNegative() {
			return nil, decimal.Zero, ErrNegativeFee
		}
		fees = fees.Add(fill.Fee.Amount)
		facts = append(facts, ledgerfacts.FillCashflowFact{
			Scope:          scope,
			PositionID:     close.PositionID,
			LineageID:      stableLineageID(open),
			FillID:         fill.FillID,
			Provenance:     fillProvenance(open, close, role, fill),
			Fee:            ledgerfacts.Money{Asset: reportingAsset, Amount: fill.Fee.Amount},
			Rebate:         ledgerfacts.Money{Asset: reportingAsset, Amount: decimal.Zero},
			SourceAsset:    reportingAsset,
			ReportingAsset: reportingAsset,
			Conversion:     passthroughConversion(close.UpdatedAt),
			AlreadySigned:  false,
		})
	}
	return facts, fees, nil
}

func ledgerScope(open, close execution.Execution, identity execution.LegIdentity) ledgerfacts.Scope {
	return ledgerfacts.Scope{
		PrincipalID:         open.PrincipalID,
		Mode:                open.Mode,
		AccountID:           identity.AccountID,
		InstrumentID:        identity.InstrumentID,
		WindowStart:         open.CreatedAt.UTC(),
		WindowEnd:           close.UpdatedAt.UTC(),
		PaginationTraversal: "ledger_reconcile:execution_close:v1",
	}
}

func syncTarget(open, close execution.Execution, identity execution.LegIdentity, reportingAsset string) ledgersync.SyncTarget {
	return ledgersync.SyncTarget{
		PrincipalID:     open.PrincipalID,
		Mode:            open.Mode,
		AccountID:       identity.AccountID,
		InstrumentID:    identity.InstrumentID,
		SettlementAsset: reportingAsset,
		WindowStart:     open.CreatedAt.UTC(),
		WindowEnd:       close.UpdatedAt.UTC(),
		PositionID:      close.PositionID,
	}
}

func provenance(open, close execution.Execution, role execution.LegRole) ledgerfacts.Provenance {
	return ledgerfacts.Provenance{
		SourceRef:      fmt.Sprintf("execution-close:%s:%s:%s", open.ID, close.ID, role),
		SourceRevision: close.UpdatedAt.UTC().Format(time.RFC3339Nano),
		SourceVersion:  close.Mode,
		ObservedAt:     close.UpdatedAt.UTC(),
		EffectiveAt:    close.UpdatedAt.UTC(),
	}
}

func fillProvenance(open, close execution.Execution, role execution.LegRole, fill execution.Fill) ledgerfacts.Provenance {
	base := provenance(open, close, role)
	base.SourceRef = fmt.Sprintf("%s:fill:%s", base.SourceRef, fill.FillID)
	if !fill.At.IsZero() {
		base.EffectiveAt = fill.At.UTC()
	} else if !fill.OccurredAt.IsZero() {
		base.EffectiveAt = fill.OccurredAt.UTC()
	}
	return base
}

func passthroughConversion(at time.Time) ledgerfacts.ConversionMetadata {
	return ledgerfacts.ConversionMetadata{
		Source:         "native-reporting-asset",
		AsOf:           at.UTC(),
		RoundingMode:   "none",
		Scale:          0,
		FormulaVersion: "ledger-reconcile-close-v1",
	}
}

func stableLineageID(open execution.Execution) string {
	if strings.TrimSpace(open.LineageID) != "" {
		return strings.TrimSpace(open.LineageID)
	}
	return strings.TrimSpace(open.ID)
}

func sameIdentity(left, right execution.LegIdentity) bool {
	return left.AccountID == right.AccountID &&
		left.Venue == right.Venue &&
		left.InstrumentID == right.InstrumentID &&
		left.VenueEnvironment == right.VenueEnvironment
}

func fillQuantity(fills []execution.Fill) decimal.Decimal {
	total := decimal.Zero
	for _, fill := range fills {
		total = total.Add(fill.Quantity)
	}
	return total
}

func fillNotional(fills []execution.Fill) decimal.Decimal {
	total := decimal.Zero
	for _, fill := range fills {
		total = total.Add(fill.Price.Mul(fill.Quantity))
	}
	return total
}

func hasCompensation(item execution.Execution) bool {
	if item.Compensation != nil {
		return item.Compensation.Attempted || item.Compensation.ManualRequired || item.Compensation.Quantity.IsPositive()
	}
	for _, leg := range item.Legs {
		if leg.Role == execution.LegRoleCompensation {
			return true
		}
	}
	return false
}
