// Package journal provides the append-only recovery log used by the live
// runtime. Command intents are written before venue-mutating calls cross the
// adapter boundary; results, reports, applied events, and reconciliation
// cursors are appended after the fact so startup replay can reconstruct the
// unresolved work set.
package journal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

type RecordType string

const (
	RecordCommandIntent        RecordType = "command_intent"
	RecordCommandResult        RecordType = "command_result"
	RecordReport               RecordType = "report"
	RecordAppliedEvent         RecordType = "applied_event"
	RecordReconciliationPass   RecordType = "reconciliation_pass"
	RecordReconciliationCursor RecordType = "reconciliation_cursor"
)

type CommandType string

const (
	CommandSubmit CommandType = "submit"
	CommandCancel CommandType = "cancel"
	CommandModify CommandType = "modify"
)

const AmbiguousOutcome = "ambiguous"

type CommandIntent struct {
	RecordID      string             `json:"record_id"`
	CommandID     string             `json:"command_id"`
	Type          CommandType        `json:"type"`
	ClientID      string             `json:"client_id"`
	VenueOrderID  string             `json:"venue_order_id,omitempty"`
	InstrumentID  model.InstrumentID `json:"instrument_id"`
	Side          enums.OrderSide    `json:"side"`
	OrderType     enums.OrderType    `json:"order_type"`
	TIF           enums.TimeInForce  `json:"tif"`
	Quantity      decimal.Decimal    `json:"quantity"`
	Price         decimal.Decimal    `json:"price"`
	ReduceOnly    bool               `json:"reduce_only"`
	StrategyID    string             `json:"strategy_id,omitempty"`
	AccountID     string             `json:"account_id,omitempty"`
	SubmittedAt   time.Time          `json:"submitted_at"`
	CorrelationID string             `json:"correlation_id"`
	Attempt       int                `json:"attempt"`
	Metadata      map[string]string  `json:"metadata,omitempty"`
}

type CommandResult struct {
	RecordID       string      `json:"record_id"`
	IntentRecordID string      `json:"intent_record_id"`
	CommandID      string      `json:"command_id"`
	Type           CommandType `json:"type"`
	ClientID       string      `json:"client_id"`
	VenueOrderID   string      `json:"venue_order_id,omitempty"`
	Outcome        string      `json:"outcome"`
	Error          string      `json:"error,omitempty"`
	ResultAt       time.Time   `json:"result_at"`
}

type ReportRecord struct {
	RecordID   string          `json:"record_id"`
	ReportID   model.ReportID  `json:"report_id,omitempty"`
	ReportedAt time.Time       `json:"reported_at"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

type AppliedEventRecord struct {
	RecordID  string          `json:"record_id"`
	EventID   model.EventID   `json:"event_id,omitempty"`
	AppliedAt time.Time       `json:"applied_at"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type ReconciliationPassRecord struct {
	RecordID  string                 `json:"record_id"`
	PassID    model.ReconciliationID `json:"pass_id"`
	StartedAt time.Time              `json:"started_at"`
	EndedAt   time.Time              `json:"ended_at,omitempty"`
	Reason    string                 `json:"reason,omitempty"`
	Payload   json.RawMessage        `json:"payload,omitempty"`
}

type ReconciliationCursor struct {
	RecordID              string                 `json:"record_id"`
	PassID                model.ReconciliationID `json:"pass_id,omitempty"`
	Scope                 string                 `json:"scope"`
	Stream                string                 `json:"stream"`
	Cursor                string                 `json:"cursor"`
	UpdatedAt             time.Time              `json:"updated_at"`
	AppliedEventRecordIDs []string               `json:"applied_event_record_ids,omitempty"`
}

type Record struct {
	Sequence  uint64          `json:"sequence"`
	Type      RecordType      `json:"type"`
	RecordID  string          `json:"record_id"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type ReplayWarning struct {
	Offset int64
	Reason string
}

type Store interface {
	AppendCommandIntent(context.Context, CommandIntent) error
	AppendCommandResult(context.Context, CommandResult) error
	AppendReport(context.Context, ReportRecord) error
	AppendAppliedEvent(context.Context, AppliedEventRecord) error
	AppendReconciliationPass(context.Context, ReconciliationPassRecord) error
	CommitReconciliationCursor(context.Context, ReconciliationCursor) error
	OpenIntents(context.Context) ([]CommandIntent, error)
	LoadReconciliationCursors(context.Context) ([]ReconciliationCursor, error)
}

type DurableStore interface {
	Store
	Records() []Record
	UnsafeNoSync() bool
	Warnings() []ReplayWarning
	Close() error
}

var (
	ErrCorrupt = errors.New("journal: corrupt record")
	// ErrRecordIDConflict means a previously accepted record ID was reused for
	// a different type or payload.
	ErrRecordIDConflict = errors.New("journal: record id conflict")
	// ErrCommandResultConflict means a definitive command result would be
	// replaced by an ambiguous or semantically different result.
	ErrCommandResultConflict = errors.New("journal: command result conflict")
	// ErrConflictIndexCapacity means the exact conflict index reached its
	// configured hard limit. The journal fails closed instead of forgetting
	// historical RecordID or command-result semantics.
	ErrConflictIndexCapacity = errors.New("journal: conflict index capacity exceeded")
)

// ErrMissingDependency means a reconciliation cursor references an applied
// event that is not durably present in the journal. Such a cursor must never
// become the active replay watermark.
var ErrMissingDependency = errors.New("journal: missing applied-event dependency")

func NewRecordID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

type state struct {
	seq             uint64
	records         []Record
	seen            map[string]struct{}
	recordIdentity  map[string]recordFingerprint
	intents         map[string]CommandIntent
	results         map[string]CommandResult
	resultSemantics map[string]commandResultSemantic
	cursors         map[string]ReconciliationCursor
	blockingRecords map[string]struct{}
	appliedEvents   map[string]struct{}
	committedEvents map[string]struct{}
	warnings        []ReplayWarning
	droppedWarnings int
	historyLimit    int
	conflictLimit   int
	unprotected     int
	cursorEventRefs map[string]int
	compactionScans uint64
}

type recordFingerprint struct {
	recordType  RecordType
	payloadHash [sha256.Size]byte
}

type commandResultSemantic struct {
	intentRecordID string
	commandID      string
	commandType    CommandType
	clientID       string
	venueOrderID   string
	outcome        string
	resultError    string
}

type appendDisposition uint8

const (
	appendRecord appendDisposition = iota
	duplicateRecord
	duplicateCommandResult
)

// defaultMemoryRetentionLimit bounds diagnostic history retained in memory.
// Recovery-critical records are retained in addition to this window.
const defaultMemoryRetentionLimit = 100_000

// minConflictIndexLimit keeps small test history windows useful while still
// giving every journal an explicit finite identity horizon. Production derives
// the hard cap from the diagnostic history window.
const minConflictIndexLimit = 1_024

// maxReplayWarnings bounds diagnostics produced by malformed or incomplete
// append-only logs. The last slot becomes an aggregate omission notice after
// the limit is reached.
const maxReplayWarnings = 1_024

func newState(historyLimit int) *state {
	return newStateWithConflictLimit(historyLimit, 0)
}

func newStateWithConflictLimit(historyLimit, conflictLimit int) *state {
	if historyLimit <= 0 {
		historyLimit = defaultMemoryRetentionLimit
	}
	if conflictLimit <= 0 {
		conflictLimit = defaultConflictIndexLimit(historyLimit)
	}
	return &state{
		seen:            make(map[string]struct{}),
		recordIdentity:  make(map[string]recordFingerprint),
		intents:         make(map[string]CommandIntent),
		results:         make(map[string]CommandResult),
		resultSemantics: make(map[string]commandResultSemantic),
		cursors:         make(map[string]ReconciliationCursor),
		blockingRecords: make(map[string]struct{}),
		appliedEvents:   make(map[string]struct{}),
		committedEvents: make(map[string]struct{}),
		cursorEventRefs: make(map[string]int),
		historyLimit:    historyLimit,
		conflictLimit:   conflictLimit,
	}
}

func defaultConflictIndexLimit(historyLimit int) int {
	if historyLimit < minConflictIndexLimit/2 {
		return minConflictIndexLimit
	}
	maxInt := int(^uint(0) >> 1)
	if historyLimit > maxInt/2 {
		return maxInt
	}
	return historyLimit * 2
}

func (s *state) applyRecord(r Record) error {
	if r.RecordID == "" {
		return fmt.Errorf("%w: empty record id", ErrCorrupt)
	}
	disposition, err := s.validateRecord(r.Type, r.RecordID, r.Payload)
	if err != nil {
		return err
	}
	if disposition != appendRecord {
		if r.Sequence > s.seq {
			s.seq = r.Sequence
		}
		if disposition == duplicateCommandResult {
			if err := s.rememberRecordIdentity(r.Type, r.RecordID, r.Payload); err != nil {
				return err
			}
		}
		return nil
	}
	if err := s.rememberRecordIdentity(r.Type, r.RecordID, r.Payload); err != nil {
		return err
	}
	s.seen[r.RecordID] = struct{}{}
	if r.Sequence > s.seq {
		s.seq = r.Sequence
	}
	s.records = append(s.records, r)
	// New records start as ordinary history. Recovery-critical record types
	// subtract themselves below; state transitions add back records whose
	// protection ends. Keeping this count exact avoids scanning all retained
	// records merely to learn that compaction cannot be necessary yet.
	s.unprotected++
	switch r.Type {
	case RecordCommandIntent:
		var intent CommandIntent
		if err := json.Unmarshal(r.Payload, &intent); err != nil {
			return err
		}
		s.intents[intent.RecordID] = intent
		s.unprotected--
	case RecordCommandResult:
		var result CommandResult
		if err := json.Unmarshal(r.Payload, &result); err != nil {
			return err
		}
		previous, hadPrevious := s.results[result.IntentRecordID]
		s.results[result.IntentRecordID] = result
		s.resultSemantics[result.IntentRecordID] = commandResultSemantics(result)
		if result.Outcome == AmbiguousOutcome {
			// The new ambiguous result and its intent remain recovery-critical.
			s.unprotected--
			if hadPrevious {
				// Only the latest ambiguous result is protected.
				s.unprotected++
			}
		} else {
			// A definitive result releases the intent and any previous ambiguous
			// result. The new definitive result is already ordinary history.
			s.unprotected++
			if hadPrevious && previous.Outcome == AmbiguousOutcome {
				s.unprotected++
			}
		}
	case RecordReport:
		if resolvedRecordID := reportResolvedFindingRecordID(r.Payload); resolvedRecordID != "" {
			if _, wasBlocking := s.blockingRecords[resolvedRecordID]; wasBlocking {
				s.unprotected++
			}
			delete(s.blockingRecords, resolvedRecordID)
		} else if reportContainsBlockingFinding(r.Payload) {
			s.blockingRecords[r.RecordID] = struct{}{}
			s.unprotected--
		}
	case RecordAppliedEvent:
		s.appliedEvents[r.RecordID] = struct{}{}
		s.unprotected--
	case RecordReconciliationCursor:
		var cursor ReconciliationCursor
		if err := json.Unmarshal(r.Payload, &cursor); err != nil {
			return err
		}
		key := cursorKey(cursor)
		previous, hadPrevious := s.cursors[key]
		if hadPrevious {
			// Only the latest cursor for a scope/stream is protected.
			s.unprotected++
		}
		// The new cursor is recovery-critical.
		s.unprotected--
		s.replaceCursorEventRefs(previous, hadPrevious, cursor)
		s.cursors[key] = cursor
		for _, recordID := range cursor.AppliedEventRecordIDs {
			s.committedEvents[recordID] = struct{}{}
		}
	}
	s.compactIfNeeded()
	return nil
}

func (s *state) compactIfNeeded() {
	if s.unprotected <= s.historyLimit+retentionSlack(s.historyLimit) {
		return
	}

	s.compactionScans++
	protected := s.protectedRecordIDs()

	keepHistory := make(map[string]struct{}, s.historyLimit)
	for i := len(s.records) - 1; i >= 0 && len(keepHistory) < s.historyLimit; i-- {
		recordID := s.records[i].RecordID
		if _, ok := protected[recordID]; ok {
			continue
		}
		keepHistory[recordID] = struct{}{}
	}

	kept := make([]Record, 0, len(protected)+len(keepHistory))
	seen := make(map[string]struct{}, len(protected)+len(keepHistory))
	unprotected := 0
	for _, record := range s.records {
		_, protect := protected[record.RecordID]
		_, history := keepHistory[record.RecordID]
		if !protect && !history {
			continue
		}
		kept = append(kept, record)
		seen[record.RecordID] = struct{}{}
		if !protect {
			unprotected++
		}
	}
	s.records = kept
	s.seen = seen
	s.unprotected = unprotected
	s.pruneIndexesAfterCompaction()
}

func (s *state) replaceCursorEventRefs(previous ReconciliationCursor, hadPrevious bool, current ReconciliationCursor) {
	previousIDs := uniqueRecordIDs(previous.AppliedEventRecordIDs)
	currentIDs := uniqueRecordIDs(current.AppliedEventRecordIDs)

	if hadPrevious {
		for recordID := range previousIDs {
			if _, stillReferenced := currentIDs[recordID]; stillReferenced {
				continue
			}
			refs := s.cursorEventRefs[recordID]
			if refs <= 1 {
				delete(s.cursorEventRefs, recordID)
				// The old cursor committed this event. Once its final cursor
				// reference is removed, the event becomes ordinary history.
				s.unprotected++
				continue
			}
			s.cursorEventRefs[recordID] = refs - 1
		}
	}

	for recordID := range currentIDs {
		if hadPrevious {
			if _, alreadyReferenced := previousIDs[recordID]; alreadyReferenced {
				continue
			}
		}
		refs := s.cursorEventRefs[recordID]
		if refs == 0 {
			if _, alreadyCommitted := s.committedEvents[recordID]; alreadyCommitted {
				// A retained committed event can become protected again when a
				// later cursor starts depending on it.
				s.unprotected--
			}
		}
		s.cursorEventRefs[recordID] = refs + 1
	}
}

func uniqueRecordIDs(recordIDs []string) map[string]struct{} {
	unique := make(map[string]struct{}, len(recordIDs))
	for _, recordID := range recordIDs {
		unique[recordID] = struct{}{}
	}
	return unique
}

func retentionSlack(limit int) int {
	if limit <= 64 {
		return 0
	}
	slack := limit / 10
	if slack > 1024 {
		return 1024
	}
	return slack
}

func (s *state) protectedRecordIDs() map[string]struct{} {
	protected := make(map[string]struct{})
	for intentRecordID, intent := range s.intents {
		result, hasResult := s.results[intentRecordID]
		if hasResult && result.Outcome != AmbiguousOutcome {
			continue
		}
		protected[intent.RecordID] = struct{}{}
		if hasResult {
			protected[result.RecordID] = struct{}{}
		}
	}
	for _, cursor := range s.cursors {
		protected[cursor.RecordID] = struct{}{}
		for _, recordID := range cursor.AppliedEventRecordIDs {
			protected[recordID] = struct{}{}
		}
	}
	for recordID := range s.blockingRecords {
		protected[recordID] = struct{}{}
	}
	// AppliedEventRecord.Payload is opaque to the journal. An applied event is
	// committed only when a cursor explicitly lists its record ID as a
	// dependency; until then it remains recovery-critical regardless of payload.
	for recordID := range s.appliedEvents {
		if _, committed := s.committedEvents[recordID]; committed {
			continue
		}
		protected[recordID] = struct{}{}
	}
	return protected
}

func (s *state) pruneIndexesAfterCompaction() {
	for intentRecordID, intent := range s.intents {
		result, hasResult := s.results[intentRecordID]
		if hasResult && result.Outcome != AmbiguousOutcome {
			delete(s.intents, intentRecordID)
			delete(s.results, intentRecordID)
			continue
		}
		if _, retained := s.seen[intent.RecordID]; !retained {
			delete(s.intents, intentRecordID)
			delete(s.results, intentRecordID)
		}
	}
	for intentRecordID := range s.results {
		if _, retained := s.intents[intentRecordID]; !retained {
			delete(s.results, intentRecordID)
		}
	}
	for recordID := range s.blockingRecords {
		if _, retained := s.seen[recordID]; !retained {
			delete(s.blockingRecords, recordID)
		}
	}
	for recordID := range s.appliedEvents {
		if _, retained := s.seen[recordID]; !retained {
			delete(s.appliedEvents, recordID)
		}
	}
	for recordID := range s.committedEvents {
		if _, retained := s.seen[recordID]; !retained {
			delete(s.committedEvents, recordID)
		}
	}
}

func reportContainsBlockingFinding(payload []byte) bool {
	var report ReportRecord
	if err := json.Unmarshal(payload, &report); err == nil && len(report.Payload) > 0 {
		return jsonValueContainsBlocking(report.Payload)
	}
	return jsonValueContainsBlocking(payload)
}

const findingResolutionMarker = "boltertrader.reconcile.finding_resolution.v1"

type findingResolutionMarkerPayload struct {
	Marker          string `json:"marker"`
	FindingRecordID string `json:"finding_record_id"`
}

func reportResolvedFindingRecordID(payload []byte) string {
	var report ReportRecord
	if err := json.Unmarshal(payload, &report); err == nil && len(report.Payload) > 0 {
		payload = report.Payload
	}
	var resolution findingResolutionMarkerPayload
	if err := json.Unmarshal(payload, &resolution); err != nil || resolution.Marker != findingResolutionMarker {
		return ""
	}
	return resolution.FindingRecordID
}

func jsonValueContainsBlocking(payload []byte) bool {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return false
	}
	var contains func(any) bool
	contains = func(value any) bool {
		switch typed := value.(type) {
		case map[string]any:
			for key, nested := range typed {
				switch strings.ToLower(key) {
				case "blocking":
					if blocking, ok := nested.(bool); ok && blocking {
						return true
					}
				case "severity":
					if severity, ok := nested.(string); ok && strings.EqualFold(severity, "blocking") {
						return true
					}
				}
				if contains(nested) {
					return true
				}
			}
		case []any:
			for _, nested := range typed {
				if contains(nested) {
					return true
				}
			}
		}
		return false
	}
	return contains(value)
}

func (s *state) openIntents() []CommandIntent {
	out := make([]CommandIntent, 0)
	for recordID, intent := range s.intents {
		result, ok := s.results[recordID]
		if !ok || result.Outcome == AmbiguousOutcome {
			out = append(out, intent)
		}
	}
	return out
}

func (s *state) loadCursors() []ReconciliationCursor {
	out := make([]ReconciliationCursor, 0, len(s.cursors))
	for _, c := range s.cursors {
		out = append(out, c)
	}
	return out
}

func (s *state) snapshotRecords() []Record {
	out := make([]Record, len(s.records))
	copy(out, s.records)
	return out
}

func (s *state) validateCursorDependencies(cursor ReconciliationCursor) error {
	for _, recordID := range cursor.AppliedEventRecordIDs {
		if _, ok := s.appliedEvents[recordID]; !ok {
			return fmt.Errorf("%w %q", ErrMissingDependency, recordID)
		}
	}
	return nil
}

func fingerprintRecord(rt RecordType, payload []byte) recordFingerprint {
	return recordFingerprint{
		recordType:  rt,
		payloadHash: sha256.Sum256(payload),
	}
}

func (s *state) rememberRecordIdentity(rt RecordType, recordID string, payload []byte) error {
	if _, exists := s.recordIdentity[recordID]; exists {
		return nil
	}
	if len(s.recordIdentity) >= s.conflictLimit {
		return fmt.Errorf("%w: limit %d", ErrConflictIndexCapacity, s.conflictLimit)
	}
	s.recordIdentity[recordID] = fingerprintRecord(rt, payload)
	return nil
}

func (s *state) validateRecord(rt RecordType, recordID string, payload []byte) (appendDisposition, error) {
	if recordID == "" {
		return appendRecord, fmt.Errorf("journal: empty record id for %s", rt)
	}

	incoming := fingerprintRecord(rt, payload)
	if existing, duplicate := s.recordIdentity[recordID]; duplicate {
		if existing == incoming {
			return duplicateRecord, nil
		}
		return appendRecord, fmt.Errorf(
			"%w: record id %q changed from type %q to %q or changed payload",
			ErrRecordIDConflict,
			recordID,
			existing.recordType,
			rt,
		)
	}

	switch rt {
	case RecordCommandResult:
		var result CommandResult
		if err := json.Unmarshal(payload, &result); err != nil {
			return appendRecord, err
		}
		disposition, err := s.validateCommandResult(result)
		if err != nil || disposition != appendRecord {
			return disposition, err
		}
	case RecordReconciliationCursor:
		var cursor ReconciliationCursor
		if err := json.Unmarshal(payload, &cursor); err != nil {
			return appendRecord, err
		}
		if err := s.validateCursorDependencies(cursor); err != nil {
			return appendRecord, err
		}
	}
	if len(s.recordIdentity) >= s.conflictLimit {
		return appendRecord, fmt.Errorf("%w: limit %d", ErrConflictIndexCapacity, s.conflictLimit)
	}
	return appendRecord, nil
}

func (s *state) validateCommandResult(result CommandResult) (appendDisposition, error) {
	incoming := commandResultSemantics(result)
	previous, exists := s.resultSemantics[result.IntentRecordID]
	if !exists {
		intent, intentExists := s.intents[result.IntentRecordID]
		if !intentExists {
			return appendRecord, fmt.Errorf(
				"%w: result references unknown intent %q",
				ErrCommandResultConflict,
				result.IntentRecordID,
			)
		}
		if result.CommandID != intent.CommandID || result.Type != intent.Type || result.ClientID != intent.ClientID {
			return appendRecord, fmt.Errorf(
				"%w: result identity does not match intent %q",
				ErrCommandResultConflict,
				result.IntentRecordID,
			)
		}
		return appendRecord, nil
	}
	if previous == incoming {
		return duplicateCommandResult, nil
	}
	if previous.intentRecordID != incoming.intentRecordID ||
		previous.commandID != incoming.commandID ||
		previous.commandType != incoming.commandType ||
		previous.clientID != incoming.clientID {
		return appendRecord, fmt.Errorf(
			"%w: intent %q result changed command identity",
			ErrCommandResultConflict,
			result.IntentRecordID,
		)
	}
	if previous.outcome == AmbiguousOutcome {
		return appendRecord, nil
	}
	return appendRecord, fmt.Errorf(
		"%w: intent %q result transition %q -> %q changes definitive semantics",
		ErrCommandResultConflict,
		result.IntentRecordID,
		previous.outcome,
		result.Outcome,
	)
}

func commandResultSemantics(result CommandResult) commandResultSemantic {
	return commandResultSemantic{
		intentRecordID: result.IntentRecordID,
		commandID:      result.CommandID,
		commandType:    result.Type,
		clientID:       result.ClientID,
		venueOrderID:   result.VenueOrderID,
		outcome:        result.Outcome,
		resultError:    result.Error,
	}
}

func (s *state) appendPayload(rt RecordType, recordID string, payload any, now time.Time) (Record, []byte, error) {
	r, frame, ok, err := s.buildPayload(rt, recordID, payload, now)
	if err != nil || !ok {
		return r, frame, err
	}
	if err := s.applyRecord(r); err != nil {
		return Record{}, nil, err
	}
	return r, frame, nil
}

func (s *state) buildPayload(rt RecordType, recordID string, payload any, now time.Time) (Record, []byte, bool, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Record{}, nil, false, err
	}
	disposition, err := s.validateRecord(rt, recordID, body)
	if err != nil {
		return Record{}, nil, false, err
	}
	if disposition != appendRecord {
		return Record{}, nil, false, nil
	}
	r := Record{Sequence: s.seq + 1, Type: rt, RecordID: recordID, Timestamp: now, Payload: body}
	frame, err := marshalFrame(r)
	if err != nil {
		return Record{}, nil, false, err
	}
	return r, frame, true, nil
}

type MemoryJournal struct {
	mu sync.Mutex
	st *state
}

func NewMemory() *MemoryJournal { return NewMemoryWithRetention(defaultMemoryRetentionLimit) }

// NewMemoryWithRetention keeps a bounded window of ordinary diagnostic records
// in memory, in addition to recovery-critical records: open/ambiguous intents,
// blocking reports, uncommitted applied events, and the latest cursor per scope
// plus its applied-event dependencies. Limits above 64 use at most 1,024
// records of batching slack to avoid an O(limit) compaction on every append.
// An applied event becomes committed for retention only when a successfully
// committed cursor explicitly lists its record ID in AppliedEventRecordIDs;
// AppliedEventRecord.Payload remains opaque to the journal.
// Exact record fingerprints and command-result semantics are retained up to a
// finite hard cap derived from this window; reaching that cap fails closed
// instead of weakening replay semantics. A non-positive limit uses the
// conservative default.
func NewMemoryWithRetention(limit int) *MemoryJournal {
	return &MemoryJournal{st: newState(limit)}
}

func (j *MemoryJournal) AppendCommandIntent(ctx context.Context, intent CommandIntent) error {
	return j.append(ctx, RecordCommandIntent, intent.RecordID, intent)
}

func (j *MemoryJournal) AppendCommandResult(ctx context.Context, result CommandResult) error {
	return j.append(ctx, RecordCommandResult, result.RecordID, result)
}

func (j *MemoryJournal) AppendReport(ctx context.Context, report ReportRecord) error {
	return j.append(ctx, RecordReport, report.RecordID, report)
}

func (j *MemoryJournal) AppendAppliedEvent(ctx context.Context, event AppliedEventRecord) error {
	return j.append(ctx, RecordAppliedEvent, event.RecordID, event)
}

func (j *MemoryJournal) AppendReconciliationPass(ctx context.Context, pass ReconciliationPassRecord) error {
	return j.append(ctx, RecordReconciliationPass, pass.RecordID, pass)
}

func (j *MemoryJournal) CommitReconciliationCursor(ctx context.Context, cursor ReconciliationCursor) error {
	return j.append(ctx, RecordReconciliationCursor, cursor.RecordID, cursor)
}

func (j *MemoryJournal) OpenIntents(ctx context.Context) ([]CommandIntent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st.openIntents(), nil
}

func (j *MemoryJournal) LoadReconciliationCursors(ctx context.Context) ([]ReconciliationCursor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st.loadCursors(), nil
}

func (j *MemoryJournal) Records() []Record {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st.snapshotRecords()
}

func (j *MemoryJournal) UnsafeNoSync() bool        { return true }
func (j *MemoryJournal) Warnings() []ReplayWarning { return nil }
func (j *MemoryJournal) Close() error              { return nil }

func (j *MemoryJournal) append(ctx context.Context, rt RecordType, recordID string, payload any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	_, _, err := j.st.appendPayload(rt, recordID, payload, time.Now())
	return err
}

type FileOptions struct {
	UnsafeNoSync bool
}

type journalFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

type FileJournal struct {
	mu        sync.Mutex
	st        *state
	file      journalFile
	path      string
	unsafe    bool
	poisonErr error
}

func OpenFile(path string, opts FileOptions) (*FileJournal, error) {
	return OpenFileWithRetention(path, opts, defaultMemoryRetentionLimit)
}

// OpenFileWithRetention opens the same append-only durable journal as
// OpenFile, while bounding its replayed in-memory diagnostic window. It does
// not compact or rewrite the disk recovery log. A non-positive limit uses the
// conservative production default.
func OpenFileWithRetention(path string, opts FileOptions, limit int) (*FileJournal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	st := newState(limit)
	if err := replayFile(path, st); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	return &FileJournal{st: st, file: f, path: path, unsafe: opts.UnsafeNoSync}, nil
}

func (j *FileJournal) AppendCommandIntent(ctx context.Context, intent CommandIntent) error {
	return j.append(ctx, RecordCommandIntent, intent.RecordID, intent)
}

func (j *FileJournal) AppendCommandResult(ctx context.Context, result CommandResult) error {
	return j.append(ctx, RecordCommandResult, result.RecordID, result)
}

func (j *FileJournal) AppendReport(ctx context.Context, report ReportRecord) error {
	return j.append(ctx, RecordReport, report.RecordID, report)
}

func (j *FileJournal) AppendAppliedEvent(ctx context.Context, event AppliedEventRecord) error {
	return j.append(ctx, RecordAppliedEvent, event.RecordID, event)
}

func (j *FileJournal) AppendReconciliationPass(ctx context.Context, pass ReconciliationPassRecord) error {
	return j.append(ctx, RecordReconciliationPass, pass.RecordID, pass)
}

func (j *FileJournal) CommitReconciliationCursor(ctx context.Context, cursor ReconciliationCursor) error {
	return j.append(ctx, RecordReconciliationCursor, cursor.RecordID, cursor)
}

func (j *FileJournal) OpenIntents(ctx context.Context) ([]CommandIntent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st.openIntents(), nil
}

func (j *FileJournal) LoadReconciliationCursors(ctx context.Context) ([]ReconciliationCursor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st.loadCursors(), nil
}

func (j *FileJournal) Records() []Record {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st.snapshotRecords()
}

func (j *FileJournal) UnsafeNoSync() bool { return j.unsafe }

func (j *FileJournal) Warnings() []ReplayWarning {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]ReplayWarning, len(j.st.warnings))
	copy(out, j.st.warnings)
	return out
}

func (j *FileJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	file := j.file
	j.file = nil
	return file.Close()
}

func (j *FileJournal) append(ctx context.Context, rt RecordType, recordID string, payload any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		if j.poisonErr != nil {
			return fmt.Errorf("journal: file unavailable after append failure: %w", j.poisonErr)
		}
		return errors.New("journal: file is closed")
	}
	record, frame, ok, err := j.st.buildPayload(rt, recordID, payload, time.Now())
	if err != nil || !ok {
		return err
	}
	n, err := j.file.Write(frame)
	if n != len(frame) {
		if err == nil {
			err = io.ErrShortWrite
		} else {
			err = errors.Join(err, io.ErrShortWrite)
		}
	}
	if err != nil {
		return j.poisonLocked(err)
	}
	if !j.unsafe {
		if err := j.file.Sync(); err != nil {
			return j.poisonLocked(err)
		}
	}
	if err := j.st.applyRecord(record); err != nil {
		return j.poisonLocked(err)
	}
	return nil
}

func (j *FileJournal) poisonLocked(cause error) error {
	file := j.file
	j.file = nil
	if file != nil {
		if err := file.Close(); err != nil {
			cause = errors.Join(cause, fmt.Errorf("journal: close poisoned file: %w", err))
		}
	}
	j.poisonErr = cause
	return cause
}

func cursorKey(c ReconciliationCursor) string {
	return c.Scope + "\x00" + c.Stream
}

func marshalFrame(r Record) ([]byte, error) {
	payload, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return encodeFrame(payload), nil
}

func encodeFrame(payload []byte) []byte {
	frame := make([]byte, 12+len(payload))
	putUint64(frame[:8], uint64(len(payload)))
	putUint32(frame[8:12], crc32.ChecksumIEEE(payload))
	copy(frame[12:], payload)
	return frame
}

func replayFile(path string, st *state) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	var offset int64
	for {
		header := make([]byte, 12)
		n, err := io.ReadFull(f, header)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			st.addReplayWarning(ReplayWarning{Offset: offset, Reason: "truncated final frame header"})
			return os.Truncate(path, offset)
		}
		if err != nil {
			return err
		}
		if n != len(header) {
			return fmt.Errorf("%w: short frame header", ErrCorrupt)
		}
		length := getUint64(header[:8])
		if length > 64*1024*1024 {
			return fmt.Errorf("%w: frame length %d exceeds limit", ErrCorrupt, length)
		}
		wantCRC := getUint32(header[8:12])
		payload := make([]byte, int(length))
		if _, err := io.ReadFull(f, payload); errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			st.addReplayWarning(ReplayWarning{Offset: offset, Reason: "truncated final frame payload"})
			return os.Truncate(path, offset)
		} else if err != nil {
			return err
		}
		if got := crc32.ChecksumIEEE(payload); got != wantCRC {
			return fmt.Errorf("%w: checksum mismatch at offset %d", ErrCorrupt, offset)
		}
		var record Record
		if err := json.Unmarshal(payload, &record); err != nil {
			return fmt.Errorf("%w: decode at offset %d: %v", ErrCorrupt, offset, err)
		}
		if err := st.applyRecord(record); err != nil {
			if errors.Is(err, ErrMissingDependency) {
				st.addReplayWarning(ReplayWarning{Offset: offset, Reason: err.Error()})
				offset += int64(len(header)) + int64(len(payload))
				continue
			}
			return err
		}
		offset += int64(len(header)) + int64(len(payload))
	}
}

func (s *state) addReplayWarning(warning ReplayWarning) {
	if len(s.warnings) < maxReplayWarnings {
		s.warnings = append(s.warnings, warning)
		return
	}
	s.droppedWarnings++
	s.warnings[maxReplayWarnings-1] = ReplayWarning{
		Offset: warning.Offset,
		Reason: fmt.Sprintf(
			"additional replay warnings omitted: %d; latest reason: %s",
			s.droppedWarnings,
			warning.Reason,
		),
	}
}

func putUint32(dst []byte, v uint32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
	dst[3] = byte(v >> 24)
}

func putUint64(dst []byte, v uint64) {
	putUint32(dst[:4], uint32(v))
	putUint32(dst[4:], uint32(v>>32))
}

func getUint32(src []byte) uint32 {
	return uint32(src[0]) | uint32(src[1])<<8 | uint32(src[2])<<16 | uint32(src[3])<<24
}

func getUint64(src []byte) uint64 {
	return uint64(getUint32(src[:4])) | uint64(getUint32(src[4:]))<<32
}
