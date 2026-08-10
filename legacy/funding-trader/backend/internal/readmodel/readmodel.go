package readmodel

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrNotFound      = errors.New("read-model item not found")
	ErrReplayExpired = errors.New("read-model replay window expired")
)

type EventKind string

const ContractVersion = "private-v1-mode-1"

const (
	SnapshotEvent                     EventKind = "snapshot"
	SystemStatus                      EventKind = "system.status"
	OpportunityUpsert                 EventKind = "opportunity.upsert"
	AlertUpsert                       EventKind = "alert.upsert"
	ExecutionUpsert                   EventKind = "execution.upsert"
	PositionUpsert                    EventKind = "position.upsert"
	ActivityAppend                    EventKind = "activity.append"
	AuditAppend                       EventKind = "audit.append"
	ResnapshotRequired                EventKind = "resnapshot.required"
	LedgerFundingPaymentAppend        EventKind = "ledger.funding_payment.append"
	LedgerPositionVerificationChanged EventKind = "ledger.position.verification_changed"
	LedgerPortfolioUpdated            EventKind = "ledger.portfolio.updated"
	VerificationStatus                EventKind = "verification.status"
	Heartbeat                         EventKind = "heartbeat"
)

type Scope struct {
	PrincipalID string
	Mode        string
}

type PageQuery struct {
	Cursor string
	Limit  int
}

type OpportunitySort string

const (
	NetAPRDescending OpportunitySort = "netApr_desc"
	NetAPRAscending  OpportunitySort = "netApr_asc"
)

type OpportunityQuery struct {
	PageQuery
	Sort         OpportunitySort
	ShortVenueID string
	Status       string
}

type AlertQuery struct {
	PageQuery
	Severity string
}

type PositionQuery struct {
	PageQuery
	States []string
}

type ActivityQuery struct {
	PageQuery
	Kinds []string
	From  time.Time
	To    time.Time
}

type ExecutionQuery struct {
	PageQuery
	States []string
}

type AuditQuery struct {
	PageQuery
}

type Page struct {
	Items      []json.RawMessage
	NextCursor string
	AsOf       time.Time
	Counts     map[string]int
}

type Reader interface {
	ListOpportunities(context.Context, Scope, OpportunityQuery) (Page, error)
	Opportunity(context.Context, Scope, string) (json.RawMessage, error)
	ListAlerts(context.Context, Scope, AlertQuery) (Page, error)
	ListPositions(context.Context, Scope, PositionQuery) (Page, error)
	ListActivities(context.Context, Scope, ActivityQuery) (Page, error)
	ListAuditEvents(context.Context, Scope, AuditQuery) (Page, error)
	Snapshot(context.Context, Scope, []EventKind) (Snapshot, error)
}

type ExecutionReader interface {
	ListExecutions(context.Context, Scope, ExecutionQuery) (Page, error)
	Execution(context.Context, Scope, string) (json.RawMessage, error)
}

type Snapshot struct {
	Opportunities []json.RawMessage `json:"opportunities,omitempty"`
	Alerts        []json.RawMessage `json:"alerts,omitempty"`
	Executions    []json.RawMessage `json:"executions,omitempty"`
	Positions     []json.RawMessage `json:"positions,omitempty"`
	Activities    []json.RawMessage `json:"activities,omitempty"`
	AuditEvents   []json.RawMessage `json:"auditEvents,omitempty"`
	AsOf          time.Time         `json:"asOf"`
}

type Projection struct {
	ID             string
	Status         string
	Symbol         string
	LongAccountID  string
	ShortAccountID string
	NetAPR         decimal.Decimal
	FormulaVersion string
	ObservedAt     time.Time
	Payload        json.RawMessage
}

type AlertProjection struct {
	Projection
	OpportunityID string
	AccountID     string
	InstrumentID  string
}

type PositionProjection struct {
	ID        string
	Scope     Scope
	Symbol    string
	Status    string
	UpdatedAt time.Time
	Payload   json.RawMessage
}

type ActivityProjection struct {
	ID          string
	Scope       Scope
	Kind        string
	PositionID  string
	ExecutionID string
	OccurredAt  time.Time
	Payload     json.RawMessage
}

type FundingObservation struct {
	AccountID        string
	InstrumentID     string
	VenueEnvironment string
	FundingTime      time.Time
	ObservedAt       time.Time
	NextFundingTime  time.Time
	Rate             decimal.Decimal
	MarkPrice        decimal.Decimal
	HasMarkPrice     bool
}

type MarketSnapshot struct {
	Mode          string
	ObservedAt    time.Time
	Opportunities []Projection
	Alerts        []AlertProjection
	Funding       []FundingObservation
}

type Event struct {
	ID              int64
	BootID          string
	PrincipalID     string
	Mode            string
	ContractVersion string
	Kind            EventKind
	Scope           []string
	EntityID        string
	Payload         json.RawMessage
	OccurredAt      time.Time
}
