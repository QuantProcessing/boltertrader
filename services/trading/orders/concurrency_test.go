package orders

import (
	"context"
	"errors"
	"testing"
	"time"
)

type pausedTransitionStore struct {
	Store
	entered, resume chan struct{}
}

func (store *pausedTransitionStore) Transition(ctx context.Context, request TransitionRequest, now time.Time) (Command, error) {
	if request.State == StateSubmitted {
		close(store.entered)
		select {
		case <-store.resume:
		case <-ctx.Done():
			return Command{}, ctx.Err()
		}
	}
	return store.Store.Transition(ctx, request, now)
}

func TestConcurrentTransitionCannotReopenTerminalCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := &pausedTransitionStore{Store: NewMemoryStore(), entered: make(chan struct{}), resume: make(chan struct{})}
	now := time.Now().UTC()
	if _, _, err := store.Create(ctx, Command{CommandID: "c", State: StateAccepted}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, func() time.Time { return now })
	finished := make(chan error, 1)
	go func() {
		_, err := service.Transition(ctx, TransitionRequest{CommandID: "c", State: StateSubmitted})
		finished <- err
	}()
	select {
	case <-store.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := service.Transition(ctx, TransitionRequest{CommandID: "c", State: StateFilled}); err != nil {
		t.Fatal(err)
	}
	close(store.resume)
	if err := <-finished; !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("stale transition err=%v", err)
	}
	current, err := service.Get(ctx, "c")
	if err != nil || current.State != StateFilled {
		t.Fatalf("command=%+v err=%v", current, err)
	}
	tasks, err := store.ClaimDue(ctx, now.Add(time.Minute), now.Add(2*time.Minute), 10)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("terminal command gained reconciliation tasks=%+v err=%v", tasks, err)
	}
}

func TestStoreMergesConcurrentEvidenceWithoutLosingFills(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if _, _, err := store.Create(ctx, Command{CommandID: "c", State: StateAccepted}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	start, done := make(chan struct{}), make(chan error, 2)
	for _, id := range []string{"fill-a", "fill-b"} {
		go func() {
			<-start
			_, err := store.Transition(ctx, TransitionRequest{CommandID: "c", State: StateSubmitted,
				Reason: ReconciliationAwaitingTerminal, NextAttemptAt: now.Add(time.Minute),
				Evidence: ExecutionEvidence{CommandID: "g-c", GatewayOrderID: "g-o", ClientOrderID: "1", State: "open", ResourceVersion: 2, Fills: []FillEvidence{{FillID: id}}},
			}, now)
			done <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.Get(ctx, "c")
	if err != nil || len(current.ExecutionEvidence.Fills) != 2 {
		t.Fatalf("merged command=%+v err=%v", current, err)
	}
}
