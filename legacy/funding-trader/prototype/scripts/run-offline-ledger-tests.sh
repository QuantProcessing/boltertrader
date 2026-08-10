#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
prototype_dir="$(cd "${script_dir}/.." && pwd)"
repository_dir="$(cd "${prototype_dir}/.." && pwd)"

cd "${repository_dir}/backend"
go test \
  ./internal/ledgerfacts \
  ./internal/ledgerprojection \
  ./internal/ledgerreconcile \
  ./internal/ledgersync \
  ./internal/readiness \
  ./internal/httpapi \
  ./internal/store/postgres

cd "${prototype_dir}"
npm test
npm run build
