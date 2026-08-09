package accounts

import (
	"context"
	"errors"
	"time"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

type Reconciler struct {
	store   Store
	secrets SecretWriter
	gateway GatewayDesiredClient
	now     func() time.Time
	hooks   LifecycleHooks
}

func NewReconciler(store Store, secrets SecretWriter, gateway GatewayDesiredClient, now func() time.Time) (*Reconciler, error) {
	if store == nil || secrets == nil || gateway == nil {
		return nil, errors.New("credential reconciler dependencies are required")
	}
	if now == nil {
		now = time.Now
	}
	return &Reconciler{store: store, secrets: secrets, gateway: gateway, now: now}, nil
}

func (reconciler *Reconciler) SetHooks(hooks LifecycleHooks) { reconciler.hooks = hooks }

func (reconciler *Reconciler) RunOnce(ctx context.Context, limit int) error {
	operations, err := reconciler.store.ListRecoverableCredentialOperations(ctx, limit)
	if err != nil {
		return errors.New("credential recovery scan failed")
	}
	for _, operation := range operations {
		if err := reconciler.reconcile(ctx, operation); err != nil {
			return err
		}
	}
	return nil
}

func (reconciler *Reconciler) reconcile(ctx context.Context, operation CredentialOperation) error {
	account, err := reconciler.store.Get(ctx, operation.AccountID)
	if err != nil {
		return errors.New("credential recovery account missing")
	}
	if operation.State == OperationAwaitingMaterial {
		lookup, ok := reconciler.secrets.(SecretOperationLookup)
		if !ok {
			return reconciler.recordStableFailure(ctx, account, operation, "SECRET_LOOKUP_UNAVAILABLE")
		}
		reference, err := lookup.LookupOperation(ctx, operation.ID)
		if errors.Is(err, secretstore.ErrSecretNotFound) {
			return nil
		}
		if err != nil {
			return reconciler.recordStableFailure(ctx, account, operation, "SECRET_LOOKUP_FAILED")
		}
		account, operation, err = reconciler.store.BindCredentialReference(ctx, operation.ID, reference)
		if err != nil {
			return errors.New("credential recovery bind failed")
		}
	}
	if operation.State == OperationStored {
		operation.State = OperationApplying
		operation.UpdatedAt = reconciler.now().UTC()
		account, operation, err = reconciler.store.CompareAndSwapAccountAndOperation(ctx, TransitionRequest{OperationID: operation.ID, ExpectedOperationState: OperationStored, ExpectedDesiredStateRevision: account.DesiredStateRevision, Account: account, Operation: operation})
		if err != nil {
			return errors.New("credential recovery transition failed")
		}
	}
	if operation.State == OperationApplying {
		truth, err := reconciler.gateway.ApplyDesired(ctx, DesiredApply{OperationID: operation.ID, Account: account})
		if err != nil {
			return reconciler.recordStableFailure(ctx, account, operation, "GATEWAY_APPLY_FAILED")
		}
		if !truth.Active || truth.AccountID != account.ID || truth.DesiredStateRevision != account.DesiredStateRevision || truth.RuntimeGeneration != account.RuntimeGeneration || truth.CredentialVersion != account.CredentialVersion {
			return reconciler.recordStableFailure(ctx, account, operation, "GATEWAY_FENCE_MISMATCH")
		}
		account.State = StateActive
		account.UpdatedAt = reconciler.now().UTC()
		operation.State, operation.StableErrorCode, operation.UpdatedAt = OperationActive, "", account.UpdatedAt
		if _, _, err := reconciler.store.CompareAndSwapAccountAndOperation(ctx, TransitionRequest{OperationID: operation.ID, ExpectedOperationState: OperationApplying, ExpectedDesiredStateRevision: account.DesiredStateRevision, Account: account, Operation: operation}); err != nil {
			return errors.New("credential recovery promotion failed")
		}
	}
	if operation.State == OperationCleanupPending {
		retirement, err := reconciler.gateway.RetireAccount(ctx, RetireRequest{OperationID: operation.ID, AccountID: account.ID})
		if err != nil || !retirement.Tombstoned || retirement.Blockers.Any() {
			return reconciler.recordStableFailure(ctx, account, operation, "GATEWAY_RETIREMENT_PENDING")
		}
		if err := callHook(reconciler.hooks.AfterGatewayTombstone); err != nil {
			return err
		}
		cleaner, ok := reconciler.secrets.(SecretCleaner)
		if !ok {
			return reconciler.recordStableFailure(ctx, account, operation, "SECRET_DELETE_UNAVAILABLE")
		}
		if account.PreviousSecretRef != "" {
			_ = cleaner.Delete(ctx, account.PreviousSecretRef)
		}
		if account.SecretRef != "" {
			_ = cleaner.Delete(ctx, account.SecretRef)
		}
		if err := callHook(reconciler.hooks.AfterProviderDelete); err != nil {
			return err
		}
		account.SecretRef, account.PreviousSecretRef, account.State = "", "", StateRemoved
		account.UpdatedAt = reconciler.now().UTC()
		operation.State, operation.StableErrorCode, operation.UpdatedAt = OperationActive, "", account.UpdatedAt
		if _, _, err := reconciler.store.CompareAndSwapAccountAndOperation(ctx, TransitionRequest{OperationID: operation.ID, ExpectedOperationState: OperationCleanupPending, ExpectedDesiredStateRevision: account.DesiredStateRevision, Account: account, Operation: operation}); err != nil {
			return errors.New("credential cleanup commit failed")
		}
	}
	return nil
}

func (reconciler *Reconciler) recordStableFailure(ctx context.Context, account Account, operation CredentialOperation, code string) error {
	operation.StableErrorCode = code
	operation.UpdatedAt = reconciler.now().UTC()
	_, _, _ = reconciler.store.CompareAndSwapAccountAndOperation(ctx, TransitionRequest{OperationID: operation.ID, ExpectedOperationState: operation.State, ExpectedDesiredStateRevision: account.DesiredStateRevision, Account: account, Operation: operation})
	return errors.New(code)
}
