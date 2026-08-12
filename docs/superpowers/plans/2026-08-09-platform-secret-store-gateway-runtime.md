# Platform Secret Store and Gateway Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the approved mode-isolated credential lifecycle and multi-route Gateway runtime so the authenticated BolterTrader Platform can manage accounts, share public exchange capabilities, execute product-neutral private commands, and complete bounded Test acceptance without exposing credentials or enabling Live writes.

**Architecture:** Shared root packages define strict credential bundles, trusted route profiles, and the encrypted filesystem Secret Store used through separate Platform writer and Gateway resolver facets. Platform owns durable credential operations and desired account state; Gateway persists immutable pending/active snapshots, builds complete candidate route groups, promotes them through a durable CAS after handoff leases drain, and publishes product-neutral runtime truth. Browser account management remains write-only for credential values, while Test and Live are separate process modes and all private Live writes remain default-denied.

**Tech Stack:** Go 1.26, PostgreSQL/pgx v5, AES-256-GCM, React 19, TypeScript 5.9, Vite, Vitest, Playwright, POSIX shell, OMX Ultragoal and Team.

---

## Delivery Rules

- The authoritative design is `docs/superpowers/specs/2026-08-09-platform-secret-store-gateway-runtime-design.md`.
- Never read, print, migrate, or scrape repository `.env` credential values. Provisioning accepts credential bytes only through the authenticated browser request or a non-echoing stdin command.
- Never perform a private Live venue write. Test acceptance uses only the running Platform's `Test` mode and caps every order or funding leg at 100 USDT notional.
- Keep `funding-trader` unchanged. All implementation belongs to the BolterTrader repository.
- Do not edit applied migrations `backend/migrations/0001` through `0013` or `gateway/store/postgres/migrations/001` through `002`; add new ordered migrations.
- G010 is no-trade cutover and real-stack verification. Actual Test orders belong to G011. Source retirement and irreversible cleanup require the explicit G010/G011 gates in Tasks 15 and 16.
- Each task starts RED, reaches GREEN, runs the listed regression gate, and ends with one focused Lore-style commit.

## File Responsibility Map

| Path | Responsibility |
| --- | --- |
| `credential/` | Strict versioned credential envelope, all nine venue schemas, typed decoding, bounded clearing, and redacted formatting. |
| `routeprofile/` | Trusted `(processMode, venue, routeProfileId)` endpoint authority and credential-free public connector keys. |
| `secretstore/` | Scoped writer/resolver contracts, canonical opaque references, key sources, and encrypted filesystem provider. |
| `backend/internal/platform/accounts/` | Platform account, route, credential-operation state machine, and desired-state reconciliation. |
| `backend/internal/store/postgres/` | Platform account/route/credential-operation persistence. |
| `gateway/store/` | Immutable desired snapshots, active/pending heads, tombstones, promotion CAS, and handoff persistence contracts. |
| `gateway/account/` | Candidate/active/retiring runtime groups, atomic routing snapshot, route readiness, and secret-to-client construction. |
| `gateway/publicconn/` | One public connector and coalesced refresh loop per trusted profile key. |
| `gateway/ratelimit/` | Hierarchical venue/profile, principal/account, and route budget coordination. |
| `gateway/order/` | Route-aware command routing, generation-scoped handoff leases, durable venue handoff, and crash recovery. |
| `backend/internal/app/gateway_adapter.go` | Authenticated Platform-to-Gateway desired-state client and product-neutral runtime projection. |
| `web/src/app/routes/Accounts.tsx` | Typed write-only credential forms, account routes, and active/pending route diagnostics. |
| `migration/`, `scripts/` | Reversible migration order, secure provisioning, Docker-free local lifecycle, and real-stack acceptance gates. |

## Team Execution Lanes

Use one leader plus three OMX workers after the shared Task 1 contracts are committed:

Relevant available agent roles are `executor`, `test-engineer`, `code-reviewer`, `architect`, `debugger`, `dependency-expert`, and `verifier`. Implementation uses the first two; the final Ultragoal quality gate uses distinct `code-reviewer` and `architect` roles, with `debugger`, `dependency-expert`, or `verifier` added only for a concrete blocker.

| Lane | Role | Ownership | Reasoning | Verification responsibility |
| --- | --- | --- | --- | --- |
| Leader | integrator | Plan, shared contract integration, cross-lane wiring, Ultragoal state, commits that touch both Platform and Gateway | high | Merge each green checkpoint, run boundary gates, checkpoint only verified stories. |
| Worker 1 | executor | `secretstore/`, `credential/`, `routeprofile/`, `backend/internal/platform/`, `backend/internal/store/postgres/`, `backend/migrations/` | high | Secret lifecycle crash windows, Platform race tests, PostgreSQL account operations, redaction. |
| Worker 2 | executor | `gateway/store/`, `gateway/account/`, `gateway/order/`, `gateway/publicconn/`, `gateway/ratelimit/`, `gateway/process/`, `cmd/exchange-gateway/` | high | Snapshot/promotion/handoff crash tests, race tests, restart/readiness, shared connector counters. |
| Worker 3 | test-engineer | `api/`, `internal/productplatformcontract/`, `gateway/contracts/`, `web/`, `migration/`, `scripts/`, verification docs | medium | Frozen contracts, browser unit/E2E, migration adversarial tests, real-stack evidence and cleanup gates. |

The leader sends ACK-readback handoffs at every shared contract change. Workers do not edit `<omx-state>/ultragoal`, do not access credential values, and do not issue venue writes. G010/G011/G012 remain leader-run sequential gates. Suggested launch after Task 1: `omx team 3:executor "Execute Tasks 2-14 from docs/superpowers/plans/2026-08-09-platform-secret-store-gateway-runtime.md with strict file ownership, TDD, and no venue writes"`. The third worker receives the test-engineer ownership in its inbox even though the runtime role prompt is `executor`.

### Task 1: Freeze Shared Credential, Secret, Route, and Fence Types

**Files:**
- Create: `credential/bundle.go`
- Create: `credential/registry.go`
- Create: `credential/bundle_test.go`
- Create: `routeprofile/profile.go`
- Create: `routeprofile/registry.go`
- Create: `routeprofile/registry_test.go`
- Create: `secretstore/contracts.go`
- Create: `secretstore/contracts_test.go`
- Create: `gateway/domain/fence.go`
- Create: `gateway/domain/fence_test.go`
- Modify: `gateway/config/config.go`
- Test: `gateway/config/config_test.go`

- [ ] **Step 1: Write failing contract tests for strict bundles, trusted profiles, opaque references, and full route fences**

```go
func TestRegistryRejectsUnknownCredentialFields(t *testing.T) {
	payload := []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret","extra":"forbidden"}}`)
	_, err := credential.DefaultRegistry().Decode("binance", payload)
	if !errors.Is(err, credential.ErrInvalidBundle) { t.Fatalf("err=%v", err) }
}

func TestRegistryDerivesNativeEnvironmentFromTrustedProfile(t *testing.T) {
	profile, err := routeprofile.DefaultRegistry().Resolve(routeprofile.ModeTest, "binance", "binance.perp.usdt.demo.v1")
	if err != nil { t.Fatal(err) }
	if profile.NativeEnvironment != routeprofile.EnvironmentDemo { t.Fatalf("environment=%s", profile.NativeEnvironment) }
	if _, err := routeprofile.DefaultRegistry().Resolve(routeprofile.ModeTest, "binance", "binance.perp.usdt.live.v1"); !errors.Is(err, routeprofile.ErrModeConflict) { t.Fatalf("err=%v", err) }
}

func TestOpaqueRefRejectsPathLikeValues(t *testing.T) {
	for _, raw := range []string{"../secret", "/tmp/secret", "short", strings.Repeat("a", 44)} {
		if _, err := secretstore.ParseOpaqueRef(raw); !errors.Is(err, secretstore.ErrInvalidReference) { t.Fatalf("raw=%q err=%v", raw, err) }
	}
}

func TestWriteFenceRequiresRouteID(t *testing.T) {
	fence := domain.WriteFence{AccountFence: domain.AccountFence{AccountID: "acct_1", DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1}}
	if err := fence.Validate(); err == nil { t.Fatal("missing route id accepted") }
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./credential ./routeprofile ./secretstore ./gateway/domain ./gateway/config -count=1`

Expected: FAIL because the new packages and `domain.WriteFence` do not exist.

- [ ] **Step 3: Implement the shared public types and validation contracts**

Use these exact externally visible shapes:

```go
// secretstore/contracts.go
type Mode string
const ( ModeTest Mode = "test"; ModeLive Mode = "live" )

type Scope struct { Mode Mode; Namespace string; KeyVersion string }
type OperationID string
type OpaqueRef string

type CredentialWriter interface {
	Check(context.Context, Scope) error
	Put(context.Context, Scope, OperationID, []byte) (OpaqueRef, error)
	LookupOperation(context.Context, Scope, OperationID) (OpaqueRef, error)
	Delete(context.Context, Scope, OpaqueRef) error
}
type CredentialResolver interface {
	Check(context.Context, Scope) error
	Resolve(context.Context, Scope, OpaqueRef) ([]byte, error)
}
```

`ParseOperationID` accepts only `uuid.Parse` values whose input already equals `parsed.String()`; `ParseOpaqueRef` accepts exactly 43 URL-safe base64 characters and rejects whitespace, separators, padding, and non-canonical encodings. `Scope.Validate` enforces `boltertrader/test + v1` for Test and `boltertrader/live + v1` for Live.

```go
// credential/bundle.go
type BundleV1 struct {
	SchemaVersion int             `json:"schemaVersion"`
	Venue         string          `json:"venue"`
	AuthKind      AuthKind        `json:"authKind"`
	Metadata      json.RawMessage `json:"metadata"`
	Secrets       json.RawMessage `json:"secrets"`
}

type Decoded struct {
	Venue       string
	AuthKind    AuthKind
	Credentials any
	clear       func()
}
func (decoded *Decoded) Clear() { if decoded != nil && decoded.clear != nil { decoded.clear(); decoded.clear = nil } }
```

Register exactly these venue/auth mappings and reject all other pairs:

```go
var defaultAuthKinds = map[string]AuthKind{
	"binance": AuthAPIKeySecret,
	"bybit": AuthAPIKeySecret,
	"gate": AuthAPIKeySecret,
	"okx": AuthAPIKeySecretPassphrase,
	"bitget": AuthAPIKeySecretPassphrase,
	"lighter": AuthLighterSigner,
	"hyperliquid": AuthEVMPrivateKey,
	"aster": AuthAsterAPIWallet,
	"nado": AuthNadoSigner,
}
```

The six typed variants are `APIKeySecret`, `APIKeySecretPassphrase`, `LighterSigner`, `EVMPrivateKey`, `AsterAPIWallet`, and `NadoSigner`. Every nested object is decoded with `DisallowUnknownFields`, one JSON value, duplicate-key detection before typed decode, a 32 KiB body limit, per-string 8 KiB limit, and the existing address/index validation rules from `gateway/config/config.go`. `Decoded.String`, `GoString`, and JSON formatting return only type and `redacted` markers.

```go
// routeprofile/profile.go
type Key struct { Venue string; Mode Mode; RouteProfileID string }
type Profile struct {
	ID string; Venue string; Mode Mode; NativeEnvironment NativeEnvironment
	Product Product; SettlementAsset string
}

// gateway/domain/fence.go
type AccountFence struct {
	AccountID AccountID `json:"accountId"`
	DesiredStateRevision uint64 `json:"desiredStateRevision"`
	RuntimeGeneration uint64 `json:"runtimeGeneration"`
	CredentialVersion uint64 `json:"credentialVersion"`
}
type WriteFence struct {
	AccountFence
	RouteID RouteID `json:"routeId"`
}
```

Move the current `validateProfile` table out of `gateway/config` into `routeprofile.DefaultRegistry`. Give each supported row a stable `venue.product.settlement.native-environment.v1` identity, omitting the settlement segment for empty settlement; examples are `binance.spot.demo.v1` and `binance.perp.usdt.demo.v1`. Register all current valid rows for Binance, OKX, Lighter, Hyperliquid, Bybit, Bitget, Gate, Aster, and Nado. `Resolve` keys by process mode plus venue plus ID, derives the native environment, and rejects Test/live and Live/demo-or-testnet combinations before any secret or network dependency is invoked.

- [ ] **Step 4: Run shared contract tests and the existing configuration suite**

Run: `go test ./credential ./routeprofile ./secretstore ./gateway/domain ./gateway/config -count=1`

Expected: PASS; credential formatting contains no submitted values; the route registry covers every row previously accepted by `validateProfile`.

- [ ] **Step 5: Commit the shared contracts**

```bash
git add credential routeprofile secretstore/contracts.go secretstore/contracts_test.go gateway/domain/fence.go gateway/domain/fence_test.go gateway/config/config.go gateway/config/config_test.go
git commit -m "feat: freeze credential and route runtime contracts" -m "Constraint: derive endpoints only from process mode and trusted route profiles" -m "Tested: go test ./credential ./routeprofile ./secretstore ./gateway/domain ./gateway/config -count=1"
```

### Task 2: Implement the Mode-Isolated Encrypted Filesystem Secret Store

**Files:**
- Create: `secretstore/keysource.go`
- Create: `secretstore/keysource_test.go`
- Create: `secretstore/keychain_darwin.go`
- Create: `secretstore/keychain_unsupported.go`
- Create: `secretstore/filesystem.go`
- Create: `secretstore/filesystem_test.go`
- Create: `secretstore/redaction_test.go`
- Modify: `backend/internal/platform/secret/store.go`
- Modify: `backend/internal/platform/secret/store_test.go`
- Modify: `gateway/secret/secret.go`
- Modify: `gateway/secret/secret_test.go`

- [ ] **Step 1: Write failing provider tests for crypto, permissions, replay, lookup, delete, and isolation**

```go
func TestFilesystemProviderRoundTripReplayAndConflict(t *testing.T) {
	provider := newTestProvider(t, secretstore.ModeTest)
	scope := secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"}
	op, err := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	if err != nil { t.Fatal(err) }
	ref, err := provider.Writer().Put(context.Background(), scope, op, []byte(`{"schemaVersion":1}`))
	if err != nil { t.Fatal(err) }
	replay, err := provider.Writer().Put(context.Background(), scope, op, []byte(`{"schemaVersion":1}`))
	if err != nil || replay != ref { t.Fatalf("replay=%q err=%v", replay, err) }
	if _, err := provider.Writer().Put(context.Background(), scope, op, []byte(`{"schemaVersion":2}`)); !errors.Is(err, secretstore.ErrOperationConflict) { t.Fatalf("err=%v", err) }
	resolved, err := provider.Resolver().Resolve(context.Background(), scope, ref)
	if err != nil || string(resolved) != `{"schemaVersion":1}` { t.Fatalf("resolved=%q err=%v", resolved, err) }
	clear(resolved)
}
```

Add table tests that mutate the nonce, ciphertext, AAD scope, and envelope version; use key files with `0644`, symlinks, directories, wrong owners where supported, empty/invalid 64-character hex values, and Test/Live key reuse. Assert stable codes only and assert no error contains a directory, opaque reference, key identifier, nonce, ciphertext, or plaintext fragment.

- [ ] **Step 2: Run the provider suite and verify RED**

Run: `go test ./secretstore ./backend/internal/platform/secret ./gateway/secret -count=1`

Expected: FAIL because file/keychain sources and the encrypted provider are absent and the Platform store still has the old `Put` signature.

- [ ] **Step 3: Implement key loading, deterministic references, envelopes, and facet wrappers**

Use this envelope and write sequence:

```go
type envelopeV1 struct {
	Version        int    `json:"version"`
	Nonce          string `json:"nonce"`
	Ciphertext     string `json:"ciphertext"`
	MaterialDigest string `json:"materialDigest"`
	CreatedAt      string `json:"createdAt"`
}

func deriveReference(key []byte, scope Scope, operationID OperationID) OpaqueRef {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("boltertrader-secret-ref-v1\x00" + string(scope.Mode) + "\x00" + scope.Namespace + "\x00" + scope.KeyVersion + "\x00" + string(operationID)))
	return OpaqueRef(base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

func envelopeAAD(scope Scope, ref OpaqueRef) []byte {
	return []byte("boltertrader-secret-envelope-v1\x00" + string(scope.Mode) + "\x00" + scope.Namespace + "\x00" + scope.KeyVersion + "\x00" + string(ref))
}
```

`FileKeySource.Load` accepts only an absolute, regular, current-user-owned, non-symlink `0600` file containing exactly 64 lowercase or uppercase hexadecimal characters plus optional trailing newline, decodes 32 bytes, and clears all temporary buffers. `KeychainSource` invokes macOS `security find-generic-password -w -s "$BT_SECRET_KEYCHAIN_SERVICE" -a "$BT_SECRET_KEYCHAIN_ACCOUNT"` through an injected runner; unsupported platforms return `SECRET_STORE_KEY_SOURCE_UNSUPPORTED` without fallback.

The provider stores under `{stateDir}/{mode}/objects/{sha256(ref)}.json`. It creates every directory as `0700`, files as `0600`, writes a same-directory temporary file, calls file sync, atomic rename, and directory sync. Provider bootstrap metadata records a keyed check value and rejects reuse of one master key across sibling Test/Live directories. `Put` compares the authenticated material digest on replay, `LookupOperation` derives the same reference without material, `Delete` is idempotent, and `Resolve` rejects non-canonical/cross-scope references before path access. `Writer()` returns a concrete wrapper implementing only `CredentialWriter`; `Resolver()` returns a different wrapper implementing only `CredentialResolver`.

- [ ] **Step 4: Adapt Platform and Gateway secret wrappers without exposing broader facets**

`backend/internal/platform/secret.Store` must accept `secretstore.CredentialWriter`, expose `Put(ctx, operationID, value)`, `LookupOperation`, and `Delete`, and continue returning a redacted `Reference`. Keep `gateway/secret.EnvProvider` only for operational token and DSN references; exchange credentials must use `secretstore.CredentialResolver`.

- [ ] **Step 5: Run crypto, race, and redaction tests**

Run: `go test -race ./secretstore ./backend/internal/platform/secret ./gateway/secret -count=1`

Expected: PASS with random nonce round trips, replay recovery, tamper rejection, permission rejection, deterministic operation lookup, idempotent deletion, and no secret-bearing output.

- [ ] **Step 6: Commit the provider**

```bash
git add secretstore backend/internal/platform/secret gateway/secret
git commit -m "feat: add encrypted filesystem credential store" -m "Constraint: isolate Test and Live by scope, directory, and master key" -m "Tested: go test -race ./secretstore ./backend/internal/platform/secret ./gateway/secret -count=1"
```

### Task 3: Wire Production Secret Store and Zero-Inventory Readiness

**Files:**
- Modify: `backend/internal/config/config.go`
- Modify: `backend/internal/config/config_test.go`
- Modify: `backend/internal/app/app.go`
- Modify: `backend/internal/app/app_test.go`
- Modify: `cmd/exchange-gateway/app.go`
- Modify: `cmd/exchange-gateway/app_test.go`
- Modify: `gateway/process/process.go`
- Modify: `gateway/process/process_test.go`
- Modify: `gateway/health/health.go`
- Modify: `gateway/health/health_test.go`
- Modify: `gateway/account/manager.go`
- Modify: `gateway/account/manager_test.go`
- Modify: `scripts/dev-platform`
- Modify: `scripts/dev_platform_test.go`
- Modify: `scripts/dev-platform.env.example`
- Modify: `gateway/dev-platform.test.yaml`
- Modify: `docs/guides/docker-free-local-platform.md`

- [ ] **Step 1: Write failing startup and readiness tests**

```go
func TestRuntimeReadyWithHealthyControlPlaneAndNoAccounts(t *testing.T) {
	manager := newEmptyManagerForTest(t)
	if !manager.RuntimeReady() { t.Fatal("empty inventory must be runtime-control ready") }
}

func TestRunRejectsMissingCredentialResolverBeforeListen(t *testing.T) {
	listened := false
	err := run(context.Background(), testArgs(t), runtimeDeps{listen: func(string, string) (net.Listener, error) { listened = true; return nil, nil }})
	if err == nil || listened { t.Fatalf("err=%v listened=%t", err, listened) }
}
```

Platform tests must prove `NewWithDependencies` calls provider `Check`, a nil or unhealthy writer makes control-plane readiness false, and an empty account store does not. Gateway health JSON must report `serviceReady`, `activeAccountCount`, and `readyAccountCount` separately.

- [ ] **Step 2: Run startup tests and verify RED**

Run: `go test ./backend/internal/config ./backend/internal/app ./cmd/exchange-gateway ./gateway/process ./gateway/health ./gateway/account ./scripts -count=1`

Expected: FAIL because production does not load the Secret Store and empty inventory is currently runtime-unready.

- [ ] **Step 3: Add explicit non-secret provider configuration and production wiring**

Use these environment names in both processes:

```text
BT_SECRET_KEY_SOURCE=file|keychain
BT_SECRET_STORE_DIR=$FT_LOCAL_RUNTIME_DIR/secret-store
BT_SECRET_STORE_NAMESPACE=boltertrader/test|boltertrader/live
BT_SECRET_MASTER_KEY_VERSION=v1
BT_SECRET_MASTER_KEY_FILE=$FT_LOCAL_RUNTIME_DIR/credentials/$FT_MODE/secret-store-master.key
BT_SECRET_KEYCHAIN_SERVICE=com.boltertrader.secret-store
BT_SECRET_KEYCHAIN_ACCOUNT=boltertrader-$FT_MODE
```

Configuration validates exactly one key-source branch, process-mode namespace equality, absolute paths, and no implicit fallback. Platform constructs `provider.Writer()` and passes it through `Dependencies.SecretProvider`; Gateway constructs `provider.Resolver()` and keeps `EnvProvider` only for listener tokens and PostgreSQL DSN. Both call `Check` before starting listeners, even with zero accounts.

- [ ] **Step 4: Separate control-plane readiness from account-route eligibility**

```go
type RuntimeReadiness struct {
	ServiceReady       bool `json:"serviceReady"`
	ActiveAccountCount int  `json:"activeAccountCount"`
	ReadyAccountCount  int  `json:"readyAccountCount"`
}
```

`Manager.RuntimeReady` reports manager/control-plane availability rather than `len(active)>0`; absent account and absent route requests still return stable fail-closed errors. `process.Start` restores store state before listeners and does not resolve static YAML account credentials. Keep `accounts: []` accepted during the compatibility window.

- [ ] **Step 5: Update the Docker-free launcher and documentation**

`scripts/dev-platform` must export `BT_SECRET_KEY_SOURCE=file`, `BT_SECRET_STORE_DIR=$RUNTIME_DIR/secret-store`, and `BT_SECRET_MASTER_KEY_VERSION=v1`; retain the existing per-mode `0600` key file and `0700` directories. Doctor checks permissions and cross-mode separation without hashing or printing key material. Do not add Docker commands.

- [ ] **Step 6: Run startup regression and shell syntax gates**

Run: `go test ./backend/internal/config ./backend/internal/app ./cmd/exchange-gateway ./gateway/process ./gateway/health ./gateway/account ./scripts -count=1`

Run: `sh -n scripts/dev-platform scripts/dev-platform-service scripts/test-real-platform backend/scripts/secret-bootstrap.sh`

Expected: PASS; healthy zero-account Test startup is service-ready; missing/tampered provider state prevents listener startup; shell scripts remain syntax-valid.

- [ ] **Step 7: Commit production wiring**

```bash
git add backend/internal/config backend/internal/app cmd/exchange-gateway gateway/process gateway/health gateway/account scripts gateway/dev-platform.test.yaml docs/guides/docker-free-local-platform.md
git commit -m "feat: wire scoped credential storage into platform runtime" -m "Constraint: empty account inventory is control-plane ready but cannot trade" -m "Tested: go test ./backend/internal/config ./backend/internal/app ./cmd/exchange-gateway ./gateway/process ./gateway/health ./gateway/account ./scripts -count=1"
```

### Task 4: Freeze V5 Browser, Internal Authority, and Private Command Contracts

**Files:**
- Modify: `api/product-platform/v1/contract.json`
- Modify: `api/product-platform/v1/contract.schema.json`
- Modify: `api/product-platform/v1/authority_contract_test.go`
- Modify: `internal/productplatformcontract/contract.go`
- Modify: `internal/productplatformcontract/contract_test.go`
- Modify: `internal/productplatformcontract/testdata/valid-gateway-desired.json`
- Modify: `internal/productplatformcontract/testdata/invalid-gateway-secret-material.json`
- Modify: `internal/productplatformcontract/testdata/frozen-markdown-expectations.json`
- Modify: `api/exchange-gateway/v1/internal-openapi.yaml`
- Modify: `api/exchange-gateway/v1/openapi.yaml`
- Modify: `gateway/contracts/internal_openapi_contract_test.go`
- Modify: `gateway/contracts/openapi_contract_test.go`
- Modify: `gateway/contracts/openapi_http_semantics_test.go`
- Modify: `gateway/contracts/operations_contract_test.go`
- Modify: `gateway/contracts/testdata/acceptance_crosswalk.json`

- [ ] **Step 1: Write failing frozen-contract assertions for route groups and redaction**

The tests must require:

```json
{
  "operationId": "018f47a8-2578-7d3c-9d7d-5e4f4a445c33",
  "accountId": "acct_1",
  "venue": "binance",
  "nativeEnvironment": "demo",
  "secretRef": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  "desiredStateRevision": 2,
  "runtimeGeneration": 2,
  "credentialVersion": 1,
  "requestedState": "active",
  "routes": [{
    "routeId": "perp-usdt",
    "product": "perp",
    "settlementAsset": "USDT",
    "routeProfileId": "binance.perp.usdt.demo.v1",
    "requestedState": "active"
  }]
}
```

Responses must use separate view schemas and never include `secretRef`, `previousSecretRef`, credential fields, provider paths, or Gateway authentication details. Every private place/preview/command fence must require `accountId`, `routeId`, `desiredStateRevision`, `runtimeGeneration`, and `credentialVersion`.

- [ ] **Step 2: Run contract tests and verify RED**

Run: `go test ./api/product-platform/v1 ./internal/productplatformcontract ./gateway/contracts -count=1`

Expected: FAIL because v4 accepts caller-selected `venueEnvironment`, desired-state DTOs have one handle, and public writes lack `routeId`.

- [ ] **Step 3: Freeze product contract revision 5**

Set `contractVersion` to `boltertrader-product-platform-upgrade/v5` and `revision` to `5`. Browser account creation accepts exactly `venue`, `displayName`, `productPolicy`, `routes`, and `credentialBundle`; rotation accepts exactly `credentialBundle`. Browser account responses expose `mode`, derived `nativeEnvironment`, product policy, routes, account fence, requested state, active/pending runtime summaries, and stable redacted reasons.

Replace the legacy internal mutation allowlists with one idempotent desired apply route:

```text
PUT /internal/v1/accounts/desired/{accountId}/operations/{operationId}
  body: accountId, venue, nativeEnvironment, secretRef,
        desiredStateRevision, runtimeGeneration, credentialVersion,
        requestedState, routes
```

Keep authenticated read routes for active/pending runtime truth and blocker facts. Add an idempotent retirement route whose request identifies `accountId`, `operationId`, and the expected full account fence; Gateway derives the reference to tombstone from its active/retiring snapshot instead of accepting it from browser-facing data.

- [ ] **Step 4: Freeze strict OpenAPI schemas and stable errors**

Add stable errors for `OPERATION_ID_CONFLICT`, `ROUTE_PROFILE_CONFLICT`, `CANDIDATE_NOT_READY`, `PROMOTION_FENCE_CONFLICT`, `HANDOFF_DRAIN_BLOCKED`, `RETIRED_SECRET_REFERENCE`, `PRIVATE_STREAM_GAP`, and `LIVE_WRITE_DISABLED`. OpenAPI request bodies remain bounded and reject unknown/duplicate fields. Internal desired responses use `DesiredRuntimeView`; private command schemas use `WriteFence` with required `routeId`.

- [ ] **Step 5: Run the frozen contract gate**

Run: `go test ./api/product-platform/v1 ./internal/productplatformcontract ./gateway/contracts -count=1`

Expected: PASS with revision 5, strict route arrays, sole raw-reference transport restricted to the authenticated internal apply request, and full route fences on every private command.

- [ ] **Step 6: Commit contract revision 5**

```bash
git add api/product-platform/v1 internal/productplatformcontract api/exchange-gateway/v1 gateway/contracts
git commit -m "feat: freeze route-aware platform contracts" -m "Constraint: raw secret references cross only the authenticated desired apply request" -m "Tested: go test ./api/product-platform/v1 ./internal/productplatformcontract ./gateway/contracts -count=1"
```

### Task 5: Add Platform Accounts, Routes, and Credential Operation Persistence

**Files:**
- Create: `backend/migrations/0014_account_routes_credentials.sql`
- Modify: `backend/migrations/integration_order_test.go`
- Modify: `backend/internal/platform/accounts/model.go`
- Modify: `backend/internal/platform/accounts/store.go`
- Create: `backend/internal/platform/accounts/operation.go`
- Create: `backend/internal/platform/accounts/operation_test.go`
- Modify: `backend/internal/store/postgres/account_store.go`
- Modify: `backend/internal/store/postgres/account_store_test.go`
- Modify: `backend/internal/store/postgres/account_store_integration_test.go`
- Modify: `backend/internal/store/postgres/store_contract_test.go`

- [ ] **Step 1: Write failing model and store tests for multiple routes and durable operations**

```go
func TestAccountStoreCreatesDraftAndCredentialOperationAtomically(t *testing.T) {
	account := testAccount("acct_1")
	account.SecretRef = ""
	operation := testCredentialOperation("018f47a8-2578-7d3c-9d7d-5e4f4a445c33", account.ID)
	if err := store.CreateDraft(context.Background(), account, operation); err != nil { t.Fatal(err) }
	got, err := store.GetCredentialOperation(context.Background(), operation.ID)
	if err != nil { t.Fatal(err) }
	if got.State != OperationAwaitingMaterial || got.AccountID != account.ID { t.Fatalf("operation=%+v", got) }
}

func TestAccountRejectsDuplicateRouteID(t *testing.T) {
	account := testAccount("acct_1")
	account.Routes = []AccountRoute{{ID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: RouteActive}, {ID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: RouteActive}}
	if err := account.Validate(); err == nil { t.Fatal("duplicate route accepted") }
}
```

- [ ] **Step 2: Run Platform account persistence tests and verify RED**

Run: `cd backend && go test ./internal/platform/accounts ./internal/store/postgres ./migrations -count=1`

Expected: FAIL because routes, operation rows, and migration `0014` do not exist.

- [ ] **Step 3: Add the additive Platform migration**

The migration must contain these structures and constraints; retain `product_ids` and `venue_environment` as compatibility columns until Task 14:

```sql
ALTER TABLE platform_accounts ADD COLUMN mode TEXT;
ALTER TABLE platform_accounts ADD COLUMN native_environment TEXT;
ALTER TABLE platform_accounts ADD COLUMN previous_secret_ref TEXT;
ALTER TABLE platform_accounts ALTER COLUMN secret_ref DROP NOT NULL;

UPDATE platform_accounts
SET mode = CASE WHEN venue_environment = 'live' THEN 'live' ELSE 'test' END,
    native_environment = venue_environment;

ALTER TABLE platform_accounts ALTER COLUMN mode SET NOT NULL;
ALTER TABLE platform_accounts ALTER COLUMN native_environment SET NOT NULL;
ALTER TABLE platform_accounts ADD CONSTRAINT platform_accounts_mode_check CHECK (mode IN ('test','live'));
ALTER TABLE platform_accounts ADD CONSTRAINT platform_accounts_native_environment_check CHECK (native_environment IN ('demo','testnet','live'));

CREATE TABLE platform_account_routes (
    account_id TEXT NOT NULL REFERENCES platform_accounts(account_id),
    route_id TEXT NOT NULL,
    product TEXT NOT NULL CHECK (product IN ('spot','perp')),
    settlement_asset TEXT NOT NULL DEFAULT '',
    route_profile_id TEXT NOT NULL,
    requested_state TEXT NOT NULL CHECK (requested_state IN ('active','disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (account_id, route_id)
);

CREATE TABLE platform_credential_operations (
    operation_id TEXT PRIMARY KEY,
    principal_id TEXT NOT NULL,
    account_id TEXT NOT NULL REFERENCES platform_accounts(account_id),
    mode TEXT NOT NULL CHECK (mode IN ('test','live')),
    kind TEXT NOT NULL CHECK (kind IN ('create','rotate','retire')),
    state TEXT NOT NULL CHECK (state IN ('awaiting_material','stored','applying','active','cleanup_pending','abandoned','failed')),
    idempotency_key_digest TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    target_desired_state_revision BIGINT NOT NULL CHECK (target_desired_state_revision > 0),
    target_runtime_generation BIGINT NOT NULL CHECK (target_runtime_generation > 0),
    target_credential_version BIGINT NOT NULL CHECK (target_credential_version > 0),
    secret_ref TEXT,
    stable_error_code TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (mode, principal_id, kind, idempotency_key_digest)
);

CREATE INDEX platform_credential_operations_recovery
ON platform_credential_operations (state, updated_at, operation_id)
WHERE state IN ('awaiting_material','stored','applying','cleanup_pending');
```

- [ ] **Step 4: Implement exact account and operation models**

```go
type AccountRoute struct {
	ID string `json:"routeId"`
	Product Product `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
	RouteProfileID string `json:"routeProfileId"`
	RequestedState RouteState `json:"requestedState"`
}

type CredentialOperation struct {
	ID secretstore.OperationID
	PrincipalID string
	AccountID string
	Mode secretstore.Mode
	Kind OperationKind
	State OperationState
	IdempotencyKeyDigest string
	RequestDigest string
	TargetDesiredStateRevision int64
	TargetRuntimeGeneration int64
	TargetCredentialVersion int64
	SecretRef string
	StableErrorCode string
	CreatedAt time.Time
	UpdatedAt time.Time
}
```

`Account` gains process `Mode`, derived `NativeEnvironment`, `PreviousSecretRef`, `ProductPolicy`, and ordered `Routes`. Public projection omits both secret references and credential operation digests. Validation requires non-empty unique route IDs, one trusted profile per route, uppercase settlement assets, and at least one active route for creating/active/rotating accounts.

- [ ] **Step 5: Extend memory and PostgreSQL stores with atomic operation methods**

```go
type Store interface {
	CreateDraft(context.Context, Account, CredentialOperation) error
	Get(context.Context, string) (Account, error)
	List(context.Context) ([]Account, error)
	GetCredentialOperation(context.Context, secretstore.OperationID) (CredentialOperation, error)
	FindCredentialOperation(context.Context, OperationLookup) (CredentialOperation, error)
	BindCredentialReference(context.Context, secretstore.OperationID, string) (Account, CredentialOperation, error)
	CompareAndSwapAccountAndOperation(context.Context, TransitionRequest) (Account, CredentialOperation, error)
	ListRecoverableCredentialOperations(context.Context, int) ([]CredentialOperation, error)
}
```

All account plus operation transitions use one PostgreSQL transaction with row locks. Sort routes by `route_id` on reads and writes so request digests and API output are deterministic.

- [ ] **Step 6: Run migration, unit, and real PostgreSQL contract tests**

Run: `cd backend && go test ./internal/platform/accounts ./internal/store/postgres ./migrations -count=1`

Run: `cd backend && go test ./internal/store/postgres -run 'TestAccountStore.*Integration' -count=1`

Expected: PASS when the configured test PostgreSQL is available; otherwise the integration test reports its existing explicit skip while all offline store contracts pass. Migration order ends at `0014_account_routes_credentials.sql`.

- [ ] **Step 7: Commit Platform persistence**

```bash
git add backend/migrations/0014_account_routes_credentials.sql backend/migrations/integration_order_test.go backend/internal/platform/accounts backend/internal/store/postgres
git commit -m "feat: persist platform account routes and credential operations" -m "Constraint: account drafts and operation journals commit atomically without secret values" -m "Tested: cd backend && go test ./internal/platform/accounts ./internal/store/postgres ./migrations -count=1"
```

### Task 6: Implement Recoverable Platform Credential Lifecycle and HTTP Idempotency

**Files:**
- Modify: `backend/internal/platform/accounts/adapters.go`
- Modify: `backend/internal/platform/accounts/service.go`
- Modify: `backend/internal/platform/accounts/service_test.go`
- Create: `backend/internal/platform/accounts/reconciler.go`
- Create: `backend/internal/platform/accounts/reconciler_test.go`
- Modify: `backend/internal/httpapi/accounts.go`
- Modify: `backend/internal/httpapi/accounts_test.go`
- Modify: `backend/internal/app/app.go`
- Modify: `backend/internal/app/app_test.go`

- [ ] **Step 1: Write failing crash-window and idempotency tests**

Cover these exact interruptions with injectable hooks: after draft commit, after provider `Put`, after reference binding, after Gateway apply, after Gateway promotion observation, after Gateway tombstone, and after provider delete. The lost-response test must replay the same idempotency key and body to the same account/operation; a changed body must fail before provider or Gateway calls.

```go
func TestCreateRecoversReferenceAfterPutResponseWasLost(t *testing.T) {
	service, writer, gateway := newLifecycleHarness(t)
	service.Hooks.AfterProviderPut = func() error { return errCrash }
	request := validCreateRequest()
	_, err := service.Create(context.Background(), request, "principal_1", "create-1")
	if !errors.Is(err, errCrash) { t.Fatalf("err=%v", err) }
	service.Hooks.AfterProviderPut = nil
	account, err := service.Create(context.Background(), request, "principal_1", "create-1")
	if err != nil { t.Fatal(err) }
	if writer.PutCalls != 1 || writer.LookupCalls != 1 || gateway.ApplyCalls != 1 { t.Fatalf("writer=%+v gateway=%+v", writer, gateway) }
	if account.State != StateActive { t.Fatalf("state=%s", account.State) }
}
```

- [ ] **Step 2: Run lifecycle tests and verify RED**

Run: `cd backend && go test ./internal/platform/accounts ./internal/httpapi ./internal/app -run 'Test(Create|Rotate|Remove|Credential|Account)' -count=1`

Expected: FAIL because account methods still write secrets before durable operation state and HTTP discards the idempotency key.

- [ ] **Step 3: Implement the durable create/rotate/retire state machine**

Use one `GatewayDesiredClient` dependency:

```go
type GatewayDesiredClient interface {
	ApplyDesired(context.Context, DesiredApply) (GatewayRuntimeTruth, error)
	GetRuntimeTruth(context.Context, string) (GatewayRuntimeTruth, error)
	RetireAccount(context.Context, RetireRequest) (GatewayRetirement, error)
}
```

Creation performs: normalize and validate route/profile plus bundle; create inactive draft and `awaiting_material` operation; call idempotent writer `Put`; bind ref and mark `stored`; send desired snapshot and mark `applying`; observe exact active fence; CAS account and operation to `active`. Rotation retains `PreviousSecretRef` until the new generation is active and its old reference is tombstoned. Removal combines Platform reservations with Gateway blockers, records `retire`, waits for Gateway tombstone/lease drain, calls idempotent writer `Delete`, then commits the terminal removed tombstone.

`Reconciler.RunOnce` scans bounded recoverable operations ordered by creation time. For `awaiting_material`, it calls `LookupOperation`; for `stored/applying`, it reapplies the same desired operation and observes runtime truth; for `cleanup_pending`, it repeats tombstone observation and provider deletion. Stable error codes are persisted; raw provider/Gateway errors are not.

- [ ] **Step 4: Pass browser idempotency keys and accept typed credential bundles**

```go
type createAccountRequest struct {
	Venue string `json:"venue"`
	DisplayName string `json:"displayName"`
	ProductPolicy []string `json:"productPolicy"`
	Routes []platformaccounts.AccountRoute `json:"routes"`
	CredentialBundle json.RawMessage `json:"credentialBundle"`
}
```

The handler passes the trimmed `Idempotency-Key` to service create/rotate/remove methods, clears `CredentialBundle` immediately after the service call, and never includes it in validation messages, activity records, or audit metadata. Mode and native environment are never accepted from the browser.

- [ ] **Step 5: Run crash-window, HTTP, race, and redaction tests**

Run: `cd backend && go test -race ./internal/platform/accounts ./internal/httpapi ./internal/app -count=1`

Expected: PASS for same-request replay, conflicting replay rejection, operation lookup recovery, rotation cleanup, tombstone-first deletion, and response/log redaction.

- [ ] **Step 6: Commit the Platform lifecycle**

```bash
git add backend/internal/platform/accounts backend/internal/httpapi/accounts.go backend/internal/httpapi/accounts_test.go backend/internal/app
git commit -m "feat: make credential lifecycle recoverable" -m "Constraint: durable operation state precedes provider and gateway side effects" -m "Tested: cd backend && go test -race ./internal/platform/accounts ./internal/httpapi ./internal/app -count=1"
```

### Task 7: Persist Immutable Gateway Pending and Active Runtime Snapshots

**Files:**
- Create: `gateway/store/postgres/migrations/003_runtime_snapshots.sql`
- Modify: `gateway/store/desired.go`
- Modify: `gateway/store/desired_contract_test.go`
- Modify: `gateway/store/postgres/desired.go`
- Modify: `gateway/store/postgres/desired_test.go`
- Modify: `gateway/store/postgres/migration_contract_test.go`
- Modify: `gateway/store/postgres/postgres_test.go`
- Create: `gateway/store/postgres/runtime_snapshot_crash_test.go`

- [ ] **Step 1: Write failing store tests for immutable pending state and promotion CAS**

```go
func TestPreparePendingReplayAndConflict(t *testing.T) {
	store := newDesiredStore(t)
	pending := testPendingSnapshot("018f47a8-2578-7d3c-9d7d-5e4f4a445c33", 2)
	first, err := store.PreparePending(context.Background(), pending)
	if err != nil { t.Fatal(err) }
	replay, err := store.PreparePending(context.Background(), pending)
	if err != nil || replay.SnapshotID != first.SnapshotID { t.Fatalf("replay=%+v err=%v", replay, err) }
	changed := pending; changed.Routes[0].RouteProfileID = "binance.perp.usdt.live.v1"
	if _, err := store.PreparePending(context.Background(), changed); !errors.Is(err, storepkg.ErrOperationConflict) { t.Fatalf("err=%v", err) }
}

func TestPromotionCASLeavesActiveUnchangedOnConflict(t *testing.T) {
	store := newDesiredStore(t)
	active, pending := seedActiveAndPending(t, store)
	_, err := store.PromotePending(context.Background(), storepkg.PromotionRequest{AccountID: active.AccountID, OperationID: pending.OperationID, ExpectedActiveFence: differentFence(active.Fence)})
	if !errors.Is(err, storepkg.ErrPromotionFenceConflict) { t.Fatalf("err=%v", err) }
	assertActiveSnapshot(t, store, active.SnapshotID)
}
```

- [ ] **Step 2: Run store tests and verify RED**

Run: `go test ./gateway/store ./gateway/store/postgres -run 'Test(PreparePending|Promotion|RuntimeSnapshot|Migration)' -count=1`

Expected: FAIL because the store has only one mutable desired row and two migrations.

- [ ] **Step 3: Add migration 003 without changing prior checksums**

```sql
CREATE TABLE gateway_runtime_snapshots (
    snapshot_id UUID PRIMARY KEY,
    account_id TEXT NOT NULL,
    operation_id UUID NOT NULL,
    request_digest TEXT NOT NULL,
    venue TEXT NOT NULL,
    native_environment TEXT NOT NULL CHECK (native_environment IN ('demo','testnet','live')),
    secret_ref TEXT NOT NULL,
    desired_state_revision BIGINT NOT NULL CHECK (desired_state_revision > 0),
    runtime_generation BIGINT NOT NULL CHECK (runtime_generation > 0),
    credential_version BIGINT NOT NULL CHECK (credential_version > 0),
    requested_state TEXT NOT NULL CHECK (requested_state IN ('creating','active','rotating','draining','drained','disabled','removing','removed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, desired_state_revision, runtime_generation, credential_version),
    UNIQUE (operation_id)
);

CREATE TABLE gateway_runtime_snapshot_routes (
    snapshot_id UUID NOT NULL REFERENCES gateway_runtime_snapshots(snapshot_id),
    route_id TEXT NOT NULL,
    product TEXT NOT NULL CHECK (product IN ('spot','perp')),
    settlement_asset TEXT NOT NULL DEFAULT '',
    route_profile_id TEXT NOT NULL,
    requested_state TEXT NOT NULL CHECK (requested_state IN ('active','disabled')),
    PRIMARY KEY (snapshot_id, route_id)
);

CREATE TABLE gateway_account_runtime_heads (
    account_id TEXT PRIMARY KEY,
    active_snapshot_id UUID REFERENCES gateway_runtime_snapshots(snapshot_id),
    pending_snapshot_id UUID REFERENCES gateway_runtime_snapshots(snapshot_id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (active_snapshot_id IS NULL OR active_snapshot_id <> pending_snapshot_id)
);

CREATE TABLE gateway_desired_operations (
    operation_id UUID PRIMARY KEY REFERENCES gateway_runtime_snapshots(operation_id),
    snapshot_id UUID NOT NULL UNIQUE REFERENCES gateway_runtime_snapshots(snapshot_id),
    request_digest TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('prepared','candidate_ready','candidate_failed','applied')),
    applied_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (state <> 'applied' OR applied_at IS NOT NULL)
);

CREATE TABLE gateway_candidate_observations (
    operation_id UUID NOT NULL REFERENCES gateway_desired_operations(operation_id),
    observation_revision BIGINT NOT NULL CHECK (observation_revision > 0),
    route_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('building','ready','failed')),
    reason_code TEXT,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (operation_id, observation_revision, route_id)
);

CREATE TABLE gateway_retiring_generations (
    account_id TEXT NOT NULL,
    snapshot_id UUID NOT NULL UNIQUE REFERENCES gateway_runtime_snapshots(snapshot_id),
    state TEXT NOT NULL CHECK (state IN ('reconciling','teardown_pending','retired')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, snapshot_id)
);

CREATE TABLE gateway_retired_secret_refs (
    mode TEXT NOT NULL CHECK (mode IN ('test','live')),
    namespace TEXT NOT NULL,
    secret_ref TEXT NOT NULL,
    account_id TEXT NOT NULL,
    tombstone_revision BIGINT NOT NULL CHECK (tombstone_revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (mode, namespace, secret_ref)
);

CREATE INDEX gateway_runtime_snapshots_recovery ON gateway_runtime_snapshots (account_id, runtime_generation DESC);
CREATE INDEX gateway_desired_operations_recovery ON gateway_desired_operations (state, updated_at) WHERE state <> 'applied';
```

The migration contract test must execute the full SQL against real PostgreSQL, including the foreign key from `gateway_desired_operations.operation_id` to the unique `gateway_runtime_snapshots.operation_id`; text-only validity is insufficient.

- [ ] **Step 4: Implement immutable store types and operations**

```go
type RouteDefinition struct { RouteID domain.RouteID; Product string; SettlementAsset string; RouteProfileID string; RequestedState RouteState }
type DesiredSnapshot struct { SnapshotID uuid.UUID; OperationID secretstore.OperationID; RequestDigest string; AccountID domain.AccountID; Venue string; NativeEnvironment string; SecretRef secretstore.OpaqueRef; Fence domain.AccountFence; RequestedState DesiredAccountState; Routes []RouteDefinition; CreatedAt time.Time }
type PendingSnapshot struct { DesiredSnapshot; OperationState OperationState; Observations []CandidateObservation }
type ActiveSnapshot struct { DesiredSnapshot; PromotedAt time.Time }

type DesiredStateStore interface {
	PreparePending(context.Context, DesiredSnapshot) (PendingSnapshot, error)
	RecordCandidateObservation(context.Context, CandidateObservation) error
	MarkCandidateReady(context.Context, secretstore.OperationID) error
	PromotePending(context.Context, PromotionRequest) (PromotionResult, error)
	LoadRuntimeState(context.Context, domain.AccountID) (RuntimeState, error)
	ListRuntimeStateForRecovery(context.Context) ([]RuntimeState, error)
	TombstoneSecretReference(context.Context, TombstoneRequest) (Tombstone, error)
	IsSecretReferenceRetired(context.Context, secretstore.Scope, secretstore.OpaqueRef) (bool, error)
}
```

Canonical digest input sorts routes by route ID and includes operation-independent normalized identity, native environment, secret reference, requested state, and the full proposed fence. Reusing an operation ID with another digest fails before resolver or runtime calls. `PromotePending` locks the account head, compares the complete expected active fence, inserts retiring history, swaps active/pending pointers, and marks the operation applied in one transaction.

- [ ] **Step 5: Run store, migration, replay, and crash tests**

Run: `go test ./gateway/store ./gateway/store/postgres -count=1`

Expected: PASS for pending-not-active, failed-candidate preservation, lost-response replay, durable CAS, active-first restart, tombstone persistence, and migration count `3`.

- [ ] **Step 6: Commit Gateway snapshot persistence**

```bash
git add gateway/store gateway/store/postgres/migrations/003_runtime_snapshots.sql gateway/store/postgres
git commit -m "feat: persist immutable gateway runtime snapshots" -m "Constraint: pending candidate state never mutates active runtime truth" -m "Tested: go test ./gateway/store ./gateway/store/postgres -count=1"
```

### Task 8: Build Multi-Route Candidate Groups and Atomic Runtime Publication

**Files:**
- Create: `gateway/account/runtime_group.go`
- Create: `gateway/account/runtime_group_test.go`
- Create: `gateway/account/profile_registry.go`
- Create: `gateway/account/profile_registry_test.go`
- Modify: `gateway/account/factory.go`
- Modify: `gateway/account/manager.go`
- Modify: `gateway/account/manager_test.go`
- Modify: `cmd/exchange-gateway/authority.go`
- Modify: `cmd/exchange-gateway/authority_test.go`
- Modify: `gateway/process/process.go`
- Modify: `gateway/process/process_test.go`

- [ ] **Step 1: Write failing candidate, promotion, and restart tests**

```go
func TestCandidatePromotesSpotAndPerpAsOneGeneration(t *testing.T) {
	manager, lifecycle := newRuntimeGroupHarness(t)
	pending := testPendingWithRoutes("spot", "perp-usdt")
	group, err := manager.BuildCandidate(context.Background(), pending)
	if err != nil { t.Fatal(err) }
	if got := manager.ActiveSnapshot(pending.AccountID); got != nil { t.Fatal("candidate became active before durable promotion") }
	lifecycle.Ready("spot"); lifecycle.Ready("perp-usdt")
	if err := manager.PublishPromoted(context.Background(), group, pending.Fence); err != nil { t.Fatal(err) }
	assertRoutes(t, manager.ActiveSnapshot(pending.AccountID), "spot", "perp-usdt")
}

func TestOneFailedRouteDestroysWholeCandidateAndPreservesActive(t *testing.T) {
	manager, lifecycle := newRuntimeGroupHarnessWithActive(t)
	before := manager.ActiveSnapshot("acct_1")
	lifecycle.Fail("perp-usdt", "authentication_failed")
	if _, err := manager.BuildCandidate(context.Background(), testPendingWithRoutes("spot", "perp-usdt")); !errors.Is(err, ErrCandidateFailed) { t.Fatalf("err=%v", err) }
	if after := manager.ActiveSnapshot("acct_1"); after.Generation != before.Generation { t.Fatalf("before=%+v after=%+v", before, after) }
}
```

Add restart tests for active plus failed pending, and promotion-CAS-committed plus in-memory-pointer-not-published. Add a private-stream test proving SDK reconnect alone does not restore readiness until a snapshot/sequence barrier is observed.

- [ ] **Step 2: Run manager and authority tests and verify RED**

Run: `go test ./gateway/account ./cmd/exchange-gateway ./gateway/process -run 'Test(Candidate|RuntimeGroup|Promotion|Restore|PrivateStream|RuntimeReady)' -count=1`

Expected: FAIL because Manager owns one handle per account and authority mutates DB/runtime in inconsistent orders.

- [ ] **Step 3: Implement immutable runtime groups and candidate construction**

```go
type RuntimeGroup struct {
	Snapshot store.DesiredSnapshot
	Routes map[domain.RouteID]*RouteRuntime
	Admission *GenerationAdmission
	State GroupState
}
type RoutingSnapshot struct {
	Fence domain.AccountFence
	Routes map[domain.RouteID]RouteView
}
type accountRuntime struct {
	active atomic.Pointer[RoutingSnapshot]
	candidate *RuntimeGroup
	retiring map[uint64]*RuntimeGroup
}
```

The adapter-owned registry consumes a validated `credential.Decoded` value plus a trusted `routeprofile.Profile` and returns a private `clientHandle`. Resolve one bundle once per candidate, validate its venue/auth kind, construct every active route as non-writing, clear parser buffers, validate authentication/capability/catalog/private-state barrier, and destroy every constructed handle if any route fails. Disabled routes remain in snapshot diagnostics but have no private client.

- [ ] **Step 4: Make durable promotion the sole linearization point**

Authority order is: normalize; `PreparePending`; build/observe candidate; close old admission; wait for resolved in-memory and restored durable handoffs; call store `PromotePending`; atomically publish the matching routing pointer; open new admission; keep old generation reconciliation-only. On restart: load tombstones and uncertain handoffs, reconstruct active snapshots first, publish them, then resume pending candidates. A committed promotion always restores as active even when the prior process died before pointer publication.

- [ ] **Step 5: Make inventory and ownership dynamic**

Replace startup-only account registration and recovery membership with idempotent `EnsureAccountOwnership(accountID)` invoked before candidate build. `process.Start` no longer loops over YAML account credentials. An empty recovered store starts successfully; a failed account candidate affects only its own readiness.

- [ ] **Step 6: Run multi-route, restart, private-stream, and race tests**

Run: `go test -race ./gateway/account ./cmd/exchange-gateway ./gateway/process -count=1`

Expected: PASS with no partially published candidate, active-first recovery, route-specific readiness, stream-gap fail-closed behavior, and zero-account readiness.

- [ ] **Step 7: Commit runtime groups**

```bash
git add gateway/account cmd/exchange-gateway/authority.go cmd/exchange-gateway/authority_test.go gateway/process
git commit -m "feat: promote account route groups atomically" -m "Constraint: durable promotion CAS precedes in-memory write admission" -m "Tested: go test -race ./gateway/account ./cmd/exchange-gateway ./gateway/process -count=1"
```

### Task 9: Close the Fence-to-Venue Race with Durable Handoff Leases

**Files:**
- Create: `gateway/store/postgres/migrations/004_order_handoffs.sql`
- Modify: `gateway/store/store.go`
- Modify: `gateway/store/store_contract_test.go`
- Modify: `gateway/store/postgres/commands.go`
- Modify: `gateway/store/postgres/migration_contract_test.go`
- Modify: `gateway/store/postgres/postgres_test.go`
- Modify: `gateway/order/types.go`
- Modify: `gateway/order/account_router.go`
- Modify: `gateway/order/account_router_test.go`
- Modify: `gateway/order/coordinator.go`
- Modify: `gateway/order/coordinator_test.go`
- Modify: `gateway/order/postgres_crash_test.go`
- Modify: `api/exchange-gateway/v1/openapi.yaml`
- Modify: `backend/internal/app/gateway_adapter.go`
- Modify: `backend/internal/app/gateway_adapter_test.go`

- [ ] **Step 1: Write the failing barrier race test before changing dispatch**

```go
func TestPromotionWaitsWhileOldGenerationHandoffIsBetweenFenceAndVenue(t *testing.T) {
	harness := newDispatchPromotionBarrier(t)
	done := make(chan error, 1)
	go func() { _, err := harness.Coordinator.Place(context.Background(), harness.PlaceRequest()); done <- err }()
	harness.WaitUntilHandoffDurable()
	promoted := make(chan error, 1)
	go func() { promoted <- harness.Manager.Promote(context.Background(), harness.Pending()) }()
	assertNotClosed(t, promoted)
	harness.ReleaseVenueResult()
	if err := <-done; err != nil { t.Fatal(err) }
	if err := <-promoted; err != nil { t.Fatal(err) }
}
```

Crash tests cover: before lease acquisition; after in-memory lease but before durable handoff; after durable handoff but before venue; during venue; after durable accepted/rejected/uncertain but before lease release; and promotion CAS before routing pointer publication.

- [ ] **Step 2: Run order tests and verify RED**

Run: `go test ./gateway/order ./gateway/store ./gateway/store/postgres ./backend/internal/app -run 'Test(Handoff|PromotionWaits|CoordinatorRealPostgresProcessCrashWindows|WriteFence)' -count=1`

Expected: FAIL because the current coordinator checks the fence and then calls the venue without a generation-scoped lease or durable handoff record.

- [ ] **Step 3: Add migration 004 and handoff store methods**

```sql
CREATE TABLE gateway_order_handoffs (
    handoff_id UUID PRIMARY KEY,
    command_id UUID NOT NULL UNIQUE REFERENCES idempotency_commands(command_id),
    account_id TEXT NOT NULL,
    route_id TEXT NOT NULL,
    desired_state_revision BIGINT NOT NULL CHECK (desired_state_revision > 0),
    runtime_generation BIGINT NOT NULL CHECK (runtime_generation > 0),
    credential_version BIGINT NOT NULL CHECK (credential_version > 0),
    state TEXT NOT NULL CHECK (state IN ('started','accepted','rejected','uncertain')),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ,
    CHECK (state = 'started' OR resolved_at IS NOT NULL)
);

CREATE INDEX gateway_order_handoffs_unresolved_generation
ON gateway_order_handoffs (account_id, runtime_generation, command_id)
WHERE state = 'started';
```

`BeginHandoff` atomically transitions the command from prepared to dispatching and inserts `started` with the full fence. `ResolveHandoff` commits accepted/rejected/uncertain with the command result in the same transaction. Startup maps any restored `started` row to uncertain reconciliation before admitting promotion or new writes.

- [ ] **Step 4: Acquire and retain a generation admission lease through durable outcome**

```go
type HandoffLease interface { Release() }
type HandoffAdmission interface {
	Acquire(domain.WriteFence) (HandoffLease, error)
}

type Route struct {
	RouteID domain.RouteID
	Driver Driver
	Instrument exchange.Instrument
	Product exchange.Product
	WriteReady bool
	Reason string
	Admission HandoffAdmission
}
```

Add `RouteID` to `PlaceRequest`, canonical durable payload, public API request, Platform adapter request, idempotency digest, and recovery decode. Dispatch order is: route/fence validate; atomically acquire admission lease; allocate event; `BeginHandoff`; venue SDK call; `ResolveHandoff` to accepted/rejected/uncertain; release lease. A failure after durable `started` is always uncertain. Promotion closes old admission before waiting and does not publish the new generation until both in-memory leases and restored unresolved handoffs are clear.

- [ ] **Step 5: Run order, PostgreSQL crash, contract, and race gates**

Run: `go test -race ./gateway/order ./gateway/store ./gateway/store/postgres ./backend/internal/app ./gateway/contracts -count=1`

Expected: PASS; migration count is `4`; stale route fences never reach a driver; every durable started handoff survives as an uncertain blocker; promotion never crosses an in-flight old-generation venue call.

- [ ] **Step 6: Commit handoff fencing**

```bash
git add gateway/store gateway/store/postgres/migrations/004_order_handoffs.sql gateway/order api/exchange-gateway/v1/openapi.yaml backend/internal/app/gateway_adapter.go backend/internal/app/gateway_adapter_test.go gateway/contracts
git commit -m "fix: fence venue handoffs with generation leases" -m "Constraint: promotion waits for durable accepted, rejected, or uncertain outcomes" -m "Tested: go test -race ./gateway/order ./gateway/store ./gateway/store/postgres ./backend/internal/app ./gateway/contracts -count=1"
```

### Task 10: Share Public Connectors and Hierarchical Rate-Limit Budgets

**Files:**
- Create: `gateway/publicconn/registry.go`
- Create: `gateway/publicconn/registry_test.go`
- Create: `gateway/publicconn/factory.go`
- Create: `gateway/publicconn/factory_test.go`
- Create: `gateway/ratelimit/coordinator.go`
- Create: `gateway/ratelimit/coordinator_test.go`
- Create: `gateway/ratelimit/transport.go`
- Create: `gateway/ratelimit/transport_test.go`
- Modify: `gateway/catalog/service.go`
- Modify: `gateway/catalog/service_test.go`
- Modify: `gateway/account/factory.go`
- Modify: `gateway/account/manager.go`
- Modify: `gateway/account/manager_test.go`
- Modify: `cmd/exchange-gateway/app.go`
- Modify: `cmd/exchange-gateway/app_test.go`
- Modify: `sdk/binance/spot/client.go`
- Modify: `sdk/binance/perp/client.go`
- Modify: `sdk/binance/spot/rate_limiter_test.go`
- Modify: `sdk/binance/perp/rate_limiter_test.go`

- [ ] **Step 1: Write failing sharing and budget tests with a deterministic clock**

```go
func TestTwoAccountsShareOnePublicConnectorAndSeparatePrivateRoutes(t *testing.T) {
	registry, factory := newConnectorHarness(t)
	key := routeprofile.Key{Venue: "binance", Mode: routeprofile.ModeTest, RouteProfileID: "binance.perp.usdt.demo.v1"}
	one, err := registry.Acquire(context.Background(), key)
	if err != nil { t.Fatal(err) }
	two, err := registry.Acquire(context.Background(), key)
	if err != nil { t.Fatal(err) }
	if one.ConnectorID() != two.ConnectorID() || factory.BuildCalls(key) != 1 { t.Fatalf("one=%s two=%s builds=%d", one.ConnectorID(), two.ConnectorID(), factory.BuildCalls(key)) }
}

func TestOldCandidateAndActiveRoutesShareAuthenticatedPrincipalBudget(t *testing.T) {
	clock := newManualClock()
	coordinator := ratelimit.NewCoordinator(clock.Now)
	keys := []ratelimit.Key{{Kind: ratelimit.VenueProfile, Value: "binance/test/binance.perp.usdt.demo.v1"}, {Kind: ratelimit.AuthenticatedPrincipal, Value: "binance/principal-fingerprint"}}
	coordinator.Configure(keys[1], ratelimit.Quota{Limit: 1, Interval: time.Minute})
	if err := coordinator.Acquire(context.Background(), keys); err != nil { t.Fatal(err) }
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond); defer cancel()
	if err := coordinator.Acquire(ctx, keys); !errors.Is(err, context.DeadlineExceeded) { t.Fatalf("err=%v", err) }
}
```

Add tests proving Spot and Perp share the Binance venue/IP budget, different accounts have different private clients, duplicate concurrent refreshes coalesce, a stale connector affects only its profile, and missing authenticated identity falls back to the broader venue/profile key.

- [ ] **Step 2: Run connector, catalog, and budget tests and verify RED**

Run: `go test ./gateway/publicconn ./gateway/ratelimit ./gateway/catalog ./gateway/account ./cmd/exchange-gateway ./sdk/binance/spot ./sdk/binance/perp -count=1`

Expected: FAIL because public polling is account-owned and Binance Spot/Perp instantiate independent SDK limiters.

- [ ] **Step 3: Implement one connector per trusted public key**

```go
type Connector interface {
	Refresh(context.Context) ([]exchange.Instrument, error)
	Close() error
}
type Lease interface { ConnectorID() string; Snapshot() (catalog.Snapshot, error); Release() }
type Registry struct {
	mu sync.Mutex
	entries map[routeprofile.Key]*entry
	factory Factory
}
```

An entry owns one credential-free connector, one coalesced refresh loop, ref count, current catalog snapshot, freshness timestamps, stable redacted error, and close lifecycle. `catalog.Scope` becomes `(venue, mode, routeProfileId)` and account/route subscriptions map to the same scope; it no longer owns refresh loops. Connector build derives endpoints only through `routeprofile.Registry`.

- [ ] **Step 4: Implement hierarchical budget acquisition at the Gateway transport boundary**

```go
type KeyKind string
const ( VenueProfile KeyKind = "venue_profile"; AuthenticatedPrincipal KeyKind = "authenticated_principal"; RouteEndpoint KeyKind = "route_endpoint" )
type Key struct { Kind KeyKind; Value string }
type Quota struct { Limit int; Interval time.Duration }
type VectorProvider interface { Keys(*http.Request, RequestScope) []Key }
```

`Coordinator.Acquire` sorts keys by `KeyKind` then value, atomically reserves all configured windows under one lock, and waits for the maximum delay without partially consuming budgets. `Transport.RoundTrip` obtains the adapter-declared vector before the underlying transport. Public scope includes venue/profile/IP; private scope adds a non-exported authenticated-principal fingerprint and route/endpoint key. Candidate, active, and retiring generations for the same principal use the same fingerprint. If a venue cannot provide a stable principal identity, its vector omits the account key and uses the broader shared profile key with a conservative configured quota.

When Gateway builds Binance clients, inject the Gateway transport and disable their SDK-local limiter so Spot and Perp cannot double-count independent windows. Standalone SDK constructors keep their current local limiter for library users. Other venues use the injected transport without claiming undocumented numeric limits; their explicit profile quotas come from trusted Gateway operational configuration and fail validation if absent.

- [ ] **Step 5: Run deterministic, race, and catalog regression tests**

Run: `go test -race ./gateway/publicconn ./gateway/ratelimit ./gateway/catalog ./gateway/account ./cmd/exchange-gateway ./sdk/binance/spot ./sdk/binance/perp -count=1`

Expected: PASS; one public refresh serves two accounts, private clients remain isolated, budget acquisition is deadlock-free and cancellation-aware, and no account/product can choose or bypass budget keys.

- [ ] **Step 6: Commit shared connectors and budgets**

```bash
git add gateway/publicconn gateway/ratelimit gateway/catalog gateway/account cmd/exchange-gateway sdk/binance/spot sdk/binance/perp
git commit -m "feat: share gateway connectors and request budgets" -m "Constraint: public polling is profile-scoped while private runtimes remain account-route scoped" -m "Tested: go test -race ./gateway/publicconn ./gateway/ratelimit ./gateway/catalog ./gateway/account ./cmd/exchange-gateway ./sdk/binance/spot ./sdk/binance/perp -count=1"
```

### Task 11: Connect Platform Desired State to Gateway Runtime Truth

**Files:**
- Modify: `gateway/httpapi/internal_routes.go`
- Modify: `gateway/httpapi/internal_routes_test.go`
- Modify: `gateway/httpapi/route_contract_test.go`
- Modify: `cmd/exchange-gateway/authority.go`
- Modify: `cmd/exchange-gateway/authority_test.go`
- Modify: `backend/internal/app/gateway_adapter.go`
- Modify: `backend/internal/app/gateway_adapter_test.go`
- Create: `backend/internal/app/account_reconciler.go`
- Create: `backend/internal/app/account_reconciler_test.go`
- Modify: `backend/internal/app/app.go`
- Modify: `backend/internal/app/app_test.go`
- Modify: `backend/internal/platform/system/readiness.go`
- Modify: `backend/internal/platform/system/readiness_test.go`

- [ ] **Step 1: Write failing strict HTTP and lost-response reconciliation tests**

```go
func TestDesiredApplyResponseRedactsRawReference(t *testing.T) {
	handler := newInternalHandler(t)
	request := validDesiredApplyJSON(t)
	response := serveJSON(t, handler, http.MethodPut, "/internal/v1/accounts/desired/acct_1/operations/018f47a8-2578-7d3c-9d7d-5e4f4a445c33", request)
	if response.Code != http.StatusAccepted { t.Fatalf("status=%d body=%s", response.Code, response.Body.String()) }
	for _, forbidden := range []string{"secretRef", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "provider", "master-key"} {
		if strings.Contains(response.Body.String(), forbidden) { t.Fatalf("response leaked %q", forbidden) }
	}
}

func TestPlatformCompletesCASWhenPromotionResponseWasLost(t *testing.T) {
	reconciler, platformStore, gateway := newAccountReconcilerHarness(t)
	gateway.Active = exactRuntimeTruth(2, 2, 1)
	platformStore.Operation.State = accounts.OperationApplying
	if err := reconciler.RunOnce(context.Background()); err != nil { t.Fatal(err) }
	if platformStore.Account.State != accounts.StateActive || platformStore.Operation.State != accounts.OperationActive { t.Fatalf("account=%+v operation=%+v", platformStore.Account, platformStore.Operation) }
}
```

- [ ] **Step 2: Run HTTP, adapter, reconciler, and readiness tests and verify RED**

Run: `go test ./gateway/httpapi ./cmd/exchange-gateway ./backend/internal/app ./backend/internal/platform/system -count=1`

Expected: FAIL because the Gateway handler still uses legacy account/mutation DTOs and Platform has no desired-state client.

- [ ] **Step 3: Implement strict request-only raw-reference DTOs and redacted runtime views**

```go
type DesiredApplyRequest struct {
	OperationID string `json:"operationId"`
	AccountID string `json:"accountId"`
	Venue string `json:"venue"`
	NativeEnvironment string `json:"nativeEnvironment"`
	SecretRef string `json:"secretRef"`
	DesiredStateRevision uint64 `json:"desiredStateRevision"`
	RuntimeGeneration uint64 `json:"runtimeGeneration"`
	CredentialVersion uint64 `json:"credentialVersion"`
	RequestedState string `json:"requestedState"`
	Routes []DesiredRoute `json:"routes"`
}

type RuntimeView struct {
	AccountID string `json:"accountId"`
	Active *RuntimeSnapshotView `json:"active,omitempty"`
	Pending *PendingSnapshotView `json:"pending,omitempty"`
	Retiring []RuntimeSnapshotView `json:"retiring"`
}
```

Validate path account/operation IDs against the body, strict EOF/duplicate/unknown fields, trusted native environment equality, bounded route count and body size, monotonic fences, and canonical digest before resolver/network access. Return `202 Accepted` for prepared/building candidates and `200 OK` for exact applied replays. Views contain route readiness/reasons but no refs or provider details.

- [ ] **Step 4: Implement the authenticated Platform client and reconciler**

Add `ApplyDesired`, `GetRuntimeTruth`, and `RetireAccount` to `gatewayHTTPAdapter`. Every request carries internal bearer authentication, `X-Bolter-Mode`, request ID, exact operation ID, and bounded response decode. Adapter errors retain only stable code, retryability, account ID, route ID, and operation ID.

`accountReconciler` resumes durable operations at startup and on a bounded interval. It applies pending snapshots, compares exact active fences and all active route readiness, completes Platform CAS after lost responses, preserves active state on candidate failures, retries cleanup, and publishes only safe activity events. Platform control-plane readiness requires reconciler/store/Gateway internal authority availability but does not require any account route.

- [ ] **Step 5: Run boundary, reconciliation, and redaction tests**

Run: `go test -race ./gateway/httpapi ./cmd/exchange-gateway ./backend/internal/app ./backend/internal/platform/system -count=1`

Expected: PASS for strict DTOs, idempotent apply, response loss, candidate failure, restart recovery, retirement retry, mode mismatch, and raw-reference redaction.

- [ ] **Step 6: Commit Platform/Gateway reconciliation**

```bash
git add gateway/httpapi cmd/exchange-gateway/authority.go cmd/exchange-gateway/authority_test.go backend/internal/app backend/internal/platform/system
git commit -m "feat: reconcile platform accounts through gateway authority" -m "Constraint: platform state follows exact durable gateway runtime truth" -m "Tested: go test -race ./gateway/httpapi ./cmd/exchange-gateway ./backend/internal/app ./backend/internal/platform/system -count=1"
```

### Task 12: Deliver Typed Write-Only Account and Route Management in the Web App

**Files:**
- Modify: `web/src/app/types.ts`
- Modify: `web/src/app/App.tsx`
- Modify: `web/src/app/App.test.tsx`
- Modify: `web/src/app/routes/Accounts.tsx`
- Modify: `web/src/app/routes/Accounts.test.tsx`
- Modify: `web/src/app/view-models.ts`
- Modify: `web/src/app/view-models.test.ts`
- Modify: `web/src/state/realtime.ts`
- Modify: `web/src/test/realtime.test.ts`
- Modify: `web/e2e/fixture-platform.ts`
- Modify: `web/e2e/platform.spec.ts`
- Create: `web/e2e/platform-real.spec.ts`
- Create: `web/playwright.real.config.ts`
- Modify: `web/package.json`
- Modify: `web/src/styles/theme.css`

- [ ] **Step 1: Write failing component and browser tests from user workflows**

Tests must cover: add two accounts at one venue; choose multiple Spot/Perp routes; credential fields change by venue auth schema; submit and immediately clear all secret inputs; rotate without displaying current values; see active/pending/retiring route states; retry failed validation; drain/disable/remove blockers; empty-inventory readiness; stale public connector; private stream gap/recovery; mode conflict denial; API error redaction; refresh/resnapshot; and mobile layout without overlap.

```tsx
it("clears every credential field after account creation", async () => {
  render(<Accounts {...propsWithBinanceSchema()} />);
  await user.type(screen.getByLabelText("API key"), "browser-key");
  await user.type(screen.getByLabelText("API secret"), "browser-secret");
  await user.click(screen.getByRole("button", { name: "Add account" }));
  await waitFor(() => expect(props.onCreate).toHaveBeenCalledTimes(1));
  expect(screen.getByLabelText("API key")).toHaveValue("");
  expect(screen.getByLabelText("API secret")).toHaveValue("");
  expect(document.body.textContent).not.toContain("browser-secret");
});
```

- [ ] **Step 2: Run web unit and fixture E2E tests and verify RED**

Run: `npm --prefix web test -- --run`

Run: `npm --prefix web run test:e2e`

Expected: FAIL because the UI accepts caller-selected environment plus raw credential JSON and shows only the first account runtime.

- [ ] **Step 3: Implement v5 browser types and account orchestration**

```ts
export type AccountRoute = {
  routeId: string;
  product: "spot" | "perp";
  settlementAsset?: string;
  routeProfileId: string;
  requestedState: "active" | "disabled";
};

export type Account = {
  accountId: string;
  venue: string;
  mode: "test" | "live";
  nativeEnvironment: "demo" | "testnet" | "live";
  displayName: string;
  productPolicy: string[];
  status: AccountState;
  desiredStateRevision: number;
  runtimeGeneration: number;
  credentialVersion: number;
  routes: AccountRoute[];
};
```

Load safe credential field descriptors and route profiles from Platform. Build `credentialBundle` from controlled inputs only at submit time; do not persist it in workspace state, local/session storage, URL state, realtime payloads, or query caches. Clear inputs in a `finally` block after the request settles. Do not expose mode/environment controls; show native environment as read-only profile metadata.

- [ ] **Step 4: Redesign Accounts and Security for repeated operational use**

Use an unframed two-column desktop layout that collapses to one column on mobile. The account table expands into route rows with profile, product, settlement, active/pending state, readiness, and redacted reason. Use Lucide icons for refresh, add, rotate, validate, disable, drain, and delete actions with tooltips/accessible names; use checkboxes for product policy and route activation. Avoid nested cards and keep form/control dimensions stable. Errors appear beside the affected account or route and never echo submitted values.

- [ ] **Step 5: Add fixture-backed and real-Platform browser projects**

Keep `platform.spec.ts` deterministic and fixture-backed. `platform-real.spec.ts` must not call `page.route` for `/api/v1/**`; it connects to the Docker-free Platform URL, authenticates through the real login UI, verifies zero-account or existing-account discovery, exercises a non-writing account validation/retry path, restarts/resnapshots through the test harness, and asserts DOM, local/session storage, browser console, captured responses, and screenshots contain no credential value, raw opaque reference, provider path, or Gateway token.

Add `test:e2e:real` guarded by `BT_DEV_REAL_BROWSER_ACCEPTANCE=1` and require `FT_MODE=test`. The real project skips with an explicit reason when the opt-in flag is absent and rejects `FT_MODE=live` before browser launch.

- [ ] **Step 6: Run build, unit, fixture, and guarded real-browser gates**

Run: `npm --prefix web run build`

Run: `npm --prefix web test -- --run`

Run: `npm --prefix web run test:e2e`

Run after the local Test stack is healthy: `FT_MODE=test BT_DEV_REAL_BROWSER_ACCEPTANCE=1 npm --prefix web run test:e2e:real`

Expected: PASS; no credential fields survive submission; route diagnostics render without overlap at desktop and mobile viewports; the real browser project talks to Platform rather than fixtures.

- [ ] **Step 7: Commit the account UI**

```bash
git add web
git commit -m "feat: add typed account route management" -m "Constraint: credential inputs are write-only and environment selection is profile-derived" -m "Tested: npm --prefix web run build && npm --prefix web test -- --run && npm --prefix web run test:e2e"
```

### Task 13: Repair Migration Order and Add Secure Provisioning and Route Backfill

**Files:**
- Modify: `migration/workflow.go`
- Modify: `migration/workflow_test.go`
- Modify: `migration/workflow_adversarial_test.go`
- Modify: `migration/adversarial_contract_test.go`
- Modify: `migration/executor.go`
- Modify: `migration/executor_test.go`
- Modify: `migration/manifest.go`
- Modify: `migration/manifest_contract_test.go`
- Modify: `cmd/migration-tool/app.go`
- Modify: `cmd/migration-tool/app_test.go`
- Create: `cmd/account-route-backfill/main.go`
- Create: `cmd/account-route-backfill/app.go`
- Create: `cmd/account-route-backfill/app_test.go`
- Create: `backend/cmd/platform-credential/main.go`
- Create: `backend/cmd/platform-credential/app.go`
- Create: `backend/cmd/platform-credential/app_test.go`
- Create: `migration/backfills/account-route-v1.json`
- Modify: `migration/manifest.json`
- Modify: `scripts/test-real-platform`
- Modify: `scripts/dev-platform`
- Modify: `scripts/dev_platform_test.go`
- Modify: `docs/guides/docker-free-local-platform.md`
- Modify: `docs/reference/testing.md`

- [ ] **Step 1: Write failing workflow tests for the approved reversible order**

```go
func TestWorkflowRequiresRestoreThenFinalDeltaThenFence(t *testing.T) {
	manifest := manifestAt(PhaseRestored)
	if err := Apply(&manifest, Request{Operation: FinalDelta, ArtifactPath: testArtifact(t)}); err != nil { t.Fatal(err) }
	if manifest.Workflow.Phase != PhaseDeltaCaptured { t.Fatalf("phase=%s", manifest.Workflow.Phase) }
	if err := Apply(&manifest, Request{Operation: Fence}); err != nil { t.Fatal(err) }
	if manifest.Workflow.Phase != PhaseFenced { t.Fatalf("phase=%s", manifest.Workflow.Phase) }
}
```

Add adversarial tests that reject fence-before-delta, backfill-before-fence, a changed backfill artifact, cross-mode DSNs/roles, source writes observed after the fence, ambiguous legacy profile mappings, raw-reference output, credential arguments, terminal echo, and Live provisioning.

- [ ] **Step 2: Run migration and command tests and verify RED**

Run: `go test ./migration ./cmd/migration-tool ./cmd/account-route-backfill ./scripts -count=1`

Run: `cd backend && go test ./cmd/platform-credential -count=1`

Expected: FAIL because workflow currently enforces restore/fence/final-delta and neither safe backfill nor stdin provisioning exists.

- [ ] **Step 3: Correct workflow transitions and validation order**

Set the phases to `restored -> delta_captured -> fenced -> backfilled -> validated -> activation_ready`. Final delta captures a checksum-stable artifact; fence disables source and target writer logins and terminates sessions; before backfill, a post-fence equality probe verifies the captured source high-water marks still match. Any mismatch stops before the irreversible boundary, restores the authorized source writer, records the failure, and requires a new delta capture. Validation uses only offline-safe targeted Go gates, browser fixture gates, Docker-free Test real-stack reads, restart, then repeated real browser/readiness/stream/idempotency checks.

- [ ] **Step 4: Implement deterministic route backfill without secret material access**

`account-route-v1.json` contains only mappings of `(venue, nativeEnvironment, legacy product, settlement)` to a trusted `routeProfileId` and route ID. `account-route-backfill` reads legacy account identity and opaque references through parameterized database queries, validates every mapping with `routeprofile.Registry`, writes Platform routes and Gateway pending snapshots transactionally, and emits only counts plus account IDs. The migration executor invokes this program with the checksum-verified mapping artifact and PostgreSQL service names instead of sending the JSON to `psql`; the injected runner and command-output proof are covered by `executor_test.go`. It never opens `.env`, Gateway YAML credential fields, Secret Store envelopes, or provider key files. Ambiguous/missing/cross-mode rows are reported as stable blockers and produce no partial account write.

- [ ] **Step 5: Implement non-echoing credential provisioning through Platform service boundaries**

The backend command accepts account metadata and route JSON from a protected file or flags, reads one bounded credential bundle from stdin, disables terminal echo when stdin is a TTY, constructs the same configured Platform writer/store/Gateway client as production, invokes the account service with an explicit idempotency key, clears input buffers, and prints only `accountId`, `operationId`, and resulting state. Reject credential material in command-line flags/environment, reject Live mode, and reject non-loopback/Gateway-bypass configuration.

- [ ] **Step 6: Replace the baseline manifest only with verified local evidence**

Do not fabricate import, backup, restore, fence, validation, activation, or retirement timestamps. Keep `migration/manifest.json` at its truthful current phase until each command succeeds; add the repository-owned backfill artifact name and its checksum only through `migration-tool backfill`. Update launcher help and testing docs with the exact no-Docker sequence and explicit G010 no-trade/G011 Test-write boundary.

- [ ] **Step 7: Run migration, provisioning, shell, and security tests**

Run: `go test ./migration ./cmd/migration-tool ./cmd/account-route-backfill ./scripts -count=1`

Run: `cd backend && go test ./cmd/platform-credential -count=1`

Run: `sh -n scripts/dev-platform scripts/dev-platform-service scripts/test-real-platform backend/scripts/secret-bootstrap.sh`

Expected: PASS; restore/delta/fence ordering is enforced; backfill is deterministic and atomic; provisioning accepts stdin only and emits no sensitive bytes or references; manifest evidence remains truthful.

- [ ] **Step 8: Commit migration and provisioning tooling**

```bash
git add migration cmd/migration-tool cmd/account-route-backfill backend/cmd/platform-credential scripts docs/guides/docker-free-local-platform.md docs/reference/testing.md
git commit -m "feat: add fenced account route migration tooling" -m "Constraint: provisioning never scrapes environment files or prints credential references" -m "Tested: go test ./migration ./cmd/migration-tool ./cmd/account-route-backfill ./scripts -count=1; cd backend && go test ./cmd/platform-credential -count=1"
```

### Task 14: Remove Static Account Runtime Configuration and Prove the Integrated Offline System

**Files:**
- Modify: `gateway/config/config.go`
- Modify: `gateway/config/config_test.go`
- Modify: `gateway/dev-platform.test.yaml`
- Modify: `gateway/process/process.go`
- Modify: `gateway/process/process_test.go`
- Modify: `cmd/exchange-gateway/app.go`
- Modify: `cmd/exchange-gateway/app_test.go`
- Modify: `gateway/contracts/import_boundary_test.go`
- Modify: `gateway/contracts/compose_contract_test.go`
- Modify: `gateway/contracts/docker_contract_test.go`
- Modify: `Makefile`
- Create: `scripts/test-platform-offline`
- Create: `scripts/test_platform_offline_test.go`
- Create: `gateway/integration/platform_runtime_test.go`
- Create: `gateway/integration/platform_runtime_race_test.go`
- Modify: `docs/guides/docker-free-local-platform.md`
- Modify: `docs/reference/testing.md`

- [ ] **Step 1: Write failing tests that reject static account credentials and inventory in normal startup**

```go
func TestGatewayConfigRejectsLegacyAccountsInNormalRuntime(t *testing.T) {
	_, err := config.LoadYAML([]byte(`
server:
  mode: test
  listen: 127.0.0.1:18082
  auth_token_ref: {env: GATEWAY_AUTH_TOKEN_FILE}
postgres:
  dsn_ref: {env: GATEWAY_POSTGRES_DSN_FILE}
accounts:
  - account_id: acct_1
    venue: binance
`))
	if err == nil || !strings.Contains(err.Error(), "accounts") { t.Fatalf("err=%v", err) }
}
```

Integration tests must prove: healthy provider plus zero accounts is ready; absent route write is denied; two same-profile accounts build one public connector and two private groups; Spot+Perp promotes atomically; failed route preserves active; wrong-mode tokens, database identity, profile, opaque reference, and command fence fail before network; restart restores active before pending; retired refs stay rejected; Live private writes stay denied.

- [ ] **Step 2: Run integrated tests and verify RED**

Run: `go test ./gateway/integration ./gateway/config ./gateway/process ./cmd/exchange-gateway ./gateway/contracts ./scripts -count=1`

Expected: FAIL because config still contains static account credential variants and the integrated harness does not exist.

- [ ] **Step 3: Remove static runtime accounts from the production config surface**

`gateway/config.File` keeps only server, PostgreSQL, trusted route profile/limit configuration, and listener/auth references. Normal `LoadYAML` rejects an `accounts` field through strict decode. Move the legacy account decoder into `cmd/account-route-backfill` so migration can read the old metadata format without making it a Gateway runtime input. Delete `allowedEnvRefs` branches for exchange credentials; only operational auth/DSN references remain environment-resolvable.

Replace Docker-focused default contract assertions with Docker-free local runtime assertions. Do not delete historical Docker files in this task; ensure no default test, launcher, or guide requires Docker.

- [ ] **Step 4: Add one offline-safe aggregate gate**

`scripts/test-platform-offline` must run, in this order, with `set -eu` and no environment sourcing:

```sh
make test-gateway-contract
make test-gateway-offline
make test-gateway-race
make test-gateway-security
go test ./migration ./cmd/migration-tool ./cmd/account-route-backfill ./scripts -count=1
(cd backend && go test ./... && go test -race ./... && go vet ./...)
npm --prefix web run build
npm --prefix web test -- --run
npm --prefix web run test:e2e
```

Add `make test-platform-offline` as the documented implementation gate. The script rejects `RUN_FULL`, venue acceptance flags, `FT_MODE=live`, and any Live-write opt-in before invoking tests.

- [ ] **Step 5: Add canary-based support-bundle and redaction verification**

The integration harness writes synthetic canaries such as `BT_TEST_SECRET_CANARY_4f0a9d` into a temporary Test credential bundle, exercises create/candidate/failure/restart/delete paths, and scans captured JSON, logs, metrics, traces, audit rows, browser fixtures, screenshots, and generated support artifacts. Assert neither canary nor raw opaque ref nor temporary provider path appears. Never scan or load the repository `.env`.

- [ ] **Step 6: Run the complete offline and race gate**

Run: `make test-platform-offline`

Expected: PASS with no venue network writes, no Docker dependency, no static account runtime configuration, all race tests clean, and all credential canaries absent from observable output.

- [ ] **Step 7: Commit integrated offline readiness**

```bash
git add gateway/config gateway/dev-platform.test.yaml gateway/process cmd/exchange-gateway gateway/contracts Makefile scripts/test-platform-offline scripts/test_platform_offline_test.go gateway/integration docs/guides/docker-free-local-platform.md docs/reference/testing.md
git commit -m "test: prove dynamic platform runtime offline" -m "Constraint: default gates perform no venue writes and require no Docker runtime" -m "Tested: make test-platform-offline"
```

### Task 15: Execute G010 Fenced Cutover and Real-Stack No-Trade Verification

**Files:**
- Create: `scripts/g010-cutover`
- Create: `scripts/g010_cutover_test.go`
- Modify through command-generated evidence only: `migration/manifest.json`
- Modify through Ultragoal leader checkpoint only: `<omx-state>/ultragoal/ledger.jsonl`

- [ ] **Step 1: Write a failing shell contract test for the G010 guardrails**

The test executes `scripts/g010-cutover` with fake command shims and proves it rejects missing `BT_G010_CUTOVER_ACCEPT=1`, every mode except exact `test`, any Live-write flag, absent backup/restore evidence, non-mode-scoped state paths, post-fence source writers, failed real browser checks, and any command matching private venue write routes.

- [ ] **Step 2: Run the guard test and verify RED**

Run: `go test ./scripts -run TestG010Cutover -count=1`

Expected: FAIL because the guarded cutover driver does not exist.

- [ ] **Step 3: Implement a restartable, evidence-driven no-trade driver**

`scripts/g010-cutover` uses `migration-tool status` and executes only the next legal phase. It requires Test mode, explicit cutover opt-in, loopback service URLs, mode-scoped database/service identities, healthy Secret Store permissions, and the absence of venue-write opt-ins. It records command outputs through migration proof digests, never edits manifest fields directly, and stops before source retirement. A repeated invocation resumes from durable manifest state and rechecks every prior postcondition.

- [ ] **Step 4: Run the guard suite and commit the driver before touching migration state**

Run: `go test ./scripts -run TestG010Cutover -count=1`

Expected: PASS.

```bash
git add scripts/g010-cutover scripts/g010_cutover_test.go
git commit -m "ops: add guarded platform cutover driver" -m "Constraint: the driver rejects Live mode, venue writes, and source retirement" -m "Tested: go test ./scripts -run TestG010Cutover -count=1"
```

- [ ] **Step 5: Establish the clean pre-cutover baseline**

Run: `git status --short`

Expected: empty after the implementation commits.

Run: `make test-platform-offline`

Expected: PASS.

Run: `FT_MODE=test ./scripts/dev-platform doctor`

Expected: PASS with Test-only paths, healthy provider configuration, and no secret values in output.

- [ ] **Step 6: Execute backup, restore, final delta, fence, route backfill, and validation**

Run: `FT_MODE=test BT_G010_CUTOVER_ACCEPT=1 ./scripts/g010-cutover`

Expected: the migration manifest reaches `activation_ready`; import, backup, restore, final delta, one-writer fence, route backfill, offline validation, and real-stack validation contain verified proof digests; source retirement remains unset; no private venue command is issued.

- [ ] **Step 7: Prove the real Docker-free Test stack before and after restart**

Run: `FT_MODE=test ./scripts/dev-platform up`

Run: `FT_MODE=test ./scripts/dev-platform status`

Run: `FT_MODE=test BT_DEV_REAL_STACK_ACCEPTANCE=1 make test-real`

Run: `FT_MODE=test BT_DEV_REAL_BROWSER_ACCEPTANCE=1 npm --prefix web run test:e2e:real`

Run: `FT_MODE=test ./scripts/dev-platform restart`

Run: `FT_MODE=test ./scripts/dev-platform status`

Run: `FT_MODE=test BT_DEV_REAL_STACK_ACCEPTANCE=1 make test-real`

Run: `FT_MODE=test BT_DEV_REAL_BROWSER_ACCEPTANCE=1 npm --prefix web run test:e2e:real`

Expected: both cycles show healthy zero-or-more account control plane, exact route/runtime recovery from PostgreSQL plus encrypted filesystem state, no static account YAML dependency, no stream gap after resnapshot, idempotent operation replay, and no venue write.

- [ ] **Step 8: Record activation evidence but do not retire the source**

Run: `FT_MODE=test BT_G010_CUTOVER_ACCEPT=1 ./scripts/g010-cutover`

Expected: when all verification evidence remains current, the tool records externally observed Platform activation/cutover evidence and leaves the workflow at `activated`; `prepare-retirement` and source deletion are not executed in G010.

- [ ] **Step 9: Commit generated evidence and checkpoint G010**

```bash
git add migration/manifest.json
git commit -m "ops: complete fenced platform cutover" -m "Constraint: G010 performs no venue writes and no source retirement" -m "Tested: make test-platform-offline; FT_MODE=test BT_DEV_REAL_STACK_ACCEPTANCE=1 make test-real; FT_MODE=test BT_DEV_REAL_BROWSER_ACCEPTANCE=1 npm --prefix web run test:e2e:real"
```

The Ultragoal leader calls `get_goal`, then checkpoints G010 with the fresh active aggregate snapshot and evidence naming the manifest phase, commit, offline gate, two real-stack cycles, and two real-browser cycles. Do not call `update_goal` because G011 and G012 remain.

### Task 16: Execute G011 Bounded Real Test Trading Acceptance

**Files:**
- Create: `scripts/g011-test-acceptance`
- Create: `scripts/g011_test_acceptance_test.go`
- Create: `web/e2e/trading-real.spec.ts`
- Create: `docs/verification/2026-08-09-test-trading-acceptance.md`
- Modify through Ultragoal leader checkpoint only: `<omx-state>/ultragoal/ledger.jsonl`

- [ ] **Step 1: Write failing safety tests for the trading acceptance driver**

Tests must prove the driver rejects Live mode, missing `BT_G011_TEST_TRADING_ACCEPT=1`, any order/leg above 100 USDT, accounts whose exact routes are not ready, unknown symbols, stale market data, missing close/cancel cleanup hooks, and concurrent venue acceptance. It must stop immediately on an uncertain command and enter reconciliation/cleanup instead of issuing another write.

- [ ] **Step 2: Run the G011 driver tests and verify RED**

Run: `go test ./scripts -run TestG011TestAcceptance -count=1`

Expected: FAIL because the bounded Test acceptance driver and browser flow do not exist.

- [ ] **Step 3: Implement preflight, value calculation, and durable evidence capture**

The driver obtains mode/readiness/account/route/instrument facts only from Platform APIs. It computes order notional using the current mark or limit price and rejects `quantity * price > 100`. It creates a unique acceptance run ID and idempotency keys, records only account/route/instrument/order identifiers and normalized financial facts, and never records credentials, refs, provider paths, Gateway tokens, raw SDK errors, or request signatures.

- [ ] **Step 4: Commit the tested acceptance driver before issuing venue writes**

Run: `go test ./scripts -run TestG011TestAcceptance -count=1`

Expected: PASS.

```bash
git add scripts/g011-test-acceptance scripts/g011_test_acceptance_test.go web/e2e/trading-real.spec.ts
git commit -m "test: add bounded Test trading acceptance driver" -m "Constraint: every order or funding leg is at most 100 USDT and Live writes are rejected" -m "Tested: go test ./scripts -run TestG011TestAcceptance -count=1"
```

- [ ] **Step 5: Exercise Manual Trading through the authenticated web product**

For one ready Test account/route, perform: browse symbols and rule metadata; submit a market order at or below 100 USDT; verify order, fill, fee, position quantity, average entry, mark, unrealized PnL, ledger, activity, and audit; submit a resting limit order at or below 100 USDT and cancel it; replay an idempotency key and prove no duplicate order; send a stale route fence and prove no venue side effect; restart Platform/Gateway while the controlled position is open; verify resnapshot and unchanged position; safe-close the exact remaining quantity at or below 100 USDT; verify realized PnL and fees reconcile to fills and final position is zero.

- [ ] **Step 6: Exercise Funding Trading through the authenticated web product**

Select an opportunity whose two ready Test routes and venue minimums permit each leg at or below 100 USDT. Open through preview plus command, prove long/short leg quantities and notionals match the preview within instrument increments, verify both positions/fills/fees and lineage attribution, observe funding/reference/mark data, verify unrealized PnL from signed quantities and current marks, hold through one available Test funding observation when the venue publishes it, reconcile the funding ledger entry, and close both legs through the Funding product. If only one leg is accepted, verify compensation closes the surviving exposure and the run stops for review.

- [ ] **Step 7: Cover user-visible abnormal paths without unsafe venue behavior**

Use browser/API validation to test invalid quantity increments, below-minimum notional, stale market data, disabled route, account drain blocker, duplicate command, expired session/CSRF, transient private-stream disconnect followed by snapshot barrier, and service restart. Assertions distinguish validation denial, retryable degradation, uncertain handoff, and terminal venue rejection without leaking raw SDK details.

- [ ] **Step 8: Enforce final cleanup as a hard gate**

Run: `FT_MODE=test BT_G011_TEST_TRADING_ACCEPT=1 ./scripts/g011-test-acceptance`

Expected: PASS only when the selected acceptance accounts have zero test-created open orders, zero test-created positions, zero uncertain commands, zero active acceptance reservations, and closed Funding lineage. When cleanup cannot be proven, the script exits nonzero, prints only safe identifiers plus blocker codes, and leaves G011 incomplete.

- [ ] **Step 9: Independently verify accounting math**

For each filled order compute from normalized fills:

```text
positionQuantity = sum(signed fill quantity)
weightedEntry = sum(abs(fill quantity) * fill price) / sum(abs(fill quantity)) for opening fills
perpUnrealizedPnL = signed position quantity * (mark price - weighted entry)
grossRealizedPnL = sum(sign(open lot quantity) * matched close quantity * (close price - matched entry price))
tradingFees = sum(normalized fees in the position settlement currency)
netRealizedPnL = grossRealizedPnL - tradingFees
fundingPnL = sum(normalized funding payments attributed to the lineage)
netPnL = netRealizedPnL + unrealizedPnL + fundingPnL
```

Compare independently computed decimals to Platform portfolio and ledger projections using instrument currency and increment tolerances. Record discrepancies as blockers; never weaken tolerances to force PASS.

- [ ] **Step 10: Write the redacted acceptance report and commit it**

The report records Test mode, run ID, product flows, safe order/lineage identifiers, venue-native environment labels, notional maxima, fills/fees/funding/PnL comparison, restart evidence, abnormal-path outcomes, and final cleanup counts. It must not contain raw references, credentials, provider paths, tokens, signatures, or SDK payloads.

```bash
git add docs/verification/2026-08-09-test-trading-acceptance.md
git commit -m "test: verify bounded platform trading in Test mode" -m "Constraint: every order or funding leg is at most 100 USDT and Live writes remain disabled" -m "Tested: FT_MODE=test BT_G011_TEST_TRADING_ACCEPT=1 ./scripts/g011-test-acceptance"
```

The Ultragoal leader calls `get_goal` and checkpoints G011 with the fresh active aggregate snapshot plus report/cleanup evidence. Do not call `update_goal` because G012 remains.

### Task 17: Execute G012 Cleanup, Independent Review, and Finalization

**Files:**
- Modify: `docs/guides/docker-free-local-platform.md`
- Modify: `docs/reference/testing.md`
- Modify: `README.md`
- Modify only when the clean Test cutover and cleanup evidence authorize it: `migration/manifest.json`
- Modify through Ultragoal leader checkpoint only: `<omx-state>/ultragoal/ledger.jsonl`

- [ ] **Step 1: Confirm final operational state before cleanup**

Run: `FT_MODE=test ./scripts/dev-platform status`

Run: `FT_MODE=test BT_DEV_REAL_STACK_ACCEPTANCE=1 make test-real`

Run: `FT_MODE=test BT_DEV_REAL_BROWSER_ACCEPTANCE=1 npm --prefix web run test:e2e:real`

Run: `FT_MODE=test BT_G011_TEST_TRADING_ACCEPT=1 ./scripts/g011-test-acceptance --cleanup-check-only`

Expected: PASS with new Platform active, source writer fenced, zero acceptance exposure/blockers, dynamic Gateway accounts restored, and no Live-write enablement.

- [ ] **Step 2: Prepare retirement only after all reversible gates are clean**

Run: `FT_MODE=test BT_G010_CUTOVER_ACCEPT=1 ./scripts/g010-cutover --prepare-retirement`

Expected: workflow reaches `retirement_ready` only when activation evidence, elapsed rollback window, post-G011 cleanup, restart verification, and source-fenced proofs are all current. The command does not delete repository history, Secret Store envelopes still referenced by retiring generations, or external credentials.

- [ ] **Step 3: Run the final implementation verification suite**

Run: `make test-platform-offline`

Run: `FT_MODE=test BT_DEV_REAL_STACK_ACCEPTANCE=1 make test-real`

Run: `FT_MODE=test BT_DEV_REAL_BROWSER_ACCEPTANCE=1 npm --prefix web run test:e2e:real`

Expected: PASS after retirement preparation.

- [ ] **Step 4: Run the required changed-file cleanup pass and repeat verification**

Invoke `oh-my-codex:ai-slop-cleaner` on changed implementation and documentation files only. Accept only behavior-preserving clarity changes, then rerun `make test-platform-offline`, the real-stack gate, and the real-browser gate. Record the cleaner report and exact post-cleaner commands for the Ultragoal quality gate.

- [ ] **Step 5: Run independent code-reviewer and architect gates**

Invoke `oh-my-codex:code-review` with distinct `code-reviewer` and `architect` agents. Require `recommendation: APPROVE`, `architectStatus: CLEAR`, and completed independent evidence from both roles. Any correctness, security, migration, accounting, UI, or missing-test finding creates a new blocker story through `omx ultragoal record-review-blockers`; do not complete G012 while review is non-clean.

- [ ] **Step 6: Update final operator and user documentation**

Document Platform authentication, Test/Live separation, account/route creation, write-only credentials, Gateway discovery, shared public connectors, route readiness, Manual and Funding workflows, restart/recovery, Test acceptance caps, Live default denial, troubleshooting codes, backup/restore, and retirement status. State supported venue/profile combinations from the executable route registry rather than duplicating an unchecked prose list.

- [ ] **Step 7: Commit final documentation and clean evidence**

```bash
git add README.md docs/guides/docker-free-local-platform.md docs/reference/testing.md migration/manifest.json
git commit -m "docs: finalize product platform operations" -m "Constraint: Live private writes remain default denied" -m "Tested: make test-platform-offline; FT_MODE=test BT_DEV_REAL_STACK_ACCEPTANCE=1 make test-real; FT_MODE=test BT_DEV_REAL_BROWSER_ACCEPTANCE=1 npm --prefix web run test:e2e:real"
```

- [ ] **Step 8: Complete the aggregate Ultragoal only after the final gate is clean**

Call `get_goal`, then `update_goal({status: "complete"})`, then `get_goal` again. Checkpoint G012 with the fresh complete snapshot and a structured quality-gate JSON containing the cleaner report, post-cleaner verification commands/evidence, code-reviewer APPROVE evidence, and architect CLEAR evidence. Confirm `omx ultragoal status` reports every non-superseded story complete.

## Final Completion Evidence

Completion requires all of the following together:

- Production Platform uses only a scoped `CredentialWriter`; production Gateway uses only a scoped `CredentialResolver`; exchange credentials are absent from normal Gateway YAML/environment configuration.
- Healthy zero-account Test startup is control-plane ready, while absent/unready routes cannot trade.
- Two accounts at one venue share one public connector and separate private runtime groups and fences.
- One account can promote multiple routes only as a complete generation.
- Operation replay, rotation, restart, stream-gap, drain, removal, tombstone, and handoff crash windows have deterministic passing tests.
- Test/Live isolation passes at key, directory, database, API, profile, runtime, idempotency, and write-policy boundaries.
- Browser/API/log/metrics/trace/audit/screenshot/support output contains no credential value, raw opaque reference, provider path, sensitive SDK error, or Gateway token.
- Funding and Manual products continue to consume shared Platform/Gateway capabilities without product-specific Gateway code.
- G010 no-trade cutover, G011 bounded Test trading and PnL/accounting verification, and G012 cleanup/review each have durable checkpoint evidence.

## Specification Coverage Index

| Approved requirement | Implementation checkpoints |
| --- | --- |
| Scoped writer/resolver and encrypted provider | Tasks 1-3 |
| Strict all-venue credential bundles | Tasks 1-2 |
| Test/Live key, namespace, database, token, profile, and write isolation | Tasks 2-4, 14-17 |
| Platform account routes and durable credential operations | Tasks 5-6 |
| Immutable pending/active snapshots and tombstones | Task 7 |
| Multi-route candidate groups and atomic promotion | Task 8 |
| Full route fence and handoff linearization | Task 9 |
| Shared public connectors and hierarchical budgets | Task 10 |
| Platform-to-Gateway reconciliation and redacted runtime truth | Task 11 |
| Typed write-only browser account management | Task 12 |
| Safe migration, provisioning, and legacy backfill | Tasks 13-15 |
| Zero-account readiness and dynamic account ownership | Tasks 3, 8, 11, 14 |
| Rotation, restart, stream-gap, drain, removal, and recovery | Tasks 6-9, 11, 14-17 |
| Manual/Funding user workflows, accounting, PnL, and abnormal paths | Task 16 |
| Cleanup, independent review, and durable completion evidence | Task 17 |
