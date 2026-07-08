# P0 Engineering Baseline and CI Hygiene Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the repository's default test path offline deterministic, document the standard verification workflow, and establish the review gates needed before product-model and contract generalization.

**Architecture:** Add an explicit live-read test gate in `internal/testenv`, then migrate live public/private read tests to that gate while preserving live-write tests behind their existing venue-specific write flags. Keep local unit, fixture, and mock-server tests in the default suite. Add a small `Makefile` and baseline docs so later phases share one command and review vocabulary.

**Tech Stack:** Go 1.26, standard `testing`, `net/http/httptest`, `gorilla/websocket` test servers already present in the repo, POSIX `make`, Markdown docs.

---

## Scope

P0 is intentionally narrow. It does not change `core`, `runtime`, adapter
contracts, portfolio accounting, order semantics, or product support. It only
stabilizes the development floor that later phases depend on.

## File Structure

- Modify `internal/testenv/testenv.go`: add a live-read gate helper.
- Modify `internal/testenv/testenv_test.go`: prove live-read tests skip unless explicitly enabled.
- Modify venue test helpers under `sdk/*` and `adapter/*`: route live reads through the new helper.
- Modify `sdk/okx/announcement_test.go`: replace the default live announcement test with an `httptest` fixture.
- Modify `sdk/okx/ws_market_test.go`: keep candle subscription coverage local.
- Create `Makefile`: standard verification commands.
- Create `docs/testing-strategy.md`: test levels, live-test flags, and commands.
- Create `docs/review-checklist.md`: phase review checklist.
- Modify `README.md`: point users at the deterministic test commands and live flags.

## Live Test Policy

Default tests must be offline. These helpers define the intended behavior:

- `testenv.RequireLiveRead(t, vars...)`: skips unless `BOLTER_ENABLE_LIVE_READ_TESTS=1`; then requires any listed env vars.
- `testenv.RequireLiveWrite(t, enableVar, vars...)`: keeps the existing explicit venue write gate, such as `OKX_ENABLE_LIVE_WRITE_TESTS=1`.
- `testenv.RequireLiveCredentials(t, vars...)`: remains as a low-level credential helper for compatibility, but new live-read tests should not call it directly.

Live write helpers should build credentialed clients directly after
`RequireLiveWrite` so enabling write tests does not also require
`BOLTER_ENABLE_LIVE_READ_TESTS`.

---

### Task 1: Add the Shared Live-Read Gate

**Files:**
- Modify: `internal/testenv/testenv.go`
- Modify: `internal/testenv/testenv_test.go`

- [ ] **Step 1: Write the failing tests**

Add these tests to `internal/testenv/testenv_test.go` after
`TestRequireLiveCredentialsSkipsWhenRequiredEnvMissing`:

```go
func TestRequireLiveReadSkipsWithoutEnableFlag(t *testing.T) {
	t.Setenv("BOLTER_ENABLE_LIVE_READ_TESTS", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireLiveRead(t)
		t.Fatalf("expected RequireLiveRead to skip without BOLTER_ENABLE_LIVE_READ_TESTS=1")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireLiveReadSkipsWhenRequiredEnvMissing(t *testing.T) {
	t.Setenv("BOLTER_ENABLE_LIVE_READ_TESTS", "1")
	t.Setenv("TESTENV_REQUIRED_VAR", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireLiveRead(t, "TESTENV_REQUIRED_VAR")
		t.Fatalf("expected RequireLiveRead to skip when required env is missing")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireLiveReadAllowsEnabledReadWithoutCredentials(t *testing.T) {
	t.Setenv("BOLTER_ENABLE_LIVE_READ_TESTS", "1")

	RequireLiveRead(t)
}
```

- [ ] **Step 2: Run the tests and verify they fail**

Run:

```sh
go test ./internal/testenv -run RequireLiveRead -count=1
```

Expected: FAIL with an `undefined: RequireLiveRead` compile error.

- [ ] **Step 3: Implement the live-read helper**

Add this function to `internal/testenv/testenv.go` immediately after
`RequireLiveCredentials`:

```go
func RequireLiveRead(t testing.TB, vars ...string) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: live read test excluded by -short")
	}
	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("load repo .env: %v", err)
	}
	if os.Getenv("BOLTER_ENABLE_LIVE_READ_TESTS") != "1" {
		t.Skip("skipping live read test: set BOLTER_ENABLE_LIVE_READ_TESTS=1 to enable real exchange read execution")
	}
	RequireEnv(t, vars...)
}
```

- [ ] **Step 4: Run the tests and verify they pass**

Run:

```sh
go test ./internal/testenv -run 'RequireLive(Read|Credentials|Write)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/testenv/testenv.go internal/testenv/testenv_test.go
git commit -m "Gate live read tests explicitly" \
  -m "Constraint: Default tests must not depend on network, credentials, or live exchange state." \
  -m "Rejected: Reusing RequireLiveCredentials as the live-read gate | Write tests already compose it indirectly and should not require the global read flag." \
  -m "Confidence: high" \
  -m "Scope-risk: narrow" \
  -m "Directive: New live read tests must call RequireLiveRead instead of RequireLiveCredentials." \
  -m "Tested: go test ./internal/testenv -run 'RequireLive(Read|Credentials|Write)' -count=1" \
  -m "Not-tested: Venue packages are migrated in following commits."
```

---

### Task 2: Make OKX Tests Offline by Default

**Files:**
- Modify: `sdk/okx/client_test.go`
- Modify: `sdk/okx/announcement_test.go`
- Modify: `sdk/okx/market_test.go`
- Modify: `sdk/okx/ws_method_test.go`
- Modify: `sdk/okx/ws_market_test.go`
- Modify: `sdk/okx/ws_order_test.go`
- Test: `sdk/okx/*_test.go`

- [ ] **Step 1: Update the OKX live helpers**

In `sdk/okx/client_test.go`, replace the const/helper block with this code:

```go
const (
	okxLiveWriteFlag = "OKX_ENABLE_LIVE_WRITE_TESTS"
	okxSpotInstID    = "BTC-USDT"
	okxSwapInstID    = "BTC-USDT-SWAP"
)

func newLiveClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t)
	return NewClient()
}

func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t, "OKX_API_KEY", "OKX_API_SECRET", "OKX_API_PASSPHRASE")
	return NewClient().WithCredentials(os.Getenv("OKX_API_KEY"), os.Getenv("OKX_API_SECRET"), os.Getenv("OKX_API_PASSPHRASE"))
}

func requireOKXLiveWrite(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveWrite(t, okxLiveWriteFlag, "OKX_API_KEY", "OKX_API_SECRET", "OKX_API_PASSPHRASE")
	return NewClient().WithCredentials(os.Getenv("OKX_API_KEY"), os.Getenv("OKX_API_SECRET"), os.Getenv("OKX_API_PASSPHRASE"))
}
```

- [ ] **Step 2: Update OKX REST live call sites**

Run:

```sh
perl -0pi -e 's/newLiveClient\(\)/newLiveClient(t)/g' \
  sdk/okx/announcement_test.go \
  sdk/okx/client_test.go \
  sdk/okx/market_test.go
```

Expected: `rg -n 'newLiveClient\\(\\)' sdk/okx/announcement_test.go sdk/okx/client_test.go sdk/okx/market_test.go` returns no matches except the helper definition if it is searched without escaping `t`.

- [ ] **Step 3: Keep announcement parsing covered without live API**

Replace `sdk/okx/announcement_test.go` with:

```go
package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_GetAnnouncementsBuildsPublicQuery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v5/support/announcements" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("annType"); got != "new_crypto" {
			t.Fatalf("annType=%q, want new_crypto", got)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"details":[{"title":"Listing notice","url":"https://www.okx.com/help/listing","annType":"new_crypto","pTime":"1000","businessPTime":"900"}],"totalPage":"1"}]}`))
	}))
	defer srv.Close()

	client := NewClient()
	client.BaseURL = srv.URL
	announcements, err := client.GetAnnouncements(context.Background(), "new_crypto")
	if err != nil {
		t.Fatalf("GetAnnouncements: %v", err)
	}
	if len(announcements) != 1 || announcements[0].Title != "Listing notice" || announcements[0].AnnType != "new_crypto" {
		t.Fatalf("unexpected announcements: %+v", announcements)
	}
}
```

- [ ] **Step 4: Gate OKX websocket live helpers**

In `sdk/okx/ws_method_test.go`, replace `newLivePublicOKXWSClient` and
`newLivePrivateOKXWSClient` with:

```go
func newLivePublicOKXWSClient(t *testing.T) *WSClient {
	t.Helper()
	testenv.RequireLiveRead(t)
	ctx, cancel := context.WithCancel(context.Background())
	client := NewWSClient(ctx)
	require.NoError(t, client.Connect())
	t.Cleanup(func() {
		cancel()
		if client.Conn != nil {
			_ = client.Conn.Close()
		}
	})
	return client
}

func newLivePrivateOKXWSClient(t *testing.T) *WSClient {
	t.Helper()
	testenv.RequireLiveRead(t, "OKX_API_KEY", "OKX_API_SECRET", "OKX_API_PASSPHRASE")
	ctx, cancel := context.WithCancel(context.Background())
	client := NewWSClient(ctx).WithCredentials(os.Getenv("OKX_API_KEY"), os.Getenv("OKX_API_SECRET"), os.Getenv("OKX_API_PASSPHRASE"))
	require.NoError(t, client.Connect())
	t.Cleanup(func() {
		cancel()
		if client.Conn != nil {
			_ = client.Conn.Close()
		}
	})
	return client
}
```

- [ ] **Step 5: Keep candle subscription coverage local**

In `sdk/okx/ws_market_test.go`, replace only the first line inside
`TestWSClient_SubscribeCandles`:

```go
client := newLocalPublicOKXWSClient(t)
```

The full test should read:

```go
func TestWSClient_SubscribeCandles(t *testing.T) {
	client := newLocalPublicOKXWSClient(t)

	if err := client.SubscribeCandles(okxSpotInstID, "candle1m", func(Candle) {}); err != nil {
		t.Fatalf("SubscribeCandles: %v", err)
	}
	if client.Subs[WsSubscribeArgs{Channel: "candle1m", InstId: okxSpotInstID}] == nil {
		t.Fatalf("expected candles subscription to be registered")
	}
}
```

- [ ] **Step 6: Avoid the public-read gate inside OKX write setup**

In `sdk/okx/ws_order_test.go`, replace:

```go
insts, err := newLiveClient(t).GetInstruments(context.Background(), instType)
```

with:

```go
insts, err := NewClient().GetInstruments(context.Background(), instType)
```

This function is called only after `newLiveWriteOKXWSClient` has already passed
`testenv.RequireLiveWrite`, so it is still explicitly gated.

- [ ] **Step 7: Run OKX package tests without live flags**

Run:

```sh
go test ./sdk/okx -count=1
```

Expected: PASS. Live OKX read tests should be skipped by default. Local
`httptest` and local websocket tests should still run.

- [ ] **Step 8: Run the two previously failing tests directly**

Run:

```sh
go test ./sdk/okx -run 'TestClient_GetAnnouncementsBuildsPublicQuery|TestWSClient_SubscribeCandles' -count=1
```

Expected: PASS without network.

- [ ] **Step 9: Commit**

```sh
git add sdk/okx/client_test.go sdk/okx/announcement_test.go sdk/okx/market_test.go sdk/okx/ws_method_test.go sdk/okx/ws_market_test.go sdk/okx/ws_order_test.go
git commit -m "Make OKX tests offline by default" \
  -m "Constraint: Public OKX tests previously depended on current exchange behavior and websocket channel availability." \
  -m "Rejected: Removing the coverage | Local HTTP and websocket fixtures keep parsing and subscription behavior covered." \
  -m "Confidence: high" \
  -m "Scope-risk: moderate" \
  -m "Directive: Add new OKX live reads only through testenv.RequireLiveRead." \
  -m "Tested: go test ./sdk/okx -count=1; go test ./sdk/okx -run 'TestClient_GetAnnouncementsBuildsPublicQuery|TestWSClient_SubscribeCandles' -count=1" \
  -m "Not-tested: Real OKX live reads require BOLTER_ENABLE_LIVE_READ_TESTS=1."
```

---

### Task 3: Migrate Remaining Venue Live-Read Helpers

**Files:**
- Modify: `adapter/binance/perp/live_test.go`
- Modify: `adapter/okx/perp/live_test.go`
- Modify: `sdk/binance/spot/client_test.go`
- Modify: `sdk/binance/perp/client_test.go`
- Modify: `sdk/binance/margin/client_test.go`
- Modify: `sdk/binance/subaccount/client_test.go`
- Modify: `sdk/binance/portfolio/client_test.go`
- Modify: `sdk/bitget/client_test.go`
- Modify: `sdk/bybit/client_test.go`
- Modify: `sdk/hyperliquid/spot/client_test.go`
- Modify: `sdk/hyperliquid/perp/market_test.go`
- Modify: `sdk/hyperliquid/perp/client_test.go`
- Modify: `sdk/lighter/test_helpers_test.go`
- Modify: `sdk/grvt/test_helpers_test.go`
- Modify any test file found by the audit commands in Step 5.

- [ ] **Step 1: Update helper pattern for public live clients**

For packages that define `func newLiveClient() *Client`, change the helper to
accept `t *testing.T`, call `testenv.RequireLiveRead(t)`, and update call sites
from `newLiveClient()` to `newLiveClient(t)`.

Use this exact shape for Binance spot, Binance perp, Bitget, Bybit, Lighter,
Hyperliquid spot, Hyperliquid perp, and GRVT, adjusting the package-local return
expression as needed:

```go
func newLiveClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t)
	return NewClient()
}
```

For Hyperliquid spot:

```go
func newLiveClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t)
	return NewClient(hyperliquid.NewClient())
}
```

For Hyperliquid perp:

```go
func newLiveClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t)
	return NewClient(hyperliquid.NewClient())
}
```

For GRVT:

```go
func newLiveClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t)
	client := NewClient()
	client.HttpClient.Timeout = 20 * time.Second
	return client
}
```

- [ ] **Step 2: Update helper pattern for private live-read clients**

For private read helpers, replace `testenv.RequireLiveCredentials` with
`testenv.RequireLiveRead` and keep the same required credential names.

Example for Binance spot:

```go
func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t, "BINANCE_API_KEY", "BINANCE_SECRET_KEY")
	return NewClient().WithCredentials(
		os.Getenv("BINANCE_API_KEY"),
		os.Getenv("BINANCE_SECRET_KEY"),
	)
}
```

Example for Bybit:

```go
func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t, "BYBIT_API_KEY", "BYBIT_SECRET_KEY")
	return NewClient().WithCredentials(os.Getenv("BYBIT_API_KEY"), os.Getenv("BYBIT_SECRET_KEY"))
}
```

Example for Lighter:

```go
func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t, "LIGHTER_PRIVATE_KEY", "LIGHTER_ACCOUNT_INDEX", "LIGHTER_KEY_INDEX")
	accountIndex, err := strconv.ParseInt(os.Getenv("LIGHTER_ACCOUNT_INDEX"), 10, 64)
	if err != nil {
		t.Fatalf("parse LIGHTER_ACCOUNT_INDEX: %v", err)
	}
	keyIndex64, err := strconv.ParseUint(os.Getenv("LIGHTER_KEY_INDEX"), 10, 8)
	if err != nil {
		t.Fatalf("parse LIGHTER_KEY_INDEX: %v", err)
	}
	return NewClient().WithCredentials(os.Getenv("LIGHTER_PRIVATE_KEY"), accountIndex, uint8(keyIndex64))
}
```

- [ ] **Step 3: Keep write helpers independent from the read flag**

Any helper named `require*LiveWrite` should call `testenv.RequireLiveWrite` and
then construct a credentialed client directly. Do not call a `newLivePrivateClient`
that now uses `RequireLiveRead`.

Example for Binance spot:

```go
func requireBinanceSpotLiveWrite(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveWrite(t, binanceSpotLiveWriteFlag, "BINANCE_API_KEY", "BINANCE_SECRET_KEY")
	return NewClient().WithCredentials(
		os.Getenv("BINANCE_API_KEY"),
		os.Getenv("BINANCE_SECRET_KEY"),
	)
}
```

Example for Lighter:

```go
func requireLighterLiveWrite(t *testing.T, vars ...string) *Client {
	t.Helper()
	required := append([]string{"LIGHTER_PRIVATE_KEY", "LIGHTER_ACCOUNT_INDEX", "LIGHTER_KEY_INDEX"}, vars...)
	testenv.RequireLiveWrite(t, lighterLiveWriteFlag, required...)
	accountIndex, err := strconv.ParseInt(os.Getenv("LIGHTER_ACCOUNT_INDEX"), 10, 64)
	if err != nil {
		t.Fatalf("parse LIGHTER_ACCOUNT_INDEX: %v", err)
	}
	keyIndex64, err := strconv.ParseUint(os.Getenv("LIGHTER_KEY_INDEX"), 10, 8)
	if err != nil {
		t.Fatalf("parse LIGHTER_KEY_INDEX: %v", err)
	}
	return NewClient().WithCredentials(os.Getenv("LIGHTER_PRIVATE_KEY"), accountIndex, uint8(keyIndex64))
}
```

- [ ] **Step 4: Gate adapter live smoke tests**

In `adapter/binance/perp/live_test.go`, replace the credential gate with:

```go
testenv.RequireLiveRead(t, "BINANCE_API_KEY", "BINANCE_API_SECRET")
```

In `adapter/okx/perp/live_test.go`, replace the credential gate with:

```go
testenv.RequireLiveRead(t, "OKX_API_KEY", "OKX_API_SECRET", "OKX_API_PASSPHRASE")
```

- [ ] **Step 5: Audit remaining live-read smells**

Run:

```sh
rg -n 'func newLiveClient\(\)' sdk adapter -g '*_test.go'
rg -n 'RequireLiveCredentials' sdk adapter -g '*_test.go'
```

Expected:

- The first command returns no matches.
- The second command returns only lines inside write-gated helpers or tests that
  are immediately preceded by `testenv.RequireLiveWrite` in the same function.

For any remaining read test, replace `RequireLiveCredentials` with
`RequireLiveRead`. For any write test that uses `RequireLiveCredentials` only to
check extra order IDs, sizes, or prices after `RequireLiveWrite`, replace it
with `testenv.RequireEnv`.

Example:

```go
testenv.RequireEnv(t, "BINANCE_SPOT_TEST_ORDER_QTY", "BINANCE_SPOT_TEST_ORDER_PRICE")
```

- [ ] **Step 6: Run representative packages**

Run:

```sh
go test ./sdk/binance/spot ./sdk/binance/perp ./sdk/bitget ./sdk/bybit ./sdk/hyperliquid/spot ./sdk/hyperliquid/perp ./sdk/lighter ./sdk/grvt ./adapter/binance/perp ./adapter/okx/perp -count=1
```

Expected: PASS without network. Live read tests should skip by default.

- [ ] **Step 7: Commit**

```sh
git add adapter/binance/perp/live_test.go adapter/okx/perp/live_test.go sdk/binance sdk/bitget sdk/bybit sdk/hyperliquid sdk/lighter sdk/grvt
git commit -m "Gate venue live read tests" \
  -m "Constraint: Default package tests must stay stable even when a developer has credentials in .env." \
  -m "Rejected: Relying on missing credentials to skip live reads | Local developer machines often have credentials configured." \
  -m "Confidence: medium" \
  -m "Scope-risk: moderate" \
  -m "Directive: Live writes use venue write flags; live reads use BOLTER_ENABLE_LIVE_READ_TESTS." \
  -m "Tested: go test ./sdk/binance/spot ./sdk/binance/perp ./sdk/bitget ./sdk/bybit ./sdk/hyperliquid/spot ./sdk/hyperliquid/perp ./sdk/lighter ./sdk/grvt ./adapter/binance/perp ./adapter/okx/perp -count=1" \
  -m "Not-tested: Real live-read endpoints require BOLTER_ENABLE_LIVE_READ_TESTS=1."
```

---

### Task 4: Add Standard Verification Commands

**Files:**
- Create: `Makefile`

- [ ] **Step 1: Create the Makefile**

Create `Makefile` with:

```make
.PHONY: test test-race test-core test-adapter test-sdk test-live-read

test:
	go test ./...

test-race:
	go test -race ./runtime/...

test-core:
	go test ./core/... ./runtime/... ./strategy/...

test-adapter:
	go test ./adapter/...

test-sdk:
	go test ./sdk/...

test-live-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go test ./sdk/... ./adapter/...
```

- [ ] **Step 2: Verify the targets**

Run:

```sh
make test-core
make test-adapter
make test-sdk
```

Expected: each command exits 0 without requiring network or credentials.

- [ ] **Step 3: Commit**

```sh
git add Makefile
git commit -m "Add standard verification targets" \
  -m "Constraint: Later phases need one shared command vocabulary for test and review gates." \
  -m "Rejected: Only documenting raw go test commands | Make targets reduce drift across agents and developers." \
  -m "Confidence: high" \
  -m "Scope-risk: narrow" \
  -m "Directive: Keep default make test offline; add explicit targets for live or write tests." \
  -m "Tested: make test-core; make test-adapter; make test-sdk" \
  -m "Not-tested: make test-live-read requires explicit live-read opt-in."
```

---

### Task 5: Add Testing and Review Documentation

**Files:**
- Create: `docs/testing-strategy.md`
- Create: `docs/review-checklist.md`
- Modify: `README.md`

- [ ] **Step 1: Add the testing strategy doc**

Create `docs/testing-strategy.md` with:

```markdown
# Testing Strategy

BolterTrader's default test suite is offline deterministic. It must not depend
on credentials, public internet, current exchange listings, live market state, or
wall-clock timing beyond local test deadlines.

## Default Commands

```sh
make test
make test-race
make test-core
make test-adapter
make test-sdk
```

## Test Levels

1. Unit tests cover pure model, enum, cache, risk, accounting, conversion, and
   state-machine behavior.
2. Golden fixture tests cover SDK and adapter request/response/stream payloads.
3. Contract conformance tests cover venue-neutral client behavior in
   `core/contract/contracttest`.
4. Scenario tests cover product-level flows such as spot inventory, perp funding,
   future settlement, option premium, reconnect, and reconciliation.
5. Deterministic replay tests feed fixed event streams and assert exact final
   runtime state.
6. Race and lifecycle tests cover runtime goroutine boundaries, cancellation,
   reconnect, and channel closure.
7. Live smoke tests are opt-in and excluded from default verification.

## Live Test Flags

Live read tests require:

```sh
BOLTER_ENABLE_LIVE_READ_TESTS=1 go test ./sdk/... ./adapter/...
```

Live write tests require venue-specific flags in addition to credentials. Examples:

```sh
OKX_ENABLE_LIVE_WRITE_TESTS=1 go test -run Live ./sdk/okx
BINANCE_PERP_ENABLE_LIVE_WRITE_TESTS=1 go test -run Live ./sdk/binance/perp
```

Live write tests may create, modify, cancel, or close real exchange state. They
must remain explicitly gated and must never run from `make test`.

## Fixture Policy

- Prefer `httptest` servers and checked-in payload fixtures for default tests.
- Keep fixtures small and readable.
- Put fixture tests next to the SDK or adapter that owns the payload.
- Document expected balances, positions, and PnL inline in scenario tests.
```

- [ ] **Step 2: Add the review checklist doc**

Create `docs/review-checklist.md` with:

```markdown
# Review Checklist

Use this checklist for every phase and for substantial pull requests.

## Architecture Review

- Runtime imports only `core/{clock,contract,enums,model}` and runtime packages.
- SDK and adapter types do not leak into `core`, `runtime`, or `strategy`.
- Product-specific behavior is explicit in the model or isolated behind a policy
  boundary.
- Venue-specific behavior is contained in `adapter/<venue>` or `sdk/<venue>`.
- The change preserves portable strategy-facing APIs across supported venues.

## Contract and Test Review

- New behavior is covered by unit, fixture, scenario, or conformance tests.
- Default tests are offline deterministic.
- Live read tests call `testenv.RequireLiveRead`.
- Live write tests call `testenv.RequireLiveWrite`.
- Error cases are asserted, not only happy paths.

## Implementation Review

- State mutation remains serialized through the runtime event path.
- Decimal math is used for prices, quantities, balances, and PnL.
- Errors preserve sentinel or typed classification where available.
- Public API changes are minimal and documented.
- No unrelated refactors are mixed into the change.

## Documentation Review

- README or docs describe the behavior users actually get.
- Examples compile or are covered by tests when practical.
- Live-test instructions mention the exact opt-in flags.
- Known gaps are documented in phase plans or roadmap docs.
```

- [ ] **Step 3: Update README testing section**

Replace the current `## Testing` section in `README.md` with:

```markdown
## Testing

Default verification is offline deterministic:

```sh
make test              # go test ./...
make test-race         # runtime race checks
make test-core         # core/runtime/strategy packages
make test-adapter      # adapter contract packages
make test-sdk          # SDK packages without live endpoints
```

Live read tests are opt-in:

```sh
BOLTER_ENABLE_LIVE_READ_TESTS=1 make test-live-read
```

Live write tests are venue-specific and may create, modify, cancel, or close real
exchange state. They require credentials plus an explicit venue write flag:

```sh
OKX_API_KEY=... OKX_API_SECRET=... OKX_API_PASSPHRASE=... OKX_ENABLE_LIVE_WRITE_TESTS=1 go test -run Live ./sdk/okx/
BINANCE_API_KEY=... BINANCE_API_SECRET=... BINANCE_PERP_ENABLE_LIVE_WRITE_TESTS=1 go test -run Live ./sdk/binance/perp/
```

See [`docs/testing-strategy.md`](docs/testing-strategy.md) and
[`docs/review-checklist.md`](docs/review-checklist.md).
```

- [ ] **Step 4: Verify docs links and markdown**

Run:

```sh
rg -n 'BOLTER_ENABLE_LIVE_READ_TESTS|RequireLiveRead|RequireLiveWrite' README.md docs internal sdk adapter
git diff --check
```

Expected: the `rg` command shows the new docs and helper references; `git diff
--check` exits 0.

- [ ] **Step 5: Commit**

```sh
git add README.md docs/testing-strategy.md docs/review-checklist.md
git commit -m "Document the baseline verification workflow" \
  -m "Constraint: The long-term roadmap depends on repeatable local verification and clear review gates." \
  -m "Rejected: Keeping live-test behavior only in helper names | Contributors need explicit command-level docs." \
  -m "Confidence: high" \
  -m "Scope-risk: narrow" \
  -m "Directive: Keep README test commands aligned with Makefile targets." \
  -m "Tested: rg -n 'BOLTER_ENABLE_LIVE_READ_TESTS|RequireLiveRead|RequireLiveWrite' README.md docs internal sdk adapter; git diff --check" \
  -m "Not-tested: Markdown rendering in a browser."
```

---

### Task 6: Final P0 Verification

**Files:**
- Verify all files touched in Tasks 1-5.

- [ ] **Step 1: Run the default suite**

Run:

```sh
make test
```

Expected: PASS without network or credentials.

- [ ] **Step 2: Run runtime race checks**

Run:

```sh
make test-race
```

Expected: PASS.

- [ ] **Step 3: Run the live-gate audit**

Run:

```sh
rg -n 'func newLiveClient\(\)' sdk adapter -g '*_test.go'
rg -n 'RequireLiveCredentials' sdk adapter -g '*_test.go'
```

Expected:

- No zero-argument `newLiveClient()` helper remains.
- Any remaining `RequireLiveCredentials` call is in a write-gated path or should
  be changed to `RequireLiveRead` or `RequireEnv` before P0 is complete.

- [ ] **Step 4: Confirm clean worktree**

Run:

```sh
git status --short --branch
```

Expected: no unstaged or staged changes.

- [ ] **Step 5: Record P0 completion note**

Add a short note to the final PR or merge summary:

```text
P0 complete: default tests are offline deterministic, live reads require
BOLTER_ENABLE_LIVE_READ_TESTS=1, live writes remain venue-flagged, and the
repository now has standard verification and review docs.
```

No separate commit is needed for this step unless the implementation workflow
requires a PR description file.

## Self-Review

- Spec coverage: P0 acceptance requires offline default tests, explicit live
  gating, standard commands, and review documentation. Tasks 1-6 cover those
  requirements.
- Red-flag scan: this plan uses no vague tasks or unspecified file edits.
- Type consistency: the helper name is consistently `RequireLiveRead`; the live
  read flag is consistently `BOLTER_ENABLE_LIVE_READ_TESTS`; write tests keep
  `RequireLiveWrite`.
