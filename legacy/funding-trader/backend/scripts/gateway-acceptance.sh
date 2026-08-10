#!/usr/bin/env bash
set -euo pipefail

BACKEND_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE=""
EVIDENCE_DIR=""

usage() {
  cat <<'USAGE'
Usage:
  backend/scripts/gateway-acceptance.sh --env FILE --evidence-dir DIR

Runs the explicit BolterTrader Gateway read-only and acceptance-gate tests
against an operator-configured Gateway. The env file supplies FT_MODE,
FT_GATEWAY_BASE_URL, and exactly one of FT_GATEWAY_TOKEN or FT_GATEWAY_TOKEN_FILE.
No token or exchange secret is printed by this wrapper.

The wrapper refuses Live writes by default. It does not enable private writes;
it only runs read-only discovery plus the two-leg acceptance gate.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env)
      ENV_FILE="${2:?missing value for --env}"
      shift 2
      ;;
    --evidence-dir)
      EVIDENCE_DIR="${2:?missing value for --evidence-dir}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'ERROR: unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

[[ -n "$ENV_FILE" ]] || { usage >&2; exit 1; }
[[ -f "$ENV_FILE" ]] || { printf 'ERROR: env file not found: %s\n' "$ENV_FILE" >&2; exit 1; }
[[ -n "$EVIDENCE_DIR" ]] || { usage >&2; exit 1; }
mkdir -p "$EVIDENCE_DIR"

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

FT_MODE="${FT_MODE:-test}"
FT_PRODUCTION_WRITES_ENABLED="${FT_PRODUCTION_WRITES_ENABLED:-false}"
export FT_MODE FT_PRODUCTION_WRITES_ENABLED

normalize_bool() {
  case "$1" in
    1|t|T|true|TRUE|True) printf 'true' ;;
    0|f|F|false|FALSE|False) printf 'false' ;;
    *) return 1 ;;
  esac
}

production_writes="$(normalize_bool "$FT_PRODUCTION_WRITES_ENABLED")" || {
  printf 'ERROR: invalid FT_PRODUCTION_WRITES_ENABLED boolean\n' >&2
  exit 1
}
allow_live_writes="$(normalize_bool "${FT_LOCAL_ALLOW_LIVE_WRITES:-false}")" || {
  printf 'ERROR: invalid FT_LOCAL_ALLOW_LIVE_WRITES boolean\n' >&2
  exit 1
}

if [[ "$FT_MODE" == "live" && "$production_writes" == "true" && "$allow_live_writes" != "true" ]]; then
  printf 'ERROR: refusing Live writes by default; unset FT_PRODUCTION_WRITES_ENABLED or keep it false for acceptance evidence\n' >&2
  exit 1
fi

if [[ -n "${FT_GATEWAY_TOKEN:-}" && -n "${FT_GATEWAY_TOKEN_FILE:-}" ]] ||
  [[ -z "${FT_GATEWAY_TOKEN:-}" && -z "${FT_GATEWAY_TOKEN_FILE:-}" ]]; then
  printf 'ERROR: set exactly one of FT_GATEWAY_TOKEN_FILE or FT_GATEWAY_TOKEN in the operator env file\n' >&2
  exit 1
fi

if [[ -z "${FT_GATEWAY_BASE_URL:-}" ]]; then
  printf 'ERROR: set FT_GATEWAY_BASE_URL in the operator env file\n' >&2
  exit 1
fi

LOG_FILE="${EVIDENCE_DIR%/}/gateway-acceptance.log"
(
  cd "$BACKEND_DIR"
  RUN_GATEWAY_READ=1 \
  RUN_GATEWAY_PUBLIC_READ=1 \
  RUN_GATEWAY_ACCEPTANCE_GATE=1 \
  go test ./internal/integrations/boltertrader \
    -run 'TestGateway(ReadOnly|PublicMarket)Live|TestGatewayTwoLegWriteAcceptanceLive' \
    -count=1 -v
) 2>&1 | tee "$LOG_FILE"

printf 'gateway acceptance evidence: %s\n' "$LOG_FILE"
