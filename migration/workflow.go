package migration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Operation string

const (
	RecordImport      Operation = "record-import"
	Backup            Operation = "backup"
	Restore           Operation = "restore"
	FinalDelta        Operation = "final-delta"
	Fence             Operation = "fence"
	Backfill          Operation = "backfill"
	Validate          Operation = "validate"
	PrepareActivation Operation = "prepare-activation"
	RecordActivation  Operation = "record-activation-evidence"
	PrepareRetirement Operation = "prepare-retirement"
	Rollback          Operation = "rollback"
)

type Phase string

const (
	PhaseBaseline        Phase = "baseline"
	PhaseProvenanceReady Phase = "provenance-ready"
	PhaseBackedUp        Phase = "backed-up"
	PhaseRestored        Phase = "restored"
	PhaseDeltaCaptured   Phase = "delta-captured"
	PhaseFenced          Phase = "fenced"
	PhaseBackfilled      Phase = "backfilled"
	PhaseValidated       Phase = "validated"
	PhaseActivationReady Phase = "activation-ready"
	PhaseActivated       Phase = "activated"
	PhaseRetirementReady Phase = "retirement-ready"
)

type Workflow struct {
	Epoch            int     `json:"epoch,omitempty"`
	Phase            Phase   `json:"phase,omitempty"`
	IrreversibleAt   *string `json:"irreversibleAt,omitempty"`
	IrreversibleStep *string `json:"irreversibleStep,omitempty"`
	Events           []Event `json:"events,omitempty"`
}

type Event struct {
	Sequence     int       `json:"sequence"`
	Epoch        int       `json:"epoch"`
	Operation    Operation `json:"operation"`
	At           string    `json:"at"`
	Fingerprint  string    `json:"fingerprint"`
	ArtifactSHA  string    `json:"artifactSha256,omitempty"`
	InputSHA     string    `json:"inputSha256,omitempty"`
	ProofSHA     string    `json:"proofSha256,omitempty"`
	Name         string    `json:"name,omitempty"`
	Verifier     string    `json:"verifier,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	ActivationAt string    `json:"activationAt,omitempty"`
	CutoverAt    string    `json:"cutoverAt,omitempty"`
}

type Request struct {
	Operation    Operation
	ID           string
	Name         string
	ArtifactPath string
	ProofPath    string
	Verifier     string
	Reason       string
	ActivationAt string
	CutoverAt    string
}

type ApplyOptions struct {
	Now func() time.Time
}

type Result struct {
	Phase    Phase
	Replayed bool
}

type Lock struct {
	file *os.File
}

func AcquireLock(manifestPath string) (*Lock, error) {
	file, err := os.OpenFile(manifestPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open migration lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("migration lock is held")
		}
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	return &Lock{file: file}, nil
}

func (lock *Lock) Release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

func Apply(manifestPath string, request Request, options ApplyOptions) (Result, error) {
	lock, err := AcquireLock(manifestPath)
	if err != nil {
		return Result{}, err
	}
	defer lock.Release()

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return Result{}, fmt.Errorf("read migration manifest: %w", err)
	}
	manifest, err := DecodeManifest(data)
	if err != nil {
		return Result{}, err
	}
	if err := validateBaseline(manifest); err != nil {
		return Result{}, err
	}
	if manifest.Workflow.Phase == "" {
		manifest.Workflow.Phase = inferPhase(manifest)
	}
	if err := validateWorkflow(manifest); err != nil {
		return Result{}, err
	}
	priorOperation := false
	for _, event := range manifest.Workflow.Events {
		priorOperation = priorOperation || event.Operation == request.Operation
	}
	var artifactSHA, proofSHA, fingerprint string
	if priorOperation {
		artifactSHA, proofSHA, err = requestDigests(manifest, request)
		if err != nil {
			return Result{}, err
		}
		fingerprint = requestFingerprint(request, artifactSHA, proofSHA)
		for _, event := range manifest.Workflow.Events {
			if event.Operation == request.Operation && event.Fingerprint == fingerprint && operationPostconditionHolds(manifest, request) {
				return Result{Phase: manifest.Workflow.Phase, Replayed: true}, nil
			}
		}
		if operationPostconditionHolds(manifest, request) {
			return Result{}, fmt.Errorf("%s replay conflict", request.Operation)
		}
	}
	if err := validatePredecessor(manifest, request.Operation); err != nil {
		return Result{}, err
	}
	if !priorOperation {
		artifactSHA, proofSHA, err = requestDigests(manifest, request)
		if err != nil {
			return Result{}, err
		}
		fingerprint = requestFingerprint(request, artifactSHA, proofSHA)
	}

	now := time.Now().UTC()
	if options.Now != nil {
		now = options.Now().UTC()
	}
	at := now.Format(time.RFC3339Nano)
	if err := transition(&manifest, request, artifactSHA, at); err != nil {
		return Result{}, err
	}
	if problems := validateActivationOrder(manifest); len(problems) > 0 {
		return Result{}, fmt.Errorf("migration evidence order rejected: %s", strings.Join(problems, "; "))
	}
	manifest.Workflow.Events = append(manifest.Workflow.Events, Event{
		Sequence: len(manifest.Workflow.Events) + 1, Epoch: manifest.Workflow.Epoch, Operation: request.Operation, At: at,
		Fingerprint: fingerprint, ArtifactSHA: artifactSHA, ProofSHA: proofSHA, Name: safeName(request),
		Verifier: request.Verifier, Reason: request.Reason,
		ActivationAt: request.ActivationAt, CutoverAt: request.CutoverAt,
	})
	if err := validateWorkflow(manifest); err != nil {
		return Result{}, err
	}
	if err := writeManifestAtomic(manifestPath, manifest); err != nil {
		return Result{}, err
	}
	return Result{Phase: manifest.Workflow.Phase}, nil
}

func requestDigests(manifest Manifest, request Request) (string, string, error) {
	if request.Operation == RecordImport {
		if err := validateGitProvenance(manifest, request.ID); err != nil {
			return "", "", err
		}
	}
	artifactSHA, err := digestFile(request.ArtifactPath, request.Operation == Backup || request.Operation == FinalDelta || request.Operation == Restore)
	if err != nil {
		return "", "", err
	}
	proofRequired := request.Operation == RecordImport || request.Operation == Backup || request.Operation == Restore || request.Operation == FinalDelta || request.Operation == Fence || request.Operation == Backfill || request.Operation == Validate || request.Operation == RecordActivation || request.Operation == Rollback
	var proofSHA string
	if proofRequired {
		proofData, digest, err := readProof(request.ProofPath)
		if err != nil {
			return "", "", err
		}
		proofSHA = digest
		if err := validateTypedProof(proofData, manifest, request, artifactSHA); err != nil {
			return "", "", err
		}
	}
	return artifactSHA, proofSHA, nil
}

type operationProof struct {
	SchemaVersion int               `json:"schemaVersion"`
	Operation     Operation         `json:"operation"`
	Status        string            `json:"status"`
	MigrationID   string            `json:"migrationId"`
	Subject       string            `json:"subject"`
	Checks        []string          `json:"checks"`
	GeneratedBy   string            `json:"generatedBy,omitempty"`
	PreStateSHA   string            `json:"preStateSha256,omitempty"`
	InputSHA      string            `json:"inputSha256,omitempty"`
	Commands      []commandEvidence `json:"commands,omitempty"`
}

func validateTypedProof(data []byte, manifest Manifest, request Request, artifactSHA string) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return fmt.Errorf("decode operation proof: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var proof operationProof
	if err := decoder.Decode(&proof); err != nil {
		return fmt.Errorf("decode operation proof: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode operation proof: trailing JSON value")
	}
	if proof.SchemaVersion != 1 || proof.Operation != request.Operation || proof.Status != "pass" || proof.MigrationID != manifest.MigrationID {
		return fmt.Errorf("operation proof identity or status does not match request")
	}
	var subject string
	var required []string
	switch request.Operation {
	case RecordImport:
		subject = request.ID
		required = []string{"source-commit-matches", "source-tree-matches", "subtree-import-matches"}
	case Backup:
		subject = artifactSHA
		required = []string{"consistent-snapshot", "restorable-format"}
	case Restore:
		subject = artifactSHA
		required = []string{"clean-restore", "source-stack-boot"}
	case FinalDelta:
		subject = artifactSHA
		required = []string{"deterministic", "source-read-only"}
	case Fence:
		subject = proofStateDigest(manifest)
		required = []string{"source-writes-disabled", "target-writes-disabled"}
	case Backfill:
		subject = proofStateDigest(manifest) + "/" + request.Name + "/" + artifactSHA
		required = []string{"authoritative-ownership-mapped", "deterministic"}
	case Validate:
		subject = proofStateDigest(manifest)
		required = []string{"browser", "offline", "real-test", "restart-recovery"}
	case RecordActivation:
		subject = proofStateDigest(manifest) + "/" + request.ActivationAt + "/" + request.CutoverAt
		required = []string{"one-writer-target", "target-activated"}
	case Rollback:
		subject = proofStateDigest(manifest)
		required = []string{"source-writes-enabled", "target-writes-disabled"}
	}
	if proof.Subject != subject || !sameStringSet(proof.Checks, required) {
		return fmt.Errorf("operation proof subject or checks do not match required postconditions")
	}
	return nil
}

func proofStateDigest(manifest Manifest) string {
	value := fmt.Sprintf("epoch=%d\x00phase=%s\x00delta=%s\x00fence=%s\x00backfills=%s\x00validation=%s",
		manifest.Workflow.Epoch, manifest.Workflow.Phase, pointerValue(manifest.FinalDeltaSHA256), pointerValue(manifest.OneWriterFenceAt), strings.Join(manifest.BackfillScripts, "\x00"), pointerValue(manifest.ValidationAt))
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func readProof(path string) ([]byte, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("read operation proof: unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 1<<20 {
		return nil, "", fmt.Errorf("read operation proof: unavailable or oversized")
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, "", fmt.Errorf("read operation proof: unavailable or oversized")
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func sameStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]struct{}, len(got))
	for _, value := range got {
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	for _, value := range want {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}

func operationPostconditionHolds(manifest Manifest, request Request) bool {
	switch request.Operation {
	case RecordImport:
		return manifest.ImportCommit != nil
	case Backup:
		return manifest.BackupID != nil
	case Restore:
		return manifest.RestoreVerifiedAt != nil
	case FinalDelta:
		return manifest.FinalDeltaAt != nil
	case Fence:
		return manifest.OneWriterFenceAt != nil
	case Backfill:
		for _, name := range manifest.BackfillScripts {
			if name == request.Name {
				return true
			}
		}
		return false
	case Validate:
		return manifest.ValidationAt != nil
	case PrepareActivation:
		return manifest.Workflow.Phase == PhaseActivationReady || manifest.Workflow.Phase == PhaseActivated || manifest.Workflow.Phase == PhaseRetirementReady
	case RecordActivation:
		return manifest.Workflow.Phase == PhaseActivated || manifest.Workflow.Phase == PhaseRetirementReady
	case PrepareRetirement:
		return manifest.Workflow.Phase == PhaseRetirementReady
	case Rollback:
		return manifest.Workflow.Phase == PhaseRestored && len(manifest.Workflow.Events) > 0 && manifest.Workflow.Events[len(manifest.Workflow.Events)-1].Operation == Rollback
	default:
		return false
	}
}

func validatePredecessor(manifest Manifest, operation Operation) error {
	phase := manifest.Workflow.Phase
	switch operation {
	case Backup:
		if phase != PhaseProvenanceReady {
			return fmt.Errorf("backup requires recorded provenance import; current phase is %s", phase)
		}
	case Restore:
		if phase != PhaseBackedUp {
			return fmt.Errorf("restore requires backup; current phase is %s", phase)
		}
	case FinalDelta:
		if phase != PhaseRestored {
			return fmt.Errorf("final delta requires verified restore; current phase is %s", phase)
		}
	case Fence:
		if phase != PhaseDeltaCaptured {
			return fmt.Errorf("fence requires final delta; current phase is %s", phase)
		}
	case Backfill:
		if phase != PhaseFenced && phase != PhaseBackfilled {
			return fmt.Errorf("backfill requires durable fence; current phase is %s", phase)
		}
	case Validate:
		if phase != PhaseBackfilled {
			return fmt.Errorf("validation requires backfill; current phase is %s", phase)
		}
	case PrepareActivation:
		if phase != PhaseValidated {
			return fmt.Errorf("activation preparation requires validation; current phase is %s", phase)
		}
	case RecordActivation:
		if phase != PhaseActivationReady {
			return fmt.Errorf("activation evidence requires activation-ready preparation; current phase is %s", phase)
		}
	case PrepareRetirement:
		if phase != PhaseActivated {
			return fmt.Errorf("retirement preparation requires externally recorded activation; current phase is %s", phase)
		}
	case Rollback:
		if manifest.Workflow.IrreversibleAt != nil || phaseAtLeastBackfill(phase) {
			return fmt.Errorf("rollback rejected: irreversible boundary crossed; forward recovery required")
		}
		if phase != PhaseDeltaCaptured && phase != PhaseFenced {
			return fmt.Errorf("rollback requires delta or fence preparation; current phase is %s", phase)
		}
	}
	return nil
}

func transition(manifest *Manifest, request Request, artifactSHA, at string) error {
	switch request.Operation {
	case RecordImport:
		if manifest.Workflow.Phase != PhaseBaseline {
			return fmt.Errorf("provenance import replay conflict")
		}
		if !validCommit(request.ID) {
			return fmt.Errorf("import commit must be a lowercase 40-character git object id")
		}
		manifest.ImportCommit = stringPtr(request.ID)
		manifest.Workflow.Epoch = 1
		manifest.Workflow.Phase = PhaseProvenanceReady
	case Backup:
		if manifest.Workflow.Phase != PhaseProvenanceReady {
			if manifest.BackupID != nil {
				return fmt.Errorf("backup replay conflict")
			}
			return requires("baseline", manifest.Workflow.Phase)
		}
		if err := safeText("backup id", request.ID); err != nil {
			return err
		}
		manifest.BackupID, manifest.BackupSHA256 = stringPtr(request.ID), stringPtr(artifactSHA)
		manifest.Workflow.Phase = PhaseBackedUp
	case Restore:
		if manifest.Workflow.Phase != PhaseBackedUp {
			return fmt.Errorf("restore requires backup; current phase is %s", manifest.Workflow.Phase)
		}
		if manifest.BackupSHA256 == nil || artifactSHA != *manifest.BackupSHA256 {
			return fmt.Errorf("restore artifact does not match recorded backup checksum")
		}
		if err := safeText("verifier", request.Verifier); err != nil {
			return err
		}
		manifest.RestoreVerifiedAt = stringPtr(at)
		manifest.Workflow.Phase = PhaseRestored
	case FinalDelta:
		if manifest.Workflow.Phase != PhaseRestored {
			return fmt.Errorf("final delta requires verified restore; current phase is %s", manifest.Workflow.Phase)
		}
		manifest.FinalDeltaAt, manifest.FinalDeltaSHA256 = stringPtr(at), stringPtr(artifactSHA)
		manifest.Workflow.Phase = PhaseDeltaCaptured
	case Fence:
		if manifest.Workflow.Phase != PhaseDeltaCaptured {
			return fmt.Errorf("fence requires final delta; current phase is %s", manifest.Workflow.Phase)
		}
		manifest.OneWriterFenceAt = stringPtr(at)
		manifest.OneWriter.Owner = "BolterTrader"
		manifest.OneWriter.FenceRaised = true
		manifest.OneWriter.TargetCanonicalWritesEnabled = false
		manifest.Workflow.Phase = PhaseFenced
	case Backfill:
		if manifest.Workflow.Phase != PhaseFenced && manifest.Workflow.Phase != PhaseBackfilled {
			return fmt.Errorf("backfill requires durable fence; current phase is %s", manifest.Workflow.Phase)
		}
		if err := safeBackfillName(request.Name); err != nil {
			return err
		}
		for _, name := range manifest.BackfillScripts {
			if name == request.Name {
				return fmt.Errorf("backfill replay conflict")
			}
		}
		manifest.BackfillScripts = append(manifest.BackfillScripts, request.Name)
		if manifest.Workflow.IrreversibleAt == nil {
			manifest.Workflow.IrreversibleAt, manifest.Workflow.IrreversibleStep = stringPtr(at), stringPtr(request.Name)
		}
		manifest.Workflow.Phase = PhaseBackfilled
	case Validate:
		if manifest.Workflow.Phase != PhaseBackfilled {
			return fmt.Errorf("validation requires backfill; current phase is %s", manifest.Workflow.Phase)
		}
		manifest.ValidationAt = stringPtr(at)
		manifest.Workflow.Phase = PhaseValidated
	case PrepareActivation:
		if manifest.Workflow.Phase != PhaseValidated {
			return fmt.Errorf("activation preparation requires validation; current phase is %s", manifest.Workflow.Phase)
		}
		manifest.Workflow.Phase = PhaseActivationReady
	case RecordActivation:
		activationAt, err := time.Parse(time.RFC3339, request.ActivationAt)
		if err != nil {
			return fmt.Errorf("activation timestamp must be RFC3339")
		}
		cutoverAt, err := time.Parse(time.RFC3339, request.CutoverAt)
		if err != nil || !cutoverAt.After(activationAt) {
			return fmt.Errorf("cutover timestamp must be RFC3339 and strictly after activation")
		}
		recordedAt, _ := time.Parse(time.RFC3339Nano, at)
		if cutoverAt.After(recordedAt) {
			return fmt.Errorf("cutover timestamp cannot be later than its evidence receipt")
		}
		manifest.TargetActivationAt = stringPtr(request.ActivationAt)
		manifest.CutoverAt = stringPtr(request.CutoverAt)
		manifest.OneWriter.TargetCanonicalWritesEnabled = true
		manifest.Workflow.Phase = PhaseActivated
		if problems := validateRetirementOrder(*manifest); len(problems) > 0 {
			return fmt.Errorf("activation evidence order rejected: %s", strings.Join(problems, "; "))
		}
	case PrepareRetirement:
		if err := validateRetirementManifest(*manifest); err != nil {
			return err
		}
		manifest.Workflow.Phase = PhaseRetirementReady
	case Rollback:
		if manifest.Workflow.IrreversibleAt != nil || phaseAtLeastBackfill(manifest.Workflow.Phase) {
			return fmt.Errorf("rollback rejected: irreversible boundary crossed; forward recovery required")
		}
		if manifest.Workflow.Phase != PhaseDeltaCaptured && manifest.Workflow.Phase != PhaseFenced {
			return fmt.Errorf("rollback requires delta or fence preparation; current phase is %s", manifest.Workflow.Phase)
		}
		if err := safeText("rollback reason", request.Reason); err != nil {
			return err
		}
		manifest.FinalDeltaAt, manifest.FinalDeltaSHA256, manifest.OneWriterFenceAt = nil, nil, nil
		manifest.BackfillScripts = []string{}
		manifest.ValidationAt = nil
		manifest.OneWriter.Owner = "funding-trader"
		manifest.OneWriter.FenceRaised = false
		manifest.OneWriter.TargetCanonicalWritesEnabled = false
		manifest.Workflow.Epoch++
		manifest.Workflow.Phase = PhaseRestored
	default:
		return fmt.Errorf("unknown migration operation %q", request.Operation)
	}
	return nil
}

func inferPhase(manifest Manifest) Phase {
	switch {
	case manifest.ValidationAt != nil:
		return PhaseValidated
	case len(manifest.BackfillScripts) > 0:
		return PhaseBackfilled
	case manifest.OneWriterFenceAt != nil:
		return PhaseFenced
	case manifest.FinalDeltaAt != nil:
		return PhaseDeltaCaptured
	case manifest.RestoreVerifiedAt != nil:
		return PhaseRestored
	case manifest.BackupID != nil:
		return PhaseBackedUp
	case manifest.ImportCommit != nil:
		return PhaseProvenanceReady
	default:
		return PhaseBaseline
	}
}

func validateWorkflowState(manifest Manifest) error {
	phase := manifest.Workflow.Phase
	problem := func(detail string) error {
		return fmt.Errorf("workflow state is inconsistent for phase %s: %s", phase, detail)
	}
	hasBackup := manifest.BackupID != nil || manifest.BackupSHA256 != nil
	hasRestore := manifest.RestoreVerifiedAt != nil
	hasDelta := manifest.FinalDeltaAt != nil || manifest.FinalDeltaSHA256 != nil
	hasFence := manifest.OneWriterFenceAt != nil
	hasBackfill := len(manifest.BackfillScripts) != 0 || manifest.Workflow.IrreversibleAt != nil || manifest.Workflow.IrreversibleStep != nil
	hasValidation := manifest.ValidationAt != nil
	hasActivation := manifest.TargetActivationAt != nil || manifest.CutoverAt != nil
	if manifest.SourceRetirementAt != nil {
		return problem("source retirement is outside preparation scope")
	}
	if phase == PhaseBaseline {
		if manifest.Workflow.Epoch != 0 {
			return problem("baseline epoch must be zero")
		}
		if manifest.ImportCommit != nil || hasBackup || hasRestore || hasDelta || hasFence || hasBackfill || hasValidation || hasActivation {
			return problem("unexpected migration evidence")
		}
		return nil
	}
	if manifest.Workflow.Epoch <= 0 {
		return problem("migration epoch must be positive")
	}
	if !validCommit(pointerValue(manifest.ImportCommit)) {
		return problem("missing valid import commit")
	}
	if phase == PhaseProvenanceReady {
		if hasBackup || hasRestore || hasDelta || hasFence || hasBackfill || hasValidation || hasActivation {
			return problem("unexpected evidence after provenance import")
		}
		return nil
	}
	if blank(manifest.BackupID) || !validSHA256(pointerValue(manifest.BackupSHA256)) {
		return problem("missing backup evidence")
	}
	if phase == PhaseBackedUp {
		if hasRestore || hasDelta || hasFence || hasBackfill || hasValidation || hasActivation {
			return problem("unexpected evidence after backup")
		}
		return nil
	}
	if blank(manifest.RestoreVerifiedAt) {
		return problem("missing restore evidence")
	}
	if phase == PhaseRestored {
		if hasDelta || hasFence || hasBackfill || hasValidation || hasActivation {
			return problem("unexpected evidence after restore")
		}
		return nil
	}
	if blank(manifest.FinalDeltaAt) || !validSHA256(pointerValue(manifest.FinalDeltaSHA256)) {
		return problem("missing final delta evidence")
	}
	if phase == PhaseDeltaCaptured {
		if hasFence || hasBackfill || hasValidation || hasActivation {
			return problem("unexpected evidence after final delta")
		}
		return nil
	}
	activationObserved := phase == PhaseActivated || phase == PhaseRetirementReady
	if blank(manifest.OneWriterFenceAt) || manifest.OneWriter.Owner != "BolterTrader" || !manifest.OneWriter.FenceRaised || (manifest.OneWriter.TargetCanonicalWritesEnabled && !activationObserved) {
		return problem("missing safe one-writer fence")
	}
	if phase == PhaseFenced {
		if hasBackfill || hasValidation || hasActivation {
			return problem("unexpected evidence after fence")
		}
		return nil
	}
	if len(manifest.BackfillScripts) == 0 || manifest.Workflow.IrreversibleAt == nil || manifest.Workflow.IrreversibleStep == nil {
		return problem("missing irreversible backfill evidence")
	}
	if phase == PhaseBackfilled {
		if hasValidation || hasActivation {
			return problem("unexpected evidence after backfill")
		}
		return nil
	}
	if blank(manifest.ValidationAt) {
		return problem("missing validation evidence")
	}
	if phase == PhaseValidated || phase == PhaseActivationReady {
		if manifest.TargetActivationAt != nil || manifest.CutoverAt != nil || manifest.OneWriter.TargetCanonicalWritesEnabled {
			return problem("preparation must not claim or enable activation")
		}
		return nil
	}
	if phase == PhaseActivated || phase == PhaseRetirementReady {
		if blank(manifest.TargetActivationAt) || blank(manifest.CutoverAt) || !manifest.OneWriter.TargetCanonicalWritesEnabled {
			return problem("missing externally recorded activation evidence")
		}
		if manifest.SourceRetirementAt != nil {
			return problem("preparation must not claim source retirement")
		}
		return nil
	}
	return problem("unknown phase")
}

func ValidateWorkflow(data []byte) error {
	manifest, err := DecodeManifest(data)
	if err != nil {
		return err
	}
	if err := validateBaseline(manifest); err != nil {
		return err
	}
	if manifest.Workflow.Phase == "" {
		manifest.Workflow.Phase = inferPhase(manifest)
	}
	return validateWorkflow(manifest)
}

func validateWorkflow(manifest Manifest) error {
	if err := validateWorkflowState(manifest); err != nil {
		return err
	}
	events := manifest.Workflow.Events
	if manifest.Workflow.Phase == PhaseBaseline {
		if len(events) != 0 {
			return fmt.Errorf("workflow ledger is inconsistent: baseline has events")
		}
		return nil
	}
	if len(events) == 0 {
		return fmt.Errorf("workflow ledger is inconsistent: non-baseline phase has no events")
	}
	var previous time.Time
	ledgerPhase := PhaseBaseline
	ledgerEpoch := 0
	var firstBackfill *Event
	var importEvent, backupEvent, restoreEvent, deltaEvent, fenceEvent, validationEvent, activationEvent *Event
	var backfillNames []string
	for index := range events {
		event := &events[index]
		var err error
		ledgerPhase, ledgerEpoch, err = advanceLedger(ledgerPhase, ledgerEpoch, *event)
		if err != nil || event.Epoch != ledgerEpoch {
			return fmt.Errorf("workflow ledger transition rejected at sequence %d: %v", event.Sequence, err)
		}
		if event.Sequence != index+1 || !knownOperation(event.Operation) || !validSHA256(event.Fingerprint) {
			return fmt.Errorf("workflow ledger is inconsistent at sequence %d", index+1)
		}
		if operationNeedsArtifact(event.Operation) != validSHA256(event.ArtifactSHA) || operationNeedsProof(event.Operation) != validSHA256(event.ProofSHA) {
			return fmt.Errorf("workflow ledger evidence digest mismatch at sequence %d", event.Sequence)
		}
		parsed, err := time.Parse(time.RFC3339, event.At)
		if err != nil || (!previous.IsZero() && !parsed.After(previous)) {
			return fmt.Errorf("workflow ledger timestamps must be valid and strictly increasing")
		}
		previous = parsed
		if event.Operation == RecordActivation {
			activationAt, activationErr := time.Parse(time.RFC3339, event.ActivationAt)
			cutoverAt, cutoverErr := time.Parse(time.RFC3339, event.CutoverAt)
			if activationErr != nil || cutoverErr != nil || !cutoverAt.After(activationAt) || cutoverAt.After(parsed) {
				return fmt.Errorf("workflow ledger activation timestamps are invalid")
			}
		}
		request := Request{Operation: event.Operation, Verifier: event.Verifier, Reason: event.Reason, ActivationAt: event.ActivationAt, CutoverAt: event.CutoverAt}
		switch event.Operation {
		case RecordImport, Backup:
			request.ID = event.Name
		case Backfill:
			request.Name = event.Name
		}
		if requestFingerprint(request, event.ArtifactSHA, event.ProofSHA) != event.Fingerprint {
			return fmt.Errorf("workflow ledger fingerprint mismatch at sequence %d", event.Sequence)
		}
		if event.Operation == Backfill && firstBackfill == nil {
			firstBackfill = event
		}
		switch event.Operation {
		case RecordImport:
			importEvent = event
		case Backup:
			backupEvent = event
		case Restore:
			restoreEvent = event
		case FinalDelta:
			deltaEvent = event
		case Fence:
			fenceEvent = event
		case Backfill:
			backfillNames = append(backfillNames, event.Name)
		case Validate:
			validationEvent = event
		case RecordActivation:
			activationEvent = event
		case Rollback:
			deltaEvent, fenceEvent, validationEvent = nil, nil, nil
		}
	}
	if ledgerPhase != manifest.Workflow.Phase || ledgerEpoch != manifest.Workflow.Epoch {
		return fmt.Errorf("workflow ledger terminal state does not match manifest")
	}
	if importEvent == nil || manifest.ImportCommit == nil || importEvent.Name != *manifest.ImportCommit {
		return fmt.Errorf("workflow ledger provenance evidence mismatch")
	}
	if (backupEvent == nil) != (manifest.BackupID == nil) || (backupEvent != nil && (backupEvent.Name != *manifest.BackupID || manifest.BackupSHA256 == nil || backupEvent.ArtifactSHA != *manifest.BackupSHA256)) {
		return fmt.Errorf("workflow ledger backup evidence mismatch")
	}
	if (restoreEvent == nil) != (manifest.RestoreVerifiedAt == nil) || (restoreEvent != nil && restoreEvent.At != *manifest.RestoreVerifiedAt) {
		return fmt.Errorf("workflow ledger restore evidence mismatch")
	}
	if (deltaEvent == nil) != (manifest.FinalDeltaAt == nil) || (deltaEvent != nil && (deltaEvent.At != *manifest.FinalDeltaAt || manifest.FinalDeltaSHA256 == nil || deltaEvent.ArtifactSHA != *manifest.FinalDeltaSHA256)) {
		return fmt.Errorf("workflow ledger final delta evidence mismatch")
	}
	if (fenceEvent == nil) != (manifest.OneWriterFenceAt == nil) || (fenceEvent != nil && fenceEvent.At != *manifest.OneWriterFenceAt) {
		return fmt.Errorf("workflow ledger fence evidence mismatch")
	}
	if (validationEvent == nil) != (manifest.ValidationAt == nil) || (validationEvent != nil && validationEvent.At != *manifest.ValidationAt) {
		return fmt.Errorf("workflow ledger validation evidence mismatch")
	}
	if strings.Join(backfillNames, "\x00") != strings.Join(manifest.BackfillScripts, "\x00") {
		return fmt.Errorf("workflow ledger backfill evidence mismatch")
	}
	if activationEvent != nil && (manifest.TargetActivationAt == nil || manifest.CutoverAt == nil || activationEvent.ActivationAt != *manifest.TargetActivationAt || activationEvent.CutoverAt != *manifest.CutoverAt) {
		return fmt.Errorf("workflow ledger activation evidence mismatch")
	}
	if firstBackfill != nil {
		if manifest.Workflow.IrreversibleAt == nil || manifest.Workflow.IrreversibleStep == nil || *manifest.Workflow.IrreversibleAt != firstBackfill.At || *manifest.Workflow.IrreversibleStep != firstBackfill.Name {
			return fmt.Errorf("workflow ledger irreversible boundary does not match first backfill")
		}
	}
	wantLast := map[Phase]Operation{
		PhaseProvenanceReady: RecordImport, PhaseBackedUp: Backup, PhaseRestored: Restore,
		PhaseDeltaCaptured: FinalDelta, PhaseFenced: Fence, PhaseBackfilled: Backfill,
		PhaseValidated: Validate, PhaseActivationReady: PrepareActivation,
		PhaseActivated: RecordActivation, PhaseRetirementReady: PrepareRetirement,
	}[manifest.Workflow.Phase]
	last := events[len(events)-1].Operation
	if manifest.Workflow.Phase == PhaseRestored && last == Rollback {
		return nil
	}
	if wantLast == "" || last != wantLast {
		return fmt.Errorf("workflow ledger last operation %s does not match phase %s", last, manifest.Workflow.Phase)
	}
	return nil
}

func knownOperation(operation Operation) bool {
	switch operation {
	case RecordImport, Backup, Restore, FinalDelta, Fence, Backfill, Validate, PrepareActivation, RecordActivation, PrepareRetirement, Rollback:
		return true
	default:
		return false
	}
}

func advanceLedger(phase Phase, epoch int, event Event) (Phase, int, error) {
	switch event.Operation {
	case RecordImport:
		if phase != PhaseBaseline {
			break
		}
		return PhaseProvenanceReady, 1, nil
	case Backup:
		if phase == PhaseProvenanceReady {
			return PhaseBackedUp, epoch, nil
		}
	case Restore:
		if phase == PhaseBackedUp {
			return PhaseRestored, epoch, nil
		}
	case FinalDelta:
		if phase == PhaseRestored {
			return PhaseDeltaCaptured, epoch, nil
		}
	case Fence:
		if phase == PhaseDeltaCaptured {
			return PhaseFenced, epoch, nil
		}
	case Backfill:
		if phase == PhaseFenced || phase == PhaseBackfilled {
			return PhaseBackfilled, epoch, nil
		}
	case Validate:
		if phase == PhaseBackfilled {
			return PhaseValidated, epoch, nil
		}
	case PrepareActivation:
		if phase == PhaseValidated {
			return PhaseActivationReady, epoch, nil
		}
	case RecordActivation:
		if phase == PhaseActivationReady {
			return PhaseActivated, epoch, nil
		}
	case PrepareRetirement:
		if phase == PhaseActivated {
			return PhaseRetirementReady, epoch, nil
		}
	case Rollback:
		if phase == PhaseDeltaCaptured || phase == PhaseFenced {
			return PhaseRestored, epoch + 1, nil
		}
	}
	return phase, epoch, fmt.Errorf("%s is not allowed after %s", event.Operation, phase)
}

func operationNeedsArtifact(operation Operation) bool {
	return operation == Backup || operation == Restore || operation == FinalDelta || operation == Backfill
}

func operationNeedsProof(operation Operation) bool {
	return operation == RecordImport || operation == Backup || operation == Restore || operation == FinalDelta || operation == Fence || operation == Backfill || operation == Validate || operation == RecordActivation || operation == Rollback
}

func digestFile(path string, required bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		if required {
			return "", fmt.Errorf("required artifact path is missing")
		}
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open evidence artifact: unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("evidence artifact must be a non-empty regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash evidence artifact: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validCommit(value string) bool {
	if len(value) != 40 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 20
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func writeManifestAtomic(path string, manifest Manifest) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".migration-manifest-*")
	if err != nil {
		return fmt.Errorf("create manifest temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode migration manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync migration manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close migration manifest: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace migration manifest: %w", err)
	}
	dir, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open manifest directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync manifest directory: %w", err)
	}
	return nil
}

func requestFingerprint(request Request, artifactSHA, proofSHA string) string {
	value := strings.Join([]string{string(request.Operation), request.ID, request.Name, artifactSHA, proofSHA, request.Verifier, request.Reason}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func safeText(label, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed != value || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n\t") {
		return fmt.Errorf("%s is missing or unsafe", label)
	}
	return nil
}

func safeBackfillName(value string) error {
	if err := safeText("backfill name", value); err != nil {
		return err
	}
	if filepath.Base(value) != value || value == "." || value == ".." || strings.Contains(value, "\\") {
		return fmt.Errorf("backfill name is unsafe")
	}
	return nil
}

func safeName(request Request) string {
	if request.Name != "" {
		return request.Name
	}
	return request.ID
}

func phaseAtLeastBackfill(phase Phase) bool {
	return phase == PhaseBackfilled || phase == PhaseValidated || phase == PhaseActivationReady || phase == PhaseActivated || phase == PhaseRetirementReady
}

func requires(required string, phase Phase) error {
	return fmt.Errorf("operation requires %s; current phase is %s", required, phase)
}

func stringPtr(value string) *string { return &value }
