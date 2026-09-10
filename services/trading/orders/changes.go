package orders

import (
	"context"
	"time"
)

// SubscribeChanges is a coalescing wakeup, not an event ledger. Consumers read
// a fresh owned snapshot after waking; a slow consumer cannot lose a fill.
func (service *Service) SubscribeChanges(principal string) (<-chan struct{}, func()) {
	service.changesMu.Lock()
	defer service.changesMu.Unlock()
	if service.changes == nil {
		service.changes = make(map[string]map[chan struct{}]struct{})
	}
	if service.changes[principal] == nil {
		service.changes[principal] = make(map[chan struct{}]struct{})
	}
	ch := make(chan struct{}, 1)
	service.changes[principal][ch] = struct{}{}
	return ch, func() {
		service.changesMu.Lock()
		defer service.changesMu.Unlock()
		delete(service.changes[principal], ch)
		if len(service.changes[principal]) == 0 {
			delete(service.changes, principal)
		}
	}
}
func (service *Service) NotifyChange(principal string) {
	service.changesMu.Lock()
	defer service.changesMu.Unlock()
	for ch := range service.changes[principal] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ExpediteAccount schedules only orders already waiting for venue evidence.
// It cannot consume a handoff admission, shorten an uncertain write's lease,
// or send any exchange command.
func (service *Service) ExpediteAccount(ctx context.Context, accountID string) error {
	if store, ok := service.store.(interface {
		ExpediteAccount(context.Context, string, time.Time) error
	}); ok {
		return store.ExpediteAccount(ctx, accountID, service.now().UTC())
	}
	return nil
}
func (store *MemoryStore) ExpediteAccount(_ context.Context, accountID string, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for id, task := range store.tasks {
		if store.commands[id].AccountID == accountID && (task.Reason == ReconciliationAwaitingTerminal || task.Reason == ReconciliationEvidenceInvalid) && task.NextAttempt.After(now) {
			task.NextAttempt = now
			store.tasks[id] = task
		}
	}
	return nil
}
