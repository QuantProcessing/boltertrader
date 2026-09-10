package orders

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestOnlyOneCallerCanBeginExecutionAndRecoverySurvivesServiceRestart(t *testing.T) {
	now := time.Now().UTC()
	store := NewMemoryStore()
	service := newLiveService(store, func() time.Time { return now })
	command, _, err := service.Create(context.Background(), CreateRequest{PrincipalID: "p", AccountID: "a", ProductID: "manual", LineageID: "l", IdempotencyKey: "k", Method: "POST", Path: "/commands", CommandKind: "manual.place", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	winners := make(chan bool, 20)
	for range 20 {
		wg.Go(func() {
			_, started, err := service.BeginExecution(context.Background(), command.CommandID)
			if err != nil {
				t.Error(err)
			}
			winners <- started
		})
	}
	wg.Wait()
	close(winners)
	count := 0
	for won := range winners {
		if won {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("senders=%d", count)
	}
	restarted := NewService(store, func() time.Time { return now.Add(time.Minute) })
	current, started, err := restarted.BeginExecution(context.Background(), command.CommandID)
	if err != nil || started || current.State != StateUncertain {
		t.Fatalf("restart=%+v started=%v err=%v", current, started, err)
	}
	tasks, err := restarted.ClaimDueReconciliation(context.Background(), now.Add(time.Minute), time.Minute, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Reason != ReconciliationAmbiguousHandoff {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
}

func TestUnstartedCommandExpiresWithoutCallingGateway(t *testing.T) {
	for _, recoverByWorker := range []bool{false, true} {
		now := time.Now().UTC()
		store := NewMemoryStore()
		service := newLiveService(store, func() time.Time { return now })
		command, _, err := service.Create(context.Background(), CreateRequest{PrincipalID: "p", AccountID: "a", ProductID: "manual", LineageID: "l", IdempotencyKey: "k", Method: "POST", Path: "/commands", CommandKind: "manual.place", Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(HandoffStartWindow)
		gateway := &reconciliationGatewayFixture{}
		if recoverByWorker {
			runner, _ := NewReconciler(service, gateway, reconciliationDispatcherFixture{service}, func() time.Time { return now })
			if err := runner.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, started, err := service.BeginExecution(context.Background(), command.CommandID); err != nil || started {
				t.Fatalf("started=%v err=%v", started, err)
			}
		}
		current, err := service.Get(context.Background(), command.CommandID)
		if err != nil || current.State != StateFailed || len(current.Blockers) != 1 || current.Blockers[0].Code != HandoffNotStarted || gateway.calls != 0 {
			t.Fatalf("expired=%+v reads=%d err=%v", current, gateway.calls, err)
		}
		if tasks, err := service.ClaimDueReconciliation(context.Background(), now.Add(time.Hour), time.Minute, 10); err != nil || len(tasks) != 0 {
			t.Fatalf("tasks=%+v err=%v", tasks, err)
		}
	}
}
