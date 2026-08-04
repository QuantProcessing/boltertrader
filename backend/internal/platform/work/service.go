package work

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

type RegistrationRequest struct {
	WorkerID        string `json:"workerId"`
	InstanceID      string `json:"instanceId"`
	ProductID       string `json:"productId"`
	Mode            string `json:"mode"`
	ContractVersion string `json:"contractVersion"`
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
	WorkerID        string
	InstanceID      string
	ProductID       string
	Mode            string
	ContractVersion string
	Generation      int
	LastHeartbeat   time.Time
	Draining        bool
	State           string
}

type Readiness struct {
	WorkerID         string    `json:"workerId"`
	WorkerGeneration int       `json:"workerGeneration"`
	ContractVersion  string    `json:"contractVersion"`
	CanClaim         bool      `json:"canClaim"`
	FreshHeartbeat   bool      `json:"freshHeartbeat"`
	Draining         bool      `json:"draining"`
	State            string    `json:"state"`
	ObservedAt       time.Time `json:"observedAt"`
}

type Claim struct {
	WorkItemID   string `json:"workItemId"`
	WorkRevision int    `json:"workRevision"`
	LeaseID      string `json:"leaseId"`
	FactsCursor  string `json:"factsCursor"`
}

type Service struct {
	mu               sync.Mutex
	registrations    map[string]*Registration
	claims           []Claim
	heartbeatTTL     time.Duration
	contractVersion  string
	nextLeaseCounter int
}

func New(contractVersion string, heartbeatTTL time.Duration) *Service {
	if heartbeatTTL <= 0 {
		heartbeatTTL = 15 * time.Second
	}
	return &Service{
		registrations:   make(map[string]*Registration),
		heartbeatTTL:    heartbeatTTL,
		contractVersion: contractVersion,
		claims: []Claim{
			{WorkItemID: "work_0001", WorkRevision: 1, LeaseID: "lease_0001", FactsCursor: "cursor_0001"},
		},
	}
}

func (s *Service) Register(req RegistrationRequest) Registration {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg := s.registrations[req.WorkerID]
	if reg == nil {
		reg = &Registration{WorkerID: req.WorkerID, Generation: 1}
		s.registrations[req.WorkerID] = reg
	}
	if reg.InstanceID != "" && req.InstanceID != "" && req.InstanceID != reg.InstanceID {
		reg.Generation++
	}
	reg.InstanceID = req.InstanceID
	reg.ProductID = req.ProductID
	reg.Mode = req.Mode
	reg.ContractVersion = req.ContractVersion
	reg.LastHeartbeat = time.Now().UTC()
	reg.State = "registered"
	return *reg
}

func (s *Service) Heartbeat(workerID string, req HeartbeatRequest) (Registration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg := s.registrations[workerID]
	if reg == nil {
		return Registration{}, fmt.Errorf("worker not registered")
	}
	if req.WorkerGeneration != reg.Generation || req.InstanceID != reg.InstanceID {
		return Registration{}, fmt.Errorf("stale worker generation")
	}
	reg.State = req.State
	reg.LastHeartbeat = time.Now().UTC()
	return *reg, nil
}

func (s *Service) Drain(workerID string, req DrainRequest) (Registration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg := s.registrations[workerID]
	if reg == nil {
		return Registration{}, fmt.Errorf("worker not registered")
	}
	if req.WorkerGeneration != reg.Generation || req.InstanceID != reg.InstanceID {
		return Registration{}, fmt.Errorf("stale worker generation")
	}
	reg.Draining = true
	reg.State = "draining"
	return *reg, nil
}

func (s *Service) Readiness(workerID string) Readiness {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg := s.registrations[workerID]
	if reg == nil {
		return Readiness{WorkerID: workerID, CanClaim: false, State: "not_registered", ObservedAt: time.Now().UTC()}
	}
	fresh := time.Since(reg.LastHeartbeat) <= s.heartbeatTTL
	canClaim := fresh && !reg.Draining && reg.ContractVersion == s.contractVersion
	return Readiness{
		WorkerID:         workerID,
		WorkerGeneration: reg.Generation,
		ContractVersion:  reg.ContractVersion,
		CanClaim:         canClaim,
		FreshHeartbeat:   fresh,
		Draining:         reg.Draining,
		State:            reg.State,
		ObservedAt:       time.Now().UTC(),
	}
}

func (s *Service) Status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	workers := make([]string, 0, len(s.registrations))
	for workerID := range s.registrations {
		workers = append(workers, workerID)
	}
	sort.Strings(workers)
	return map[string]any{
		"workers": workers,
		"claims":  len(s.claims),
	}
}

func (s *Service) Claim(req ClaimRequest) ([]Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg := s.registrations[req.WorkerID]
	if reg == nil {
		return nil, fmt.Errorf("worker not registered")
	}
	if req.WorkerGeneration != reg.Generation || req.InstanceID != reg.InstanceID {
		return nil, fmt.Errorf("stale worker generation")
	}
	if req.MaxItems <= 0 || req.MaxItems > len(s.claims) {
		req.MaxItems = 1
	}
	items := append([]Claim(nil), s.claims[:req.MaxItems]...)
	return items, nil
}

func (s *Service) Facts() map[string]any {
	return map[string]any{"items": []any{}, "cursor": "cursor_0001"}
}

func (s *Service) Snapshot() map[string]any {
	return map[string]any{"items": []any{}, "cursor": "cursor_0001", "snapshot": true}
}

func (s *Service) Renew(_ string, req LeaseRequest) map[string]any {
	return map[string]any{"leaseId": req.WorkItemID, "renewed": true}
}
func (s *Service) Intent(_ string, req IntentRequest) map[string]any {
	return map[string]any{"leaseId": req.WorkItemID, "accepted": true, "intentKind": req.IntentKind}
}
func (s *Service) Complete(_ string, req CompleteRequest) map[string]any {
	return map[string]any{"leaseId": req.WorkItemID, "completed": true, "factsCursor": req.FactsCursor}
}
func (s *Service) Fail(_ string, req FailRequest) map[string]any {
	return map[string]any{"leaseId": req.WorkItemID, "failed": true, "errorCode": req.ErrorCode}
}
