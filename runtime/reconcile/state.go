package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

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
	Scope              ScopeKey
	Stream             ReportStream
	LastSuccessfulPass model.ReconciliationID
	LastReportID       model.ReportID
	LastVenueTime      time.Time
	LastLocalApplyTime time.Time
	LookbackFloor      time.Time
	Partial            bool
	UnresolvedIDs      []string
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
}

// StateStore keeps reconciliation cursors plus sticky operator-audit findings.
// Blocking findings are intentionally not auto-expired by a later clean pass:
// in this minimal live runtime they mean an operator-visible recovery/audit item
// that remains open until a future explicit resolution model is added.
type StateStore interface {
	LoadCursor(ctx context.Context, scope ScopeKey, stream ReportStream) (Cursor, error)
	BeginPass(ctx context.Context, pass PassHeader) error
	RecordFinding(ctx context.Context, finding Finding) error
	CommitCursor(ctx context.Context, cursor Cursor) error
	LoadOpenFindings(ctx context.Context, scope ScopeKey) ([]Finding, error)
}

type JournalStateStore struct {
	store    journal.Store
	findings map[string]Finding
}

func NewJournalStateStore(store journal.Store) *JournalStateStore {
	s := &JournalStateStore{store: store, findings: make(map[string]Finding)}
	s.replayFindings()
	return s
}

func (s *JournalStateStore) LoadCursor(ctx context.Context, scope ScopeKey, stream ReportStream) (Cursor, error) {
	cursors, err := s.store.LoadReconciliationCursors(ctx)
	if err != nil {
		return Cursor{}, err
	}
	var out Cursor
	for _, c := range cursors {
		var payload Cursor
		if err := json.Unmarshal([]byte(c.Cursor), &payload); err != nil {
			continue
		}
		if payload.Scope == scope && payload.Stream == stream {
			out = payload
		}
	}
	return out, nil
}

func (s *JournalStateStore) BeginPass(ctx context.Context, pass PassHeader) error {
	payload, err := json.Marshal(pass)
	if err != nil {
		return err
	}
	return s.store.AppendReconciliationPass(ctx, journal.ReconciliationPassRecord{
		RecordID:  string(DeterministicID("pass-record", string(pass.PassID))),
		PassID:    pass.PassID,
		StartedAt: pass.StartedAt,
		Reason:    pass.Scope.String(),
		Payload:   payload,
	})
}

func (s *JournalStateStore) RecordFinding(ctx context.Context, finding Finding) error {
	payload, err := json.Marshal(finding)
	if err != nil {
		return err
	}
	if err := s.store.AppendReport(ctx, journal.ReportRecord{
		RecordID:   finding.ID,
		ReportID:   model.ReportID(finding.PassID),
		ReportedAt: finding.CreatedAt,
		Payload:    payload,
	}); err != nil {
		return err
	}
	if s.findings == nil {
		s.findings = make(map[string]Finding)
	}
	s.findings[finding.ID] = finding
	return nil
}

func (s *JournalStateStore) CommitCursor(ctx context.Context, cursor Cursor) error {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return err
	}
	return s.store.CommitReconciliationCursor(ctx, journal.ReconciliationCursor{
		RecordID:  string(DeterministicID("cursor", cursor.Scope.String(), string(cursor.Stream), string(cursor.LastSuccessfulPass))),
		PassID:    cursor.LastSuccessfulPass,
		Scope:     cursor.Scope.String(),
		Stream:    string(cursor.Stream),
		Cursor:    string(payload),
		UpdatedAt: cursor.LastLocalApplyTime,
	})
}

func (s *JournalStateStore) LoadOpenFindings(ctx context.Context, scope ScopeKey) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.replayFindings()
	out := make([]Finding, 0, len(s.findings))
	for _, f := range s.findings {
		if f.Scope == scope && f.Blocking {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *JournalStateStore) replayFindings() {
	source, ok := s.store.(interface{ Records() []journal.Record })
	if !ok {
		return
	}
	if s.findings == nil {
		s.findings = make(map[string]Finding)
	}
	for _, record := range source.Records() {
		if record.Type != journal.RecordReport {
			continue
		}
		finding, ok := decodeFindingRecord(record.Payload)
		if ok && finding.ID != "" {
			s.findings[finding.ID] = finding
		}
	}
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
	return string(DeterministicID(
		"synthetic-fill",
		accountID,
		fill.InstrumentID.String(),
		fill.ClientID,
		fill.VenueOrderID,
		fill.Side.String(),
		fill.Price.String(),
		fill.Quantity.String(),
		stableEventAt.UTC().Format(time.RFC3339Nano),
	))
}
