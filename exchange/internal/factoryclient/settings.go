package factoryclient

import (
	"net/http"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// Settings carries validated local construction settings from exchange/factory
// into the private SDK-backed builders.
type Settings struct {
	Endpoint          string
	WebSocketEndpoint string
	HTTPClient        *http.Client
	AccountAddress    string
	InstrumentCatalog exchange.InstrumentCatalog
	CatalogScope      exchange.CatalogScope
	SourceDescriptor  exchange.SourceDescriptor
}
