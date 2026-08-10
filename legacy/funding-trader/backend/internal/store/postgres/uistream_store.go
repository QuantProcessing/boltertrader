package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
)

type UIStreamEventStore struct {
	readModel *ReadModelStore
	bootID    string
	scope     readmodel.Scope
}

func NewUIStreamEventStore(
	readModelStore *ReadModelStore,
	bootID string,
	scope readmodel.Scope,
) (*UIStreamEventStore, error) {
	if readModelStore == nil ||
		strings.TrimSpace(bootID) == "" ||
		validateReadModelScope(scope) != nil {
		return nil, fmt.Errorf("uistream store identity is required")
	}
	return &UIStreamEventStore{readModel: readModelStore, bootID: bootID, scope: scope}, nil
}

func (store *UIStreamEventStore) Latest(ctx context.Context, scope string) (uint64, error) {
	if scope != "all" {
		return 0, fmt.Errorf("unsupported stream scope")
	}
	var id int64
	err := store.readModel.pool.QueryRow(ctx, `
		select coalesce(max(id), 0)
		from read_model_events
		where boot_id=$1
		  and contract_version=$2
		  and mode=$3
		  and (principal_id is null or principal_id=$4)
	`, store.bootID, readmodel.ContractVersion, store.scope.Mode, store.scope.PrincipalID).Scan(&id)
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

func (store *UIStreamEventStore) Replay(
	ctx context.Context,
	scope string,
	after uint64,
	kinds []string,
	limit int,
) ([]uistream.StoredEvent, error) {
	if scope != "all" || limit < 1 || limit > maxReplayLimit {
		return nil, fmt.Errorf("invalid stream replay request")
	}
	if after > 0 {
		var exists bool
		if err := store.readModel.pool.QueryRow(ctx, `
			select exists(
				select 1 from read_model_events
				where id=$1
				  and boot_id=$2
				  and contract_version=$3
				  and mode=$4
				  and (principal_id is null or principal_id=$5)
			)
		`, int64(after), store.bootID, readmodel.ContractVersion, store.scope.Mode, store.scope.PrincipalID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, uistream.ErrResyncRequired
		}
	}
	args := []any{store.bootID, readmodel.ContractVersion, store.scope.Mode, store.scope.PrincipalID, int64(after)}
	where := `
		boot_id=$1
		and contract_version=$2
		and mode=$3
		and (principal_id is null or principal_id=$4)
		and id>$5
	`
	normalizedKinds := stringSet(kinds)
	if len(normalizedKinds) > 0 {
		args = append(args, normalizedKinds)
		where += fmt.Sprintf(" and kind=any($%d)", len(args))
	}
	args = append(args, limit+1)
	query := fmt.Sprintf(`
		select id, contract_version, kind, payload, occurred_at
		from read_model_events
		where %s
		order by id
		limit $%d
	`, where, len(args))
	rows, err := store.readModel.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]uistream.StoredEvent, 0, limit+1)
	for rows.Next() {
		var id int64
		var event uistream.StoredEvent
		if err := rows.Scan(&id, &event.ContractVersion, &event.Kind, &event.Data, &event.OccurredAt); err != nil {
			return nil, err
		}
		event.Sequence = uint64(id)
		event.Scope = "all"
		event.PrincipalID = store.scope.PrincipalID
		event.Mode = store.scope.Mode
		event.OccurredAt = event.OccurredAt.UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(events) > limit {
		return nil, uistream.ErrResyncRequired
	}
	return events, nil
}

func (store *UIStreamEventStore) Append(
	ctx context.Context,
	input uistream.EventInput,
) (uistream.StoredEvent, error) {
	if input.Scope != "all" || strings.TrimSpace(input.Kind) == "" || !json.Valid(input.Data) {
		return uistream.StoredEvent{}, fmt.Errorf("invalid stream event")
	}
	if input.PrincipalID != store.scope.PrincipalID || input.Mode != store.scope.Mode {
		return uistream.StoredEvent{}, fmt.Errorf("stream event scope conflicts with store identity")
	}
	if input.ContractVersion == "" {
		input.ContractVersion = readmodel.ContractVersion
	}
	if input.ContractVersion != readmodel.ContractVersion {
		return uistream.StoredEvent{}, fmt.Errorf("unsupported stream contract version")
	}
	now := time.Now().UTC()
	event, err := store.readModel.AppendEvent(ctx, readmodel.Event{
		BootID: store.bootID, PrincipalID: store.scope.PrincipalID,
		Mode: store.scope.Mode, ContractVersion: input.ContractVersion,
		Kind:       readmodel.EventKind(input.Kind),
		Scope:      affectedScopes(input.Kind),
		Payload:    input.Data,
		OccurredAt: now,
	})
	if err != nil {
		return uistream.StoredEvent{}, err
	}
	if event.ID < 1 {
		return uistream.StoredEvent{}, errors.New("persisted stream event has invalid sequence")
	}
	return uistream.StoredEvent{
		Sequence: uint64(event.ID), Scope: "all",
		PrincipalID: store.scope.PrincipalID, Mode: store.scope.Mode,
		ContractVersion: event.ContractVersion, Kind: input.Kind,
		Data: event.Payload, OccurredAt: event.OccurredAt,
	}, nil
}

func affectedScopes(kind string) []string {
	switch readmodel.EventKind(kind) {
	case readmodel.OpportunityUpsert:
		return []string{"opportunities"}
	case readmodel.AlertUpsert:
		return []string{"alerts"}
	case readmodel.ExecutionUpsert:
		return []string{"executions"}
	case readmodel.PositionUpsert:
		return []string{"positions"}
	case readmodel.ActivityAppend:
		return []string{"activities"}
	case readmodel.AuditAppend:
		return []string{"audit"}
	case readmodel.ResnapshotRequired:
		return []string{"all"}
	default:
		return []string{"system"}
	}
}
