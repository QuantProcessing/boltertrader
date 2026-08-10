package boltertrader

import (
	"encoding/json"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/reference"
	"github.com/QuantProcessing/funding-trader/backend/internal/config"
)

type VenueEnvironment string

const (
	GatewayDemo    VenueEnvironment = "demo"
	GatewayTestnet VenueEnvironment = "testnet"
	GatewayLive    VenueEnvironment = "live"
)

type GatewayCollection[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type GatewayErrorEnvelope struct {
	Error             GatewayErrorBody `json:"error"`
	ExistingCommandID string           `json:"existing_command_id,omitempty"`
}

type GatewayErrorBody struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlation_id"`
	Retryable     bool   `json:"retryable"`
}

type GatewayLiveness struct {
	Live bool `json:"live"`
}

type GatewayReadiness struct {
	Ready   bool     `json:"ready"`
	Reasons []string `json:"reasons,omitempty"`
}

type AccountResource struct {
	AccountID        string           `json:"account_id"`
	Venue            string           `json:"venue"`
	Product          string           `json:"product"`
	VenueEnvironment VenueEnvironment `json:"environment"`
	Settlement       string           `json:"settlement,omitempty"`
	ReadReady        bool             `json:"read_ready"`
	WriteReady       bool             `json:"write_ready"`
	Reason           string           `json:"reason,omitempty"`
}

type AccountStatus struct {
	AccountResource
	ClientReady bool          `json:"client_ready"`
	Catalog     CatalogStatus `json:"catalog"`
}

type CatalogStatus struct {
	Scope      CatalogScope       `json:"scope"`
	Revision   string             `json:"revision,omitempty"`
	Generation uint64             `json:"generation"`
	LoadedAt   time.Time          `json:"loaded_at,omitempty"`
	Age        DurationNanosecond `json:"age"`
	Stale      bool               `json:"stale"`
	ReadReady  bool               `json:"read_ready"`
	WriteReady bool               `json:"write_ready"`
	Reason     string             `json:"reason,omitempty"`
	LastError  string             `json:"last_error,omitempty"`
}

type CatalogScope struct {
	Venue            string           `json:"venue"`
	VenueEnvironment VenueEnvironment `json:"environment"`
	Product          string           `json:"product"`
	Settlement       string           `json:"settlement,omitempty"`
}

type DurationNanosecond int64

type AccountCapability struct {
	AccountID  string        `json:"account_id"`
	Capability CapabilityRow `json:"capability"`
}

type CapabilityRow struct {
	Code                           string               `json:"code"`
	RowID                          string               `json:"row_id"`
	Venue                          string               `json:"venue"`
	Product                        string               `json:"product"`
	Settlement                     string               `json:"settlement,omitempty"`
	WriteReadyDefault              bool                 `json:"write_ready_default"`
	ExactLookup                    string               `json:"exact_lookup"`
	ExactLookupStrategy            string               `json:"exact_lookup_strategy"`
	ExactFixture                   string               `json:"exact_fixture"`
	PromotionEvidenceRequired      string               `json:"promotion_evidence_required"`
	HistoryPaginationProvesAbsence bool                 `json:"history_pagination_proves_absence"`
	Operations                     CapabilityOperations `json:"operations"`
}

type CapabilityOperations struct {
	Place                  bool `json:"place"`
	Amend                  bool `json:"amend"`
	Cancel                 bool `json:"cancel"`
	OpenOrders             bool `json:"open_orders"`
	OrderHistory           bool `json:"order_history"`
	Fills                  bool `json:"fills"`
	Balances               bool `json:"balances"`
	Account                bool `json:"account"`
	Positions              bool `json:"positions"`
	Funding                bool `json:"funding"`
	AccountFundingPayments bool `json:"account_funding_payments"`
	Leverage               bool `json:"leverage"`
	PrivateStream          bool `json:"private_stream"`
	MarginMode             bool `json:"margin_mode"`
	PositionMode           bool `json:"position_mode"`
	FundsTransfer          bool `json:"funds_transfer"`
}

type InstrumentResource struct {
	InstrumentID     string           `json:"instrument_id"`
	Venue            string           `json:"venue"`
	VenueEnvironment VenueEnvironment `json:"environment"`
	Product          string           `json:"product"`
	Settlement       string           `json:"settlement,omitempty"`
	Symbol           string           `json:"symbol"`
	BaseAsset        string           `json:"base_asset"`
	QuoteAsset       string           `json:"quote_asset"`
	Rules            catalog.Rules    `json:"rules"`
}

type InstrumentRulesResource struct {
	InstrumentID string        `json:"instrument_id"`
	Rules        catalog.Rules `json:"rules"`
}

type GatewayPageRequest struct {
	AccountID      string
	InstrumentID   string
	GatewayOrderID string
	Cursor         string
	Limit          int
}

type OrderPage struct {
	Items      []OrderResult
	NextCursor string
}

type FillPage struct {
	Items      []FillResource
	NextCursor string
}

type RiskLimitReference struct {
	Available bool                  `json:"available"`
	Reason    string                `json:"reason"`
	Limits    []reference.RiskLimit `json:"limits"`
}

type FillResource struct {
	AccountID      string        `json:"account_id"`
	InstrumentID   string        `json:"instrument_id,omitempty"`
	GatewayOrderID string        `json:"gateway_order_id,omitempty"`
	Fill           exchange.Fill `json:"fill"`
}

type BalanceResource struct {
	AccountID string           `json:"account_id"`
	Balance   exchange.Balance `json:"balance"`
}

type PositionResource struct {
	AccountID    string            `json:"account_id"`
	InstrumentID string            `json:"instrument_id,omitempty"`
	Position     exchange.Position `json:"position"`
}

type FundingRateResource struct {
	AccountID    string               `json:"account_id"`
	InstrumentID string               `json:"instrument_id"`
	FundingRate  exchange.FundingRate `json:"funding_rate"`
}

type GatewayFundingPaymentRequest struct {
	AccountID       string
	InstrumentID    string
	SettlementAsset string
	Start           time.Time
	End             time.Time
	Cursor          string
	Limit           int
}

type FundingPaymentPageResource struct {
	Items               []FundingPaymentResource         `json:"items"`
	NextCursor          string                           `json:"next_cursor,omitempty"`
	Scope               FundingPaymentScopeResource      `json:"scope"`
	AccountID           string                           `json:"account_id"`
	InstrumentID        string                           `json:"instrument_id,omitempty"`
	SettlementAsset     string                           `json:"settlement_asset,omitempty"`
	PaginationTraversal FundingPaymentTraversalResource  `json:"pagination_traversal"`
	Start               time.Time                        `json:"start,omitempty"`
	End                 time.Time                        `json:"end,omitempty"`
	Page                exchange.PageInfo                `json:"page"`
	Watermark           exchange.FundingPaymentWatermark `json:"watermark"`
	Status              exchange.FundingPaymentStatus    `json:"status"`
	AsOf                time.Time                        `json:"as_of"`
	CompleteThrough     time.Time                        `json:"complete_through,omitempty"`
	Revision            int64                            `json:"revision,omitempty"`
}

type FundingPaymentScopeResource struct {
	AccountID       string    `json:"account_id"`
	InstrumentID    string    `json:"instrument_id,omitempty"`
	AllInstruments  bool      `json:"all_instruments"`
	SettlementAsset string    `json:"settlement_asset,omitempty"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
}

type FundingPaymentTraversalResource struct {
	Cursor       string `json:"cursor,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	HasMoreKnown bool   `json:"has_more_known"`
	HasMore      bool   `json:"has_more"`
}

type FundingPaymentResource struct {
	PaymentID        string           `json:"payment_id,omitempty"`
	FactID           string           `json:"fact_id"`
	SupersedesFactID string           `json:"supersedes_fact_id,omitempty"`
	AccountID        string           `json:"account_id"`
	InstrumentID     string           `json:"instrument_id"`
	Venue            exchange.Venue   `json:"venue"`
	VenueEnvironment string           `json:"venue_environment"`
	Product          exchange.Product `json:"product"`
	SettlementAsset  string           `json:"settlement_asset"`
	Amount           string           `json:"amount"`
	EffectiveAt      time.Time        `json:"effective_at,omitempty"`
	ObservedAt       time.Time        `json:"observed_at,omitempty"`
	Revision         int64            `json:"revision,omitempty"`
	Version          string           `json:"version,omitempty"`
	SourceRef        string           `json:"source_ref,omitempty"`
	FormulaVersion   string           `json:"formula_version,omitempty"`
}

type OrderResult struct {
	CommandID       string `json:"command_id"`
	GatewayOrderID  string `json:"gateway_order_id"`
	ClientOrderID   string `json:"client_order_id"`
	State           string `json:"state"`
	ResourceVersion int64  `json:"resource_version"`
}

type PlaceOrderRequest struct {
	AccountID      string `json:"account_id"`
	InstrumentID   string `json:"instrument_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Side           string `json:"side"`
	Type           string `json:"type"`
	Quantity       string `json:"quantity"`
	LimitPrice     string `json:"limit_price,omitempty"`
	LimitPolicy    string `json:"limit_policy,omitempty"`
	ReduceOnly     bool   `json:"reduce_only,omitempty"`
}

type CancelOrderRequest struct {
	AccountID      string
	GatewayOrderID string
}

type PrivateEvent struct {
	BootID          string          `json:"boot_id"`
	Sequence        uint64          `json:"sequence"`
	EventID         string          `json:"event_id"`
	AccountID       string          `json:"account_id"`
	ResourceVersion int64           `json:"resource_version"`
	Kind            string          `json:"kind"`
	OccurredAt      time.Time       `json:"occurred_at"`
	Payload         json.RawMessage `json:"payload"`
}

type ResyncPayload struct {
	Reason    string   `json:"reason"`
	Resources []string `json:"resources"`
}

type PrivateEventSubscribeRequest struct {
	AccountID string
	Cursor    string
	Kinds     []string
}

type AccountDiscovery struct {
	Mode     config.Mode
	Accounts []DiscoveredAccount
	Mixed    bool
	Ready    bool
	Reasons  []string
}

type DiscoveredAccount struct {
	AccountID        string
	Venue            string
	Product          string
	VenueEnvironment VenueEnvironment
	Settlement       string
	ReadReady        bool
	WriteReady       bool
	Reason           string
	Capability       CapabilityRow
}
