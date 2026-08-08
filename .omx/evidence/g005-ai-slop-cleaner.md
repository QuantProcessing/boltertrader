# G005 AI Slop Cleanup Report

Scope: Files changed by G005 relative to `5f442592955a26c0e01b4bb2083c7ddf76aeb477`.

Behavior lock: Root and backend full tests, targeted Gateway/backend race tests, Docker-free launcher tests, and real local PostgreSQL integration tests ran before cleanup. Real PostgreSQL exposed stale migration and frozen-intent fixtures; repeated race testing exposed timing-dependent Funding renewal fixtures.

Cleanup plan: Inventory fallback-like code and swallowed errors, then inspect dead code, duplication, naming/error handling, and missing or unstable tests without changing the frozen product, Gateway, or work protocols.

Fallback findings:

- HTTP error-body decoding preserves the non-success HTTP status when the optional structured envelope is malformed. Classified as grounded fail-safe behavior.
- Ignored close, rollback, hash-write, and test-cleanup errors preserve a more relevant primary error or occur on APIs whose writes cannot fail. Classified as grounded cleanup behavior.
- Optional reconciliation capability discovery uses a checked type assertion. Classified as an intentional optional boundary.
- Funding account-id derivation falls back to the platform funding principal only after validated payload/fence paths. Retained because changing it would alter the frozen authority contract.
- No masking fallback, quick-hack marker, TODO/FIXME, placeholder panic, or silent validation bypass was found in the scoped additions.

Passes completed:

1. Dead code: No removable dead path was identified; compilation, vet, and scoped marker searches were clean.
2. Duplication: No safe local duplication removal was identified without widening a protocol boundary.
3. Naming/error handling: Preserved the earlier explicit Funding Live decimal parse error propagation; no further production edit was justified.
4. Test reinforcement:
   - PostgreSQL migration assertions now derive the expected count from the embedded production migration set instead of a stale literal.
   - The two-principal work-intent fixture now persists the exact frozen `funding.open` payload it later submits, preserving strict full-intent validation.
   - Funding renewal tests wait for observed renewal events with a bounded timeout instead of relying on 9 ms lease deadlines and arbitrary sleeps.

Quality gates:

- `go test ./... -count=1` in root: PASS
- `go test ./... -count=1` in backend: PASS
- targeted Gateway race suite: PASS
- targeted backend app/trading/Funding/Manual/PostgreSQL race suite: PASS
- Funding renewal race tests, two cases repeated 20 times: PASS
- `go vet ./...` in root and backend: PASS
- `go mod tidy -diff` in root and backend: PASS
- `go test ./scripts -count=3`: PASS
- `sh -n scripts/dev-platform`: PASS
- Real Gateway PostgreSQL store/order tests: PASS
- Real backend PostgreSQL store tests: PASS
- `gofmt` and `git diff --check`: PASS

Changed files:

- `backend/internal/store/postgres/work_integration_test.go`: migration and frozen-intent fixtures now follow production authority.
- `backend/internal/products/funding/runtime_test.go`: renewal assertions use condition-based synchronization.
- `.omx/evidence/g005-ai-slop-cleaner.md`: records the final bounded cleanup and verification evidence.

UI/design findings: N/A. This G005 cleanup scope contains no frontend implementation changes.

Remaining risks: Live and external venue writes remain intentionally untested. Browser-level product acceptance belongs to the later durable stories and is not claimed by this cleanup pass.

## Runtime readiness contract follow-up (2026-08-08)

Scope: the eight runtime-readiness files changed after commit `a8fb46155e5786e309d250151d443ad29b346eaa`.

Behavior lock:

- The real authenticated Gateway internal handler is consumed through the Platform adapter and strict JSON decoder.
- Runtime status retains detailed `mode` and `accounts`; runtime readiness exposes only `ready`.
- Readiness requires at least one active runtime, fails when any active runtime is unready, ignores intentionally disabled or draining runtimes, and fails closed after manager shutdown.

Cleanup result: passed/no-op. No masking fallback, workaround branch, swallowed error, duplicate execution path, dead code, speculative abstraction, or missing regression test was found in the readiness diff. The exported `RuntimeReadiness` DTO is the shared HTTP contract, while the local `authorityRuntimeStatus` type remains Gateway-owned detail. The OpenAPI parsing helpers are justified by strict response-envelope and schema assertions.

Post-cleaner verification:

- Root and backend `go test ./... -count=1`: PASS.
- Root and backend `go vet ./...`: PASS.
- Root and backend `go mod tidy -diff`: PASS.
- Gateway and backend targeted race suites: PASS.
- Two Funding renewal race cases repeated 20 times: PASS.
- `go test ./scripts -count=3` and `sh -n scripts/dev-platform`: PASS.
- Real Gateway and backend PostgreSQL integration tests on `127.0.0.1:15432`: PASS.
- Scoped `gofmt -l` and `git diff --check`: PASS.

No production file changed during this cleaner pass. Live/private venue writes remain intentionally untested.
