package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	PrincipalID  string
	Action       string
	ResourceType string
	ResourceID   string
	Outcome      string
	RequestID    string
}

type Record struct {
	ID           string    `json:"auditId"`
	PrincipalID  string    `json:"principalId"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	Outcome      string    `json:"outcome"`
	RequestID    string    `json:"requestId"`
	OccurredAt   time.Time `json:"occurredAt"`
}

type Filter struct {
	ResourceID string
	Limit      int
}

type Store interface {
	Append(context.Context, Record) error
	List(context.Context, Filter) ([]Record, error)
}

type Service struct {
	store Store
	now   func() time.Time
	id    func() string
}

func New(store Store, now func() time.Time, id func() string) (*Service, error) {
	if store == nil {
		return nil, errors.New("audit store is required")
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if id == nil {
		return nil, errors.New("audit id generator is required")
	}
	return &Service{store: store, now: now, id: id}, nil
}

func (service *Service) Record(ctx context.Context, entry Entry) (Record, error) {
	if err := validateEntry(entry); err != nil {
		return Record{}, err
	}
	record := Record{
		ID: service.id(), PrincipalID: entry.PrincipalID, Action: entry.Action,
		ResourceType: entry.ResourceType, ResourceID: entry.ResourceID,
		Outcome: entry.Outcome, RequestID: entry.RequestID, OccurredAt: service.now().UTC(),
	}
	if err := service.store.Append(ctx, record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (service *Service) List(ctx context.Context, filter Filter) ([]Record, error) {
	if filter.Limit <= 0 || filter.Limit > 500 {
		return nil, errors.New("audit limit is out of range")
	}
	return service.store.List(ctx, filter)
}

func validateEntry(entry Entry) error {
	values := []string{entry.PrincipalID, entry.Action, entry.ResourceType, entry.ResourceID, entry.Outcome, entry.RequestID}
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 256 {
			return errors.New("audit entry is invalid")
		}
	}
	combined := strings.ToLower(fmt.Sprint(values))
	if strings.Contains(combined, "secretmaterial") || strings.Contains(combined, "secretref") || strings.Contains(combined, "password") || strings.Contains(combined, "authorization") {
		return errors.New("audit entry contains a sensitive field")
	}
	return nil
}

type MemoryStore struct {
	mu      sync.RWMutex
	records []Record
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (store *MemoryStore) Append(_ context.Context, record Record) error {
	store.mu.Lock()
	store.records = append(store.records, record)
	store.mu.Unlock()
	return nil
}

func (store *MemoryStore) List(_ context.Context, filter Filter) ([]Record, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]Record, 0, filter.Limit)
	for index := len(store.records) - 1; index >= 0 && len(result) < filter.Limit; index-- {
		record := store.records[index]
		if filter.ResourceID == "" || record.ResourceID == filter.ResourceID {
			result = append(result, record)
		}
	}
	return result, nil
}
