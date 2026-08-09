package routeprofile

type Mode string

const (
	ModeTest Mode = "test"
	ModeLive Mode = "live"
)

type NativeEnvironment string

const (
	EnvironmentDemo    NativeEnvironment = "demo"
	EnvironmentTestnet NativeEnvironment = "testnet"
	EnvironmentLive    NativeEnvironment = "live"
)

type Product string

const (
	ProductSpot Product = "spot"
	ProductPerp Product = "perp"
)

type Key struct {
	Venue          string
	Mode           Mode
	RouteProfileID string
}

type Profile struct {
	ID                string
	Venue             string
	Mode              Mode
	NativeEnvironment NativeEnvironment
	Product           Product
	SettlementAsset   string
}
