-- Cluster layout for the local stack: three databases, and the role topology
-- each one's code already insists on.
--
-- The official postgres image runs every file in /docker-entrypoint-initdb.d
-- exactly once, against an empty data directory, as the bootstrap superuser and
-- under `psql -v ON_ERROR_STOP=1`. Two consequences shape this file. First, a
-- single failing statement aborts the bring-up rather than leaving a
-- half-provisioned cluster that fails later somewhere unrelated. Second, and
-- less comfortably: because the trigger is an empty data directory and not a
-- version check, a named volume surviving from an earlier run means this file
-- does not execute at all. A cluster whose roles predate an edit here starts
-- cleanly and then fails deep inside a migration, so `make doctor` looks for
-- these roles rather than assuming the file ever ran.
--
-- Passwords are deliberately absent. Every process in the stack builds its DSN
-- as `postgres://<role>@<host>/<database>?sslmode=disable` and assembles no
-- password, so this cluster is only reachable under `trust` authentication.
-- That is set by POSTGRES_HOST_AUTH_METHOD in compose, not here, and it is only
-- defensible because the port is published to 127.0.0.1 alone.

-- ---------------------------------------------------------------------------
-- Gateway roles.
--
-- The gateway is the one service that verifies its own authority topology:
-- gateway/authority.GatewayV8 names these four roles, and VerifyRuntimeCatalog
-- reads the catalog back at each start and refuses to serve if the cluster
-- disagrees. The names are therefore a contract rather than a convention, and
-- each property below is load-bearing for a specific check.
-- ---------------------------------------------------------------------------

-- Maintenance exists to receive the CONNECT grant ApplyAuthority issues; no
-- process authenticates as it, and the grant would error if the role were
-- missing. NOLOGIN because a login role nothing uses is a login role whose use
-- nobody would notice.
CREATE ROLE bolter_platform_admin NOLOGIN;

-- The writer is a privilege container, never a connection. VerifyRuntimeCatalog
-- fails when it finds WriterConnect true, so this role must not be able to
-- reach the database in its own right; the runtime reaches the writer's grants
-- by membership instead.
CREATE ROLE bolter_gateway_writer NOLOGIN;

-- The migrator owns the database, which is what lets the serving role own
-- nothing it could drop. LOGIN is required because nothing in the repository
-- issues SET ROLE or SET SESSION AUTHORIZATION: cmd/gateway-migrate has to
-- arrive as this role, not become it.
CREATE ROLE bolter_gateway_migrator LOGIN INHERIT;

-- The serving identity. INHERIT is not decoration: CollectPostgresRuntimeCatalog
-- reads pg_roles.rolinherit alongside pg_has_role(...,'MEMBER'), and a NOINHERIT
-- runtime would hold the writer membership without any of its privileges — the
-- membership check would pass while every effective-privilege comparison failed.
CREATE ROLE bolter_gateway LOGIN INHERIT;
GRANT bolter_gateway_writer TO bolter_gateway;

-- ---------------------------------------------------------------------------
-- Trading and auth-edge roles.
--
-- One role per service, owning its own database, rather than the gateway's
-- four-role split. That split earns its complexity in the gateway because the
-- gateway checks it on every start and stops when it has drifted. Neither of
-- these services has an authority matrix, so the same split here would be a
-- privilege topology nothing verifies — it would drift silently, which is worse
-- than not having it. Their identities are still pinned in Go: cmd/trading and
-- cmd/authedge each refuse to start unless database and role are exactly
-- `bolter_<service>`. Nothing else checks the name, so a typo would silently
-- point a service at whatever schema happened to answer — which was worth
-- refusing before there was one set of databases and is worth refusing now.
-- ---------------------------------------------------------------------------

CREATE ROLE bolter_trading LOGIN INHERIT;
CREATE ROLE bolter_authedge LOGIN INHERIT;

-- ---------------------------------------------------------------------------
-- Databases.
-- ---------------------------------------------------------------------------

CREATE DATABASE bolter_gateway OWNER bolter_gateway_migrator;
CREATE DATABASE bolter_trading OWNER bolter_trading;
CREATE DATABASE bolter_authedge OWNER bolter_authedge;

-- No privilege on the gateway database is granted here. Its entire ACL — schema
-- ownership, CONNECT, schema USAGE, and every object grant — is applied by
-- cmd/gateway-migrate through ApplyAuthority and then read back statement for
-- statement by VerifyRuntimeCatalog. Pre-seeding any of it would create a
-- second, unverified source of truth for the same facts, and the copy that only
-- runs on a fresh volume is exactly the one that would drift unnoticed.
--
-- Trading and auth-edge get no such pass, so the two revocations each needs are
-- made here. Revoking from PUBLIC does not disturb the owner: ownership is not
-- an ACL entry, so the role owning the database and the schema keeps full
-- rights over both and can still create its own tables.
\connect bolter_trading
ALTER SCHEMA public OWNER TO bolter_trading;
REVOKE CONNECT ON DATABASE bolter_trading FROM PUBLIC;
REVOKE ALL ON SCHEMA public FROM PUBLIC;

\connect bolter_authedge
ALTER SCHEMA public OWNER TO bolter_authedge;
REVOKE CONNECT ON DATABASE bolter_authedge FROM PUBLIC;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
