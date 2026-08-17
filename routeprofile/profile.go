package routeprofile

type Product string

const (
	ProductSpot Product = "spot"
	ProductPerp Product = "perp"
)

// Key identifies a route profile the way callers hold it: the venue they are
// talking to and the profile they picked on it.
//
// It used to carry a mode as well, back when the same venue published one
// profile for its demo endpoints and another for its real ones. There is only
// one endpoint family now, so a mode in the key would be a field with one
// possible value that every caller still had to supply correctly.
type Key struct {
	Venue          string
	RouteProfileID string
}

type Profile struct {
	ID              string
	Venue           string
	Product         Product
	SettlementAsset string
}
