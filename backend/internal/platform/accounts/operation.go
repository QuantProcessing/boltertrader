package accounts

import (
	"errors"
	"time"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

type OperationKind string

const (
	OperationCreate OperationKind = "create"
	OperationRotate OperationKind = "rotate"
	OperationRetire OperationKind = "retire"
)

type OperationState string

const (
	OperationAwaitingMaterial OperationState = "awaiting_material"
	OperationStored           OperationState = "stored"
	OperationApplying         OperationState = "applying"
	OperationActive           OperationState = "active"
	OperationCleanupPending   OperationState = "cleanup_pending"
	OperationAbandoned        OperationState = "abandoned"
	OperationFailed           OperationState = "failed"
)

type CredentialOperation struct {
	ID                         secretstore.OperationID
	PrincipalID                string
	AccountID                  string
	Mode                       secretstore.Mode
	Kind                       OperationKind
	State                      OperationState
	IdempotencyKeyDigest       string
	RequestDigest              string
	TargetDesiredStateRevision int64
	TargetRuntimeGeneration    int64
	TargetCredentialVersion    int64
	SecretRef                  string
	StableErrorCode            string
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

func (operation CredentialOperation) Validate() error {
	if operation.ID == "" || operation.PrincipalID == "" || operation.AccountID == "" || operation.IdempotencyKeyDigest == "" || operation.RequestDigest == "" {
		return errors.New("credential operation identity is invalid")
	}
	if operation.Mode != secretstore.ModeTest && operation.Mode != secretstore.ModeLive {
		return errors.New("credential operation mode is invalid")
	}
	switch operation.Kind {
	case OperationCreate, OperationRotate, OperationRetire:
	default:
		return errors.New("credential operation kind is invalid")
	}
	switch operation.State {
	case OperationAwaitingMaterial, OperationStored, OperationApplying, OperationActive, OperationCleanupPending, OperationAbandoned, OperationFailed:
	default:
		return errors.New("credential operation state is invalid")
	}
	if operation.TargetDesiredStateRevision < 1 || operation.TargetRuntimeGeneration < 1 || operation.TargetCredentialVersion < 1 {
		return errors.New("credential operation fence is invalid")
	}
	return nil
}

type OperationLookup struct {
	Mode                 secretstore.Mode
	PrincipalID          string
	Kind                 OperationKind
	IdempotencyKeyDigest string
}

type TransitionRequest struct {
	OperationID                  secretstore.OperationID
	ExpectedOperationState       OperationState
	ExpectedDesiredStateRevision int64
	Account                      Account
	Operation                    CredentialOperation
}
