package ledgerfacts

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrFactConflict          = errors.New("ledger fact idempotency conflict")
	ErrAppendOnlyViolation   = errors.New("ledger append-only violation")
	ErrRevisionOrder         = errors.New("ledger source revision must increase")
	ErrCompletenessViolation = errors.New("ledger completeness violation")
	ErrActiveLineageExists   = errors.New("active lineage already exists")
	ErrFactNotFound          = errors.New("ledger fact not found")
	ErrScopeMismatch         = errors.New("ledger scope mismatch")
)

type CompletenessStatus string

const (
	AllInstruments                            = "*"
	Unsupported            CompletenessStatus = "unsupported"
	TemporarilyUnavailable CompletenessStatus = "temporarily_unavailable"
	Incomplete             CompletenessStatus = "incomplete"
	CompleteEmpty          CompletenessStatus = "complete_empty"
	Complete               CompletenessStatus = "complete"
)

type Money struct {
	Asset  string
	Amount decimal.Decimal
}

type ConversionMetadata struct {
	Source         string
	AsOf           time.Time
	RoundingMode   string
	Scale          int
	FormulaVersion string
}

type Residual struct {
	LegQuantity            decimal.Decimal
	ReportingAssetNotional decimal.Decimal
	OpenOrderCount         int
	PendingCompensation    int
	BlockerClass           string
}

type Scope struct {
	PrincipalID         string
	Mode                string
	AccountID           string
	InstrumentID        string
	WindowStart         time.Time
	WindowEnd           time.Time
	PaginationTraversal string
}

func (s Scope) AllInstruments() bool {
	return s.InstrumentID == AllInstruments
}

type Provenance struct {
	SourceRef      string
	SourceRevision string
	SourceVersion  string
	ObservedAt     time.Time
	EffectiveAt    time.Time
}

type FundingPaymentFact struct {
	FactID           string
	SupersedesFactID string
	SyncWatermarkID  string
	PaymentID        string
	PositionID       string
	Venue            string
	VenueEnvironment string
	Product          string
	FormulaVersion   string
	Scope            Scope
	Provenance       Provenance
	Payment          Money
	SequenceID       int64
}

type FillCashflowFact struct {
	FactID           string
	SupersedesFactID string
	SyncWatermarkID  string
	Scope            Scope
	PositionID       string
	LineageID        string
	FillID           string
	Provenance       Provenance
	Fee              Money
	Rebate           Money
	SourceAsset      string
	ReportingAsset   string
	Conversion       ConversionMetadata
	AlreadySigned    bool
	SequenceID       int64
}

type PositionSnapshotFact struct {
	FactID                string
	SupersedesFactID      string
	SyncWatermarkID       string
	Scope                 Scope
	PositionID            string
	Provenance            Provenance
	Closed                bool
	AsOf                  time.Time
	ReportingAsset        string
	RealizedTradingPnl    decimal.Decimal
	UnrealizedTradingPnl  decimal.Decimal
	ActualFundingCashFlow decimal.Decimal
	Fees                  decimal.Decimal
	Verified              bool
	Residual              Residual
	Conversion            ConversionMetadata
	SequenceID            int64
}

type LineageFact struct {
	FactID           string
	Scope            Scope
	LineageID        string
	PositionID       string
	VenueEnvironment string
	Product          string
	Provenance       Provenance
	Active           bool
	ClosedAt         time.Time
	SequenceID       int64
}

type LineageRevisionFact struct {
	FactID           string
	SupersedesFactID string
	SyncWatermarkID  string
	LineageFactID    string
	Scope            Scope
	Revision         string
	Status           string
	Reason           string
	Provenance       Provenance
	SequenceID       int64
}

type SyncWatermark struct {
	FactID           string
	Scope            Scope
	SourceRef        string
	Cursor           string
	Revision         string
	ObservedAt       time.Time
	EffectiveThrough time.Time
	CompleteThrough  time.Time
	Status           CompletenessStatus
	Verified         bool
	SequenceID       int64
}

type FundingIngestBatch struct {
	Scope     Scope
	Payments  []FundingPaymentFact
	Watermark SyncWatermark
}

type FundingPaymentQuery struct {
	PrincipalID       string
	Mode              string
	AccountID         string
	InstrumentID      string
	PositionID        string
	From              time.Time
	To                time.Time
	Limit             int
	CursorEffectiveAt time.Time
	CursorSequenceID  int64
}

type ReplayEntry struct {
	Kind         string
	OrderKey     string
	Revision     string
	Marker       string
	SequenceID   int64
	Funding      *FundingPaymentFact
	Fill         *FillCashflowFact
	Position     *PositionSnapshotFact
	Lineage      *LineageFact
	RevisionFact *LineageRevisionFact
	Watermark    *SyncWatermark
}
