package account

import "github.com/QuantProcessing/boltertrader/gateway/domain"

// ConnectionSpec describes one local connection and its adapter routes.
// It contains neither credential references nor a desired-state protocol.
type ConnectionSpec struct {
	AccountID         domain.AccountID
	Venue             string
	NativeEnvironment string
	Fence             domain.AccountFence
	Routes            []RouteDefinition
}

type RouteDefinition struct {
	RouteID         domain.RouteID
	Product         string
	SettlementAsset string
	RouteProfileID  string
}
