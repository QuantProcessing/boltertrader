.PHONY: test test-race test-core test-adapter test-sdk test-capabilities test-p6-offline test-live-read test-demo-acceptance test-binance-demo test-binance-demo-perp test-binance-demo-runtime-perp test-binance-demo-spot-data test-binance-demo-spot test-binance-demo-runtime-spot test-binance-demo-acceptance test-okx-demo test-okx-demo-spot test-okx-demo-runtime-spot test-okx-demo-perp test-okx-demo-runtime-perp test-okx-demo-acceptance test-hyperliquid-testnet test-hyperliquid-testnet-spot-read test-hyperliquid-testnet-spot test-hyperliquid-testnet-runtime-spot test-hyperliquid-testnet-perp-read test-hyperliquid-testnet-perp test-hyperliquid-testnet-runtime-perp test-hyperliquid-testnet-hip3 test-hyperliquid-testnet-hip3-write test-hyperliquid-testnet-runtime-hip3 test-hyperliquid-testnet-acceptance test-lighter-testnet test-lighter-testnet-read test-lighter-testnet-spot test-lighter-testnet-runtime-spot test-lighter-testnet-perp test-lighter-testnet-runtime-perp test-lighter-testnet-acceptance
.PHONY: test-bybit-demo test-bybit-demo-spot test-bybit-demo-runtime-spot test-bybit-demo-usdt-perp test-bybit-demo-runtime-usdt-perp test-bybit-demo-usdc-perp test-bybit-demo-runtime-usdc-perp test-bybit-demo-acceptance test-bybit-spot-acceptance test-bybit-usdt-perp-acceptance test-bybit-usdc-perp-acceptance test-bybit-acceptance
.PHONY: test-bitget-demo test-bitget-demo-spot test-bitget-demo-runtime-spot test-bitget-demo-usdt-perp test-bitget-demo-runtime-usdt-perp test-bitget-demo-usdc-perp test-bitget-demo-runtime-usdc-perp test-bitget-demo-acceptance test-bitget-testnet test-bitget-testnet-spot test-bitget-testnet-runtime-spot test-bitget-testnet-usdt-perp test-bitget-testnet-runtime-usdt-perp test-bitget-testnet-usdc-perp test-bitget-testnet-runtime-usdc-perp test-bitget-testnet-acceptance test-bitget-spot-acceptance test-bitget-usdt-perp-acceptance test-bitget-usdc-perp-acceptance test-bitget-acceptance test-bybit-bitget-acceptance
.PHONY: test-gate-testnet test-gate-testnet-read test-gate-testnet-spot test-gate-testnet-runtime-spot test-gate-testnet-usdt-perp test-gate-testnet-runtime-usdt-perp test-gate-testnet-usdc-perp-deferred test-gate-testnet-acceptance test-gate-spot-acceptance test-gate-usdt-perp-acceptance test-gate-acceptance
.PHONY: test-reference-data-offline test-reference-data-read test-binance-demo-reference-data-read test-okx-demo-reference-data-read test-bybit-demo-reference-data-read test-bitget-demo-reference-data-read test-gate-testnet-reference-data-read test-hyperliquid-testnet-reference-data-read test-lighter-testnet-reference-data-read test-aster-testnet-reference-data-read test-nado-testnet-reference-data-read
.PHONY: test-aster-testnet test-aster-testnet-read test-aster-testnet-spot-read test-aster-testnet-perp-read test-aster-testnet-spot test-aster-testnet-runtime-spot test-aster-testnet-perp test-aster-testnet-runtime-perp test-aster-testnet-acceptance
.PHONY: test-nado-testnet test-nado-testnet-read test-nado-testnet-spot-read test-nado-testnet-perp-read test-nado-testnet-spot test-nado-testnet-runtime-spot test-nado-testnet-perp test-nado-testnet-runtime-perp test-nado-testnet-acceptance test-aster-nado-testnet-acceptance
.PHONY: test-exchange-offline test-exchange-race test-exchange-redaction test-exchange-quality test-exchange-acceptance test-exchange-external-acceptance test-exchange-binance-demo-acceptance test-exchange-binance-demo-spot test-exchange-binance-demo-perp test-exchange-okx-demo-acceptance test-exchange-okx-demo-spot test-exchange-okx-demo-perp test-exchange-lighter-testnet-acceptance test-exchange-lighter-testnet-spot test-exchange-lighter-testnet-perp test-exchange-hyperliquid-testnet-acceptance test-exchange-hyperliquid-testnet-spot test-exchange-hyperliquid-testnet-perp test-exchange-bybit-demo-acceptance test-exchange-bybit-demo-spot test-exchange-bybit-demo-usdt-perp test-exchange-bybit-demo-usdc-perp test-exchange-bitget-demo-acceptance test-exchange-bitget-demo-spot test-exchange-bitget-demo-usdt-perp test-exchange-bitget-demo-usdc-perp test-exchange-gate-testnet-acceptance test-exchange-gate-testnet-spot test-exchange-gate-testnet-usdt-perp test-exchange-aster-testnet-acceptance test-exchange-aster-testnet-spot test-exchange-aster-testnet-usdt-perp test-exchange-nado-testnet-acceptance test-exchange-nado-testnet-spot test-exchange-nado-testnet-usdt0-perp
.PHONY: test-gateway-contract test-gateway-offline test-gateway-race test-gateway-postgres test-gateway-restart test-gateway-security test-gateway
.PHONY: gateway-compose-dsn test-gateway-docker-config test-gateway-docker-build test-gateway-docker-security test-gateway-docker-smoke test-gateway-docker-persistence test-gateway-docker-postgres test-gateway-docker
.PHONY: test-gateway-demo-testnet test-gateway-binance-spot-demo test-gateway-binance-usd-perp-demo test-gateway-okx-spot-demo test-gateway-okx-usdt-perp-demo test-gateway-lighter-spot-testnet test-gateway-lighter-perp-testnet test-gateway-hyperliquid-spot-testnet test-gateway-hyperliquid-perp-testnet test-gateway-bybit-spot-demo test-gateway-bybit-usdt-perp-demo test-gateway-bybit-usdc-perp-demo test-gateway-bitget-spot-demo test-gateway-bitget-usdt-perp-demo test-gateway-bitget-usdc-perp-demo test-gateway-gate-spot-testnet test-gateway-gate-usdt-perp-testnet test-gateway-aster-spot-testnet test-gateway-aster-usdt-perp-testnet test-gateway-nado-spot-testnet test-gateway-nado-usdt0-perp-testnet
.PHONY: dev-platform-up dev-platform-down dev-platform-restart dev-platform-status dev-platform-logs dev-platform-reset dev-platform-doctor test-dev-platform

.NOTPARALLEL:

test:
	go test -short ./...

dev-platform-up:
	./scripts/dev-platform up

dev-platform-down:
	./scripts/dev-platform down

dev-platform-restart:
	./scripts/dev-platform restart

dev-platform-status:
	./scripts/dev-platform status

dev-platform-logs:
	./scripts/dev-platform logs

dev-platform-reset:
	./scripts/dev-platform reset

dev-platform-doctor:
	./scripts/dev-platform doctor

test-dev-platform:
	go test ./scripts -count=1

test-race:
	go test -race ./runtime/...

test-core:
	go test ./core/... ./runtime/... ./strategy/...

test-adapter:
	go test -short ./adapter/...

test-sdk:
	go test -short ./sdk/...

test-capabilities:
	go test -short ./adapter -count=1
	go test -short ./adapter/... -run Capabilit -count=1

test-p6-offline: test-core test-adapter test-sdk test-capabilities test-reference-data-offline

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

gateway-compose-dsn:
	@test -n "$$GATEWAY_COMPOSE_PROJECT" || (echo "GATEWAY_COMPOSE_PROJECT is required" >&2; exit 1)
	@test -n "$$GATEWAY_COMPOSE_POSTGRES_PASSWORD" || (echo "GATEWAY_COMPOSE_POSTGRES_PASSWORD is required" >&2; exit 1)
	@case "$$GATEWAY_COMPOSE_POSTGRES_PASSWORD" in *[!A-Za-z0-9_-]*|'') echo "GATEWAY_COMPOSE_POSTGRES_PASSWORD must use safe test characters" >&2; exit 1;; esac; \
	endpoint=$$(docker compose -p "$$GATEWAY_COMPOSE_PROJECT" -f compose.yaml -f docker/compose.test.yaml port postgres 5432); \
	case "$$endpoint" in 127.0.0.1:*) ;; *) echo "active Compose project did not publish PostgreSQL on loopback" >&2; exit 1;; esac; \
	port=$${endpoint##*:}; \
	case "$$port" in *[!0-9]*|'') echo "active Compose project returned an invalid PostgreSQL port" >&2; exit 1;; esac; \
	printf 'postgres://%s:%s@127.0.0.1:%s/%s?sslmode=disable\n' "$${POSTGRES_USER:-gateway}" "$$GATEWAY_COMPOSE_POSTGRES_PASSWORD" "$$port" "$${POSTGRES_DB:-gateway}"

test-gateway-docker-config:
	@GATEWAY_AUTH_TOKEN_FILE=/dev/null GATEWAY_POSTGRES_DSN_FILE=/dev/null POSTGRES_PASSWORD_FILE=/dev/null docker compose -f compose.yaml config --quiet
	@GATEWAY_AUTH_TOKEN_FILE=/dev/null GATEWAY_POSTGRES_DSN_FILE=/dev/null POSTGRES_PASSWORD_FILE=/dev/null docker compose -f compose.yaml -f docker/compose.test.yaml config --quiet
	go test ./gateway/contracts ./gateway/config ./cmd/exchange-gateway-docker-support -run 'Test(GatewayDocker|GatewayCompose|DockerGateway|NonLoopback|RunLoads|RunRejects|Health)' -count=1

test-gateway-docker-build:
	docker build --build-arg GOPROXY="$${GATEWAY_DOCKER_GOPROXY:-https://proxy.golang.org,direct}" -f Dockerfile -t "$${GATEWAY_DOCKER_IMAGE:-boltertrader-gateway:local}" .

test-gateway-docker-security:
	@set -eu; \
	image="$${GATEWAY_DOCKER_IMAGE:-boltertrader-gateway:local}"; \
	user=$$(docker image inspect "$$image" --format '{{.Config.User}}'); \
	test "$$user" != "" && test "$$user" != "0" && test "$$user" != "root"; \
	entrypoint=$$(docker image inspect "$$image" --format '{{json .Config.Entrypoint}}'); \
	test "$$entrypoint" = '["/usr/local/bin/exchange-gateway-docker-support"]'; \
	tmp=$$(mktemp -d); cid=""; \
	trap 'if test -n "$$cid"; then docker rm -f "$$cid" >/dev/null 2>&1 || true; fi; rm -rf "$$tmp"' EXIT INT TERM; \
	cid=$$(docker create "$$image"); \
	docker export "$$cid" -o "$$tmp/rootfs.tar"; \
	if tar tf "$$tmp/rootfs.tar" | grep -E '(^|/)(\.env|\.git|\.omx)(/|$$)|(^|/)(cmd|gateway|exchange|sdk|adapter|runtime|core|internal|strategy|docs)(/|$$)|(^|/)go\.(mod|sum)$$|\.go$$|_test\.go$$' >/dev/null; then echo "forbidden path found in runtime image" >&2; exit 1; fi; \
	tar tf "$$tmp/rootfs.tar" | grep -q '^etc/ssl/certs/ca-certificates\.crt$$'; \
	tar tf "$$tmp/rootfs.tar" | grep -q '^usr/local/bin/exchange-gateway$$'; \
	tar tf "$$tmp/rootfs.tar" | grep -q '^usr/local/bin/exchange-gateway-docker-support$$'; \
	docker rm "$$cid" >/dev/null; cid=""; \
	rm -rf "$$tmp"; \
	trap - EXIT INT TERM

test-gateway-docker-smoke:
	@set -eu; \
	project="boltertrader-gateway-$@-$$$$"; \
	tmp=$$(mktemp -d); password="gateway-docker-test-password"; token="gateway-docker-test-token"; \
	trap 'docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml down -v --remove-orphans >/dev/null 2>&1 || true; rm -rf "$$tmp"' EXIT INT TERM; \
	umask 077; \
	printf '%s\n' "$$password" >"$$tmp/postgres-password"; \
	printf '%s\n' "$$token" >"$$tmp/gateway-token"; \
	printf 'postgres://gateway:%s@postgres:5432/gateway?sslmode=disable\n' "$$password" >"$$tmp/gateway-dsn"; \
	export GATEWAY_IMAGE="$${GATEWAY_DOCKER_IMAGE:-boltertrader-gateway:local}" GATEWAY_PORT=0 GATEWAY_AUTH_TOKEN_FILE="$$tmp/gateway-token" GATEWAY_POSTGRES_DSN_FILE="$$tmp/gateway-dsn" POSTGRES_PASSWORD_FILE="$$tmp/postgres-password"; \
	docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml up -d --no-build --wait --wait-timeout 90; \
	docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml exec -T gateway /usr/local/bin/exchange-gateway-docker-support health -config /etc/boltertrader/gateway.yaml -kind live; \
	docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml exec -T gateway /usr/local/bin/exchange-gateway-docker-support health -config /etc/boltertrader/gateway.yaml -kind ready; \
	account_count=$$(docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml exec -T postgres psql -U gateway -d gateway -tAc 'SELECT count(*) FROM gateway_accounts' | tr -d '[:space:]'); \
	test "$$account_count" = "0"; \
	gateway_id=$$(docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml ps -q gateway); \
	started=$$(date +%s); docker kill --signal TERM "$$gateway_id" >/dev/null; \
	while test "$$(docker inspect "$$gateway_id" --format '{{.State.Running}}')" = "true"; do now=$$(date +%s); test $$((now-started)) -le 15 || (echo "Gateway SIGTERM shutdown exceeded 15s" >&2; exit 1); sleep 1; done; \
	test "$$(docker inspect "$$gateway_id" --format '{{.State.ExitCode}}')" = "0"; \
	docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml down -v --remove-orphans >/dev/null; \
	rm -rf "$$tmp"; \
	trap - EXIT INT TERM

test-gateway-docker-persistence:
	@set -eu; \
	project="boltertrader-gateway-$@-$$$$"; \
	tmp=$$(mktemp -d); password="gateway-docker-test-password"; sentinel="gateway-volume-$@-$$$$"; \
	trap 'docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml down -v --remove-orphans >/dev/null 2>&1 || true; rm -rf "$$tmp"' EXIT INT TERM; \
	umask 077; \
	printf '%s\n' "$$password" >"$$tmp/postgres-password"; \
	printf '%s\n' "unused-token" >"$$tmp/gateway-token"; \
	printf '%s\n' "unused-dsn" >"$$tmp/gateway-dsn"; \
	export GATEWAY_AUTH_TOKEN_FILE="$$tmp/gateway-token" GATEWAY_POSTGRES_DSN_FILE="$$tmp/gateway-dsn" POSTGRES_PASSWORD_FILE="$$tmp/postgres-password"; \
	docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml up -d --wait --wait-timeout 60 postgres; \
	docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml exec -T postgres psql -U gateway -d gateway -v ON_ERROR_STOP=1 -c 'CREATE TABLE IF NOT EXISTS gateway_docker_persistence_test (value text PRIMARY KEY)' -c "INSERT INTO gateway_docker_persistence_test(value) VALUES ('$$sentinel')"; \
	docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml up -d --force-recreate --no-deps --wait --wait-timeout 60 postgres; \
	observed=$$(docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml exec -T postgres psql -U gateway -d gateway -tAc "SELECT value FROM gateway_docker_persistence_test WHERE value = '$$sentinel'" | tr -d '[:space:]'); \
	test "$$observed" = "$$sentinel"; \
	docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml exec -T postgres psql -U gateway -d gateway -c 'DROP TABLE gateway_docker_persistence_test' >/dev/null; \
	docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml down -v --remove-orphans >/dev/null; \
	rm -rf "$$tmp"; \
	trap - EXIT INT TERM

test-gateway-docker-postgres:
	@set -eu; \
	project="boltertrader-gateway-$@-$$$$"; \
	tmp=$$(mktemp -d); password="gateway-docker-test-password"; \
	trap 'docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml down -v --remove-orphans >/dev/null 2>&1 || true; rm -rf "$$tmp"' EXIT INT TERM; \
	umask 077; \
	printf '%s\n' "$$password" >"$$tmp/postgres-password"; \
	printf '%s\n' "unused-token" >"$$tmp/gateway-token"; \
	printf '%s\n' "unused-dsn" >"$$tmp/gateway-dsn"; \
	export GATEWAY_AUTH_TOKEN_FILE="$$tmp/gateway-token" GATEWAY_POSTGRES_DSN_FILE="$$tmp/gateway-dsn" POSTGRES_PASSWORD_FILE="$$tmp/postgres-password"; \
	docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml up -d --wait --wait-timeout 60 postgres; \
	dsn=$$(GATEWAY_COMPOSE_PROJECT="$$project" GATEWAY_COMPOSE_POSTGRES_PASSWORD="$$password" $(MAKE) --no-print-directory -s gateway-compose-dsn); \
	GATEWAY_TEST_POSTGRES_DSN="$$dsn" $(MAKE) --no-print-directory test-gateway-postgres; \
	GATEWAY_TEST_POSTGRES_DSN="$$dsn" $(MAKE) --no-print-directory test-gateway-restart; \
	docker compose -p "$$project" -f compose.yaml -f docker/compose.test.yaml down -v --remove-orphans >/dev/null; \
	rm -rf "$$tmp"; \
	trap - EXIT INT TERM

test-gateway-docker: test-gateway-docker-config test-gateway-docker-build test-gateway-docker-security test-gateway-docker-smoke test-gateway-docker-persistence test-gateway-docker-postgres

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

test-reference-data-offline:
	go test -short ./core/model ./core/contract ./runtime/cache ./runtime ./runtime/runtimetest ./adapter/internal/runtimeaccept ./adapter/binance/perp ./adapter/okx/perp ./adapter/bybit ./adapter/bitget ./adapter/gate ./adapter/hyperliquid/perp ./adapter/lighter ./adapter/aster/perp ./adapter/nado ./sdk/nado -run 'Reference|OpenInterest|Capabilit' -count=1

test-live-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go test ./sdk/... ./adapter/...

test-reference-data-read: test-binance-demo-reference-data-read test-okx-demo-reference-data-read test-bybit-demo-reference-data-read test-bitget-demo-reference-data-read test-gate-testnet-reference-data-read test-hyperliquid-testnet-reference-data-read test-lighter-testnet-reference-data-read test-aster-testnet-reference-data-read test-nado-testnet-reference-data-read

test-binance-demo-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceDemoReferenceDataReadAcceptance$$' ./adapter/binance/perp/ -count=1 -timeout=3m

test-okx-demo-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXPerpDemoReferenceDataReadAcceptance$$' ./adapter/okx/perp/ -count=1 -timeout=3m

test-bybit-demo-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoReferenceDataReadAcceptance$$' ./adapter/bybit/ -count=1 -timeout=4m

test-bitget-demo-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoReferenceDataReadAcceptance$$' ./adapter/bitget/ -count=1 -timeout=4m

test-gate-testnet-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetReferenceDataReadAcceptance$$' ./adapter/gate/ -count=1 -timeout=3m

test-hyperliquid-testnet-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetReferenceDataReadAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=4m

test-lighter-testnet-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestLighterTestnetReferenceDataReadAcceptance$$' ./adapter/lighter/ -count=1 -timeout=3m

test-aster-testnet-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterPerpTestnetReferenceDataReadAcceptance$$' ./adapter/aster/perp/ -count=1 -timeout=3m

test-nado-testnet-reference-data-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoTestnetReferenceDataReadAcceptance$$' ./adapter/nado/ -count=1 -timeout=3m

test-aster-testnet: test-aster-testnet-acceptance

test-aster-testnet-read: test-aster-testnet-spot-read test-aster-testnet-perp-read test-aster-testnet-reference-data-read

test-aster-testnet-spot-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterSpotTestnetReadAcceptance$$' ./adapter/aster/spot/ -count=1 -timeout=3m

test-aster-testnet-perp-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterPerpTestnetReadAcceptance$$' ./adapter/aster/perp/ -count=1 -timeout=3m

test-aster-testnet-spot:
	BOLTER_ENABLE_ASTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterSpotTestnetAdapterAcceptance$$' ./adapter/aster/spot/ -count=1 -timeout=5m

test-aster-testnet-runtime-spot:
	BOLTER_ENABLE_ASTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterSpotTestnetRuntimeAcceptance$$' ./adapter/aster/spot/ -count=1 -timeout=6m

test-aster-testnet-perp:
	BOLTER_ENABLE_ASTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterPerpTestnetAdapterAcceptance$$' ./adapter/aster/perp/ -count=1 -timeout=5m

test-aster-testnet-runtime-perp:
	BOLTER_ENABLE_ASTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestAsterPerpTestnetRuntimeAcceptance$$' ./adapter/aster/perp/ -count=1 -timeout=6m

test-aster-testnet-acceptance: test-aster-testnet-read test-aster-testnet-spot test-aster-testnet-runtime-spot test-aster-testnet-perp test-aster-testnet-runtime-perp

test-nado-testnet: test-nado-testnet-acceptance

test-nado-testnet-read: test-nado-testnet-spot-read test-nado-testnet-perp-read test-nado-testnet-reference-data-read

test-nado-testnet-spot-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoSpotTestnetReadAcceptance$$' ./adapter/nado/ -count=1 -timeout=3m

test-nado-testnet-perp-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoPerpTestnetReadAcceptance$$' ./adapter/nado/ -count=1 -timeout=3m

test-nado-testnet-spot:
	BOLTER_ENABLE_NADO_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoSpotTestnetAdapterAcceptance$$' ./adapter/nado/ -count=1 -timeout=5m

test-nado-testnet-runtime-spot:
	BOLTER_ENABLE_NADO_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoSpotTestnetRuntimeAcceptance$$' ./adapter/nado/ -count=1 -timeout=6m

test-nado-testnet-perp:
	BOLTER_ENABLE_NADO_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoPerpTestnetAdapterAcceptance$$' ./adapter/nado/ -count=1 -timeout=5m

test-nado-testnet-runtime-perp:
	BOLTER_ENABLE_NADO_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestNadoPerpTestnetRuntimeAcceptance$$' ./adapter/nado/ -count=1 -timeout=6m

test-nado-testnet-acceptance: test-nado-testnet-read test-nado-testnet-spot test-nado-testnet-runtime-spot test-nado-testnet-perp test-nado-testnet-runtime-perp

test-aster-nado-testnet-acceptance: test-aster-testnet-acceptance test-nado-testnet-acceptance

test-demo-acceptance: test-binance-demo-acceptance test-okx-demo-acceptance test-bybit-acceptance test-bitget-acceptance

test-binance-demo: test-binance-demo-acceptance

test-binance-demo-perp:
	BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceDemoExecAcceptance$$' ./adapter/binance/perp/ -count=1 -timeout=5m

test-binance-demo-runtime-perp:
	BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceDemoRuntimeAcceptance$$' ./adapter/binance/perp/ -count=1 -timeout=6m

test-binance-demo-spot-data:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceSpotDemoDataAcceptance$$' ./adapter/binance/spot/ -count=1 -timeout=2m

test-binance-demo-spot:
	BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceSpotDemoExecAcceptance$$' ./adapter/binance/spot/ -count=1 -timeout=5m

test-binance-demo-runtime-spot:
	BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBinanceSpotDemoRuntimeAcceptance$$' ./adapter/binance/spot/ -count=1 -timeout=6m

test-binance-demo-acceptance: test-binance-demo-perp test-binance-demo-runtime-perp test-binance-demo-spot-data test-binance-demo-spot test-binance-demo-runtime-spot

test-okx-demo: test-okx-demo-acceptance

test-okx-demo-spot:
	BOLTER_ENABLE_OKX_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXSpotDemoExecAcceptance$$' ./adapter/okx/spot/ -count=1 -timeout=5m

test-okx-demo-runtime-spot:
	BOLTER_ENABLE_OKX_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXSpotDemoRuntimeAcceptance$$' ./adapter/okx/spot/ -count=1 -timeout=6m

test-okx-demo-perp:
	BOLTER_ENABLE_OKX_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXPerpDemoExecAcceptance$$' ./adapter/okx/perp/ -count=1 -timeout=5m

test-okx-demo-runtime-perp:
	BOLTER_ENABLE_OKX_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestOKXPerpDemoRuntimeAcceptance$$' ./adapter/okx/perp/ -count=1 -timeout=6m

test-okx-demo-acceptance: test-okx-demo-spot test-okx-demo-runtime-spot test-okx-demo-perp test-okx-demo-runtime-perp

test-hyperliquid-testnet: test-hyperliquid-testnet-acceptance

test-hyperliquid-testnet-spot-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidSpotTestnetReadAcceptance$$' ./adapter/hyperliquid/spot/ -count=1 -timeout=3m

test-hyperliquid-testnet-spot:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidSpotTestnetWriteAcceptance$$' ./adapter/hyperliquid/spot/ -count=1 -timeout=5m

test-hyperliquid-testnet-runtime-spot:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidSpotTestnetRuntimeAcceptance$$' ./adapter/hyperliquid/spot/ -count=1 -timeout=6m

test-hyperliquid-testnet-perp-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetReadAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=3m

test-hyperliquid-testnet-perp:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetWriteAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=5m

test-hyperliquid-testnet-runtime-perp:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetRuntimeAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=6m

test-hyperliquid-testnet-hip3:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetHIP3ReadAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=3m

test-hyperliquid-testnet-hip3-write:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetHIP3WriteAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=5m

test-hyperliquid-testnet-runtime-hip3:
	BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestHyperliquidPerpTestnetHIP3RuntimeAcceptance$$' ./adapter/hyperliquid/perp/ -count=1 -timeout=6m

test-hyperliquid-testnet-acceptance: test-hyperliquid-testnet-spot-read test-hyperliquid-testnet-perp-read test-hyperliquid-testnet-hip3 test-hyperliquid-testnet-spot test-hyperliquid-testnet-runtime-spot test-hyperliquid-testnet-perp test-hyperliquid-testnet-runtime-perp test-hyperliquid-testnet-hip3-write test-hyperliquid-testnet-runtime-hip3

test-lighter-testnet: test-lighter-testnet-acceptance

test-lighter-testnet-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestLighterTestnetReadAcceptance$$' ./adapter/lighter/ -count=1 -timeout=3m

test-lighter-testnet-spot:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestLighterTestnetSpotWriteAcceptance$$' ./adapter/lighter/ -count=1 -timeout=5m

test-lighter-testnet-runtime-spot:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestLighterTestnetSpotRuntimeAcceptance$$' ./adapter/lighter/ -count=1 -timeout=6m

test-lighter-testnet-perp:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestLighterTestnetPerpWriteAcceptance$$' ./adapter/lighter/ -count=1 -timeout=5m

test-lighter-testnet-runtime-perp:
	BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestLighterTestnetPerpRuntimeAcceptance$$' ./adapter/lighter/ -count=1 -timeout=6m

test-lighter-testnet-acceptance: test-lighter-testnet-read test-lighter-testnet-spot test-lighter-testnet-runtime-spot test-lighter-testnet-perp test-lighter-testnet-runtime-perp

test-bybit-demo: test-bybit-demo-acceptance

test-bybit-demo-spot:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoSpotAcceptance$$' ./adapter/bybit/ -count=1 -timeout=5m

test-bybit-demo-runtime-spot:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoSpotRuntimeAcceptance$$' ./adapter/bybit/ -count=1 -timeout=6m

test-bybit-demo-usdt-perp:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoUSDTPerpAcceptance$$' ./adapter/bybit/ -count=1 -timeout=5m

test-bybit-demo-runtime-usdt-perp:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoUSDTPerpRuntimeAcceptance$$' ./adapter/bybit/ -count=1 -timeout=6m

test-bybit-demo-usdc-perp:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoUSDCPerpAcceptance$$' ./adapter/bybit/ -count=1 -timeout=5m

test-bybit-demo-runtime-usdc-perp:
	BOLTER_ENABLE_BYBIT_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBybitDemoUSDCPerpRuntimeAcceptance$$' ./adapter/bybit/ -count=1 -timeout=6m

test-bybit-demo-acceptance: test-bybit-demo-spot test-bybit-demo-runtime-spot test-bybit-demo-usdt-perp test-bybit-demo-runtime-usdt-perp test-bybit-demo-usdc-perp test-bybit-demo-runtime-usdc-perp

test-bybit-spot-acceptance: test-bybit-demo-spot test-bybit-demo-runtime-spot

test-bybit-usdt-perp-acceptance: test-bybit-demo-usdt-perp test-bybit-demo-runtime-usdt-perp

test-bybit-usdc-perp-acceptance: test-bybit-demo-usdc-perp test-bybit-demo-runtime-usdc-perp

test-bybit-acceptance: test-bybit-spot-acceptance test-bybit-usdt-perp-acceptance test-bybit-usdc-perp-acceptance

test-bitget-demo: test-bitget-demo-acceptance

test-bitget-demo-spot:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoSpotAcceptance$$' ./adapter/bitget/ -count=1 -timeout=5m

test-bitget-demo-runtime-spot:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoSpotRuntimeAcceptance$$' ./adapter/bitget/ -count=1 -timeout=6m

test-bitget-demo-usdt-perp:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoUSDTPerpAcceptance$$' ./adapter/bitget/ -count=1 -timeout=5m

test-bitget-demo-runtime-usdt-perp:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoUSDTPerpRuntimeAcceptance$$' ./adapter/bitget/ -count=1 -timeout=6m

test-bitget-demo-usdc-perp:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoUSDCPerpAcceptance$$' ./adapter/bitget/ -count=1 -timeout=5m

test-bitget-demo-runtime-usdc-perp:
	BOLTER_ENABLE_BITGET_DEMO_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestBitgetDemoUSDCPerpRuntimeAcceptance$$' ./adapter/bitget/ -count=1 -timeout=6m

test-bitget-demo-acceptance: test-bitget-demo-spot test-bitget-demo-runtime-spot test-bitget-demo-usdt-perp test-bitget-demo-runtime-usdt-perp test-bitget-demo-usdc-perp test-bitget-demo-runtime-usdc-perp

test-bitget-testnet: test-bitget-demo

test-bitget-testnet-spot: test-bitget-demo-spot

test-bitget-testnet-runtime-spot: test-bitget-demo-runtime-spot

test-bitget-testnet-usdt-perp: test-bitget-demo-usdt-perp

test-bitget-testnet-runtime-usdt-perp: test-bitget-demo-runtime-usdt-perp

test-bitget-testnet-usdc-perp: test-bitget-demo-usdc-perp

test-bitget-testnet-runtime-usdc-perp: test-bitget-demo-runtime-usdc-perp

test-bitget-testnet-acceptance: test-bitget-demo-acceptance

test-bitget-spot-acceptance: test-bitget-demo-spot test-bitget-demo-runtime-spot

test-bitget-usdt-perp-acceptance: test-bitget-demo-usdt-perp test-bitget-demo-runtime-usdt-perp

test-bitget-usdc-perp-acceptance: test-bitget-demo-usdc-perp test-bitget-demo-runtime-usdc-perp

test-bitget-acceptance: test-bitget-spot-acceptance test-bitget-usdt-perp-acceptance test-bitget-usdc-perp-acceptance

test-bybit-bitget-acceptance: test-bybit-acceptance test-bitget-acceptance

test-gate-testnet: test-gate-testnet-acceptance

test-gate-testnet-read:
	BOLTER_ENABLE_LIVE_READ_TESTS=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetReadAcceptance$$' ./adapter/gate/ -count=1 -timeout=3m

test-gate-testnet-spot:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetSpotAcceptance$$' ./adapter/gate/ -count=1 -timeout=5m

test-gate-testnet-runtime-spot:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetSpotRuntimeAcceptance$$' ./adapter/gate/ -count=1 -timeout=6m

test-gate-testnet-usdt-perp:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetUSDTPerpAcceptance$$' ./adapter/gate/ -count=1 -timeout=5m

test-gate-testnet-runtime-usdt-perp:
	BOLTER_ENABLE_GATE_TESTNET_WRITES=1 go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetUSDTPerpRuntimeAcceptance$$' ./adapter/gate/ -count=1 -timeout=6m

test-gate-testnet-usdc-perp-deferred:
	go run ./internal/testenv/cmd/noskipgotest -- -v -run '^TestGateTestnetUSDCPerpDeferredCapability$$' ./adapter/gate/ -count=1 -timeout=2m

test-gate-testnet-acceptance: test-gate-testnet-read test-gate-testnet-spot test-gate-testnet-runtime-spot test-gate-testnet-usdt-perp test-gate-testnet-runtime-usdt-perp test-gate-testnet-usdc-perp-deferred

test-gate-spot-acceptance: test-gate-testnet-spot test-gate-testnet-runtime-spot

test-gate-usdt-perp-acceptance: test-gate-testnet-usdt-perp test-gate-testnet-runtime-usdt-perp

test-gate-acceptance: test-gate-testnet-acceptance
