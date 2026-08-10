#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d /tmp/funding-trader-local-dev-test.XXXXXX)"
fake_bin="${test_root}/bin"
cleanup_pids=()

cleanup() {
  local pid
  for pid in "${cleanup_pids[@]:-}"; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  rm -rf "$test_root"
}
trap cleanup EXIT

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

write_fake_commands() {
  mkdir -p "$fake_bin"

  export FT_TEST_DIRECT_SERVICE_TEMPLATE="${test_root}/direct-service"
  export FT_TEST_FAKE_POSTGRES="${test_root}/postgres-test-service"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'if [[ -n "${FT_TEST_SERVICE_PID_CAPTURE:-}" ]]; then printf "%s\n" "$$" >"$FT_TEST_SERVICE_PID_CAPTURE"; fi' \
    'trap '\''exit 0'\'' TERM INT' \
    'while :; do sleep 1; done' >"$FT_TEST_DIRECT_SERVICE_TEMPLATE"
  chmod +x "$FT_TEST_DIRECT_SERVICE_TEMPLATE"
  cp "$FT_TEST_DIRECT_SERVICE_TEMPLATE" "$FT_TEST_FAKE_POSTGRES"

  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'if [[ -n "${FT_TEST_LSOF_CAPTURE:-}" ]]; then printf "%s\n" "$*" >"$FT_TEST_LSOF_CAPTURE"; fi' \
    'if [[ -n "${FT_TEST_LSOF_ENV_CAPTURE:-}" ]]; then' \
    '  if [[ -n "${FT_GATEWAY_TOKEN:-}" || -n "${FT_GATEWAY_TOKEN_FILE:-}" || -n "${FT_BOOTSTRAP_PASSWORD:-}" ]]; then' \
    '    printf "leaked\n" >"$FT_TEST_LSOF_ENV_CAPTURE"' \
    '  else' \
    '    printf "clean\n" >"$FT_TEST_LSOF_ENV_CAPTURE"' \
    '  fi' \
    'fi' \
    'exit 1' >"${fake_bin}/lsof"
  printf '%s\n' '#!/usr/bin/env bash' 'mkdir -p "$2"' >"${fake_bin}/initdb"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'args=" $* "' \
    'if [[ "$args" == *" stop "* && -n "${FT_TEST_PG_CTL_MARKER:-}" ]]; then printf "invoked\n" >"$FT_TEST_PG_CTL_MARKER"; fi' \
    'pgdata=""' \
    'while [[ $# -gt 0 ]]; do' \
    '  case "$1" in' \
    '    -D) pgdata="${2:?missing -D value}"; shift 2 ;;' \
    '    *) shift ;;' \
    '  esac' \
    'done' \
    '[[ -n "$pgdata" ]] || exit 2' \
    'if [[ "$args" == *" start "* ]]; then' \
    '  nohup "${FT_TEST_FAKE_POSTGRES:?}" >/dev/null 2>&1 &' \
    '  printf "%s\n" "$!" >"$pgdata/postmaster.pid"' \
    'fi' \
    'if [[ "$args" == *" status "* ]]; then printf "pg_ctl: server is running (PID: %s)\\n" "$(sed -n '\''1p'\'' "$pgdata/postmaster.pid")"; fi' \
    'if [[ "$args" == *" stop "* && -f "$pgdata/postmaster.pid" ]]; then kill "$(sed -n '\''1p'\'' "$pgdata/postmaster.pid")" 2>/dev/null || true; fi' \
    'exit 0' >"${fake_bin}/pg_ctl"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"${fake_bin}/createdb"
  printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"${fake_bin}/createuser"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'if [[ "${1:-}" == "build" ]]; then' \
    '  while [[ $# -gt 0 ]]; do' \
    '    if [[ "$1" == "-o" ]]; then' \
    '      shift' \
    '      cp "${FT_TEST_DIRECT_SERVICE_TEMPLATE:?}" "$1"' \
    '      chmod +x "$1"' \
    '      exit 0' \
    '    fi' \
    '    shift' \
    '  done' \
    'fi' \
    'printf "go-invoked\\n" >>"${FT_TEST_GO_MARKER:?}"' \
    'if [[ -n "${FT_TEST_SERVICE_PID_CAPTURE:-}" ]]; then' \
    '  sleep 30 &' \
    '  child=$!' \
    '  printf "%s\n" "$child" >"$FT_TEST_SERVICE_PID_CAPTURE"' \
    '  wait "$child"' \
    '  exit 0' \
    'fi' \
    'trap '\''exit 0'\'' TERM INT' \
    'while :; do sleep 1; done' >"${fake_bin}/go"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'if [[ -n "${FT_BOOTSTRAP_PASSWORD:-}" || -n "${FT_GATEWAY_TOKEN:-}" || -n "${FT_GATEWAY_TOKEN_FILE:-}" ]]; then' \
    '  printf "leaked\\n" >"${FT_TEST_FRONTEND_ENV_CAPTURE:?}"' \
    'else' \
    '  printf "clean\\n" >"${FT_TEST_FRONTEND_ENV_CAPTURE:?}"' \
    'fi' \
    'if [[ -n "${FT_TEST_SERVICE_PID_CAPTURE:-}" ]]; then' \
    '  sleep 30 &' \
    '  child=$!' \
    '  printf "%s\n" "$child" >"$FT_TEST_SERVICE_PID_CAPTURE"' \
    '  wait "$child"' \
    '  exit 0' \
    'fi' \
    'trap '\''exit 0'\'' TERM INT' \
    'while :; do sleep 1; done' >"${fake_bin}/npm"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'if [[ -n "${FT_BOOTSTRAP_PASSWORD:-}" || -n "${FT_GATEWAY_TOKEN:-}" || -n "${FT_GATEWAY_TOKEN_FILE:-}" ]]; then' \
    '  printf "leaked\\n" >"${FT_TEST_FRONTEND_ENV_CAPTURE:?}"' \
    'else' \
    '  printf "clean\\n" >"${FT_TEST_FRONTEND_ENV_CAPTURE:?}"' \
    'fi' \
    'if [[ -n "${FT_TEST_SERVICE_PID_CAPTURE:-}" ]]; then printf "%s\n" "$$" >"$FT_TEST_SERVICE_PID_CAPTURE"; fi' \
    'trap '\''exit 0'\'' TERM INT' \
    'while :; do sleep 1; done' >"${fake_bin}/node"
  chmod +x "${fake_bin}"/*
}

wait_for_file() {
  local path="$1"
  local attempt
  for attempt in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
    [[ -f "$path" ]] && return 0
    sleep 0.1
  done
  return 1
}

assert_refuses_live_spelling() {
  local spelling="$1"
  local env_file="${test_root}/live-${spelling}.env"
  local output="${test_root}/live-${spelling}.out"
  local marker="${test_root}/go-${spelling}.marker"

  printf '%s\n' \
    'FT_MODE=live' \
    "FT_PRODUCTION_WRITES_ENABLED=${spelling}" \
    'FT_LOCAL_ALLOW_LIVE_WRITES=false' \
    'FT_HTTP_ADDR=127.0.0.1:28091' \
    'FT_GATEWAY_BASE_URL=http://127.0.0.1:28082' \
    'FT_GATEWAY_TOKEN=dummy-test-token' \
    'FT_BOOTSTRAP_EMAIL=owner@example.test' \
    'FT_BOOTSTRAP_PASSWORD=strong-local-test-password' >"$env_file"

  if PATH="${fake_bin}:/usr/bin:/bin" \
    FT_TEST_GO_MARKER="$marker" \
    "${repo_root}/scripts/local-dev-stack.sh" \
      --runtime-dir "${test_root}/runtime-${spelling}" \
      --backend-env "$env_file" start backend >"$output" 2>&1; then
    fail "local stack accepted FT_PRODUCTION_WRITES_ENABLED=${spelling} in Live mode"
  fi
  grep -q 'refusing Live writes' "$output" || fail "local stack did not explain the Live refusal for ${spelling}"
  [[ ! -e "$marker" ]] || fail "local stack invoked Go after the Live refusal for ${spelling}"

  if PATH="${fake_bin}:/usr/bin:/bin" \
    FT_TEST_GO_MARKER="$marker" \
    "${repo_root}/backend/scripts/gateway-acceptance.sh" \
      --env "$env_file" \
      --evidence-dir "${test_root}/evidence-${spelling}" >"$output" 2>&1; then
    fail "acceptance wrapper accepted FT_PRODUCTION_WRITES_ENABLED=${spelling} in Live mode"
  fi
  grep -q 'refusing Live writes' "$output" || fail "acceptance wrapper did not explain the Live refusal for ${spelling}"
  [[ ! -e "$marker" ]] || fail "acceptance wrapper invoked Go after the Live refusal for ${spelling}"
}

assert_backend_env_isolated_from_frontend() {
  local env_file="${test_root}/test.env"
  local capture="${test_root}/frontend-env.capture"
  local marker="${test_root}/go-test.marker"
  local runtime="${test_root}/runtime-test"
  local output="${test_root}/start-all.out"
  local attempt

  printf '%s\n' \
    'FT_MODE=test' \
    'FT_PRODUCTION_WRITES_ENABLED=false' \
    'FT_HTTP_ADDR=127.0.0.1:28091' \
    'FT_GATEWAY_BASE_URL=http://127.0.0.1:28082' \
    'FT_GATEWAY_TOKEN=backend-only-token' \
    'FT_BOOTSTRAP_EMAIL=owner@example.test' \
    'FT_BOOTSTRAP_PASSWORD=backend-only-password' >"$env_file"

  if ! PATH="${fake_bin}:/usr/bin:/bin" \
    FT_LOCAL_PG_PORT=25439 \
  FT_FRONTEND_PORT=23001 \
  FT_LOCAL_VITE_BIN="${fake_bin}/node" \
  FT_TEST_GO_MARKER="$marker" \
    FT_TEST_FRONTEND_ENV_CAPTURE="$capture" \
    "${repo_root}/scripts/local-dev-stack.sh" \
      --runtime-dir "$runtime" \
      --backend-env "$env_file" start all >"$output" 2>&1; then
    sed -n '1,20p' "$output" >&2
    if [[ -f "${runtime}/postgres-15432/postmaster.pid" ]]; then
      local postgres_pid
      postgres_pid="$(sed -n '1p' "${runtime}/postgres-15432/postmaster.pid")"
      ps -p "$postgres_pid" -o pid= -o lstart= -o command= >&2 || true
    fi
    [[ ! -f "${runtime}/logs/backend.log" ]] || sed -n '1,20p' "${runtime}/logs/backend.log" >&2
    fail "isolated local stack failed to start"
  fi

  [[ ! -f "${runtime}/pids/backend.pid" ]] || cleanup_pids+=("$(sed -n '1p' "${runtime}/pids/backend.pid")")
  [[ ! -f "${runtime}/pids/frontend.pid" ]] || cleanup_pids+=("$(sed -n '1p' "${runtime}/pids/frontend.pid")")
  [[ ! -f "${runtime}/postgres-15432/postmaster.pid" ]] || cleanup_pids+=("$(sed -n '1p' "${runtime}/postgres-15432/postmaster.pid")")

  wait_for_file "$capture" || true
  if [[ ! -f "$capture" ]]; then
    [[ ! -f "${runtime}/logs/frontend.log" ]] || sed -n '1,20p' "${runtime}/logs/frontend.log" >&2
    fail "frontend environment capture was not produced"
  fi
  [[ "$(sed -n '1p' "$capture")" == "clean" ]] || fail "backend secrets leaked into the frontend process"

  PATH="${fake_bin}:/usr/bin:/bin" \
  FT_LOCAL_PG_PORT=25439 \
  FT_FRONTEND_PORT=23001 \
  FT_LOCAL_VITE_BIN="${fake_bin}/node" \
  "${repo_root}/scripts/local-dev-stack.sh" --runtime-dir "$runtime" stop all >/dev/null 2>&1
}

assert_direct_backend_process_is_owned_and_stopped() {
  local env_file="${test_root}/direct-backend.env"
  local runtime="${test_root}/runtime-direct-backend"
  local capture="${test_root}/direct-backend.pid"
  local marker="${test_root}/go-direct.marker"
  local recorded service_pid

  printf '%s\n' \
    'FT_MODE=test' \
    'FT_PRODUCTION_WRITES_ENABLED=false' \
    'FT_HTTP_ADDR=127.0.0.1:28092' \
    'FT_GATEWAY_BASE_URL=http://127.0.0.1:28082' \
    'FT_GATEWAY_TOKEN=dummy-test-token' \
    'FT_BOOTSTRAP_EMAIL=owner@example.test' \
    'FT_BOOTSTRAP_PASSWORD=strong-local-test-password' >"$env_file"

  PATH="${fake_bin}:/usr/bin:/bin" \
  FT_TEST_GO_MARKER="$marker" \
  FT_TEST_SERVICE_PID_CAPTURE="$capture" \
  "${repo_root}/scripts/local-dev-stack.sh" --runtime-dir "$runtime" --backend-env "$env_file" start backend >/dev/null
  wait_for_file "$capture" || fail "direct backend process did not report its pid"
  recorded="$(sed -n '1p' "${runtime}/pids/backend.pid")"
  service_pid="$(sed -n '1p' "$capture")"
  cleanup_pids+=("$recorded" "$service_pid")
  [[ "$recorded" == "$service_pid" ]] || fail "backend pidfile points to a Go wrapper instead of the serving process"

  PATH="${fake_bin}:/usr/bin:/bin" \
  "${repo_root}/scripts/local-dev-stack.sh" --runtime-dir "$runtime" stop backend >/dev/null
  if kill -0 "$service_pid" 2>/dev/null; then
    fail "owned backend process remained alive after stop"
  fi
}

assert_direct_frontend_process_is_owned_and_stopped() {
  local runtime="${test_root}/runtime-direct-frontend"
  local capture="${test_root}/direct-frontend.pid"
  local env_capture="${test_root}/direct-frontend.env"
  local recorded service_pid

  PATH="${fake_bin}:/usr/bin:/bin" \
  FT_FRONTEND_PORT=23002 \
  FT_LOCAL_VITE_BIN="${fake_bin}/node" \
  FT_TEST_FRONTEND_ENV_CAPTURE="$env_capture" \
  FT_TEST_SERVICE_PID_CAPTURE="$capture" \
  "${repo_root}/scripts/local-dev-stack.sh" --runtime-dir "$runtime" start frontend >/dev/null
  wait_for_file "$capture" || fail "direct frontend process did not report its pid"
  recorded="$(sed -n '1p' "${runtime}/pids/frontend.pid")"
  service_pid="$(sed -n '1p' "$capture")"
  cleanup_pids+=("$recorded" "$service_pid")
  [[ "$recorded" == "$service_pid" ]] || fail "frontend pidfile points to an npm wrapper instead of the Vite process"
  grep -Fxq "${fake_bin}/node" "${runtime}/ownership/frontend.identity" || fail "frontend ignored the explicit Vite binary override"

  PATH="${fake_bin}:/usr/bin:/bin" \
  FT_FRONTEND_PORT=23002 \
  "${repo_root}/scripts/local-dev-stack.sh" --runtime-dir "$runtime" stop frontend >/dev/null
  if kill -0 "$service_pid" 2>/dev/null; then
    fail "owned frontend process remained alive after stop"
  fi
}

assert_status_uses_backend_env_ports() {
  local env_file="${test_root}/status.env"
  local capture="${test_root}/status-lsof.args"
  local env_capture="${test_root}/status-lsof.env"

  printf '%s\n' \
    'FT_GATEWAY_BASE_URL=http://127.0.0.1:28083' \
    'FT_GATEWAY_TOKEN=status-must-not-export-this-token' \
    'FT_BOOTSTRAP_PASSWORD=status-must-not-export-this-password' >"$env_file"
  PATH="${fake_bin}:/usr/bin:/bin" \
  FT_TEST_LSOF_CAPTURE="$capture" \
  FT_TEST_LSOF_ENV_CAPTURE="$env_capture" \
  "${repo_root}/scripts/local-dev-stack.sh" --backend-env "$env_file" status gateway >/dev/null
  grep -q -- '-iTCP:28083' "$capture" || fail "status ignored FT_GATEWAY_BASE_URL from the backend env file"
  [[ "$(sed -n '1p' "$env_capture")" == "clean" ]] || fail "status exported backend secrets into its helper process"
}

assert_unowned_postgres_data_is_never_stopped() {
  local runtime="${test_root}/runtime-unowned-postgres"
  local marker="${test_root}/pg-ctl-stop.marker"

  mkdir -p "${runtime}/pids" "${runtime}/postgres-15432"
  printf '%s\n' '999999' >"${runtime}/pids/postgres.pid"
  PATH="${fake_bin}:/usr/bin:/bin" \
  FT_TEST_PG_CTL_MARKER="$marker" \
  "${repo_root}/scripts/local-dev-stack.sh" --runtime-dir "$runtime" stop postgres >/dev/null 2>&1 || true
  [[ ! -e "$marker" ]] || fail "unowned PostgreSQL data caused pg_ctl stop to run"
}

assert_untrusted_pidfile_cannot_kill_process() {
  local runtime="${test_root}/runtime-pid"
  local innocent_pid

  mkdir -p "${runtime}/pids"
  sleep 30 &
  innocent_pid=$!
  cleanup_pids+=("$innocent_pid")
  printf '%s\n' "$innocent_pid" >"${runtime}/pids/backend.pid"

  "${repo_root}/scripts/local-dev-stack.sh" --runtime-dir "$runtime" stop backend >/dev/null 2>&1 || true
  kill -0 "$innocent_pid" 2>/dev/null || fail "an untrusted pidfile killed an unrelated process"
}

assert_exactly_one_gateway_token_source() {
  local env_file="${test_root}/both-tokens.env"
  local output="${test_root}/both-tokens.out"
  local marker="${test_root}/go-both-tokens.marker"
  local token_file="${test_root}/gateway.token"

  printf '%s\n' 'dummy-token-file-value' >"$token_file"
  printf '%s\n' \
    'FT_MODE=test' \
    'FT_PRODUCTION_WRITES_ENABLED=false' \
    'FT_GATEWAY_BASE_URL=http://127.0.0.1:28082' \
    'FT_GATEWAY_TOKEN=dummy-inline-token' \
    "FT_GATEWAY_TOKEN_FILE=${token_file}" >"$env_file"

  if PATH="${fake_bin}:/usr/bin:/bin" \
    FT_TEST_GO_MARKER="$marker" \
    "${repo_root}/backend/scripts/gateway-acceptance.sh" \
      --env "$env_file" \
      --evidence-dir "${test_root}/evidence-both" >"$output" 2>&1; then
    fail "acceptance wrapper allowed both Gateway token sources"
  fi
  grep -q 'exactly one' "$output" || fail "acceptance wrapper did not explain the token-source conflict"
  [[ ! -e "$marker" ]] || fail "acceptance wrapper invoked Go with conflicting token sources"
}

write_fake_commands
for spelling in TRUE 1 t; do
  assert_refuses_live_spelling "$spelling"
done
assert_backend_env_isolated_from_frontend
assert_untrusted_pidfile_cannot_kill_process
assert_exactly_one_gateway_token_source
assert_direct_backend_process_is_owned_and_stopped
assert_direct_frontend_process_is_owned_and_stopped
assert_status_uses_backend_env_ports
assert_unowned_postgres_data_is_never_stopped

printf 'PASS: Docker-free local lifecycle safety checks passed\n'
