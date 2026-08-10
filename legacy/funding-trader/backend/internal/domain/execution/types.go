package execution

import (
	"time"

	"github.com/shopspring/decimal"
)

type Operation string

const (
	OperationOpen  Operation = "open"
	OperationClose Operation = "close"
)

type State string

const (
	StatePreviewed                  State = "previewed"
	StateSubmitting                 State = "submitting"
	StateLeg1Working                State = "leg_1_working"
	StateLeg2Working                State = "leg_2_working"
	StateHedged                     State = "hedged"
	StatePartiallyHedged            State = "partially_hedged"
	StateCompensating               State = "compensating"
	StateClosed                     State = "closed"
	StateFailed                     State = "failed"
	StateUncertain                  State = "uncertain"
	StateManualInterventionRequired State = "manual_intervention_required"
)

type LegRole string

const (
	LegRoleLong         LegRole = "long"
	LegRoleShort        LegRole = "short"
	LegRoleCompensation LegRole = "compensation"
)

type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

type LegStatus string

const (
	LegStatusAccepted  LegStatus = "accepted"
	LegStatusWorking   LegStatus = "working"
	LegStatusPartial   LegStatus = "partial"
	LegStatusFilled    LegStatus = "filled"
	LegStatusCanceled  LegStatus = "canceled"
	LegStatusRejected  LegStatus = "rejected"
	LegStatusUncertain LegStatus = "uncertain"
)

type ReasonCode string

const (
	ReasonOriginalBlocker      ReasonCode = "original_opportunity_blocker"
	ReasonStaleData            ReasonCode = "stale_data"
	ReasonCapacityUsage        ReasonCode = "capacity_usage"
	ReasonLiquidationBuffer    ReasonCode = "liquidation_buffer"
	ReasonNonPositiveProfit    ReasonCode = "non_positive_net_profit"
	ReasonRiskUnverifiable     ReasonCode = "risk_unverifiable"
	ReasonAccountUnverified    ReasonCode = "account_unverifiable"
	ReasonRulesUnverified      ReasonCode = "rules_unverifiable"
	ReasonMinTradeRules        ReasonCode = "min_trade_rules"
	ReasonInvalidCapital       ReasonCode = "invalid_capital"
	ReasonInvalidLeverage      ReasonCode = "invalid_leverage"
	ReasonInvalidOperation     ReasonCode = "invalid_operation"
	ReasonInvalidOpportunityID ReasonCode = "invalid_opportunity_id"
	ReasonInvalidPositionID    ReasonCode = "invalid_position_id"
	ReasonInvalidPreviewID     ReasonCode = "invalid_preview_id"
	ReasonInvalidSnapshotID    ReasonCode = "invalid_snapshot_id"
	ReasonPositionUnverifiable ReasonCode = "position_unverifiable"
	ReasonCloseExceedsExposure ReasonCode = "close_exceeds_exposure"
	ReasonLowStability         ReasonCode = "low_stability"
	ReasonRecentReversal       ReasonCode = "recent_reversal"
)

type Finding struct {
	Code    ReasonCode `json:"code"`
	Message string     `json:"message"`
}

type LegIdentity struct {
	AccountID        string `json:"accountId"`
	Venue            string `json:"venue"`
	InstrumentID     string `json:"instrumentId"`
	VenueEnvironment string `json:"venueEnvironment"`
}

func (identity LegIdentity) Valid() bool {
	return identity.AccountID != "" &&
		identity.Venue != "" &&
		identity.InstrumentID != "" &&
		identity.VenueEnvironment != ""
}

type PreviewLegInput struct {
	LegIdentity
	Symbol              string
	GatewayInstrumentID string
	MarkPrice           decimal.Decimal
	QuantityIncrement   decimal.Decimal
	MinQuantity         decimal.Decimal
	MinNotional         decimal.Decimal
}

type PreviewInput struct {
	PreviewID                 string
	SnapshotID                string
	PrincipalID               string
	Mode                      string
	Operation                 Operation
	OpportunityID             string
	PositionID                string
	ParentExecutionID         string
	LineageID                 string
	AvailableCapitalUSDT      decimal.Decimal
	CapitalUSDT               decimal.Decimal
	Leverage                  int
	GrossAprPct               decimal.Decimal
	FeesUSDT                  decimal.Decimal
	SlippageUSDT              decimal.Decimal
	DataObservedAt            time.Time
	PreviewedAt               time.Time
	CapacityNotional          decimal.Decimal
	LiquidationBufferPct      decimal.Decimal
	StabilityPct              decimal.Decimal
	RecentReversal            bool
	RiskVerifiable            bool
	AccountVerifiable         bool
	RulesVerifiable           bool
	OriginalBlockers          []Finding
	CurrentPositionVerifiable bool
	CurrentLongQuantity       decimal.Decimal
	CurrentShortQuantity      decimal.Decimal
	Long                      PreviewLegInput
	Short                     PreviewLegInput
}

type PlannedLeg struct {
	LegIdentity
	Role                LegRole         `json:"role"`
	Side                Side            `json:"side"`
	Symbol              string          `json:"symbol"`
	GatewayInstrumentID string          `json:"gatewayInstrumentId"`
	MarkPrice           decimal.Decimal `json:"markPrice"`
	Notional            decimal.Decimal `json:"notional"`
	Quantity            decimal.Decimal `json:"quantity"`
	ReduceOnly          bool            `json:"reduceOnly"`
}

type PreviewResult struct {
	PreviewID            string          `json:"previewId"`
	SnapshotID           string          `json:"snapshotId"`
	PrincipalID          string          `json:"principalId"`
	Mode                 string          `json:"mode"`
	Operation            Operation       `json:"operation"`
	OpportunityID        string          `json:"opportunityId"`
	PositionID           string          `json:"positionId,omitempty"`
	ParentExecutionID    string          `json:"parentExecutionId,omitempty"`
	LineageID            string          `json:"lineageId,omitempty"`
	State                State           `json:"state"`
	PreviewedAt          time.Time       `json:"previewedAt"`
	ExpiresAt            time.Time       `json:"expiresAt"`
	CapitalUSDT          decimal.Decimal `json:"capitalUsdt"`
	Leverage             int             `json:"leverage"`
	ExpectedNetAprPct    decimal.Decimal `json:"expectedNetAprPct"`
	InitialLongQuantity  decimal.Decimal `json:"initialLongQuantity"`
	InitialShortQuantity decimal.Decimal `json:"initialShortQuantity"`
	SingleLegNotional    decimal.Decimal `json:"singleLegNotional"`
	GrossExposure        decimal.Decimal `json:"grossExposure"`
	GrossProfit7d        decimal.Decimal `json:"grossProfit7d"`
	FeesUSDT             decimal.Decimal `json:"feesUsdt"`
	SlippageUSDT         decimal.Decimal `json:"slippageUsdt"`
	NetProfit7d          decimal.Decimal `json:"netProfit7d"`
	MaxSlippagePct       decimal.Decimal `json:"maxSlippagePct"`
	PlannedLegs          []PlannedLeg    `json:"plannedLegs"`
	Blockers             []Finding       `json:"blockers"`
	Warnings             []Finding       `json:"warnings"`
	CanSubmit            bool            `json:"canSubmit"`
}

func (preview PreviewResult) Blocked() bool {
	return len(preview.Blockers) > 0
}
