package executionmonitor

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
)

type Gateway interface {
	SubscribePrivateEvents(context.Context, boltertrader.PrivateEventSubscribeRequest) (PrivateStream, error)
}

type PrivateStream interface {
	Next(context.Context) (boltertrader.PrivateEvent, error)
	Close() error
}

type Reconciler interface {
	Recover(context.Context) error
}

type ResnapshotPublisher interface {
	PublishResnapshotRequired(context.Context, Resnapshot) error
}

type Resnapshot struct {
	Scope  []string `json:"scope"`
	Reason string   `json:"reason"`
}

type Readiness struct {
	Ready                        bool
	AuthoritativeResnapshotFresh bool
	Reason                       string
}

type Options struct {
	Gateway             Gateway
	Reconciler          Reconciler
	ResnapshotPublisher ResnapshotPublisher
	Accounts            []string
	RecoverMinInterval  time.Duration
	InitialBackoff      time.Duration
	MaxBackoff          time.Duration
}

type Monitor struct {
	gateway             Gateway
	reconciler          Reconciler
	resnapshotPublisher ResnapshotPublisher
	accounts            []string
	recoverMinInterval  time.Duration
	initialBackoff      time.Duration
	maxBackoff          time.Duration

	mu                           sync.Mutex
	recovering                   bool
	pendingRecovery              bool
	lastRecover                  time.Time
	recoverErr                   error
	lastSafeErr                  string
	recoverComplete              chan struct{}
	cursors                      map[string]string
	subscribed                   map[string]bool
	ready                        bool
	readinessReason              string
	recoveryBlocked              bool
	authoritativeResnapshotFresh bool
	recoveryWG                   sync.WaitGroup
}

func New(options Options) *Monitor {
	initialBackoff := options.InitialBackoff
	if initialBackoff <= 0 {
		initialBackoff = 250 * time.Millisecond
	}
	maxBackoff := options.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 5 * time.Second
	}
	if maxBackoff < initialBackoff {
		maxBackoff = initialBackoff
	}
	accounts := make([]string, 0, len(options.Accounts))
	seenAccounts := make(map[string]struct{}, len(options.Accounts))
	for _, account := range options.Accounts {
		if account = strings.TrimSpace(account); account != "" {
			if _, exists := seenAccounts[account]; exists {
				continue
			}
			seenAccounts[account] = struct{}{}
			accounts = append(accounts, account)
		}
	}
	return &Monitor{
		gateway: options.Gateway, reconciler: options.Reconciler, resnapshotPublisher: options.ResnapshotPublisher,
		accounts:           accounts,
		recoverMinInterval: options.RecoverMinInterval, initialBackoff: initialBackoff, maxBackoff: maxBackoff,
		cursors: make(map[string]string), subscribed: make(map[string]bool), readinessReason: "not-subscribed",
	}
}

func (monitor *Monitor) Run(ctx context.Context) error {
	if monitor == nil || monitor.gateway == nil || monitor.reconciler == nil || len(monitor.accounts) == 0 {
		return errors.New("execution monitor dependencies are required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer monitor.recoveryWG.Wait()
	defer cancel()
	errCh := make(chan error, len(monitor.accounts))
	var wg sync.WaitGroup
	for _, account := range monitor.accounts {
		account := account
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := monitor.watchAccount(runCtx, account); err != nil && !errors.Is(err, context.Canceled) {
				errCh <- err
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		cancel()
		<-done
		return ctx.Err()
	case err := <-errCh:
		cancel()
		<-done
		return err
	case <-done:
		return nil
	}
}

func (monitor *Monitor) LastSafeError() string {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	return monitor.lastSafeErr
}

func (monitor *Monitor) Readiness() Readiness {
	if monitor == nil {
		return Readiness{Reason: "not-configured"}
	}
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	return Readiness{
		Ready: monitor.ready, AuthoritativeResnapshotFresh: monitor.authoritativeResnapshotFresh,
		Reason: monitor.readinessReason,
	}
}

func (monitor *Monitor) watchAccount(ctx context.Context, accountID string) error {
	backoff := monitor.initialBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		stream, err := monitor.gateway.SubscribePrivateEvents(ctx, boltertrader.PrivateEventSubscribeRequest{
			AccountID: accountID,
			Cursor:    monitor.cursor(accountID),
			Kinds:     []string{"order", "fill", "position", "account_health"},
		})
		if err != nil {
			monitor.markUnready(accountID, "gateway-private-stream")
			if err := sleep(ctx, backoff); err != nil {
				return err
			}
			backoff = nextBackoff(backoff, monitor.maxBackoff)
			continue
		}
		monitor.markSubscribed(accountID)
		err = monitor.consume(ctx, accountID, stream)
		_ = stream.Close()
		if err := ctx.Err(); err != nil {
			return err
		}
		if boltertrader.IsResnapshotRequired(err) {
			monitor.clearCursor(accountID)
			monitor.markUnready(accountID, "gateway-private-resnapshot")
			if recoverErr := monitor.recoverNow(ctx); recoverErr != nil {
				monitor.markFailure("gateway-private-resnapshot")
			} else if monitor.resnapshotPublisher != nil {
				reason := resnapshotReason(err)
				if publishErr := monitor.resnapshotPublisher.PublishResnapshotRequired(ctx, Resnapshot{
					Scope: browserResourceScopes(), Reason: reason,
				}); publishErr != nil {
					monitor.markFailure("gateway-private-resnapshot")
				} else {
					monitor.markRecoverySucceeded()
				}
			} else {
				monitor.markFailure("gateway-private-resnapshot")
			}
			backoff = monitor.initialBackoff
			continue
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			monitor.markUnready(accountID, "gateway-private-stream")
		}
		if err := sleep(ctx, backoff); err != nil {
			return err
		}
		backoff = nextBackoff(backoff, monitor.maxBackoff)
	}
}

func (monitor *Monitor) consume(ctx context.Context, accountID string, stream PrivateStream) error {
	for {
		event, err := stream.Next(ctx)
		if err != nil {
			return err
		}
		monitor.updateCursor(accountID, event)
		if shouldRecover(event.Kind) {
			monitor.triggerRecover(ctx)
		}
	}
}

func shouldRecover(kind string) bool {
	switch kind {
	case "order", "fill", "position", "account_health":
		return true
	default:
		return false
	}
}

func (monitor *Monitor) triggerRecover(ctx context.Context) {
	monitor.mu.Lock()
	if monitor.recoverMinInterval > 0 && !monitor.lastRecover.IsZero() && time.Since(monitor.lastRecover) < monitor.recoverMinInterval {
		monitor.mu.Unlock()
		return
	}
	if monitor.recovering {
		monitor.pendingRecovery = true
		monitor.mu.Unlock()
		return
	}
	monitor.recovering = true
	monitor.pendingRecovery = false
	monitor.recoverComplete = make(chan struct{})
	done := monitor.recoverComplete
	monitor.refreshReadinessLocked()
	monitor.mu.Unlock()

	monitor.recoveryWG.Add(1)
	go func() {
		defer monitor.recoveryWG.Done()
		monitor.runRecoveryLoop(ctx, done)
	}()
}

func (monitor *Monitor) recoverNow(ctx context.Context) error {
	monitor.mu.Lock()
	if monitor.recovering {
		done := monitor.recoverComplete
		monitor.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			monitor.mu.Lock()
			err := monitor.recoverErr
			monitor.mu.Unlock()
			return err
		}
	}
	monitor.recovering = true
	monitor.pendingRecovery = false
	monitor.recoverComplete = make(chan struct{})
	done := monitor.recoverComplete
	monitor.refreshReadinessLocked()
	monitor.mu.Unlock()

	return monitor.runRecoveryLoop(ctx, done)
}

func (monitor *Monitor) runRecoveryLoop(ctx context.Context, done chan struct{}) error {
	var err error
	for {
		err = monitor.reconciler.Recover(ctx)
		monitor.mu.Lock()
		monitor.lastRecover = time.Now().UTC()
		monitor.recoverErr = err
		if err == nil {
			monitor.recoveryBlocked = false
		} else {
			monitor.recoveryBlocked = true
			monitor.lastSafeErr = "gateway-private-recovery"
			monitor.readinessReason = "gateway-private-recovery"
		}
		if monitor.pendingRecovery && ctx.Err() == nil {
			monitor.pendingRecovery = false
			monitor.mu.Unlock()
			continue
		}
		monitor.recovering = false
		close(done)
		monitor.refreshReadinessLocked()
		monitor.mu.Unlock()
		return err
	}
}

func (monitor *Monitor) markSubscribed(accountID string) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.subscribed[accountID] = true
	monitor.authoritativeResnapshotFresh = false
	monitor.refreshReadinessLocked()
}

func (monitor *Monitor) markUnready(accountID, reason string) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	delete(monitor.subscribed, accountID)
	monitor.authoritativeResnapshotFresh = false
	monitor.ready = false
	monitor.readinessReason = reason
	monitor.lastSafeErr = reason
}

func (monitor *Monitor) markFailure(reason string) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.ready = false
	monitor.readinessReason = reason
	monitor.lastSafeErr = reason
	monitor.recoveryBlocked = true
	monitor.authoritativeResnapshotFresh = false
}

func (monitor *Monitor) markRecoverySucceeded() {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.recoveryBlocked = false
	monitor.authoritativeResnapshotFresh = true
	monitor.refreshReadinessLocked()
}

func (monitor *Monitor) refreshReadinessLocked() {
	if monitor.recovering {
		monitor.ready = false
		monitor.readinessReason = "reconciling"
		return
	}
	if monitor.recoveryBlocked {
		monitor.ready = false
		if monitor.readinessReason == "" {
			monitor.readinessReason = "gateway-private-resnapshot"
		}
		return
	}
	if len(monitor.subscribed) < len(monitor.accounts) {
		monitor.ready = false
		if monitor.readinessReason == "" {
			monitor.readinessReason = "not-subscribed"
		}
		return
	}
	monitor.ready = true
	monitor.readinessReason = ""
}

func sleep(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func nextBackoff(current, max time.Duration) time.Duration {
	next := current * 2
	if next <= 0 || next > max {
		return max
	}
	return next
}

func (monitor *Monitor) cursor(accountID string) string {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	return monitor.cursors[accountID]
}

func (monitor *Monitor) updateCursor(accountID string, event boltertrader.PrivateEvent) {
	if strings.TrimSpace(event.BootID) == "" || event.Sequence == 0 {
		return
	}
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.cursors[accountID] = event.BootID + ":" + strconv.FormatUint(event.Sequence, 10)
}

func (monitor *Monitor) clearCursor(accountID string) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	delete(monitor.cursors, accountID)
}

func browserResourceScopes() []string {
	return []string{"executions", "positions", "activities"}
}

func resnapshotReason(err error) string {
	var resnapshot *boltertrader.ResnapshotRequiredError
	if errors.As(err, &resnapshot) {
		return safeReason(resnapshot.Reason)
	}
	return "resync-required"
}

var safeReasonPattern = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func safeReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	parts := strings.Fields(reason)
	filtered := parts[:0]
	for _, part := range parts {
		if sensitiveReasonPart(part) {
			continue
		}
		filtered = append(filtered, part)
	}
	if len(filtered) != 0 {
		reason = strings.Join(filtered, " ")
	}
	reason = safeReasonPattern.ReplaceAllString(reason, "-")
	reason = strings.Trim(reason, "-_.")
	if reason == "" {
		return "resync-required"
	}
	if len(reason) > 64 {
		reason = reason[:64]
		reason = strings.Trim(reason, "-_.")
	}
	return reason
}

func sensitiveReasonPart(part string) bool {
	part = strings.ToLower(part)
	return strings.Contains(part, "token") ||
		strings.Contains(part, "secret") ||
		strings.Contains(part, "credential") ||
		strings.Contains(part, "password") ||
		strings.Contains(part, "api-key") ||
		strings.HasPrefix(part, "sk-")
}
