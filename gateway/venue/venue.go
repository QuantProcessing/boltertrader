// Package venue declares which exchange markets the gateway serves and how a
// gateway instrument id maps onto exchange-layer instruments.
//
// A market is one (venue, product, settlement) route, e.g. Binance USDT
// perpetuals. The list here is the entire supported surface; anything absent
// stays reachable through the exchange package but is not served.
package venue

import (
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// Market is one tradable route of a venue.
type Market struct {
	Venue      exchange.Venue
	Product    exchange.Product
	Settlement string // "" for spot
}

func (market Market) String() string {
	if market.Settlement == "" {
		return string(market.Venue) + ":" + string(market.Product)
	}
	return string(market.Venue) + ":" + string(market.Product) + ":" + market.Settlement
}

// Markets lists every served route in display order.
var Markets = []Market{
	{exchange.VenueBinance, exchange.ProductPerp, "USDT"},
	{exchange.VenueBinance, exchange.ProductSpot, ""},
	{exchange.VenueOKX, exchange.ProductPerp, "USDT"},
	{exchange.VenueOKX, exchange.ProductSpot, ""},
	{exchange.VenueBybit, exchange.ProductPerp, "USDT"},
	{exchange.VenueBybit, exchange.ProductPerp, "USDC"},
	{exchange.VenueBybit, exchange.ProductSpot, ""},
	{exchange.VenueBitget, exchange.ProductPerp, "USDT"},
	{exchange.VenueBitget, exchange.ProductPerp, "USDC"},
	{exchange.VenueBitget, exchange.ProductSpot, ""},
	{exchange.VenueGate, exchange.ProductPerp, "USDT"},
	{exchange.VenueGate, exchange.ProductSpot, ""},
	{exchange.VenueHyperliquid, exchange.ProductPerp, "USDC"},
	{exchange.VenueHyperliquid, exchange.ProductSpot, ""},
	{exchange.VenueLighter, exchange.ProductPerp, "USDC"},
	{exchange.VenueLighter, exchange.ProductSpot, ""},
}

// Venues lists served venues in display order.
func Venues() []exchange.Venue {
	seen := map[exchange.Venue]bool{}
	var venues []exchange.Venue
	for _, market := range Markets {
		if !seen[market.Venue] {
			seen[market.Venue] = true
			venues = append(venues, market.Venue)
		}
	}
	return venues
}

// MarketsOf returns the served routes of one venue.
func MarketsOf(venue exchange.Venue) []Market {
	var markets []Market
	for _, market := range Markets {
		if market.Venue == venue {
			markets = append(markets, market)
		}
	}
	return markets
}

// Supported reports whether venue is served.
func Supported(venue exchange.Venue) bool { return len(MarketsOf(venue)) > 0 }

// InstrumentID is the gateway's public instrument identity:
//
//	{venue}:{product}:{symbol}
//
// Symbol is the exchange-layer canonical symbol (BASE-QUOTE), which already
// carries the settlement asset for linear perpetuals. Builder-deployed
// Hyperliquid perpetuals keep their dex prefix ("xyz:TSLA-USDC"), so the id
// may contain more than two colons; only the first two are separators.
type InstrumentID string

func NewInstrumentID(venue exchange.Venue, product exchange.Product, symbol string) InstrumentID {
	return InstrumentID(string(venue) + ":" + string(product) + ":" + symbol)
}

// Parse splits an instrument id into its venue, product and symbol.
func (id InstrumentID) Parse() (exchange.Venue, exchange.Product, string, error) {
	parts := strings.SplitN(string(id), ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("instrument id must be venue:product:symbol")
	}
	product := exchange.Product(parts[1])
	if product != exchange.ProductSpot && product != exchange.ProductPerp {
		return "", "", "", fmt.Errorf("instrument id product must be spot or perp")
	}
	return exchange.Venue(parts[0]), product, parts[2], nil
}

// MarketOf resolves the served route an instrument belongs to.
func MarketOf(instrument exchange.Instrument) (Market, bool) {
	identity := instrument.Identity
	settlement := ""
	if identity.Product == exchange.ProductPerp {
		settlement = strings.ToUpper(identity.SettleAsset)
		if settlement == "" {
			settlement = strings.ToUpper(identity.QuoteAsset)
		}
	}
	for _, market := range Markets {
		if market.Venue == identity.Venue && market.Product == identity.Product && market.Settlement == settlement {
			return market, true
		}
	}
	return Market{}, false
}
