package lifecycle

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/routeprofile"
)

// Environment is the venue endpoint family an account talks to. The demo and
// testnet members are gone with the endpoints they named, so the type now has
// one member — it stays a type rather than collapsing into a bare string
// because it is persisted and served, and a string there would let any value
// through.
type Environment string

const (
	EnvironmentLive Environment = "live"
)

type Product = routeprofile.Product

const (
	ProductSpot = routeprofile.ProductSpot
	ProductPerp = routeprofile.ProductPerp
)

type RouteState string

const (
	RouteActive   RouteState = "active"
	RouteDisabled RouteState = "disabled"
)

type AccountRoute struct {
	ID              string     `json:"routeId"`
	Product         Product    `json:"product"`
	SettlementAsset string     `json:"settlementAsset,omitempty"`
	RouteProfileID  string     `json:"routeProfileId"`
	RequestedState  RouteState `json:"requestedState"`
}

type RotationFence struct {
	ID                    string    `json:"rotationFenceId"`
	State                 string    `json:"state"`
	FromCredentialVersion int64     `json:"fromCredentialVersion"`
	ToCredentialVersion   int64     `json:"toCredentialVersion"`
	FromRuntimeGeneration int64     `json:"fromRuntimeGeneration"`
	ToRuntimeGeneration   int64     `json:"toRuntimeGeneration"`
	ArmedAt               time.Time `json:"armedAt"`
}

type Account struct {
	ID                   string
	Venue                string
	VenueEnvironment     Environment
	NativeEnvironment    Environment
	DisplayName          string
	ProductIDs           []string
	ProductPolicy        []string
	Routes               []AccountRoute
	SecretRef            string
	PreviousSecretRef    string
	DesiredStateRevision int64
	RuntimeGeneration    int64
	CredentialVersion    int64
	State                State
	Rotation             *RotationFence
	LastValidatedAt      *time.Time
	UpdatedAt            time.Time
}

type PublicAccount struct {
	ID                   string         `json:"accountId"`
	Venue                string         `json:"venue"`
	NativeEnvironment    Environment    `json:"nativeEnvironment"`
	DisplayName          string         `json:"displayName"`
	ProductPolicy        []string       `json:"productPolicy"`
	Routes               []AccountRoute `json:"routes,omitempty"`
	Status               State          `json:"status"`
	DesiredStateRevision int64          `json:"desiredStateRevision"`
	RuntimeGeneration    int64          `json:"runtimeGeneration"`
	CredentialVersion    int64          `json:"credentialVersion"`
	LastValidatedAt      *time.Time     `json:"lastValidatedAt"`
	UpdatedAt            time.Time      `json:"updatedAt"`
}

func (account Account) Public() PublicAccount {
	productIDs := account.ProductIDs
	if len(productIDs) == 0 {
		productIDs = account.ProductPolicy
	}
	return PublicAccount{
		ID: account.ID, Venue: account.Venue, NativeEnvironment: account.NativeEnvironment,
		DisplayName: account.DisplayName, ProductPolicy: append([]string(nil), productIDs...), Routes: append([]AccountRoute(nil), account.Routes...),
		Status: account.State, DesiredStateRevision: account.DesiredStateRevision,
		RuntimeGeneration: account.RuntimeGeneration, CredentialVersion: account.CredentialVersion,
		LastValidatedAt: account.LastValidatedAt, UpdatedAt: account.UpdatedAt,
	}
}

func (account Account) Validate() error {
	if account.ID == "" || account.ID != strings.TrimSpace(account.ID) || account.Venue == "" {
		return errors.New("account identity is invalid")
	}
	// Read back from storage, these are just strings, so a row written before
	// the demo and testnet endpoints were removed has to be rejected rather
	// than silently routed at the live venue.
	if account.VenueEnvironment != EnvironmentLive || account.NativeEnvironment != EnvironmentLive {
		return errors.New("account environment is invalid")
	}
	seen := make(map[string]struct{}, len(account.Routes))
	active := 0
	for _, route := range account.Routes {
		if route.ID == "" || route.ID != strings.TrimSpace(route.ID) || route.RouteProfileID == "" {
			return errors.New("account route is invalid")
		}
		if _, exists := seen[route.ID]; exists {
			return errors.New("account route id is duplicated")
		}
		seen[route.ID] = struct{}{}
		if route.SettlementAsset != strings.ToUpper(route.SettlementAsset) {
			return errors.New("route settlement asset is invalid")
		}
		profile, err := routeprofile.DefaultRegistry().Resolve(account.Venue, route.RouteProfileID)
		if err != nil || profile.Product != route.Product || profile.SettlementAsset != route.SettlementAsset {
			return errors.New("route profile is invalid")
		}
		switch route.RequestedState {
		case RouteActive:
			active++
		case RouteDisabled:
		default:
			return errors.New("route requested state is invalid")
		}
	}
	if (account.State == StateCreating || account.State == StateActive || account.State == StateRotating) && active == 0 {
		return errors.New("at least one active route is required")
	}
	return nil
}

type DesiredState struct {
	AccountID            string         `json:"accountId"`
	Venue                string         `json:"venue"`
	VenueEnvironment     Environment    `json:"venueEnvironment"`
	SecretRef            string         `json:"secretRef"`
	DesiredStateRevision int64          `json:"desiredStateRevision"`
	RuntimeGeneration    int64          `json:"runtimeGeneration"`
	CredentialVersion    int64          `json:"credentialVersion"`
	RequestedState       State          `json:"requestedState"`
	Rotation             *RotationFence `json:"rotationFence,omitempty"`
}

func (account Account) Desired() DesiredState {
	return DesiredState{AccountID: account.ID, Venue: account.Venue, VenueEnvironment: account.VenueEnvironment, SecretRef: account.SecretRef, DesiredStateRevision: account.DesiredStateRevision, RuntimeGeneration: account.RuntimeGeneration, CredentialVersion: account.CredentialVersion, RequestedState: account.State, Rotation: account.Rotation}
}

type CreateRequest struct {
	Venue            string
	VenueEnvironment Environment
	DisplayName      string
	ProductIDs       []string
	SecretMaterial   []byte
	ProductPolicy    []string
	Routes           []AccountRoute
	CredentialBundle []byte
}

type Blockers struct {
	OpenOrders        int
	Reservations      int
	UncertainCommands int
	OwnedPositions    int
}

func (blockers Blockers) Any() bool {
	return blockers.OpenOrders > 0 || blockers.Reservations > 0 || blockers.UncertainCommands > 0 || blockers.OwnedPositions > 0
}

func validateCreate(request CreateRequest) error {
	if strings.TrimSpace(request.Venue) == "" || request.Venue != strings.TrimSpace(request.Venue) || len(request.Venue) > 64 {
		return errors.New("venue is invalid")
	}
	if request.VenueEnvironment != EnvironmentLive {
		return errors.New("venue environment is invalid")
	}
	if err := validateMetadata(request.DisplayName, request.ProductIDs); err != nil {
		return err
	}
	if len(request.SecretMaterial) == 0 {
		return errors.New("secret material is required")
	}
	return nil
}

func validateMetadata(displayName string, productIDs []string) error {
	if err := validateDisplayName(displayName); err != nil {
		return err
	}
	if len(productIDs) == 0 {
		return errors.New("product ids are required")
	}
	seen := make(map[string]struct{}, len(productIDs))
	for _, productID := range productIDs {
		if strings.TrimSpace(productID) == "" || productID != strings.TrimSpace(productID) {
			return errors.New("product id is invalid")
		}
		if _, exists := seen[productID]; exists {
			return errors.New("product id is duplicated")
		}
		seen[productID] = struct{}{}
	}
	return nil
}

func validateDisplayName(displayName string) error {
	if strings.TrimSpace(displayName) == "" || displayName != strings.TrimSpace(displayName) || len(displayName) > 128 {
		return errors.New("display name is invalid")
	}
	return nil
}
