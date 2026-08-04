package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

var (
	ErrDesiredStateRevisionConflict = errors.New("gateway desired-state revision conflict")
	ErrStaleRuntimeGeneration       = errors.New("gateway stale runtime generation")
	ErrInvalidAccountTransition     = errors.New("gateway invalid account transition")
)

type DesiredAccountState string

const (
	DesiredCreating DesiredAccountState = "creating"
	DesiredActive   DesiredAccountState = "active"
	DesiredRotating DesiredAccountState = "rotating"
	DesiredDraining DesiredAccountState = "draining"
	DesiredDrained  DesiredAccountState = "drained"
	DesiredDisabled DesiredAccountState = "disabled"
	DesiredRemoving DesiredAccountState = "removing"
	DesiredRemoved  DesiredAccountState = "removed"
)

func (state DesiredAccountState) Validate() error {
	switch state {
	case DesiredCreating, DesiredActive, DesiredRotating, DesiredDraining,
		DesiredDrained, DesiredDisabled, DesiredRemoving, DesiredRemoved:
		return nil
	default:
		return fmt.Errorf("desired account state is invalid")
	}
}

type RotationFenceState string

const (
	RotationArmed            RotationFenceState = "armed"
	RotationReplacementReady RotationFenceState = "replacement_ready"
	RotationReleased         RotationFenceState = "released"
	RotationExpired          RotationFenceState = "expired"
	RotationBlocked          RotationFenceState = "blocked"
)

type RotationFence struct {
	ID                    string
	State                 RotationFenceState
	FromCredentialVersion int64
	ToCredentialVersion   int64
	FromRuntimeGeneration int64
	ToRuntimeGeneration   int64
	ArmedAt               time.Time
	ReleasedAt            *time.Time
	BlockedReason         string
}

func (fence RotationFence) Validate() error {
	if err := validateOpaqueValue("rotation fence id", fence.ID, 256); err != nil {
		return err
	}
	switch fence.State {
	case RotationArmed, RotationReplacementReady, RotationReleased, RotationExpired, RotationBlocked:
	default:
		return fmt.Errorf("rotation fence state is invalid")
	}
	if fence.FromCredentialVersion <= 0 || fence.ToCredentialVersion <= fence.FromCredentialVersion {
		return fmt.Errorf("rotation credential versions are invalid")
	}
	if fence.FromRuntimeGeneration <= 0 || fence.ToRuntimeGeneration <= fence.FromRuntimeGeneration {
		return fmt.Errorf("rotation runtime generations are invalid")
	}
	if fence.ArmedAt.IsZero() {
		return fmt.Errorf("rotation armed time is required")
	}
	if fence.ReleasedAt != nil && fence.ReleasedAt.IsZero() {
		return fmt.Errorf("rotation released time is invalid")
	}
	if fence.State == RotationReleased && fence.ReleasedAt == nil {
		return fmt.Errorf("released rotation time is required")
	}
	if len(fence.BlockedReason) > 128 || strings.ContainsAny(fence.BlockedReason, "\n\r\t") {
		return fmt.Errorf("rotation blocked reason is invalid")
	}
	return nil
}

type DesiredAccount struct {
	AccountID            domain.AccountID
	Venue                string
	VenueEnvironment     string
	SecretRef            string
	PreviousSecretRef    string
	DesiredStateRevision int64
	RuntimeGeneration    int64
	CredentialVersion    int64
	RequestedState       DesiredAccountState
	RotationFence        *RotationFence
	UpdatedAt            time.Time
}

func (desired DesiredAccount) Validate() error {
	if err := desired.AccountID.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("venue", desired.Venue, 128); err != nil {
		return err
	}
	switch desired.VenueEnvironment {
	case "demo", "testnet", "live":
	default:
		return fmt.Errorf("venue environment is invalid")
	}
	if err := validateOpaqueValue("secret reference", desired.SecretRef, 512); err != nil {
		return err
	}
	if desired.PreviousSecretRef != "" {
		if err := validateOpaqueValue("previous secret reference", desired.PreviousSecretRef, 512); err != nil {
			return err
		}
	}
	if desired.DesiredStateRevision <= 0 || desired.RuntimeGeneration <= 0 || desired.CredentialVersion <= 0 {
		return fmt.Errorf("desired-state fences must be positive")
	}
	if err := desired.RequestedState.Validate(); err != nil {
		return err
	}
	if desired.RotationFence != nil {
		if err := desired.RotationFence.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func ValidateDesiredAccountTransition(from, to DesiredAccountState) error {
	if err := from.Validate(); err != nil {
		return err
	}
	if err := to.Validate(); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	allowed := map[DesiredAccountState]map[DesiredAccountState]bool{
		DesiredCreating: {DesiredActive: true, DesiredDisabled: true},
		DesiredActive:   {DesiredRotating: true, DesiredDraining: true, DesiredDisabled: true},
		DesiredRotating: {DesiredActive: true, DesiredDisabled: true},
		DesiredDraining: {DesiredDrained: true},
		DesiredDrained:  {DesiredDisabled: true, DesiredRemoving: true},
		DesiredDisabled: {DesiredRemoving: true},
		DesiredRemoving: {DesiredRemoved: true},
	}
	if allowed[from][to] {
		return nil
	}
	return ErrInvalidAccountTransition
}

type RuntimeFence struct {
	AccountID            domain.AccountID
	DesiredStateRevision int64
	RuntimeGeneration    int64
	CredentialVersion    int64
}

func (fence RuntimeFence) Validate() error {
	if err := fence.AccountID.Validate(); err != nil {
		return err
	}
	if fence.DesiredStateRevision <= 0 || fence.RuntimeGeneration <= 0 || fence.CredentialVersion <= 0 {
		return fmt.Errorf("runtime fence values must be positive")
	}
	return nil
}

type DesiredStateStore interface {
	CreateDesiredAccount(context.Context, DesiredAccount) (DesiredAccount, error)
	ApplyDesiredAccount(context.Context, int64, DesiredAccount) (DesiredAccount, error)
	LoadDesiredAccount(context.Context, domain.AccountID) (DesiredAccount, error)
	ListDesiredAccounts(context.Context) ([]DesiredAccount, error)
	ListRecoverableDesiredAccounts(context.Context) ([]DesiredAccount, error)
	DeleteDesiredAccount(context.Context, domain.AccountID, int64) error
	ValidateRuntimeFence(context.Context, RuntimeFence) error
}

func validateOpaqueValue(name, value string, max int) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > max || strings.ContainsAny(value, "\n\r\t") {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}
