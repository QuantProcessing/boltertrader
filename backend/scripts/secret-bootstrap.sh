#!/bin/sh
set -eu

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

mode=${FT_MODE:-}
key_file=${BT_SECRET_MASTER_KEY_FILE:-}
key_version=${BT_SECRET_MASTER_KEY_VERSION:-v1}
runtime_dir=${FT_LOCAL_RUNTIME_DIR:-/tmp/boltertrader-local}

case "$mode" in
  test)
    namespace=boltertrader/test
    other_mode=live
    ;;
  live)
    namespace=boltertrader/live
    other_mode=test
    ;;
  *)
    fail SECRET_STORE_KEY_MISMATCH
    ;;
esac

[ "$key_version" = v1 ] || fail SECRET_STORE_KEY_VERSION_UNSUPPORTED
[ -n "$key_file" ] && [ -f "$key_file" ] && [ -s "$key_file" ] || fail SECRET_STORE_KEY_MISSING

permissions=$(stat -f '%Lp' "$key_file" 2>/dev/null || stat -c '%a' "$key_file" 2>/dev/null) || fail SECRET_STORE_KEY_MISSING
[ "$permissions" = 600 ] || fail SECRET_STORE_KEY_MISMATCH

if command -v sha256sum >/dev/null 2>&1; then
  key_check=$(sha256sum "$key_file" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  key_check=$(shasum -a 256 "$key_file" | awk '{print $1}')
else
  fail SECRET_STORE_KEY_VERSION_UNSUPPORTED
fi
[ -n "$key_check" ] || fail SECRET_STORE_KEY_MISMATCH

state_dir=$runtime_dir/secret-store
mode_dir=$state_dir/$mode
other_metadata=$state_dir/$other_mode/bootstrap.meta

umask 077
mkdir -p "$mode_dir"
chmod 700 "$state_dir" "$mode_dir"

if [ -f "$other_metadata" ] && grep -Fqx "key_check=sha256:$key_check" "$other_metadata"; then
  fail SECRET_STORE_KEY_MISMATCH
fi

metadata_tmp=$mode_dir/bootstrap.meta.tmp.$$
trap 'rm -f "$metadata_tmp"' EXIT INT TERM
{
  printf 'namespace=%s\n' "$namespace"
  printf 'key_version=%s\n' "$key_version"
  printf 'key_check=sha256:%s\n' "$key_check"
} >"$metadata_tmp"
chmod 600 "$metadata_tmp"
mv "$metadata_tmp" "$mode_dir/bootstrap.meta"
trap - EXIT INT TERM
