#!/bin/sh
set -eu

: "${PSQL:=psql}"

admin_user=${PGUSER:-$(id -un)}

"$PSQL" -v ON_ERROR_STOP=1 -d postgres <<'SQL'
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bolter_platform_test') THEN
    CREATE ROLE bolter_platform_test LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
  ELSE
    ALTER ROLE bolter_platform_test LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bolter_gateway_test') THEN
    CREATE ROLE bolter_gateway_test LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
  ELSE
    ALTER ROLE bolter_gateway_test LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bolter_platform_live') THEN
    CREATE ROLE bolter_platform_live LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
  ELSE
    ALTER ROLE bolter_platform_live LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bolter_gateway_live') THEN
    CREATE ROLE bolter_gateway_live LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
  ELSE
    ALTER ROLE bolter_gateway_live LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
  END IF;
END
$$;

SELECT 'CREATE DATABASE bolter_platform_test OWNER bolter_platform_test'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_platform_test')\gexec
SELECT 'CREATE DATABASE bolter_gateway_test OWNER bolter_gateway_test'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_gateway_test')\gexec
SELECT 'CREATE DATABASE bolter_platform_live OWNER bolter_platform_live'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_platform_live')\gexec
SELECT 'CREATE DATABASE bolter_gateway_live OWNER bolter_gateway_live'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_gateway_live')\gexec

REVOKE CONNECT ON DATABASE bolter_platform_test FROM PUBLIC;
REVOKE CONNECT ON DATABASE bolter_gateway_test FROM PUBLIC;
REVOKE CONNECT ON DATABASE bolter_platform_live FROM PUBLIC;
REVOKE CONNECT ON DATABASE bolter_gateway_live FROM PUBLIC;
GRANT CONNECT ON DATABASE bolter_platform_test TO bolter_platform_test;
GRANT CONNECT ON DATABASE bolter_gateway_test TO bolter_gateway_test;
GRANT CONNECT ON DATABASE bolter_platform_live TO bolter_platform_live;
GRANT CONNECT ON DATABASE bolter_gateway_live TO bolter_gateway_live;
SQL

verify_owner_access() {
  role=$1
  database=$2
  PGUSER="$role" "$PSQL" -v ON_ERROR_STOP=1 -d "$database" -c 'SELECT 1' >/dev/null
}

verify_cross_access_denied() {
  role=$1
  database=$2
  if PGUSER="$role" "$PSQL" -v ON_ERROR_STOP=1 -d "$database" -c 'SELECT 1' >/dev/null 2>&1; then
    printf 'database isolation failure for role %s\n' "$role" >&2
    exit 1
  fi
}

for owner in bolter_platform_test bolter_gateway_test bolter_platform_live bolter_gateway_live; do
  verify_owner_access "$owner" "$owner"
  for database in bolter_platform_test bolter_gateway_test bolter_platform_live bolter_gateway_live; do
    if [ "$owner" != "$database" ]; then
      verify_cross_access_denied "$owner" "$database"
    fi
  done
done

PGUSER=$admin_user "$PSQL" -v ON_ERROR_STOP=1 -d postgres <<'SQL' >/dev/null
DO $$
BEGIN
  IF (
    SELECT count(*)
    FROM pg_roles
    WHERE rolname IN ('bolter_platform_test', 'bolter_gateway_test', 'bolter_platform_live', 'bolter_gateway_live')
      AND NOT rolsuper
      AND NOT rolcreatedb
      AND NOT rolcreaterole
      AND NOT rolinherit
      AND NOT rolreplication
  ) <> 4 THEN
    RAISE EXCEPTION 'database owner role isolation check failed';
  END IF;
END
$$;
SQL
