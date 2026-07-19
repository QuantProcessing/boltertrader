package factoryclient

import "net/http"

// Settings carries validated local construction settings from exchange/factory
// into the private SDK-backed builders.
type Settings struct {
	Endpoint          string
	WebSocketEndpoint string
	HTTPClient        *http.Client
	Environment       string
	AccountAddress    string
}
