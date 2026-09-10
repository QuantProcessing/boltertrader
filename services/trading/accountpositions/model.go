// Package accountpositions queries and aggregates current venue positions from
// Gateway. It does not persist positions or reconstruct them from local fills.
package accountpositions

import (
	"context"
	"strings"
	"time"
)

type Freshness string

const (
	FreshnessSyncing     Freshness = "syncing"
	FreshnessLive        Freshness = "live"
	FreshnessDelayed     Freshness = "delayed"
	FreshnessStale       Freshness = "stale"
	FreshnessUnavailable Freshness = "unavailable"
)

func (freshness Freshness) Valid() bool {
	switch freshness {
	case FreshnessSyncing, FreshnessLive, FreshnessDelayed, FreshnessStale, FreshnessUnavailable:
		return true
	default:
		return false
	}
}

type RouteHealth string

const (
	RouteLive        RouteHealth = "live"
	RouteDegraded    RouteHealth = "degraded"
	RouteUnavailable RouteHealth = "unavailable"
)

type AccountRoute struct {
	RouteID         string `json:"routeId"`
	Product         string `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
}

type Account struct {
	AccountID   string         `json:"accountId"`
	Label       string         `json:"label"`
	Venue       string         `json:"venue"`
	Environment string         `json:"environment"`
	Status      string         `json:"status"`
	Routes      []AccountRoute `json:"routes"`
}

type OptionalDecimal struct {
	Value string `json:"value"`
	Valid bool   `json:"valid"`
}

type SourcePosition struct {
	NativeSymbol     string          `json:"native_symbol,omitempty"`
	NativeID         string          `json:"native_id,omitempty"`
	PositionSlot     string          `json:"position_slot"`
	Side             string          `json:"side,omitempty"`
	Quantity         string          `json:"quantity"`
	EntryPrice       string          `json:"entry_price"`
	MarkPrice        string          `json:"mark_price"`
	UnrealizedPnL    string          `json:"unrealized_pnl"`
	LiquidationPrice OptionalDecimal `json:"liquidation_price"`
	Leverage         OptionalDecimal `json:"leverage"`
	MarginUsed       OptionalDecimal `json:"margin_used"`
}

type SourceItem struct {
	AccountID       string         `json:"account_id"`
	RouteID         string         `json:"route_id"`
	InstrumentID    string         `json:"instrument_id"`
	PositionSlot    string         `json:"position_slot"`
	Position        SourcePosition `json:"position"`
	ResourceVersion uint64         `json:"resource_version"`
	ObservedAt      time.Time      `json:"observed_at"`
	VerifiedAt      time.Time      `json:"verified_at"`
}

type SourceSnapshot struct {
	AccountID  string       `json:"account_id"`
	Freshness  Freshness    `json:"freshness"`
	Reason     string       `json:"reason,omitempty"`
	VerifiedAt time.Time    `json:"verified_at"`
	Items      []SourceItem `json:"items"`
}

// Source reads the current account directory and venue position snapshots.
type Source interface {
	ListAccounts(context.Context, string) ([]Account, error)
	PositionSnapshot(context.Context, string, string) (SourceSnapshot, error)
}

type Key struct {
	AccountID    string `json:"accountId"`
	RouteID      string `json:"routeId"`
	InstrumentID string `json:"instrumentId"`
	PositionSlot string `json:"positionSlot"`
}

func (key Key) String() string {
	return key.AccountID + "\x00" + key.RouteID + "\x00" + key.InstrumentID + "\x00" + key.PositionSlot
}

func (key Key) Valid() bool {
	return strings.TrimSpace(key.AccountID) != "" && strings.TrimSpace(key.RouteID) != "" &&
		strings.TrimSpace(key.InstrumentID) != "" && validSlot(key.PositionSlot)
}

type Item struct {
	Key
	AccountLabel          string      `json:"accountLabel"`
	Venue                 string      `json:"venue"`
	Environment           string      `json:"environment"`
	Product               string      `json:"product"`
	SettlementAsset       string      `json:"settlementAsset,omitempty"`
	Symbol                string      `json:"symbol"`
	Side                  string      `json:"side"`
	Quantity              string      `json:"quantity"`
	EntryPrice            string      `json:"entryPrice,omitempty"`
	MarkPrice             string      `json:"markPrice,omitempty"`
	MarkPriceSource       string      `json:"markPriceSource"`
	UnrealizedPnL         string      `json:"unrealizedPnl,omitempty"`
	UnrealizedPnLCurrency string      `json:"unrealizedPnlCurrency,omitempty"`
	UnrealizedPnLSource   string      `json:"unrealizedPnlSource"`
	LiquidationPrice      string      `json:"liquidationPrice,omitempty"`
	Leverage              string      `json:"leverage,omitempty"`
	MarginUsed            string      `json:"marginUsed,omitempty"`
	MarginMode            string      `json:"marginMode,omitempty"`
	RouteHealth           RouteHealth `json:"routeHealth"`
	Freshness             Freshness   `json:"freshness"`
	WriteReady            bool        `json:"writeReady"`
	BlockedReason         string      `json:"blockedReason,omitempty"`
	ResourceVersion       uint64      `json:"resourceVersion"`
	ObservedAt            time.Time   `json:"observedAt"`
	VerifiedAt            time.Time   `json:"verifiedAt"`
	ReceivedAt            time.Time   `json:"receivedAt"`
}

type AccountState struct {
	Account
	RouteHealth RouteHealth `json:"routeHealth"`
	Freshness   Freshness   `json:"freshness"`
	WriteReady  bool        `json:"writeReady"`
	Reason      string      `json:"reason,omitempty"`
	VerifiedAt  time.Time   `json:"verifiedAt,omitempty"`
	ReceivedAt  time.Time   `json:"receivedAt"`
}

type SettlementSummary struct {
	SettlementAsset  string `json:"settlementAsset"`
	PositionCount    int    `json:"positionCount"`
	LiveCount        int    `json:"liveCount"`
	StaleCount       int    `json:"staleCount"`
	UnrealizedPnL    string `json:"unrealizedPnl,omitempty"`
	ExcludedPnLCount int    `json:"excludedPnlCount"`
}

type Summary struct {
	PositionCount     int                 `json:"positionCount"`
	AccountCount      int                 `json:"accountCount"`
	StaleAccountCount int                 `json:"staleAccountCount"`
	BySettlementAsset []SettlementSummary `json:"bySettlementAsset"`
}

type Snapshot struct {
	ProjectionRevision uint64         `json:"projectionRevision"`
	VerifiedAt         time.Time      `json:"verifiedAt"`
	Summary            Summary        `json:"summary"`
	Accounts           []AccountState `json:"accounts"`
	Items              []Item         `json:"items"`
}

type Filter struct {
	AccountID    string
	InstrumentID string
	Venue        string
	Product      string
	Status       Freshness
}

type Reader interface {
	Snapshot(context.Context, string, Filter) (Snapshot, error)
}

func validSlot(slot string) bool {
	switch slot {
	case "net", "long", "short":
		return true
	default:
		return false
	}
}
