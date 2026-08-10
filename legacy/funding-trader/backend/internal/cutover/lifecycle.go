package cutover

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidProof      = errors.New("invalid cutover proof")
	ErrInvalidTransition = errors.New("invalid cutover transition")
	ErrRestoreForbidden  = errors.New("backup restore is forbidden")
	ErrBackupMismatch    = errors.New("backup does not match verified cutover proof")
)

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Proof struct {
	BackupID            string
	BackupSHA256        string
	BackupVerifiedAt    time.Time
	OldServiceStopped   bool
	AutoRestartDisabled bool
}

func (proof Proof) Validate() error {
	switch {
	case strings.TrimSpace(proof.BackupID) == "":
		return fmt.Errorf("%w: backup id is required", ErrInvalidProof)
	case strings.TrimSpace(proof.BackupID) != proof.BackupID:
		return fmt.Errorf("%w: backup id must be normalized", ErrInvalidProof)
	case !sha256Pattern.MatchString(proof.BackupSHA256):
		return fmt.Errorf("%w: backup sha256 is invalid", ErrInvalidProof)
	case proof.BackupVerifiedAt.IsZero():
		return fmt.Errorf("%w: backup verification time is required", ErrInvalidProof)
	case !proof.OldServiceStopped:
		return fmt.Errorf("%w: old service must be stopped", ErrInvalidProof)
	case !proof.AutoRestartDisabled:
		return fmt.Errorf("%w: automatic restart must be disabled", ErrInvalidProof)
	default:
		return nil
	}
}

type Phase string

const (
	PhaseBackupVerified Phase = "backup_verified"
	PhaseValidated      Phase = "validated"
	PhaseReopened       Phase = "reopened"
	PhaseRestore        Phase = "restore_authorized"
	PhaseWriteAccepted  Phase = "write_accepted"
)

type State struct {
	Phase                    Phase
	SchemaVersion            int
	BackupID                 string
	BackupVerifiedAt         time.Time
	NormalTrafficReopened    bool
	NewContractWriteAccepted bool
}

type Lifecycle struct {
	mu    sync.Mutex
	proof Proof
	state State
}

func NewLifecycle(proof Proof) (*Lifecycle, error) {
	if err := proof.Validate(); err != nil {
		return nil, err
	}
	return &Lifecycle{
		proof: proof,
		state: State{
			Phase:            PhaseBackupVerified,
			BackupID:         proof.BackupID,
			BackupVerifiedAt: proof.BackupVerifiedAt.UTC(),
		},
	}, nil
}

func (lifecycle *Lifecycle) MarkMigrationValidated(schemaVersion int) error {
	if schemaVersion <= 0 {
		return fmt.Errorf("%w: schema version must be positive", ErrInvalidTransition)
	}
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.state.Phase != PhaseBackupVerified {
		return fmt.Errorf("%w: migration can only be validated once", ErrInvalidTransition)
	}
	lifecycle.state.Phase = PhaseValidated
	lifecycle.state.SchemaVersion = schemaVersion
	return nil
}

func (lifecycle *Lifecycle) MarkTrafficReopened() error {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.state.Phase != PhaseValidated {
		return fmt.Errorf("%w: traffic requires a validated migration", ErrInvalidTransition)
	}
	lifecycle.state.Phase = PhaseReopened
	lifecycle.state.NormalTrafficReopened = true
	return nil
}

func (lifecycle *Lifecycle) MarkWriteAccepted() error {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.state.Phase != PhaseValidated &&
		lifecycle.state.Phase != PhaseReopened {
		return fmt.Errorf("%w: writes require a validated migration", ErrInvalidTransition)
	}
	lifecycle.state.Phase = PhaseWriteAccepted
	lifecycle.state.NewContractWriteAccepted = true
	return nil
}

func (lifecycle *Lifecycle) AuthorizeRestore(backupID string) error {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if backupID != lifecycle.proof.BackupID {
		return ErrBackupMismatch
	}
	if lifecycle.state.Phase != PhaseValidated ||
		lifecycle.state.NormalTrafficReopened ||
		lifecycle.state.NewContractWriteAccepted {
		return ErrRestoreForbidden
	}
	lifecycle.state.Phase = PhaseRestore
	return nil
}

func (lifecycle *Lifecycle) Snapshot() State {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	return lifecycle.state
}
