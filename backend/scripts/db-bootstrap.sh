#!/bin/sh
set -eu

: "${PSQL:=psql}"

cluster_admin_user=${PGUSER:-$(id -un)}
maintenance_user=bolter_platform_test_admin

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

DO $$
DECLARE
  role_name text;
BEGIN
  FOREACH role_name IN ARRAY ARRAY[
    'bolter_funding_test_migrator',
    'bolter_platform_test_migrator',
    'bolter_gateway_test_migrator',
    'bolter_funding_test',
    'bolter_platform_test',
    'bolter_gateway_test',
    'bolter_funding_test_writer',
    'bolter_platform_test_writer',
    'bolter_gateway_test_writer',
    'bolter_platform_live',
    'bolter_gateway_live'
  ] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('GRANT %I TO bolter_platform_test_admin WITH ADMIN OPTION', role_name);
    END IF;
  END LOOP;
END
$$;

SQL

PGUSER=$maintenance_user "$PSQL" -v ON_ERROR_STOP=1 -d postgres <<'SQL'
DO $$
BEGIN
  IF current_user <> session_user OR current_user <> 'bolter_platform_test_admin' THEN
    RAISE EXCEPTION 'maintenance session identity mismatch';
  END IF;
END
$$;

DO $$
DECLARE
  role_name text;
BEGIN
  IF EXISTS (
    SELECT 1
    FROM pg_roles
    WHERE rolname IN (
      'bolter_funding_test_migrator',
      'bolter_platform_test_migrator',
      'bolter_gateway_test_migrator',
      'bolter_funding_test',
      'bolter_platform_test',
      'bolter_gateway_test',
      'bolter_funding_test_writer',
      'bolter_platform_test_writer',
      'bolter_gateway_test_writer',
      'bolter_platform_live',
      'bolter_gateway_live'
    ) AND (rolsuper OR rolreplication OR rolbypassrls)
  ) THEN
    RAISE EXCEPTION 'managed role has a protected privileged attribute';
  END IF;

  FOREACH role_name IN ARRAY ARRAY[
    'bolter_funding_test_migrator',
    'bolter_platform_test_migrator',
    'bolter_gateway_test_migrator'
  ] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('CREATE ROLE %I NOLOGIN NOCREATEDB NOCREATEROLE NOINHERIT', role_name);
    ELSE
      EXECUTE format('ALTER ROLE %I NOLOGIN NOCREATEDB NOCREATEROLE NOINHERIT', role_name);
    END IF;
  END LOOP;

  FOREACH role_name IN ARRAY ARRAY['bolter_funding_test', 'bolter_platform_test', 'bolter_gateway_test'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('CREATE ROLE %I LOGIN NOCREATEDB NOCREATEROLE INHERIT', role_name);
    ELSE
      EXECUTE format('ALTER ROLE %I LOGIN NOCREATEDB NOCREATEROLE INHERIT', role_name);
    END IF;
  END LOOP;

  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'bolter_funding_test_writer') THEN
    CREATE ROLE bolter_funding_test_writer LOGIN NOCREATEDB NOCREATEROLE NOINHERIT;
  ELSE
    ALTER ROLE bolter_funding_test_writer LOGIN NOCREATEDB NOCREATEROLE NOINHERIT;
  END IF;
  FOREACH role_name IN ARRAY ARRAY['bolter_platform_test_writer', 'bolter_gateway_test_writer'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('CREATE ROLE %I NOLOGIN NOCREATEDB NOCREATEROLE NOINHERIT', role_name);
    ELSE
      EXECUTE format('ALTER ROLE %I NOLOGIN NOCREATEDB NOCREATEROLE NOINHERIT', role_name);
    END IF;
  END LOOP;

  FOREACH role_name IN ARRAY ARRAY['bolter_platform_live', 'bolter_gateway_live'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
      EXECUTE format('CREATE ROLE %I LOGIN NOCREATEDB NOCREATEROLE INHERIT', role_name);
    ELSE
      EXECUTE format('ALTER ROLE %I LOGIN NOCREATEDB NOCREATEROLE INHERIT', role_name);
    END IF;
  END LOOP;
END
$$;

SQL

PGUSER=$cluster_admin_user "$PSQL" -v ON_ERROR_STOP=1 -d postgres <<'SQL'
REVOKE bolter_funding_test_migrator, bolter_platform_test_migrator, bolter_gateway_test_migrator,
  bolter_funding_test, bolter_platform_test, bolter_gateway_test,
  bolter_funding_test_writer, bolter_platform_test_writer, bolter_gateway_test_writer,
  bolter_platform_live, bolter_gateway_live
FROM bolter_platform_test_admin GRANTED BY bolter_platform_test_admin;
GRANT bolter_funding_test_migrator, bolter_platform_test_migrator, bolter_gateway_test_migrator
  TO bolter_platform_test_admin WITH SET TRUE;
GRANT bolter_funding_test_migrator, bolter_platform_test_migrator, bolter_gateway_test_migrator
  TO bolter_platform_test_admin WITH INHERIT FALSE;
GRANT bolter_funding_test_migrator, bolter_platform_test_migrator, bolter_gateway_test_migrator
  TO bolter_platform_test_admin WITH ADMIN FALSE;
GRANT bolter_funding_test_writer, bolter_platform_test, bolter_platform_test_writer,
  bolter_gateway_test, bolter_gateway_test_writer
  TO bolter_platform_test_admin WITH ADMIN TRUE;
GRANT bolter_funding_test_writer, bolter_platform_test, bolter_platform_test_writer,
  bolter_gateway_test, bolter_gateway_test_writer
  TO bolter_platform_test_admin WITH INHERIT FALSE;
GRANT bolter_funding_test_writer, bolter_platform_test, bolter_platform_test_writer,
  bolter_gateway_test, bolter_gateway_test_writer
  TO bolter_platform_test_admin WITH SET FALSE;
GRANT bolter_platform_live, bolter_gateway_live
  TO bolter_platform_test_admin WITH ADMIN FALSE;
GRANT bolter_platform_live, bolter_gateway_live
  TO bolter_platform_test_admin WITH INHERIT FALSE;
GRANT bolter_platform_live, bolter_gateway_live
  TO bolter_platform_test_admin WITH SET TRUE;
GRANT pg_signal_backend TO bolter_platform_test_admin WITH ADMIN FALSE;
GRANT pg_signal_backend TO bolter_platform_test_admin WITH INHERIT FALSE;
GRANT pg_signal_backend TO bolter_platform_test_admin WITH SET TRUE;
REVOKE bolter_funding_test FROM bolter_platform_test_admin;
SELECT 'ALTER DATABASE bolter_funding_test OWNER TO bolter_funding_test_migrator'
WHERE EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_funding_test')\gexec
SELECT 'ALTER DATABASE bolter_platform_test OWNER TO bolter_platform_test_migrator'
WHERE EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_platform_test')\gexec
SELECT 'ALTER DATABASE bolter_gateway_test OWNER TO bolter_gateway_test_migrator'
WHERE EXISTS (SELECT 1 FROM pg_database WHERE datname = 'bolter_gateway_test')\gexec
SQL

PGUSER=$maintenance_user "$PSQL" -v ON_ERROR_STOP=1 -d postgres <<'SQL'
DO $$
BEGIN
  IF current_user <> session_user OR current_user <> 'bolter_platform_test_admin' THEN
    RAISE EXCEPTION 'maintenance session identity mismatch';
  END IF;
END
$$;

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

SET ROLE bolter_funding_test_migrator;
SELECT CASE WHEN current_user = 'bolter_funding_test_migrator' THEN current_user ELSE null END;
ALTER DATABASE bolter_funding_test OWNER TO bolter_funding_test_migrator;
REVOKE CONNECT ON DATABASE bolter_funding_test FROM PUBLIC;
GRANT CONNECT ON DATABASE bolter_funding_test TO bolter_funding_test_migrator, bolter_funding_test;
GRANT CONNECT ON DATABASE bolter_funding_test TO bolter_platform_test_admin;
RESET ROLE;
SELECT CASE WHEN current_user = session_user AND current_user = 'bolter_platform_test_admin' THEN current_user ELSE null END;

SET ROLE bolter_platform_test_migrator;
SELECT CASE WHEN current_user = 'bolter_platform_test_migrator' THEN current_user ELSE null END;
ALTER DATABASE bolter_platform_test OWNER TO bolter_platform_test_migrator;
REVOKE CONNECT ON DATABASE bolter_platform_test FROM PUBLIC;
GRANT CONNECT ON DATABASE bolter_platform_test TO bolter_platform_test_migrator, bolter_platform_test;
GRANT CONNECT ON DATABASE bolter_platform_test TO bolter_platform_test_admin;
RESET ROLE;
SELECT CASE WHEN current_user = session_user AND current_user = 'bolter_platform_test_admin' THEN current_user ELSE null END;

SET ROLE bolter_gateway_test_migrator;
SELECT CASE WHEN current_user = 'bolter_gateway_test_migrator' THEN current_user ELSE null END;
ALTER DATABASE bolter_gateway_test OWNER TO bolter_gateway_test_migrator;
REVOKE CONNECT ON DATABASE bolter_gateway_test FROM PUBLIC;
GRANT CONNECT ON DATABASE bolter_gateway_test TO bolter_gateway_test_migrator, bolter_gateway_test;
GRANT CONNECT ON DATABASE bolter_gateway_test TO bolter_platform_test_admin;
RESET ROLE;
SELECT CASE WHEN current_user = session_user AND current_user = 'bolter_platform_test_admin' THEN current_user ELSE null END;

REVOKE CONNECT ON DATABASE bolter_maintenance_test FROM PUBLIC;
GRANT CONNECT ON DATABASE bolter_maintenance_test TO bolter_platform_test_admin;

SET ROLE bolter_platform_live;
SELECT CASE WHEN current_user = 'bolter_platform_live' THEN current_user ELSE null END;
ALTER DATABASE bolter_platform_live OWNER TO bolter_platform_live;
REVOKE CONNECT ON DATABASE bolter_platform_live FROM PUBLIC;
GRANT CONNECT ON DATABASE bolter_platform_live TO bolter_platform_live;
RESET ROLE;
SELECT CASE WHEN current_user = session_user AND current_user = 'bolter_platform_test_admin' THEN current_user ELSE null END;

SET ROLE bolter_gateway_live;
SELECT CASE WHEN current_user = 'bolter_gateway_live' THEN current_user ELSE null END;
ALTER DATABASE bolter_gateway_live OWNER TO bolter_gateway_live;
REVOKE CONNECT ON DATABASE bolter_gateway_live FROM PUBLIC;
GRANT CONNECT ON DATABASE bolter_gateway_live TO bolter_gateway_live;
RESET ROLE;
SELECT CASE WHEN current_user = session_user AND current_user = 'bolter_platform_test_admin' THEN current_user ELSE null END;
SQL

PGUSER=$cluster_admin_user "$PSQL" -v ON_ERROR_STOP=1 -d postgres <<'SQL'
REVOKE bolter_platform_live, bolter_gateway_live FROM bolter_platform_test_admin;
SQL

configure_test_database() {
  database=$1
  migrator=$2
  runtime=$3
  writer=$4
  PGUSER=$maintenance_user "$PSQL" -v ON_ERROR_STOP=1 -d "$database" \
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

PGUSER=$maintenance_user "$PSQL" -v ON_ERROR_STOP=1 -d postgres <<'SQL' >/dev/null
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_roles
    WHERE rolname IN ('bolter_funding_test', 'bolter_platform_test', 'bolter_gateway_test')
      AND (rolsuper OR rolcreatedb OR rolcreaterole OR NOT rolinherit OR rolreplication OR rolbypassrls)
  ) THEN
    RAISE EXCEPTION 'runtime role invariant failed';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_roles
    WHERE rolname IN ('bolter_funding_test_migrator', 'bolter_platform_test_migrator', 'bolter_gateway_test_migrator')
      AND (rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolinherit OR rolreplication OR rolbypassrls)
  ) THEN
    RAISE EXCEPTION 'migrator role invariant failed';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_roles WHERE rolname='bolter_platform_test_admin'
      AND rolcanlogin AND NOT rolsuper AND rolcreatedb AND rolcreaterole AND NOT rolinherit AND NOT rolreplication AND NOT rolbypassrls
  ) THEN
    RAISE EXCEPTION 'maintenance role invariant failed';
  END IF;
END
$$;
SQL
