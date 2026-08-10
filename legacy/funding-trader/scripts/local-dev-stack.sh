#!/usr/bin/env bash
set -euo pipefail
umask 077

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUNTIME_DIR="${FT_LOCAL_RUNTIME_DIR:-/tmp/funding-trader-local}"
PID_DIR="${RUNTIME_DIR}/pids"
LOG_DIR="${RUNTIME_DIR}/logs"
OWNERSHIP_DIR="${RUNTIME_DIR}/ownership"
BIN_DIR="${RUNTIME_DIR}/bin"
PGDATA="${FT_LOCAL_PGDATA:-${RUNTIME_DIR}/postgres-15432}"
PG_PORT="${FT_LOCAL_PG_PORT:-15432}"
GATEWAY_URL="${FT_GATEWAY_BASE_URL:-http://127.0.0.1:18082}"
BACKEND_ADDR_DEFAULT="${FT_HTTP_ADDR:-127.0.0.1:18091}"
FRONTEND_HOST="${FT_FRONTEND_HOST:-127.0.0.1}"
FRONTEND_PORT="${FT_FRONTEND_PORT:-13001}"
BACKEND_ENV_FILE=""
FRONTEND_ENV_FILE=""

usage() {
  cat <<'USAGE'
Usage:
  scripts/local-dev-stack.sh [--runtime-dir DIR] [--backend-env FILE] [--frontend-env FILE] <command> [service]

Commands:
  status [all|postgres|gateway|backend|frontend]
  start  [all|postgres|backend|frontend]
  stop   [all|postgres|backend|frontend]

Defaults:
  PostgreSQL: 127.0.0.1:15432, data/logs/pids under /tmp/funding-trader-local
  Gateway:    http://127.0.0.1:18082 (external status only; never started/stopped)
  Backend:    127.0.0.1:18091
  Frontend:   127.0.0.1:13001

Backend start requires --backend-env FILE. The env file must provide operator
secrets such as FT_GATEWAY_TOKEN_FILE or FT_GATEWAY_TOKEN; this script never
embeds or prints them. Live writes are refused unless FT_LOCAL_ALLOW_LIVE_WRITES=true
is present in the operator env.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --runtime-dir)
      RUNTIME_DIR="${2:?missing value for --runtime-dir}"
      PID_DIR="${RUNTIME_DIR}/pids"
      LOG_DIR="${RUNTIME_DIR}/logs"
      OWNERSHIP_DIR="${RUNTIME_DIR}/ownership"
      BIN_DIR="${RUNTIME_DIR}/bin"
      PGDATA="${FT_LOCAL_PGDATA:-${RUNTIME_DIR}/postgres-15432}"
      shift 2
      ;;
    --backend-env)
      BACKEND_ENV_FILE="${2:?missing value for --backend-env}"
      shift 2
      ;;
    --frontend-env)
      FRONTEND_ENV_FILE="${2:?missing value for --frontend-env}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      break
      ;;
  esac
done

COMMAND="${1:-}"
SERVICE="${2:-all}"

fail() {
  printf 'ERROR: %s\n' "$1" >&2
  exit 1
}

prepare_private_dir() {
  local path="$1"
  [[ ! -L "$path" ]] || fail "refusing symlinked runtime path: $path"
  [[ ! -e "$path" || -d "$path" ]] || fail "runtime path is not a directory: $path"
  mkdir -p "$path"
  [[ -O "$path" ]] || fail "runtime path is not owned by the current user: $path"
  chmod 700 "$path"
}

prepare_private_dir "$RUNTIME_DIR"
prepare_private_dir "$PID_DIR"
prepare_private_dir "$LOG_DIR"
prepare_private_dir "$OWNERSHIP_DIR"
prepare_private_dir "$BIN_DIR"

normalize_bool() {
  case "$1" in
    1|t|T|true|TRUE|True) printf 'true' ;;
    0|f|F|false|FALSE|False) printf 'false' ;;
    *) return 1 ;;
  esac
}

pid_file() {
  printf '%s/%s.pid' "$PID_DIR" "$1"
}

start_file() {
  printf '%s/%s.start' "$OWNERSHIP_DIR" "$1"
}

identity_file() {
  printf '%s/%s.identity' "$OWNERSHIP_DIR" "$1"
}

postgres_owner_marker() {
  printf '%s/.funding-trader-local-owned' "$PGDATA"
}

is_alive() {
  local pid="$1"
  [[ "$pid" =~ ^[0-9]+$ ]] && [[ "$pid" -gt 1 ]] && kill -0 "$pid" 2>/dev/null
}

read_pid() {
  local file
  file="$(pid_file "$1")"
  [[ -f "$file" ]] && sed -n '1p' "$file" || true
}

process_start_time() {
  local pid="$1"
  ps -p "$pid" -o lstart= 2>/dev/null | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//'
}

clear_process_record() {
  rm -f "$(pid_file "$1")" "$(start_file "$1")" "$(identity_file "$1")"
}

record_owned_process() {
  local name="$1"
  local pid="$2"
  local identity="$3"
  local started
  is_alive "$pid" || return 1
  started="$(process_start_time "$pid")"
  [[ -n "$started" && -n "$identity" ]] || return 1
  printf '%s\n' "$pid" >"$(pid_file "$name")"
  printf '%s\n' "$started" >"$(start_file "$name")"
  printf '%s\n' "$identity" >"$(identity_file "$name")"
}

owned_pid() {
  local name="$1"
  local pid started expected_identity current_start current_command
  pid="$(read_pid "$name")"
  [[ -f "$(start_file "$name")" && -f "$(identity_file "$name")" ]] || return 1
  started="$(sed -n '1p' "$(start_file "$name")")"
  expected_identity="$(sed -n '1p' "$(identity_file "$name")")"
  is_alive "$pid" || return 1
  current_start="$(process_start_time "$pid")"
  [[ -n "$current_start" && "$current_start" == "$started" ]] || return 1
  current_command="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  [[ -n "$expected_identity" && "$current_command" == *"$expected_identity"* ]] || return 1
  printf '%s\n' "$pid"
}

port_in_use() {
  local port="$1"
  lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
}

assert_port_free_or_owned() {
  local name="$1"
  local port="$2"
  local pid
  pid="$(owned_pid "$name" || true)"
  if [[ -n "$pid" ]]; then
    return 0
  fi
  if port_in_use "$port"; then
    fail "port ${port} is already occupied; not taking it over"
  fi
}

load_env_file() {
  local file="$1"
  [[ -n "$file" ]] || fail "missing required env file path"
  [[ -f "$file" ]] || fail "env file not found: $file"
  set -a
  # shellcheck disable=SC1090
  source "$file"
  set +a
}

status_pid() {
  local name="$1"
  local pid
  pid="$(owned_pid "$name" || true)"
  if [[ -n "$pid" ]]; then
    printf '%s: running pid=%s log=%s/%s.log\n' "$name" "$pid" "$LOG_DIR" "$name"
  elif is_alive "$(read_pid "$name")"; then
    printf '%s: untrusted-pidfile ignored\n' "$name"
  else
    printf '%s: stopped\n' "$name"
  fi
}

status_postgres() {
  status_pid postgres
  if port_in_use "$PG_PORT"; then
    printf 'postgres-port: listening 127.0.0.1:%s\n' "$PG_PORT"
  else
    printf 'postgres-port: closed 127.0.0.1:%s\n' "$PG_PORT"
  fi
}

status_gateway() {
  local host_port=""
  if [[ "$GATEWAY_URL" =~ ^https?://(127\.0\.0\.1|localhost):([0-9]+)(/.*)?$ ]]; then
    host_port="${BASH_REMATCH[2]}"
  fi
  if [[ -z "$host_port" ]]; then
    printf 'gateway: external-url %s (local port status unavailable)\n' "$GATEWAY_URL"
  elif port_in_use "$host_port"; then
    printf 'gateway: external-listening %s\n' "$GATEWAY_URL"
  else
    printf 'gateway: external-not-listening %s\n' "$GATEWAY_URL"
  fi
}

status_backend() {
  status_pid backend
  local port="${BACKEND_ADDR_DEFAULT##*:}"
  if port_in_use "$port"; then
    printf 'backend-port: listening %s\n' "$BACKEND_ADDR_DEFAULT"
  else
    printf 'backend-port: closed %s\n' "$BACKEND_ADDR_DEFAULT"
  fi
}

status_frontend() {
  status_pid frontend
  if port_in_use "$FRONTEND_PORT"; then
    printf 'frontend-port: listening %s:%s\n' "$FRONTEND_HOST" "$FRONTEND_PORT"
  else
    printf 'frontend-port: closed %s:%s\n' "$FRONTEND_HOST" "$FRONTEND_PORT"
  fi
}

start_postgres() {
  local marker pid
  assert_port_free_or_owned postgres "$PG_PORT"
  if [[ -n "$(owned_pid postgres || true)" ]]; then
    printf 'postgres: already running\n'
    return
  fi
  command -v initdb >/dev/null 2>&1 || fail "initdb not found; install local PostgreSQL tools"
  command -v pg_ctl >/dev/null 2>&1 || fail "pg_ctl not found; install local PostgreSQL tools"
  command -v createdb >/dev/null 2>&1 || fail "createdb not found; install local PostgreSQL tools"
  command -v createuser >/dev/null 2>&1 || fail "createuser not found; install local PostgreSQL tools"

  marker="$(postgres_owner_marker)"
  if [[ -d "$PGDATA" && ! -f "$marker" ]]; then
    fail "existing PostgreSQL data is not owned by this local stack: $PGDATA"
  fi
  if [[ ! -d "$PGDATA" ]]; then
    initdb -D "$PGDATA" --auth-local=trust --auth-host=trust >"$LOG_DIR/postgres-init.log" 2>&1
    printf 'funding-trader-local-postgres-v1\n' >"$marker"
  fi

  clear_process_record postgres
  pg_ctl -D "$PGDATA" -l "$LOG_DIR/postgres.log" -o "-p ${PG_PORT} -c listen_addresses=127.0.0.1" start >/dev/null
  pid="$(sed -n '1p' "$PGDATA/postmaster.pid" 2>/dev/null || true)"
  if ! record_owned_process postgres "$pid" "postgres"; then
    pg_ctl -D "$PGDATA" stop -m fast >/dev/null 2>&1 || true
    fail "PostgreSQL started without a verifiable owned postmaster"
  fi

  createuser -h 127.0.0.1 -p "$PG_PORT" funding_trader >/dev/null 2>&1 || true
  createdb -h 127.0.0.1 -p "$PG_PORT" -O funding_trader funding_trader >/dev/null 2>&1 || true
  printf 'postgres: started on 127.0.0.1:%s\n' "$PG_PORT"
}

start_backend() (
  local backend_bin="${BIN_DIR}/funding-trader-api"
  (
    cd "$ROOT_DIR/backend"
    go build -o "$backend_bin" ./cmd/funding-trader-api
  )

  load_env_file "$BACKEND_ENV_FILE"
  FT_HTTP_ADDR="${FT_HTTP_ADDR:-$BACKEND_ADDR_DEFAULT}"
  FT_GATEWAY_BASE_URL="${FT_GATEWAY_BASE_URL:-$GATEWAY_URL}"
  FT_DATABASE_URL="${FT_DATABASE_URL:-postgres://funding_trader@127.0.0.1:${PG_PORT}/funding_trader?sslmode=disable}"
  FT_PRODUCTION_WRITES_ENABLED="${FT_PRODUCTION_WRITES_ENABLED:-false}"
  FT_MODE="${FT_MODE:-test}"
  export FT_HTTP_ADDR FT_GATEWAY_BASE_URL FT_DATABASE_URL FT_PRODUCTION_WRITES_ENABLED FT_MODE

  local production_writes allow_live_writes
  production_writes="$(normalize_bool "$FT_PRODUCTION_WRITES_ENABLED")" || fail "invalid FT_PRODUCTION_WRITES_ENABLED boolean"
  allow_live_writes="$(normalize_bool "${FT_LOCAL_ALLOW_LIVE_WRITES:-false}")" || fail "invalid FT_LOCAL_ALLOW_LIVE_WRITES boolean"
  if [[ "$FT_MODE" == "live" && "$production_writes" == "true" && "$allow_live_writes" != "true" ]]; then
    fail "refusing Live writes by default; set FT_LOCAL_ALLOW_LIVE_WRITES=true only for an explicit operator-approved Live write run"
  fi

  local backend_port="${FT_HTTP_ADDR##*:}"
  assert_port_free_or_owned backend "$backend_port"
  if [[ -n "$(owned_pid backend || true)" ]]; then
    printf 'backend: already running\n'
    return
  fi
  clear_process_record backend
  (
    cd "$ROOT_DIR/backend"
    nohup "$backend_bin" >"$LOG_DIR/backend.log" 2>&1 &
    local pid=$!
    if ! record_owned_process backend "$pid" "$backend_bin"; then
      kill "$pid" 2>/dev/null || true
      fail "backend process exited before ownership could be recorded"
    fi
  )
  printf 'backend: starting on %s log=%s/backend.log\n' "$FT_HTTP_ADDR" "$LOG_DIR"
)

start_frontend() (
  local vite_bin
  if [[ -n "$FRONTEND_ENV_FILE" ]]; then
    load_env_file "$FRONTEND_ENV_FILE"
  fi
  vite_bin="${FT_LOCAL_VITE_BIN:-${ROOT_DIR}/prototype/node_modules/.bin/vite}"
  FT_BACKEND_ORIGIN="${FT_BACKEND_ORIGIN:-http://$BACKEND_ADDR_DEFAULT}"
  export FT_BACKEND_ORIGIN
  assert_port_free_or_owned frontend "$FRONTEND_PORT"
  if [[ -n "$(owned_pid frontend || true)" ]]; then
    printf 'frontend: already running\n'
    return
  fi
  [[ -x "$vite_bin" ]] || fail "Vite executable not found; run npm install in prototype"
  clear_process_record frontend
  (
    cd "$ROOT_DIR/prototype"
    nohup "$vite_bin" --host "$FRONTEND_HOST" --port "$FRONTEND_PORT" >"$LOG_DIR/frontend.log" 2>&1 &
    local pid=$!
    if ! record_owned_process frontend "$pid" "$vite_bin"; then
      kill "$pid" 2>/dev/null || true
      fail "frontend process exited before ownership could be recorded"
    fi
  )
  printf 'frontend: starting on %s:%s log=%s/frontend.log\n' "$FRONTEND_HOST" "$FRONTEND_PORT" "$LOG_DIR"
)

stop_owned() {
  local name="$1"
  local pid
  pid="$(owned_pid "$name" || true)"
  if [[ -z "$pid" ]]; then
    if is_alive "$(read_pid "$name")"; then
      printf '%s: refusing unverified pidfile; process left untouched\n' "$name"
      return
    fi
    clear_process_record "$name"
    printf '%s: stopped\n' "$name"
    return
  fi
  kill "$pid"
  local attempt
  for attempt in 1 2 3 4 5 6 7 8 9 10; do
    is_alive "$pid" || break
    sleep 0.1
  done
  is_alive "$pid" && fail "${name} did not stop after SIGTERM; ownership record retained"
  clear_process_record "$name"
  printf '%s: stop requested for pid=%s\n' "$name" "$pid"
}

stop_postgres() {
  local pid postmaster_pid marker
  marker="$(postgres_owner_marker)"
  if [[ ! -f "$marker" ]]; then
    printf 'postgres: no owned pidfile; not stopping external PostgreSQL\n'
    return
  fi
  pid="$(owned_pid postgres || true)"
  postmaster_pid="$(sed -n '1p' "$PGDATA/postmaster.pid" 2>/dev/null || true)"
  if [[ -z "$pid" || "$pid" != "$postmaster_pid" ]]; then
    printf 'postgres: ownership mismatch; process left untouched\n'
    return
  fi
  pg_ctl -D "$PGDATA" stop -m fast >/dev/null
  clear_process_record postgres
  printf 'postgres: stopped\n'
}

status_with_config() (
  local status_config
  if [[ -n "$BACKEND_ENV_FILE" ]]; then
    status_config="$(
      (
        load_env_file "$BACKEND_ENV_FILE"
        printf '%s\t%s\t%s\t%s\t%s\n' \
          "${FT_GATEWAY_BASE_URL:-$GATEWAY_URL}" \
          "${FT_HTTP_ADDR:-$BACKEND_ADDR_DEFAULT}" \
          "${FT_LOCAL_PG_PORT:-$PG_PORT}" \
          "${FT_FRONTEND_HOST:-$FRONTEND_HOST}" \
          "${FT_FRONTEND_PORT:-$FRONTEND_PORT}"
      )
    )"
    IFS=$'\t' read -r GATEWAY_URL BACKEND_ADDR_DEFAULT PG_PORT FRONTEND_HOST FRONTEND_PORT <<<"$status_config"
  fi

  case "$1" in
    all) status_postgres; status_gateway; status_backend; status_frontend ;;
    postgres) status_postgres ;;
    gateway) status_gateway ;;
    backend) status_backend ;;
    frontend) status_frontend ;;
    *) fail "unknown status service: $1" ;;
  esac
)

case "$COMMAND:$SERVICE" in
  status:all|status:postgres|status:gateway|status:backend|status:frontend) status_with_config "$SERVICE" ;;
  start:all) start_postgres; start_backend; start_frontend ;;
  start:postgres) start_postgres ;;
  start:backend) start_backend ;;
  start:frontend) start_frontend ;;
  stop:all) stop_owned frontend; stop_owned backend; stop_postgres; printf 'gateway: external process left untouched\n' ;;
  stop:postgres) stop_postgres ;;
  stop:backend) stop_owned backend ;;
  stop:frontend) stop_owned frontend ;;
  *)
    usage
    [[ -n "$COMMAND" ]] && fail "unknown command/service: ${COMMAND:-<missing>} ${SERVICE:-}"
    exit 1
    ;;
esac
