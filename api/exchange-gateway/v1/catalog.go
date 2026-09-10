package gatewayv1

import "time"

type CatalogVenue struct {
	Venue       string `json:"venue"`
	Environment string `json:"environment"`
	Ready       bool   `json:"ready"`
}

type CatalogInstrument struct {
	InstrumentID         string     `json:"instrumentId"`
	Venue                string     `json:"venue"`
	Environment          string     `json:"environment"`
	Product              string     `json:"product"`
	Settlement           string     `json:"settlement,omitempty"`
	Symbol               string     `json:"symbol"`
	BaseAsset            string     `json:"baseAsset"`
	QuoteAsset           string     `json:"quoteAsset"`
	MarketFamily         string     `json:"marketFamily"`
	AssetClass           string     `json:"assetClass"`
	ClassificationSource string     `json:"classificationSource,omitempty"`
	RawAssetType         string     `json:"rawAssetType,omitempty"`
	RawCategories        []string   `json:"rawCategories,omitempty"`
	Rules                TradeRules `json:"rules"`
	Ready                bool       `json:"ready"`
}

type CatalogInstruments struct {
	Revision    string              `json:"revision"`
	ObservedAt  time.Time           `json:"observedAt"`
	Instruments []CatalogInstrument `json:"instruments"`
}

type TradeRules struct {
	PriceIncrement    string `json:"priceIncrement"`
	QuantityIncrement string `json:"quantityIncrement"`
	MinQuantity       string `json:"minQuantity"`
	MinNotional       string `json:"minNotional,omitempty"`
}
