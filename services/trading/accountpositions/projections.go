package accountpositions

import "time"

// Application projections are owned here, independently of HTTP handlers.
type AccountRuntimeProjection struct {
	AccountID  string    `json:"accountId"`
	RuntimeID  string    `json:"-"`
	Connected  bool      `json:"connected"`
	ObservedAt time.Time `json:"observedAt"`
}

type AccountBalanceProjection struct {
	AccountID       string `json:"accountId"`
	Product         string `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
	Asset           string `json:"asset"`
	Available       string `json:"available"`
	AvailableMargin string `json:"availableMargin,omitempty"`
	Locked          string `json:"locked"`
	Total           string `json:"total"`
}

type AccountPositionProjection struct {
	AccountID     string `json:"accountId"`
	InstrumentID  string `json:"instrumentId"`
	Side          string `json:"side,omitempty"`
	Quantity      string `json:"quantity"`
	EntryPrice    string `json:"entryPrice"`
	MarkPrice     string `json:"markPrice"`
	UnrealizedPnL string `json:"unrealizedPnl"`
}
