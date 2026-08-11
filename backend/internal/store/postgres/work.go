package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/shopspring/decimal"
)

type WorkStore struct{ db *sql.DB }

func NewWorkStore(db *sql.DB) *WorkStore {
	if db == nil {
		return nil
	}
	return &WorkStore{db: db}
}

func (store *WorkStore) Readiness(ctx context.Context) error {
	if store == nil || store.db == nil {
		return fmt.Errorf("work persistence is unavailable")
	}
	if err := store.db.PingContext(ctx); err != nil {
		return fmt.Errorf("work persistence is unavailable: %w", err)
	}
	return nil
}

func (store *WorkStore) EnqueueWork(ctx context.Context, req work.EnqueueRequest, now time.Time) (work.WorkItem, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return work.WorkItem{}, false, fmt.Errorf("begin work enqueue")
	}
	defer tx.Rollback()
	operationKind := "browser-enqueue:" + req.PrincipalID + ":" + req.Mode
	lockKey, _ := json.Marshal([]string{operationKind, req.IdempotencyKey})
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(lockKey)); err != nil {
		return work.WorkItem{}, false, fmt.Errorf("lock work enqueue idempotency")
	}
	hash := work.CanonicalRequestHash(req)
	var replay work.WorkItem
	if ok, err := loadWorkOperationValue(ctx, tx, operationKind, req.IdempotencyKey, hash, &replay); ok || err != nil {
		return replay, ok, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, req.FactsCursor); err != nil {
		return work.WorkItem{}, false, fmt.Errorf("lock work fact batch")
	}
	rows, err := tx.QueryContext(ctx, `SELECT fact_id,cursor,account_id,product_id,mode,kind,payload,effective_at FROM platform_work_facts WHERE cursor=$1 ORDER BY fact_id`, req.FactsCursor)
	if err != nil {
		return work.WorkItem{}, false, fmt.Errorf("load work fact batch")
	}
	existingBatch, _, err := scanWorkFacts(rows)
	if closeErr := rows.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return work.WorkItem{}, false, fmt.Errorf("load work fact batch: %w", err)
	}
	if len(existingBatch) > 0 && !work.EqualFactBatch(existingBatch, req.Facts) {
		return work.WorkItem{}, false, work.ErrIdempotencyConflict
	}
	if len(existingBatch) == 0 {
		for _, fact := range req.Facts {
			var factID string
			err = tx.QueryRowContext(ctx, `INSERT INTO platform_work_facts(fact_id,cursor,account_id,product_id,mode,kind,payload,effective_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (fact_id) DO UPDATE SET fact_id=EXCLUDED.fact_id
		WHERE platform_work_facts.cursor=EXCLUDED.cursor AND platform_work_facts.account_id=EXCLUDED.account_id AND platform_work_facts.product_id=EXCLUDED.product_id AND platform_work_facts.mode=EXCLUDED.mode AND platform_work_facts.kind=EXCLUDED.kind AND platform_work_facts.payload=EXCLUDED.payload AND platform_work_facts.effective_at=EXCLUDED.effective_at
		RETURNING fact_id`, fact.FactID, fact.Cursor, fact.AccountID, fact.ProductID, fact.Mode, fact.Kind, fact.Payload, fact.EffectiveAt).Scan(&factID)
			if errors.Is(err, sql.ErrNoRows) {
				return work.WorkItem{}, false, work.ErrIdempotencyConflict
			}
			if err != nil || factID != fact.FactID {
				return work.WorkItem{}, false, fmt.Errorf("persist browser work fact: %w", err)
			}
		}
	}
	idHash := work.CanonicalRequestHash(struct{ Principal, Mode, Key string }{req.PrincipalID, req.Mode, req.IdempotencyKey})
	item, err := scanBrowserWorkItem(tx.QueryRowContext(ctx, `INSERT INTO platform_work_items(work_item_id,principal_id,work_kind,lineage_id,work_revision,product_id,mode,status,facts_cursor,account_fences,payload,not_before,created_at,updated_at)
		VALUES ($1,$2,$3,$4,1,$5,$6,'open',$7,$8,$9,$10,$10,$10)
		RETURNING work_item_id,principal_id,work_kind,lineage_id,work_revision,product_id,mode,status,facts_cursor,account_fences,payload,result,error_code,retryable,attempt,max_attempts,not_before,lease_generation,next_attempt_at,created_at,updated_at`, "work_"+idHash[:24], req.PrincipalID, req.WorkKind, req.LineageID, req.ProductID, req.Mode, req.FactsCursor, defaultJSON(req.AccountFences), defaultJSON(req.Payload), now))
	if err != nil {
		return work.WorkItem{}, false, err
	}
	if err := saveWorkOperationValue(ctx, tx, operationKind, req.IdempotencyKey, hash, item, now); err != nil {
		return work.WorkItem{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return work.WorkItem{}, false, fmt.Errorf("commit work enqueue")
	}
	return item, false, nil
}

func (store *WorkStore) GetOwnedWork(ctx context.Context, principal, mode, id string) (work.WorkItem, error) {
	return scanBrowserWorkItem(store.db.QueryRowContext(ctx, `SELECT work_item_id,principal_id,work_kind,lineage_id,work_revision,product_id,mode,status,facts_cursor,account_fences,payload,result,error_code,retryable,attempt,max_attempts,not_before,lease_generation,next_attempt_at,created_at,updated_at FROM platform_work_items WHERE work_item_id=$1 AND principal_id=$2 AND mode=$3`, id, principal, mode))
}

func (store *WorkStore) ListOwnedWork(ctx context.Context, principal, mode, lineage string) ([]work.WorkItem, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT work_item_id,principal_id,work_kind,lineage_id,work_revision,product_id,mode,status,facts_cursor,account_fences,payload,result,error_code,retryable,attempt,max_attempts,not_before,lease_generation,next_attempt_at,created_at,updated_at FROM platform_work_items WHERE principal_id=$1 AND mode=$2 AND ($3='' OR lineage_id=$3) ORDER BY created_at,work_item_id`, principal, mode, lineage)
	if err != nil {
		return nil, fmt.Errorf("list browser work")
	}
	defer rows.Close()
	items := []work.WorkItem{}
	for rows.Next() {
		item, err := scanBrowserWorkItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (store *WorkStore) RegisterWorker(ctx context.Context, req work.RegistrationRequest, now time.Time, heartbeatTTL time.Duration) (work.Registration, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return work.Registration{}, fmt.Errorf("begin worker registration")
	}
	defer tx.Rollback()
	hash := work.CanonicalRequestHash(req)
	var replay work.Registration
	if ok, err := loadWorkOperationValue(ctx, tx, "register", req.IdempotencyKey, hash, &replay); ok || err != nil {
		return replay, err
	}
	current, err := scanWorkRegistration(tx.QueryRowContext(ctx, `SELECT worker_id,instance_id,product_id,mode,contract_version,generation,state,draining,registered_at,heartbeat_expires_at,last_heartbeat_at,last_applied_facts_cursor FROM platform_work_workers WHERE worker_id=$1 FOR UPDATE`, req.WorkerID))
	generation := 1
	if err == nil {
		if current.Mode != req.Mode {
			return work.Registration{}, work.ErrWorkerAlreadyActive
		}
		generation = current.Generation
		if current.InstanceID != req.InstanceID {
			var activeLeases int
			if countErr := tx.QueryRowContext(ctx, `SELECT count(*) FROM platform_work_leases WHERE worker_id=$1 AND worker_mode=$2 AND worker_generation=$3 AND status='active' AND expires_at>$4`, req.WorkerID, req.Mode, current.Generation, now).Scan(&activeLeases); countErr != nil {
				return work.Registration{}, fmt.Errorf("count active work leases")
			}
			replaceable := current.State == "stopped" || current.State == "stale" || !current.LastHeartbeat.Add(heartbeatTTL).After(now) || (current.Draining && activeLeases == 0)
			if !replaceable {
				return work.Registration{}, work.ErrWorkerAlreadyActive
			}
			generation++
		}
	} else if !errors.Is(err, work.ErrWorkerNotRegistered) {
		return work.Registration{}, err
	}
	registration, err := scanWorkRegistration(tx.QueryRowContext(ctx, `INSERT INTO platform_work_workers(worker_id,instance_id,product_id,mode,contract_version,generation,state,draining,registered_at,heartbeat_expires_at,last_heartbeat_at,last_applied_facts_cursor)
		VALUES ($1,$2,$3,$4,$5,$6,'starting',false,$7,$8,$7,'')
			ON CONFLICT (worker_id) DO UPDATE SET instance_id=EXCLUDED.instance_id,product_id=EXCLUDED.product_id,contract_version=EXCLUDED.contract_version,generation=EXCLUDED.generation,state='starting',draining=false,registered_at=CASE WHEN platform_work_workers.generation<>EXCLUDED.generation THEN EXCLUDED.registered_at ELSE platform_work_workers.registered_at END,heartbeat_expires_at=EXCLUDED.heartbeat_expires_at,last_heartbeat_at=EXCLUDED.last_heartbeat_at,last_applied_facts_cursor=''
			WHERE platform_work_workers.mode=EXCLUDED.mode
			RETURNING worker_id,instance_id,product_id,mode,contract_version,generation,state,draining,registered_at,heartbeat_expires_at,last_heartbeat_at,last_applied_facts_cursor`, req.WorkerID, req.InstanceID, req.ProductID, req.Mode, req.ContractVersion, generation, now, now.Add(heartbeatTTL)))
	if errors.Is(err, work.ErrWorkerNotRegistered) {
		return work.Registration{}, work.ErrWorkerAlreadyActive
	}
	if err != nil {
		return work.Registration{}, err
	}
	if err := saveWorkOperationValue(ctx, tx, "register", req.IdempotencyKey, hash, registration, now); err != nil {
		return work.Registration{}, err
	}
	if err := tx.Commit(); err != nil {
		return work.Registration{}, fmt.Errorf("commit worker registration")
	}
	return registration, nil
}

func (store *WorkStore) HeartbeatWorker(ctx context.Context, workerID string, req work.HeartbeatRequest, now time.Time) (work.Registration, error) {
	registration, err := scanWorkRegistration(store.db.QueryRowContext(ctx, workRegistrationUpdateProjection+`, heartbeat_expires_at=$5::timestamptz+(heartbeat_expires_at-last_heartbeat_at), last_heartbeat_at=$5::timestamptz, last_applied_facts_cursor=$6 WHERE worker_id=$1 AND generation=$2 AND instance_id=$3 RETURNING worker_id,instance_id,product_id,mode,contract_version,generation,state,draining,registered_at,heartbeat_expires_at,last_heartbeat_at,last_applied_facts_cursor`, workerID, req.WorkerGeneration, req.InstanceID, req.State, now, req.LastAppliedFactsCursor))
	return registration, staleWhenMissing(err)
}

func (store *WorkStore) DrainWorker(ctx context.Context, workerID string, req work.DrainRequest, now time.Time) (work.Registration, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return work.Registration{}, fmt.Errorf("begin worker drain")
	}
	defer tx.Rollback()
	hash := work.CanonicalRequestHash(req)
	var replay work.Registration
	if ok, err := loadWorkOperationValue(ctx, tx, "drain", req.IdempotencyKey, hash, &replay); ok || err != nil {
		return replay, err
	}
	registration, err := scanWorkRegistration(tx.QueryRowContext(ctx, workRegistrationUpdateProjection+`, draining=true, heartbeat_expires_at=$5::timestamptz+(heartbeat_expires_at-last_heartbeat_at), last_heartbeat_at=$5::timestamptz WHERE worker_id=$1 AND generation=$2 AND instance_id=$3 RETURNING worker_id,instance_id,product_id,mode,contract_version,generation,state,draining,registered_at,heartbeat_expires_at,last_heartbeat_at,last_applied_facts_cursor`, workerID, req.WorkerGeneration, req.InstanceID, "draining", now))
	if err = staleWhenMissing(err); err != nil {
		return work.Registration{}, err
	}
	if err := saveWorkOperationValue(ctx, tx, "drain", req.IdempotencyKey, hash, registration, now); err != nil {
		return work.Registration{}, err
	}
	if err := tx.Commit(); err != nil {
		return work.Registration{}, fmt.Errorf("commit worker drain")
	}
	return registration, nil
}

const workRegistrationUpdateProjection = `UPDATE platform_work_workers SET state=$4`

func (store *WorkStore) GetWorker(ctx context.Context, workerID string) (work.Registration, error) {
	return scanWorkRegistration(store.db.QueryRowContext(ctx, `SELECT worker_id,instance_id,product_id,mode,contract_version,generation,state,draining,registered_at,heartbeat_expires_at,last_heartbeat_at,last_applied_facts_cursor FROM platform_work_workers WHERE worker_id=$1`, workerID))
}

func (store *WorkStore) ListWorkerIDs(ctx context.Context) ([]string, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT worker_id FROM platform_work_workers ORDER BY worker_id`)
	if err != nil {
		return nil, fmt.Errorf("list work workers")
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan work worker")
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (store *WorkStore) CountActiveLeases(ctx context.Context, workerID string, generation int, now time.Time) (int, error) {
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM platform_work_leases l JOIN platform_work_workers w ON w.worker_id=l.worker_id AND w.mode=l.worker_mode WHERE l.worker_id=$1 AND l.worker_generation=$2 AND l.status='active' AND l.expires_at>$3`, workerID, generation, now).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active work leases")
	}
	return count, nil
}

func (store *WorkStore) ClaimWork(ctx context.Context, req work.ClaimRequest, now time.Time, leaseTTL time.Duration) (work.ClaimPage, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return work.ClaimPage{}, fmt.Errorf("begin work claim")
	}
	defer tx.Rollback()
	hash := work.CanonicalRequestHash(req)
	var replay work.ClaimPage
	if ok, err := loadWorkOperationValue(ctx, tx, "claim", req.IdempotencyKey, hash, &replay); ok || err != nil {
		if err == nil && replay.Lease != nil && !replay.Lease.ExpiresAt.After(now) {
			return work.ClaimPage{}, work.ErrLeaseExpired
		}
		return replay, err
	}
	worker, err := loadWorkerTx(ctx, tx, req.WorkerID, req.WorkerGeneration, req.InstanceID)
	if err != nil {
		return work.ClaimPage{}, err
	}
	if _, err := tx.ExecContext(ctx, `WITH expired AS (
		UPDATE platform_work_leases SET status='expired',updated_at=$1
		WHERE status='active' AND expires_at<=$1 RETURNING work_item_id
	)
	UPDATE platform_work_items i SET
		status=CASE WHEN i.attempt>=5 THEN 'manual_intervention' ELSE 'retry_wait' END,
		next_attempt_at=CASE WHEN i.attempt>=5 THEN NULL ELSE $1 + power(2, greatest(i.attempt-1,0)) * interval '1 second' END,
		updated_at=$1
	FROM expired e WHERE i.work_item_id=e.work_item_id AND i.status='leased'`, now); err != nil {
		return work.ClaimPage{}, fmt.Errorf("expire work leases")
	}
	limit := req.MaxItems
	if limit <= 0 || limit > 100 {
		limit = 1
	}
	rows, err := tx.QueryContext(ctx, `WITH claimable AS (
		SELECT i.work_item_id FROM platform_work_items i
		WHERE (i.status='open' OR (i.status='retry_wait' AND i.next_attempt_at<=$1))
		AND i.product_id=$6 AND i.mode=$7
		AND i.not_before<=$1
		ORDER BY i.updated_at, i.work_item_id FOR UPDATE SKIP LOCKED LIMIT $2
	), marked AS (
		UPDATE platform_work_items i SET status='leased',attempt=i.attempt+1,lease_generation=i.lease_generation+1,next_attempt_at=NULL,updated_at=$1 FROM claimable c WHERE i.work_item_id=c.work_item_id
			RETURNING i.work_item_id,i.work_kind,i.lineage_id,i.work_revision,i.facts_cursor,i.account_fences,i.payload,i.attempt,i.max_attempts,i.not_before,i.lease_generation,i.created_at,i.updated_at
	), inserted AS (
		INSERT INTO platform_work_leases(lease_id,lease_generation,work_item_id,work_revision,worker_id,worker_mode,worker_generation,facts_cursor,status,expires_at,created_at,updated_at)
		SELECT 'lease_' || lpad(nextval('platform_work_lease_seq')::text, 16, '0'), lease_generation, work_item_id, work_revision, $3, $7, $4, facts_cursor, 'active', $5, $1, $1 FROM marked
		RETURNING lease_id,lease_generation,work_item_id,work_revision,facts_cursor
	)
		SELECT inserted.work_item_id,marked.work_kind,marked.lineage_id,inserted.work_revision,inserted.lease_id,inserted.lease_generation,inserted.facts_cursor,marked.account_fences,marked.payload,marked.attempt,marked.max_attempts,marked.not_before,marked.created_at,marked.updated_at FROM inserted JOIN marked USING (work_item_id,work_revision,facts_cursor,lease_generation)`, now, limit, req.WorkerID, req.WorkerGeneration, now.Add(leaseTTL), worker.ProductID, worker.Mode)
	if err != nil {
		return work.ClaimPage{}, fmt.Errorf("claim work: %w", err)
	}
	var page work.ClaimPage
	for rows.Next() {
		var claim work.Claim
		var item work.WorkItem
		if err := rows.Scan(&claim.WorkItemID, &item.WorkKind, &item.LineageID, &claim.WorkRevision, &claim.LeaseID, &claim.LeaseGeneration, &claim.FactsCursor, &claim.AccountFences, &item.Payload, &claim.Attempt, &item.MaxAttempts, &item.NotBefore, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return work.ClaimPage{}, fmt.Errorf("scan work claim")
		}
		item.WorkItemID, item.WorkRevision, item.ProductID, item.Mode, item.Status = claim.WorkItemID, claim.WorkRevision, worker.ProductID, worker.Mode, "leased"
		item.FactsCursor, item.AccountFences, item.Attempt, item.LeaseGeneration = claim.FactsCursor, claim.AccountFences, claim.Attempt, claim.LeaseGeneration
		lease := work.Lease{LeaseID: claim.LeaseID, LeaseGeneration: claim.LeaseGeneration, WorkItemID: claim.WorkItemID, WorkRevision: claim.WorkRevision, WorkerID: req.WorkerID, WorkerGeneration: req.WorkerGeneration, FactsCursor: claim.FactsCursor, Status: "active", ExpiresAt: now.Add(leaseTTL), AcquiredAt: now, CreatedAt: now, UpdatedAt: now}
		page = work.ClaimPage{WorkItem: &item, Lease: &lease, NextCursor: claim.FactsCursor}
	}
	if err := rows.Err(); err != nil {
		return work.ClaimPage{}, err
	}
	if err := rows.Close(); err != nil {
		return work.ClaimPage{}, fmt.Errorf("close work claims")
	}
	if page.WorkItem == nil {
		page.NextCursor = req.AfterCursor
	} else {
		if err := saveWorkOperationValue(ctx, tx, "claim", req.IdempotencyKey, hash, page, now); err != nil {
			return work.ClaimPage{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return work.ClaimPage{}, fmt.Errorf("commit work claim")
	}
	return page, nil
}

func (store *WorkStore) FactPage(ctx context.Context, req work.FactRequest, snapshot bool, now time.Time) (work.FactPage, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return work.FactPage{}, fmt.Errorf("begin work fact page")
	}
	defer tx.Rollback()
	lease, err := loadActiveLease(ctx, tx, req.LeaseID, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.LeaseGeneration, now)
	if err != nil {
		return work.FactPage{}, err
	}
	var accountFences json.RawMessage
	var mode string
	if err := tx.QueryRowContext(ctx, `SELECT account_fences,mode FROM platform_work_items WHERE work_item_id=$1 AND work_revision=$2`, req.WorkItemID, req.WorkRevision).Scan(&accountFences, &mode); errors.Is(err, sql.ErrNoRows) {
		return work.FactPage{}, work.ErrWorkRevisionConflict
	} else if err != nil {
		return work.FactPage{}, fmt.Errorf("load work fact authority")
	}
	if err := validateCurrentAccountFences(ctx, tx, accountFences, mode); err != nil {
		return work.FactPage{}, err
	}
	if !snapshot && req.AfterCursor != "" {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM platform_work_facts f JOIN platform_work_items i ON i.work_item_id=$1 WHERE f.cursor=$2 AND f.product_id=i.product_id AND f.mode=i.mode AND (f.account_id='' OR (jsonb_typeof(i.account_fences)='array' AND EXISTS (SELECT 1 FROM jsonb_array_elements(i.account_fences) fence WHERE fence->>'accountId'=f.account_id))))`, req.WorkItemID, req.AfterCursor).Scan(&exists); err != nil {
			return work.FactPage{}, fmt.Errorf("validate work fact cursor")
		}
		if !exists && req.AfterCursor != lease.FactsCursor {
			return work.FactPage{}, work.ErrResnapshotRequired
		}
	}
	after := req.AfterCursor
	if snapshot {
		after = ""
	}
	rows, err := tx.QueryContext(ctx, `WITH eligible AS (
		SELECT f.fact_id,f.cursor,f.account_id,f.product_id,f.mode,f.kind,f.payload,f.effective_at
		FROM platform_work_facts f JOIN platform_work_items i ON i.work_item_id=$1
		WHERE f.product_id=i.product_id AND f.mode=i.mode AND (i.principal_id='platform:funding' OR f.cursor=i.facts_cursor)
		  AND (f.account_id='' OR (jsonb_typeof(i.account_fences)='array' AND EXISTS (SELECT 1 FROM jsonb_array_elements(i.account_fences) fence WHERE fence->>'accountId'=f.account_id)))
	), cursor_positions AS (
		SELECT cursor,min(effective_at) AS effective_at,min(fact_id) AS position_key,
		       row_number() OVER (ORDER BY min(effective_at),min(fact_id)) AS position
		FROM eligible GROUP BY cursor
	), after_position AS (
		SELECT position FROM cursor_positions WHERE cursor=$2
	), page_cursors AS (
		SELECT cursor,effective_at,position FROM cursor_positions
		WHERE $2='' OR position>COALESCE((SELECT position FROM after_position),0)
		ORDER BY position LIMIT $3
	)
	SELECT e.fact_id,e.cursor,e.account_id,e.product_id,e.mode,e.kind,e.payload,e.effective_at
	FROM eligible e JOIN page_cursors p ON p.cursor=e.cursor
	ORDER BY p.position,e.fact_id`, req.WorkItemID, after, req.Limit)
	if err != nil {
		return work.FactPage{}, fmt.Errorf("list scoped work facts")
	}
	items, cursor, err := scanWorkFacts(rows)
	if err != nil {
		_ = rows.Close()
		return work.FactPage{}, err
	}
	if err := rows.Close(); err != nil {
		return work.FactPage{}, fmt.Errorf("close scoped work facts")
	}
	if cursor == "" {
		cursor = lease.FactsCursor
	}
	if err := tx.Commit(); err != nil {
		return work.FactPage{}, fmt.Errorf("commit work fact page")
	}
	return work.FactPage{Facts: items, NextCursor: cursor, ResnapshotRequired: false, AsOf: now}, nil
}

func (store *WorkStore) RenewLease(ctx context.Context, leaseID string, req work.LeaseRequest, now time.Time, ttl time.Duration) (work.Lease, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return work.Lease{}, fmt.Errorf("begin work renewal")
	}
	defer tx.Rollback()
	hash := work.CanonicalRequestHash(req)
	var replay work.Lease
	if ok, err := loadWorkOperationValue(ctx, tx, "renew", req.IdempotencyKey, hash, &replay); ok || err != nil {
		return replay, err
	}
	var accountFences json.RawMessage
	var mode string
	if err := tx.QueryRowContext(ctx, `SELECT i.account_fences,i.mode FROM platform_work_leases l JOIN platform_work_items i ON i.work_item_id=l.work_item_id AND i.work_revision=l.work_revision AND i.mode=l.worker_mode JOIN platform_work_workers w ON w.worker_id=l.worker_id AND w.mode=l.worker_mode AND w.generation=l.worker_generation AND w.product_id=i.product_id WHERE l.lease_id=$1 AND l.worker_id=$2 AND l.worker_generation=$3 AND l.work_item_id=$4 AND l.work_revision=$5 AND l.lease_generation=$6 AND l.status='active' AND l.expires_at>$7 AND w.draining=false FOR UPDATE`, leaseID, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.LeaseGeneration, now).Scan(&accountFences, &mode); err != nil {
		return work.Lease{}, classifyLease(ctx, tx, leaseID, now, err)
	}
	if err := validateCurrentAccountFences(ctx, tx, accountFences, mode); err != nil {
		return work.Lease{}, err
	}
	lease, err := scanWorkLease(tx.QueryRowContext(ctx, `UPDATE platform_work_leases SET renewal_count=renewal_count+1,expires_at=$8,updated_at=$7 WHERE lease_id=$1 AND worker_id=$2 AND worker_generation=$3 AND work_item_id=$4 AND work_revision=$5 AND lease_generation=$6 AND status='active' AND expires_at>$7 RETURNING lease_id,lease_generation,work_item_id,work_revision,worker_id,worker_generation,facts_cursor,status,expires_at,created_at,updated_at,renewal_count`, leaseID, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.LeaseGeneration, now, now.Add(ttl)))
	if err != nil {
		return work.Lease{}, classifyLease(ctx, tx, leaseID, now, err)
	}
	if err := saveWorkOperationValue(ctx, tx, "renew", req.IdempotencyKey, hash, lease, now); err != nil {
		return work.Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return work.Lease{}, fmt.Errorf("commit work renewal")
	}
	return lease, nil
}

func (store *WorkStore) RecordIntent(ctx context.Context, leaseID string, req work.IntentRequest, now time.Time) (work.IntentRecord, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return work.IntentRecord{}, fmt.Errorf("begin work intent")
	}
	defer tx.Rollback()
	hash := work.IntentRequestHash(req)
	var lease work.Lease
	var accountFences json.RawMessage
	var mode string
	var workPayload json.RawMessage
	var lineageID, productID, principalID string
	err = tx.QueryRowContext(ctx, `SELECT l.lease_id,l.lease_generation,l.work_item_id,l.work_revision,l.worker_id,l.worker_generation,l.facts_cursor,l.status,l.expires_at,l.created_at,l.updated_at,l.renewal_count,i.account_fences,i.mode,i.payload,i.principal_id
		FROM platform_work_leases l
			JOIN platform_work_items i ON i.work_item_id=l.work_item_id AND i.work_revision=l.work_revision AND i.mode=l.worker_mode
			JOIN platform_work_workers w ON w.worker_id=l.worker_id AND w.mode=l.worker_mode AND w.generation=l.worker_generation AND w.product_id=i.product_id
		WHERE l.lease_id=$1 AND l.worker_id=$2 AND l.worker_generation=$3 AND l.work_item_id=$4 AND l.work_revision=$5 AND l.lease_generation=$6 AND l.status='active' AND l.expires_at>$7
		FOR UPDATE`, leaseID, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.LeaseGeneration, now).Scan(&lease.LeaseID, &lease.LeaseGeneration, &lease.WorkItemID, &lease.WorkRevision, &lease.WorkerID, &lease.WorkerGeneration, &lease.FactsCursor, &lease.Status, &lease.ExpiresAt, &lease.CreatedAt, &lease.UpdatedAt, &lease.RenewalCount, &accountFences, &mode, &workPayload, &principalID)
	if err != nil {
		return work.IntentRecord{}, classifyLease(ctx, tx, leaseID, now, err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT lineage_id,product_id FROM platform_work_items WHERE work_item_id=$1 AND work_revision=$2`, req.WorkItemID, req.WorkRevision).Scan(&lineageID, &productID); err != nil {
		return work.IntentRecord{}, fmt.Errorf("load work command ownership")
	}
	operationKind := "intent:" + principalID + ":" + mode
	var replay work.IntentRecord
	replayed, err := loadWorkOperationValue(ctx, tx, operationKind, req.IdempotencyKey, hash, &replay)
	if err != nil {
		return work.IntentRecord{}, err
	}
	if req.FactsCursor != "" && req.FactsCursor != lease.FactsCursor {
		return work.IntentRecord{}, work.ErrResnapshotRequired
	}
	if !work.EqualJSON(req.AccountFences, accountFences) {
		return work.IntentRecord{}, work.ErrAuthorizationStale
	}
	if err := validateCurrentAccountFences(ctx, tx, accountFences, mode); err != nil {
		return work.IntentRecord{}, err
	}
	if err := validateIntentPolicy(mode, workPayload, req); err != nil {
		return work.IntentRecord{}, err
	}
	if replayed {
		replay.Lease = lease
		replay.Command, err = scanCommand(tx.QueryRowContext(ctx, commandProjection+` WHERE command_id=$1`, replay.Command.CommandID))
		return replay, err
	}
	command := orders.Command{
		CommandID: "command_" + hash, PrincipalID: principalID, AccountID: work.IntentAccountID(req.Payload, accountFences),
		LineageID: lineageID, ProductID: productID, Mode: orders.Mode(mode), IdempotencyKey: req.IdempotencyKey,
		Method: "POST", Path: "/internal/v1/funding-work/leases/" + leaseID + "/command-intents", CommandKind: req.IntentKind,
		State: orders.StateAccepted, Payload: defaultJSON(req.Payload), PayloadHash: hash, Blockers: []orders.Blocker{}, CreatedAt: now, UpdatedAt: now,
	}
	blockers, _ := json.Marshal(command.Blockers)
	_, err = tx.ExecContext(ctx, `INSERT INTO platform_commands(command_id,principal_id,account_id,mode,product_id,lineage_id,reservation_id,idempotency_key,method,path,command_kind,state,payload,payload_hash,blockers,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,NULL,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)`, command.CommandID, command.PrincipalID, command.AccountID, command.Mode, command.ProductID, command.LineageID, command.IdempotencyKey, command.Method, command.Path, command.CommandKind, command.State, command.Payload, command.PayloadHash, blockers, now)
	if err != nil {
		return work.IntentRecord{}, fmt.Errorf("create work platform command")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO platform_work_intents(idempotency_key,command_id,lease_id,lease_generation,worker_id,worker_generation,work_item_id,work_revision,intent_kind,facts_cursor,account_fences,payload,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, req.IdempotencyKey, command.CommandID, leaseID, req.LeaseGeneration, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.IntentKind, req.FactsCursor, defaultJSON(req.AccountFences), defaultJSON(req.Payload), now)
	if err != nil {
		return work.IntentRecord{}, fmt.Errorf("record work intent")
	}
	factRows, err := tx.QueryContext(ctx, `SELECT fact_id,cursor,account_id,product_id,mode,kind,payload,effective_at FROM platform_work_facts WHERE product_id=$1 AND mode=$2 AND ($4='platform:funding' OR cursor=$5) AND (account_id='' OR (jsonb_typeof($3::jsonb)='array' AND EXISTS (SELECT 1 FROM jsonb_array_elements($3::jsonb) fence WHERE fence->>'accountId'=account_id))) ORDER BY effective_at,fact_id`, productID, mode, accountFences, principalID, lease.FactsCursor)
	if err != nil {
		return work.IntentRecord{}, fmt.Errorf("load authoritative work facts")
	}
	var facts []work.Fact
	for factRows.Next() {
		var fact work.Fact
		if err := factRows.Scan(&fact.FactID, &fact.Cursor, &fact.AccountID, &fact.ProductID, &fact.Mode, &fact.Kind, &fact.Payload, &fact.EffectiveAt); err != nil {
			factRows.Close()
			return work.IntentRecord{}, fmt.Errorf("scan authoritative work fact")
		}
		facts = append(facts, fact)
	}
	if err := factRows.Close(); err != nil {
		return work.IntentRecord{}, fmt.Errorf("close authoritative work facts")
	}
	record := work.IntentRecord{Lease: lease, Command: command, WorkPayload: workPayload, AccountFences: accountFences, Facts: facts}
	if err := saveWorkOperationValue(ctx, tx, operationKind, req.IdempotencyKey, hash, record, now); err != nil {
		return work.IntentRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return work.IntentRecord{}, fmt.Errorf("commit work intent")
	}
	return record, nil
}

func (store *WorkStore) TransitionIntentCommand(ctx context.Context, commandID string, state orders.State, now time.Time) (orders.Command, error) {
	if state != orders.StateSubmitted && state != orders.StateUncertain && state != orders.StateFailed {
		return orders.Command{}, orders.ErrInvalidTransition
	}
	request := orders.TransitionRequest{CommandID: commandID, State: state}
	if state == orders.StateUncertain {
		request.Reason = orders.ReconciliationAmbiguousHandoff
		request.NextAttemptAt = now
	}
	return NewCommandStore(store.db).Transition(ctx, request, now)
}

func (store *WorkStore) CompleteLease(ctx context.Context, leaseID string, req work.CompleteRequest, now time.Time) (work.Lease, error) {
	return store.terminalLease(ctx, "complete", req.IdempotencyKey, work.CanonicalRequestHash(req), leaseID, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.LeaseGeneration, req.FactsCursor, "completed", defaultJSON(req.Result), "", false, now)
}

func (store *WorkStore) FailLease(ctx context.Context, leaseID string, req work.FailRequest, now time.Time) (work.Lease, error) {
	return store.terminalLease(ctx, "fail", req.IdempotencyKey, work.CanonicalRequestHash(req), leaseID, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.LeaseGeneration, req.FactsCursor, "failed", defaultJSON(req.Details), req.ErrorCode, req.Retryable, now)
}

func (store *WorkStore) terminalLease(ctx context.Context, operationKind, idempotencyKey, requestHash, leaseID, workerID string, workerGeneration int, workItemID string, workRevision, leaseGeneration int, factsCursor, status string, result json.RawMessage, errorCode string, retryable bool, now time.Time) (work.Lease, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return work.Lease{}, fmt.Errorf("begin terminal work lease")
	}
	defer tx.Rollback()
	if lease, replay, err := loadWorkOperation(ctx, tx, operationKind, idempotencyKey, requestHash); replay || err != nil {
		return lease, err
	}
	lease, err := scanWorkLease(tx.QueryRowContext(ctx, `SELECT lease_id,lease_generation,work_item_id,work_revision,worker_id,worker_generation,facts_cursor,status,expires_at,created_at,updated_at,renewal_count FROM platform_work_leases WHERE lease_id=$1 AND worker_id=$2 AND worker_generation=$3 AND work_item_id=$4 AND work_revision=$5 AND lease_generation=$6 AND status='active' AND expires_at>$7 FOR UPDATE`, leaseID, workerID, workerGeneration, workItemID, workRevision, leaseGeneration, now))
	if err != nil {
		return work.Lease{}, classifyLease(ctx, tx, leaseID, now, err)
	}
	if factsCursor != "" && factsCursor != lease.FactsCursor {
		return work.Lease{}, work.ErrLeaseStale
	}
	if status == "completed" {
		if err := finalizeWorkAuthority(ctx, tx, workItemID, workRevision, now); err != nil {
			return work.Lease{}, err
		}
	}
	var committedStatus string
	err = tx.QueryRowContext(ctx, `UPDATE platform_work_items SET
		status=CASE WHEN $2='completed' THEN 'completed' WHEN $5 AND attempt<5 THEN 'retry_wait' ELSE 'manual_intervention' END,
		result=$3,error_code=$4,retryable=$5,
		next_attempt_at=CASE WHEN $2<>'completed' AND $5 AND attempt<5 THEN $6::timestamptz + power(2, greatest(attempt-1,0)) * interval '1 second' ELSE NULL END,
		updated_at=$6
		WHERE work_item_id=$1 AND work_revision=$7
		RETURNING status`, workItemID, status, result, errorCode, retryable, now, workRevision).Scan(&committedStatus)
	if err != nil {
		return work.Lease{}, fmt.Errorf("update terminal work item")
	}
	lease, err = scanWorkLease(tx.QueryRowContext(ctx, `UPDATE platform_work_leases SET status=$8,updated_at=$9 WHERE lease_id=$1 AND worker_id=$2 AND worker_generation=$3 AND work_item_id=$4 AND work_revision=$5 AND lease_generation=$6 AND status='active' AND expires_at>$7 RETURNING lease_id,lease_generation,work_item_id,work_revision,worker_id,worker_generation,facts_cursor,status,expires_at,created_at,updated_at,renewal_count`, leaseID, workerID, workerGeneration, workItemID, workRevision, leaseGeneration, now, committedStatus, now))
	if err != nil {
		return work.Lease{}, staleWhenMissing(err)
	}
	if err := saveWorkOperation(ctx, tx, operationKind, idempotencyKey, requestHash, lease, now); err != nil {
		return work.Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return work.Lease{}, fmt.Errorf("commit terminal work lease")
	}
	return lease, nil
}

func finalizeWorkAuthority(ctx context.Context, tx *sql.Tx, workItemID string, workRevision int, now time.Time) error {
	var lineageID, productID, mode, principalID string
	if err := tx.QueryRowContext(ctx, `SELECT lineage_id,product_id,mode,principal_id FROM platform_work_items WHERE work_item_id=$1 AND work_revision=$2 FOR UPDATE`, workItemID, workRevision).Scan(&lineageID, &productID, &mode, &principalID); err != nil {
		return fmt.Errorf("lock work completion authority: %w", err)
	}
	var intentCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM platform_work_intents WHERE work_item_id=$1 AND work_revision=$2`, workItemID, workRevision).Scan(&intentCount); err != nil {
		return fmt.Errorf("inspect work completion intents: %w", err)
	}
	if intentCount == 0 {
		return nil
	}
	var commandCount, incompatibleCommands, reconciliationCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER (WHERE state NOT IN ('filled','canceled')) FROM platform_commands WHERE principal_id=$1 AND mode=$2 AND product_id=$3 AND lineage_id=$4`, principalID, mode, productID, lineageID).Scan(&commandCount, &incompatibleCommands); err != nil {
		return fmt.Errorf("inspect work completion commands: %w", err)
	}
	if commandCount == 0 || incompatibleCommands != 0 {
		return fmt.Errorf("%w: owned commands are not terminal and successful", work.ErrDependencyUnavailable)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM platform_reconciliation_tasks r JOIN platform_commands c ON c.command_id=r.command_id WHERE c.principal_id=$1 AND c.mode=$2 AND c.product_id=$3 AND c.lineage_id=$4`, principalID, mode, productID, lineageID).Scan(&reconciliationCount); err != nil {
		return fmt.Errorf("inspect work completion reconciliation: %w", err)
	}
	if reconciliationCount != 0 {
		return fmt.Errorf("%w: owned commands still require reconciliation", work.ErrDependencyUnavailable)
	}
	var incompatibleExecutions int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM platform_funding_executions WHERE principal_id=$1 AND mode=$2 AND lineage_id=$3 AND (state NOT IN ('hedged','closed') OR EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(payload->'legs')='array' THEN payload->'legs' ELSE '[]'::jsonb END) leg WHERE leg->>'commandState' NOT IN ('filled','canceled')))`, principalID, mode, lineageID).Scan(&incompatibleExecutions); err != nil {
		return fmt.Errorf("inspect work completion executions: %w", err)
	}
	if incompatibleExecutions != 0 {
		return fmt.Errorf("%w: Funding execution is not terminal and compatible", work.ErrDependencyUnavailable)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE platform_reservations r SET status='released',updated_at=$5 FROM platform_commands c WHERE c.reservation_id=r.reservation_id AND c.principal_id=$1 AND c.mode=$2 AND c.product_id=$3 AND c.lineage_id=$4 AND r.status IN ('held','consumed')`, principalID, mode, productID, lineageID, now); err != nil {
		return fmt.Errorf("release work completion reservations: %w", err)
	}
	return nil
}

func (store *WorkStore) updateLease(ctx context.Context, leaseID, workerID string, workerGeneration int, workItemID string, workRevision, leaseGeneration int, now time.Time, setSQL string, arg any) (work.Lease, error) {
	lease, err := scanWorkLease(store.db.QueryRowContext(ctx, `UPDATE platform_work_leases SET `+setSQL+`, updated_at=$7 WHERE lease_id=$1 AND worker_id=$2 AND worker_generation=$3 AND work_item_id=$4 AND work_revision=$5 AND lease_generation=$6 AND status='active' AND expires_at>$7 RETURNING lease_id,lease_generation,work_item_id,work_revision,worker_id,worker_generation,facts_cursor,status,expires_at,created_at,updated_at,renewal_count`, leaseID, workerID, workerGeneration, workItemID, workRevision, leaseGeneration, now, arg))
	if err != nil {
		return lease, classifyLease(ctx, store.db, leaseID, now, err)
	}
	return lease, nil
}

func loadActiveLease(ctx context.Context, queryer workOperationQueryer, leaseID, workerID string, workerGeneration int, workItemID string, workRevision, leaseGeneration int, now time.Time) (work.Lease, error) {
	lease, err := scanWorkLease(queryer.QueryRowContext(ctx, `SELECT lease_id,lease_generation,work_item_id,work_revision,worker_id,worker_generation,facts_cursor,status,expires_at,created_at,updated_at,renewal_count FROM platform_work_leases WHERE lease_id=$1 AND worker_id=$2 AND worker_generation=$3 AND work_item_id=$4 AND work_revision=$5 AND lease_generation=$6 AND status='active' AND expires_at>$7 FOR UPDATE`, leaseID, workerID, workerGeneration, workItemID, workRevision, leaseGeneration, now))
	if err != nil {
		return lease, classifyLease(ctx, queryer, leaseID, now, err)
	}
	return lease, nil
}

func scanWorkRegistration(row scanner) (work.Registration, error) {
	var registration work.Registration
	err := row.Scan(&registration.WorkerID, &registration.InstanceID, &registration.ProductID, &registration.Mode, &registration.ContractVersion, &registration.Generation, &registration.State, &registration.Draining, &registration.RegisteredAt, &registration.HeartbeatExpiresAt, &registration.LastHeartbeat, &registration.LastAppliedFactsCursor)
	if errors.Is(err, sql.ErrNoRows) {
		return registration, work.ErrWorkerNotRegistered
	}
	if err != nil {
		return registration, fmt.Errorf("scan work worker: %w", err)
	}
	return registration, nil
}

func scanWorkLease(row scanner) (work.Lease, error) {
	var lease work.Lease
	err := row.Scan(&lease.LeaseID, &lease.LeaseGeneration, &lease.WorkItemID, &lease.WorkRevision, &lease.WorkerID, &lease.WorkerGeneration, &lease.FactsCursor, &lease.Status, &lease.ExpiresAt, &lease.CreatedAt, &lease.UpdatedAt, &lease.RenewalCount)
	lease.AcquiredAt = lease.CreatedAt
	if errors.Is(err, sql.ErrNoRows) {
		return lease, work.ErrLeaseStale
	}
	if err != nil {
		return lease, fmt.Errorf("scan work lease")
	}
	return lease, nil
}

func scanWorkFacts(rows *sql.Rows) ([]work.Fact, string, error) {
	items := []work.Fact{}
	cursor := ""
	for rows.Next() {
		var item work.Fact
		if err := rows.Scan(&item.FactID, &item.Cursor, &item.AccountID, &item.ProductID, &item.Mode, &item.Kind, &item.Payload, &item.EffectiveAt); err != nil {
			return nil, "", fmt.Errorf("scan work fact")
		}
		cursor = item.Cursor
		items = append(items, item)
	}
	return items, cursor, rows.Err()
}

func loadWorkerTx(ctx context.Context, tx *sql.Tx, workerID string, generation int, instanceID string) (work.Registration, error) {
	registration, err := scanWorkRegistration(tx.QueryRowContext(ctx, `SELECT worker_id,instance_id,product_id,mode,contract_version,generation,state,draining,registered_at,heartbeat_expires_at,last_heartbeat_at,last_applied_facts_cursor FROM platform_work_workers WHERE worker_id=$1 AND generation=$2 AND instance_id=$3`, workerID, generation, instanceID))
	return registration, staleWhenMissing(err)
}

func staleWhenMissing(err error) error {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, work.ErrWorkerNotRegistered) {
		return work.ErrWorkerStale
	}
	return err
}

type workOperationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type rowScanner interface{ Scan(...any) error }

func scanBrowserWorkItem(row rowScanner) (work.WorkItem, error) {
	var item work.WorkItem
	var result []byte
	var next sql.NullTime
	err := row.Scan(&item.WorkItemID, &item.PrincipalID, &item.WorkKind, &item.LineageID, &item.WorkRevision, &item.ProductID, &item.Mode, &item.Status, &item.FactsCursor, &item.AccountFences, &item.Payload, &result, &item.ErrorCode, &item.Retryable, &item.Attempt, &item.MaxAttempts, &item.NotBefore, &item.LeaseGeneration, &next, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, work.ErrWorkNotFound
	}
	if err != nil {
		return item, fmt.Errorf("scan browser work: %w", err)
	}
	item.Result = json.RawMessage(result)
	if next.Valid {
		item.NextAttemptAt = next.Time
	}
	return item, nil
}

func loadWorkOperation(ctx context.Context, queryer workOperationQueryer, kind, key, requestHash string) (work.Lease, bool, error) {
	var lease work.Lease
	replay, err := loadWorkOperationValue(ctx, queryer, kind, key, requestHash, &lease)
	return lease, replay, err
}

func loadWorkOperationValue(ctx context.Context, queryer workOperationQueryer, kind, key, requestHash string, target any) (bool, error) {
	var storedHash string
	var response json.RawMessage
	err := queryer.QueryRowContext(ctx, `SELECT request_hash,response FROM platform_work_operations WHERE operation_kind=$1 AND idempotency_key=$2`, kind, key).Scan(&storedHash, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load work operation")
	}
	if storedHash != requestHash {
		return true, work.ErrIdempotencyConflict
	}
	if err := json.Unmarshal(response, target); err != nil {
		return true, fmt.Errorf("decode work operation")
	}
	return true, nil
}

func saveWorkOperation(ctx context.Context, tx *sql.Tx, kind, key, requestHash string, lease work.Lease, now time.Time) error {
	return saveWorkOperationValue(ctx, tx, kind, key, requestHash, lease, now)
}

func saveWorkOperationValue(ctx context.Context, tx *sql.Tx, kind, key, requestHash string, value any, now time.Time) error {
	response, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode work operation")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_work_operations(operation_kind,idempotency_key,request_hash,response,created_at) VALUES ($1,$2,$3,$4,$5)`, kind, key, requestHash, response, now); err != nil {
		return fmt.Errorf("save work operation")
	}
	return nil
}

func classifyLease(ctx context.Context, queryer workOperationQueryer, leaseID string, now time.Time, cause error) error {
	if !errors.Is(cause, sql.ErrNoRows) && !errors.Is(cause, work.ErrLeaseStale) {
		return cause
	}
	var expiresAt time.Time
	var status string
	err := queryer.QueryRowContext(ctx, `SELECT expires_at,status FROM platform_work_leases WHERE lease_id=$1`, leaseID).Scan(&expiresAt, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return work.ErrWorkNotFound
	}
	if err != nil {
		return fmt.Errorf("classify work lease")
	}
	if status == "expired" || !expiresAt.After(now) {
		return work.ErrLeaseExpired
	}
	return work.ErrLeaseStale
}

func validateCurrentAccountFences(ctx context.Context, queryer workOperationQueryer, encoded json.RawMessage, mode string) error {
	fences, err := work.DecodeAccountFences(defaultJSON(encoded))
	if err != nil {
		return work.ErrAuthorizationStale
	}
	sort.Slice(fences, func(left, right int) bool { return fences[left].AccountID < fences[right].AccountID })
	for _, fence := range fences {
		var desired, runtime, credential int64
		var state, environment string
		err := queryer.QueryRowContext(ctx, `SELECT desired_state_revision,runtime_generation,credential_version,state,venue_environment FROM platform_accounts WHERE account_id=$1 FOR UPDATE`, fence.AccountID).Scan(&desired, &runtime, &credential, &state, &environment)
		modeMismatch := (mode == "live" && environment != "live") || (mode == "test" && environment == "live")
		if err != nil || state != "active" || modeMismatch || desired != fence.DesiredStateRevision || runtime != fence.RuntimeGeneration || credential != fence.CredentialVersion {
			return work.ErrAuthorizationStale
		}
	}
	return nil
}

func validateIntentPolicy(mode string, frozenPayload json.RawMessage, req work.IntentRequest) error {
	if mode == "live" {
		return work.ErrLiveWriteDisabled
	}
	if req.IntentKind == "funding.open" && !work.EqualJSON(defaultJSON(frozenPayload), defaultJSON(req.Payload)) {
		return work.ErrWorkRevisionConflict
	}
	var frozen, intent map[string]any
	if json.Unmarshal(defaultJSON(frozenPayload), &frozen) != nil || json.Unmarshal(defaultJSON(req.Payload), &intent) != nil {
		return work.ErrWorkRevisionConflict
	}
	for _, key := range []string{"previewId", "opportunityId", "longAccountId", "shortAccountId", "canonicalMarketId", "lineageId"} {
		if expected, exists := frozen[key]; exists && fmt.Sprint(intent[key]) != fmt.Sprint(expected) {
			return work.ErrWorkRevisionConflict
		}
	}
	for _, key := range []string{"notionalPerLeg", "maxCompensationNotional"} {
		value, exists := intent[key]
		if !exists {
			continue
		}
		notional, err := decimal.NewFromString(fmt.Sprint(value))
		if err != nil || !notional.IsPositive() || notional.GreaterThan(decimal.NewFromInt(100)) {
			return work.ErrWorkRevisionConflict
		}
		if authorized, exists := frozen[key]; exists {
			maximum, err := decimal.NewFromString(fmt.Sprint(authorized))
			if err != nil || notional.GreaterThan(maximum) {
				return work.ErrWorkRevisionConflict
			}
		}
	}
	return nil
}

func defaultJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}
