package productplatform

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type authorityContract struct {
	ContractVersion string              `json:"contractVersion"`
	Revision        int                 `json:"revision"`
	StrictMutations map[string][]string `json:"strictMutations"`
	Conventions     struct {
		Modes                             []string `json:"modes"`
		LivePrivateWritesEnabledByDefault bool     `json:"livePrivateWritesEnabledByDefault"`
	} `json:"conventions"`
	Authority struct {
		BrowserReceivesGatewayCredential    bool     `json:"browserReceivesGatewayCredential"`
		BrowserReceivesSecretValue          bool     `json:"browserReceivesSecretValue"`
		WorkerReceivesGatewayCredential     bool     `json:"workerReceivesGatewayCredential"`
		WorkerReceivesDatabaseCredential    bool     `json:"workerReceivesDatabaseCredential"`
		WorkerReceivesSecretStoreCredential bool     `json:"workerReceivesSecretStoreCredential"`
		WorkerOwns                          []string `json:"workerOwns"`
	} `json:"authority"`
	StableWorkerErrors map[string]bool `json:"stableWorkerErrors"`
	Fencing            struct {
		IntentValidationOrder            []string `json:"intentValidationOrder"`
		SideEffectFreeErrors             []string `json:"sideEffectFreeErrors"`
		EffectsForbiddenBeforeValidation []string `json:"effectsForbiddenBeforeValidation"`
	} `json:"fencing"`
	Listeners []struct {
		ID                 string   `json:"id"`
		Namespace          string   `json:"namespace"`
		AcceptedPrincipals []string `json:"acceptedPrincipals"`
		RejectedPrincipals []string `json:"rejectedPrincipals"`
		Routes             []string `json:"routes"`
	} `json:"listeners"`
	Runtime struct {
		Worker struct {
			TokenFiles struct {
				Test string `json:"test"`
				Live string `json:"live"`
			} `json:"tokenFiles"`
			TokenValuesMustDifferAcrossModes bool     `json:"tokenValuesMustDifferAcrossModes"`
			RequiredEnvironment              []string `json:"requiredEnvironment"`
			ForbiddenEnvironment             []string `json:"forbiddenEnvironment"`
		} `json:"worker"`
		ModeIsolation struct {
			CrossModeReuseAllowed bool     `json:"crossModeReuseAllowed"`
			Resources             []string `json:"resources"`
		} `json:"modeIsolation"`
		Databases []struct {
			Mode      string `json:"mode"`
			Component string `json:"component"`
			Database  string `json:"database"`
			OwnerRole string `json:"ownerRole"`
		} `json:"databases"`
	} `json:"runtime"`
}

func TestV5AccountAndGatewayMutationsAreRouteAware(t *testing.T) {
	contract := readAuthorityContract(t)
	if contract.ContractVersion != "boltertrader-product-platform-upgrade/v5" || contract.Revision != 5 {
		t.Fatalf("contract=%s/r%d", contract.ContractVersion, contract.Revision)
	}
	want := map[string][]string{
		"POST /api/v1/accounts":                                                  {"venue", "displayName", "productPolicy", "routes", "credentialBundle"},
		"POST /api/v1/accounts/{accountId}/rotate":                               {"credentialBundle"},
		"PUT /internal/v1/accounts/desired/{accountId}/operations/{operationId}": {"operationId", "accountId", "venue", "nativeEnvironment", "secretRef", "desiredStateRevision", "runtimeGeneration", "credentialVersion", "requestedState", "routes"},
	}
	for route, fields := range want {
		if !reflect.DeepEqual(contract.StrictMutations[route], fields) {
			t.Errorf("%s fields=%v want=%v", route, contract.StrictMutations[route], fields)
		}
	}
}

func TestListenerCredentialsCannotCrossAuthenticate(t *testing.T) {
	contract := readAuthorityContract(t)
	want := map[string]struct {
		namespace string
		accept    []string
		reject    []string
	}{
		"platform-browser":      {"/api/v1", []string{"browser-session"}, []string{"funding-worker", "gateway-adapter"}},
		"platform-funding-work": {"/internal/v1/funding-work", []string{"funding-worker"}, []string{"browser-session", "gateway-adapter"}},
		"gateway-internal":      {"/internal/v1", []string{"gateway-adapter"}, []string{"browser-session", "funding-worker"}},
	}

	seenRoutes := make(map[string]string)
	for _, listener := range contract.Listeners {
		expected, ok := want[listener.ID]
		if !ok {
			t.Fatalf("unexpected listener %q", listener.ID)
		}
		if listener.Namespace != expected.namespace {
			t.Errorf("listener %s namespace = %q, want %q", listener.ID, listener.Namespace, expected.namespace)
		}
		assertStringSet(t, listener.ID+" accepted principals", listener.AcceptedPrincipals, expected.accept)
		assertStringSet(t, listener.ID+" rejected principals", listener.RejectedPrincipals, expected.reject)

		for _, route := range listener.Routes {
			if owner, exists := seenRoutes[route]; exists {
				t.Errorf("route %q is mounted on both %s and %s", route, owner, listener.ID)
			}
			seenRoutes[route] = listener.ID
			path := strings.SplitN(route, " ", 2)
			if len(path) != 2 || !strings.HasPrefix(path[1], listener.Namespace) {
				t.Errorf("listener %s route %q escapes namespace %q", listener.ID, route, listener.Namespace)
			}
		}
		delete(want, listener.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing listeners: %v", mapKeys(want))
	}
}

func TestWorkerHasNoGatewayDatabaseOrSecretAuthority(t *testing.T) {
	contract := readAuthorityContract(t)
	denials := map[string]bool{
		"browser gateway credential":     contract.Authority.BrowserReceivesGatewayCredential,
		"browser secret value":           contract.Authority.BrowserReceivesSecretValue,
		"worker gateway credential":      contract.Authority.WorkerReceivesGatewayCredential,
		"worker database credential":     contract.Authority.WorkerReceivesDatabaseCredential,
		"worker Secret Store credential": contract.Authority.WorkerReceivesSecretStoreCredential,
	}
	for name, granted := range denials {
		if granted {
			t.Errorf("contract grants forbidden %s", name)
		}
	}
	assertStringSet(t, "worker ownership", contract.Authority.WorkerOwns, []string{"ephemeralFundingComputation"})
	assertStringSet(t, "worker required environment", contract.Runtime.Worker.RequiredEnvironment, []string{
		"BT_PLATFORM_WORK_URL",
		"BT_FUNDING_WORKER_TOKEN_FILE",
		"BT_FUNDING_WORKER_ID",
		"BT_FUNDING_WORKER_INSTANCE_ID",
		"BT_FUNDING_WORK_CONTRACT_VERSION",
		"FT_MODE",
	})
	assertStringSet(t, "worker forbidden environment", contract.Runtime.Worker.ForbiddenEnvironment, []string{
		"BT_GATEWAY_URL",
		"BT_GATEWAY_TOKEN",
		"DATABASE_URL",
		"DB_DSN",
		"BT_SECRET_MASTER_KEY_FILE",
		"TRADING_SECRET",
		"TRADING_SECRET_REF",
	})
}

func TestTestAndLiveDatabaseAuthorityIsIsolated(t *testing.T) {
	contract := readAuthorityContract(t)
	assertStringSet(t, "modes", contract.Conventions.Modes, []string{"test", "live"})
	if contract.Conventions.LivePrivateWritesEnabledByDefault {
		t.Fatal("Live private writes must be default-denied")
	}
	if contract.Runtime.ModeIsolation.CrossModeReuseAllowed {
		t.Fatal("Test and Live authority resources must not be reusable across modes")
	}
	if !contract.Runtime.Worker.TokenValuesMustDifferAcrossModes {
		t.Fatal("Test and Live worker token values must differ")
	}
	if got, want := contract.Runtime.Worker.TokenFiles.Test, "$FT_LOCAL_RUNTIME_DIR/credentials/test/funding-worker.token"; got != want {
		t.Errorf("Test worker token file = %q, want %q", got, want)
	}
	if got, want := contract.Runtime.Worker.TokenFiles.Live, "$FT_LOCAL_RUNTIME_DIR/credentials/live/funding-worker.token"; got != want {
		t.Errorf("Live worker token file = %q, want %q", got, want)
	}
	if contract.Runtime.Worker.TokenFiles.Test == contract.Runtime.Worker.TokenFiles.Live {
		t.Fatal("Test and Live worker token files must be distinct")
	}
	assertStringSet(t, "mode-isolated authority resources", contract.Runtime.ModeIsolation.Resources, []string{
		"workerTokenFile",
		"workerTokenValue",
		"workerRegistration",
		"factsCursor",
		"workItem",
		"lease",
		"database",
		"ownerRole",
		"secretStoreNamespace",
		"secretStoreMasterKey",
		"idempotencyNamespace",
		"auditScope",
	})

	want := map[string]string{
		"test/platform": "bolter_platform_test",
		"test/gateway":  "bolter_gateway_test",
		"live/platform": "bolter_platform_live",
		"live/gateway":  "bolter_gateway_live",
	}
	seenDatabases := make(map[string]string)
	seenRoles := make(map[string]string)
	for _, database := range contract.Runtime.Databases {
		key := database.Mode + "/" + database.Component
		if database.Database != want[key] {
			t.Errorf("database %s = %q, want %q", key, database.Database, want[key])
		}
		if other, exists := seenDatabases[database.Database]; exists {
			t.Errorf("database %q is shared by %s and %s", database.Database, other, key)
		}
		if other, exists := seenRoles[database.OwnerRole]; exists {
			t.Errorf("database role %q is shared by %s and %s", database.OwnerRole, other, key)
		}
		seenDatabases[database.Database] = key
		seenRoles[database.OwnerRole] = key
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing mode/component databases: %v", mapKeys(want))
	}
}

func TestDenialErrorsHaveFrozenRetrySemantics(t *testing.T) {
	contract := readAuthorityContract(t)
	want := map[string]bool{
		"WORKER_AUTH_REQUIRED":        false,
		"WORKER_SCOPE_DENIED":         false,
		"WORKER_GENERATION_STALE":     false,
		"WORK_LEASE_EXPIRED":          false,
		"WORK_LEASE_STALE":            false,
		"WORK_REVISION_CONFLICT":      true,
		"WORK_AUTHORIZATION_STALE":    true,
		"RESNAPSHOT_REQUIRED":         true,
		"IDEMPOTENCY_CONFLICT":        false,
		"LIVE_PRIVATE_WRITE_DISABLED": false,
	}
	for code, retryable := range want {
		got, ok := contract.StableWorkerErrors[code]
		if !ok {
			t.Errorf("stable worker error %s is missing", code)
			continue
		}
		if got != retryable {
			t.Errorf("stable worker error %s retryable = %t, want %t", code, got, retryable)
		}
	}
}

func TestStaleWorkDenialsAreSideEffectFree(t *testing.T) {
	contract := readAuthorityContract(t)
	wantOrder := []string{
		"authenticateAndScope",
		"workerGenerationAndReadiness",
		"leaseAndWorkRevision",
		"accountFencesAndFactsCursor",
		"idempotency",
		"liveDenialAndProductPolicy",
		"commandCreation",
		"gatewayHandoff",
	}
	if !reflect.DeepEqual(contract.Fencing.IntentValidationOrder, wantOrder) {
		t.Errorf("intent validation order = %v, want %v", contract.Fencing.IntentValidationOrder, wantOrder)
	}
	assertStringSet(t, "side-effect-free stale errors", contract.Fencing.SideEffectFreeErrors, []string{
		"WORKER_GENERATION_STALE",
		"WORK_LEASE_EXPIRED",
		"WORK_LEASE_STALE",
		"WORK_REVISION_CONFLICT",
		"WORK_AUTHORIZATION_STALE",
		"RESNAPSHOT_REQUIRED",
	})
	assertStringSet(t, "effects forbidden before validation", contract.Fencing.EffectsForbiddenBeforeValidation, []string{
		"workClaimCommit",
		"leaseRenewal",
		"idempotencyCommit",
		"reservationMutation",
		"commandCreation",
		"gatewayHandoff",
	})
	for _, code := range contract.Fencing.SideEffectFreeErrors {
		if _, ok := contract.StableWorkerErrors[code]; !ok {
			t.Errorf("side-effect-free error %s is absent from stableWorkerErrors", code)
		}
	}
}

func TestFundingOpportunityReservationScopeKindIsOpenOnly(t *testing.T) {
	data, err := os.ReadFile("contract.schema.json")
	if err != nil {
		t.Fatalf("read product platform schema: %v", err)
	}
	var schema struct {
		Definitions struct {
			ScopeRequirement struct {
				Properties struct {
					ReservationKind struct {
						Const string   `json:"const"`
						Enum  []string `json:"enum"`
					} `json:"reservationKind"`
				} `json:"properties"`
			} `json:"fundingReservationScopeRequirement"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode product platform schema: %v", err)
	}
	kind := schema.Definitions.ScopeRequirement.Properties.ReservationKind
	if kind.Const != "open" || len(kind.Enum) != 0 {
		t.Fatalf("Funding reservation scope kind = const %q enum %v, want open-only const", kind.Const, kind.Enum)
	}
}

func readAuthorityContract(t *testing.T) authorityContract {
	t.Helper()
	path := os.Getenv("BT_PRODUCT_PLATFORM_CONTRACT")
	if path == "" {
		path = "contract.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read product platform contract %s: %v", path, err)
	}
	var contract authorityContract
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatalf("decode product platform contract %s: %v", path, err)
	}
	return contract
}

func assertStringSet(t *testing.T, name string, got, want []string) {
	t.Helper()
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func mapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
