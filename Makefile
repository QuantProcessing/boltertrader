.PHONY: test test-sdk test-browser
.PHONY: test-exchange-offline test-exchange-race test-exchange-redaction test-exchange-quality test-exchange-acceptance test-exchange-external-acceptance test-exchange-binance-demo-acceptance test-exchange-binance-demo-spot test-exchange-binance-demo-perp test-exchange-okx-demo-acceptance test-exchange-okx-demo-spot test-exchange-okx-demo-perp test-exchange-lighter-testnet-acceptance test-exchange-lighter-testnet-spot test-exchange-lighter-testnet-perp test-exchange-hyperliquid-testnet-acceptance test-exchange-hyperliquid-testnet-spot test-exchange-hyperliquid-testnet-perp test-exchange-bybit-demo-acceptance test-exchange-bybit-demo-spot test-exchange-bybit-demo-usdt-perp test-exchange-bybit-demo-usdc-perp test-exchange-bitget-demo-acceptance test-exchange-bitget-demo-spot test-exchange-bitget-demo-usdt-perp test-exchange-bitget-demo-usdc-perp test-exchange-gate-testnet-acceptance test-exchange-gate-testnet-spot test-exchange-gate-testnet-usdt-perp test-exchange-aster-testnet-acceptance test-exchange-aster-testnet-spot test-exchange-aster-testnet-usdt-perp test-exchange-nado-testnet-acceptance test-exchange-nado-testnet-spot test-exchange-nado-testnet-usdt0-perp
.PHONY: test-gateway-contract test-gateway-offline test-gateway-race test-gateway-postgres test-gateway-restart test-gateway-security test-gateway
.PHONY: up down down-hard logs doctor
.PHONY: compose-dsn test-compose-config test-compose-build test-compose-security test-compose-smoke test-compose-persistence test-compose-postgres test-compose
.PHONY: test-gateway-demo-testnet test-gateway-binance-spot-demo test-gateway-binance-usd-perp-demo test-gateway-okx-spot-demo test-gateway-okx-usdt-perp-demo test-gateway-lighter-spot-testnet test-gateway-lighter-perp-testnet test-gateway-hyperliquid-spot-testnet test-gateway-hyperliquid-perp-testnet test-gateway-bybit-spot-demo test-gateway-bybit-usdt-perp-demo test-gateway-bybit-usdc-perp-demo test-gateway-bitget-spot-demo test-gateway-bitget-usdt-perp-demo test-gateway-bitget-usdc-perp-demo test-gateway-gate-spot-testnet test-gateway-gate-usdt-perp-testnet test-gateway-aster-spot-testnet test-gateway-aster-usdt-perp-testnet test-gateway-nado-spot-testnet test-gateway-nado-usdt0-perp-testnet

.NOTPARALLEL:

test:
	go test -short ./...

test-browser:
	npm --prefix web ci
	npm --prefix web run build
	npm --prefix web test
	npm --prefix web run test:e2e

test-sdk:
	go test -short ./sdk/...

test-exchange-offline:
	go test -short ./exchange/... -count=1

test-exchange-race:
	go test -race ./exchange/... -count=1

test-exchange-redaction:
	go test -short ./exchange/... -run 'Test(ConfigFormattingRedactsCredentials|ConstructedClientFormattingRedactsCredentials|ExpandedConstructedClientFormattingRedactsCredentials|AllVenueQueryNormalizersRedactUnderlyingErrors|AllVenueMutationNormalizersRedactVenueMessages|NormalizedErrorKindsAndMetadata)$$' -count=1

test-exchange-quality: test-exchange-offline test-exchange-redaction test-exchange-race

test-gateway-contract:
	go test -short ./gateway/contracts ./gateway/domain ./gateway/capability ./gateway/store -count=1

test-gateway-offline:
	go test -short ./gateway/... ./cmd/exchange-gateway -count=1

test-gateway-race:
	go test -race -short ./gateway/... ./cmd/exchange-gateway -count=1

test-gateway-postgres:
	@test -n "$$GATEWAY_TEST_POSTGRES_DSN" || (echo "GATEWAY_TEST_POSTGRES_DSN is required" >&2; exit 1)
	go test ./gateway/store/postgres/... -run 'Test(Migrate|CommandStore|AuditStore|AccountOwnership|RegisterAccount).*RealPostgres' -count=1

test-gateway-restart:
	@test -n "$$GATEWAY_TEST_POSTGRES_DSN" || (echo "GATEWAY_TEST_POSTGRES_DSN is required" >&2; exit 1)
	go test ./gateway/order/... -run '^TestCoordinatorRealPostgresProcessCrashWindows$$' -count=1

test-gateway-security:
	go test -short ./gateway/contracts ./gateway/config ./gateway/secret ./gateway/store/postgres -run 'Test(GatewayProductionImportBoundary|OpenAPI|ConfigFormatting|RefRejects|EnvProvider|RedactErr|AcceptanceCrosswalk|GatewayOperations)' -count=1

test-gateway: test-gateway-offline test-gateway-race test-gateway-security

# -----------------------------------------------------------------------------
# The local stack.
#
# `make up` is the whole story: it generates credentials and ports into
# .env.stack, then brings up postgres, the three migrators, and the three
# services. `make doctor` says what is wrong when something is. Both delegate to
# Go under internal/devsetup rather than growing shell here — the 663-line script
# these replaced is the reason this file is not allowed to become one again.
#
# Every compose invocation in this file passes --env-file explicitly, including
# the ones that only read metadata. Without it compose auto-loads the
# repository's ./.env, which holds live venue API credentials that would then be
# interpolated into the stack's environment.
# -----------------------------------------------------------------------------
COMPOSE = docker compose --env-file .env.stack -f compose.yaml

up:
	go run ./internal/devsetup/cmd/bootstrap
	$(COMPOSE) up -d --build --wait --wait-timeout 180
	@$(MAKE) --no-print-directory doctor

down:
	@test -f .env.stack || (echo "no .env.stack; nothing to bring down" >&2; exit 0)
	$(COMPOSE) down --remove-orphans

# The volume too, which is the only way an edit to the initdb script takes
# effect: the postgres image runs those scripts once, when the data directory is
# empty, so a surviving volume means a changed role set is never applied.
down-hard:
	@test -f .env.stack || (echo "no .env.stack; nothing to bring down" >&2; exit 0)
	$(COMPOSE) down --volumes --remove-orphans

logs:
	$(COMPOSE) logs --no-color --tail 200

doctor:
	go run ./internal/devsetup/cmd/doctor

# -----------------------------------------------------------------------------
# Container gates.
#
# These run against ephemeral compose projects with their own generated
# credentials, so none of them needs `make up` to have run and none of them
# touches the stack an operator has up. Each one bootstraps a throwaway root with
# the same generator `make up` uses — rather than writing token files in shell —
# because the services validate what they read: the parent directory has to be
# named for BT_MODE, the mode has to be 0600, and the bootstrap hash has to parse
# as bcrypt. A placeholder of the wrong shape fails inside a container at startup,
# which is the least legible place for it to fail.
#
# Every published port is overridden to 0 in the environment, where they beat the
# generated .env.stack, so the kernel assigns them: that is what makes these
# projects safe to run beside a live `make up` and beside each other. BT_SECRET_DIR
# is overridden for a subtler reason — the generated value is relative, and compose
# resolves a relative bind mount against the compose file's directory, which would
# silently mount the repository's own credentials instead of the throwaway ones.
# -----------------------------------------------------------------------------
compose-dsn:
	@test -n "$$BOLTER_COMPOSE_PROJECT" || (echo "BOLTER_COMPOSE_PROJECT is required" >&2; exit 1)
	@test -n "$$BOLTER_COMPOSE_ENV_FILE" || (echo "BOLTER_COMPOSE_ENV_FILE is required" >&2; exit 1)
	@endpoint=$$(docker compose --env-file "$$BOLTER_COMPOSE_ENV_FILE" -f compose.yaml -p "$$BOLTER_COMPOSE_PROJECT" port postgres 5432); \
	case "$$endpoint" in 127.0.0.1:*) ;; *) echo "active Compose project did not publish PostgreSQL on loopback" >&2; exit 1;; esac; \
	port=$${endpoint##*:}; \
	case "$$port" in *[!0-9]*|'') echo "active Compose project returned an invalid PostgreSQL port" >&2; exit 1;; esac; \
	printf 'postgres://bolter_gateway_test_migrator@127.0.0.1:%s/bolter_gateway_test?sslmode=disable\n' "$$port"

# No password appears in that DSN, and none is missing: the cluster runs under
# trust authentication on a port published only to 127.0.0.1, which is what lets
# every service in this repository connect without holding a credential it could
# log. See the POSTGRES_HOST_AUTH_METHOD comment in compose.yaml.
test-compose-config:
	@set -eu; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT INT TERM; \
	go run ./internal/devsetup/cmd/bootstrap -root "$$tmp" -quiet >/dev/null; \
	BT_SECRET_DIR="$$tmp/.secrets" POSTGRES_PORT=0 GATEWAY_PORT=0 TRADING_PORT=0 AUTHEDGE_PORT=0 WEB_PORT=0 \
		docker compose --env-file "$$tmp/.env.stack" -f compose.yaml config --quiet
	go test ./gateway/config ./cmd/exchange-gateway-docker-support -run 'Test(NonLoopback|RunLoads|RunRejects|Health)' -count=1

test-compose-build:
	docker build --build-arg GOPROXY="$${BOLTER_DOCKER_GOPROXY:-https://proxy.golang.org,direct}" -f Dockerfile -t "$${BOLTER_IMAGE:-boltertrader:local}" .
	docker build -f docker/web/Dockerfile -t "$${BOLTER_WEB_IMAGE:-boltertrader-web:local}" .

# What must not be in the image: the repository. A distroless image holding seven
# static binaries has no reason to carry source, and this grep exists because the
# way source gets in is a widened COPY that nobody notices until the image ships
# with .env inside it.
test-compose-security:
	@set -eu; \
	image="$${BOLTER_IMAGE:-boltertrader:local}"; \
	user=$$(docker image inspect "$$image" --format '{{.Config.User}}'); \
	test "$$user" != "" && test "$$user" != "0" && test "$$user" != "root"; \
	entrypoint=$$(docker image inspect "$$image" --format '{{json .Config.Entrypoint}}'); \
	test "$$entrypoint" = '["/usr/local/bin/exchange-gateway-docker-support"]'; \
	tmp=$$(mktemp -d); cid=""; \
	trap 'if test -n "$$cid"; then docker rm -f "$$cid" >/dev/null 2>&1 || true; fi; rm -rf "$$tmp"' EXIT INT TERM; \
	cid=$$(docker create "$$image"); \
	docker export "$$cid" -o "$$tmp/rootfs.tar"; \
	if tar tf "$$tmp/rootfs.tar" | grep -E '(^|/)(\.env|\.git|\.omx|\.secrets)(/|$$)|(^|/)(cmd|gateway|exchange|sdk|services|internal|api|credential|secretstore|routeprofile|backfills|docs|web)(/|$$)|(^|/)go\.(mod|sum)$$|\.go$$|_test\.go$$' >/dev/null; then echo "forbidden path found in runtime image" >&2; exit 1; fi; \
	tar tf "$$tmp/rootfs.tar" | grep -q '^etc/ssl/certs/ca-certificates\.crt$$'; \
	for binary in exchange-gateway exchange-gateway-docker-support gateway-migrate trading trading-migrate authedge authedge-migrate; do \
		tar tf "$$tmp/rootfs.tar" | grep -q "^usr/local/bin/$$binary$$" || (echo "runtime image is missing $$binary" >&2; exit 1); \
	done; \
	docker rm "$$cid" >/dev/null; cid=""; \
	rm -rf "$$tmp"; \
	trap - EXIT INT TERM

# The whole stack end to end: every migrator runs to completion, all three
# services come up, and the gateway answers its own readiness probe. Then SIGTERM
# each one, because a service that will not stop inside its grace period is one
# that gets SIGKILLed with an order in flight.
test-compose-smoke:
	@set -eu; \
	project="boltertrader-smoke-$$$$"; \
	tmp=$$(mktemp -d); \
	trap 'docker compose --env-file "$$tmp/.env.stack" -f compose.yaml -p "$$project" down --volumes --remove-orphans >/dev/null 2>&1 || true; rm -rf "$$tmp"' EXIT INT TERM; \
	go run ./internal/devsetup/cmd/bootstrap -root "$$tmp" -quiet >/dev/null; \
	export BT_SECRET_DIR="$$tmp/.secrets" POSTGRES_PORT=0 GATEWAY_PORT=0 TRADING_PORT=0 AUTHEDGE_PORT=0 WEB_PORT=0; \
	compose="docker compose --env-file $$tmp/.env.stack -f compose.yaml -p $$project"; \
	$$compose up -d --no-build --wait --wait-timeout 180; \
	$$compose exec -T gateway /usr/local/bin/exchange-gateway-docker-support health -config /etc/boltertrader/gateway.yaml -kind live; \
	$$compose exec -T gateway /usr/local/bin/exchange-gateway-docker-support health -config /etc/boltertrader/gateway.yaml -kind ready; \
	account_count=$$($$compose exec -T postgres psql -U bolter_gateway_test_migrator -d bolter_gateway_test -tAc 'SELECT count(*) FROM gateway_accounts' | tr -d '[:space:]'); \
	test "$$account_count" = "0"; \
	for service in authedge trading gateway; do \
		id=$$($$compose ps -q "$$service"); \
		started=$$(date +%s); docker kill --signal TERM "$$id" >/dev/null; \
		while test "$$(docker inspect "$$id" --format '{{.State.Running}}')" = "true"; do \
			now=$$(date +%s); test $$((now-started)) -le 15 || (echo "$$service SIGTERM shutdown exceeded 15s" >&2; exit 1); sleep 1; \
		done; \
		test "$$(docker inspect "$$id" --format '{{.State.ExitCode}}')" = "0" || (echo "$$service exited non-zero on SIGTERM" >&2; exit 1); \
	done; \
	$$compose down --volumes --remove-orphans >/dev/null; \
	rm -rf "$$tmp"; \
	trap - EXIT INT TERM

test-compose-persistence:
	@set -eu; \
	project="boltertrader-persistence-$$$$"; \
	tmp=$$(mktemp -d); sentinel="persistence-$$$$"; \
	trap 'docker compose --env-file "$$tmp/.env.stack" -f compose.yaml -p "$$project" down --volumes --remove-orphans >/dev/null 2>&1 || true; rm -rf "$$tmp"' EXIT INT TERM; \
	go run ./internal/devsetup/cmd/bootstrap -root "$$tmp" -quiet >/dev/null; \
	export BT_SECRET_DIR="$$tmp/.secrets" POSTGRES_PORT=0 GATEWAY_PORT=0 TRADING_PORT=0 AUTHEDGE_PORT=0 WEB_PORT=0; \
	compose="docker compose --env-file $$tmp/.env.stack -f compose.yaml -p $$project"; \
	$$compose up -d --wait --wait-timeout 120 postgres; \
	$$compose exec -T postgres psql -U bolter_gateway_test_migrator -d bolter_gateway_test -v ON_ERROR_STOP=1 -c 'CREATE TABLE IF NOT EXISTS compose_persistence_probe (value text PRIMARY KEY)' -c "INSERT INTO compose_persistence_probe(value) VALUES ('$$sentinel')"; \
	$$compose up -d --force-recreate --no-deps --wait --wait-timeout 120 postgres; \
	observed=$$($$compose exec -T postgres psql -U bolter_gateway_test_migrator -d bolter_gateway_test -tAc "SELECT value FROM compose_persistence_probe WHERE value = '$$sentinel'" | tr -d '[:space:]'); \
	test "$$observed" = "$$sentinel"; \
	$$compose exec -T postgres psql -U bolter_gateway_test_migrator -d bolter_gateway_test -c 'DROP TABLE compose_persistence_probe' >/dev/null; \
	$$compose down --volumes --remove-orphans >/dev/null; \
	rm -rf "$$tmp"; \
	trap - EXIT INT TERM

test-compose-postgres:
	@set -eu; \
	project="boltertrader-postgres-$$$$"; \
	tmp=$$(mktemp -d); \
	trap 'docker compose --env-file "$$tmp/.env.stack" -f compose.yaml -p "$$project" down --volumes --remove-orphans >/dev/null 2>&1 || true; rm -rf "$$tmp"' EXIT INT TERM; \
	go run ./internal/devsetup/cmd/bootstrap -root "$$tmp" -quiet >/dev/null; \
	export BT_SECRET_DIR="$$tmp/.secrets" POSTGRES_PORT=0 GATEWAY_PORT=0 TRADING_PORT=0 AUTHEDGE_PORT=0 WEB_PORT=0; \
	compose="docker compose --env-file $$tmp/.env.stack -f compose.yaml -p $$project"; \
	$$compose up -d --no-build --wait --wait-timeout 120 postgres gateway-migrate; \
	endpoint=$$($$compose port postgres 5432); \
	case "$$endpoint" in 127.0.0.1:*) ;; *) echo "ephemeral project did not publish PostgreSQL on loopback" >&2; exit 1;; esac; \
	dsn="postgres://bolter_gateway_test_migrator@127.0.0.1:$${endpoint##*:}/bolter_gateway_test?sslmode=disable"; \
	GATEWAY_TEST_POSTGRES_DSN="$$dsn" go test ./gateway/store/postgres/... -run 'Test(Migrate|CommandStore|AuditStore|AccountOwnership|RegisterAccount).*RealPostgres' -count=1; \
	GATEWAY_TEST_POSTGRES_DSN="$$dsn" go test ./gateway/order/... -run '^TestCoordinatorRealPostgresProcessCrashWindows$$$$' -count=1; \
	$$compose down --volumes --remove-orphans >/dev/null; \
	rm -rf "$$tmp"; \
	trap - EXIT INT TERM

test-compose: test-compose-config test-compose-build test-compose-security test-compose-smoke test-compose-persistence test-compose-postgres

test-exchange-acceptance: test-exchange-binance-demo-acceptance test-exchange-okx-demo-acceptance test-exchange-lighter-testnet-acceptance test-exchange-hyperliquid-testnet-acceptance test-exchange-bybit-demo-acceptance test-exchange-bitget-demo-acceptance test-exchange-gate-testnet-acceptance test-exchange-aster-testnet-acceptance test-exchange-nado-testnet-acceptance

test-exchange-external-acceptance: test-exchange-acceptance

test-exchange-binance-demo-acceptance: test-exchange-binance-demo-spot test-exchange-binance-demo-perp

test-exchange-binance-demo-spot:
	BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBinanceSpotDemoAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-exchange-binance-demo-perp:
	BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBinancePerpDemoAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-exchange-okx-demo-acceptance: test-exchange-okx-demo-spot test-exchange-okx-demo-perp

test-exchange-okx-demo-spot:
	BOLTER_ENABLE_OKX_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeOKXSpotDemoAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-exchange-okx-demo-perp:
	BOLTER_ENABLE_OKX_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeOKXPerpDemoAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-exchange-lighter-testnet-acceptance: test-exchange-lighter-testnet-spot test-exchange-lighter-testnet-perp

test-exchange-lighter-testnet-spot:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeLighterSpotTestnetAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-exchange-lighter-testnet-perp:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeLighterPerpTestnetAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-exchange-hyperliquid-testnet-acceptance: test-exchange-hyperliquid-testnet-spot test-exchange-hyperliquid-testnet-perp

test-exchange-hyperliquid-testnet-spot:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeHyperliquidSpotTestnetAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-exchange-hyperliquid-testnet-perp:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeHyperliquidPerpTestnetAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-gateway-demo-testnet: test-gateway-binance-spot-demo test-gateway-binance-usd-perp-demo test-gateway-okx-spot-demo test-gateway-okx-usdt-perp-demo test-gateway-lighter-spot-testnet test-gateway-lighter-perp-testnet test-gateway-hyperliquid-spot-testnet test-gateway-hyperliquid-perp-testnet test-gateway-bybit-spot-demo test-gateway-bybit-usdt-perp-demo test-gateway-bybit-usdc-perp-demo test-gateway-bitget-spot-demo test-gateway-bitget-usdt-perp-demo test-gateway-bitget-usdc-perp-demo test-gateway-gate-spot-testnet test-gateway-gate-usdt-perp-testnet test-gateway-aster-spot-testnet test-gateway-aster-usdt-perp-testnet test-gateway-nado-spot-testnet test-gateway-nado-usdt0-perp-testnet

test-gateway-binance-spot-demo:
	BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBinanceSpotDemoAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-gateway-binance-usd-perp-demo:
	BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBinancePerpDemoAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-gateway-okx-spot-demo:
	BOLTER_ENABLE_OKX_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeOKXSpotDemoAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-gateway-okx-usdt-perp-demo:
	BOLTER_ENABLE_OKX_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeOKXPerpDemoAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-gateway-lighter-spot-testnet:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeLighterSpotTestnetAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-gateway-lighter-perp-testnet:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeLighterPerpTestnetAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-gateway-hyperliquid-spot-testnet:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeHyperliquidSpotTestnetAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-gateway-hyperliquid-perp-testnet:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeHyperliquidPerpTestnetAcceptance$$' ./exchange/... -count=1 -timeout=6m

test-gateway-bybit-spot-demo:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBybitSpotDemoAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-gateway-bybit-usdt-perp-demo:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBybitUSDTPerpDemoAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-gateway-bybit-usdc-perp-demo:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBybitUSDCPerpDemoAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-gateway-bitget-spot-demo:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBitgetSpotDemoAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-gateway-bitget-usdt-perp-demo:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBitgetUSDTPerpDemoAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-gateway-bitget-usdc-perp-demo:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBitgetUSDCPerpDemoAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-gateway-gate-spot-testnet:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeGateSpotTestnetAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-gateway-gate-usdt-perp-testnet:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeGateUSDTPerpTestnetAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-gateway-aster-spot-testnet:
	BOLTER_ENABLE_ASTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeAsterSpotTestnetAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-gateway-aster-usdt-perp-testnet:
	BOLTER_ENABLE_ASTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeAsterUSDTPerpTestnetAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-gateway-nado-spot-testnet:
	BOLTER_ENABLE_NADO_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeNadoSpotTestnetAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-gateway-nado-usdt0-perp-testnet:
	BOLTER_ENABLE_NADO_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeNadoUSDT0PerpTestnetAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-exchange-bybit-demo-acceptance: test-exchange-bybit-demo-spot test-exchange-bybit-demo-usdt-perp test-exchange-bybit-demo-usdc-perp

test-exchange-bybit-demo-spot:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBybitSpotDemoAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-exchange-bybit-demo-usdt-perp:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBybitUSDTPerpDemoAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-exchange-bybit-demo-usdc-perp:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBybitUSDCPerpDemoAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-exchange-bitget-demo-acceptance: test-exchange-bitget-demo-spot test-exchange-bitget-demo-usdt-perp test-exchange-bitget-demo-usdc-perp

test-exchange-bitget-demo-spot:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBitgetSpotDemoAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-exchange-bitget-demo-usdt-perp:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBitgetUSDTPerpDemoAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-exchange-bitget-demo-usdc-perp:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeBitgetUSDCPerpDemoAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-exchange-gate-testnet-acceptance: test-exchange-gate-testnet-spot test-exchange-gate-testnet-usdt-perp

test-exchange-gate-testnet-spot:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeGateSpotTestnetAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-exchange-gate-testnet-usdt-perp:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeGateUSDTPerpTestnetAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-exchange-aster-testnet-acceptance: test-exchange-aster-testnet-spot test-exchange-aster-testnet-usdt-perp

test-exchange-aster-testnet-spot:
	BOLTER_ENABLE_ASTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeAsterSpotTestnetAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-exchange-aster-testnet-usdt-perp:
	BOLTER_ENABLE_ASTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeAsterUSDTPerpTestnetAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-exchange-nado-testnet-acceptance: test-exchange-nado-testnet-spot test-exchange-nado-testnet-usdt0-perp

test-exchange-nado-testnet-spot:
	BOLTER_ENABLE_NADO_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeNadoSpotTestnetAcceptance$$' ./exchange/... -count=1 -timeout=15m

test-exchange-nado-testnet-usdt0-perp:
	BOLTER_ENABLE_NADO_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestExchangeNadoUSDT0PerpTestnetAcceptance$$' ./exchange/... -count=1 -timeout=15m
