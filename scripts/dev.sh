#!/usr/bin/env bash
# Runs the stack locally: gateway (127.0.0.1:8081) + trader (127.0.0.1:8080).
#
#   scripts/dev.sh            build web + both binaries, run until Ctrl-C
#   HTTPS_PROXY=http://127.0.0.1:7892 scripts/dev.sh   (venues behind a proxy)
#
# State lives in ./data (gitignored): gateway token, master key, encrypted
# accounts, trader password hash. The trader password is printed once on the
# first run; change it with: go run ./cmd/trader passwd
set -euo pipefail
cd "$(dirname "$0")/.."

mkdir -p data bin
(cd web && { [ -d node_modules ] || npm ci --no-audit --no-fund; } && npm run build)
go build -o bin/gateway ./cmd/gateway
go build -o bin/trader ./cmd/trader

GATEWAY_DATA_DIR=data/gateway bin/gateway &
GATEWAY_PID=$!
trap 'kill $GATEWAY_PID 2>/dev/null || true' EXIT

TRADER_DATA_DIR=data/trader TRADER_WEB_DIR=web/dist GATEWAY_TOKEN_FILE=data/gateway/token bin/trader
