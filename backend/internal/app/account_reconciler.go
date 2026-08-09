package app

import (
	"context"
	"errors"
	"sync"
	"time"
)

type accountOperationReconciler interface {
	RunOnce(context.Context, int) error
}

type accountReconciler struct {
	runner    accountOperationReconciler
	authority runtimeRouteProbe
	interval  time.Duration
	limit     int

	mu       sync.RWMutex
	observed bool
	ready    bool
	reason   string
}

func newAccountReconciler(runner accountOperationReconciler, authority runtimeRouteProbe, interval time.Duration, limit int) (*accountReconciler, error) {
	if runner == nil || authority == nil || interval <= 0 || limit < 1 || limit > 1024 {
		return nil, errors.New("account reconciler dependencies are invalid")
	}
	return &accountReconciler{runner: runner, authority: authority, interval: interval, limit: limit, reason: "account_reconciler_unobserved"}, nil
}

func (reconciler *accountReconciler) Run(ctx context.Context) {
	_ = reconciler.RunOnce(ctx)
	ticker := time.NewTicker(reconciler.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = reconciler.RunOnce(ctx)
		}
	}
}

func (reconciler *accountReconciler) RunOnce(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := reconciler.authority.CheckRuntimeRoute(probeCtx); err != nil {
		reconciler.record(false, "gateway_account_authority_unavailable")
		return errors.New("gateway account authority is unavailable")
	}
	if err := reconciler.runner.RunOnce(probeCtx, reconciler.limit); err != nil {
		reconciler.record(false, "account_reconciliation_failed")
		return errors.New("account reconciliation failed")
	}
	reconciler.record(true, "ready")
	return nil
}

func (reconciler *accountReconciler) Readiness() error {
	reconciler.mu.RLock()
	defer reconciler.mu.RUnlock()
	if !reconciler.observed || !reconciler.ready {
		return errors.New(reconciler.reason)
	}
	return nil
}

func (reconciler *accountReconciler) record(ready bool, reason string) {
	reconciler.mu.Lock()
	reconciler.observed = true
	reconciler.ready = ready
	reconciler.reason = reason
	reconciler.mu.Unlock()
}
