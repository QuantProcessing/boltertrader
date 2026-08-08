package funding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/calculation"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/planning"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
	"github.com/shopspring/decimal"
)

type processorFunc func(context.Context, Work) (Outcome, error)

func (function processorFunc) Process(ctx context.Context, work Work) (Outcome, error) {
	return function(ctx, work)
}

type fakeProtocolClient struct {
	mu sync.Mutex

	cancel context.CancelFunc
	claim  Claim

	claimCalls    int
	snapshotCalls int
	factsCalls    int
	heartbeats    int
	renewKeys     []string
	renewTTL      time.Duration
	renewed       chan struct{}
	intentRenewed chan struct{}
	termRenewed   chan struct{}
	snapshotErr   error
	factsErr      error
	inIntent      bool
	inTerminal    bool
	renewedIntent bool
	renewedTerm   bool
	intentKeys    []string
	completed     bool
	failed        bool
	failedOptions []FailOptions
	drained       bool
	events        []string
}

func (client *fakeProtocolClient) Register(context.Context) (int, error) { return 7, nil }

func (client *fakeProtocolClient) Heartbeat(context.Context, HeartbeatOptions) (Registration, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.heartbeats++
	return Registration{Generation: 7, State: "ready"}, nil
}

func (client *fakeProtocolClient) Readiness(context.Context, int) (Readiness, error) {
	return Readiness{WorkerGeneration: 7, CanClaim: true, FreshHeartbeat: true, State: "ready"}, nil
}

func (client *fakeProtocolClient) ClaimPage(ctx context.Context, _ ClaimOptions) (ClaimPage, error) {
	client.mu.Lock()
	client.claimCalls++
	call := client.claimCalls
	claim := client.claim
	cancel := client.cancel
	client.mu.Unlock()
	if call == 1 {
		return claimPage(claim), nil
	}
	if cancel != nil {
		cancel()
	}
	<-ctx.Done()
	return ClaimPage{}, ctx.Err()
}

func (client *fakeProtocolClient) Snapshot(context.Context, LeaseRef) (FactsPage, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.snapshotCalls++
	if client.snapshotErr != nil {
		return FactsPage{}, client.snapshotErr
	}
	return FactsPage{Facts: []Fact{{FactID: "fact-snapshot", Cursor: "cursor-snapshot"}}, NextCursor: "cursor-snapshot", ResnapshotRequired: true}, nil
}

func (client *fakeProtocolClient) Facts(context.Context, LeaseRef, string, int) (FactsPage, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.factsCalls++
	if client.factsErr != nil {
		return FactsPage{}, client.factsErr
	}
	if client.factsCalls == 1 {
		return FactsPage{}, &HTTPError{StatusCode: 409, Code: "RESNAPSHOT_REQUIRED", Retryable: true}
	}
	return FactsPage{Facts: []Fact{{FactID: "fact-page", Cursor: "cursor-final"}}, NextCursor: "cursor-final"}, nil
}

func (client *fakeProtocolClient) Renew(_ context.Context, lease LeaseRef, key string) (LeaseResult, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.renewKeys = append(client.renewKeys, key)
	client.renewedIntent = client.renewedIntent || client.inIntent
	client.renewedTerm = client.renewedTerm || client.inTerminal
	notifyRenewal(client.renewed)
	if client.inIntent {
		notifyRenewal(client.intentRenewed)
	}
	if client.inTerminal {
		notifyRenewal(client.termRenewed)
	}
	ttl := client.renewTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return LeaseResult{LeaseID: lease.LeaseID, WorkItemID: lease.WorkItemID, LeaseGeneration: lease.LeaseGeneration, ExpiresAt: time.Now().Add(ttl)}, nil
}

func (client *fakeProtocolClient) CommandIntent(_ context.Context, options IntentOptions) (LeaseResult, error) {
	client.mu.Lock()
	client.intentKeys = append(client.intentKeys, options.IdempotencyKey)
	client.events = append(client.events, "intent")
	client.inIntent = true
	renewed := client.intentRenewed
	client.mu.Unlock()
	if err := waitForRenewal(renewed, "intent"); err != nil {
		return LeaseResult{}, err
	}
	client.mu.Lock()
	client.inIntent = false
	client.mu.Unlock()
	return LeaseResult{Accepted: true}, nil
}

func (client *fakeProtocolClient) Complete(context.Context, CompleteOptions) (LeaseResult, error) {
	client.mu.Lock()
	client.inTerminal = true
	renewed := client.termRenewed
	client.mu.Unlock()
	if err := waitForRenewal(renewed, "terminalization"); err != nil {
		return LeaseResult{}, err
	}
	client.mu.Lock()
	client.inTerminal = false
	client.completed = true
	client.events = append(client.events, "complete")
	client.mu.Unlock()
	return LeaseResult{Completed: true}, nil
}

func (client *fakeProtocolClient) Fail(_ context.Context, options FailOptions) (LeaseResult, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.failed = true
	client.failedOptions = append(client.failedOptions, options)
	client.events = append(client.events, "fail")
	if client.cancel != nil {
		client.cancel()
	}
	return LeaseResult{Failed: true}, nil
}

func TestFailLeaseClassifiesTransientAndDeterministicFailures(t *testing.T) {
	tests := []struct {
		name      string
		cause     error
		retryable bool
	}{
		{name: "transport", cause: &HTTPError{StatusCode: http.StatusServiceUnavailable, Code: "DEPENDENCY_UNAVAILABLE", Retryable: true}, retryable: true},
		{name: "cancellation", cause: context.Canceled, retryable: true},
		{name: "deterministic validation", cause: errors.New("invalid funding work payload"), retryable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeProtocolClient{}
			identities, err := NewFileIdentityStore(t.TempDir()+"/identities.json", "worker-a", "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			runtime := &Runtime{client: client, identities: identities, retryInterval: time.Millisecond}
			lease := LeaseRef{WorkerGeneration: 1, WorkItemID: "work-1", WorkRevision: 1, LeaseID: "lease-1", LeaseGeneration: 1}
			if err := runtime.failLease(context.Background(), &lease, "cursor-1", test.cause); err != nil {
				t.Fatalf("failLease: %v", err)
			}
			if len(client.failedOptions) != 1 || client.failedOptions[0].Retryable != test.retryable {
				t.Fatalf("Fail options=%+v, want retryable=%t", client.failedOptions, test.retryable)
			}
		})
	}
}

func (client *fakeProtocolClient) Drain(context.Context, DrainOptions) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.drained = true
	client.events = append(client.events, "drain")
	return nil
}

func TestRuntimeResnapshotsRenewsCompletesAndDrains(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	claim := testRuntimeClaim()
	claim.ExpiresAt = time.Now().Add(150 * time.Millisecond)
	renewed := make(chan struct{}, 2)
	client := &fakeProtocolClient{cancel: cancel, claim: claim, renewTTL: 150 * time.Millisecond, renewed: renewed}
	identities, err := NewFileIdentityStore(t.TempDir()+"/identities.json", "worker-a", "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	processor := processorFunc(func(context.Context, Work) (Outcome, error) {
		for range 2 {
			if err := waitForRenewal(renewed, "processing"); err != nil {
				return Outcome{}, err
			}
		}
		return Outcome{
			Intents: []Intent{{Kind: "funding.open", Payload: json.RawMessage(`{"previewId":"preview-1"}`)}},
			Result:  json.RawMessage(`{"outcome":"completed","commandIds":["command-1"],"finalFactsCursor":"cursor-final","summaryCode":"OPENED"}`),
		}, nil
	})
	runtime, err := NewRuntime(RuntimeOptions{Client: client, Processor: processor, Identities: identities, HeartbeatInterval: 3 * time.Millisecond, RetryInterval: time.Millisecond, ShutdownTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.snapshotCalls != 2 || client.factsCalls != 2 {
		t.Fatalf("snapshot calls=%d facts calls=%d", client.snapshotCalls, client.factsCalls)
	}
	if len(client.renewKeys) < 2 || client.renewKeys[0] == client.renewKeys[1] {
		t.Fatalf("renew keys=%v", client.renewKeys)
	}
	if len(client.intentKeys) != 1 || !client.completed || client.failed || !client.drained {
		t.Fatalf("intent=%v completed=%t failed=%t drained=%t", client.intentKeys, client.completed, client.failed, client.drained)
	}
	if client.heartbeats < 2 {
		t.Fatalf("heartbeats=%d", client.heartbeats)
	}
}

func TestRuntimeRenewsThroughIntentHandoffAndTerminalization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	claim := testRuntimeClaim()
	claim.ExpiresAt = time.Now().Add(150 * time.Millisecond)
	client := &fakeProtocolClient{
		cancel: cancel, claim: claim, renewTTL: 150 * time.Millisecond,
		intentRenewed: make(chan struct{}, 1), termRenewed: make(chan struct{}, 1),
	}
	identities, err := NewFileIdentityStore(t.TempDir()+"/identities.json", "worker-a", "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(RuntimeOptions{
		Client: client,
		Processor: processorFunc(func(context.Context, Work) (Outcome, error) {
			return Outcome{Intents: []Intent{{Kind: "funding.open", Payload: json.RawMessage(`{"previewId":"preview-1"}`)}}, Result: json.RawMessage(`{"outcome":"completed"}`)}, nil
		}),
		Identities: identities, HeartbeatInterval: 3 * time.Millisecond, RetryInterval: time.Millisecond, ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Run(ctx); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if !client.renewedIntent || !client.renewedTerm || !client.completed {
		t.Fatalf("renewed during intent=%t terminal=%t completed=%t events=%v", client.renewedIntent, client.renewedTerm, client.completed, client.events)
	}
}

func notifyRenewal(channel chan struct{}) {
	if channel == nil {
		return
	}
	select {
	case channel <- struct{}{}:
	default:
	}
}

func waitForRenewal(channel <-chan struct{}, phase string) error {
	if channel == nil {
		return nil
	}
	select {
	case <-channel:
		return nil
	case <-time.After(time.Second):
		return errors.New("timed out waiting for renewal during " + phase)
	}
}

func TestIntentIdentitySurvivesLeaseReclaimAndFencesRevisionAndKind(t *testing.T) {
	client := &fakeProtocolClient{}
	identities, err := NewFileIdentityStore(t.TempDir()+"/identities.json", "worker-a", "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(RuntimeOptions{Client: client, Processor: processorFunc(func(context.Context, Work) (Outcome, error) { return Outcome{}, nil }), Identities: identities})
	if err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"outcome":"completed","commandIds":[],"finalFactsCursor":"cursor-1","summaryCode":"DONE"}`)
	finish := func(lease LeaseRef, kind string) {
		t.Helper()
		err := runtime.finishLease(context.Background(), &lease, json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-short","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`), "cursor-1", Outcome{Intents: []Intent{{Kind: kind, Payload: json.RawMessage(`{}`)}}, Result: result})
		if err != nil {
			t.Fatal(err)
		}
	}
	finish(LeaseRef{WorkerGeneration: 1, WorkItemID: "work-1", WorkRevision: 3, LeaseID: "lease-old", LeaseGeneration: 1}, "funding.open")
	finish(LeaseRef{WorkerGeneration: 1, WorkItemID: "work-1", WorkRevision: 3, LeaseID: "lease-reclaimed", LeaseGeneration: 2}, "funding.open")
	finish(LeaseRef{WorkerGeneration: 1, WorkItemID: "work-1", WorkRevision: 4, LeaseID: "lease-revision", LeaseGeneration: 3}, "funding.open")
	finish(LeaseRef{WorkerGeneration: 1, WorkItemID: "work-1", WorkRevision: 3, LeaseID: "lease-kind", LeaseGeneration: 4}, "funding.close")

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.intentKeys) != 4 || client.intentKeys[0] != client.intentKeys[1] || client.intentKeys[0] == client.intentKeys[2] || client.intentKeys[0] == client.intentKeys[3] {
		t.Fatalf("intent keys=%v", client.intentKeys)
	}
}

func TestRuntimeMarksTransientProcessingFailuresRetryable(t *testing.T) {
	tests := []struct {
		name          string
		client        *fakeProtocolClient
		processor     Processor
		wantRetryable bool
	}{
		{
			name:          "snapshot transport",
			client:        &fakeProtocolClient{claim: testRuntimeClaim(), snapshotErr: errors.New("dial tcp: host is down")},
			processor:     processorFunc(func(context.Context, Work) (Outcome, error) { return Outcome{}, nil }),
			wantRetryable: true,
		},
		{
			name:          "processor cancellation",
			client:        &fakeProtocolClient{claim: testRuntimeClaim()},
			processor:     processorFunc(func(context.Context, Work) (Outcome, error) { return Outcome{}, context.Canceled }),
			wantRetryable: true,
		},
		{
			name:          "processor invalid work",
			client:        &fakeProtocolClient{claim: testRuntimeClaim()},
			processor:     processorFunc(func(context.Context, Work) (Outcome, error) { return Outcome{}, errors.New("invalid work") }),
			wantRetryable: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identities, err := NewFileIdentityStore(t.TempDir()+"/identities.json", "worker-a", "instance-a")
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := NewRuntime(RuntimeOptions{Client: test.client, Processor: test.processor, Identities: identities, HeartbeatInterval: time.Hour, RetryInterval: time.Millisecond, ShutdownTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			test.client.cancel = cancel
			runErr := runtime.Run(ctx)
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				t.Fatalf("Run err=%v", runErr)
			}
			test.client.mu.Lock()
			defer test.client.mu.Unlock()
			if len(test.client.failedOptions) != 1 || test.client.failedOptions[0].Retryable != test.wantRetryable {
				t.Fatalf("failed options=%+v want retryable=%t", test.client.failedOptions, test.wantRetryable)
			}
		})
	}
}

func TestRuntimeCancellationFailsActiveLeaseBeforeDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeProtocolClient{claim: testRuntimeClaim()}
	identities, err := NewFileIdentityStore(t.TempDir()+"/identities.json", "worker-a", "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	processor := processorFunc(func(ctx context.Context, _ Work) (Outcome, error) {
		close(started)
		<-ctx.Done()
		return Outcome{}, ctx.Err()
	})
	runtime, err := NewRuntime(RuntimeOptions{Client: client, Processor: processor, Identities: identities, HeartbeatInterval: time.Hour, ShutdownTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if !client.failed || !client.drained || client.completed {
		t.Fatalf("failed=%t drained=%t completed=%t events=%v", client.failed, client.drained, client.completed, client.events)
	}
	if len(client.events) < 2 || client.events[len(client.events)-2] != "fail" || client.events[len(client.events)-1] != "drain" {
		t.Fatalf("events=%v", client.events)
	}
}

func TestRuntimeStopsFactsPagingWhenCursorDoesNotAdvance(t *testing.T) {
	client := &repeatedCursorClient{fakeProtocolClient: &fakeProtocolClient{}}
	runtime := &Runtime{client: client}
	lease := LeaseRef{WorkerGeneration: 1, WorkItemID: "work-1", WorkRevision: 1, LeaseID: "lease-1", LeaseGeneration: 1}
	facts, cursor, err := runtime.loadFacts(context.Background(), lease, "cursor-claim")
	if err != nil || len(facts) != 1 || cursor != "cursor-snapshot" || client.factsCalls != 1 {
		t.Fatalf("facts=%+v cursor=%q calls=%d err=%v", facts, cursor, client.factsCalls, err)
	}
}

func TestRenewalDelayLeavesOneThirdOfLeaseAsSafetyMargin(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	delay, err := renewalDelay(now, now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if delay != 20*time.Second {
		t.Fatalf("delay=%s", delay)
	}
}

func TestRenewalDelayRejectsMissingOrExpiredDeadline(t *testing.T) {
	now := time.Now()
	for _, expiresAt := range []time.Time{{}, now} {
		if _, err := renewalDelay(now, expiresAt); err == nil {
			t.Fatalf("expiresAt=%s: expected error", expiresAt)
		}
	}
}

func TestRenewLoopReplaysAmbiguousRenewalBeforeExpiry(t *testing.T) {
	client := &ambiguousRenewClient{}
	identities, err := NewFileIdentityStore(t.TempDir()+"/identities.json", "worker-a", "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{client: client, identities: identities, retryInterval: time.Millisecond}
	lease := LeaseRef{WorkerGeneration: 1, WorkItemID: "work-1", WorkRevision: 1, LeaseID: "lease-1", LeaseGeneration: 1}
	_, err = runtime.renewLease(context.Background(), &lease, time.Now().Add(time.Second))
	if err != nil {
		t.Fatalf("renewLoop error=%v", err)
	}
	if len(client.keys) != 2 || client.keys[0] != client.keys[1] {
		t.Fatalf("renew keys=%v", client.keys)
	}
}

func TestPayloadProcessorUsesOnlyPlatformWorkPlan(t *testing.T) {
	claim, facts := computedOpenWork(t)
	outcome, err := (PayloadProcessor{}).Process(context.Background(), Work{Claim: claim, Facts: facts})
	if err != nil || len(outcome.Intents) != 1 || outcome.Intents[0].Kind != "funding.open" || len(outcome.Result) == 0 {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestPayloadProcessorAdmitsOnlyConfiguredBoundedLiveClaims(t *testing.T) {
	disabled, err := NewPayloadProcessor("live", fundingpolicy.DefaultAdmission())
	if !errors.Is(err, fundingpolicy.ErrModeDisabled) || disabled != nil {
		t.Fatalf("disabled processor=%+v err=%v", disabled, err)
	}
	admission, err := fundingpolicy.NewAdmission(true, decimal.NewFromInt(75))
	if err != nil {
		t.Fatal(err)
	}
	processor, err := NewPayloadProcessor("live", admission)
	if err != nil {
		t.Fatal(err)
	}
	claim, facts := computedOpenWorkForMode(t, "live", admission, decimal.NewFromInt(75))
	outcome, err := processor.Process(context.Background(), Work{Claim: claim, Facts: facts})
	if err != nil || len(outcome.Intents) != 1 || outcome.Intents[0].Kind != "funding.open" {
		t.Fatalf("bounded Live outcome=%+v err=%v", outcome, err)
	}
	claim.Mode = "test"
	if _, err := processor.Process(context.Background(), Work{Claim: claim, Facts: facts}); err == nil {
		t.Fatal("cross-mode claim was accepted")
	}
	oversized, oversizedFacts := computedOpenWorkForMode(t, "live", admission, decimal.NewFromInt(76))
	if _, err := processor.Process(context.Background(), Work{Claim: oversized, Facts: oversizedFacts}); err == nil {
		t.Fatal("oversized Live claim was accepted")
	}
}

func TestPayloadProcessorKeepsExactTestNotionalCeiling(t *testing.T) {
	processor, err := NewPayloadProcessor("test", fundingpolicy.DefaultAdmission())
	if err != nil {
		t.Fatal(err)
	}
	claim, facts := computedOpenWorkForMode(t, "test", fundingpolicy.DefaultAdmission(), decimal.NewFromInt(100))
	if _, err := processor.Process(context.Background(), Work{Claim: claim, Facts: facts}); err != nil {
		t.Fatalf("100 USDT Test claim rejected: %v", err)
	}
	claim, facts = computedOpenWorkForMode(t, "test", fundingpolicy.DefaultAdmission(), decimal.NewFromInt(101))
	if _, err := processor.Process(context.Background(), Work{Claim: claim, Facts: facts}); err == nil {
		t.Fatal("101 USDT Test claim was accepted")
	}
}

func TestPayloadProcessorRelaysOnlyStrictFrozenRecoveryIntents(t *testing.T) {
	for _, test := range []struct{ kind, payload string }{
		{"close", `{"lineageId":"lineage-1","reason":"operator"}`},
		{"compensate", `{"lineageId":"lineage-1","failedCommandId":"failed","survivingCommandId":"surviving","maxCompensationNotional":"25","reason":"partial"}`},
		{"reconcile", `{"lineageId":"lineage-1","commandIds":["command-1"],"reason":"uncertain"}`},
	} {
		claim := Claim{WorkItemID: "work-1", WorkKind: test.kind, LineageID: "lineage-1", ProductID: "funding", Mode: "test", FactsCursor: "cursor-1", Payload: json.RawMessage(test.payload)}
		outcome, err := (PayloadProcessor{}).Process(context.Background(), Work{Claim: claim})
		if err != nil || len(outcome.Intents) != 1 || outcome.Intents[0].Kind != "funding."+test.kind {
			t.Fatalf("kind=%s outcome=%+v err=%v", test.kind, outcome, err)
		}
	}
	live := Claim{WorkKind: "close", LineageID: "lineage-1", ProductID: "funding", Mode: "live", Payload: json.RawMessage(`{"lineageId":"lineage-1","reason":"operator"}`)}
	if outcome, err := (PayloadProcessor{}).Process(context.Background(), Work{Claim: live}); err == nil || len(outcome.Intents) != 0 {
		t.Fatalf("live outcome=%+v err=%v", outcome, err)
	}
	oversized := Claim{WorkKind: "compensate", LineageID: "lineage-1", ProductID: "funding", Mode: "test", Payload: json.RawMessage(`{"lineageId":"lineage-1","failedCommandId":"failed","survivingCommandId":"surviving","maxCompensationNotional":"101","reason":"partial"}`)}
	if outcome, err := (PayloadProcessor{}).Process(context.Background(), Work{Claim: oversized}); err == nil || len(outcome.Intents) != 0 {
		t.Fatalf("oversized compensation outcome=%+v err=%v", outcome, err)
	}
}

func TestFileIdentityStoreReplaysAmbiguousOperationAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/identities.json"
	first, err := NewFileIdentityStore(path, "worker-a", "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	key, err := first.Begin("claim")
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewFileIdentityStore(path, "worker-a", "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Begin("claim")
	if err != nil || replayed != key {
		t.Fatalf("replayed=%q key=%q err=%v", replayed, key, err)
	}
	if err := restarted.Commit("claim", replayed); err != nil {
		t.Fatal(err)
	}
	next, err := restarted.Begin("claim")
	if err != nil || next == key {
		t.Fatalf("next=%q key=%q err=%v", next, key, err)
	}
}

func TestRuntimeReplaysAmbiguousClaimWithSameIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &ambiguousClaimClient{fakeProtocolClient: &fakeProtocolClient{}, cancel: cancel}
	identities, err := NewFileIdentityStore(t.TempDir()+"/identities.json", "worker-a", "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(RuntimeOptions{
		Client: client, Processor: processorFunc(func(context.Context, Work) (Outcome, error) { return Outcome{}, nil }), Identities: identities,
		HeartbeatInterval: time.Hour, RetryInterval: time.Millisecond, ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(client.keys) != 2 || client.keys[0] != client.keys[1] {
		t.Fatalf("claim keys=%v", client.keys)
	}
}

func TestRuntimeStopsOnStaleWorkerGeneration(t *testing.T) {
	client := &fakeProtocolClient{claim: testRuntimeClaim()}
	stale := &staleHeartbeatClient{fakeProtocolClient: client}
	identities, err := NewFileIdentityStore(t.TempDir()+"/identities.json", "worker-a", "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(RuntimeOptions{Client: stale, Processor: processorFunc(func(context.Context, Work) (Outcome, error) { return Outcome{}, nil }), Identities: identities})
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.Run(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != "WORKER_GENERATION_STALE" {
		t.Fatalf("Run error=%v", err)
	}
}

func TestRuntimeStopsClaimLoopWhenHeartbeatBecomesStale(t *testing.T) {
	client := &heartbeatBecomesStaleClient{fakeProtocolClient: &fakeProtocolClient{}}
	identities, err := NewFileIdentityStore(t.TempDir()+"/identities.json", "worker-a", "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(RuntimeOptions{
		Client: client, Processor: processorFunc(func(context.Context, Work) (Outcome, error) { return Outcome{}, nil }), Identities: identities,
		HeartbeatInterval: time.Millisecond, RetryInterval: time.Millisecond, ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.Run(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != "WORKER_GENERATION_STALE" {
		t.Fatalf("Run error=%v", err)
	}
}

type staleHeartbeatClient struct{ *fakeProtocolClient }

func (*staleHeartbeatClient) Heartbeat(context.Context, HeartbeatOptions) (Registration, error) {
	return Registration{}, &HTTPError{StatusCode: 409, Code: "WORKER_GENERATION_STALE", Retryable: false}
}

type heartbeatBecomesStaleClient struct {
	*fakeProtocolClient
	heartbeatCalls int
}

type ambiguousClaimClient struct {
	*fakeProtocolClient
	cancel context.CancelFunc
	keys   []string
}

type repeatedCursorClient struct {
	*fakeProtocolClient
	factsCalls int
}

type ambiguousRenewClient struct {
	fakeProtocolClient
	keys []string
}

func (client *ambiguousRenewClient) Renew(_ context.Context, lease LeaseRef, key string) (LeaseResult, error) {
	client.keys = append(client.keys, key)
	if len(client.keys) == 1 {
		return LeaseResult{}, &HTTPError{StatusCode: 503, Code: "UNAVAILABLE", Retryable: true}
	}
	return LeaseResult{LeaseID: lease.LeaseID, WorkItemID: lease.WorkItemID, LeaseGeneration: lease.LeaseGeneration, ExpiresAt: time.Now().Add(30 * time.Second)}, nil
}

func (client *repeatedCursorClient) Snapshot(context.Context, LeaseRef) (FactsPage, error) {
	return FactsPage{Facts: []Fact{{FactID: "fact-snapshot", Cursor: "cursor-snapshot"}}, NextCursor: "cursor-snapshot", ResnapshotRequired: true}, nil
}

func (client *repeatedCursorClient) Facts(context.Context, LeaseRef, string, int) (FactsPage, error) {
	client.factsCalls++
	return FactsPage{NextCursor: "cursor-snapshot"}, nil
}

func (client *ambiguousClaimClient) ClaimPage(_ context.Context, options ClaimOptions) (ClaimPage, error) {
	client.keys = append(client.keys, options.IdempotencyKey)
	if len(client.keys) == 1 {
		return ClaimPage{}, errors.New("response lost after server commit")
	}
	client.cancel()
	return ClaimPage{}, nil
}

func (client *heartbeatBecomesStaleClient) Heartbeat(context.Context, HeartbeatOptions) (Registration, error) {
	client.heartbeatCalls++
	if client.heartbeatCalls > 1 {
		return Registration{}, &HTTPError{StatusCode: 409, Code: "WORKER_GENERATION_STALE", Retryable: false}
	}
	return Registration{Generation: 7, State: "ready"}, nil
}

func (*heartbeatBecomesStaleClient) ClaimPage(ctx context.Context, _ ClaimOptions) (ClaimPage, error) {
	<-ctx.Done()
	return ClaimPage{}, ctx.Err()
}

func claimPage(claim Claim) ClaimPage {
	return ClaimPage{
		WorkItem: &WorkItem{
			WorkItemID: claim.WorkItemID, WorkKind: claim.WorkKind, LineageID: claim.LineageID, ProductID: claim.ProductID, Mode: claim.Mode, WorkRevision: claim.WorkRevision, FactsCursor: claim.FactsCursor,
			AccountFences: claim.AccountFences, Payload: claim.Payload, Attempt: claim.Attempt,
		},
		Lease:      &Lease{LeaseID: claim.LeaseID, LeaseGeneration: claim.LeaseGeneration, ExpiresAt: claim.ExpiresAt},
		NextCursor: claim.FactsCursor,
	}
}

func computedOpenWork(t *testing.T) (Claim, []Fact) {
	return computedOpenWorkForMode(t, "test", fundingpolicy.DefaultAdmission(), decimal.NewFromInt(50))
}

func computedOpenWorkForMode(t *testing.T, mode string, admission fundingpolicy.Admission, notional decimal.Decimal) (Claim, []Fact) {
	t.Helper()
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	leg := func(account string, generation int, venue, instrument, rate, mark string) calculation.LegFact {
		price := decimal.RequireFromString(mark)
		return calculation.LegFact{
			AccountFence: calculation.AccountFence{AccountID: account, RuntimeGeneration: generation}, Venue: venue, VenueEnvironment: mode, InstrumentID: instrument, Symbol: "BTCUSDT",
			Current: calculation.FundingPoint{Rate: decimal.RequireFromString(rate), ObservedAt: now.Add(-time.Second), FundingTime: now, NextFunding: now.Add(8 * time.Hour)},
			History: []calculation.FundingPoint{{Rate: decimal.RequireFromString(rate)}}, MarketObservedAt: now.Add(-time.Second), MarkPrice: price,
			BBO:               calculation.Quote{BidPrice: price, BidSize: decimal.NewFromInt(10), AskPrice: price, AskSize: decimal.NewFromInt(10)},
			Depth:             calculation.Depth{Bids: []calculation.Level{{Price: price, Quantity: decimal.NewFromInt(10)}}, Asks: []calculation.Level{{Price: price, Quantity: decimal.NewFromInt(10)}}},
			QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001"), MinNotional: decimal.NewFromInt(5), TakerFeeRate: decimal.RequireFromString("0.0001"),
		}
	}
	input := calculation.Input{Mode: mode, ProductID: "funding", OpportunityID: "opportunity-1", ObservedAt: now, RequestedNotionalPerLeg: notional, Long: leg("account-long", 4, "venue-a", "btc-a", "0.0001", "100"), Short: leg("account-short", 7, "venue-b", "btc-b", "0.0011", "100")}
	notionalText, currency := notional.String(), "USDT"
	authorities := []planning.ReservationAuthority{{ReservationID: "reservation-long", Revision: now.Add(-time.Minute), PrincipalID: "platform:funding", Mode: mode, ProductID: "funding", Kind: "open", AccountID: "account-long", Venue: "venue-a", VenueEnvironment: mode, InstrumentID: "btc-a", Notional: &notionalText, Currency: &currency, Status: "held"}, {ReservationID: "reservation-short", Revision: now.Add(-time.Minute), PrincipalID: "platform:funding", Mode: mode, ProductID: "funding", Kind: "open", AccountID: "account-short", Venue: "venue-b", VenueEnvironment: mode, InstrumentID: "btc-b", Notional: &notionalText, Currency: &currency, Status: "held"}}
	planningFact := planning.NewPlanningFact(input, "platform:funding", "reservation-long", "reservation-short", authorities, nil)
	input = planningFact.Input
	plan, _ := calculation.ComputeWithAdmission(input, admission)
	request := planning.OpenIntent{PreviewID: plan.PreviewID, OpportunityID: plan.OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: plan.RouteID, NotionalPerLeg: notional.String(), MaxSlippageBps: "10", HoldThroughSettlement: true}
	payload, _ := json.Marshal(request)
	factPayload, _ := json.Marshal(planningFact)
	fences := json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":11,"runtimeGeneration":4,"credentialVersion":3},{"accountId":"account-short","desiredStateRevision":13,"runtimeGeneration":7,"credentialVersion":5}]`)
	claim := Claim{WorkItemID: "work-1", WorkKind: "open", LineageID: "lineage-1", ProductID: "funding", Mode: mode, WorkRevision: 1, FactsCursor: "cursor-1", AccountFences: fences, Payload: payload}
	return claim, []Fact{{FactID: "fact-1", Cursor: "cursor-1", ProductID: "funding", Mode: mode, Kind: planning.FactKind, Payload: factPayload, EffectiveAt: now}}
}

func testRuntimeClaim() Claim {
	return Claim{
		WorkItemID: "work-1", WorkRevision: 1, LeaseID: "lease-1", LeaseGeneration: 1,
		ExpiresAt:   time.Now().Add(30 * time.Second),
		FactsCursor: "cursor-claim", AccountFences: json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-short","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`),
		Payload: json.RawMessage(`{"intents":[{"intentKind":"funding.open","payload":{"notionalPerLeg":"50"}}],"result":{"outcome":"completed","commandIds":["command-1"],"finalFactsCursor":"cursor-final","summaryCode":"OPENED"}}`), Attempt: 1,
	}
}
