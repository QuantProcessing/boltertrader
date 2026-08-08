package work

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestBrowserEnqueueIsDurableIdempotentAndPrincipalScoped(t *testing.T) {
	now := time.Date(2026, 8, 6, 2, 30, 0, 0, time.UTC)
	service := NewWithStore("contract-v1", time.Minute, NewMemoryStore())
	service.SetClock(func() time.Time { return now })
	request := EnqueueRequest{
		PrincipalID: "browser-a", IdempotencyKey: "open-a", WorkKind: "open",
		LineageID: "lineage-a", ProductID: "funding", Mode: "test",
		FactsCursor: "cursor-preview-a", Facts: []Fact{{FactID: "fact-preview-a", Cursor: "cursor-preview-a", ProductID: "funding", Mode: "test", Kind: "funding.execution-input", Payload: json.RawMessage(`{}`), EffectiveAt: now}},
		AccountFences: json.RawMessage(`[{"accountId":"long","desiredStateRevision":4,"runtimeGeneration":2,"credentialVersion":3},{"accountId":"short","desiredStateRevision":5,"runtimeGeneration":3,"credentialVersion":4}]`),
		Payload:       json.RawMessage(`{"previewId":"preview-a"}`),
	}
	first, replay, err := service.Enqueue(context.Background(), request)
	if err != nil || replay || first.WorkItemID == "" || first.Status != "open" || first.WorkRevision != 1 {
		t.Fatalf("Enqueue() first=%+v replay=%v err=%v", first, replay, err)
	}
	second, replay, err := service.Enqueue(context.Background(), request)
	if err != nil || !replay || second.WorkItemID != first.WorkItemID {
		t.Fatalf("Enqueue() replay=%+v replay=%v err=%v", second, replay, err)
	}
	if _, _, err := service.Enqueue(context.Background(), func() EnqueueRequest {
		changed := request
		changed.Payload = json.RawMessage(`{"previewId":"changed"}`)
		return changed
	}()); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("Enqueue() conflict err=%v", err)
	}
	owned, err := service.GetOwnedWork(context.Background(), "browser-a", "test", first.WorkItemID)
	if err != nil || owned.LineageID != "lineage-a" {
		t.Fatalf("GetOwnedWork()=%+v err=%v", owned, err)
	}
	if _, err := service.GetOwnedWork(context.Background(), "browser-b", "test", first.WorkItemID); !errors.Is(err, ErrWorkNotFound) {
		t.Fatalf("cross-principal GetOwnedWork() err=%v", err)
	}
	items, err := service.ListOwnedWork(context.Background(), "browser-a", "test", "lineage-a")
	if err != nil || len(items) != 1 || items[0].WorkItemID != first.WorkItemID {
		t.Fatalf("ListOwnedWork()=%+v err=%v", items, err)
	}
}

func TestBrowserEnqueuePreservesModeAndRejectsInvalidFundingWork(t *testing.T) {
	service := NewWithStore("contract-v1", time.Minute, NewMemoryStore())
	now := time.Date(2026, 8, 6, 2, 30, 0, 0, time.UTC)
	base := EnqueueRequest{PrincipalID: "browser-a", IdempotencyKey: "a", WorkKind: "open", LineageID: "lineage-a", ProductID: "funding", Mode: "test", FactsCursor: "cursor-test", Facts: []Fact{{FactID: "fact-test", Cursor: "cursor-test", ProductID: "funding", Mode: "test", Kind: "funding.execution-input", Payload: json.RawMessage(`{}`), EffectiveAt: now}}, AccountFences: json.RawMessage(`[{"accountId":"long","desiredStateRevision":4,"runtimeGeneration":2,"credentialVersion":3},{"accountId":"short","desiredStateRevision":5,"runtimeGeneration":3,"credentialVersion":4}]`), Payload: json.RawMessage(`{}`)}
	live := base
	live.Mode = "live"
	live.FactsCursor = "cursor-live"
	live.Facts = []Fact{{FactID: "fact-live", Cursor: "cursor-live", ProductID: "funding", Mode: "live", Kind: "funding.execution-input", Payload: json.RawMessage(`{}`), EffectiveAt: now}}
	service.SetLiveWritesEnabled(true)
	item, _, err := service.Enqueue(context.Background(), live)
	if err != nil || item.Mode != "live" {
		t.Fatalf("live Enqueue()=%+v err=%v", item, err)
	}
	for name, mutate := range map[string]func(*EnqueueRequest){
		"invalid mode":      func(r *EnqueueRequest) { r.Mode = "paper" },
		"foreign product":   func(r *EnqueueRequest) { r.ProductID = "manual" },
		"unknown work kind": func(r *EnqueueRequest) { r.WorkKind = "raw-order" },
		"missing principal": func(r *EnqueueRequest) { r.PrincipalID = "" },
		"empty fences":      func(r *EnqueueRequest) { r.AccountFences = json.RawMessage(`[]`) },
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, _, err := service.Enqueue(context.Background(), request); err == nil {
				t.Fatal("Enqueue() unexpectedly succeeded")
			}
		})
	}
}

func TestBrowserFactBatchIsAtomicAndImmutableInMemory(t *testing.T) {
	now := time.Date(2026, 8, 6, 2, 30, 0, 0, time.UTC)
	service := NewWithStore("contract-v1", time.Minute, NewMemoryStore())
	service.SetClock(func() time.Time { return now })
	facts := []Fact{{FactID: "fact-close-b", Cursor: "cursor-close", AccountID: "short", ProductID: "funding", Mode: "test", Kind: "funding.unwind-reservation", Payload: json.RawMessage(`{"reservationId":"short"}`), EffectiveAt: now}, {FactID: "fact-close-a", Cursor: "cursor-close", AccountID: "long", ProductID: "funding", Mode: "test", Kind: "funding.unwind-reservation", Payload: json.RawMessage(`{"reservationId":"long"}`), EffectiveAt: now}}
	request := EnqueueRequest{PrincipalID: "browser-a", IdempotencyKey: "close-a", WorkKind: "close", LineageID: "lineage-a", ProductID: "funding", Mode: "test", FactsCursor: "cursor-close", Facts: facts, AccountFences: json.RawMessage(`[{"accountId":"long","desiredStateRevision":4,"runtimeGeneration":2,"credentialVersion":3},{"accountId":"short","desiredStateRevision":5,"runtimeGeneration":3,"credentialVersion":4}]`), Payload: json.RawMessage(`{}`)}
	if _, replay, err := service.Enqueue(context.Background(), request); err != nil || replay {
		t.Fatalf("first batch replay=%v err=%v", replay, err)
	}
	reordered := request
	reordered.Facts = []Fact{facts[1], facts[0]}
	if _, _, err := service.Enqueue(context.Background(), reordered); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("reordered replay err=%v", err)
	}
	changed := request
	changed.IdempotencyKey = "close-changed"
	changed.Facts = append([]Fact(nil), facts...)
	changed.Facts[1].Payload = json.RawMessage(`{"reservationId":"changed"}`)
	if _, _, err := service.Enqueue(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed batch err=%v", err)
	}
	duplicate := request
	duplicate.IdempotencyKey = "close-duplicate"
	duplicate.Facts = []Fact{facts[0], facts[0]}
	if _, _, err := service.Enqueue(context.Background(), duplicate); err == nil {
		t.Fatal("duplicate fact ID accepted")
	}
	stored := service.store.(*MemoryStore).exactFactsLocked(request.FactsCursor, request.AccountFences)
	if len(stored) != 2 || stored[0].FactID != "fact-close-a" || stored[1].FactID != "fact-close-b" {
		t.Fatalf("stored batch=%+v", stored)
	}
}
