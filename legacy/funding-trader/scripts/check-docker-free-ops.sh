#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ops_doc="${repo_root}/docs/OPERATIONS.md"

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

require_file() {
  local path="$1"
  [[ -f "${repo_root}/${path}" ]] || fail "missing ${path}"
}

require_executable() {
  local path="$1"
  [[ -x "${repo_root}/${path}" ]] || fail "${path} is not executable"
}

require_doc_text() {
  local pattern="$1"
  local label="$2"
  grep -Eq "$pattern" "$ops_doc" || fail "OPERATIONS.md missing ${label}"
}

forbidden_doc_text() {
  local pattern="$1"
  local label="$2"
  if grep -Eiq "$pattern" "$ops_doc"; then
    fail "OPERATIONS.md still contains ${label}"
  fi
}

require_file "docs/OPERATIONS.md"
require_file "scripts/local-dev-stack.sh"
require_executable "scripts/local-dev-stack.sh"
require_file "scripts/local-dev-stack.test.sh"
require_executable "scripts/local-dev-stack.test.sh"
require_file "backend/scripts/gateway-acceptance.sh"
require_executable "backend/scripts/gateway-acceptance.sh"

forbidden_doc_text 'docker compose|Docker 可运行本地 PostgreSQL' "Docker-dependent local run instructions"

require_doc_text '15432' "PostgreSQL 15432 guidance"
require_doc_text '18082' "Gateway 18082 guidance"
require_doc_text '18091' "backend 18091 guidance"
require_doc_text '13001' "frontend 13001 guidance"
require_doc_text 'FT_PRODUCTION_WRITES_ENABLED=false' "default-deny live write guidance"
require_doc_text 'FT_LOCAL_RUNTIME_DIR|/tmp/funding-trader-local' "runtime outside repository guidance"
require_doc_text '4 KiB|4096|TCC' "macOS Docker metadata stub note"

"${repo_root}/scripts/local-dev-stack.test.sh"

printf 'PASS: Docker-free operations runbook and scripts are present\n'
