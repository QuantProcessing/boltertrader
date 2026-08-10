#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
prototype_dir="$(cd "${script_dir}/.." && pwd)"

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <run-directory>/browser-trading-acceptance-evidence.json" >&2
  exit 2
fi

manifest_path="$1"

cd "${prototype_dir}"
node ./scripts/validate-browser-evidence.mjs "${manifest_path}"
