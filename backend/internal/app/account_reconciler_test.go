package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type recordingAccountOperationReconciler struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (runner *recordingAccountOperationReconciler) RunOnce(context.Context, int) error {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.calls++
	return runner.err
}

func (runner *recordingAccountOperationReconciler) callCount() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.calls
}

type recordingAccountAuthorityProbe struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (probe *recordingAccountAuthorityProbe) CheckRuntimeRoute(context.Context) error {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	probe.calls++
	return probe.err
}

func TestAccountReconcilerReadinessTracksAuthorityAndDurableRecovery(t *testing.T) {
	runner := &recordingAccountOperationReconciler{}
	probe := &recordingAccountAuthorityProbe{err: errors.New("authority unavailable")}
	reconciler, err := newAccountReconciler(runner, probe, time.Second, 32)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Readiness(); err == nil {
		t.Fatal("unobserved reconciler reported ready")
	}
	if err := reconciler.RunOnce(context.Background()); err == nil || runner.callCount() != 0 {
		t.Fatalf("authority failure err=%v runner calls=%d", err, runner.callCount())
	}
	probe.mu.Lock()
	probe.err = nil
	probe.mu.Unlock()
	runner.mu.Lock()
	runner.err = errors.New("recovery scan failed")
	runner.mu.Unlock()
	if err := reconciler.RunOnce(context.Background()); err == nil {
		t.Fatal("durable recovery failure reported ready")
	}
	runner.mu.Lock()
	runner.err = nil
	runner.mu.Unlock()
	if err := reconciler.RunOnce(context.Background()); err != nil || reconciler.Readiness() != nil {
		t.Fatalf("reconciler did not recover: run=%v readiness=%v", err, reconciler.Readiness())
	}
}

func TestAccountReconcilerRunsImmediatelyAndAtBoundedIntervals(t *testing.T) {
	runner := &recordingAccountOperationReconciler{}
	probe := &recordingAccountAuthorityProbe{}
	reconciler, err := newAccountReconciler(runner, probe, 5*time.Millisecond, 8)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reconciler.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for runner.callCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("account reconciler did not stop")
	}
	if calls := runner.callCount(); calls < 2 || calls > 100 {
		t.Fatalf("reconcile calls=%d", calls)
	}
}
