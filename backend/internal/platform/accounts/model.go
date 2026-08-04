package accounts

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/desiredstate"
)

type State = desiredstate.State

const (
	StateCreating = desiredstate.StateCreating
	StateActive   = desiredstate.StateActive
	StateRotating = desiredstate.StateRotating
	StateDraining = desiredstate.StateDraining
	StateDrained  = desiredstate.StateDrained
	StateDisabled = desiredstate.StateDisabled
	StateRemoving = desiredstate.StateRemoving
	StateRemoved  = desiredstate.StateRemoved
)

type Environment string

const (
	EnvironmentDemo    Environment = "demo"
	EnvironmentTestnet Environment = "testnet"
	EnvironmentLive    Environment = "live"
)

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
	DisplayName          string
	ProductIDs           []string
	SecretRef            string
	DesiredStateRevision int64
	RuntimeGeneration    int64
	CredentialVersion    int64
	State                State
	Rotation             *RotationFence
	LastValidatedAt      *time.Time
	UpdatedAt            time.Time
}

type PublicAccount struct {
	ID                   string      `json:"accountId"`
	Venue                string      `json:"venue"`
	VenueEnvironment     Environment `json:"venueEnvironment"`
	DisplayName          string      `json:"displayName"`
	ProductIDs           []string    `json:"productIds"`
	Status               State       `json:"status"`
	DesiredStateRevision int64       `json:"desiredStateRevision"`
	RuntimeGeneration    int64       `json:"runtimeGeneration"`
	CredentialVersion    int64       `json:"credentialVersion"`
	LastValidatedAt      *time.Time  `json:"lastValidatedAt"`
	UpdatedAt            time.Time   `json:"updatedAt"`
}

func (account Account) Public() PublicAccount {
	return PublicAccount{
		ID: account.ID, Venue: account.Venue, VenueEnvironment: account.VenueEnvironment,
		DisplayName: account.DisplayName, ProductIDs: append([]string(nil), account.ProductIDs...),
		Status: account.State, DesiredStateRevision: account.DesiredStateRevision,
		RuntimeGeneration: account.RuntimeGeneration, CredentialVersion: account.CredentialVersion,
		LastValidatedAt: account.LastValidatedAt, UpdatedAt: account.UpdatedAt,
	}
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
	switch request.VenueEnvironment {
	case EnvironmentDemo, EnvironmentTestnet, EnvironmentLive:
	default:
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
	if strings.TrimSpace(displayName) == "" || displayName != strings.TrimSpace(displayName) || len(displayName) > 128 {
		return errors.New("display name is invalid")
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
