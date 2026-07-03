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

var ErrCorrupt = errors.New("journal: corrupt record")

func NewRecordID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

type state struct {
	seq      uint64
	records  []Record
	seen     map[string]struct{}
	intents  map[string]CommandIntent
	results  map[string]CommandResult
	cursors  map[string]ReconciliationCursor
	warnings []ReplayWarning
}

func newState() *state {
	return &state{
		seen:    make(map[string]struct{}),
		intents: make(map[string]CommandIntent),
		results: make(map[string]CommandResult),
		cursors: make(map[string]ReconciliationCursor),
	}
}

func (s *state) applyRecord(r Record) error {
	if r.RecordID == "" {
		return fmt.Errorf("%w: empty record id", ErrCorrupt)
	}
	if _, ok := s.seen[r.RecordID]; ok {
		return nil
	}
	s.seen[r.RecordID] = struct{}{}
	if r.Sequence > s.seq {
		s.seq = r.Sequence
	}
	s.records = append(s.records, r)
	switch r.Type {
	case RecordCommandIntent:
		var intent CommandIntent
		if err := json.Unmarshal(r.Payload, &intent); err != nil {
			return err
		}
		s.intents[intent.RecordID] = intent
	case RecordCommandResult:
		var result CommandResult
		if err := json.Unmarshal(r.Payload, &result); err != nil {
			return err
		}
		s.results[result.IntentRecordID] = result
	case RecordReconciliationCursor:
		var cursor ReconciliationCursor
		if err := json.Unmarshal(r.Payload, &cursor); err != nil {
			return err
		}
		s.cursors[cursorKey(cursor)] = cursor
	}
	return nil
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
	if recordID == "" {
		return Record{}, nil, false, fmt.Errorf("journal: empty record id for %s", rt)
	}
	if _, ok := s.seen[recordID]; ok {
		return Record{}, nil, false, nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Record{}, nil, false, err
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

func NewMemory() *MemoryJournal { return &MemoryJournal{st: newState()} }

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

type FileJournal struct {
	mu     sync.Mutex
	st     *state
	file   *os.File
	path   string
	unsafe bool
}

func OpenFile(path string, opts FileOptions) (*FileJournal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	st := newState()
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
	err := j.file.Close()
	j.file = nil
	return err
}

func (j *FileJournal) append(ctx context.Context, rt RecordType, recordID string, payload any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return errors.New("journal: file is closed")
	}
	record, frame, ok, err := j.st.buildPayload(rt, recordID, payload, time.Now())
	if err != nil || !ok {
		return err
	}
	if _, err := j.file.Write(frame); err != nil {
		return err
	}
	if !j.unsafe {
		if err := j.file.Sync(); err != nil {
			return err
		}
	}
	return j.st.applyRecord(record)
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
			st.warnings = append(st.warnings, ReplayWarning{Offset: offset, Reason: "truncated final frame header"})
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
			st.warnings = append(st.warnings, ReplayWarning{Offset: offset, Reason: "truncated final frame payload"})
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
			return err
		}
		offset += int64(len(header)) + int64(len(payload))
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
