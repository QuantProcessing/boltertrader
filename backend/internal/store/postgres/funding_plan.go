package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
)

type FundingPlanStore struct{ db *sql.DB }

func NewFundingPlanStore(db *sql.DB) *FundingPlanStore {
	if db == nil {
		return nil
	}
	return &FundingPlanStore{db: db}
}

func (store *FundingPlanStore) ReserveFundingPlan(ctx context.Context, plan work.FundingCommandPlan) (work.FundingCommandPlan, bool, error) {
	legs, err := json.Marshal(plan.Legs)
	if err != nil {
		return work.FundingCommandPlan{}, false, err
	}
	result, err := store.db.ExecContext(ctx, `INSERT INTO platform_funding_command_plans(plan_key,preview_hash,principal_id,mode,intent_kind,intent_payload,lineage_id,legs,expires_at,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (principal_id,mode,plan_key) DO NOTHING`, plan.PlanKey, plan.PreviewHash, plan.PrincipalID, plan.Mode, plan.IntentKind, plan.IntentPayload, plan.LineageID, legs, plan.ExpiresAt, plan.CreatedAt)
	if err != nil {
		return work.FundingCommandPlan{}, false, fmt.Errorf("reserve Funding command plan")
	}
	rows, _ := result.RowsAffected()
	if rows == 1 {
		return plan, false, nil
	}
	existing, err := store.GetFundingPlan(ctx, plan.PrincipalID, plan.Mode, plan.PlanKey)
	if err != nil {
		return work.FundingCommandPlan{}, true, err
	}
	if existing.PreviewHash != plan.PreviewHash || existing.IntentKind != plan.IntentKind || existing.LineageID != plan.LineageID || !work.EqualJSON(existing.IntentPayload, plan.IntentPayload) || !work.EqualJSON(mustMarshal(existing.Legs), mustMarshal(plan.Legs)) || !existing.ExpiresAt.Equal(plan.ExpiresAt) {
		return existing, true, work.ErrIdempotencyConflict
	}
	return existing, true, nil
}

func (store *FundingPlanStore) GetFundingPlan(ctx context.Context, principal string, mode orders.Mode, key string) (work.FundingCommandPlan, error) {
	var plan work.FundingCommandPlan
	var legs json.RawMessage
	err := store.db.QueryRowContext(ctx, `SELECT plan_key,preview_hash,principal_id,mode,intent_kind,intent_payload,lineage_id,legs,expires_at,created_at FROM platform_funding_command_plans WHERE principal_id=$1 AND mode=$2 AND plan_key=$3`, principal, mode, key).Scan(&plan.PlanKey, &plan.PreviewHash, &plan.PrincipalID, &plan.Mode, &plan.IntentKind, &plan.IntentPayload, &plan.LineageID, &legs, &plan.ExpiresAt, &plan.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return work.FundingCommandPlan{}, work.ErrWorkRevisionConflict
	}
	if err != nil {
		return work.FundingCommandPlan{}, fmt.Errorf("load Funding command plan")
	}
	if json.Unmarshal(legs, &plan.Legs) != nil {
		return work.FundingCommandPlan{}, fmt.Errorf("decode Funding command plan")
	}
	return plan, nil
}

func mustMarshal(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
