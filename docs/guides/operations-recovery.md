# Operations and recovery

> Canonical language: English. [Chinese mirror](../zh-CN/guides/operations-recovery.md)

The supported deployment is the existing single-host Compose stack: one
PostgreSQL instance, Gateway, Trading, Auth edge and Web. The Go image also
ships `gateway-market` and `gateway-monitor`; no separate monitoring service
is needed. Published ports bind to loopback.

## Start and diagnose

`make up` generates missing service credentials, builds the images, runs the
three migrators, starts the services and checks readiness. `make down` keeps the
database volume. `make down-hard` deletes it and is only appropriate for data you
intend to discard. An existing database with schema/role drift needs inspection
and a planned migration, not deletion as a troubleshooting shortcut.

Venue credentials are encrypted in PostgreSQL. Keep `.secrets/stack/secret_master_key`
with the database backup: replacing it does not decrypt existing accounts. The
remaining files in `.secrets/stack` are service tokens and login bootstrap
material. Nothing uses `.secrets/store` any more.

`make doctor` checks Docker, service-key files, the PostgreSQL role layout and
HTTP readiness. It reports public quotes as **not checked** unless a specific
instrument is requested:

```sh
go run ./internal/devsetup/cmd/doctor -instrument 'binance:live:spot:BTC-USDT'
```

Use an exact ID from `gateway-market`'s catalog. This diagnostic opens one BBO
snapshot, validates its source timestamp and releases the subscription. A
healthy process, a successful catalog load and a fresh quote are separate facts.

`gateway_market_hot_instruments{venue,market_family}` counts distinct instruments
with active consumer demand. It is not a connection count or evidence that every
derivative dataset has a subscription; the old `gateway_market_hot_subscriptions`
metric incorrectly duplicated this count across datasets and has been removed.

If venue access requires a proxy, configure it explicitly:

```sh
GATEWAY_HTTP_PROXY=http://127.0.0.1:10900 make up
```

Bootstrap rewrites loopback for Docker's `host.docker.internal`. Go does not use
`ALL_PROXY` for these HTTP/WebSocket clients; a host-launched Gateway needs
`HTTP_PROXY`, `HTTPS_PROXY` and an appropriate `NO_PROXY`. Image dependency
fetches have a separate optional `BOLTER_DOCKER_HTTP_PROXY` build argument. A
proxy that accepts TCP does not prove a venue route works; use the quote diagnostic.

## Unknown orders

An uncertain result is not a retry signal. Recovery only queries the existing
order identity. The order-state tooltip exposes the persisted task's reason,
attempt count, last diagnostic time and confirmed Gateway state/version. Query
failures preserve prior fills and do not turn an order into `failed`.

Keep the command, client-order and Gateway-order IDs. Check exchange orders,
fills and current positions before deciding to place another order. The UI has
no action to invent a terminal failure or automatically replace an unknown
order. A newly created command that never began sending expires after 30 seconds;
this is distinct from one whose send outcome is unknown.

Account deletion removes local credentials and connections; it does not cancel
orders or close positions. Historical command and fill evidence remains queryable.

## Backup and restore

Use a private directory outside the repository. Stop all four application
containers so the three database dumps describe one maintenance point. Keep
PostgreSQL running while taking the dumps:

```sh
umask 077
backup_dir="$HOME/bolter-backups/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$backup_dir"
docker compose --env-file .env.stack stop web authedge trading gateway
for database in bolter_gateway bolter_trading bolter_authedge; do
  docker compose --env-file .env.stack exec -T postgres \
    pg_dump -U bolter_bootstrap -Fc "$database" > "$backup_dir/$database.dump" || exit 1
done
cp -R .secrets "$backup_dir/.secrets"
cp .env.stack "$backup_dir/.env.stack"
cp docker/gateway.docker.yaml "$backup_dir/gateway.yaml"
docker compose --env-file .env.stack start gateway trading authedge web
make doctor
```

Also retain the exact source commit/image versions and any custom Gateway
configuration selected through `GATEWAY_CONFIG_FILE`. Store the backup on a
separate durable device; it contains account secrets and session data.

Restore into a **new empty deployment**, leaving the original data intact:

1. Use the recorded code/images. Restore `.secrets` before starting any application;
   directories must be 0700 and files 0600. Adjust copied `.env.stack` paths, host
   UID and ports for the new machine. Restore the custom Gateway config if used.
2. Start only PostgreSQL: `docker compose --env-file .env.stack up -d --wait postgres`.
   Its init script creates the required databases, owners and roles.
3. Restore each custom-format dump into its corresponding empty database using
   `docker compose --env-file .env.stack exec -T postgres pg_restore -U bolter_bootstrap
   --exit-on-error -d bolter_gateway < "$backup_dir/bolter_gateway.dump"`, and repeat
   for Trading and Auth edge. Preserve ownership and ACLs; do not use `--clean`
   against an existing deployment.
4. Run `make up` to apply checksum-verified migrations and readiness checks, then
   query current exchange state. Do not treat restored local commands as new intents.

`make test-compose-recovery` rehearses outages and a three-database restore in a
throwaway project. `--public-seconds 180` on `scripts/acceptance/stack_recovery.py`
also runs a bounded Binance BTC spot / OKX BTC perpetual monitor through restarts,
records resource samples and then removes all test containers, volumes and keys.
This requires public network access but no venue credentials. The report states
what was observed; a three-minute sample is not a long-duration reliability claim.
