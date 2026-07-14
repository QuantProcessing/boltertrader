package exec

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
)

var (
	ErrDuplicateClientID              = errors.New("exec: duplicate client id")
	ErrDuplicateVenueOrderID          = errors.New("exec: duplicate venue order id")
	ErrInFlightResultIdentityConflict = errors.New("exec: in-flight result identity conflict")
	// ErrInFlightIntentAlreadyResolved means authoritative event evidence won
	// the race with a later adapter return. The late result must not be journaled
	// or applied to cache; callers reconcile their return value from canonical
	// state instead.
	ErrInFlightIntentAlreadyResolved = errors.New("exec: in-flight intent already resolved")
)

type InFlightState string

const (
	InFlightSubmitted     InFlightState = "submitted"
	InFlightPendingCancel InFlightState = "pending_cancel"
	InFlightPendingModify InFlightState = "pending_modify"
)

type InFlightEntry struct {
	Intent      journal.CommandIntent
	State       InFlightState
	LastOutcome OutcomeClass
	LastError   string
	Attempts    int
	LastProbeAt time.Time
	JournalID   string
	ResolvedAt  time.Time
}

type InFlightJournal struct {
	mu       sync.RWMutex
	byClient map[string]InFlightEntry
	byVenue  map[string]string
}

func NewInFlightJournal() *InFlightJournal {
	return &InFlightJournal{
		byClient: make(map[string]InFlightEntry),
		byVenue:  make(map[string]string),
	}
}

func (j *InFlightJournal) TrackIntent(intent journal.CommandIntent, state InFlightState) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.trackIntentLocked(intent, state)
}

// TrackIntentChecked adds an intent without replacing recovery state for an
// older command that uses the same client or venue identity.
func (j *InFlightJournal) TrackIntentChecked(intent journal.CommandIntent, state InFlightState) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return trackIntentChecked(j.byClient, j.byVenue, intent, state)
}

func (j *InFlightJournal) trackIntentLocked(intent journal.CommandIntent, state InFlightState) {
	if prior, ok := j.byClient[intent.ClientID]; ok && prior.Intent.VenueOrderID != "" {
		deleteVenueOwner(j.byVenue, prior.Intent.VenueOrderID, intent.ClientID)
	}
	entry := InFlightEntry{
		Intent:    intent,
		State:     state,
		Attempts:  intent.Attempt,
		JournalID: intent.RecordID,
	}
	j.byClient[intent.ClientID] = entry
	if intent.VenueOrderID != "" {
		j.byVenue[intent.VenueOrderID] = intent.ClientID
	}
}

func trackIntentChecked(
	byClient map[string]InFlightEntry,
	byVenue map[string]string,
	intent journal.CommandIntent,
	state InFlightState,
) error {
	if _, ok := byClient[intent.ClientID]; ok {
		return fmt.Errorf("%w %q", ErrDuplicateClientID, intent.ClientID)
	}
	if intent.VenueOrderID != "" {
		if owner, ok := byVenue[intent.VenueOrderID]; ok && owner != intent.ClientID {
			return fmt.Errorf("%w %q", ErrDuplicateVenueOrderID, intent.VenueOrderID)
		}
	}
	entry := InFlightEntry{
		Intent:    intent,
		State:     state,
		Attempts:  intent.Attempt,
		JournalID: intent.RecordID,
	}
	byClient[intent.ClientID] = entry
	if intent.VenueOrderID != "" {
		byVenue[intent.VenueOrderID] = intent.ClientID
	}
	return nil
}

func (j *InFlightJournal) discardIntent(recordID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for clientID, entry := range j.byClient {
		if entry.Intent.RecordID != recordID {
			continue
		}
		delete(j.byClient, clientID)
		if entry.Intent.VenueOrderID != "" {
			deleteVenueOwner(j.byVenue, entry.Intent.VenueOrderID, clientID)
		}
		return
	}
}

func (j *InFlightJournal) ApplyResult(result journal.CommandResult) {
	_ = j.ApplyResultChecked(result)
}

// ApplyResultChecked applies a result only when every supplied identity still
// belongs to the same in-flight intent. A conflicting venue alias leaves all
// recovery state untouched.
func (j *InFlightJournal) ApplyResultChecked(result journal.CommandResult) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	entry, ok, err := j.validateResultLocked(result)
	if err != nil {
		return err
	}
	j.applyResultLocked(result, entry, ok)
	return nil
}

// commitResult keeps result identity validation, durable append, and in-memory
// resolution in one critical section so another intent cannot claim an alias
// between validation and persistence.
func (j *InFlightJournal) commitResult(result journal.CommandResult, appendResult func() error) error {
	finalize, err := j.prepareResultCommit(result, appendResult)
	if err != nil {
		return err
	}
	if finalize != nil {
		finalize()
	}
	return nil
}

// prepareResultCommit validates and durably appends a result while retaining
// the in-flight lock. The returned finalizer applies the in-memory resolution
// and releases that lock. Cache-backed command paths call the finalizer only
// after their prepared cache update is visible, so a concurrent command cannot
// observe a cleared intent together with stale order identity.
func (j *InFlightJournal) prepareResultCommit(result journal.CommandResult, appendResult func() error) (func(), error) {
	j.mu.Lock()
	entry, ok, err := j.validateResultLocked(result)
	if err != nil {
		j.mu.Unlock()
		return nil, err
	}
	if !ok {
		j.mu.Unlock()
		return nil, ErrInFlightIntentAlreadyResolved
	}
	if err := appendResult(); err != nil {
		j.mu.Unlock()
		return nil, err
	}
	return func() {
		j.applyResultLocked(result, entry, true)
		j.mu.Unlock()
	}, nil
}

func (j *InFlightJournal) validateResultLocked(result journal.CommandResult) (InFlightEntry, bool, error) {
	var entry InFlightEntry
	var ok bool
	if result.IntentRecordID != "" {
		for _, got := range j.byClient {
			if got.Intent.RecordID == result.IntentRecordID {
				entry = got
				ok = true
				break
			}
		}
		if !ok || (result.ClientID != "" && result.ClientID != entry.Intent.ClientID) {
			if ok {
				return InFlightEntry{}, false, fmt.Errorf(
					"%w: result client id %q does not match intent client id %q",
					ErrInFlightResultIdentityConflict,
					result.ClientID,
					entry.Intent.ClientID,
				)
			}
			entry = InFlightEntry{}
			ok = false
		}
		if !ok {
			return InFlightEntry{}, false, nil
		}
	} else {
		entry, ok = j.byClient[result.ClientID]
	}
	if result.VenueOrderID == "" {
		return entry, ok, nil
	}
	ownerClientID, mapped := j.byVenue[result.VenueOrderID]
	if !mapped {
		return entry, ok, nil
	}
	owner, ownerOK := j.byClient[ownerClientID]
	if !ownerOK {
		return InFlightEntry{}, false, venueResultIdentityConflict(result.VenueOrderID)
	}
	expectedRecordID := result.IntentRecordID
	if ok {
		expectedRecordID = entry.Intent.RecordID
	}
	if expectedRecordID != "" && owner.Intent.RecordID != expectedRecordID {
		return InFlightEntry{}, false, venueResultIdentityConflict(result.VenueOrderID)
	}
	if expectedRecordID == "" && result.ClientID != "" && ownerClientID != result.ClientID {
		return InFlightEntry{}, false, venueResultIdentityConflict(result.VenueOrderID)
	}
	return entry, ok, nil
}

func venueResultIdentityConflict(venueOrderID string) error {
	return errors.Join(
		ErrInFlightResultIdentityConflict,
		fmt.Errorf("%w %q", ErrDuplicateVenueOrderID, venueOrderID),
	)
}

func (j *InFlightJournal) applyResultLocked(result journal.CommandResult, entry InFlightEntry, ok bool) {
	if !ok {
		return
	}
	if result.VenueOrderID != "" {
		if entry.Intent.VenueOrderID != "" && entry.Intent.VenueOrderID != result.VenueOrderID {
			deleteVenueOwner(j.byVenue, entry.Intent.VenueOrderID, entry.Intent.ClientID)
		}
		entry.Intent.VenueOrderID = result.VenueOrderID
		j.byVenue[result.VenueOrderID] = entry.Intent.ClientID
	}
	entry.LastOutcome = OutcomeClass(result.Outcome)
	entry.LastError = result.Error
	if entry.LastOutcome == OutcomeAmbiguous {
		j.byClient[entry.Intent.ClientID] = entry
		return
	}
	entry.ResolvedAt = result.ResultAt
	delete(j.byClient, entry.Intent.ClientID)
	if entry.Intent.VenueOrderID != "" {
		deleteVenueOwner(j.byVenue, entry.Intent.VenueOrderID, entry.Intent.ClientID)
	}
}

func (j *InFlightJournal) Resolve(clientID, venueOrderID string, at time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if clientID == "" && venueOrderID != "" {
		clientID = j.byVenue[venueOrderID]
	}
	entry, ok := j.byClient[clientID]
	if !ok {
		return
	}
	entry.ResolvedAt = at
	delete(j.byClient, clientID)
	if entry.Intent.VenueOrderID != "" {
		deleteVenueOwner(j.byVenue, entry.Intent.VenueOrderID, clientID)
	}
	if venueOrderID != "" {
		deleteVenueOwner(j.byVenue, venueOrderID, clientID)
	}
}

func deleteVenueOwner(byVenue map[string]string, venueOrderID, clientID string) {
	if owner, ok := byVenue[venueOrderID]; ok && owner == clientID {
		delete(byVenue, venueOrderID)
	}
}

func (j *InFlightJournal) ByClientID(clientID string) (InFlightEntry, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	entry, ok := j.byClient[clientID]
	return entry, ok
}

func (j *InFlightJournal) ByVenueOrderID(venueOrderID string) (InFlightEntry, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	clientID, ok := j.byVenue[venueOrderID]
	if !ok {
		return InFlightEntry{}, false
	}
	entry, ok := j.byClient[clientID]
	return entry, ok
}

func (j *InFlightJournal) MatchFill(fill model.Fill) (InFlightEntry, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if fill.ClientID != "" {
		if entry, ok := j.byClient[fill.ClientID]; ok && inFlightEntryMatchesFill(entry, fill) {
			return entry, true
		}
	}
	if fill.VenueOrderID != "" {
		if clientID, ok := j.byVenue[fill.VenueOrderID]; ok {
			entry, entryOK := j.byClient[clientID]
			return entry, entryOK && inFlightEntryMatchesFill(entry, fill)
		}
	}
	if fill.ClientID != "" || fill.VenueOrderID == "" || fill.InstrumentID.Symbol == "" ||
		fill.Side == enums.SideUnknown || !fill.Quantity.IsPositive() {
		return InFlightEntry{}, false
	}
	var matched InFlightEntry
	matches := 0
	for _, entry := range j.byClient {
		if entry.State != InFlightSubmitted || entry.Intent.Type != journal.CommandSubmit {
			continue
		}
		if !inFlightEntryMatchesFill(entry, fill) ||
			(entry.Intent.Quantity.IsPositive() && fill.Quantity.GreaterThan(entry.Intent.Quantity)) {
			continue
		}
		matched = entry
		matches++
		if matches > 1 {
			return InFlightEntry{}, false
		}
	}
	return matched, matches == 1
}

func inFlightEntryMatchesFill(entry InFlightEntry, fill model.Fill) bool {
	intent := entry.Intent
	if fill.AccountID != "" && intent.AccountID != "" && fill.AccountID != intent.AccountID {
		return false
	}
	if fill.InstrumentID != (model.InstrumentID{}) && intent.InstrumentID != (model.InstrumentID{}) && fill.InstrumentID != intent.InstrumentID {
		return false
	}
	if fill.Side != enums.SideUnknown && intent.Side != enums.SideUnknown && fill.Side != intent.Side {
		return false
	}
	if fill.ClientID != "" && intent.ClientID != "" && fill.ClientID != intent.ClientID {
		return false
	}
	if fill.VenueOrderID != "" && intent.VenueOrderID != "" && fill.VenueOrderID != intent.VenueOrderID {
		return false
	}
	return true
}

func (j *InFlightJournal) Open() []InFlightEntry {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]InFlightEntry, 0, len(j.byClient))
	for _, entry := range j.byClient {
		out = append(out, entry)
	}
	return out
}

func (j *InFlightJournal) Count() int {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return len(j.byClient)
}

func (j *InFlightJournal) ReplayOpenIntents(intents []journal.CommandIntent) {
	for _, intent := range intents {
		state := InFlightSubmitted
		switch intent.Type {
		case journal.CommandCancel:
			state = InFlightPendingCancel
		case journal.CommandModify:
			state = InFlightPendingModify
		}
		j.TrackIntent(intent, state)
	}
}

// ReplayOpenIntentsChecked restores a complete journal batch transactionally.
// A conflicting identity leaves the existing in-memory recovery set unchanged.
func (j *InFlightJournal) ReplayOpenIntentsChecked(intents []journal.CommandIntent) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	byClient := make(map[string]InFlightEntry, len(j.byClient)+len(intents))
	for clientID, entry := range j.byClient {
		byClient[clientID] = entry
	}
	byVenue := make(map[string]string, len(j.byVenue)+len(intents))
	for venueOrderID, clientID := range j.byVenue {
		byVenue[venueOrderID] = clientID
	}
	for _, intent := range intents {
		state := InFlightSubmitted
		switch intent.Type {
		case journal.CommandCancel:
			state = InFlightPendingCancel
		case journal.CommandModify:
			state = InFlightPendingModify
		}
		if err := trackIntentChecked(byClient, byVenue, intent, state); err != nil {
			return err
		}
	}
	j.byClient = byClient
	j.byVenue = byVenue
	return nil
}
