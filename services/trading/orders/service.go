package orders

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrNotFound            = errors.New("command not found")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrLiveWriteDisabled   = errors.New("live private write disabled")
	ErrInvalidTransition   = errors.New("invalid command state transition")
)

type Mode string

const (
	ModeTest Mode = "test"
	ModeLive Mode = "live"
)

type State string

const (
	StateAccepted  State = "accepted"
	StateSubmitted State = "submitted"
	StateUncertain State = "uncertain"
	StateFilled    State = "filled"
	StateCanceled  State = "canceled"
	StateFailed    State = "failed"
)

type HandoffOutcome string

const (
	HandoffNotAttempted   HandoffOutcome = "not_attempted"
	HandoffRejected       HandoffOutcome = "rejected"
	HandoffAccepted       HandoffOutcome = "accepted"
	HandoffOutcomeUnknown HandoffOutcome = "outcome_unknown"
)

type OrderEventEvidence struct {
	EventID         string    `json:"eventId"`
	State           string    `json:"state"`
	ResourceVersion int64     `json:"resourceVersion"`
	OccurredAt      time.Time `json:"occurredAt"`
}

type FillEvidence struct {
	FillID             string    `json:"fillId"`
	AccountID          string    `json:"accountId"`
	InstrumentID       string    `json:"instrumentId"`
	Side               string    `json:"side"`
	Quantity           string    `json:"quantity"`
	Price              string    `json:"price"`
	Fee                string    `json:"fee"`
	FeeCurrency        string    `json:"feeCurrency"`
	SettlementCurrency string    `json:"settlementCurrency"`
	OccurredAt         time.Time `json:"occurredAt"`
}

type ExecutionEvidence struct {
	CommandID       string               `json:"commandId,omitempty"`
	GatewayOrderID  string               `json:"gatewayOrderId,omitempty"`
	ClientOrderID   string               `json:"clientOrderId,omitempty"`
	State           string               `json:"state,omitempty"`
	ResourceVersion int64                `json:"resourceVersion,omitempty"`
	OrderEvents     []OrderEventEvidence `json:"orderEvents,omitempty"`
	Fills           []FillEvidence       `json:"fills,omitempty"`
}

type HandoffResult struct {
	Outcome  HandoffOutcome    `json:"outcome"`
	Evidence ExecutionEvidence `json:"evidence"`
}

type ReconciliationReason string

const (
	ReconciliationAmbiguousHandoff ReconciliationReason = "ambiguous_handoff"
	ReconciliationLookupFailed     ReconciliationReason = "lookup_failed"
	ReconciliationEvidenceInvalid  ReconciliationReason = "evidence_invalid"
	ReconciliationAwaitingTerminal ReconciliationReason = "awaiting_terminal_evidence"
)

type Command struct {
	CommandID         string            `json:"commandId"`
	PrincipalID       string            `json:"-"`
	AccountID         string            `json:"accountId"`
	LineageID         string            `json:"lineageId"`
	ProductID         string            `json:"productId"`
	Mode              Mode              `json:"mode"`
	ReservationID     *string           `json:"reservationId"`
	IdempotencyKey    string            `json:"idempotencyKey"`
	Method            string            `json:"-"`
	Path              string            `json:"-"`
	CommandKind       string            `json:"commandKind"`
	State             State             `json:"state"`
	Payload           json.RawMessage   `json:"payload"`
	PayloadHash       string            `json:"-"`
	Blockers          []Blocker         `json:"blockers"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	ExecutionEvidence ExecutionEvidence `json:"executionEvidence,omitempty"`
}

type Blocker struct {
	Code string `json:"code"`
}

type CreateRequest struct {
	PrincipalID, AccountID, ProductID, LineageID, ReservationID string
	IdempotencyKey, Method, Path, CommandKind                   string
	Mode                                                        Mode
	Payload                                                     json.RawMessage
}

type ReservationClaim struct {
	ReservationID, PrincipalID, ProductID string
	Mode                                  Mode
}

type ReservationGate interface {
	Consume(context.Context, ReservationClaim) error
}

type ReconciliationTask struct {
	CommandID    string               `json:"commandId"`
	Mode         Mode                 `json:"mode"`
	Reason       ReconciliationReason `json:"reason"`
	NextAttempt  time.Time            `json:"nextAttemptAt"`
	LeaseUntil   time.Time            `json:"leaseUntil"`
	AttemptCount int                  `json:"attemptCount"`
}

type TransitionRequest struct {
	CommandID     string
	State         State
	Reason        ReconciliationReason
	NextAttemptAt time.Time
	Evidence      ExecutionEvidence
}

type Store interface {
	Reserve(context.Context, Command) (Command, bool, error)
	Abort(context.Context, string) error
	Get(context.Context, string) (Command, error)
	ListOwned(context.Context, string, Mode, string) ([]Command, error)
	Transition(context.Context, TransitionRequest, time.Time) (Command, error)
	ClaimDue(context.Context, Mode, time.Time, time.Time, int) ([]ReconciliationTask, error)
}

type AtomicReservationStore interface {
	ReserveWithReservation(context.Context, Command, ReservationClaim) (Command, bool, error)
}

type Service struct {
	store Store
	gate  ReservationGate
	now   func() time.Time
	live  atomic.Bool
}

func NewService(store Store, gate ReservationGate, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, gate: gate, now: now}
}

func (service *Service) SetLiveWritesEnabled(enabled bool) { service.live.Store(enabled) }

func (service *Service) Get(ctx context.Context, commandID string) (Command, error) {
	return service.store.Get(ctx, commandID)
}

func (service *Service) GetOwned(ctx context.Context, principal string, mode Mode, commandID string) (Command, error) {
	command, err := service.store.Get(ctx, commandID)
	if err != nil {
		return Command{}, err
	}
	if command.PrincipalID != principal || command.Mode != mode {
		return Command{}, ErrNotFound
	}
	return command, nil
}

func (service *Service) ListOwned(ctx context.Context, principal string, mode Mode, productID string) ([]Command, error) {
	if principal == "" || (mode != ModeTest && mode != ModeLive) || productID == "" {
		return nil, fmt.Errorf("command ownership fields are required")
	}
	return service.store.ListOwned(ctx, principal, mode, productID)
}

func (service *Service) Create(ctx context.Context, request CreateRequest) (Command, bool, error) {
	canonical, hash, err := request.validate()
	if err != nil {
		return Command{}, false, err
	}
	if request.Mode == ModeLive && !service.live.Load() {
		return Command{}, false, ErrLiveWriteDisabled
	}
	now := service.now().UTC()
	commandID, err := newCommandID()
	if err != nil {
		return Command{}, false, err
	}
	command := Command{
		CommandID: commandID, PrincipalID: request.PrincipalID,
		AccountID: request.AccountID, LineageID: request.LineageID, ProductID: request.ProductID,
		Mode: request.Mode, IdempotencyKey: request.IdempotencyKey, Method: strings.ToUpper(request.Method), Path: request.Path,
		CommandKind: request.CommandKind, State: StateAccepted, Payload: canonical, PayloadHash: hash,
		Blockers: []Blocker{}, CreatedAt: now, UpdatedAt: now,
	}
	if request.ReservationID != "" {
		value := request.ReservationID
		command.ReservationID = &value
	}
	if command.ReservationID != nil && service.gate != nil {
		claim := ReservationClaim{
			ReservationID: *command.ReservationID, PrincipalID: command.PrincipalID, Mode: command.Mode, ProductID: command.ProductID,
		}
		if store, ok := service.store.(AtomicReservationStore); ok {
			return store.ReserveWithReservation(ctx, command, claim)
		}
	}
	stored, replay, err := service.store.Reserve(ctx, command)
	if err != nil || replay {
		return stored, replay, err
	}
	if command.ReservationID != nil && service.gate != nil {
		err = service.gate.Consume(ctx, ReservationClaim{
			ReservationID: *command.ReservationID, PrincipalID: command.PrincipalID, Mode: command.Mode, ProductID: command.ProductID,
		})
		if err != nil {
			_ = service.store.Abort(ctx, command.CommandID)
			return Command{}, false, fmt.Errorf("consume reservation: %w", err)
		}
	}
	return stored, false, nil
}

func newCommandID() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("generate command id: %w", err)
	}
	return "command_" + hex.EncodeToString(entropy[:]), nil
}

func (service *Service) Transition(ctx context.Context, request TransitionRequest) (Command, error) {
	if request.CommandID == "" {
		return Command{}, fmt.Errorf("command id is required")
	}
	if request.State == StateSubmitted && request.Reason == "" && request.NextAttemptAt.IsZero() {
		request.Reason = ReconciliationAwaitingTerminal
		request.NextAttemptAt = service.now().UTC().Add(5 * time.Second)
	}
	if request.State == StateUncertain || request.State == StateSubmitted {
		if request.NextAttemptAt.IsZero() || !request.Reason.valid() {
			return Command{}, fmt.Errorf("uncertain state requires a bounded reconciliation plan")
		}
	} else if request.Reason != "" || !request.NextAttemptAt.IsZero() {
		return Command{}, fmt.Errorf("reconciliation is valid only for nonterminal state")
	}
	current, err := service.store.Get(ctx, request.CommandID)
	if err != nil {
		return Command{}, err
	}
	if !allowedTransition(current.State, request.State) {
		return current, ErrInvalidTransition
	}
	if err := validateExecutionEvidence(current.ExecutionEvidence, request.Evidence); err != nil {
		return current, err
	}
	request.Evidence, err = mergeExecutionEvidence(current.ExecutionEvidence, request.Evidence)
	if err != nil {
		return current, err
	}
	return service.store.Transition(ctx, request, service.now().UTC())
}

func validateExecutionEvidence(current, next ExecutionEvidence) error {
	if executionEvidenceEmpty(next) {
		return nil
	}
	if next.CommandID == "" || next.GatewayOrderID == "" || next.ClientOrderID == "" || next.State == "" || next.ResourceVersion < 1 {
		return fmt.Errorf("Gateway execution evidence is incomplete")
	}
	if !executionEvidenceEmpty(current) && (current.CommandID != next.CommandID || current.GatewayOrderID != next.GatewayOrderID || current.ClientOrderID != next.ClientOrderID || next.ResourceVersion < current.ResourceVersion) {
		return fmt.Errorf("Gateway execution evidence conflicts with the durable command")
	}
	return nil
}

func executionEvidenceEmpty(value ExecutionEvidence) bool {
	return value.CommandID == "" && value.GatewayOrderID == "" && value.ClientOrderID == "" && value.State == "" && value.ResourceVersion == 0 && len(value.OrderEvents) == 0 && len(value.Fills) == 0
}

func mergeExecutionEvidence(current, next ExecutionEvidence) (ExecutionEvidence, error) {
	if executionEvidenceEmpty(next) {
		return current, nil
	}
	merged := next
	events := make(map[string]OrderEventEvidence, len(current.OrderEvents)+len(next.OrderEvents))
	merged.OrderEvents = nil
	for _, event := range append(append([]OrderEventEvidence(nil), current.OrderEvents...), next.OrderEvents...) {
		if previous, exists := events[event.EventID]; exists {
			if previous != event {
				return current, fmt.Errorf("Gateway order event evidence conflicts")
			}
			continue
		}
		events[event.EventID] = event
		merged.OrderEvents = append(merged.OrderEvents, event)
	}
	fills := make(map[string]FillEvidence, len(current.Fills)+len(next.Fills))
	merged.Fills = nil
	for _, fill := range append(append([]FillEvidence(nil), current.Fills...), next.Fills...) {
		if previous, exists := fills[fill.FillID]; exists {
			if previous != fill {
				return current, fmt.Errorf("Gateway fill evidence conflicts")
			}
			continue
		}
		fills[fill.FillID] = fill
		merged.Fills = append(merged.Fills, fill)
	}
	return merged, nil
}

func allowedTransition(from, to State) bool {
	if from == to {
		return true
	}
	switch from {
	case StateAccepted:
		return to == StateSubmitted || to == StateUncertain || to == StateFilled || to == StateCanceled || to == StateFailed
	case StateSubmitted:
		return to == StateUncertain || to == StateFilled || to == StateCanceled || to == StateFailed
	case StateUncertain:
		return to == StateSubmitted || to == StateFilled || to == StateCanceled || to == StateFailed
	default:
		return false
	}
}

func (service *Service) ClaimDueReconciliation(ctx context.Context, mode Mode, now time.Time, lease time.Duration, limit int) ([]ReconciliationTask, error) {
	if (mode != ModeTest && mode != ModeLive) || now.IsZero() || lease <= 0 || limit <= 0 || limit > 100 {
		return nil, fmt.Errorf("invalid reconciliation claim")
	}
	return service.store.ClaimDue(ctx, mode, now, now.Add(lease), limit)
}

func (request CreateRequest) validate() (json.RawMessage, string, error) {
	if request.PrincipalID == "" || request.AccountID == "" || request.ProductID == "" || request.LineageID == "" ||
		request.IdempotencyKey == "" || request.Method == "" || request.Path == "" || request.CommandKind == "" {
		return nil, "", fmt.Errorf("command ownership and idempotency fields are required")
	}
	if request.Mode != ModeTest && request.Mode != ModeLive {
		return nil, "", fmt.Errorf("invalid mode")
	}
	var decoded any
	if len(request.Payload) == 0 || json.Unmarshal(request.Payload, &decoded) != nil {
		return nil, "", fmt.Errorf("payload must be valid JSON")
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}

func (reason ReconciliationReason) valid() bool {
	return reason == ReconciliationAmbiguousHandoff || reason == ReconciliationLookupFailed || reason == ReconciliationEvidenceInvalid || reason == ReconciliationAwaitingTerminal
}

type MemoryStore struct {
	mu       sync.Mutex
	commands map[string]Command
	keys     map[string]string
	tasks    map[string]ReconciliationTask
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{commands: make(map[string]Command), keys: make(map[string]string), tasks: make(map[string]ReconciliationTask)}
}

func commandKey(command Command) string {
	return strings.Join([]string{command.PrincipalID, string(command.Mode), command.Method, command.Path, command.IdempotencyKey}, "\x00")
}

func (store *MemoryStore) Reserve(_ context.Context, command Command) (Command, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := commandKey(command)
	if id := store.keys[key]; id != "" {
		existing := store.commands[id]
		if existing.PayloadHash != command.PayloadHash || existing.AccountID != command.AccountID || existing.ProductID != command.ProductID || existing.LineageID != command.LineageID || existing.CommandKind != command.CommandKind || stringValue(existing.ReservationID) != stringValue(command.ReservationID) {
			return existing, true, ErrIdempotencyConflict
		}
		return existing, true, nil
	}
	store.commands[command.CommandID] = command
	store.keys[key] = command.CommandID
	return command, false, nil
}

func (store *MemoryStore) Abort(_ context.Context, id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	command, ok := store.commands[id]
	if !ok {
		return nil
	}
	delete(store.keys, commandKey(command))
	delete(store.commands, id)
	delete(store.tasks, id)
	return nil
}

func (store *MemoryStore) Get(_ context.Context, id string) (Command, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	command, ok := store.commands[id]
	if !ok {
		return Command{}, ErrNotFound
	}
	return command, nil
}

func (store *MemoryStore) ListOwned(_ context.Context, principal string, mode Mode, productID string) ([]Command, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	items := make([]Command, 0)
	for _, command := range store.commands {
		if command.PrincipalID == principal && command.Mode == mode && command.ProductID == productID {
			items = append(items, command)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CommandID < items[j].CommandID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items, nil
}

func (store *MemoryStore) Transition(_ context.Context, request TransitionRequest, now time.Time) (Command, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	command, ok := store.commands[request.CommandID]
	if !ok {
		return Command{}, ErrNotFound
	}
	command.State, command.UpdatedAt = request.State, now
	if !executionEvidenceEmpty(request.Evidence) {
		request.Evidence.OrderEvents = append([]OrderEventEvidence(nil), request.Evidence.OrderEvents...)
		request.Evidence.Fills = append([]FillEvidence(nil), request.Evidence.Fills...)
		command.ExecutionEvidence = request.Evidence
	}
	store.commands[request.CommandID] = command
	if request.State == StateUncertain || request.State == StateSubmitted {
		task := store.tasks[request.CommandID]
		task.CommandID, task.Mode, task.Reason, task.NextAttempt, task.LeaseUntil = request.CommandID, command.Mode, request.Reason, request.NextAttemptAt, time.Time{}
		store.tasks[request.CommandID] = task
	} else {
		delete(store.tasks, request.CommandID)
	}
	return command, nil
}

func (store *MemoryStore) ClaimDue(_ context.Context, mode Mode, now, leaseUntil time.Time, limit int) ([]ReconciliationTask, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]ReconciliationTask, 0, limit)
	for id, task := range store.tasks {
		if task.Mode != mode || task.NextAttempt.After(now) || task.LeaseUntil.After(now) {
			continue
		}
		task.LeaseUntil, task.AttemptCount = leaseUntil, task.AttemptCount+1
		store.tasks[id] = task
		result = append(result, task)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
