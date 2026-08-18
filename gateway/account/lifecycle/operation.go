package lifecycle

import (
	"errors"
	"strings"
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
	ID                               secretstore.OperationID
	PrincipalID                      string
	AccountID                        string
	Kind                             OperationKind
	State                            OperationState
	IdempotencyKeyDigest             string
	RequestDigest                    string
	TargetDesiredStateRevision       int64
	TargetRuntimeGeneration          int64
	TargetCredentialVersion          int64
	RotationCleanupTombstoneRevision int64
	RotationCleanupCompletedAt       time.Time
	SecretRef                        string
	StableErrorCode                  string
	CreatedAt                        time.Time
	UpdatedAt                        time.Time
}

func (operation CredentialOperation) Validate() error {
	if _, err := secretstore.ParseOperationID(string(operation.ID)); err != nil ||
		operation.PrincipalID == "" || operation.PrincipalID != strings.TrimSpace(operation.PrincipalID) ||
		operation.AccountID == "" || operation.AccountID != strings.TrimSpace(operation.AccountID) ||
		operation.IdempotencyKeyDigest == "" || operation.RequestDigest == "" {
		return errors.New("credential operation identity is invalid")
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
	proofRecorded := operation.RotationCleanupTombstoneRevision != 0 || !operation.RotationCleanupCompletedAt.IsZero()
	if proofRecorded && (operation.Kind != OperationRotate || operation.RotationCleanupTombstoneRevision != operation.TargetDesiredStateRevision || operation.RotationCleanupCompletedAt.IsZero()) {
		return errors.New("credential operation rotation cleanup proof is invalid")
	}
	return nil
}

type AccountFence struct {
	AccountID            string
	DesiredStateRevision int64
	RuntimeGeneration    int64
	CredentialVersion    int64
}

func (fence AccountFence) Validate() error {
	if strings.TrimSpace(fence.AccountID) == "" || fence.AccountID != strings.TrimSpace(fence.AccountID) ||
		fence.DesiredStateRevision < 1 || fence.RuntimeGeneration < 1 || fence.CredentialVersion < 1 {
		return errors.New("account fence is invalid")
	}
	return nil
}

type RotationCleanupRequest struct {
	OperationID           secretstore.OperationID
	AccountID             string
	ExpectedActiveFence   AccountFence
	ExpectedRetiringFence AccountFence
}

func (request RotationCleanupRequest) Validate() error {
	if _, err := secretstore.ParseOperationID(string(request.OperationID)); err != nil {
		return errors.New("rotation cleanup operation is invalid")
	}
	if strings.TrimSpace(request.AccountID) == "" || request.AccountID != strings.TrimSpace(request.AccountID) ||
		request.ExpectedActiveFence.Validate() != nil || request.ExpectedRetiringFence.Validate() != nil ||
		request.ExpectedActiveFence.AccountID != request.AccountID || request.ExpectedRetiringFence.AccountID != request.AccountID ||
		request.ExpectedActiveFence.DesiredStateRevision <= 1 || request.ExpectedActiveFence.RuntimeGeneration <= 1 || request.ExpectedActiveFence.CredentialVersion <= 1 ||
		request.ExpectedActiveFence.DesiredStateRevision-1 != request.ExpectedRetiringFence.DesiredStateRevision ||
		request.ExpectedActiveFence.RuntimeGeneration-1 != request.ExpectedRetiringFence.RuntimeGeneration ||
		request.ExpectedActiveFence.CredentialVersion-1 != request.ExpectedRetiringFence.CredentialVersion {
		return errors.New("rotation cleanup fence is invalid")
	}
	return nil
}

type RotationCleanupProof struct {
	OperationID          secretstore.OperationID
	AccountID            string
	ActiveFence          AccountFence
	RetiringFence        AccountFence
	TombstoneRevision    int64
	RetiringState        string
	SecretReferenceState string
	CompletedAt          time.Time
}

func (proof RotationCleanupProof) ValidateFor(request RotationCleanupRequest) error {
	if request.Validate() != nil || proof.OperationID != request.OperationID || proof.AccountID != request.AccountID ||
		proof.ActiveFence != request.ExpectedActiveFence || proof.RetiringFence != request.ExpectedRetiringFence ||
		proof.TombstoneRevision != request.ExpectedActiveFence.DesiredStateRevision ||
		proof.RetiringState != "retired" || proof.SecretReferenceState != "tombstoned" || proof.CompletedAt.IsZero() {
		return errors.New("rotation cleanup proof is invalid")
	}
	return nil
}

type RotationCleanupCompletion struct {
	OperationID       secretstore.OperationID
	AccountID         string
	PreviousSecretRef string
	CompletedAt       time.Time
}

func (completion RotationCleanupCompletion) Validate() error {
	if _, err := secretstore.ParseOperationID(string(completion.OperationID)); err != nil {
		return errors.New("rotation cleanup completion operation is invalid")
	}
	if strings.TrimSpace(completion.AccountID) == "" || completion.AccountID != strings.TrimSpace(completion.AccountID) ||
		completion.PreviousSecretRef == "" || completion.PreviousSecretRef != strings.TrimSpace(completion.PreviousSecretRef) || completion.CompletedAt.IsZero() {
		return errors.New("rotation cleanup completion is invalid")
	}
	return nil
}

func rotationCleanupRequestFor(account Account, operation CredentialOperation) (RotationCleanupRequest, error) {
	request := RotationCleanupRequest{
		OperationID: operation.ID,
		AccountID:   account.ID,
		ExpectedActiveFence: AccountFence{
			AccountID: account.ID, DesiredStateRevision: account.DesiredStateRevision,
			RuntimeGeneration: account.RuntimeGeneration, CredentialVersion: account.CredentialVersion,
		},
		ExpectedRetiringFence: AccountFence{
			AccountID: account.ID, DesiredStateRevision: account.DesiredStateRevision - 1,
			RuntimeGeneration: account.RuntimeGeneration - 1, CredentialVersion: account.CredentialVersion - 1,
		},
	}
	if operation.Kind != OperationRotate || operation.AccountID != account.ID ||
		operation.TargetDesiredStateRevision != account.DesiredStateRevision ||
		operation.TargetRuntimeGeneration != account.RuntimeGeneration ||
		operation.TargetCredentialVersion != account.CredentialVersion || account.State != StateActive ||
		account.PreviousSecretRef == "" || account.SecretRef == "" || operation.SecretRef != account.SecretRef || request.Validate() != nil {
		return RotationCleanupRequest{}, errors.New("rotation cleanup state is invalid")
	}
	return request, nil
}

func ValidateRotationCleanupProof(account Account, operation CredentialOperation, proof RotationCleanupProof) error {
	if operation.State != OperationCleanupPending {
		return errors.New("rotation cleanup operation is not pending")
	}
	request, err := rotationCleanupRequestFor(account, operation)
	if err != nil || proof.ValidateFor(request) != nil {
		return errors.New("rotation cleanup proof is invalid")
	}
	if operation.RotationCleanupTombstoneRevision != 0 || !operation.RotationCleanupCompletedAt.IsZero() {
		if operation.RotationCleanupTombstoneRevision != proof.TombstoneRevision || !operation.RotationCleanupCompletedAt.Equal(proof.CompletedAt) {
			return errors.New("rotation cleanup proof conflicts with persisted proof")
		}
	}
	return nil
}

func HasRotationCleanupProof(account Account, operation CredentialOperation) bool {
	request, err := rotationCleanupRequestFor(account, operation)
	return err == nil && operation.State == OperationCleanupPending &&
		operation.RotationCleanupTombstoneRevision == request.ExpectedActiveFence.DesiredStateRevision &&
		!operation.RotationCleanupCompletedAt.IsZero()
}

func ValidateRotationCleanupCompletion(account Account, operation CredentialOperation, completion RotationCleanupCompletion) error {
	if completion.Validate() != nil || completion.OperationID != operation.ID || completion.AccountID != account.ID ||
		operation.Kind != OperationRotate || operation.AccountID != account.ID ||
		operation.TargetDesiredStateRevision != account.DesiredStateRevision ||
		operation.TargetRuntimeGeneration != account.RuntimeGeneration || operation.TargetCredentialVersion != account.CredentialVersion ||
		operation.RotationCleanupTombstoneRevision != account.DesiredStateRevision || operation.RotationCleanupCompletedAt.IsZero() {
		return errors.New("rotation cleanup completion state is invalid")
	}
	if operation.State == OperationActive && account.PreviousSecretRef == "" {
		return nil
	}
	if operation.State != OperationCleanupPending || account.State != StateActive || account.PreviousSecretRef != completion.PreviousSecretRef {
		return errors.New("rotation cleanup completion fence is invalid")
	}
	return nil
}

type OperationLookup struct {
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
