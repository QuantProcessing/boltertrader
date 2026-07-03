package exec

import (
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
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

func (j *InFlightJournal) ApplyResult(result journal.CommandResult) {
	j.mu.Lock()
	defer j.mu.Unlock()
	entry, ok := j.byClient[result.ClientID]
	if !ok && result.IntentRecordID != "" {
		for _, got := range j.byClient {
			if got.Intent.RecordID == result.IntentRecordID {
				entry = got
				ok = true
				break
			}
		}
	}
	if !ok {
		return
	}
	if result.VenueOrderID != "" {
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
		delete(j.byVenue, entry.Intent.VenueOrderID)
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
		delete(j.byVenue, entry.Intent.VenueOrderID)
	}
	if venueOrderID != "" {
		delete(j.byVenue, venueOrderID)
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
		if entry, ok := j.byClient[fill.ClientID]; ok {
			return entry, true
		}
	}
	if fill.VenueOrderID != "" {
		if clientID, ok := j.byVenue[fill.VenueOrderID]; ok {
			entry, entryOK := j.byClient[clientID]
			return entry, entryOK
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
		if entry.Intent.VenueOrderID != "" && entry.Intent.VenueOrderID != fill.VenueOrderID {
			continue
		}
		if entry.Intent.InstrumentID != fill.InstrumentID {
			continue
		}
		if entry.Intent.Side != enums.SideUnknown && entry.Intent.Side != fill.Side {
			continue
		}
		if entry.Intent.Quantity.IsPositive() && fill.Quantity.GreaterThan(entry.Intent.Quantity) {
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
