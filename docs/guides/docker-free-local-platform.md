# Docker-Free Local Platform

The supported local stack runs directly on the host in Test mode. It does not
use Docker. Account inventory is Platform-owned, and the canonical Gateway
starts with no venue accounts: zero-account inventory is a healthy, write-free
state.

## Prerequisites

Install Go, Node.js with npm, PostgreSQL client and server tools, `curl`,
`lsof`, and `htpasswd`. The browser gate installs locked web dependencies with
`npm ci`.

The Platform requires an initial operator username and bcrypt password hash.
Supply them in your shell or an ignored local environment file. Do not put the
password or its hash in a tracked file.

```sh
export BT_BOOTSTRAP_USERNAME=operator
read -r -s BT_LOCAL_PASSWORD && printf '\n'
export BT_BOOTSTRAP_PASSWORD_HASH="$(htpasswd -bnBC 12 operator "$BT_LOCAL_PASSWORD" | cut -d: -f2-)"
unset BT_LOCAL_PASSWORD
```

## Launch

From the repository root, load the canonical non-secret command template and
start the stack:

```sh
. ./scripts/dev-platform.env.example
make dev-platform-doctor
make dev-platform-up
make dev-platform-status
```

The supervisor creates mode-scoped opaque files beneath
`$FT_LOCAL_RUNTIME_DIR/credentials/test` with permissions `0600`. It does not
print their contents. The Platform receives only the Secret Store source
identifier (`file`), absolute state directory, Test namespace, key version, and
mode-scoped key-file identifier. Key material is never placed in a command
argument, log line, tracked environment template value, or doctor output.
The Test Gateway has no configured venue accounts, and Live private writes
remain disabled.

Use `make dev-platform-logs` for diagnostics and `make dev-platform-down` for
an ordered shutdown. Shutdown drains the funding worker through its fenced,
authenticated work API before stopping processes. `make dev-platform-reset`
removes Test runtime state only after owned processes have stopped.

## Acceptance Gates

`make test-browser` builds and type-checks the web application, runs unit tests,
and runs the fixture-backed Playwright browser suite. It performs no external
writes.

`make test-real` is the **G010 no-trade gate** for an already-running local Test
stack. It fails unless the
operator explicitly attests the gate, the mode is Test, Live writes are off,
all venue-write enable variables are absent, and every supervised service is
ready:

```sh
BT_DEV_REAL_STACK_ACCEPTANCE=1 make test-real
```

This gate performs readiness reads only. It does not activate migration
cutover, retire the source system, configure venue credentials, or submit venue
writes. A zero-account inventory is a valid ready result.

**G011 is a separate Test-write boundary.** G010 evidence never authorizes
G011. Provisioning an account, enabling a private route, validating private
connectivity, or submitting any venue command requires the explicit G011
handoff and its own scoped Test credentials and write attestation. Live mode
and Live private writes remain outside both local gates.
