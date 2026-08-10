#!/bin/sh
set -eu

: "${PSQL:=psql}"

admin_user=${PGUSER:-$(id -un)}

# The invoking local PostgreSQL administrator bootstraps only the bounded
# maintenance identity. All application-role changes happen under that role.
"$PSQL" -v ON_ERROR_STOP=1 -d postgres <<'SQL'
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bolter_platform_test_admin') THEN
    CREATE ROLE bolter_platform_test_admin LOGIN NOSUPERUSER CREATEDB CREATEROLE NOINHERIT NOREPLICATION;
  ELSE
    ALTER ROLE bolter_platform_test_admin LOGIN NOSUPERUSER CREATEDB CREATEROLE NOINHERIT NOREPLICATION;
  END IF;
END
$$;
GRANT pg_signal_backend TO bolter_platform_test_admin;

SET ROLE bolter_platform_test_admin;
SELECT CASE WHEN current_user = 'bolter_platform_test_admin' THEN current_user ELSE null END;

DO $$
DECLARE
  role_name text;
BEGIN
  FOREACH role_name IN ARRAY ARRAY[
    'bolter_funding_test_migrator',
    'bolter_platform_test_migrator',
    'bolter_gateway_test_migrator'
  ] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('CREATE ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION', role_name);
    ELSE
      EXECUTE format('ALTER ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION', role_name);
    END IF;
  END LOOP;

  FOREACH role_name IN ARRAY ARRAY['bolter_funding_test', 'bolter_platform_test', 'bolter_gateway_test'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION', role_name);
    ELSE
      EXECUTE format('ALTER ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION', role_name);
    END IF;
  END LOOP;

  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bolter_funding_test_writer') THEN
    CREATE ROLE bolter_funding_test_writer LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
  ELSE
    ALTER ROLE bolter_funding_test_writer LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
  END IF;
  FOREACH role_name IN ARRAY ARRAY['bolter_platform_test_writer', 'bolter_gateway_test_writer'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('CREATE ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION', role_name);
    ELSE
      EXECUTE format('ALTER ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION', role_name);
    END IF;
  END LOOP;

  FOREACH role_name IN ARRAY ARRAY['bolter_platform_live', 'bolter_gateway_live'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION', role_name);
    ELSE
      EXECUTE format('ALTER ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION', role_name);
    END IF;
  END LOOP;
END
$$;

GRANT bolter_funding_test_migrator TO bolter_platform_test_admin;
GRANT bolter_platform_test_migrator TO bolter_platform_test_admin;
GRANT bolter_gateway_test_migrator TO bolter_platform_test_admin;
GRANT bolter_funding_test_writer TO bolter_platform_test_admin WITH ADMIN OPTION;
GRANT bolter_platform_test TO bolter_platform_test_admin WITH ADMIN OPTION;
GRANT bolter_platform_test_writer TO bolter_platform_test_admin WITH ADMIN OPTION;
GRANT bolter_gateway_test TO bolter_platform_test_admin WITH ADMIN OPTION;
GRANT bolter_gateway_test_writer TO bolter_platform_test_admin WITH ADMIN OPTION;

SELECT 'CREATE DATABASE bolter_funding_test OWNER bolter_funding_test_migrator'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_funding_test')\gexec
SELECT 'CREATE DATABASE bolter_platform_test OWNER bolter_platform_test_migrator'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_platform_test')\gexec
SELECT 'CREATE DATABASE bolter_gateway_test OWNER bolter_gateway_test_migrator'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_gateway_test')\gexec
SELECT 'CREATE DATABASE bolter_maintenance_test OWNER bolter_platform_test_admin'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_maintenance_test')\gexec
SELECT 'CREATE DATABASE bolter_platform_live OWNER bolter_platform_live'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_platform_live')\gexec
SELECT 'CREATE DATABASE bolter_gateway_live OWNER bolter_gateway_live'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_gateway_live')\gexec

ALTER DATABASE bolter_funding_test OWNER TO bolter_funding_test_migrator;
ALTER DATABASE bolter_platform_test OWNER TO bolter_platform_test_migrator;
ALTER DATABASE bolter_gateway_test OWNER TO bolter_gateway_test_migrator;

REVOKE CONNECT ON DATABASE bolter_funding_test FROM PUBLIC;
REVOKE CONNECT ON DATABASE bolter_platform_test FROM PUBLIC;
REVOKE CONNECT ON DATABASE bolter_gateway_test FROM PUBLIC;
REVOKE CONNECT ON DATABASE bolter_maintenance_test FROM PUBLIC;
REVOKE CONNECT ON DATABASE bolter_platform_live FROM PUBLIC;
REVOKE CONNECT ON DATABASE bolter_gateway_live FROM PUBLIC;
GRANT CONNECT ON DATABASE bolter_funding_test TO bolter_funding_test_migrator, bolter_funding_test;
GRANT CONNECT ON DATABASE bolter_platform_test TO bolter_platform_test_migrator, bolter_platform_test;
GRANT CONNECT ON DATABASE bolter_gateway_test TO bolter_gateway_test_migrator, bolter_gateway_test;
GRANT CONNECT ON DATABASE bolter_maintenance_test TO bolter_platform_test_admin;
GRANT CONNECT ON DATABASE bolter_platform_live TO bolter_platform_live;
GRANT CONNECT ON DATABASE bolter_gateway_live TO bolter_gateway_live;

RESET ROLE;
SELECT CASE WHEN current_user = session_user THEN current_user ELSE null END;
SQL

configure_test_database() {
  database=$1
  migrator=$2
  runtime=$3
  writer=$4
  PGUSER=$admin_user "$PSQL" -v ON_ERROR_STOP=1 -d "$database" \
    -v migrator="$migrator" -v runtime="$runtime" -v writer="$writer" <<'SQL'
SET ROLE :"migrator";
SELECT CASE WHEN current_user = :'migrator' THEN current_user ELSE null END;
ALTER SCHEMA public OWNER TO :"migrator";
REVOKE ALL ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO :"runtime", :"writer";
RESET ROLE;
SELECT CASE WHEN current_user = session_user THEN current_user ELSE null END;
SQL
}

configure_test_database bolter_funding_test bolter_funding_test_migrator bolter_funding_test bolter_funding_test_writer
configure_test_database bolter_platform_test bolter_platform_test_migrator bolter_platform_test bolter_platform_test_writer
configure_test_database bolter_gateway_test bolter_gateway_test_migrator bolter_gateway_test bolter_gateway_test_writer

PGUSER=$admin_user "$PSQL" -v ON_ERROR_STOP=1 -d postgres <<'SQL' >/dev/null
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_roles
    WHERE rolname IN ('bolter_funding_test', 'bolter_platform_test', 'bolter_gateway_test')
      AND (rolsuper OR rolcreatedb OR rolcreaterole OR NOT rolinherit OR rolreplication)
  ) THEN
    RAISE EXCEPTION 'runtime role invariant failed';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_roles
    WHERE rolname IN ('bolter_funding_test_migrator', 'bolter_platform_test_migrator', 'bolter_gateway_test_migrator')
      AND (rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolinherit OR rolreplication)
  ) THEN
    RAISE EXCEPTION 'migrator role invariant failed';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname='bolter_platform_test_admin'
      AND rolcanlogin AND NOT rolsuper AND rolcreatedb AND rolcreaterole AND NOT rolinherit AND NOT rolreplication
  ) THEN
    RAISE EXCEPTION 'maintenance role invariant failed';
  END IF;
END
$$;
SQL
