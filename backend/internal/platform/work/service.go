package work

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/shopspring/decimal"
)

var (
	ErrWorkerNotRegistered   = errors.New("worker not registered")
	ErrWorkerAlreadyActive   = errors.New("worker already active")
	ErrWorkerStale           = errors.New("stale worker generation")
	ErrWorkerNotReady        = errors.New("worker is not ready to claim")
	ErrWorkerDraining        = errors.New("worker is draining")
	ErrLeaseStale            = errors.New("stale lease fence")
	ErrLeaseExpired          = errors.New("work lease expired")
	ErrWorkRevisionConflict  = errors.New("work revision conflict")
	ErrAuthorizationStale    = errors.New("work authorization stale")
	ErrWorkNotFound          = errors.New("work not found")
	ErrDependencyUnavailable = errors.New("work dependency unavailable")
	ErrAttemptsExhausted     = errors.New("work attempts exhausted")
	ErrIdempotencyConflict   = errors.New("work idempotency conflict")
	ErrResnapshotRequired    = errors.New("work facts resnapshot required")
	ErrLiveWriteDisabled     = errors.New("live private write disabled")
)

type RegistrationRequest struct {
	WorkerID        string `json:"workerId"`
	InstanceID      string `json:"instanceId"`
	ProductID       string `json:"productId"`
	Mode            string `json:"mode"`
	ContractVersion string `json:"contractVersion"`
	IdempotencyKey  string `json:"-"`
}

type HeartbeatRequest struct {
	WorkerGeneration       int      `json:"workerGeneration"`
	InstanceID             string   `json:"instanceId"`
	State                  string   `json:"state"`
	ActiveLeaseIDs         []string `json:"activeLeaseIds"`
	LastAppliedFactsCursor string   `json:"lastAppliedFactsCursor"`
}

type DrainRequest struct {
	WorkerGeneration int    `json:"workerGeneration"`
	InstanceID       string `json:"instanceId"`
	IdempotencyKey   string `json:"idempotencyKey"`
	Reason           string `json:"reason"`
}

type ClaimRequest struct {
	WorkerID         string `json:"workerId"`
	WorkerGeneration int    `json:"workerGeneration"`
	InstanceID       string `json:"instanceId"`
	IdempotencyKey   string `json:"idempotencyKey"`
	AfterCursor      string `json:"afterCursor"`
	WaitSeconds      int    `json:"waitSeconds"`
	MaxItems         int    `json:"maxItems"`
}

type LeaseRequest struct {
	WorkerID         string `json:"workerId"`
	WorkerGeneration int    `json:"workerGeneration"`
	WorkItemID       string `json:"workItemId"`
	WorkRevision     int    `json:"workRevision"`
	LeaseGeneration  int    `json:"leaseGeneration"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type IntentRequest struct {
	WorkerID         string          `json:"workerId"`
	WorkerGeneration int             `json:"workerGeneration"`
	WorkItemID       string          `json:"workItemId"`
	WorkRevision     int             `json:"workRevision"`
	LeaseGeneration  int             `json:"leaseGeneration"`
	IdempotencyKey   string          `json:"idempotencyKey"`
	IntentKind       string          `json:"intentKind"`
	FactsCursor      string          `json:"factsCursor"`
	AccountFences    json.RawMessage `json:"accountFences"`
	Payload          json.RawMessage `json:"payload"`
}

type CompleteRequest struct {
	WorkerID         string          `json:"workerId"`
	WorkerGeneration int             `json:"workerGeneration"`
	WorkItemID       string          `json:"workItemId"`
	WorkRevision     int             `json:"workRevision"`
	LeaseGeneration  int             `json:"leaseGeneration"`
	IdempotencyKey   string          `json:"idempotencyKey"`
	FactsCursor      string          `json:"factsCursor"`
	Result           json.RawMessage `json:"result"`
}

type FailRequest struct {
	WorkerID         string          `json:"workerId"`
	WorkerGeneration int             `json:"workerGeneration"`
	WorkItemID       string          `json:"workItemId"`
	WorkRevision     int             `json:"workRevision"`
	LeaseGeneration  int             `json:"leaseGeneration"`
	IdempotencyKey   string          `json:"idempotencyKey"`
	FactsCursor      string          `json:"factsCursor"`
	ErrorCode        string          `json:"errorCode"`
	Retryable        bool            `json:"retryable"`
	Details          json.RawMessage `json:"details"`
}

type Registration struct {
	WorkerID               string    `json:"workerId"`
	InstanceID             string    `json:"instanceId"`
	ProductID              string    `json:"productId"`
	Mode                   string    `json:"mode"`
	ContractVersion        string    `json:"contractVersion"`
	Generation             int       `json:"generation"`
	RegisteredAt           time.Time `json:"registeredAt"`
	HeartbeatExpiresAt     time.Time `json:"heartbeatExpiresAt"`
	LastHeartbeat          time.Time `json:"lastHeartbeat"`
	LastAppliedFactsCursor string    `json:"lastAppliedFactsCursor,omitempty"`
	Draining               bool      `json:"draining"`
	State                  string    `json:"state"`
}

type Readiness struct {
	WorkerID          string    `json:"workerId"`
	WorkerGeneration  int       `json:"workerGeneration"`
	ContractVersion   string    `json:"contractVersion"`
	Mode              string    `json:"mode"`
	Registered        bool      `json:"registered"`
	Heartbeating      bool      `json:"heartbeating"`
	CanClaim          bool      `json:"canClaim"`
	FreshHeartbeat    bool      `json:"freshHeartbeat"`
	Draining          bool      `json:"draining"`
	State             string    `json:"state"`
	ObservedAt        time.Time `json:"observedAt"`
	LastHeartbeatAt   time.Time `json:"lastHeartbeatAt,omitempty"`
	ActiveLeaseCount  int       `json:"activeLeaseCount"`
	DependenciesReady bool      `json:"dependenciesReady"`
}

type Claim struct {
	WorkItemID      string          `json:"workItemId"`
	WorkRevision    int             `json:"workRevision"`
	LeaseID         string          `json:"leaseId"`
	LeaseGeneration int             `json:"leaseGeneration"`
	FactsCursor     string          `json:"factsCursor"`
	AccountFences   json.RawMessage `json:"accountFences,omitempty"`
	Attempt         int             `json:"attempt"`
}

type WorkItem struct {
	WorkItemID      string          `json:"workItemId"`
	PrincipalID     string          `json:"-"`
	WorkKind        string          `json:"workKind"`
	LineageID       string          `json:"lineageId"`
	WorkRevision    int             `json:"workRevision"`
	ProductID       string          `json:"productId"`
	Mode            string          `json:"mode"`
	Status          string          `json:"state"`
	FactsCursor     string          `json:"factsCursor"`
	AccountFences   json.RawMessage `json:"accountFences"`
	Payload         json.RawMessage `json:"payload"`
	Result          json.RawMessage `json:"result,omitempty"`
	ErrorCode       string          `json:"errorCode,omitempty"`
	Retryable       bool            `json:"retryable,omitempty"`
	Attempt         int             `json:"attempt"`
	MaxAttempts     int             `json:"maxAttempts"`
	NotBefore       time.Time       `json:"notBefore"`
	LeaseGeneration int             `json:"leaseGeneration"`
	NextAttemptAt   time.Time       `json:"nextAttemptAt,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

type EnqueueRequest struct {
	PrincipalID    string          `json:"principalId"`
	IdempotencyKey string          `json:"idempotencyKey"`
	WorkKind       string          `json:"workKind"`
	LineageID      string          `json:"lineageId"`
	ProductID      string          `json:"productId"`
	Mode           string          `json:"mode"`
	FactsCursor    string          `json:"factsCursor"`
	Facts          []Fact          `json:"facts"`
	AccountFences  json.RawMessage `json:"accountFences"`
	Payload        json.RawMessage `json:"payload"`
}

type Lease struct {
	LeaseID          string    `json:"leaseId"`
	LeaseGeneration  int       `json:"leaseGeneration"`
	WorkItemID       string    `json:"workItemId"`
	WorkRevision     int       `json:"workRevision"`
	WorkerID         string    `json:"workerId"`
	WorkerGeneration int       `json:"workerGeneration"`
	FactsCursor      string    `json:"factsCursor"`
	Status           string    `json:"status"`
	ExpiresAt        time.Time `json:"expiresAt"`
	AcquiredAt       time.Time `json:"acquiredAt"`
	RenewalCount     int       `json:"renewalCount"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type Fact struct {
	FactID      string          `json:"factId"`
	Cursor      string          `json:"cursor"`
	AccountID   string          `json:"accountId"`
	ProductID   string          `json:"productId"`
	Mode        string          `json:"mode"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
	EffectiveAt time.Time       `json:"effectiveAt"`
}

type ClaimPage struct {
	WorkItem   *WorkItem `json:"workItem"`
	Lease      *Lease    `json:"lease"`
	NextCursor string    `json:"nextCursor"`
}

type FactRequest struct {
	WorkerID         string
	WorkerGeneration int
	WorkItemID       string
	WorkRevision     int
	LeaseID          string
	LeaseGeneration  int
	AfterCursor      string
	Limit            int
}

type FactPage struct {
	Facts              []Fact    `json:"facts"`
	NextCursor         string    `json:"nextCursor"`
	ResnapshotRequired bool      `json:"resnapshotRequired"`
	AsOf               time.Time `json:"asOf"`
}

type IntentRecord struct {
	Lease         Lease           `json:"lease"`
	Command       orders.Command  `json:"command"`
	WorkPayload   json.RawMessage `json:"workPayload,omitempty"`
	AccountFences json.RawMessage `json:"accountFences,omitempty"`
	Facts         []Fact          `json:"facts,omitempty"`
}

type CommandGateway interface {
	Handoff(context.Context, orders.Command) (orders.HandoffResult, error)
}

// PayloadProcessor resolves a durable product intent into durable, replayable
// product-neutral child commands before any Gateway handoff.
type PayloadProcessor interface {
	Process(context.Context, orders.Command) ([]orders.Command, error)
	RecordHandoff(context.Context, orders.Command, orders.HandoffResult) (orders.Command, error)
}

// IntentPlanProducer independently derives durable execution plans from the
// Platform-owned work payload and fact snapshot before product intents can be
// normalized into Gateway commands.
type IntentPlanProducer interface {
	Prepare(context.Context, IntentRecord) error
}

type DependencyReadinessEvaluator interface {
	Check(context.Context) error
}

type DependencyReadinessFunc func(context.Context) error

func (evaluate DependencyReadinessFunc) Check(ctx context.Context) error { return evaluate(ctx) }

type Store interface {
	EnqueueWork(context.Context, EnqueueRequest, time.Time) (WorkItem, bool, error)
	GetOwnedWork(context.Context, string, string, string) (WorkItem, error)
	ListOwnedWork(context.Context, string, string, string) ([]WorkItem, error)
	RegisterWorker(context.Context, RegistrationRequest, time.Time, time.Duration) (Registration, error)
	HeartbeatWorker(context.Context, string, HeartbeatRequest, time.Time) (Registration, error)
	DrainWorker(context.Context, string, DrainRequest, time.Time) (Registration, error)
	GetWorker(context.Context, string) (Registration, error)
	ListWorkerIDs(context.Context) ([]string, error)
	CountActiveLeases(context.Context, string, int, time.Time) (int, error)
	ClaimWork(context.Context, ClaimRequest, time.Time, time.Duration) (ClaimPage, error)
	FactPage(context.Context, FactRequest, bool, time.Time) (FactPage, error)
	RenewLease(context.Context, string, LeaseRequest, time.Time, time.Duration) (Lease, error)
	RecordIntent(context.Context, string, IntentRequest, time.Time) (IntentRecord, error)
	TransitionIntentCommand(context.Context, string, orders.State, time.Time) (orders.Command, error)
	CompleteLease(context.Context, string, CompleteRequest, time.Time) (Lease, error)
	FailLease(context.Context, string, FailRequest, time.Time) (Lease, error)
}

type Service struct {
	store           Store
	heartbeatTTL    time.Duration
	leaseTTL        time.Duration
	contractVersion string
	now             func() time.Time
	processor       PayloadProcessor
	planProducer    IntentPlanProducer
	gateway         CommandGateway
	dependencies    DependencyReadinessEvaluator
	live            atomic.Bool
}

func New(contractVersion string, heartbeatTTL time.Duration) *Service {
	return NewWithStore(contractVersion, heartbeatTTL, NewMemoryStore())
}

func NewWithStore(contractVersion string, heartbeatTTL time.Duration, store Store) *Service {
	return NewWithStoreAndGateway(contractVersion, heartbeatTTL, store, nil)
}

func NewWithStoreAndGateway(contractVersion string, heartbeatTTL time.Duration, store Store, gateway CommandGateway) *Service {
	return NewWithStoreGatewayAndProcessor(contractVersion, heartbeatTTL, store, nil, gateway)
}

func NewWithStoreGatewayAndProcessor(contractVersion string, heartbeatTTL time.Duration, store Store, processor PayloadProcessor, gateway CommandGateway) *Service {
	return NewWithStoreGatewayProcessorAndPlanProducer(contractVersion, heartbeatTTL, store, processor, nil, gateway)
}

func NewWithStoreGatewayProcessorAndPlanProducer(contractVersion string, heartbeatTTL time.Duration, store Store, processor PayloadProcessor, planProducer IntentPlanProducer, gateway CommandGateway) *Service {
	if heartbeatTTL <= 0 {
		heartbeatTTL = 15 * time.Second
	}
	if store == nil {
		store = NewMemoryStore()
	}
	return &Service{store: store, heartbeatTTL: heartbeatTTL, leaseTTL: 30 * time.Second, contractVersion: contractVersion, now: time.Now, processor: processor, planProducer: planProducer, gateway: gateway}
}

func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *Service) SetLiveWritesEnabled(enabled bool) { s.live.Store(enabled) }

func (s *Service) SetDependencyReadinessEvaluator(evaluator DependencyReadinessEvaluator) {
	s.dependencies = evaluator
}

func (s *Service) Enqueue(ctx context.Context, req EnqueueRequest) (WorkItem, bool, error) {
	if ctx == nil || strings.TrimSpace(req.PrincipalID) == "" || strings.TrimSpace(req.IdempotencyKey) == "" || strings.TrimSpace(req.LineageID) == "" || req.ProductID != "funding" || (req.Mode != "test" && req.Mode != "live") || !validBrowserWorkKind(req.WorkKind) || !validBrowserFacts(req) || !validBrowserAccountFences(req.AccountFences) || !json.Valid(workJSON(req.Payload)) {
		return WorkItem{}, false, ErrWorkRevisionConflict
	}
	if req.Mode == "live" && !s.live.Load() {
		return WorkItem{}, false, ErrLiveWriteDisabled
	}
	return s.store.EnqueueWork(ctx, req, s.now().UTC())
}

func validBrowserFacts(req EnqueueRequest) bool {
	if len(req.Facts) == 0 || strings.TrimSpace(req.FactsCursor) == "" {
		return false
	}
	ids := make(map[string]struct{}, len(req.Facts))
	for _, fact := range req.Facts {
		if strings.TrimSpace(fact.FactID) == "" || fact.Cursor != req.FactsCursor || fact.ProductID != req.ProductID || fact.Mode != req.Mode || strings.TrimSpace(fact.Kind) == "" || !json.Valid(fact.Payload) || fact.EffectiveAt.IsZero() {
			return false
		}
		if _, duplicate := ids[fact.FactID]; duplicate {
			return false
		}
		ids[fact.FactID] = struct{}{}
	}
	return true
}

func EqualFactBatch(left, right []Fact) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]Fact(nil), left...)
	right = append([]Fact(nil), right...)
	sort.Slice(left, func(i, j int) bool { return left[i].FactID < left[j].FactID })
	sort.Slice(right, func(i, j int) bool { return right[i].FactID < right[j].FactID })
	for index := range left {
		if left[index].FactID != right[index].FactID || left[index].Cursor != right[index].Cursor || left[index].AccountID != right[index].AccountID || left[index].ProductID != right[index].ProductID || left[index].Mode != right[index].Mode || left[index].Kind != right[index].Kind || !EqualJSON(left[index].Payload, right[index].Payload) || !left[index].EffectiveAt.Equal(right[index].EffectiveAt) {
			return false
		}
	}
	return true
}

func (s *Service) GetOwnedWork(ctx context.Context, principal, mode, id string) (WorkItem, error) {
	return s.store.GetOwnedWork(ctx, principal, mode, id)
}

func (s *Service) ListOwnedWork(ctx context.Context, principal, mode, lineage string) ([]WorkItem, error) {
	return s.store.ListOwnedWork(ctx, principal, mode, lineage)
}

func validBrowserWorkKind(kind string) bool {
	switch kind {
	case "open", "close", "compensate", "reconcile", "observeSettlement":
		return true
	default:
		return false
	}
}

func validBrowserAccountFences(encoded json.RawMessage) bool {
	_, err := DecodeAccountFences(encoded)
	return err == nil
}

type AccountFence struct {
	AccountID            string `json:"accountId"`
	DesiredStateRevision int64  `json:"desiredStateRevision"`
	RuntimeGeneration    int64  `json:"runtimeGeneration"`
	CredentialVersion    int64  `json:"credentialVersion"`
}

// FundingV1AccountFenceCount is specific to the long/short Funding V1 plan.
// Other products must define their own account-fence cardinality.
const FundingV1AccountFenceCount = 2

func DecodeAccountFences(encoded json.RawMessage) ([]AccountFence, error) {
	var fences []AccountFence
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fences); err != nil || len(fences) != FundingV1AccountFenceCount {
		return nil, ErrAuthorizationStale
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, ErrAuthorizationStale
	}
	seen := make(map[string]struct{}, len(fences))
	for _, fence := range fences {
		if strings.TrimSpace(fence.AccountID) == "" || fence.DesiredStateRevision < 1 || fence.RuntimeGeneration < 1 || fence.CredentialVersion < 1 {
			return nil, ErrAuthorizationStale
		}
		if _, exists := seen[fence.AccountID]; exists {
			return nil, ErrAuthorizationStale
		}
		seen[fence.AccountID] = struct{}{}
	}
	return fences, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrAuthorizationStale
	}
	return nil
}

func (s *Service) Register(req RegistrationRequest) Registration {
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = "compat-register:" + req.WorkerID + ":" + req.InstanceID
	}
	reg, err := s.RegisterResult(req)
	if err != nil {
		return Registration{WorkerID: req.WorkerID, InstanceID: req.InstanceID, ProductID: req.ProductID, Mode: req.Mode, ContractVersion: req.ContractVersion, State: "registration_rejected"}
	}
	ready, err := s.Heartbeat(req.WorkerID, HeartbeatRequest{WorkerGeneration: reg.Generation, InstanceID: reg.InstanceID, State: "ready"})
	if err != nil {
		return Registration{WorkerID: req.WorkerID, InstanceID: req.InstanceID, ProductID: req.ProductID, Mode: req.Mode, ContractVersion: req.ContractVersion, State: "registration_rejected"}
	}
	return ready
}

func (s *Service) RegisterResult(req RegistrationRequest) (Registration, error) {
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return Registration{}, fmt.Errorf("idempotency key is required")
	}
	return s.store.RegisterWorker(context.Background(), req, s.now().UTC(), s.heartbeatTTL)
}

func (s *Service) Heartbeat(workerID string, req HeartbeatRequest) (Registration, error) {
	return s.store.HeartbeatWorker(context.Background(), workerID, req, s.now().UTC())
}

func (s *Service) Drain(workerID string, req DrainRequest) (Registration, error) {
	return s.store.DrainWorker(context.Background(), workerID, req, s.now().UTC())
}

func (s *Service) ValidateWorker(workerID string, generation int, instanceID string) error {
	reg, err := s.store.GetWorker(context.Background(), workerID)
	if err != nil {
		return err
	}
	if reg.Generation != generation || reg.InstanceID != instanceID {
		return ErrWorkerStale
	}
	return nil
}

func (s *Service) Readiness(workerID string) Readiness {
	observed := s.now().UTC()
	dependenciesReady := s.dependencyReadinessError() == nil
	reg, err := s.store.GetWorker(context.Background(), workerID)
	if err != nil {
		return Readiness{WorkerID: workerID, CanClaim: false, State: "not_registered", ObservedAt: observed, DependenciesReady: dependenciesReady}
	}
	fresh := reg.State == "ready" && !reg.LastHeartbeat.IsZero() && observed.Sub(reg.LastHeartbeat) <= s.heartbeatTTL
	canClaim := fresh && !reg.Draining && reg.ContractVersion == s.contractVersion && dependenciesReady
	activeLeases, countErr := s.store.CountActiveLeases(context.Background(), workerID, reg.Generation, observed)
	if countErr != nil {
		canClaim = false
	}
	return Readiness{WorkerID: workerID, WorkerGeneration: reg.Generation, ContractVersion: reg.ContractVersion, Mode: reg.Mode, Registered: true, Heartbeating: fresh, CanClaim: canClaim, FreshHeartbeat: fresh, Draining: reg.Draining, State: reg.State, ObservedAt: observed, LastHeartbeatAt: reg.LastHeartbeat, ActiveLeaseCount: activeLeases, DependenciesReady: dependenciesReady}
}

func (s *Service) dependencyReadinessError() error {
	if s.dependencies == nil {
		return nil
	}
	if err := s.dependencies.Check(context.Background()); err != nil {
		return fmt.Errorf("%w: %v", ErrDependencyUnavailable, err)
	}
	return nil
}

func (s *Service) AnyWorkerReady() bool {
	workerIDs, err := s.store.ListWorkerIDs(context.Background())
	if err != nil {
		return false
	}
	for _, workerID := range workerIDs {
		if s.Readiness(workerID).CanClaim {
			return true
		}
	}
	return false
}

func (s *Service) Status() map[string]any {
	ready := s.dependencyReadinessError() == nil
	return map[string]any{"ready": ready, "contractVersion": s.contractVersion, "registrationOpen": ready}
}

func (s *Service) Claim(req ClaimRequest) ([]Claim, error) {
	page, err := s.ClaimPage(req)
	if err != nil || page.WorkItem == nil || page.Lease == nil {
		return nil, err
	}
	return []Claim{{WorkItemID: page.WorkItem.WorkItemID, WorkRevision: page.WorkItem.WorkRevision, LeaseID: page.Lease.LeaseID, LeaseGeneration: page.Lease.LeaseGeneration, FactsCursor: page.WorkItem.FactsCursor, AccountFences: cloneRaw(page.WorkItem.AccountFences), Attempt: page.WorkItem.Attempt}}, nil
}

func (s *Service) ClaimPage(req ClaimRequest) (ClaimPage, error) {
	if ready := s.Readiness(req.WorkerID); !ready.CanClaim || ready.WorkerGeneration != req.WorkerGeneration {
		if !ready.DependenciesReady {
			return ClaimPage{}, ErrDependencyUnavailable
		}
		if ready.State == "not_registered" {
			return ClaimPage{}, ErrWorkerNotRegistered
		}
		if ready.Draining {
			return ClaimPage{}, ErrWorkerDraining
		}
		return ClaimPage{}, ErrWorkerNotReady
	}
	return s.store.ClaimWork(context.Background(), req, s.now().UTC(), s.leaseTTL)
}

func (s *Service) Facts() map[string]any {
	return map[string]any{"facts": []Fact{}, "nextCursor": "", "resnapshotRequired": true}
}

func (s *Service) Snapshot() map[string]any {
	return map[string]any{"facts": []Fact{}, "nextCursor": "", "resnapshotRequired": true}
}

func (s *Service) FactPage(req FactRequest, snapshot bool) (FactPage, error) {
	if req.Limit <= 0 {
		req.Limit = 100
	}
	if req.Limit > 500 {
		return FactPage{}, fmt.Errorf("fact limit must not exceed 500")
	}
	return s.store.FactPage(context.Background(), req, snapshot, s.now().UTC())
}

func (s *Service) Renew(leaseID string, req LeaseRequest) map[string]any {
	result, err := s.RenewResult(leaseID, req)
	if err != nil {
		return rejectedLease(leaseID, req.WorkItemID, err)
	}
	return result
}

func (s *Service) RenewResult(leaseID string, req LeaseRequest) (map[string]any, error) {
	lease, err := s.RenewLeaseResult(leaseID, req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"leaseId": lease.LeaseID, "workItemId": lease.WorkItemID, "leaseGeneration": lease.LeaseGeneration, "renewalCount": lease.RenewalCount, "renewed": true, "expiresAt": lease.ExpiresAt}, nil
}

func (s *Service) RenewLeaseResult(leaseID string, req LeaseRequest) (Lease, error) {
	ready := s.Readiness(req.WorkerID)
	if !ready.CanClaim || ready.WorkerGeneration != req.WorkerGeneration {
		if !ready.DependenciesReady {
			return Lease{}, ErrDependencyUnavailable
		}
		if ready.State == "not_registered" {
			return Lease{}, ErrWorkerNotRegistered
		}
		if ready.Draining {
			return Lease{}, ErrWorkerDraining
		}
		return Lease{}, ErrWorkerNotReady
	}
	return s.store.RenewLease(context.Background(), leaseID, req, s.now().UTC(), s.leaseTTL)
}
func (s *Service) Intent(leaseID string, req IntentRequest) map[string]any {
	result, err := s.IntentResult(leaseID, req)
	if err != nil {
		return rejectedLease(leaseID, req.WorkItemID, err)
	}
	return result
}
func (s *Service) Complete(leaseID string, req CompleteRequest) map[string]any {
	result, err := s.CompleteResult(leaseID, req)
	if err != nil {
		return rejectedLease(leaseID, req.WorkItemID, err)
	}
	return result
}
func (s *Service) Fail(leaseID string, req FailRequest) map[string]any {
	result, err := s.FailResult(leaseID, req)
	if err != nil {
		return rejectedLease(leaseID, req.WorkItemID, err)
	}
	return result
}

func (s *Service) IntentResult(leaseID string, req IntentRequest) (map[string]any, error) {
	ctx := context.Background()
	now := s.now().UTC()
	record, err := s.store.RecordIntent(ctx, leaseID, req, now)
	if err != nil {
		return nil, err
	}
	if record.Command.State == orders.StateAccepted && s.gateway != nil {
		state := orders.StateSubmitted
		var processErr error
		handoffOutcome := orders.HandoffNotAttempted
		if s.planProducer != nil {
			processErr = s.planProducer.Prepare(ctx, record)
		}
		var commands []orders.Command
		if processErr == nil {
			commands, processErr = s.processCommands(ctx, record.Command)
		}
		if processErr == nil {
			for index, command := range commands {
				if command.State == orders.StateAccepted {
					handoff, handoffErr := s.gateway.Handoff(ctx, command)
					handoffOutcome = handoff.Outcome
					commands[index], processErr = s.processor.RecordHandoff(ctx, command, handoff)
					if handoffErr != nil && processErr == nil {
						processErr = handoffErr
					}
				}
				if processErr != nil {
					break
				}
			}
		}
		if processErr != nil {
			state = orders.StateFailed
			if handoffOutcome == orders.HandoffAccepted || handoffOutcome == orders.HandoffOutcomeUnknown {
				state = orders.StateUncertain
			}
		}
		updated, transitionErr := s.store.TransitionIntentCommand(ctx, record.Command.CommandID, state, now)
		if transitionErr != nil {
			return nil, transitionErr
		}
		record.Command = updated
		return map[string]any{"leaseId": record.Lease.LeaseID, "workItemId": record.Lease.WorkItemID, "accepted": true, "intentKind": req.IntentKind, "command": record.Command, "commands": commands}, nil
	}
	return map[string]any{"leaseId": record.Lease.LeaseID, "workItemId": record.Lease.WorkItemID, "accepted": true, "intentKind": req.IntentKind, "command": record.Command}, nil
}

func (s *Service) processCommands(ctx context.Context, parent orders.Command) ([]orders.Command, error) {
	if s.processor == nil {
		return nil, fmt.Errorf("Platform intent payload processor is required")
	}
	commands, err := s.processor.Process(ctx, parent)
	if err != nil {
		return nil, err
	}
	for _, command := range commands {
		if command.CommandID == "" || command.CommandID == parent.CommandID || command.ProductID != parent.ProductID || command.LineageID != parent.LineageID || command.Mode != parent.Mode {
			return nil, fmt.Errorf("Platform intent produced an invalid order command")
		}
	}
	return commands, nil
}

func (s *Service) CompleteResult(leaseID string, req CompleteRequest) (map[string]any, error) {
	if validator, ok := s.store.(interface {
		ValidateCompletion(context.Context, string, CompleteRequest, time.Time) error
	}); ok {
		if err := validator.ValidateCompletion(context.Background(), leaseID, req, s.now().UTC()); err != nil {
			return nil, err
		}
	}
	lease, err := s.store.CompleteLease(context.Background(), leaseID, req, s.now().UTC())
	if err != nil {
		return nil, err
	}
	return map[string]any{"leaseId": lease.LeaseID, "workItemId": lease.WorkItemID, "completed": true, "factsCursor": req.FactsCursor}, nil
}

func (s *Service) FailResult(leaseID string, req FailRequest) (map[string]any, error) {
	lease, err := s.store.FailLease(context.Background(), leaseID, req, s.now().UTC())
	if err != nil {
		return nil, err
	}
	return map[string]any{"leaseId": lease.LeaseID, "workItemId": lease.WorkItemID, "failed": true, "errorCode": req.ErrorCode, "status": lease.Status}, nil
}

func rejectedLease(leaseID, workItemID string, err error) map[string]any {
	return map[string]any{"leaseId": leaseID, "workItemId": workItemID, "accepted": false, "error": err.Error()}
}

type MemoryStore struct {
	mu          sync.Mutex
	workers     map[string]Registration
	workItems   map[string]WorkItem
	leases      map[string]Lease
	facts       []Fact
	commands    map[string]orders.Command
	nextLease   int
	nextCommand int
	operations  map[string]operationRecord
}

func (store *MemoryStore) Readiness(context.Context) error { return nil }

func (store *MemoryStore) EnqueueWork(_ context.Context, req EnqueueRequest, now time.Time) (WorkItem, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	operationKind := "browser-enqueue:" + req.PrincipalID + ":" + req.Mode
	hash := CanonicalRequestHash(req)
	var replay WorkItem
	if ok, err := store.replayValue(operationKind, req.IdempotencyKey, hash, &replay); ok || err != nil {
		return replay, ok, err
	}
	existingBatch := []Fact{}
	for _, fact := range store.facts {
		if fact.Cursor == req.FactsCursor {
			existingBatch = append(existingBatch, fact)
		}
	}
	if len(existingBatch) > 0 && !EqualFactBatch(existingBatch, req.Facts) {
		return WorkItem{}, false, ErrIdempotencyConflict
	}
	if len(existingBatch) == 0 {
		for _, requested := range req.Facts {
			fact := requested
			for _, existing := range store.facts {
				if existing.FactID == fact.FactID {
					if CanonicalRequestHash(existing) != CanonicalRequestHash(fact) {
						return WorkItem{}, false, ErrIdempotencyConflict
					}
					fact = Fact{}
					break
				}
			}
			if fact.FactID != "" {
				fact.Payload = cloneRaw(fact.Payload)
				store.facts = append(store.facts, fact)
			}
		}
	}
	sort.Slice(store.facts, func(i, j int) bool {
		return store.facts[i].EffectiveAt.Before(store.facts[j].EffectiveAt) || (store.facts[i].EffectiveAt.Equal(store.facts[j].EffectiveAt) && store.facts[i].FactID < store.facts[j].FactID)
	})
	idHash := sha256.Sum256([]byte(req.PrincipalID + "\x00" + req.Mode + "\x00" + req.IdempotencyKey))
	item := WorkItem{WorkItemID: fmt.Sprintf("work_%x", idHash[:12]), PrincipalID: req.PrincipalID, WorkKind: req.WorkKind, LineageID: req.LineageID, WorkRevision: 1, ProductID: req.ProductID, Mode: req.Mode, Status: "open", FactsCursor: req.FactsCursor, AccountFences: cloneRaw(workJSON(req.AccountFences)), Payload: cloneRaw(workJSON(req.Payload)), MaxAttempts: 5, NotBefore: now, CreatedAt: now, UpdatedAt: now}
	store.workItems[item.WorkItemID] = item
	store.rememberValue(operationKind, req.IdempotencyKey, hash, item)
	return item, false, nil
}

func (store *MemoryStore) GetOwnedWork(_ context.Context, principal, mode, id string) (WorkItem, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	item, ok := store.workItems[id]
	if !ok || item.PrincipalID != principal || item.Mode != mode {
		return WorkItem{}, ErrWorkNotFound
	}
	return cloneWorkItem(item), nil
}

func (store *MemoryStore) ListOwnedWork(_ context.Context, principal, mode, lineage string) ([]WorkItem, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	items := []WorkItem{}
	for _, item := range store.workItems {
		if item.PrincipalID == principal && item.Mode == mode && (lineage == "" || item.LineageID == lineage) {
			items = append(items, cloneWorkItem(item))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.Before(items[j].CreatedAt) || (items[i].CreatedAt.Equal(items[j].CreatedAt) && items[i].WorkItemID < items[j].WorkItemID)
	})
	return items, nil
}

func cloneWorkItem(item WorkItem) WorkItem {
	item.AccountFences = cloneRaw(item.AccountFences)
	item.Payload = cloneRaw(item.Payload)
	item.Result = cloneRaw(item.Result)
	return item
}

func workJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

type operationRecord struct {
	hash     string
	response json.RawMessage
}

func NewMemoryStore() *MemoryStore {
	now := time.Now().UTC()
	store := &MemoryStore{workers: make(map[string]Registration), workItems: make(map[string]WorkItem), leases: make(map[string]Lease), commands: make(map[string]orders.Command), operations: make(map[string]operationRecord)}
	store.workItems["work_0001"] = WorkItem{WorkItemID: "work_0001", WorkKind: "open", LineageID: "lineage_0001", WorkRevision: 1, ProductID: "funding", Mode: "test", Status: "open", FactsCursor: "cursor_0001", AccountFences: json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1},{"accountId":"account-short","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1}]`), Payload: json.RawMessage(`{}`), MaxAttempts: 5, NotBefore: time.Unix(0, 0).UTC(), CreatedAt: now, UpdatedAt: now}
	return store
}

func NewMemoryStoreWithSnapshot(items []WorkItem, facts []Fact) *MemoryStore {
	store := &MemoryStore{workers: make(map[string]Registration), workItems: make(map[string]WorkItem), leases: make(map[string]Lease), commands: make(map[string]orders.Command), operations: make(map[string]operationRecord)}
	for _, item := range items {
		item.Payload = cloneRaw(item.Payload)
		item.AccountFences = cloneRaw(item.AccountFences)
		store.workItems[item.WorkItemID] = item
	}
	for _, fact := range facts {
		fact.Payload = cloneRaw(fact.Payload)
		store.facts = append(store.facts, fact)
	}
	return store
}

func (store *MemoryStore) RegisterWorker(_ context.Context, req RegistrationRequest, now time.Time, heartbeatTTL time.Duration) (Registration, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	hash := CanonicalRequestHash(req)
	var replay Registration
	if ok, err := store.replayValue("register", req.IdempotencyKey, hash, &replay); ok || err != nil {
		return replay, err
	}
	reg := store.workers[req.WorkerID]
	if reg.WorkerID == "" {
		reg = Registration{WorkerID: req.WorkerID, Generation: 1, RegisteredAt: now}
	}
	if reg.Mode != "" && reg.Mode != req.Mode {
		return Registration{}, ErrWorkerAlreadyActive
	}
	if reg.InstanceID != "" && req.InstanceID != "" && req.InstanceID != reg.InstanceID {
		if reg.State != "stopped" && reg.State != "stale" && !(reg.Draining && !store.hasActiveLeaseLocked(req.WorkerID, reg.Generation, now)) && now.Before(reg.HeartbeatExpiresAt) {
			return Registration{}, ErrWorkerAlreadyActive
		}
		reg.Generation++
		reg.RegisteredAt = now
	}
	reg.InstanceID, reg.ProductID, reg.Mode, reg.ContractVersion = req.InstanceID, req.ProductID, req.Mode, req.ContractVersion
	reg.LastHeartbeat, reg.HeartbeatExpiresAt, reg.Draining, reg.State = now, now.Add(heartbeatTTL), false, "starting"
	store.workers[req.WorkerID] = reg
	store.rememberValue("register", req.IdempotencyKey, hash, reg)
	return reg, nil
}

func (store *MemoryStore) HeartbeatWorker(_ context.Context, workerID string, req HeartbeatRequest, now time.Time) (Registration, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	reg, err := store.requireWorker(workerID, req.WorkerGeneration, req.InstanceID)
	if err != nil {
		return Registration{}, err
	}
	ttl := reg.HeartbeatExpiresAt.Sub(reg.LastHeartbeat)
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	reg.State, reg.LastHeartbeat, reg.LastAppliedFactsCursor = req.State, now, req.LastAppliedFactsCursor
	reg.HeartbeatExpiresAt = now.Add(ttl)
	store.workers[workerID] = reg
	return reg, nil
}

func (store *MemoryStore) DrainWorker(_ context.Context, workerID string, req DrainRequest, now time.Time) (Registration, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	hash := CanonicalRequestHash(req)
	var replay Registration
	if ok, err := store.replayValue("drain", req.IdempotencyKey, hash, &replay); ok || err != nil {
		return replay, err
	}
	reg, err := store.requireWorker(workerID, req.WorkerGeneration, req.InstanceID)
	if err != nil {
		return Registration{}, err
	}
	reg.Draining, reg.State, reg.LastHeartbeat = true, "draining", now
	store.workers[workerID] = reg
	store.rememberValue("drain", req.IdempotencyKey, hash, reg)
	return reg, nil
}

func (store *MemoryStore) GetWorker(_ context.Context, workerID string) (Registration, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	reg := store.workers[workerID]
	if reg.WorkerID == "" {
		return Registration{}, ErrWorkerNotRegistered
	}
	return reg, nil
}

func (store *MemoryStore) ListWorkerIDs(context.Context) ([]string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	ids := make([]string, 0, len(store.workers))
	for id := range store.workers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (store *MemoryStore) CountActiveLeases(_ context.Context, workerID string, generation int, now time.Time) (int, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	count := 0
	for _, lease := range store.leases {
		if lease.WorkerID == workerID && lease.WorkerGeneration == generation && lease.Status == "active" && lease.ExpiresAt.After(now) {
			count++
		}
	}
	return count, nil
}

func (store *MemoryStore) ClaimWork(_ context.Context, req ClaimRequest, now time.Time, leaseTTL time.Duration) (ClaimPage, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	hash := CanonicalRequestHash(req)
	var replay ClaimPage
	if ok, err := store.replayValue("claim", req.IdempotencyKey, hash, &replay); ok || err != nil {
		if err == nil && replay.Lease != nil && !replay.Lease.ExpiresAt.After(now) {
			return ClaimPage{}, ErrLeaseExpired
		}
		return replay, err
	}
	worker, err := store.requireWorker(req.WorkerID, req.WorkerGeneration, req.InstanceID)
	if err != nil {
		return ClaimPage{}, err
	}
	limit := req.MaxItems
	if limit <= 0 || limit > 100 {
		limit = 1
	}
	store.expireLeases(now)
	for id, item := range store.workItems {
		if item.ProductID != worker.ProductID || item.Mode != worker.Mode || item.NotBefore.After(now) || (item.Status != "open" && !(item.Status == "retry_wait" && !item.NextAttemptAt.After(now))) {
			continue
		}
		if item.ProductID == "funding" {
			if _, err := DecodeAccountFences(item.AccountFences); err != nil {
				continue
			}
		}
		active := false
		for _, lease := range store.leases {
			if lease.WorkItemID == id && lease.Status == "active" && lease.ExpiresAt.After(now) {
				active = true
				break
			}
		}
		if active {
			continue
		}
		store.nextLease++
		item.Attempt++
		item.LeaseGeneration++
		lease := Lease{LeaseID: fmt.Sprintf("lease_%04d", store.nextLease), LeaseGeneration: item.LeaseGeneration, WorkItemID: item.WorkItemID, WorkRevision: item.WorkRevision, WorkerID: req.WorkerID, WorkerGeneration: req.WorkerGeneration, FactsCursor: item.FactsCursor, Status: "active", ExpiresAt: now.Add(leaseTTL), AcquiredAt: now, CreatedAt: now, UpdatedAt: now}
		store.leases[lease.LeaseID] = lease
		item.Status, item.UpdatedAt = "leased", now
		store.workItems[id] = item
		itemCopy, leaseCopy := item, lease
		page := ClaimPage{WorkItem: &itemCopy, Lease: &leaseCopy, NextCursor: item.FactsCursor}
		store.rememberValue("claim", req.IdempotencyKey, hash, page)
		return page, nil
	}
	page := ClaimPage{NextCursor: req.AfterCursor}
	store.rememberValue("claim", req.IdempotencyKey, hash, page)
	return page, nil
}

func (store *MemoryStore) FactPage(_ context.Context, req FactRequest, snapshot bool, now time.Time) (FactPage, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	lease, err := store.requireLease(req.LeaseID, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.LeaseGeneration, now)
	if err != nil {
		return FactPage{}, err
	}
	item := store.workItems[lease.WorkItemID]
	if item.WorkRevision != req.WorkRevision {
		return FactPage{}, ErrWorkRevisionConflict
	}
	if !snapshot && req.AfterCursor != "" && req.AfterCursor != lease.FactsCursor && !store.hasCursorLocked(req.AfterCursor, item.AccountFences) {
		return FactPage{}, ErrResnapshotRequired
	}
	items := store.factsAfterLocked("", req.Limit, item.AccountFences)
	if browserOwnedWork(item) {
		items = store.exactFactsLocked(item.FactsCursor, item.AccountFences)
		if !snapshot && req.AfterCursor == item.FactsCursor {
			items = nil
		}
	} else if !snapshot {
		items = store.factsAfterLocked(req.AfterCursor, req.Limit, item.AccountFences)
	}
	nextCursor := req.AfterCursor
	if snapshot {
		nextCursor = item.FactsCursor
		if !browserOwnedWork(item) {
			nextCursor = store.lastCursorLocked()
		}
	} else if len(items) > 0 {
		nextCursor = items[len(items)-1].Cursor
	}
	return FactPage{Facts: items, NextCursor: nextCursor, ResnapshotRequired: false, AsOf: now}, nil
}
func (store *MemoryStore) RenewLease(_ context.Context, leaseID string, req LeaseRequest, now time.Time, ttl time.Duration) (Lease, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	hash := CanonicalRequestHash(req)
	var replay Lease
	if ok, err := store.replayValue("renew", req.IdempotencyKey, hash, &replay); ok || err != nil {
		return replay, err
	}
	lease, err := store.requireLease(leaseID, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.LeaseGeneration, now)
	if err != nil {
		return Lease{}, err
	}
	lease.RenewalCount++
	lease.ExpiresAt, lease.UpdatedAt = now.Add(ttl), now
	store.leases[leaseID] = lease
	store.rememberValue("renew", req.IdempotencyKey, hash, lease)
	return lease, nil
}
func (store *MemoryStore) RecordIntent(_ context.Context, leaseID string, req IntentRequest, now time.Time) (IntentRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	hash := IntentRequestHash(req)
	lease, err := store.requireLease(leaseID, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.LeaseGeneration, now)
	if err != nil {
		return IntentRecord{}, err
	}
	if req.FactsCursor != "" && req.FactsCursor != lease.FactsCursor {
		return IntentRecord{}, ErrResnapshotRequired
	}
	item := store.workItems[lease.WorkItemID]
	if _, err := DecodeAccountFences(item.AccountFences); err != nil {
		return IntentRecord{}, ErrAuthorizationStale
	}
	principalID := workPrincipalID(item)
	operationKind := "intent:" + principalID + ":" + item.Mode
	var replay IntentRecord
	replayed, err := store.replayValue(operationKind, req.IdempotencyKey, hash, &replay)
	if err != nil {
		return IntentRecord{}, err
	}
	if !EqualJSON(req.AccountFences, item.AccountFences) {
		return IntentRecord{}, ErrAuthorizationStale
	}
	if item.Mode == "live" {
		return IntentRecord{}, ErrLiveWriteDisabled
	}
	if err := validateTestIntent(req.Payload); err != nil {
		return IntentRecord{}, err
	}
	if replayed {
		replay.Lease = lease
		replay.Command = store.commands[replay.Command.CommandID]
		return replay, nil
	}
	store.nextCommand++
	command := orders.Command{
		CommandID: fmt.Sprintf("command_work_%016x", store.nextCommand), PrincipalID: principalID,
		AccountID: IntentAccountID(req.Payload, req.AccountFences), LineageID: item.LineageID, ProductID: item.ProductID,
		Mode: orders.Mode(item.Mode), IdempotencyKey: req.IdempotencyKey, Method: "POST",
		Path: "/internal/v1/funding-work/leases/" + leaseID + "/command-intents", CommandKind: req.IntentKind,
		State: orders.StateAccepted, Payload: cloneRaw(req.Payload), PayloadHash: hash, Blockers: []orders.Blocker{}, CreatedAt: now, UpdatedAt: now,
	}
	store.commands[command.CommandID] = command
	facts := make([]Fact, 0, len(store.facts))
	for _, fact := range store.facts {
		if fact.ProductID == item.ProductID && fact.Mode == item.Mode && FactAllowedByFences(fact, item.AccountFences) && (!browserOwnedWork(item) || fact.Cursor == item.FactsCursor) {
			facts = append(facts, fact)
		}
	}
	record := IntentRecord{Lease: lease, Command: command, WorkPayload: cloneRaw(item.Payload), AccountFences: cloneRaw(item.AccountFences), Facts: facts}
	store.rememberValue(operationKind, req.IdempotencyKey, hash, record)
	return record, nil
}

func browserOwnedWork(item WorkItem) bool {
	return item.PrincipalID != "" && item.PrincipalID != "platform:funding"
}

func workPrincipalID(item WorkItem) string {
	if item.PrincipalID == "" {
		return "platform:funding"
	}
	return item.PrincipalID
}

func (store *MemoryStore) exactFactsLocked(cursor string, fences json.RawMessage) []Fact {
	items := []Fact{}
	for _, fact := range store.facts {
		if fact.Cursor == cursor && FactAllowedByFences(fact, fences) {
			items = append(items, fact)
		}
	}
	return items
}

func (store *MemoryStore) TransitionIntentCommand(_ context.Context, commandID string, state orders.State, now time.Time) (orders.Command, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	command, ok := store.commands[commandID]
	if !ok {
		return orders.Command{}, orders.ErrNotFound
	}
	if command.State != orders.StateAccepted || (state != orders.StateSubmitted && state != orders.StateUncertain && state != orders.StateFailed) {
		return command, orders.ErrInvalidTransition
	}
	command.State, command.UpdatedAt = state, now
	store.commands[commandID] = command
	return command, nil
}

func IntentAccountID(payload, fences json.RawMessage) string {
	var intent struct {
		LongAccountID string `json:"longAccountId"`
		AccountID     string `json:"accountId"`
	}
	_ = json.Unmarshal(payload, &intent)
	if intent.AccountID != "" {
		return intent.AccountID
	}
	if intent.LongAccountID != "" {
		return intent.LongAccountID
	}
	var scoped []struct {
		AccountID string `json:"accountId"`
	}
	_ = json.Unmarshal(fences, &scoped)
	if len(scoped) > 0 {
		return scoped[0].AccountID
	}
	return "platform:funding"
}

func validateTestIntent(payload json.RawMessage) error {
	var intent map[string]any
	if json.Unmarshal(defaultRaw(payload), &intent) != nil {
		return ErrWorkRevisionConflict
	}
	for _, key := range []string{"notionalPerLeg", "maxCompensationNotional"} {
		value, exists := intent[key]
		if !exists {
			continue
		}
		var notional float64
		if _, err := fmt.Sscan(fmt.Sprint(value), &notional); err != nil || notional <= 0 || notional > 100 {
			return ErrWorkRevisionConflict
		}
	}
	return nil
}
func (store *MemoryStore) CompleteLease(_ context.Context, leaseID string, req CompleteRequest, now time.Time) (Lease, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	hash := CanonicalRequestHash(req)
	if replay, ok, err := store.replay("complete", req.IdempotencyKey, hash); ok || err != nil {
		return replay, err
	}
	lease, err := store.requireLease(leaseID, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.LeaseGeneration, now)
	if err != nil {
		return Lease{}, err
	}
	item := store.workItems[lease.WorkItemID]
	if req.FactsCursor != "" && req.FactsCursor != lease.FactsCursor {
		return Lease{}, ErrLeaseStale
	}
	item.Status, item.Result, item.UpdatedAt = "completed", cloneRaw(req.Result), now
	lease.Status, lease.UpdatedAt = "completed", now
	store.workItems[item.WorkItemID], store.leases[leaseID] = item, lease
	store.remember("complete", req.IdempotencyKey, hash, lease)
	return lease, nil
}

func (store *MemoryStore) ValidateCompletion(_ context.Context, leaseID string, req CompleteRequest, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, replayed, err := store.replay("complete", req.IdempotencyKey, CanonicalRequestHash(req)); replayed || err != nil {
		return err
	}
	lease, err := store.requireLease(leaseID, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.LeaseGeneration, now)
	if err != nil {
		return err
	}
	item := store.workItems[lease.WorkItemID]
	for _, command := range store.commands {
		if command.PrincipalID == workPrincipalID(item) && command.Mode == orders.Mode(item.Mode) && command.ProductID == item.ProductID && command.LineageID == item.LineageID && command.State != orders.StateFilled && command.State != orders.StateCanceled {
			return fmt.Errorf("%w: owned commands are not terminal and successful", ErrDependencyUnavailable)
		}
	}
	return nil
}
func (store *MemoryStore) FailLease(_ context.Context, leaseID string, req FailRequest, now time.Time) (Lease, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	hash := CanonicalRequestHash(req)
	if replay, ok, err := store.replay("fail", req.IdempotencyKey, hash); ok || err != nil {
		return replay, err
	}
	lease, err := store.requireLease(leaseID, req.WorkerID, req.WorkerGeneration, req.WorkItemID, req.WorkRevision, req.LeaseGeneration, now)
	if err != nil {
		return Lease{}, err
	}
	item := store.workItems[lease.WorkItemID]
	if req.FactsCursor != "" && req.FactsCursor != lease.FactsCursor {
		return Lease{}, ErrLeaseStale
	}
	item.ErrorCode, item.Retryable, item.Result, item.UpdatedAt = req.ErrorCode, req.Retryable, cloneRaw(req.Details), now
	if req.Retryable && item.Attempt < 5 {
		item.Status = "retry_wait"
		item.NextAttemptAt = now.Add(retryDelay(item.Attempt))
	} else {
		item.Status = "manual_intervention"
	}
	lease.Status, lease.UpdatedAt = item.Status, now
	store.workItems[item.WorkItemID], store.leases[leaseID] = item, lease
	store.remember("fail", req.IdempotencyKey, hash, lease)
	return lease, nil
}

func (store *MemoryStore) expireLeases(now time.Time) {
	for id, lease := range store.leases {
		if lease.Status != "active" || lease.ExpiresAt.After(now) {
			continue
		}
		lease.Status, lease.UpdatedAt = "expired", now
		store.leases[id] = lease
		item := store.workItems[lease.WorkItemID]
		if item.Attempt >= 5 {
			item.Status = "manual_intervention"
		} else {
			item.Status = "retry_wait"
			item.NextAttemptAt = now.Add(retryDelay(item.Attempt))
		}
		item.UpdatedAt = now
		store.workItems[item.WorkItemID] = item
	}
}

func (store *MemoryStore) replay(kind, key, hash string) (Lease, bool, error) {
	var lease Lease
	ok, err := store.replayValue(kind, key, hash, &lease)
	return lease, ok, err
}

func (store *MemoryStore) remember(kind, key, hash string, lease Lease) {
	store.rememberValue(kind, key, hash, lease)
}

func (store *MemoryStore) replayValue(kind, key, hash string, target any) (bool, error) {
	record, ok := store.operations[kind+"\x00"+key]
	if !ok {
		return false, nil
	}
	if record.hash != hash {
		return true, ErrIdempotencyConflict
	}
	if err := json.Unmarshal(record.response, target); err != nil {
		return true, fmt.Errorf("decode work operation: %w", err)
	}
	return true, nil
}

func (store *MemoryStore) rememberValue(kind, key, hash string, value any) {
	response, _ := json.Marshal(value)
	store.operations[kind+"\x00"+key] = operationRecord{hash: hash, response: response}
}

func (store *MemoryStore) hasActiveLeaseLocked(workerID string, generation int, now time.Time) bool {
	for _, lease := range store.leases {
		if lease.WorkerID == workerID && lease.WorkerGeneration == generation && lease.Status == "active" && lease.ExpiresAt.After(now) {
			return true
		}
	}
	return false
}

func (store *MemoryStore) hasCursorLocked(cursor string, fences json.RawMessage) bool {
	for _, fact := range store.facts {
		if fact.Cursor == cursor && FactAllowedByFences(fact, fences) {
			return true
		}
	}
	return false
}

func (store *MemoryStore) requireWorker(workerID string, generation int, instanceID string) (Registration, error) {
	reg := store.workers[workerID]
	if reg.WorkerID == "" {
		return Registration{}, ErrWorkerNotRegistered
	}
	if reg.Generation != generation || reg.InstanceID != instanceID {
		return Registration{}, ErrWorkerStale
	}
	return reg, nil
}

func (store *MemoryStore) requireLease(leaseID, workerID string, workerGeneration int, workItemID string, workRevision, leaseGeneration int, now time.Time) (Lease, error) {
	lease := store.leases[leaseID]
	if lease.LeaseID == "" || lease.WorkerID != workerID || lease.WorkerGeneration != workerGeneration || lease.WorkItemID != workItemID || lease.WorkRevision != workRevision || lease.LeaseGeneration != leaseGeneration || lease.Status != "active" || !lease.ExpiresAt.After(now) {
		return Lease{}, ErrLeaseStale
	}
	return lease, nil
}
func (store *MemoryStore) factsAfterLocked(after string, limit int, fences json.RawMessage) []Fact {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	type factBatch struct {
		cursor      string
		effectiveAt time.Time
		positionKey string
		facts       []Fact
	}
	batchesByCursor := make(map[string]*factBatch)
	for _, fact := range store.facts {
		if !FactAllowedByFences(fact, fences) {
			continue
		}
		batch := batchesByCursor[fact.Cursor]
		if batch == nil {
			batch = &factBatch{cursor: fact.Cursor, effectiveAt: fact.EffectiveAt, positionKey: fact.FactID}
			batchesByCursor[fact.Cursor] = batch
		}
		if fact.EffectiveAt.Before(batch.effectiveAt) {
			batch.effectiveAt = fact.EffectiveAt
		}
		if fact.FactID < batch.positionKey {
			batch.positionKey = fact.FactID
		}
		batch.facts = append(batch.facts, fact)
	}
	batches := make([]*factBatch, 0, len(batchesByCursor))
	for _, batch := range batchesByCursor {
		sort.Slice(batch.facts, func(i, j int) bool { return batch.facts[i].FactID < batch.facts[j].FactID })
		batches = append(batches, batch)
	}
	sort.Slice(batches, func(i, j int) bool {
		if batches[i].effectiveAt.Equal(batches[j].effectiveAt) {
			return batches[i].positionKey < batches[j].positionKey
		}
		return batches[i].effectiveAt.Before(batches[j].effectiveAt)
	})
	start := 0
	if after != "" {
		for index, batch := range batches {
			if batch.cursor == after {
				start = index + 1
				break
			}
		}
	}
	end := min(start+limit, len(batches))
	items := []Fact{}
	for _, batch := range batches[start:end] {
		items = append(items, batch.facts...)
	}
	return items
}

func FactAllowedByFences(fact Fact, fences json.RawMessage) bool {
	if fact.AccountID == "" {
		return true
	}
	var scoped []struct {
		AccountID string `json:"accountId"`
	}
	if json.Unmarshal(fences, &scoped) != nil {
		return false
	}
	for _, fence := range scoped {
		if fence.AccountID == fact.AccountID {
			return true
		}
	}
	return false
}
func (store *MemoryStore) lastCursorLocked() string {
	if len(store.facts) == 0 {
		return "cursor_0001"
	}
	type factPosition struct {
		cursor      string
		effectiveAt time.Time
		positionKey string
	}
	positions := make(map[string]factPosition)
	for _, fact := range store.facts {
		position, ok := positions[fact.Cursor]
		if !ok {
			positions[fact.Cursor] = factPosition{cursor: fact.Cursor, effectiveAt: fact.EffectiveAt, positionKey: fact.FactID}
			continue
		}
		if fact.EffectiveAt.Before(position.effectiveAt) {
			position.effectiveAt = fact.EffectiveAt
		}
		if fact.FactID < position.positionKey {
			position.positionKey = fact.FactID
		}
		positions[fact.Cursor] = position
	}
	var last factPosition
	for _, position := range positions {
		if last.cursor == "" || position.effectiveAt.After(last.effectiveAt) || (position.effectiveAt.Equal(last.effectiveAt) && position.positionKey > last.positionKey) {
			last = position
		}
	}
	return last.cursor
}
func cloneRaw(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}
func canonicalJSON(value json.RawMessage) string {
	if len(value) == 0 {
		return "{}"
	}
	return strings.TrimSpace(string(value))
}

func CanonicalRequestHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	encoded, err = canonicalJSONBytes(encoded)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func IntentRequestHash(req IntentRequest) string {
	return CanonicalRequestHash(struct {
		WorkItemID    string          `json:"workItemId"`
		WorkRevision  int             `json:"workRevision"`
		IntentKind    string          `json:"intentKind"`
		FactsCursor   string          `json:"factsCursor"`
		AccountFences json.RawMessage `json:"accountFences"`
		Payload       json.RawMessage `json:"payload"`
	}{
		WorkItemID:    req.WorkItemID,
		WorkRevision:  req.WorkRevision,
		IntentKind:    req.IntentKind,
		FactsCursor:   req.FactsCursor,
		AccountFences: req.AccountFences,
		Payload:       req.Payload,
	})
}

func EqualJSON(left, right json.RawMessage) bool {
	leftValue, leftErr := canonicalJSONBytes(defaultRaw(left))
	rightValue, rightErr := canonicalJSONBytes(defaultRaw(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	return bytes.Equal(leftValue, rightValue)
}

func canonicalJSONBytes(encoded []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	value, err := normalizeJSONNumbers(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func normalizeJSONNumbers(value any) (any, error) {
	switch current := value.(type) {
	case json.Number:
		number, err := decimal.NewFromString(current.String())
		if err != nil {
			return nil, err
		}
		return json.Number(number.String()), nil
	case []any:
		for index := range current {
			normalized, err := normalizeJSONNumbers(current[index])
			if err != nil {
				return nil, err
			}
			current[index] = normalized
		}
	case map[string]any:
		for key := range current {
			normalized, err := normalizeJSONNumbers(current[key])
			if err != nil {
				return nil, err
			}
			current[key] = normalized
		}
	}
	return value, nil
}

func defaultRaw(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`null`)
	}
	return value
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 5 {
		attempt = 5
	}
	return time.Second * time.Duration(1<<uint(attempt-1))
}
