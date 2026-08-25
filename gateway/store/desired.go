package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
)

var (
	ErrDesiredStateRevisionConflict = errors.New("gateway desired-state revision conflict")
	ErrStaleRuntimeGeneration       = errors.New("gateway stale runtime generation")
	ErrInvalidAccountTransition     = errors.New("gateway invalid account transition")
	ErrOperationConflict            = errors.New("gateway desired operation conflict")
	ErrPromotionFenceConflict       = errors.New("gateway promotion fence conflict")
	ErrCandidateNotReady            = errors.New("gateway candidate not ready")
	ErrRetiredSecretReference       = errors.New("gateway retired secret reference")
	ErrHandoffDrainBlocked          = errors.New("gateway handoff drain blocked")
	ErrMigrationPromotionConflict   = errors.New("gateway migration promotion conflict")
)

type RouteState string

const (
	RouteActive   RouteState = "active"
	RouteDisabled RouteState = "disabled"
)

type RouteDefinition struct {
	RouteID         domain.RouteID
	Product         string
	SettlementAsset string
	RouteProfileID  string
	RequestedState  RouteState
}

func (route RouteDefinition) Validate() error {
	if err := route.RouteID.Validate(); err != nil {
		return err
	}
	if route.Product != "spot" && route.Product != "perp" {
		return fmt.Errorf("route product is invalid")
	}
	if route.Product == "spot" && route.SettlementAsset != "" {
		return fmt.Errorf("spot route settlement asset is invalid")
	}
	if route.SettlementAsset != strings.ToUpper(route.SettlementAsset) || strings.ContainsAny(route.SettlementAsset, "\n\r\t ") {
		return fmt.Errorf("route settlement asset is invalid")
	}
	if err := validateOpaqueValue("route profile id", route.RouteProfileID, 256); err != nil {
		return err
	}
	if route.RequestedState != RouteActive && route.RequestedState != RouteDisabled {
		return fmt.Errorf("route requested state is invalid")
	}
	return nil
}

type DesiredSnapshot struct {
	SnapshotID        uuid.UUID
	OperationID       secretstore.OperationID
	RequestDigest     string
	AccountID         domain.AccountID
	Venue             string
	NativeEnvironment string
	SecretRef         secretstore.OpaqueRef
	Fence             domain.AccountFence
	RequestedState    DesiredAccountState
	Routes            []RouteDefinition
	CreatedAt         time.Time
}

func (snapshot DesiredSnapshot) Validate() error {
	if snapshot.SnapshotID == uuid.Nil {
		return fmt.Errorf("snapshot id is required")
	}
	if _, err := secretstore.ParseOperationID(snapshot.OperationID.String()); err != nil {
		return err
	}
	if err := snapshot.AccountID.Validate(); err != nil {
		return err
	}
	if snapshot.Fence.AccountID != snapshot.AccountID {
		return fmt.Errorf("snapshot account fence mismatch")
	}
	if err := snapshot.Fence.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("venue", snapshot.Venue, 128); err != nil {
		return err
	}
	// Live is the only environment the platform still has endpoints for; demo
	// and testnet were the other two members of this allow-list. A stored
	// snapshot naming either was written before the collapse, and it is refused
	// rather than quietly read as live: nothing here can tell whether the
	// credentials behind it were ever pointed at a real venue.
	if snapshot.NativeEnvironment != string(exchange.EnvironmentLive) {
		return fmt.Errorf("native environment is invalid")
	}
	if _, err := secretstore.ParseOpaqueRef(snapshot.SecretRef.RawForTrustedBoundary()); err != nil {
		return err
	}
	if err := snapshot.RequestedState.Validate(); err != nil {
		return err
	}
	if len(snapshot.Routes) == 0 {
		return fmt.Errorf("snapshot routes are required")
	}
	seen := make(map[domain.RouteID]struct{}, len(snapshot.Routes))
	activeRoutes := 0
	for _, route := range snapshot.Routes {
		if err := route.Validate(); err != nil {
			return err
		}
		if _, exists := seen[route.RouteID]; exists {
			return fmt.Errorf("snapshot route id is duplicated")
		}
		seen[route.RouteID] = struct{}{}
		if route.RequestedState == RouteActive {
			activeRoutes++
		}
	}
	if activeRoutes == 0 && snapshot.RequestedState != DesiredDisabled && snapshot.RequestedState != DesiredRemoved {
		return fmt.Errorf("snapshot active route is required")
	}
	return nil
}

func (snapshot DesiredSnapshot) CanonicalDigest() (string, error) {
	if err := snapshot.Validate(); err != nil {
		return "", err
	}
	routes := append([]RouteDefinition(nil), snapshot.Routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].RouteID < routes[j].RouteID })
	type canonicalRoute struct {
		RouteID         string     `json:"routeId"`
		Product         string     `json:"product"`
		SettlementAsset string     `json:"settlementAsset"`
		RouteProfileID  string     `json:"routeProfileId"`
		RequestedState  RouteState `json:"requestedState"`
	}
	canonicalRoutes := make([]canonicalRoute, 0, len(routes))
	for _, route := range routes {
		canonicalRoutes = append(canonicalRoutes, canonicalRoute{string(route.RouteID), route.Product, route.SettlementAsset, route.RouteProfileID, route.RequestedState})
	}
	payload := struct {
		AccountID         string              `json:"accountId"`
		Venue             string              `json:"venue"`
		NativeEnvironment string              `json:"nativeEnvironment"`
		SecretRef         string              `json:"secretRef"`
		Fence             domain.AccountFence `json:"fence"`
		RequestedState    DesiredAccountState `json:"requestedState"`
		Routes            []canonicalRoute    `json:"routes"`
	}{string(snapshot.AccountID), snapshot.Venue, snapshot.NativeEnvironment, snapshot.SecretRef.RawForTrustedBoundary(), snapshot.Fence, snapshot.RequestedState, canonicalRoutes}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode desired snapshot digest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

type OperationState string

const (
	OperationPrepared        OperationState = "prepared"
	OperationCandidateReady  OperationState = "candidate_ready"
	OperationCandidateFailed OperationState = "candidate_failed"
	OperationApplied         OperationState = "applied"
)

type CandidateState string

const (
	CandidateBuilding CandidateState = "building"
	CandidateReady    CandidateState = "ready"
	CandidateFailed   CandidateState = "failed"
)

type CandidateObservation struct {
	OperationID         secretstore.OperationID
	ObservationRevision uint64
	RouteID             domain.RouteID
	State               CandidateState
	ReasonCode          string
	ObservedAt          time.Time
}

type PendingSnapshot struct {
	DesiredSnapshot
	OperationState OperationState
	Observations   []CandidateObservation
}

type ActiveSnapshot struct {
	DesiredSnapshot
	PromotedAt time.Time
}

type RuntimeState struct {
	AccountID         domain.AccountID
	Active            *ActiveSnapshot
	Pending           *PendingSnapshot
	Retiring          []ActiveSnapshot
	Tombstoned        bool
	TombstoneRevision uint64
}

type PromotionRequest struct {
	AccountID           domain.AccountID
	OperationID         secretstore.OperationID
	ExpectedActiveFence domain.AccountFence
}

type PromotionResult struct {
	Active   ActiveSnapshot
	Retiring *ActiveSnapshot
}

type MigrationPromotionItem struct {
	AccountID      domain.AccountID
	OperationID    string
	RuntimeID      string
	RouteSetSHA256 string
	InputSHA256    string
}

type MigrationPromotionRequest struct {
	MigrationID string
	FenceSHA256 string
	Items       []MigrationPromotionItem
}

type MigrationPromotionDigest struct {
	PendingSHA256      string
	ActiveSHA256       string
	OperationSetSHA256 string
}

type MigrationPromotionResult struct {
	Before   MigrationPromotionDigest
	After    MigrationPromotionDigest
	Replayed bool
}

func (request MigrationPromotionRequest) OperationSetSHA256() (string, error) {
	if err := validateOpaqueValue("migration id", request.MigrationID, 128); err != nil || !validSHA256(request.FenceSHA256) {
		return "", ErrMigrationPromotionConflict
	}
	items := append([]MigrationPromotionItem(nil), request.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].AccountID < items[j].AccountID })
	seenAccounts := make(map[domain.AccountID]struct{}, len(items))
	seenOperations := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.AccountID.Validate() != nil || !validSHA256(item.RouteSetSHA256) || !validSHA256(item.InputSHA256) {
			return "", ErrMigrationPromotionConflict
		}
		operationID, err := uuid.Parse(item.OperationID)
		if err != nil || operationID == uuid.Nil {
			return "", ErrMigrationPromotionConflict
		}
		runtimeID, err := uuid.Parse(item.RuntimeID)
		if err != nil || runtimeID == uuid.Nil {
			return "", ErrMigrationPromotionConflict
		}
		if _, duplicate := seenAccounts[item.AccountID]; duplicate {
			return "", ErrMigrationPromotionConflict
		}
		if _, duplicate := seenOperations[item.OperationID]; duplicate {
			return "", ErrMigrationPromotionConflict
		}
		seenAccounts[item.AccountID] = struct{}{}
		seenOperations[item.OperationID] = struct{}{}
	}
	payload := struct {
		MigrationID string                   `json:"migrationId"`
		FenceSHA256 string                   `json:"fenceSha256"`
		Items       []MigrationPromotionItem `json:"items"`
	}{request.MigrationID, request.FenceSHA256, items}
	return canonicalSHA256(payload)
}

func MigrationRouteSetSHA256(routes []RouteDefinition) (string, error) {
	routes = append([]RouteDefinition(nil), routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].RouteID < routes[j].RouteID })
	for _, route := range routes {
		if err := route.Validate(); err != nil {
			return "", ErrMigrationPromotionConflict
		}
	}
	return canonicalSHA256(routes)
}

func canonicalSHA256(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ErrMigrationPromotionConflict
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

type RotationFinalizationRequest struct {
	OperationID           secretstore.OperationID
	AccountID             domain.AccountID
	ExpectedActiveFence   domain.AccountFence
	ExpectedRetiringFence domain.AccountFence
	Scope                 secretstore.Scope
}

func (request RotationFinalizationRequest) Validate() error {
	if _, err := secretstore.ParseOperationID(request.OperationID.String()); err != nil {
		return err
	}
	if err := request.AccountID.Validate(); err != nil {
		return err
	}
	if err := request.ExpectedActiveFence.Validate(); err != nil {
		return ErrPromotionFenceConflict
	}
	if err := request.ExpectedRetiringFence.Validate(); err != nil {
		return ErrPromotionFenceConflict
	}
	if request.ExpectedActiveFence.AccountID != request.AccountID || request.ExpectedRetiringFence.AccountID != request.AccountID {
		return ErrPromotionFenceConflict
	}
	if request.ExpectedActiveFence.RuntimeID != "" && request.ExpectedRetiringFence.RuntimeID != "" {
		if request.ExpectedActiveFence.RuntimeID == request.ExpectedRetiringFence.RuntimeID {
			return ErrPromotionFenceConflict
		}
	} else {
		// Compatibility for in-process callers that still construct the old
		// three-counter fence. Production APIs only accept RuntimeID.
		if request.ExpectedActiveFence.DesiredStateRevision != request.ExpectedRetiringFence.DesiredStateRevision+1 ||
			request.ExpectedActiveFence.RuntimeGeneration != request.ExpectedRetiringFence.RuntimeGeneration+1 ||
			request.ExpectedActiveFence.CredentialVersion != request.ExpectedRetiringFence.CredentialVersion+1 {
			return ErrPromotionFenceConflict
		}
	}
	return request.Scope.Validate()
}

func (request RotationFinalizationRequest) CanonicalDigest() (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	payload := struct {
		AccountID             domain.AccountID    `json:"accountId"`
		ExpectedActiveFence   domain.AccountFence `json:"expectedActiveFence"`
		ExpectedRetiringFence domain.AccountFence `json:"expectedRetiringFence"`
		TombstoneRevision     uint64              `json:"tombstoneRevision"`
		Namespace             string              `json:"namespace"`
		KeyVersion            string              `json:"keyVersion"`
	}{
		request.AccountID,
		request.ExpectedActiveFence,
		request.ExpectedRetiringFence,
		request.ExpectedActiveFence.DesiredStateRevision,
		request.Scope.Namespace,
		request.Scope.KeyVersion,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode rotation finalization digest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

type RotationFinalizationResult struct {
	OperationID          secretstore.OperationID
	AccountID            domain.AccountID
	ActiveFence          domain.AccountFence
	RetiringFence        domain.AccountFence
	TombstoneRevision    uint64
	RetiringState        string
	SecretReferenceState string
	CompletedAt          time.Time
}

type RetirementRequest struct {
	OperationID         secretstore.OperationID
	AccountID           domain.AccountID
	ExpectedActiveFence domain.AccountFence
	TombstoneRevision   uint64
	Scope               secretstore.Scope
}

func (request RetirementRequest) Validate() error {
	if _, err := secretstore.ParseOperationID(request.OperationID.String()); err != nil {
		return err
	}
	if err := request.AccountID.Validate(); err != nil {
		return err
	}
	if err := request.ExpectedActiveFence.Validate(); err != nil || request.ExpectedActiveFence.AccountID != request.AccountID {
		return ErrPromotionFenceConflict
	}
	return request.Scope.Validate()
}

func (request RetirementRequest) CanonicalDigest() (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	payload := struct {
		AccountID           domain.AccountID    `json:"accountId"`
		ExpectedActiveFence domain.AccountFence `json:"expectedActiveFence"`
		Namespace           string              `json:"namespace"`
		KeyVersion          string              `json:"keyVersion"`
	}{request.AccountID, request.ExpectedActiveFence, request.Scope.Namespace, request.Scope.KeyVersion}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode retirement digest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

type RetirementResult struct {
	OperationID       secretstore.OperationID
	AccountID         domain.AccountID
	RuntimeID         string
	TombstoneRevision uint64
	Tombstoned        bool
	CompletedAt       time.Time
}

type TombstoneRequest struct {
	Scope             secretstore.Scope
	SecretRef         secretstore.OpaqueRef
	AccountID         domain.AccountID
	TombstoneRevision uint64
}

type Tombstone struct {
	TombstoneRequest
	CreatedAt time.Time
}

// RuntimeSnapshotStore owns immutable candidate/active runtime truth. The
// legacy DesiredStateStore remains available during the compatibility window.
type RuntimeSnapshotStore interface {
	PreparePending(context.Context, DesiredSnapshot) (PendingSnapshot, error)
	RecordCandidateObservation(context.Context, CandidateObservation) error
	MarkCandidateReady(context.Context, secretstore.OperationID) error
	MarkCandidateFailed(context.Context, secretstore.OperationID) error
	PromotePending(context.Context, PromotionRequest) (PromotionResult, error)
	LookupRotationFinalization(context.Context, RotationFinalizationRequest) (RotationFinalizationResult, error)
	FinalizeRotation(context.Context, RotationFinalizationRequest) (RotationFinalizationResult, error)
	RetireAccount(context.Context, RetirementRequest) (RetirementResult, error)
	LoadRuntimeState(context.Context, domain.AccountID) (RuntimeState, error)
	ListRuntimeStateForRecovery(context.Context) ([]RuntimeState, error)
	TombstoneSecretReference(context.Context, TombstoneRequest) (Tombstone, error)
	IsSecretReferenceRetired(context.Context, secretstore.Scope, secretstore.OpaqueRef) (bool, error)
}

type DesiredAccountState string

const (
	DesiredCreating DesiredAccountState = "creating"
	DesiredActive   DesiredAccountState = "active"
	DesiredRotating DesiredAccountState = "rotating"
	DesiredDraining DesiredAccountState = "draining"
	DesiredDrained  DesiredAccountState = "drained"
	DesiredDisabled DesiredAccountState = "disabled"
	DesiredRemoving DesiredAccountState = "removing"
	DesiredRemoved  DesiredAccountState = "removed"
)

func (state DesiredAccountState) Validate() error {
	switch state {
	case DesiredCreating, DesiredActive, DesiredRotating, DesiredDraining,
		DesiredDrained, DesiredDisabled, DesiredRemoving, DesiredRemoved:
		return nil
	default:
		return fmt.Errorf("desired account state is invalid")
	}
}

type RotationFenceState string

const (
	RotationArmed            RotationFenceState = "armed"
	RotationReplacementReady RotationFenceState = "replacement_ready"
	RotationReleased         RotationFenceState = "released"
	RotationExpired          RotationFenceState = "expired"
	RotationBlocked          RotationFenceState = "blocked"
)

type RotationFence struct {
	ID                    string
	State                 RotationFenceState
	FromCredentialVersion int64
	ToCredentialVersion   int64
	FromRuntimeGeneration int64
	ToRuntimeGeneration   int64
	ArmedAt               time.Time
	ReleasedAt            *time.Time
	BlockedReason         string
}

func (fence RotationFence) Validate() error {
	if err := validateOpaqueValue("rotation fence id", fence.ID, 256); err != nil {
		return err
	}
	switch fence.State {
	case RotationArmed, RotationReplacementReady, RotationReleased, RotationExpired, RotationBlocked:
	default:
		return fmt.Errorf("rotation fence state is invalid")
	}
	if fence.FromCredentialVersion <= 0 || fence.ToCredentialVersion <= fence.FromCredentialVersion {
		return fmt.Errorf("rotation credential versions are invalid")
	}
	if fence.FromRuntimeGeneration <= 0 || fence.ToRuntimeGeneration <= fence.FromRuntimeGeneration {
		return fmt.Errorf("rotation runtime generations are invalid")
	}
	if fence.ArmedAt.IsZero() {
		return fmt.Errorf("rotation armed time is required")
	}
	if fence.ReleasedAt != nil && fence.ReleasedAt.IsZero() {
		return fmt.Errorf("rotation released time is invalid")
	}
	if fence.State == RotationReleased && fence.ReleasedAt == nil {
		return fmt.Errorf("released rotation time is required")
	}
	if len(fence.BlockedReason) > 128 || strings.ContainsAny(fence.BlockedReason, "\n\r\t") {
		return fmt.Errorf("rotation blocked reason is invalid")
	}
	return nil
}

type DesiredAccount struct {
	AccountID            domain.AccountID
	RuntimeID            string
	Venue                string
	VenueEnvironment     string
	SecretRef            string
	PreviousSecretRef    string
	DesiredStateRevision int64
	RuntimeGeneration    int64
	CredentialVersion    int64
	RequestedState       DesiredAccountState
	RotationFence        *RotationFence
	UpdatedAt            time.Time
}

func (desired DesiredAccount) Validate() error {
	if err := desired.AccountID.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("venue", desired.Venue, 128); err != nil {
		return err
	}
	// One environment left, for the reason the native-environment check above
	// records. A pre-collapse row naming demo or testnet fails here.
	if desired.VenueEnvironment != string(exchange.EnvironmentLive) {
		return fmt.Errorf("venue environment is invalid")
	}
	if err := validateOpaqueValue("secret reference", desired.SecretRef, 512); err != nil {
		return err
	}
	if desired.PreviousSecretRef != "" {
		if err := validateOpaqueValue("previous secret reference", desired.PreviousSecretRef, 512); err != nil {
			return err
		}
	}
	if desired.RuntimeID == "" && (desired.DesiredStateRevision <= 0 || desired.RuntimeGeneration <= 0 || desired.CredentialVersion <= 0) {
		return fmt.Errorf("runtime id is required")
	}
	if err := desired.RequestedState.Validate(); err != nil {
		return err
	}
	if desired.RotationFence != nil {
		if err := desired.RotationFence.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func ValidateDesiredAccountTransition(from, to DesiredAccountState) error {
	if err := from.Validate(); err != nil {
		return err
	}
	if err := to.Validate(); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	allowed := map[DesiredAccountState]map[DesiredAccountState]bool{
		DesiredCreating: {DesiredActive: true, DesiredDisabled: true},
		DesiredActive:   {DesiredRotating: true, DesiredDraining: true, DesiredDisabled: true},
		DesiredRotating: {DesiredActive: true, DesiredDisabled: true},
		DesiredDraining: {DesiredDrained: true},
		DesiredDrained:  {DesiredDisabled: true, DesiredRemoving: true},
		DesiredDisabled: {DesiredRemoving: true},
		DesiredRemoving: {DesiredRemoved: true},
	}
	if allowed[from][to] {
		return nil
	}
	return ErrInvalidAccountTransition
}

type RuntimeFence struct {
	AccountID            domain.AccountID
	RuntimeID            string
	DesiredStateRevision int64
	RuntimeGeneration    int64
	CredentialVersion    int64
}

func (fence RuntimeFence) Validate() error {
	if err := fence.AccountID.Validate(); err != nil {
		return err
	}
	if fence.RuntimeID == "" && (fence.DesiredStateRevision <= 0 || fence.RuntimeGeneration <= 0 || fence.CredentialVersion <= 0) {
		return fmt.Errorf("runtime id is required")
	}
	return nil
}

type DesiredStateStore interface {
	CreateDesiredAccount(context.Context, DesiredAccount) (DesiredAccount, error)
	ApplyDesiredAccount(context.Context, int64, DesiredAccount) (DesiredAccount, error)
	LoadDesiredAccount(context.Context, domain.AccountID) (DesiredAccount, error)
	ListDesiredAccounts(context.Context) ([]DesiredAccount, error)
	ListRecoverableDesiredAccounts(context.Context) ([]DesiredAccount, error)
	DeleteDesiredAccount(context.Context, domain.AccountID, int64) error
	ValidateRuntimeFence(context.Context, RuntimeFence) error
}

func validateOpaqueValue(name, value string, max int) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > max || strings.ContainsAny(value, "\n\r\t") {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}
