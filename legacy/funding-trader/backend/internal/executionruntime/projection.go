package executionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
)

type ExecutionSnapshotReader interface {
	ListExecutions(context.Context, readmodel.Scope) ([]execution.Execution, error)
}

type ProjectionWriter interface {
	ApplyExecutionProjection(
		context.Context,
		string,
		readmodel.PositionProjection,
		readmodel.ActivityProjection,
	) ([]readmodel.Event, error)
}

type StoredBroadcaster interface {
	BroadcastStored(uistream.StoredEvent) error
}

type ProjectionStore struct {
	reader      ExecutionSnapshotReader
	writer      ProjectionWriter
	broadcaster StoredBroadcaster
	bootID      string
}

func NewProjectionStore(reader ExecutionSnapshotReader, writer ProjectionWriter, broadcaster StoredBroadcaster, bootID string) *ProjectionStore {
	return &ProjectionStore{
		reader:      reader,
		writer:      writer,
		broadcaster: broadcaster,
		bootID:      strings.TrimSpace(bootID),
	}
}

func (store *ProjectionStore) ListExecutions(ctx context.Context, scope readmodel.Scope) ([]execution.Execution, error) {
	if store == nil || store.reader == nil {
		return nil, errors.New("execution snapshot reader is not configured")
	}
	return store.reader.ListExecutions(ctx, scope)
}

func (store *ProjectionStore) ApplyExecutionProjection(
	ctx context.Context,
	_ string,
	position readmodel.PositionProjection,
	activity readmodel.ActivityProjection,
) ([]readmodel.Event, error) {
	if store == nil || store.writer == nil {
		return nil, errors.New("projection writer is not configured")
	}
	events, err := store.writer.ApplyExecutionProjection(ctx, store.bootID, position, activity)
	if err != nil {
		return nil, err
	}
	return events, nil
}

func (store *ProjectionStore) BroadcastProjectionEvents(events []readmodel.Event) error {
	if store == nil {
		return errors.New("projection store is not configured")
	}
	ordered := append([]readmodel.Event(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].ID < ordered[j].ID
	})
	for _, event := range ordered {
		if event.ID == 0 {
			continue
		}
		if err := store.broadcast(event); err != nil {
			return err
		}
	}
	return nil
}

func (store *ProjectionStore) broadcast(event readmodel.Event) error {
	if store == nil || store.broadcaster == nil {
		return errors.New("projection broadcaster is not configured")
	}
	if event.ID <= 0 || strings.TrimSpace(string(event.Kind)) == "" || !json.Valid(event.Payload) || event.OccurredAt.IsZero() {
		return errors.New("committed projection event is invalid")
	}
	return store.broadcaster.BroadcastStored(uistream.StoredEvent{
		Sequence:   uint64(event.ID),
		Scope:      "all",
		Kind:       string(event.Kind),
		Data:       cloneJSON(event.Payload),
		OccurredAt: event.OccurredAt.UTC(),
	})
}

type ProjectingPublisher struct {
	inner       executionservice.Publisher
	projector   *executionprojection.Projector
	broadcaster interface {
		BroadcastProjectionEvents([]readmodel.Event) error
	}
}

func NewProjectingPublisher(inner executionservice.Publisher, store executionprojection.Store) *ProjectingPublisher {
	publisher := &ProjectingPublisher{
		inner: inner, projector: executionprojection.NewProjector(store, ""),
	}
	if broadcaster, ok := store.(interface {
		BroadcastProjectionEvents([]readmodel.Event) error
	}); ok {
		publisher.broadcaster = broadcaster
	}
	return publisher
}

func (publisher *ProjectingPublisher) Publish(ctx context.Context, events []executionservice.CommittedEvent) error {
	if publisher == nil {
		return errors.New("projecting publisher is not configured")
	}
	if publisher.projector == nil {
		return errors.New("execution projector is not configured")
	}
	var projectionEvents []readmodel.Event
	for _, event := range events {
		if event.Kind != string(readmodel.ExecutionUpsert) {
			continue
		}
		snapshot, err := decodeCommittedExecution(event)
		if err != nil {
			return err
		}
		scope := readmodel.Scope{PrincipalID: snapshot.PrincipalID, Mode: snapshot.Mode}
		if strings.TrimSpace(scope.PrincipalID) == "" || !isProductMode(scope.Mode) {
			return errors.New("execution projection scope is invalid")
		}
		committed, err := publisher.projector.ProjectEvents(ctx, scope, snapshot)
		if err != nil {
			return err
		}
		projectionEvents = append(projectionEvents, committed...)
	}
	if publisher.inner != nil {
		if err := publisher.inner.Publish(ctx, events); err != nil {
			return err
		}
	}
	if len(projectionEvents) > 0 {
		if publisher.broadcaster == nil {
			return errors.New("projection broadcaster is not configured")
		}
		if err := publisher.broadcaster.BroadcastProjectionEvents(projectionEvents); err != nil {
			return err
		}
	}
	return nil
}

func (publisher *ProjectingPublisher) Repair(
	ctx context.Context,
	scope executionservice.Scope,
) error {
	if publisher == nil || publisher.projector == nil {
		return errors.New("execution projection repair is not configured")
	}
	events, err := publisher.projector.RebuildEvents(ctx, readmodel.Scope{
		PrincipalID: scope.PrincipalID,
		Mode:        scope.Mode,
	})
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	if publisher.broadcaster == nil {
		return errors.New("projection broadcaster is not configured")
	}
	return publisher.broadcaster.BroadcastProjectionEvents(events)
}

func decodeCommittedExecution(event executionservice.CommittedEvent) (execution.Execution, error) {
	if strings.TrimSpace(event.EntityID) == "" || !json.Valid(event.Payload) {
		return execution.Execution{}, errors.New("committed execution event is invalid")
	}
	if _, err := strconv.ParseUint(strings.TrimSpace(event.ID), 10, 64); err != nil {
		return execution.Execution{}, errors.New("committed execution event id is invalid")
	}
	restored, err := execution.RestoreExecution(event.Payload)
	if err != nil {
		return execution.Execution{}, fmt.Errorf("decode execution snapshot: %w", err)
	}
	snapshot := restored.Snapshot()
	if snapshot.ID != event.EntityID {
		return execution.Execution{}, errors.New("committed execution entity mismatch")
	}
	if snapshot.PrincipalID == "" || !isProductMode(snapshot.Mode) {
		return execution.Execution{}, errors.New("committed execution scope is invalid")
	}
	return snapshot, nil
}

func isProductMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "test", "live":
		return true
	default:
		return false
	}
}
