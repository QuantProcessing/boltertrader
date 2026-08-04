package productplatformcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

const Version = "boltertrader-product-platform-upgrade/v4"

var ErrInvalidContract = errors.New("invalid product platform contract")

type Contract struct {
	Schema              string              `json:"$schema"`
	SchemaVersion       int                 `json:"schemaVersion"`
	ContractVersion     string              `json:"contractVersion"`
	Revision            int                 `json:"revision"`
	Status              string              `json:"status"`
	Sources             []string            `json:"sources"`
	Conventions         Conventions         `json:"conventions"`
	Authority           Authority           `json:"authority"`
	SchemaIdentifiers   []string            `json:"schemaIdentifiers"`
	StableWorkerErrors  map[string]bool     `json:"stableWorkerErrors"`
	StableWorkerStatus  map[string]int      `json:"stableWorkerHTTPStatus"`
	Fencing             Fencing             `json:"fencing"`
	Listeners           []Listener          `json:"listeners"`
	StrictMutations     map[string][]string `json:"strictMutations"`
	StrictNestedPayload map[string][]string `json:"strictNestedPayloads"`
	Runtime             Runtime             `json:"runtime"`
}

type Conventions struct {
	JSONFieldCase                     string   `json:"jsonFieldCase"`
	DecimalEncoding                   string   `json:"decimalEncoding"`
	TimestampEncoding                 string   `json:"timestampEncoding"`
	Modes                             []string `json:"modes"`
	VenueEnvironments                 []string `json:"venueEnvironments"`
	TestIncludes                      []string `json:"testIncludes"`
	LivePrivateWritesEnabledByDefault bool     `json:"livePrivateWritesEnabledByDefault"`
	MutationResponsesRequireRequestID bool     `json:"mutationResponsesRequireRequestId"`
	StreamEventsRequire               []string `json:"streamEventsRequire"`
}

type Authority struct {
	BrowserReceivesGatewayCredential    bool     `json:"browserReceivesGatewayCredential"`
	BrowserReceivesSecretValue          bool     `json:"browserReceivesSecretValue"`
	WorkerReceivesGatewayCredential     bool     `json:"workerReceivesGatewayCredential"`
	WorkerReceivesDatabaseCredential    bool     `json:"workerReceivesDatabaseCredential"`
	WorkerReceivesSecretStoreCredential bool     `json:"workerReceivesSecretStoreCredential"`
	GatewayAcceptsSecretMaterial        bool     `json:"gatewayAcceptsSecretMaterial"`
	GatewayAcceptsOpaqueSecretReference bool     `json:"gatewayAcceptsOpaqueSecretReference"`
	PlatformOwns                        []string `json:"platformOwns"`
	GatewayOwns                         []string `json:"gatewayOwns"`
	WorkerOwns                          []string `json:"workerOwns"`
}

type Listener struct {
	ID                 string   `json:"id"`
	Address            string   `json:"address"`
	Namespace          string   `json:"namespace"`
	AcceptedPrincipals []string `json:"acceptedPrincipals"`
	RejectedPrincipals []string `json:"rejectedPrincipals"`
	Routes             []string `json:"routes"`
}

type Fencing struct {
	IntentValidationOrder            []string `json:"intentValidationOrder"`
	SideEffectFreeErrors             []string `json:"sideEffectFreeErrors"`
	EffectsForbiddenBeforeValidation []string `json:"effectsForbiddenBeforeValidation"`
}

type Runtime struct {
	ProcessStartOrder []string          `json:"processStartOrder"`
	ProcessStopOrder  []string          `json:"processStopOrder"`
	Ports             map[string]int    `json:"ports"`
	Worker            WorkerRuntime     `json:"worker"`
	ModeIsolation     ModeIsolation     `json:"modeIsolation"`
	Databases         []DatabaseRuntime `json:"databases"`
}

type WorkerRuntime struct {
	HeartbeatIntervalSeconds int               `json:"heartbeatIntervalSeconds"`
	HeartbeatTTLSeconds      int               `json:"heartbeatTTLSeconds"`
	LeaseDurationSeconds     int               `json:"leaseDurationSeconds"`
	MaxAttempts              int               `json:"maxAttempts"`
	RetryBackoffSeconds      []int             `json:"retryBackoffSeconds"`
	ClaimMaxItems            int               `json:"claimMaxItems"`
	TokenFiles               map[string]string `json:"tokenFiles"`
	TokenValuesMustDiffer    bool              `json:"tokenValuesMustDifferAcrossModes"`
	RequiredEnvironment      []string          `json:"requiredEnvironment"`
	ForbiddenEnvironment     []string          `json:"forbiddenEnvironment"`
}

type ModeIsolation struct {
	CrossModeReuseAllowed bool     `json:"crossModeReuseAllowed"`
	Resources             []string `json:"resources"`
}

type DatabaseRuntime struct {
	Mode      string `json:"mode"`
	Component string `json:"component"`
	Database  string `json:"database"`
	OwnerRole string `json:"ownerRole"`
}

func Decode(data []byte) (Contract, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var contract Contract
	if err := decoder.Decode(&contract); err != nil {
		return Contract{}, invalid("decode: %v", err)
	}
	if err := requireEOF(decoder); err != nil {
		return Contract{}, err
	}
	if err := contract.Validate(); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func (contract Contract) Validate() error {
	if contract.Schema != "./contract.schema.json" || contract.SchemaVersion != 1 ||
		contract.ContractVersion != Version || contract.Revision != 4 || contract.Status != "frozen" {
		return invalid("identity does not match frozen revision 4")
	}
	if err := validateConventions(contract.Conventions); err != nil {
		return err
	}
	if err := validateAuthority(contract.Authority); err != nil {
		return err
	}
	if err := requireStringSet("schema identifiers", contract.SchemaIdentifiers, requiredSchemaIdentifiers); err != nil {
		return err
	}
	if err := requireBoolMap("stable worker errors", contract.StableWorkerErrors, requiredWorkerErrors); err != nil {
		return err
	}
	if !mapsEqual(contract.StableWorkerStatus, requiredWorkerHTTPStatus) {
		return invalid("stable worker HTTP statuses do not match frozen identifiers")
	}
	if err := validateFencing(contract.Fencing); err != nil {
		return err
	}
	routes, err := validateListeners(contract.Listeners)
	if err != nil {
		return err
	}
	for route, fields := range contract.StrictMutations {
		if _, ok := routes[route]; !ok {
			return invalid("strict mutation %q is not mounted", route)
		}
		if strings.HasPrefix(route, "GET ") {
			return invalid("strict mutation %q uses GET", route)
		}
		if err := validateUniqueFields(route, fields); err != nil {
			return err
		}
		if strings.HasPrefix(route, "POST /internal/") || strings.HasPrefix(route, "PUT /internal/") {
			if slices.Contains(fields, "secretMaterial") {
				return invalid("internal mutation %q accepts secretMaterial", route)
			}
		}
	}
	for name, fields := range contract.StrictNestedPayload {
		if err := validateUniqueFields(name, fields); err != nil {
			return err
		}
	}
	return validateRuntime(contract.Runtime)
}

func (contract Contract) ValidateMutation(route string, body []byte) error {
	fields, ok := contract.StrictMutations[route]
	if !ok {
		return invalid("mutation %q has no strict allowlist", route)
	}
	return validateObjectFields(route, fields, body)
}

func (contract Contract) ValidateNested(name string, body []byte) error {
	fields, ok := contract.StrictNestedPayload[name]
	if !ok {
		return invalid("nested payload %q has no strict allowlist", name)
	}
	return validateObjectFields(name, fields, body)
}

func validateObjectFields(name string, allowed []string, body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil {
		return invalid("%s body: %v", name, err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return invalid("%s body must be an object", name)
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return invalid("%s body: %v", name, err)
		}
		field, ok := token.(string)
		if !ok {
			return invalid("%s body contains a non-string field", name)
		}
		if _, duplicate := seen[field]; duplicate {
			return invalid("%s rejects duplicate field %q", name, field)
		}
		seen[field] = struct{}{}
		if !slices.Contains(allowed, field) {
			return invalid("%s rejects unknown field %q", name, field)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return invalid("%s field %q: %v", name, field, err)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return invalid("%s body: %v", name, err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return invalid("%s body must end with an object delimiter", name)
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	return nil
}

func validateConventions(value Conventions) error {
	if value.JSONFieldCase != "camelCase" || value.DecimalEncoding != "string" ||
		value.TimestampEncoding != "RFC3339 UTC" || value.LivePrivateWritesEnabledByDefault ||
		!value.MutationResponsesRequireRequestID || !slices.Equal(value.Modes, []string{"test", "live"}) ||
		!slices.Equal(value.VenueEnvironments, []string{"demo", "testnet", "live"}) ||
		!slices.Equal(value.TestIncludes, []string{"demo", "testnet"}) ||
		!slices.Equal(value.StreamEventsRequire, []string{"eventId", "sequence", "cursor", "observedAt"}) {
		return invalid("conventions do not match the frozen encoding and Live-denial rules")
	}
	return nil
}

func validateAuthority(value Authority) error {
	if value.BrowserReceivesGatewayCredential || value.BrowserReceivesSecretValue ||
		value.WorkerReceivesGatewayCredential || value.WorkerReceivesDatabaseCredential ||
		value.WorkerReceivesSecretStoreCredential || value.GatewayAcceptsSecretMaterial ||
		!value.GatewayAcceptsOpaqueSecretReference {
		return invalid("authority grants a forbidden credential or secret capability")
	}
	return nil
}

func validateListeners(listeners []Listener) (map[string]string, error) {
	want := map[string]struct{}{"platform-browser": {}, "platform-funding-work": {}, "gateway-internal": {}}
	seen := make(map[string]struct{}, len(listeners))
	routes := make(map[string]string)
	for _, listener := range listeners {
		if _, ok := want[listener.ID]; !ok {
			return nil, invalid("unknown listener %q", listener.ID)
		}
		if _, duplicate := seen[listener.ID]; duplicate {
			return nil, invalid("duplicate listener %q", listener.ID)
		}
		seen[listener.ID] = struct{}{}
		for _, principal := range listener.AcceptedPrincipals {
			if slices.Contains(listener.RejectedPrincipals, principal) {
				return nil, invalid("listener %q both accepts and rejects %q", listener.ID, principal)
			}
		}
		for _, route := range listener.Routes {
			parts := strings.SplitN(route, " ", 2)
			if len(parts) != 2 || !strings.HasPrefix(parts[1], listener.Namespace) {
				return nil, invalid("route %q escapes listener %q namespace", route, listener.ID)
			}
			if owner, duplicate := routes[route]; duplicate {
				return nil, invalid("route %q is mounted on %q and %q", route, owner, listener.ID)
			}
			routes[route] = listener.ID
		}
	}
	if len(seen) != len(want) {
		return nil, invalid("listener set is incomplete")
	}
	return routes, nil
}

func validateFencing(fencing Fencing) error {
	wantOrder := []string{"authenticateAndScope", "workerGenerationAndReadiness", "leaseAndWorkRevision", "accountFencesAndFactsCursor", "idempotency", "liveDenialAndProductPolicy", "commandCreation", "gatewayHandoff"}
	wantErrors := []string{"WORKER_GENERATION_STALE", "WORK_LEASE_EXPIRED", "WORK_LEASE_STALE", "WORK_REVISION_CONFLICT", "WORK_AUTHORIZATION_STALE", "RESNAPSHOT_REQUIRED"}
	wantEffects := []string{"workClaimCommit", "leaseRenewal", "idempotencyCommit", "reservationMutation", "commandCreation", "gatewayHandoff"}
	if !slices.Equal(fencing.IntentValidationOrder, wantOrder) {
		return invalid("intent validation order does not fence stale authority before effects")
	}
	if err := requireStringSet("side-effect-free errors", fencing.SideEffectFreeErrors, wantErrors); err != nil {
		return err
	}
	return requireStringSet("effects forbidden before validation", fencing.EffectsForbiddenBeforeValidation, wantEffects)
}

func validateRuntime(runtime Runtime) error {
	start := []string{"postgres", "gateway", "platform", "funding-worker", "web"}
	stop := slices.Clone(start)
	slices.Reverse(stop)
	if !slices.Equal(runtime.ProcessStartOrder, start) || !slices.Equal(runtime.ProcessStopOrder, stop) {
		return invalid("process start/stop order does not match the frozen dependency order")
	}
	wantPorts := map[string]int{"postgres": 15432, "gateway": 18082, "platformBrowser": 18091, "platformFundingWork": 18092, "web": 13001}
	if !mapsEqual(runtime.Ports, wantPorts) {
		return invalid("runtime ports do not match the frozen local contract")
	}
	worker := runtime.Worker
	if worker.HeartbeatIntervalSeconds != 5 || worker.HeartbeatTTLSeconds != 15 ||
		worker.LeaseDurationSeconds != 30 || worker.MaxAttempts != 5 || worker.ClaimMaxItems != 1 ||
		!worker.TokenValuesMustDiffer || !slices.Equal(worker.RetryBackoffSeconds, []int{1, 2, 4, 8}) {
		return invalid("worker timing or retry fences do not match the frozen contract")
	}
	wantTokenFiles := map[string]string{"test": "$FT_LOCAL_RUNTIME_DIR/credentials/test/funding-worker.token", "live": "$FT_LOCAL_RUNTIME_DIR/credentials/live/funding-worker.token"}
	if !mapsEqual(worker.TokenFiles, wantTokenFiles) {
		return invalid("worker token files do not preserve mode isolation")
	}
	wantIsolation := []string{"workerTokenFile", "workerTokenValue", "workerRegistration", "factsCursor", "workItem", "lease", "database", "ownerRole", "secretStoreNamespace", "secretStoreMasterKey", "idempotencyNamespace", "auditScope"}
	if runtime.ModeIsolation.CrossModeReuseAllowed {
		return invalid("runtime permits cross-mode resource reuse")
	}
	if err := requireStringSet("mode-isolated resources", runtime.ModeIsolation.Resources, wantIsolation); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(runtime.Databases))
	for _, database := range runtime.Databases {
		key := database.Mode + "/" + database.Component
		if _, duplicate := seen[key]; duplicate || database.Database == "" || database.Database != database.OwnerRole {
			return invalid("database ownership matrix is invalid at %q", key)
		}
		seen[key] = struct{}{}
	}
	if len(seen) != 4 {
		return invalid("database ownership matrix must contain four mode/component entries")
	}
	return nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return invalid("multiple JSON values")
		}
		return invalid("trailing JSON: %v", err)
	}
	return nil
}

func validateUniqueFields(name string, fields []string) error {
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if field == "" {
			return invalid("%s contains an empty field", name)
		}
		if _, duplicate := seen[field]; duplicate {
			return invalid("%s repeats field %q", name, field)
		}
		seen[field] = struct{}{}
	}
	return nil
}

func requireStringSet(name string, got, want []string) error {
	if len(got) != len(want) {
		return invalid("%s count=%d want=%d", name, len(got), len(want))
	}
	for _, value := range want {
		if !slices.Contains(got, value) {
			return invalid("%s is missing %q", name, value)
		}
	}
	return validateUniqueFields(name, got)
}

func requireBoolMap(name string, got, want map[string]bool) error {
	if !mapsEqual(got, want) {
		return invalid("%s do not match frozen identifiers", name)
	}
	return nil
}

func mapsEqual[K comparable, V comparable](left, right map[K]V) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidContract, fmt.Sprintf(format, args...))
}

var requiredSchemaIdentifiers = []string{
	"MetaEnvelope", "ErrorEnvelope", "AccountCore", "SecretReference",
	"AccountDesiredStateCore", "AccountRuntimeCore", "RotationFenceCore", "NettingScope",
	"ReservationCore", "CommandCore", "FillCore", "FundingPaymentCore", "PnLCore",
	"RealtimeEventCore", "WorkerRegistrationCore", "AccountFenceCore", "FundingWorkItemCore",
	"WorkLeaseCore", "FundingFactPageCore", "FundingCommandIntentCore", "WorkTerminalCore",
	"WorkerReadinessCore",
}

var requiredWorkerErrors = map[string]bool{
	"WORKER_AUTH_REQUIRED": false, "WORKER_SCOPE_DENIED": false,
	"WORKER_ALREADY_ACTIVE": true, "WORKER_GENERATION_STALE": false,
	"WORKER_NOT_READY": true, "WORKER_DRAINING": false,
	"WORK_LEASE_EXPIRED": false, "WORK_LEASE_STALE": false,
	"WORK_REVISION_CONFLICT": true, "WORK_AUTHORIZATION_STALE": true,
	"RESNAPSHOT_REQUIRED": true, "WORK_ATTEMPTS_EXHAUSTED": false,
	"IDEMPOTENCY_CONFLICT": false, "LIVE_PRIVATE_WRITE_DISABLED": false,
}

var requiredWorkerHTTPStatus = map[string]int{
	"WORKER_AUTH_REQUIRED": 401, "WORKER_SCOPE_DENIED": 403,
	"WORKER_ALREADY_ACTIVE": 409, "WORKER_GENERATION_STALE": 409,
	"WORKER_NOT_READY": 503, "WORKER_DRAINING": 409,
	"WORK_LEASE_EXPIRED": 409, "WORK_LEASE_STALE": 409,
	"WORK_REVISION_CONFLICT": 409, "WORK_AUTHORIZATION_STALE": 409,
	"RESNAPSHOT_REQUIRED": 409, "WORK_ATTEMPTS_EXHAUSTED": 409,
	"IDEMPOTENCY_CONFLICT": 409, "LIVE_PRIVATE_WRITE_DISABLED": 403,
}
