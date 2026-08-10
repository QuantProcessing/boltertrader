create table if not exists schema_migrations (
  version integer primary key,
  checksum text not null,
  applied_at timestamptz not null default now()
);

create table if not exists users (
  id text primary key,
  email text not null unique,
  password_hash text not null,
  environment text not null,
  last_login_at timestamptz,
  created_at timestamptz not null default now()
);

create table if not exists sessions (
  id text primary key,
  user_id text not null references users(id) on delete cascade,
  token_digest text not null unique,
  csrf_digest text not null,
  expires_at timestamptz not null,
  revoked_at timestamptz,
  created_at timestamptz not null default now(),
  last_authenticated_at timestamptz not null default now()
);

create table if not exists idempotency_records (
  id text primary key,
  key text not null unique,
  request_hash text not null,
  response_code integer,
  response_body jsonb,
  created_at timestamptz not null default now()
);

create table if not exists opportunities (
  id text primary key,
  symbol text not null,
  status text not null,
  payload jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now()
);

create table if not exists alerts (
  id text primary key,
  opportunity_id text references opportunities(id),
  status text not null,
  payload jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now()
);

create table if not exists executions (
  id text primary key,
  status text not null,
  payload jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now()
);

create table if not exists positions (
  id text primary key,
  symbol text not null,
  payload jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now()
);

create table if not exists activities (
  id text primary key,
  type text not null,
  payload jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now()
);

create table if not exists audit_events (
  id bigserial primary key,
  request_id text not null,
  action text not null,
  outcome text not null,
  email text,
  created_at timestamptz not null default now()
);
