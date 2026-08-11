package funding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/planning"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
	"github.com/shopspring/decimal"
)

const (
	defaultHeartbeatInterval     = 5 * time.Second
	defaultRetryInterval         = time.Second
	maxRenewalExpirySafetyMargin = time.Second
)

type ProtocolClient interface {
	Register(context.Context) (int, error)
	Heartbeat(context.Context, HeartbeatOptions) (Registration, error)
	Readiness(context.Context, int) (Readiness, error)
	ClaimPage(context.Context, ClaimOptions) (ClaimPage, error)
	Snapshot(context.Context, LeaseRef) (FactsPage, error)
	Facts(context.Context, LeaseRef, string, int) (FactsPage, error)
	Renew(context.Context, LeaseRef, string) (LeaseResult, error)
	CommandIntent(context.Context, IntentOptions) (LeaseResult, error)
	Complete(context.Context, CompleteOptions) (LeaseResult, error)
	Fail(context.Context, FailOptions) (LeaseResult, error)
	Drain(context.Context, DrainOptions) error
}

type IdentityStore interface {
	Begin(string) (string, error)
	Commit(string, string) error
}

type Intent struct {
	Kind    string
	Payload json.RawMessage
}

type Work struct {
	Claim Claim
	Facts []Fact
}

type Outcome struct {
	Intents []Intent
	Result  json.RawMessage
}

type Processor interface {
	Process(context.Context, Work) (Outcome, error)
}

type transientProtocolError struct {
	cause error
}

func (err transientProtocolError) Error() string { return err.cause.Error() }
func (err transientProtocolError) Unwrap() error { return err.cause }

type RuntimeOptions struct {
	Client            ProtocolClient
	Processor         Processor
	Identities        IdentityStore
	HeartbeatInterval time.Duration
	RetryInterval     time.Duration
	ClaimWaitSeconds  int
	ShutdownTimeout   time.Duration
	ValidationReady   func() error
}

type Runtime struct {
	client            ProtocolClient
	processor         Processor
	identities        IdentityStore
	heartbeatInterval time.Duration
	retryInterval     time.Duration
	claimWaitSeconds  int
	shutdownTimeout   time.Duration
	validationReady   func() error

	mu          sync.Mutex
	generation  int
	activeLease *LeaseRef
	factsCursor string
}

func NewRuntime(options RuntimeOptions) (*Runtime, error) {
	if options.Client == nil || options.Processor == nil || options.Identities == nil {
		return nil, fmt.Errorf("client, processor, and identity store are required")
	}
	if options.ClaimWaitSeconds < 0 || options.ClaimWaitSeconds > maxClaimWaitSeconds {
		return nil, fmt.Errorf("claim wait seconds must be between 0 and %d", maxClaimWaitSeconds)
	}
	if options.HeartbeatInterval <= 0 {
		options.HeartbeatInterval = defaultHeartbeatInterval
	}
	if options.RetryInterval <= 0 {
		options.RetryInterval = defaultRetryInterval
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = 10 * time.Second
	}
	return &Runtime{
		client: options.Client, processor: options.Processor, identities: options.Identities,
		heartbeatInterval: options.HeartbeatInterval,
		retryInterval:     options.RetryInterval, claimWaitSeconds: options.ClaimWaitSeconds,
		shutdownTimeout: options.ShutdownTimeout, validationReady: options.ValidationReady,
	}, nil
}

// ValidateAndWait performs only the passive Platform probe, publishes local
// readiness, and remains alive until the supervisor cancels the process.
func (runtime *Runtime) ValidateAndWait(ctx context.Context) error {
	passive, ok := runtime.client.(interface{ PassiveStatus(context.Context) error })
	if !ok {
		return fmt.Errorf("protocol client does not support passive status")
	}
	if err := passive.PassiveStatus(ctx); err != nil {
		return err
	}
	if runtime.validationReady != nil {
		if err := runtime.validationReady(); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return nil
}

func (runtime *Runtime) Run(ctx context.Context) error {
	generation, err := runtime.register(ctx)
	if err != nil {
		return err
	}
	runtime.mu.Lock()
	runtime.generation = generation
	runtime.mu.Unlock()
	if err := runtime.heartbeat(ctx, "ready"); err != nil {
		return err
	}
	ready, err := runtime.client.Readiness(ctx, generation)
	if err != nil {
		return err
	}
	if !ready.CanClaim {
		return fmt.Errorf("worker is not ready to claim")
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	heartbeatCtx, stopHeartbeat := context.WithCancel(runCtx)
	heartbeatDone := make(chan error, 1)
	go func() {
		heartbeatErr := runtime.heartbeatLoop(heartbeatCtx)
		if heartbeatErr != nil && !errors.Is(heartbeatErr, context.Canceled) {
			cancelRun()
		}
		heartbeatDone <- heartbeatErr
	}()

	var runErr error
	for runCtx.Err() == nil {
		claimKey, beginErr := runtime.identities.Begin("claim")
		if beginErr != nil {
			runErr = beginErr
			break
		}
		page, claimErr := runtime.client.ClaimPage(runCtx, ClaimOptions{WorkerGeneration: generation, IdempotencyKey: claimKey, AfterCursor: runtime.cursor(), WaitSeconds: runtime.claimWaitSeconds, MaxItems: 1})
		if claimErr != nil {
			if shouldRetry(claimErr) && wait(runCtx, runtime.retryInterval) {
				continue
			}
			if runCtx.Err() == nil {
				runErr = claimErr
			}
			break
		}
		if err := runtime.identities.Commit("claim", claimKey); err != nil {
			runErr = err
			break
		}
		if page.NextCursor != "" {
			runtime.setCursor(page.NextCursor)
		}
		claim, ok := page.Claim()
		if !ok {
			if !wait(runCtx, runtime.retryInterval) {
				break
			}
			continue
		}
		if err := runtime.processClaim(runCtx, generation, claim); err != nil {
			if reconcileLease(err) {
				continue
			}
			if runCtx.Err() == nil {
				runErr = err
			}
			break
		}
	}

	stopHeartbeat()
	select {
	case heartbeatErr := <-heartbeatDone:
		if runErr == nil && heartbeatErr != nil && !errors.Is(heartbeatErr, context.Canceled) {
			runErr = heartbeatErr
		}
	case <-time.After(runtime.shutdownTimeout):
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), runtime.shutdownTimeout)
	defer cancel()
	if err := runtime.drain(shutdownCtx); runErr == nil && err != nil {
		runErr = err
	}
	return runErr
}

func (runtime *Runtime) register(ctx context.Context) (int, error) {
	for {
		generation, err := runtime.client.Register(ctx)
		if err == nil {
			return generation, nil
		}
		if !shouldRetry(err) || !wait(ctx, runtime.retryInterval) {
			return 0, err
		}
	}
}

func (runtime *Runtime) heartbeatLoop(ctx context.Context) error {
	ticker := time.NewTicker(runtime.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := runtime.heartbeat(ctx, "ready"); err != nil && !shouldRetry(err) {
				return err
			}
		}
	}
}

func (runtime *Runtime) heartbeat(ctx context.Context, state string) error {
	runtime.mu.Lock()
	options := HeartbeatOptions{WorkerGeneration: runtime.generation, State: state, LastAppliedFactsCursor: runtime.factsCursor}
	if runtime.activeLease != nil {
		options.ActiveLeaseIDs = []string{runtime.activeLease.LeaseID}
	} else {
		options.ActiveLeaseIDs = []string{}
	}
	runtime.mu.Unlock()
	_, err := runtime.client.Heartbeat(ctx, options)
	return err
}

func (runtime *Runtime) processClaim(ctx context.Context, generation int, claim Claim) error {
	lease := LeaseRef{WorkerGeneration: generation, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, LeaseID: claim.LeaseID, LeaseGeneration: claim.LeaseGeneration}
	if err := validateLeaseRef(lease); err != nil {
		return err
	}
	runtime.mu.Lock()
	runtime.activeLease = &lease
	runtime.mu.Unlock()
	defer func() {
		runtime.mu.Lock()
		runtime.activeLease = nil
		runtime.mu.Unlock()
	}()

	renewCtx, stopRenew := context.WithCancel(context.Background())
	renewDone := make(chan error, 1)
	go func() { renewDone <- runtime.renewLoop(renewCtx, &lease, claim.ExpiresAt) }()

	facts, cursor, workErr := runtime.loadFacts(ctx, lease, claim.FactsCursor)
	var outcome Outcome
	if workErr == nil {
		outcome, workErr = runtime.processor.Process(ctx, Work{Claim: claim, Facts: facts})
	}
	terminalCtx, cancel := context.WithTimeout(context.Background(), runtime.shutdownTimeout)
	var terminalErr error
	if workErr != nil {
		terminalErr = runtime.failLease(terminalCtx, &lease, cursor, workErr)
	} else {
		terminalErr = runtime.finishLease(terminalCtx, &lease, claim.AccountFences, cursor, outcome)
	}
	cancel()
	stopRenew()
	var renewErr error
	select {
	case renewErr = <-renewDone:
		if errors.Is(renewErr, context.Canceled) {
			renewErr = nil
		}
	case <-time.After(runtime.shutdownTimeout):
		renewErr = fmt.Errorf("lease renew loop did not stop")
	}
	if renewErr != nil {
		return renewErr
	}
	return terminalErr
}

func (runtime *Runtime) loadFacts(ctx context.Context, lease LeaseRef, claimCursor string) ([]Fact, string, error) {
	snapshot, err := runtime.client.Snapshot(ctx, lease)
	if err != nil {
		return nil, "", classifyProtocolError(err)
	}
	facts := append([]Fact(nil), snapshot.Facts...)
	cursor := snapshot.NextCursor
	if cursor == "" {
		cursor = claimCursor
	}
	resnapshotted := false
	for {
		page, pageErr := runtime.client.Facts(ctx, lease, cursor, 500)
		if pageErr != nil {
			var httpErr *HTTPError
			if errors.As(pageErr, &httpErr) && httpErr.Code == "RESNAPSHOT_REQUIRED" {
				if resnapshotted {
					return nil, cursor, pageErr
				}
				resnapshotted = true
				snapshot, pageErr = runtime.client.Snapshot(ctx, lease)
				if pageErr == nil {
					facts = append([]Fact(nil), snapshot.Facts...)
					cursor = snapshot.NextCursor
					continue
				}
			}
			return nil, cursor, classifyProtocolError(pageErr)
		}
		facts = append(facts, page.Facts...)
		nextCursor := page.NextCursor
		if nextCursor == "" || nextCursor == cursor || len(page.Facts) < 500 {
			if nextCursor != "" {
				cursor = nextCursor
			}
			break
		}
		cursor = nextCursor
	}
	runtime.mu.Lock()
	runtime.factsCursor = cursor
	runtime.mu.Unlock()
	return facts, cursor, nil
}

func (runtime *Runtime) renewLoop(ctx context.Context, lease *LeaseRef, expiresAt time.Time) error {
	for {
		delay, err := renewalDelay(time.Now(), expiresAt)
		if err != nil {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}

		expiresAt, err = runtime.renewLease(ctx, lease, expiresAt)
		if err != nil {
			return err
		}
	}
}

func (runtime *Runtime) renewLease(ctx context.Context, lease *LeaseRef, expiresAt time.Time) (time.Time, error) {
	operation := "renew:" + lease.LeaseID
	key, err := runtime.identities.Begin(operation)
	if err != nil {
		return time.Time{}, err
	}
	for {
		remaining := expiresAt.Sub(time.Now())
		attemptDeadline := expiresAt.Add(-renewalSafetyMargin(remaining))
		if !attemptDeadline.After(time.Now()) {
			return time.Time{}, fmt.Errorf("lease renewal safety deadline reached")
		}
		attemptCtx, cancelAttempt := context.WithDeadline(ctx, attemptDeadline)
		runtime.mu.Lock()
		result, renewErr := runtime.client.Renew(attemptCtx, *lease, key)
		if renewErr == nil && result.LeaseGeneration > lease.LeaseGeneration {
			lease.LeaseGeneration = result.LeaseGeneration
			if runtime.activeLease != nil {
				runtime.activeLease.LeaseGeneration = result.LeaseGeneration
			}
		}
		runtime.mu.Unlock()
		cancelAttempt()
		if renewErr != nil {
			if shouldRetry(renewErr) {
				retryDelay, delayErr := renewalRetryDelay(time.Now(), expiresAt, runtime.retryInterval)
				if delayErr != nil {
					return time.Time{}, renewErr
				}
				if !wait(ctx, retryDelay) {
					return time.Time{}, ctx.Err()
				}
				continue
			}
			return time.Time{}, renewErr
		}
		if _, err := renewalDelay(time.Now(), result.ExpiresAt); err != nil {
			return time.Time{}, fmt.Errorf("renew lease returned invalid expiry: %w", err)
		}
		if err := runtime.identities.Commit(operation, key); err != nil {
			return time.Time{}, err
		}
		return result.ExpiresAt, nil
	}
}

func renewalDelay(now, expiresAt time.Time) (time.Duration, error) {
	remaining := expiresAt.Sub(now)
	if expiresAt.IsZero() || remaining <= 0 {
		return 0, fmt.Errorf("lease expiry must be in the future")
	}
	return remaining - remaining/3, nil
}

func renewalRetryDelay(now, expiresAt time.Time, retryInterval time.Duration) (time.Duration, error) {
	remaining := expiresAt.Sub(now)
	remaining -= renewalSafetyMargin(remaining)
	if expiresAt.IsZero() || remaining <= 0 {
		return 0, fmt.Errorf("lease renewal safety deadline reached before retry")
	}
	limit := remaining / 3
	delay := retryInterval
	if retryInterval <= 0 || retryInterval > limit {
		delay = limit
	}
	if delay+controlRequestTimeout > remaining {
		return 0, fmt.Errorf("lease renewal retry budget exceeds safety deadline")
	}
	return delay, nil
}

func renewalSafetyMargin(remaining time.Duration) time.Duration {
	margin := remaining / 5
	if margin > maxRenewalExpirySafetyMargin {
		return maxRenewalExpirySafetyMargin
	}
	return margin
}

func (runtime *Runtime) finishLease(ctx context.Context, lease *LeaseRef, accountFences json.RawMessage, factsCursor string, outcome Outcome) error {
	for index, intent := range outcome.Intents {
		key := stableIntentKey(lease.WorkItemID, lease.WorkRevision, index, intent.Kind)
		err := runtime.retry(ctx, func() error {
			runtime.mu.Lock()
			currentLease := *lease
			runtime.mu.Unlock()
			_, requestErr := runtime.client.CommandIntent(ctx, IntentOptions{LeaseRef: currentLease, IdempotencyKey: key, IntentKind: intent.Kind, FactsCursor: factsCursor, AccountFences: accountFences, Payload: intent.Payload})
			return requestErr
		})
		if err != nil {
			return err
		}
	}
	key, err := runtime.identities.Begin("complete:" + lease.LeaseID)
	if err != nil {
		return err
	}
	err = runtime.retry(ctx, func() error {
		runtime.mu.Lock()
		currentLease := *lease
		runtime.mu.Unlock()
		_, requestErr := runtime.client.Complete(ctx, CompleteOptions{LeaseRef: currentLease, IdempotencyKey: key, FactsCursor: factsCursor, Result: outcome.Result})
		return requestErr
	})
	if err != nil {
		return err
	}
	return runtime.identities.Commit("complete:"+lease.LeaseID, key)
}

func stableIntentKey(workItemID string, workRevision, intentIndex int, intentKind string) string {
	identity := fmt.Sprintf("%s\x00%d\x00%d\x00%s", workItemID, workRevision, intentIndex, intentKind)
	return fmt.Sprintf("intent:%x", sha256.Sum256([]byte(identity)))
}

func (runtime *Runtime) failLease(ctx context.Context, lease *LeaseRef, factsCursor string, cause error) error {
	key, err := runtime.identities.Begin("fail:" + lease.LeaseID)
	if err != nil {
		return err
	}
	details, _ := json.Marshal(map[string]any{"blockerCodes": []string{"PROCESSING_FAILED"}, "commandIds": []string{}, "lastSafeState": "facts_applied"})
	failErr := runtime.retry(ctx, func() error {
		runtime.mu.Lock()
		currentLease := *lease
		runtime.mu.Unlock()
		_, requestErr := runtime.client.Fail(ctx, FailOptions{LeaseRef: currentLease, IdempotencyKey: key, FactsCursor: factsCursor, ErrorCode: "PROCESSING_FAILED", Retryable: retryableProcessingFailure(cause), Details: details})
		return requestErr
	})
	if failErr != nil {
		return fmt.Errorf("process work: %v; fail lease: %w", cause, failErr)
	}
	if err := runtime.identities.Commit("fail:"+lease.LeaseID, key); err != nil {
		return err
	}
	return nil
}

func retryableProcessingFailure(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Retryable
	}
	var protocolErr transientProtocolError
	if errors.As(err, &protocolErr) {
		return true
	}
	var transportErr net.Error
	return errors.As(err, &transportErr)
}

func classifyProtocolError(err error) error {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return err
	}
	return transientProtocolError{cause: err}
}

func (runtime *Runtime) drain(ctx context.Context) error {
	key, err := runtime.identities.Begin("drain")
	if err != nil {
		return err
	}
	err = runtime.retry(ctx, func() error {
		return runtime.client.Drain(ctx, DrainOptions{WorkerGeneration: runtime.generation, IdempotencyKey: key, Reason: "shutdown"})
	})
	if err != nil {
		return err
	}
	return runtime.identities.Commit("drain", key)
}

func (runtime *Runtime) cursor() string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.factsCursor
}

func (runtime *Runtime) setCursor(cursor string) {
	runtime.mu.Lock()
	runtime.factsCursor = cursor
	runtime.mu.Unlock()
}

func shouldRetry(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Retryable
	}
	return true
}

func reconcileLease(err error) bool {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	switch httpErr.Code {
	case "WORK_LEASE_EXPIRED", "WORK_LEASE_STALE", "WORK_AUTHORIZATION_STALE", "WORK_REVISION_CONFLICT", "RESNAPSHOT_REQUIRED":
		return true
	default:
		return false
	}
}

func (runtime *Runtime) retry(ctx context.Context, operation func() error) error {
	for {
		err := operation()
		if err == nil || !shouldRetry(err) || !wait(ctx, runtime.retryInterval) {
			return err
		}
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type PayloadProcessor struct {
	mode      string
	admission fundingpolicy.Admission
}

func NewPayloadProcessor(mode string, admission fundingpolicy.Admission) (*PayloadProcessor, error) {
	if mode != "test" && mode != "live" {
		return nil, fmt.Errorf("invalid Funding worker mode")
	}
	if mode == "live" && !admission.LiveEnabled() {
		return nil, fundingpolicy.ErrModeDisabled
	}
	return &PayloadProcessor{mode: mode, admission: admission}, nil
}

func (processor PayloadProcessor) Process(_ context.Context, job Work) (Outcome, error) {
	mode := processor.mode
	if mode == "" {
		mode = "test"
	}
	if job.Claim.Mode != mode || (mode == "live" && !processor.admission.LiveEnabled()) {
		return Outcome{}, fmt.Errorf("Funding claim mode is not admitted by this worker")
	}
	if job.Claim.ProductID != "funding" {
		return Outcome{}, fmt.Errorf("invalid Funding product scope")
	}
	if job.Claim.WorkKind != "open" {
		return frozenOutcome(job, processor.admission)
	}
	facts := make([]planning.Fact, 0, len(job.Facts))
	finalCursor := job.Claim.FactsCursor
	for _, fact := range job.Facts {
		facts = append(facts, planning.Fact{Kind: fact.Kind, Payload: fact.Payload})
		if fact.Cursor != "" {
			finalCursor = fact.Cursor
		}
	}
	derived, err := planning.DeriveOpenWithAdmission(job.Claim.Payload, job.Claim.AccountFences, facts, processor.admission)
	if err != nil {
		return Outcome{}, fmt.Errorf("derive Funding intent from Platform facts: %w", err)
	}
	result, err := json.Marshal(map[string]any{
		"outcome": "completed", "commandIds": []string{}, "finalFactsCursor": finalCursor, "summaryCode": "OPEN_PLANNED",
	})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Intents: []Intent{{Kind: "funding.open", Payload: derived.IntentPayload}}, Result: result}, nil
}

func frozenOutcome(job Work, admission fundingpolicy.Admission) (Outcome, error) {
	kind := "funding." + job.Claim.WorkKind
	valid := false
	switch job.Claim.WorkKind {
	case "close":
		var value struct {
			LineageID string `json:"lineageId"`
			Reason    string `json:"reason"`
		}
		valid = decodeStrict(job.Claim.Payload, &value) == nil && value.LineageID == job.Claim.LineageID && value.Reason != ""
	case "compensate":
		var value struct {
			LineageID               string `json:"lineageId"`
			FailedCommandID         string `json:"failedCommandId"`
			SurvivingCommandID      string `json:"survivingCommandId"`
			MaxCompensationNotional string `json:"maxCompensationNotional"`
			Reason                  string `json:"reason"`
		}
		decodeErr := decodeStrict(job.Claim.Payload, &value)
		notional, notionalErr := decimal.NewFromString(value.MaxCompensationNotional)
		valid = decodeErr == nil && value.LineageID == job.Claim.LineageID && value.FailedCommandID != "" && value.SurvivingCommandID != "" && notionalErr == nil && admission.Allows(job.Claim.Mode, notional) && value.Reason != ""
	case "reconcile":
		var value struct {
			LineageID  string   `json:"lineageId"`
			CommandIDs []string `json:"commandIds"`
			Reason     string   `json:"reason"`
		}
		valid = decodeStrict(job.Claim.Payload, &value) == nil && value.LineageID == job.Claim.LineageID && len(value.CommandIDs) > 0 && value.Reason != ""
	default:
		return Outcome{}, fmt.Errorf("unsupported Funding work kind")
	}
	if !valid {
		return Outcome{}, fmt.Errorf("invalid frozen Funding work payload")
	}
	result, _ := json.Marshal(map[string]any{"outcome": "completed", "commandIds": []string{}, "finalFactsCursor": job.Claim.FactsCursor, "summaryCode": strings.ToUpper(job.Claim.WorkKind) + "_PLANNED"})
	return Outcome{Intents: []Intent{{Kind: kind, Payload: append(json.RawMessage(nil), job.Claim.Payload...)}}, Result: result}, nil
}

func decodeStrict(payload json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("invalid trailing JSON")
	}
	return nil
}
