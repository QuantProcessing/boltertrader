# Running the local stack

> Canonical language: English. [Chinese mirror](../zh-CN/getting-started/local-stack.md)

This page owns the local stack's lifecycle: what `make up` starts, how to tell
whether it is healthy, and how to take it down. Install the tools first —
[Prerequisites](prerequisites.md). Then place an order in
[Your first order](first-order.md).

## Start it

```sh
make up
```

Three things happen, in order:

1. `go run ./internal/devsetup/cmd/bootstrap` generates the stack's credentials
   into `.secrets/stack/` and writes `.env.stack`. Running it twice is a no-op.
2. `docker compose --env-file .env.stack -f compose.yaml up -d --build --wait`
   builds and starts the containers, waiting up to 180 seconds for their health
   checks.
3. `make doctor` runs and reports on every part of the stack.

On the first run — and only on the run that generates it — bootstrap prints the
bootstrap operator's username and password. That is the only time it is printed
in readable form: the stack stores nothing but the bcrypt hash. The same value
is also written to `.secrets/stack/bootstrap_password` with mode `0600`. To reset
it, delete `.secrets/stack/bootstrap_password_hash` and run `make up` again.

The default username is `operator`. Override it with `BT_BOOTSTRAP_USERNAME`.

## What runs

Eight containers, of which three exit as soon as they finish:

| Container | Role |
| --- | --- |
| `postgres` | One instance, three databases, one owning role each |
| `gateway-migrate` | Applies the gateway's migrations, then exits |
| `trading-migrate` | Applies the trading service's migrations, then exits |
| `authedge-migrate` | Applies the auth edge's migrations, then exits |
| `gateway` | The shared capability plane |
| `trading` | Manual trading and funding arbitrage |
| `authedge` | Browser sessions and forwarding |
| `web` | Caddy, serving the frontend and reverse-proxying `/api/*` |

Published ports, all bound to `127.0.0.1` and nothing else:

| Address | Service |
| --- | --- |
| `http://127.0.0.1:18090` | Web — this is the one you open |
| `http://127.0.0.1:18091` | Auth edge |
| `http://127.0.0.1:18092` | Trading service |
| `http://127.0.0.1:18080` | Gateway |
| `127.0.0.1:15432` | PostgreSQL |

Only the web port is meant for a browser. The other four are published for
`make doctor` and for debugging.

To move a port, set it on the command that starts the stack:

```sh
POSTGRES_PORT=15433 GATEWAY_PORT=18081 make up
```

The values land in `.env.stack`, and every later `make doctor`, `make logs`, and
`make down` reads them back from there.

## Check it

```sh
make doctor
```

It reports one line per check and never stops early, so a run that finds five
problems says so once. Checks run in dependency order: `docker`, `.env.stack`,
`credentials`, `credential store`, `postgres`, `postgres roles`, `gateway`,
`trading`, `auth edge`, `web`. Each failing line is followed by the command that
fixes it.

A `skip` is not a pass. It means the check could not run because something it
depends on already failed — "postgres is down so nothing downstream was tested"
is a different answer from "the downstream checks passed".

The gateway and trading probes authenticate: they read the generated service
tokens from `.secrets/stack/` and call the services' readiness endpoints. The
auth edge probe expects `401` from `/api/v1/me` without a session, because that
is the correct answer.

## Watch it

```sh
make logs
```

Follows all containers with the last 200 lines of scrollback.

## Stop it

```sh
make down
```

Stops and removes the containers. Volumes survive, so the next `make up` comes
back with the same accounts, orders, and history.

```sh
make down-hard
```

Adds `--volumes`. Everything in PostgreSQL is destroyed, including your accounts
and the venue credentials you registered through the interface. The generated
files in `.secrets/` are not touched, so the bootstrap password stays valid.

## Where the state lives

- **PostgreSQL** holds the gateway's accounts, catalog, and durable orders; the
  trading service's command ledger and projections; and the auth edge's
  sessions. Three databases, one owning role each, and no service reads
  another's tables.
- **`.secrets/stack/`** holds the service tokens, the credential-store master
  key, the gateway's DSN, and the bootstrap password and its hash. Mode `0700`
  on the directory, `0600` on each file.
- **`.secrets/store/`** is the gateway's encrypted venue-credential store. It is
  a bind mount, and it is the only path in the stack a service writes to.
- **`.env.stack`** holds ports, uids, and the paths to those secret files.
  Nothing else. It is not `.env`, and the stack never reads `.env` — see
  [Prerequisites](prerequisites.md).

All four are ignored by Git.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `docker: not on PATH` | Compose plugin missing | Install Docker Desktop or OrbStack |
| `.env.stack: missing or empty` | Bootstrap never ran | `make up` |
| Port checks fail after a clean `make up` | Something else holds the port | Re-run with an override, e.g. `WEB_PORT=18095 make up` |
| `credentials` reports a loosened mode | An editor or backup tool changed it | `make up` re-tightens it |
| `gateway` or `trading` returns a non-200 | The service failed to start | `make logs` has the reason |

Deeper failure modes — a migration that stopped midway, an order whose terminal
state is unconfirmed — belong to
[Operations and recovery](../guides/operations-recovery.md).

## Next

[Your first order](first-order.md) walks the full browser loop against a real
venue account, with real money. To walk the same loop with no venue behind it,
run `npm --prefix web run dev:mock` instead.
