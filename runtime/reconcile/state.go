package reconcile

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
)

type ScopeKey struct {
	Venue        string
	AccountID    string
	InstrumentID model.InstrumentID
}

func (s ScopeKey) String() string {
	return s.Venue + "|" + s.AccountID + "|" + s.InstrumentID.String()
}

type ReportStream string

const (
	StreamOrders    ReportStream = "orders"
	StreamFills     ReportStream = "fills"
	StreamPositions ReportStream = "positions"
	StreamBalances  ReportStream = "balances"
)

type PassHeader struct {
	PassID        model.ReconciliationID
	Scope         ScopeKey
	StartedAt     time.Time
	StableEventAt time.Time
	QueryFrom     time.Time
	QueryTo       time.Time
}

type Cursor struct {
	Scope                 ScopeKey
	Stream                ReportStream
	FillInstrumentIDs     []model.InstrumentID
	LastSuccessfulPass    model.ReconciliationID
	LastReportID          model.ReportID
	LastVenueTime         time.Time
	LastLocalApplyTime    time.Time
	LookbackFloor         time.Time
	Partial               bool
	FillsPartial          bool
	UnresolvedIDs         []string
	AppliedEventRecordIDs []string
}

type FindingSeverity string

const (
	FindingInfo     FindingSeverity = "info"
	FindingWarning  FindingSeverity = "warning"
	FindingBlocking FindingSeverity = "blocking"
)

type Finding struct {
	ID        string
	PassID    model.ReconciliationID
	Scope     ScopeKey
	Stream    ReportStream
	Severity  FindingSeverity
	Code      string
	Message   string
	Blocking  bool
	CreatedAt time.Time
	revision  uint64
}

// StateStore keeps reconciliation cursors plus operator-audit findings.
// Resolution remains an optional additive capability so existing custom stores
// stay source-compatible and conservatively retain blocking findings.
type StateStore interface {
	LoadCursor(ctx context.Context, scope ScopeKey, stream ReportStream) (Cursor, error)
	BeginPass(ctx context.Context, pass PassHeader) error
	RecordFinding(ctx context.Context, finding Finding) error
	CommitCursor(ctx context.Context, cursor Cursor) error
	LoadOpenFindings(ctx context.Context, scope ScopeKey) ([]Finding, error)
}

// FindingResolution is an explicit, durable close event for one open finding.
// Automatic reconciliation only emits these for narrowly proven conditions;
// operators may use the same capability for findings that require review.
type FindingResolution struct {
	FindingID        string
	PassID           model.ReconciliationID
	ResolvedAt       time.Time
	Reason           string
	expectedRevision uint64
}

// FindingResolver is an optional StateStore capability. It is deliberately not
// part of StateStore so third-party stores continue to compile and fail closed.
type FindingResolver interface {
	ResolveFinding(ctx context.Context, resolution FindingResolution) error
}

type findingBatchResolver interface {
	resolveFindings(ctx context.Context, resolutions []FindingResolution) error
}

// revisionFindingResolver marks the built-in state store as supporting the
// compare-and-swap revision carried by automatic finding resolutions. The
// unexported marker deliberately keeps third-party resolvers fail-closed.
type revisionFindingResolver interface {
	FindingResolver
	supportsFindingRevisionCAS()
}

// AppliedFillRecorder is the optional durability surface used when a state
// store can persist the exact fill event that a cursor advance depends on.
// Existing StateStore implementations remain source-compatible.
type AppliedFillRecorder interface {
	RecordAppliedFill(
		ctx context.Context,
		pass PassHeader,
		meta contract.EventMeta,
		fill model.Fill,
		appliedAt time.Time,
	) (string, error)
}

// AppliedFillDependency is a durably recorded recovered fill referenced by a
// reconciliation cursor. Replay uses it to rebuild idempotency indexes without
// re-emitting callbacks or mutating the portfolio a second time.
type AppliedFillDependency struct {
	RecordID string
	Fill     model.Fill
}

// AppliedFillLoader is the optional replay surface paired with
// AppliedFillRecorder. Existing StateStore implementations remain compatible;
// a cursor with dependencies requires this surface before it can advance a
// reconciler after restart.
type AppliedFillLoader interface {
	LoadAppliedFills(ctx context.Context, recordIDs []string) ([]AppliedFillDependency, error)
}

// AppliedFillReplayLoader exposes retained applied-fill records that may not
// yet be referenced by a cursor (for example, after a cursor commit failure).
// Replay seeds idempotency only; it never emits callbacks again.
type AppliedFillReplayLoader interface {
	LoadAppliedFillReplay(ctx context.Context, scope ScopeKey) ([]AppliedFillDependency, error)
}

type appliedFillReplayCapability interface {
	canReplayUncommittedAppliedFills() bool
}

func canReplayUncommittedAppliedFills(store StateStore) bool {
	if _, recordsAppliedFills := store.(AppliedFillRecorder); recordsAppliedFills {
		if _, replaysAppliedFills := store.(AppliedFillReplayLoader); !replaysAppliedFills {
			return false
		}
	}
	capability, declared := store.(appliedFillReplayCapability)
	return !declared || capability.canReplayUncommittedAppliedFills()
}

type JournalStateStore struct {
	mu                     sync.Mutex
	store                  journal.Store
	findings               map[string]Finding
	findingRecords         map[string][]string
	findingVersions        map[string]uint64
	findingMutationEpoch   uint64
	findingMutations       []findingMutation
	findingMutationNext    int
	findingOperations      map[string]int
	findingIdentitySeq     uint64
	pendingFindingRecords  map[string]pendingFindingRecord
	pendingFindingLimit    int
	resolvingEpochs        map[string][]string
	resolvedFindingRecords map[string]map[string]struct{}
	findingEntropy         io.Reader
	findingNonceOnce       sync.Once
	findingNonce           string
	findingNonceErr        error
}

const (
	defaultPendingFindingLimit = 1024
	maxFindingMutationHistory  = 1024
)

type findingMutation struct {
	epoch     uint64
	findingID string
}

type pendingFindingRecord struct {
	findingID string
	recordID  string
	report    journal.ReportRecord
	ambiguous bool
}

var (
	errPendingFindingCapacity          = errors.New("reconcile: pending finding identity capacity exceeded")
	errPendingFindingReplayUnavailable = errors.New("reconcile: pending finding state cannot be replayed")
)

func NewJournalStateStore(store journal.Store) *JournalStateStore {
	s := &JournalStateStore{
		store:                  store,
		findings:               make(map[string]Finding),
		findingRecords:         make(map[string][]string),
		findingVersions:        make(map[string]uint64),
		findingOperations:      make(map[string]int),
		pendingFindingRecords:  make(map[string]pendingFindingRecord),
		pendingFindingLimit:    defaultPendingFindingLimit,
		resolvingEpochs:        make(map[string][]string),
		resolvedFindingRecords: make(map[string]map[string]struct{}),
		findingEntropy:         rand.Reader,
	}
	_ = s.replayFindings(context.Background())
	return s
}

func (s *JournalStateStore) canReplayUncommittedAppliedFills() bool {
	_, ok := s.store.(interface{ Records() []journal.Record })
	return ok
}

func (s *JournalStateStore) LoadCursor(ctx context.Context, scope ScopeKey, stream ReportStream) (Cursor, error) {
	cursors, err := s.store.LoadReconciliationCursors(ctx)
	if err != nil {
		return Cursor{}, err
	}
	targetScope := scope.String()
	targetStream := string(stream)
	var out Cursor
	for _, c := range cursors {
		if c.Scope != targetScope || c.Stream != targetStream {
			continue
		}
		var payload Cursor
		if err := json.Unmarshal([]byte(c.Cursor), &payload); err != nil {
			return Cursor{}, fmt.Errorf(
				"reconcile: load cursor record %q for scope %q stream %q: decode payload: %w",
				c.RecordID,
				targetScope,
				stream,
				err,
			)
		}
		if payload.Scope != scope || payload.Stream != stream {
			return Cursor{}, fmt.Errorf(
				"reconcile: load cursor record %q for scope %q stream %q: payload identity scope %q stream %q does not match journal identity",
				c.RecordID,
				targetScope,
				stream,
				payload.Scope.String(),
				payload.Stream,
			)
		}
		out = payload
	}
	return out, nil
}

func (s *JournalStateStore) BeginPass(ctx context.Context, pass PassHeader) error {
	payload, err := json.Marshal(pass)
	if err != nil {
		return err
	}
	return s.store.AppendReconciliationPass(ctx, journal.ReconciliationPassRecord{
		RecordID:  string(DeterministicID("pass-record", string(pass.PassID), string(payload))),
		PassID:    pass.PassID,
		StartedAt: pass.StartedAt,
		Reason:    pass.Scope.String(),
		Payload:   payload,
	})
}

func (s *JournalStateStore) RecordFinding(ctx context.Context, finding Finding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if finding.ID == "" {
		return fmt.Errorf("reconcile: finding id required")
	}
	payload, err := json.Marshal(finding)
	if err != nil {
		return err
	}
	if !findingIsBlocking(finding) {
		return s.store.AppendReport(ctx, journal.ReportRecord{
			RecordID:   string(DeterministicID("finding-report", finding.ID, string(payload))),
			ReportID:   model.ReportID(finding.PassID),
			ReportedAt: finding.CreatedAt,
			Payload:    payload,
		})
	}
	payloadHash := sha256.Sum256(payload)
	payloadFingerprint := hex.EncodeToString(payloadHash[:])
	requestedAttemptKey := string(DeterministicID(
		"finding-attempt",
		finding.ID,
		payloadFingerprint,
	))
	s.mu.Lock()
	attemptKey, _, retry := s.pendingFindingAttemptLocked(requestedAttemptKey, finding.ID)
	if !retry && s.coalesceBlockingFindingLocked(finding) {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	instanceNonce := ""
	if !retry {
		instanceNonce, err = s.findingInstanceNonce()
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	attemptKey, pending, retry := s.pendingFindingAttemptLocked(requestedAttemptKey, finding.ID)
	if !retry && s.coalesceBlockingFindingLocked(finding) {
		s.mu.Unlock()
		return nil
	}
	if !retry && instanceNonce == "" {
		s.mu.Unlock()
		instanceNonce, err = s.findingInstanceNonce()
		if err != nil {
			return err
		}
		s.mu.Lock()
		attemptKey, pending, retry = s.pendingFindingAttemptLocked(requestedAttemptKey, finding.ID)
		if !retry && s.coalesceBlockingFindingLocked(finding) {
			s.mu.Unlock()
			return nil
		}
	}
	resolutionEpoch := strings.Join(s.resolvingEpochs[finding.ID], ",")
	recordID := pending.recordID
	if !retry {
		limit := s.pendingFindingLimit
		if limit <= 0 {
			limit = defaultPendingFindingLimit
		}
		if len(s.pendingFindingRecords) >= limit {
			s.mu.Unlock()
			return fmt.Errorf("%w: limit %d", errPendingFindingCapacity, limit)
		}
		s.findingIdentitySeq++
		recordID = string(DeterministicID(
			"finding-open-v3",
			instanceNonce,
			strconv.FormatUint(s.findingIdentitySeq, 10),
			finding.ID,
			payloadFingerprint,
			resolutionEpoch,
		))
		attemptKey = requestedAttemptKey
		pending = pendingFindingRecord{
			findingID: finding.ID,
			recordID:  recordID,
			report: journal.ReportRecord{
				RecordID:   recordID,
				ReportID:   model.ReportID(finding.PassID),
				ReportedAt: finding.CreatedAt,
				Payload:    append([]byte(nil), payload...),
			},
		}
		s.pendingFindingRecords[attemptKey] = pending
	}
	s.beginFindingOperationLocked(finding.ID)
	s.bumpFindingVersionLocked(finding.ID)
	startVersion := s.findingVersions[finding.ID]
	s.mu.Unlock()
	defer s.endFindingOperation(finding.ID)
	if err := s.store.AppendReport(ctx, pending.report); err != nil {
		// A Store may durably append before losing the acknowledgement. Advance
		// the per-finding barrier again so a concurrent targeted replay cannot
		// publish a snapshot captured before that ambiguous boundary.
		s.mu.Lock()
		s.bumpFindingVersionLocked(finding.ID)
		if pending := s.pendingFindingRecords[attemptKey]; pending.recordID == recordID {
			pending.ambiguous = true
			s.pendingFindingRecords[attemptKey] = pending
		}
		s.mu.Unlock()
		return err
	}

	// AppendReport is an injected extension point and may synchronously call
	// back into this state store. A per-finding version keeps unrelated finding
	// activity from hiding this successful append. Replayable stores rebuild the
	// one contested logical finding; opaque stores retain every successful local
	// record identity so a later resolution can close them all.
	_, replayable := s.store.(interface{ Records() []journal.Record })
	s.mu.Lock()
	if s.findingRecordWasResolvedLocked(finding.ID, recordID) {
		if s.pendingFindingRecords[attemptKey].recordID == recordID {
			delete(s.pendingFindingRecords, attemptKey)
		}
		s.mu.Unlock()
		return nil
	}
	if s.pendingFindingRecords[attemptKey].recordID == recordID {
		delete(s.pendingFindingRecords, attemptKey)
	}
	if !replayable || s.findingVersions[finding.ID] == startVersion {
		s.addFindingRecordLocked(finding, recordID)
		s.mu.Unlock()
		return nil
	}
	if containsFindingRecord(s.findingRecords[finding.ID], recordID) {
		s.findings[finding.ID] = mergeFindingMetadata(s.findings[finding.ID], finding)
		s.bumpFindingVersionLocked(finding.ID)
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := s.replayFindingUntilStable(ctx, finding.ID); err != nil {
		return err
	}
	s.mu.Lock()
	if !s.findingRecordWasResolvedLocked(finding.ID, recordID) && containsFindingRecord(s.findingRecords[finding.ID], recordID) {
		s.findings[finding.ID] = mergeFindingMetadata(s.findings[finding.ID], finding)
		s.bumpFindingVersionLocked(finding.ID)
	}
	s.mu.Unlock()
	return nil
}

func (s *JournalStateStore) coalesceBlockingFindingLocked(finding Finding) bool {
	existing, open := s.findings[finding.ID]
	if !open || len(s.findingRecords[finding.ID]) == 0 || len(s.resolvingEpochs[finding.ID]) != 0 || !findingIsBlocking(existing) || !findingIsBlocking(finding) {
		return false
	}
	// Preserve one recovery-critical journal record per unresolved logical
	// condition while keeping current metadata visible in memory.
	s.findings[finding.ID] = mergeFindingMetadata(existing, finding)
	s.clearPendingFindingRecordsLocked(s.findingRecords[finding.ID])
	s.bumpFindingVersionLocked(finding.ID)
	return true
}

const findingResolutionMarker = "boltertrader.reconcile.finding_resolution.v1"

type findingResolutionRecord struct {
	Marker          string                 `json:"marker"`
	FindingID       string                 `json:"finding_id"`
	FindingRecordID string                 `json:"finding_record_id"`
	PassID          model.ReconciliationID `json:"pass_id,omitempty"`
	ResolvedAt      time.Time              `json:"resolved_at"`
	Reason          string                 `json:"reason,omitempty"`
}

// ResolveFinding durably closes any currently open finding. Automatic callers
// are gated in reconcile.go; this store-level method also supports explicit
// operator resolution for conditions that cannot be proven automatically.
func (s *JournalStateStore) ResolveFinding(ctx context.Context, resolution FindingResolution) error {
	return s.resolveFindings(ctx, []FindingResolution{resolution})
}

func (*JournalStateStore) supportsFindingRevisionCAS() {}

func (s *JournalStateStore) resolveFindings(ctx context.Context, resolutions []FindingResolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unique := make(map[string]struct{}, len(resolutions))
	normalized := make([]FindingResolution, 0, len(resolutions))
	for _, resolution := range resolutions {
		if resolution.FindingID == "" {
			return fmt.Errorf("reconcile: finding resolution id required")
		}
		if _, duplicate := unique[resolution.FindingID]; duplicate {
			continue
		}
		unique[resolution.FindingID] = struct{}{}
		if resolution.ResolvedAt.IsZero() {
			resolution.ResolvedAt = time.Now()
		}
		normalized = append(normalized, resolution)
	}
	if len(normalized) == 0 {
		return nil
	}
	if err := s.replayFindings(ctx); err != nil {
		return err
	}
	refreshIDs := make([]string, 0, len(normalized))
	var resolutionErr error
	for _, resolution := range normalized {
		refreshIDs = append(refreshIDs, resolution.FindingID)
		if err := s.resolveFindingCurrent(ctx, resolution); err != nil {
			resolutionErr = errors.Join(resolutionErr, err)
			break
		}
	}
	refreshErr := s.replayFindingsByID(ctx, refreshIDs)
	return errors.Join(resolutionErr, refreshErr, s.pendingFindingReplayError())
}

func (s *JournalStateStore) resolveFindingCurrent(ctx context.Context, resolution FindingResolution) error {
	s.mu.Lock()
	if resolution.expectedRevision != 0 && s.findingVersions[resolution.FindingID] != resolution.expectedRevision {
		s.mu.Unlock()
		return nil
	}
	findingRecordIDs := append([]string(nil), s.findingRecords[resolution.FindingID]...)
	if len(findingRecordIDs) == 0 {
		s.mu.Unlock()
		return nil
	}
	resolutionEpoch := string(DeterministicID(
		"finding-resolution-active",
		resolution.FindingID,
		strings.Join(findingRecordIDs, ","),
		string(resolution.PassID),
		resolution.ResolvedAt.UTC().Format(time.RFC3339Nano),
	))
	s.resolvingEpochs[resolution.FindingID] = append(s.resolvingEpochs[resolution.FindingID], resolutionEpoch)
	s.bumpFindingVersionLocked(resolution.FindingID)
	s.beginFindingOperationLocked(resolution.FindingID)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		epochs := s.resolvingEpochs[resolution.FindingID]
		for i, epoch := range epochs {
			if epoch == resolutionEpoch {
				epochs = append(epochs[:i], epochs[i+1:]...)
				break
			}
		}
		if len(epochs) == 0 {
			delete(s.resolvingEpochs, resolution.FindingID)
		} else {
			s.resolvingEpochs[resolution.FindingID] = epochs
		}
		s.bumpFindingVersionLocked(resolution.FindingID)
		s.endFindingOperationLocked(resolution.FindingID)
		s.mu.Unlock()
	}()
	for _, findingRecordID := range findingRecordIDs {
		record := findingResolutionRecord{
			Marker:          findingResolutionMarker,
			FindingID:       resolution.FindingID,
			FindingRecordID: findingRecordID,
			PassID:          resolution.PassID,
			ResolvedAt:      resolution.ResolvedAt,
			Reason:          resolution.Reason,
		}
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		recordID := string(DeterministicID(
			"finding-resolution",
			findingRecordID,
			string(resolution.PassID),
			resolution.ResolvedAt.UTC().Format(time.RFC3339Nano),
		))
		if err := s.store.AppendReport(ctx, journal.ReportRecord{
			RecordID:   recordID,
			ReportID:   model.ReportID(resolution.PassID),
			ReportedAt: resolution.ResolvedAt,
			Payload:    payload,
		}); err != nil {
			return err
		}
		s.mu.Lock()
		s.markFindingRecordResolvedLocked(resolution.FindingID, findingRecordID)
		s.removeFindingRecordLocked(resolution.FindingID, findingRecordID)
		s.mu.Unlock()
	}
	return nil
}

type appliedFillRecord struct {
	PassID model.ReconciliationID `json:"pass_id"`
	Meta   contract.EventMeta     `json:"meta"`
	Fill   model.Fill             `json:"fill"`
}

func (s *JournalStateStore) RecordAppliedFill(
	ctx context.Context,
	pass PassHeader,
	meta contract.EventMeta,
	fill model.Fill,
	appliedAt time.Time,
) (string, error) {
	payload, err := json.Marshal(appliedFillRecord{PassID: pass.PassID, Meta: meta, Fill: fill})
	if err != nil {
		return "", err
	}
	recordID := string(DeterministicID("applied-fill", string(meta.EventID)))
	if err := s.store.AppendAppliedEvent(ctx, journal.AppliedEventRecord{
		RecordID:  recordID,
		EventID:   meta.EventID,
		AppliedAt: appliedAt,
		Payload:   payload,
	}); err != nil {
		return recordID, err
	}
	return recordID, nil
}

func (s *JournalStateStore) LoadAppliedFills(ctx context.Context, recordIDs []string) ([]AppliedFillDependency, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(recordIDs) == 0 {
		return nil, nil
	}
	source, ok := s.store.(interface{ Records() []journal.Record })
	if !ok {
		return nil, fmt.Errorf("reconcile: state store cannot replay applied-fill dependencies")
	}
	wanted := make(map[string]struct{}, len(recordIDs))
	for _, recordID := range recordIDs {
		wanted[recordID] = struct{}{}
	}
	loaded := make(map[string]AppliedFillDependency, len(recordIDs))
	for _, record := range source.Records() {
		if _, ok := wanted[record.RecordID]; !ok || record.Type != journal.RecordAppliedEvent {
			continue
		}
		var event journal.AppliedEventRecord
		if err := json.Unmarshal(record.Payload, &event); err != nil {
			return nil, fmt.Errorf("reconcile: decode applied event %q: %w", record.RecordID, err)
		}
		var applied appliedFillRecord
		if err := json.Unmarshal(event.Payload, &applied); err != nil {
			return nil, fmt.Errorf("reconcile: decode applied fill %q: %w", record.RecordID, err)
		}
		loaded[record.RecordID] = AppliedFillDependency{RecordID: record.RecordID, Fill: applied.Fill}
	}
	out := make([]AppliedFillDependency, 0, len(recordIDs))
	for _, recordID := range recordIDs {
		dependency, ok := loaded[recordID]
		if !ok {
			return nil, fmt.Errorf("reconcile: %w %q", journal.ErrMissingDependency, recordID)
		}
		out = append(out, dependency)
	}
	return out, nil
}

func (s *JournalStateStore) LoadAppliedFillReplay(ctx context.Context, scope ScopeKey) ([]AppliedFillDependency, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, ok := s.store.(interface{ Records() []journal.Record })
	if !ok {
		return nil, fmt.Errorf("reconcile: state store cannot enumerate applied fills for crash recovery")
	}
	var out []AppliedFillDependency
	for _, record := range source.Records() {
		if record.Type != journal.RecordAppliedEvent {
			continue
		}
		var event journal.AppliedEventRecord
		if err := json.Unmarshal(record.Payload, &event); err != nil {
			return nil, fmt.Errorf("reconcile: decode applied event %q: %w", record.RecordID, err)
		}
		var applied appliedFillRecord
		if err := json.Unmarshal(event.Payload, &applied); err != nil || applied.Fill.TradeID == "" {
			continue
		}
		if !appliedFillInScope(applied.Fill, applied.Meta, scope) {
			continue
		}
		out = append(out, AppliedFillDependency{RecordID: record.RecordID, Fill: applied.Fill})
	}
	return out, nil
}

func appliedFillInScope(fill model.Fill, meta contract.EventMeta, scope ScopeKey) bool {
	venue := fill.InstrumentID.Venue
	if venue == "" {
		venue = meta.Venue
	}
	if scope.Venue != "" && !strings.EqualFold(scope.Venue, venue) {
		return false
	}
	accountID := fill.AccountID
	if accountID == "" {
		accountID = meta.AccountID
	}
	if scope.AccountID != "" && scope.AccountID != accountID {
		return false
	}
	if scope.InstrumentID.Symbol != "" && scope.InstrumentID != fill.InstrumentID {
		return false
	}
	return true
}

func (s *JournalStateStore) CommitCursor(ctx context.Context, cursor Cursor) error {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return err
	}
	return s.store.CommitReconciliationCursor(ctx, journal.ReconciliationCursor{
		RecordID:              string(DeterministicID("cursor", cursor.Scope.String(), string(cursor.Stream), string(payload))),
		PassID:                cursor.LastSuccessfulPass,
		Scope:                 cursor.Scope.String(),
		Stream:                string(cursor.Stream),
		Cursor:                string(payload),
		UpdatedAt:             cursor.LastLocalApplyTime,
		AppliedEventRecordIDs: append([]string(nil), cursor.AppliedEventRecordIDs...),
	})
}

func (s *JournalStateStore) LoadOpenFindings(ctx context.Context, scope ScopeKey) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.replayFindings(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	out := make([]Finding, 0, len(s.findings))
	for _, f := range s.findings {
		if f.Scope == scope && findingIsBlocking(f) {
			f.revision = s.findingVersions[f.ID]
			out = append(out, f)
		}
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func findingIsBlocking(finding Finding) bool {
	return finding.Blocking || finding.Severity == FindingBlocking
}

func mergeFindingMetadata(current, candidate Finding) Finding {
	if current.ID == "" {
		return candidate
	}
	if candidate.CreatedAt.After(current.CreatedAt) {
		return candidate
	}
	if current.CreatedAt.After(candidate.CreatedAt) {
		return current
	}
	currentPayload, _ := json.Marshal(current)
	candidatePayload, _ := json.Marshal(candidate)
	if string(candidatePayload) > string(currentPayload) {
		return candidate
	}
	return current
}

func (s *JournalStateStore) findingInstanceNonce() (string, error) {
	s.findingNonceOnce.Do(func() {
		entropy := s.findingEntropy
		if entropy == nil {
			entropy = rand.Reader
		}
		var nonce [16]byte
		if _, err := io.ReadFull(entropy, nonce[:]); err != nil {
			s.findingNonceErr = fmt.Errorf("reconcile: generate finding record identity: %w", err)
			return
		}
		s.findingNonce = hex.EncodeToString(nonce[:])
	})
	return s.findingNonce, s.findingNonceErr
}

func (s *JournalStateStore) bumpFindingVersionLocked(findingID string) {
	s.recordFindingMutationLocked(findingID)
	if s.findingVersions == nil {
		s.findingVersions = make(map[string]uint64)
	}
	s.findingVersions[findingID] = s.findingMutationEpoch
}

func (s *JournalStateStore) recordFindingMutationLocked(findingID string) {
	s.findingMutationEpoch++
	if s.findingMutationEpoch == 0 {
		s.findingMutationEpoch = 1
		s.findingMutations = s.findingMutations[:0]
		s.findingMutationNext = 0
	}
	mutation := findingMutation{epoch: s.findingMutationEpoch, findingID: findingID}
	if len(s.findingMutations) < maxFindingMutationHistory {
		s.findingMutations = append(s.findingMutations, mutation)
		return
	}
	s.findingMutations[s.findingMutationNext] = mutation
	s.findingMutationNext = (s.findingMutationNext + 1) % maxFindingMutationHistory
}

func (s *JournalStateStore) findingMutationsSinceLocked(startEpoch uint64) (map[string]struct{}, bool) {
	if startEpoch == s.findingMutationEpoch {
		return nil, true
	}
	if startEpoch > s.findingMutationEpoch || len(s.findingMutations) == 0 {
		return nil, false
	}
	oldestIndex := 0
	if len(s.findingMutations) == maxFindingMutationHistory {
		oldestIndex = s.findingMutationNext
	}
	oldestEpoch := s.findingMutations[oldestIndex].epoch
	if oldestEpoch > 0 && startEpoch < oldestEpoch-1 {
		return nil, false
	}
	changed := make(map[string]struct{})
	for offset := 0; offset < len(s.findingMutations); offset++ {
		mutation := s.findingMutations[(oldestIndex+offset)%len(s.findingMutations)]
		if mutation.epoch > startEpoch {
			changed[mutation.findingID] = struct{}{}
		}
	}
	return changed, true
}

func (s *JournalStateStore) beginFindingOperationLocked(findingID string) {
	if s.findingOperations == nil {
		s.findingOperations = make(map[string]int)
	}
	s.findingOperations[findingID]++
}

func (s *JournalStateStore) endFindingOperation(findingID string) {
	s.mu.Lock()
	s.endFindingOperationLocked(findingID)
	s.mu.Unlock()
}

func (s *JournalStateStore) endFindingOperationLocked(findingID string) {
	if operations := s.findingOperations[findingID]; operations > 1 {
		s.findingOperations[findingID] = operations - 1
	} else {
		delete(s.findingOperations, findingID)
		delete(s.resolvedFindingRecords, findingID)
	}
	s.pruneFindingVersionLocked(findingID)
}

func (s *JournalStateStore) markFindingRecordResolvedLocked(findingID, recordID string) {
	if s.resolvedFindingRecords == nil {
		s.resolvedFindingRecords = make(map[string]map[string]struct{})
	}
	resolved := s.resolvedFindingRecords[findingID]
	if resolved == nil {
		resolved = make(map[string]struct{})
		s.resolvedFindingRecords[findingID] = resolved
	}
	resolved[recordID] = struct{}{}
}

func (s *JournalStateStore) findingRecordWasResolvedLocked(findingID, recordID string) bool {
	_, resolved := s.resolvedFindingRecords[findingID][recordID]
	return resolved
}

func (s *JournalStateStore) pruneFindingVersionLocked(findingID string) {
	if len(s.findingRecords[findingID]) != 0 || len(s.resolvingEpochs[findingID]) != 0 || s.findingOperations[findingID] != 0 {
		return
	}
	if _, exists := s.findingVersions[findingID]; exists {
		delete(s.findingVersions, findingID)
		s.recordFindingMutationLocked(findingID)
	}
}

func (s *JournalStateStore) pruneInactiveFindingVersionsLocked() {
	for findingID := range s.findingVersions {
		if len(s.findingRecords[findingID]) == 0 && len(s.resolvingEpochs[findingID]) == 0 && s.findingOperations[findingID] == 0 {
			delete(s.findingVersions, findingID)
			s.recordFindingMutationLocked(findingID)
		}
	}
}

func (s *JournalStateStore) pendingFindingAttemptLocked(requestedKey, findingID string) (string, pendingFindingRecord, bool) {
	if pending, ok := s.pendingFindingRecords[requestedKey]; ok {
		return requestedKey, pending, true
	}
	selectedKey := ""
	var selected pendingFindingRecord
	for attemptKey, pending := range s.pendingFindingRecords {
		if pending.findingID != findingID || !pending.ambiguous || (selectedKey != "" && attemptKey >= selectedKey) {
			continue
		}
		selectedKey = attemptKey
		selected = pending
	}
	return selectedKey, selected, selectedKey != ""
}

func (s *JournalStateStore) clearPendingFindingRecordsLocked(recordIDs []string) bool {
	if len(recordIDs) == 0 || len(s.pendingFindingRecords) == 0 {
		return false
	}
	cleared := false
	for attemptKey, pending := range s.pendingFindingRecords {
		if containsFindingRecord(recordIDs, pending.recordID) {
			delete(s.pendingFindingRecords, attemptKey)
			cleared = true
		}
	}
	return cleared
}

func (s *JournalStateStore) replayFindings(ctx context.Context) error {
	source, ok := s.store.(interface{ Records() []journal.Record })
	if !ok {
		return s.pendingFindingReplayError()
	}
	for attempt := 0; attempt < maxFindingReplayAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		startMutationEpoch := s.findingMutationEpoch
		s.mu.Unlock()

		findings, findingRecords := replayFindingRecords(source.Records())
		s.mu.Lock()
		changedFindingIDs, complete := s.findingMutationsSinceLocked(startMutationEpoch)
		if !complete {
			s.mu.Unlock()
			continue
		}
		findingIDs := make(map[string]struct{}, len(s.findingRecords)+len(findingRecords)+len(changedFindingIDs))
		for findingID := range s.findingRecords {
			findingIDs[findingID] = struct{}{}
		}
		for findingID := range findingRecords {
			findingIDs[findingID] = struct{}{}
		}
		for findingID := range changedFindingIDs {
			findingIDs[findingID] = struct{}{}
		}
		pendingFindingIDs := make(map[string]struct{}, len(s.pendingFindingRecords))
		for _, pending := range s.pendingFindingRecords {
			pendingFindingIDs[pending.findingID] = struct{}{}
		}
		var replayContested []string
		for findingID := range findingIDs {
			if _, changed := changedFindingIDs[findingID]; changed {
				_, pending := pendingFindingIDs[findingID]
				if len(s.findingRecords[findingID]) == 0 && (len(findingRecords[findingID]) != 0 || pending) {
					replayContested = append(replayContested, findingID)
				}
				continue
			}
			finding, open := findings[findingID]
			s.applyFindingSnapshotLocked(findingID, finding, findingRecords[findingID], open)
		}
		s.pruneInactiveFindingVersionsLocked()
		s.mu.Unlock()
		if err := s.replayFindingsByID(ctx, replayContested); err != nil {
			return err
		}
		return s.pendingFindingReplayError()
	}
	return fmt.Errorf("reconcile: findings changed during %d journal replay attempts", maxFindingReplayAttempts)
}

func (s *JournalStateStore) pendingFindingReplayError() error {
	s.mu.Lock()
	pending := 0
	for _, record := range s.pendingFindingRecords {
		if record.ambiguous {
			pending++
		}
	}
	s.mu.Unlock()
	if pending == 0 {
		return nil
	}
	return fmt.Errorf("%w: %d ambiguous append(s)", errPendingFindingReplayUnavailable, pending)
}

const maxFindingReplayAttempts = 16

func (s *JournalStateStore) replayFindingUntilStable(ctx context.Context, findingID string) error {
	return s.replayFindingsByID(ctx, []string{findingID})
}

func (s *JournalStateStore) replayFindingsByID(ctx context.Context, findingIDs []string) error {
	source, ok := s.store.(interface{ Records() []journal.Record })
	if !ok {
		return nil
	}
	unique := make(map[string]struct{}, len(findingIDs))
	ids := make([]string, 0, len(findingIDs))
	for _, findingID := range findingIDs {
		if findingID == "" {
			continue
		}
		if _, exists := unique[findingID]; exists {
			continue
		}
		unique[findingID] = struct{}{}
		ids = append(ids, findingID)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	s.mu.Lock()
	for _, findingID := range ids {
		s.beginFindingOperationLocked(findingID)
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		for _, findingID := range ids {
			s.endFindingOperationLocked(findingID)
		}
		s.mu.Unlock()
	}()
	remaining := append([]string(nil), ids...)
	for attempt := 0; attempt < maxFindingReplayAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		startVersions := make(map[string]uint64, len(remaining))
		for _, findingID := range remaining {
			startVersions[findingID] = s.findingVersions[findingID]
		}
		s.mu.Unlock()

		findings, findingRecords := replayFindingRecords(source.Records())
		s.mu.Lock()
		next := make([]string, 0, len(remaining))
		for _, findingID := range remaining {
			if s.findingVersions[findingID] != startVersions[findingID] {
				next = append(next, findingID)
				continue
			}
			finding, open := findings[findingID]
			s.applyFindingSnapshotLocked(findingID, finding, findingRecords[findingID], open)
		}
		s.mu.Unlock()
		if len(next) == 0 {
			return nil
		}
		remaining = next
	}
	return fmt.Errorf("reconcile: %d finding(s) changed during %d journal replay attempts", len(remaining), maxFindingReplayAttempts)
}

func (s *JournalStateStore) addFindingRecordLocked(finding Finding, recordID string) {
	if s.findings == nil {
		s.findings = make(map[string]Finding)
	}
	if s.findingRecords == nil {
		s.findingRecords = make(map[string][]string)
	}
	if !containsFindingRecord(s.findingRecords[finding.ID], recordID) {
		s.findingRecords[finding.ID] = append(s.findingRecords[finding.ID], recordID)
	}
	s.findings[finding.ID] = mergeFindingMetadata(s.findings[finding.ID], finding)
	s.clearPendingFindingRecordsLocked([]string{recordID})
	s.bumpFindingVersionLocked(finding.ID)
}

func (s *JournalStateStore) removeFindingRecordLocked(findingID, recordID string) {
	recordIDs := s.findingRecords[findingID]
	for i, candidate := range recordIDs {
		if candidate != recordID {
			continue
		}
		recordIDs = append(recordIDs[:i], recordIDs[i+1:]...)
		if len(recordIDs) == 0 {
			delete(s.findings, findingID)
			delete(s.findingRecords, findingID)
		} else {
			s.findingRecords[findingID] = recordIDs
		}
		s.clearPendingFindingRecordsLocked([]string{recordID})
		s.bumpFindingVersionLocked(findingID)
		return
	}
}

func (s *JournalStateStore) applyFindingSnapshotLocked(findingID string, finding Finding, recordIDs []string, open bool) {
	if !open || len(recordIDs) == 0 {
		if _, exists := s.findings[findingID]; !exists && len(s.findingRecords[findingID]) == 0 {
			return
		}
		delete(s.findings, findingID)
		delete(s.findingRecords, findingID)
		s.bumpFindingVersionLocked(findingID)
		return
	}
	current, exists := s.findings[findingID]
	sameRecords := exists && sameFindingRecords(s.findingRecords[findingID], recordIDs)
	metadataChanged := false
	if !sameRecords {
		s.findings[findingID] = finding
		s.findingRecords[findingID] = append([]string(nil), recordIDs...)
	} else {
		// The same durable record may have fresher per-pass operator context in
		// memory because repeated blocking findings intentionally coalesce.
		merged := mergeFindingMetadata(current, finding)
		metadataChanged = merged != current
		s.findings[findingID] = merged
	}
	pendingCleared := s.clearPendingFindingRecordsLocked(recordIDs)
	if sameRecords && !metadataChanged && !pendingCleared {
		return
	}
	s.bumpFindingVersionLocked(findingID)
}

func containsFindingRecord(recordIDs []string, recordID string) bool {
	for _, candidate := range recordIDs {
		if candidate == recordID {
			return true
		}
	}
	return false
}

func sameFindingRecords(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for _, recordID := range left {
		if !containsFindingRecord(right, recordID) {
			return false
		}
	}
	return true
}

type openFindingRecord struct {
	recordID string
	finding  Finding
}

func replayFindingRecords(records []journal.Record) (map[string]Finding, map[string][]string) {
	openRecords := make(map[string][]openFindingRecord)
	for _, record := range records {
		if record.Type != journal.RecordReport {
			continue
		}
		if resolution, ok := decodeFindingResolutionRecord(record.Payload); ok {
			retained := openRecords[resolution.FindingID][:0]
			for _, candidate := range openRecords[resolution.FindingID] {
				if candidate.recordID != resolution.FindingRecordID {
					retained = append(retained, candidate)
				}
			}
			if len(retained) == 0 {
				delete(openRecords, resolution.FindingID)
			} else {
				openRecords[resolution.FindingID] = retained
			}
			continue
		}
		finding, ok := decodeFindingRecord(record.Payload)
		if !ok || finding.ID == "" || !findingIsBlocking(finding) {
			continue
		}
		alreadyOpen := false
		for _, candidate := range openRecords[finding.ID] {
			if candidate.recordID == record.RecordID {
				alreadyOpen = true
				break
			}
		}
		if !alreadyOpen {
			openRecords[finding.ID] = append(openRecords[finding.ID], openFindingRecord{
				recordID: record.RecordID,
				finding:  finding,
			})
		}
	}

	findings := make(map[string]Finding)
	findingRecords := make(map[string][]string)
	for findingID, candidates := range openRecords {
		if len(candidates) == 0 {
			continue
		}
		findings[findingID] = candidates[len(candidates)-1].finding
		findingRecords[findingID] = make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			findingRecords[findingID] = append(findingRecords[findingID], candidate.recordID)
		}
	}
	return findings, findingRecords
}

func decodeFindingResolutionRecord(payload []byte) (findingResolutionRecord, bool) {
	var report journal.ReportRecord
	if err := json.Unmarshal(payload, &report); err == nil && len(report.Payload) > 0 {
		var resolution findingResolutionRecord
		if err := json.Unmarshal(report.Payload, &resolution); err == nil && resolution.Marker == findingResolutionMarker && resolution.FindingID != "" && resolution.FindingRecordID != "" {
			return resolution, true
		}
	}
	var resolution findingResolutionRecord
	if err := json.Unmarshal(payload, &resolution); err == nil && resolution.Marker == findingResolutionMarker && resolution.FindingID != "" && resolution.FindingRecordID != "" {
		return resolution, true
	}
	return findingResolutionRecord{}, false
}

func decodeFindingRecord(payload []byte) (Finding, bool) {
	var report journal.ReportRecord
	if err := json.Unmarshal(payload, &report); err == nil && len(report.Payload) > 0 {
		var finding Finding
		if err := json.Unmarshal(report.Payload, &finding); err == nil {
			return finding, true
		}
	}
	var finding Finding
	if err := json.Unmarshal(payload, &finding); err == nil {
		return finding, true
	}
	return Finding{}, false
}

type noopStateStore struct{}

func (noopStateStore) LoadCursor(context.Context, ScopeKey, ReportStream) (Cursor, error) {
	return Cursor{}, nil
}
func (noopStateStore) BeginPass(context.Context, PassHeader) error  { return nil }
func (noopStateStore) RecordFinding(context.Context, Finding) error { return nil }
func (noopStateStore) CommitCursor(context.Context, Cursor) error   { return nil }
func (noopStateStore) LoadOpenFindings(context.Context, ScopeKey) ([]Finding, error) {
	return nil, nil
}

func DeterministicID(parts ...string) model.EventID {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(p))
	}
	return model.EventID(hex.EncodeToString(h.Sum(nil))[:32])
}

func PassID(scope ScopeKey, stableEventAt time.Time) model.ReconciliationID {
	return model.ReconciliationID(fmt.Sprintf("%s:%s", scope.String(), DeterministicID("pass", scope.String(), stableEventAt.UTC().Format(time.RFC3339Nano))))
}

func SyntheticTradeID(accountID string, fill model.Fill, stableEventAt time.Time) string {
	eventAt := fill.Timestamp
	if eventAt.IsZero() {
		eventAt = stableEventAt
	}
	return string(DeterministicID(
		"synthetic-fill",
		accountID,
		fill.InstrumentID.String(),
		fill.ClientID,
		fill.VenueOrderID,
		fill.Side.String(),
		fill.Price.String(),
		fill.Quantity.String(),
		eventAt.UTC().Format(time.RFC3339Nano),
	))
}
