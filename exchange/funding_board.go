package exchange

import (
	"time"

	"github.com/shopspring/decimal"
)

// FundingBoardEntry is the normalized, credential-free product projection for
// one perpetual instrument. Collection snapshots and loader contracts are
// adapter-private; only this stable row crosses into Gateway's public board.
type FundingBoardEntry struct {
	Symbol                    string          `json:"symbol"`
	NativeSymbol              string          `json:"native_symbol,omitempty"`
	NativeID                  string          `json:"native_id,omitempty"`
	BaseAsset                 string          `json:"base_asset,omitempty"`
	QuoteAsset                string          `json:"quote_asset,omitempty"`
	Rate                      decimal.Decimal `json:"rate"`
	RateValid                 bool            `json:"-"`
	IntervalHours             OptionalInt     `json:"interval_hours"`
	NextFundingTime           time.Time       `json:"next_funding_time,omitempty"`
	MarkPrice                 OptionalDecimal `json:"mark_price"`
	IndexPrice                OptionalDecimal `json:"index_price"`
	OpenInterest              OptionalDecimal `json:"open_interest"`
	OpenInterestNotional      OptionalDecimal `json:"open_interest_notional"`
	OpenInterestNotionalAsset string          `json:"open_interest_notional_asset,omitempty"`
	FundingObservedAt         time.Time       `json:"funding_observed_at,omitempty"`
	MarkObservedAt            time.Time       `json:"mark_observed_at,omitempty"`
	IndexObservedAt           time.Time       `json:"index_observed_at,omitempty"`
	PriceObservedAt           time.Time       `json:"price_observed_at,omitempty"`
	OpenInterestObservedAt    time.Time       `json:"open_interest_observed_at,omitempty"`
	StaleFields               []string        `json:"stale_fields,omitempty"`
	MissingFields             []string        `json:"missing_fields,omitempty"`
}
