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
	InstrumentProduct  string    `json:"instrumentProduct,omitempty"`
	BaseAsset          string    `json:"baseAsset,omitempty"`
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
	VenueCode       string               `json:"venueCode,omitempty"`
	VenueMessage    string               `json:"venueMessage,omitempty"`
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
	ReconciliationAwaitingHandoff  ReconciliationReason = "awaiting_handoff"
)

// Commands must begin execution promptly. Recovery never sends a stale intent.
const HandoffStartWindow = 30 * time.Second
const HandoffNotStarted = "HANDOFF_NOT_STARTED"

type Command struct {
	CommandID   string `json:"commandId"`
	PrincipalID string `json:"-"`
	AccountID   string `json:"accountId"`
	LineageID   string `json:"lineageId"`
	ProductID   string `json:"productId"`

	// ReservationID is retained for historical reads; new commands do not reserve resources.
	ReservationID     *string               `json:"reservationId"`
	IdempotencyKey    string                `json:"idempotencyKey"`
	Method            string                `json:"-"`
	Path              string                `json:"-"`
	CommandKind       string                `json:"commandKind"`
	State             State                 `json:"state"`
	Payload           json.RawMessage       `json:"payload"`
	PayloadHash       string                `json:"-"`
	Blockers          []Blocker             `json:"blockers"`
	CreatedAt         time.Time             `json:"createdAt"`
	UpdatedAt         time.Time             `json:"updatedAt"`
	ExecutionEvidence ExecutionEvidence     `json:"executionEvidence,omitempty"`
	Reconciliation    *ReconciliationStatus `json:"reconciliation,omitempty"`
}

type ReconciliationStatus struct {
	Reason        ReconciliationReason `json:"reason"`
	NextAttemptAt time.Time            `json:"nextAttemptAt"`
	AttemptCount  int                  `json:"attemptCount"`
}

type Blocker struct {
	Code string `json:"code"`
}

type CreateRequest struct {
	PrincipalID, AccountID, ProductID, LineageID string
	IdempotencyKey, Method, Path, CommandKind    string
	Payload                                      json.RawMessage
}

// IdempotencyIdentity is the persisted request namespace, independent of runtime routing.
type IdempotencyIdentity struct {
	PrincipalID, Method, Path, Key string
}

func (service *Service) Find(ctx context.Context, identity IdempotencyIdentity) (Command, error) {
	if identity.PrincipalID == "" || identity.Method == "" || identity.Path == "" || identity.Key == "" {
		return Command{}, fmt.Errorf("command lookup identity is required")
	}
	identity.Method = strings.ToUpper(identity.Method)
	return service.store.Find(ctx, identity)
}

func (store *MemoryStore) Find(_ context.Context, identity IdempotencyIdentity) (Command, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := commandKey(Command{PrincipalID: identity.PrincipalID, Method: identity.Method, Path: identity.Path, IdempotencyKey: identity.Key})
	id, ok := store.keys[key]
	if !ok {
		return Command{}, ErrNotFound
	}
	return store.withReconciliation(store.commands[id]), nil
}

type ReconciliationTask struct {
	CommandID    string               `json:"commandId"`
	Reason       ReconciliationReason `json:"reason"`
	NextAttempt  time.Time            `json:"nextAttemptAt"`
	LeaseUntil   time.Time            `json:"leaseUntil"`
	AttemptCount int                  `json:"attemptCount"`
}

type TransitionRequest struct {
	CommandID     string
	ExpectedState State
	State         State
	Blockers      []Blocker
	Reason        ReconciliationReason
	NextAttemptAt time.Time
	Evidence      ExecutionEvidence
}

type Store interface {
	Create(context.Context, Command) (Command, bool, error)
	Get(context.Context, string) (Command, error)
	Find(context.Context, IdempotencyIdentity) (Command, error)
	ListOwned(context.Context, string, string) ([]Command, error)
	// Transition validates and merges against the stored command atomically with
	// the state, evidence and reconciliation task update. Implementations must
	// hold their lock/transaction across ReduceTransition and the write.
	Transition(context.Context, TransitionRequest, time.Time) (Command, error)
	ClaimDue(context.Context, time.Time, time.Time, int) ([]ReconciliationTask, error)
}

type Service struct {
	store     Store
	now       func() time.Time
	live      atomic.Bool
	changesMu sync.Mutex
	changes   map[string]map[chan struct{}]struct{}
}

func NewService(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

func (service *Service) SetLiveWritesEnabled(enabled bool) { service.live.Store(enabled) }

func (service *Service) Get(ctx context.Context, commandID string) (Command, error) {
	return service.store.Get(ctx, commandID)
}

func (service *Service) GetOwned(ctx context.Context, principal string, commandID string) (Command, error) {
	command, err := service.store.Get(ctx, commandID)
	if err != nil {
		return Command{}, err
	}
	if command.PrincipalID != principal {
		return Command{}, ErrNotFound
	}
	return command, nil
}

func (service *Service) ListOwned(ctx context.Context, principal string, productID string) ([]Command, error) {
	if principal == "" || productID == "" {
		return nil, fmt.Errorf("command ownership fields are required")
	}
	return service.store.ListOwned(ctx, principal, productID)
}

func (service *Service) Create(ctx context.Context, request CreateRequest) (Command, bool, error) {
	canonical, hash, err := request.validate()
	if err != nil {
		return Command{}, false, err
	}
	// Every command reaches a real venue, so the operator switch is the only
	// thing between this call and the exchange. It used to also require the
	// command to declare live mode, back when a test command could be admitted
	// while the switch was off.
	if !service.live.Load() {
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
		IdempotencyKey: request.IdempotencyKey, Method: strings.ToUpper(request.Method), Path: request.Path,
		CommandKind: request.CommandKind, State: StateAccepted, Payload: canonical, PayloadHash: hash,
		Blockers: []Blocker{}, CreatedAt: now, UpdatedAt: now,
	}
	created, changed, err := service.store.Create(ctx, command)
	if err == nil && changed {
		service.NotifyChange(created.PrincipalID)
	}
	return created, changed, err
}

func newCommandID() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("generate command id: %w", err)
	}
	return "command_" + hex.EncodeToString(entropy[:]), nil
}

func (service *Service) Transition(ctx context.Context, request TransitionRequest) (Command, error) {
	now := service.now().UTC()
	if request.State == StateSubmitted && request.Reason == "" && request.NextAttemptAt.IsZero() {
		request.Reason = ReconciliationAwaitingTerminal
		delay := 500 * time.Millisecond
		if current, err := service.store.Get(ctx, request.CommandID); err == nil && current.State == StateSubmitted {
			delay = 5 * time.Second
		}
		request.NextAttemptAt = now.Add(delay)
	}
	updated, err := service.store.Transition(ctx, request, now)
	if err == nil {
		service.NotifyChange(updated.PrincipalID)
	}
	return updated, err
}

// BeginExecution atomically consumes the single permission to send this
// command. State and recovery task are persisted before any network write.
// Replays and concurrent callers only observe the current result.
func (service *Service) BeginExecution(ctx context.Context, commandID string) (Command, bool, error) {
	current, err := service.Get(ctx, commandID)
	if err != nil || current.State != StateAccepted {
		return current, false, err
	}
	now := service.now().UTC()
	request := TransitionRequest{CommandID: commandID, ExpectedState: StateAccepted,
		State: StateUncertain, Reason: ReconciliationAmbiguousHandoff, NextAttemptAt: now.Add(HandoffStartWindow)}
	expired := !now.Before(current.CreatedAt.Add(HandoffStartWindow))
	if expired {
		request = TransitionRequest{CommandID: commandID, ExpectedState: StateAccepted, State: StateFailed,
			Blockers: []Blocker{{Code: HandoffNotStarted}}}
	}
	updated, err := service.store.Transition(ctx, request, now)
	if err != nil && updated.CommandID == "" {
		updated = current
	}
	if errors.Is(err, ErrInvalidTransition) {
		return updated, false, nil
	}
	return updated, err == nil && !expired, err
}

// ReduceTransition checks a transition against the latest persisted command.
// Stores call it while holding exclusive access to that command. Keeping the
// merge here prevents concurrent evidence observations from replacing each
// other and prevents a stale writer from reopening a terminal command.
func ReduceTransition(current Command, request TransitionRequest, now time.Time) (Command, error) {
	if request.CommandID == "" {
		return current, fmt.Errorf("command id is required")
	}
	if request.CommandID != current.CommandID {
		return current, fmt.Errorf("command identity mismatch")
	}
	if request.ExpectedState != "" && current.State != request.ExpectedState {
		return current, ErrInvalidTransition
	}
	if request.State == StateUncertain || request.State == StateSubmitted {
		if request.NextAttemptAt.IsZero() || !request.Reason.valid() {
			return current, fmt.Errorf("nonterminal state requires a bounded reconciliation plan")
		}
	} else if request.Reason != "" || !request.NextAttemptAt.IsZero() {
		return current, fmt.Errorf("reconciliation is valid only for nonterminal state")
	}
	if !allowedTransition(current.State, request.State) {
		return current, ErrInvalidTransition
	}
	if err := validateExecutionEvidence(current.ExecutionEvidence, request.Evidence); err != nil {
		return current, err
	}
	evidence, err := mergeExecutionEvidence(current.ExecutionEvidence, request.Evidence)
	if err != nil {
		return current, err
	}
	current.State, current.UpdatedAt, current.ExecutionEvidence = request.State, now, evidence
	if request.Blockers != nil {
		current.Blockers = append([]Blocker(nil), request.Blockers...)
	}
	return current, nil
}

func validateExecutionEvidence(current, next ExecutionEvidence) error {
	if executionEvidenceEmpty(next) {
		return nil
	}
	if next.CommandID == "" || next.GatewayOrderID == "" || next.ClientOrderID == "" || next.State == "" || next.ResourceVersion < 1 {
		return fmt.Errorf("Gateway execution evidence is incomplete")
	}
	if !executionEvidenceEmpty(current) && (current.CommandID != next.CommandID || current.GatewayOrderID != next.GatewayOrderID || current.ClientOrderID != next.ClientOrderID || next.ResourceVersion < current.ResourceVersion ||
		(current.VenueCode != "" && next.VenueCode != "" && current.VenueCode != next.VenueCode) ||
		(current.VenueMessage != "" && next.VenueMessage != "" && current.VenueMessage != next.VenueMessage)) {
		return fmt.Errorf("Gateway execution evidence conflicts with the durable command")
	}
	return nil
}

func executionEvidenceEmpty(value ExecutionEvidence) bool {
	return value.CommandID == "" && value.GatewayOrderID == "" && value.ClientOrderID == "" && value.State == "" && value.ResourceVersion == 0 && value.VenueCode == "" && value.VenueMessage == "" && len(value.OrderEvents) == 0 && len(value.Fills) == 0
}

func mergeExecutionEvidence(current, next ExecutionEvidence) (ExecutionEvidence, error) {
	if executionEvidenceEmpty(next) {
		return current, nil
	}
	merged := next
	if merged.VenueCode == "" {
		merged.VenueCode = current.VenueCode
	}
	if merged.VenueMessage == "" {
		merged.VenueMessage = current.VenueMessage
	}
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
	indexes := map[string]int{}
	merged.Fills = nil
	for _, fill := range append(append([]FillEvidence(nil), current.Fills...), next.Fills...) {
		if index, exists := indexes[fill.FillID]; exists {
			previous := merged.Fills[index]
			oldFacts, newFacts := previous, fill
			oldFacts.InstrumentProduct, oldFacts.BaseAsset = "", ""
			newFacts.InstrumentProduct, newFacts.BaseAsset = "", ""
			if oldFacts != newFacts || (previous.InstrumentProduct != "" && fill.InstrumentProduct != "" && previous.InstrumentProduct != fill.InstrumentProduct) || (previous.BaseAsset != "" && fill.BaseAsset != "" && previous.BaseAsset != fill.BaseAsset) {
				return current, fmt.Errorf("Gateway fill evidence conflicts")
			}
			if previous.InstrumentProduct == "" {
				previous.InstrumentProduct = fill.InstrumentProduct
			}
			if previous.BaseAsset == "" {
				previous.BaseAsset = fill.BaseAsset
			}
			merged.Fills[index] = previous
			continue
		}
		indexes[fill.FillID] = len(merged.Fills)
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

func (service *Service) ClaimDueReconciliation(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]ReconciliationTask, error) {
	if now.IsZero() || lease <= 0 || limit <= 0 || limit > 100 {
		return nil, fmt.Errorf("invalid reconciliation claim")
	}
	return service.store.ClaimDue(ctx, now, now.Add(lease), limit)
}

func (request CreateRequest) validate() (json.RawMessage, string, error) {
	if request.PrincipalID == "" || request.AccountID == "" || request.ProductID == "" || request.LineageID == "" ||
		request.IdempotencyKey == "" || request.Method == "" || request.Path == "" || request.CommandKind == "" {
		return nil, "", fmt.Errorf("command ownership and idempotency fields are required")
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
	return strings.Join([]string{command.PrincipalID, command.Method, command.Path, command.IdempotencyKey}, "\x00")
}

func (store *MemoryStore) Create(_ context.Context, command Command) (Command, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := commandKey(command)
	if id := store.keys[key]; id != "" {
		existing := store.commands[id]
		if existing.PayloadHash != command.PayloadHash || existing.AccountID != command.AccountID || existing.ProductID != command.ProductID || existing.LineageID != command.LineageID || existing.CommandKind != command.CommandKind {
			return existing, true, ErrIdempotencyConflict
		}
		return store.withReconciliation(existing), true, nil
	}
	store.commands[command.CommandID] = command
	store.keys[key] = command.CommandID
	if command.State == StateAccepted {
		store.tasks[command.CommandID] = ReconciliationTask{CommandID: command.CommandID,
			Reason: ReconciliationAwaitingHandoff, NextAttempt: command.CreatedAt.Add(HandoffStartWindow)}
	}
	return store.withReconciliation(command), false, nil
}

func (store *MemoryStore) Get(_ context.Context, id string) (Command, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	command, ok := store.commands[id]
	if !ok {
		return Command{}, ErrNotFound
	}
	return store.withReconciliation(command), nil
}

func (store *MemoryStore) ListOwned(_ context.Context, principal string, productID string) ([]Command, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	items := make([]Command, 0)
	for _, command := range store.commands {
		if command.PrincipalID == principal && command.ProductID == productID {
			items = append(items, store.withReconciliation(command))
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
	command, err := ReduceTransition(command, request, now)
	if err != nil {
		return command, err
	}
	store.commands[request.CommandID] = command
	if request.State == StateUncertain || request.State == StateSubmitted {
		task := store.tasks[request.CommandID]
		task.CommandID, task.Reason, task.NextAttempt, task.LeaseUntil = request.CommandID, request.Reason, request.NextAttemptAt, time.Time{}
		store.tasks[request.CommandID] = task
	} else {
		delete(store.tasks, request.CommandID)
	}
	return store.withReconciliation(command), nil
}

func (store *MemoryStore) ClaimDue(_ context.Context, now, leaseUntil time.Time, limit int) ([]ReconciliationTask, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]ReconciliationTask, 0, limit)
	for id, task := range store.tasks {
		if task.NextAttempt.After(now) || task.LeaseUntil.After(now) {
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

// Caller holds store.mu. This is read-only task status, not permission to resend.
func (store *MemoryStore) withReconciliation(command Command) Command {
	command.Reconciliation = nil
	if task, ok := store.tasks[command.CommandID]; ok {
		command.Reconciliation = &ReconciliationStatus{Reason: task.Reason, NextAttemptAt: task.NextAttempt, AttemptCount: task.AttemptCount}
	}
	return command
}
