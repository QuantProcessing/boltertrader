package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/cutover"
	"github.com/QuantProcessing/funding-trader/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrCutoverIdentity           = errors.New("cutover database identity rejected")
	ErrActiveApplicationSessions = errors.New("active application database sessions detected")
	ErrCutoverBusy               = errors.New("cutover database is busy")
	ErrAuthoritativeCollision    = errors.New("authoritative mode collision detected")
	ErrMissingProvenance         = errors.New("missing native venue provenance")
	ErrSchemaDrift               = errors.New("database schema drift detected")
)

func cutoverError(kind error, diagnostic string) error {
	return fmt.Errorf("%w: %s", kind, diagnostic)
}

type CutoverReport struct {
	SchemaVersion      int
	SessionsRevoked    int64
	DerivedRowsCleared int64
	AuthoritativeRows  map[string]int64
	Duration           time.Duration
}

type CutoverRunner struct {
	pool      *pgxpool.Pool
	proof     cutover.Proof
	afterCopy func(context.Context, pgx.Tx) error
}

func NewCutoverRunner(pool *pgxpool.Pool, proof cutover.Proof) *CutoverRunner {
	return &CutoverRunner{pool: pool, proof: proof}
}

func (runner *CutoverRunner) Run(ctx context.Context) (report CutoverReport, runErr error) {
	started := time.Now()
	if runner == nil || runner.pool == nil {
		return report, cutoverError(ErrCutoverIdentity, "cutover pool is required")
	}
	if err := runner.proof.Validate(); err != nil {
		return report, err
	}

	conn, err := runner.pool.Acquire(ctx)
	if err != nil {
		return report, cutoverError(ErrCutoverIdentity, "cutover connection unavailable")
	}
	defer conn.Release()
	if err := requireCutoverIdentity(ctx, conn); err != nil {
		return report, err
	}
	if err := requireNoApplicationSessions(ctx, conn); err != nil {
		return report, err
	}

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return report, cutoverError(ErrCutoverBusy, "cannot begin cutover transaction")
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(88442201)"); err != nil {
		return report, cutoverError(ErrCutoverBusy, "migration advisory lock unavailable")
	}
	if _, err := tx.Exec(ctx, `
		create table if not exists schema_migrations (
		  version integer primary key,
		  checksum text not null,
		  applied_at timestamptz not null default now()
		)
	`); err != nil {
		return report, cutoverError(ErrSchemaDrift, "schema migration ledger unavailable")
	}

	alreadyApplied, err := migrationApplied(ctx, tx, migrations.ProductMode())
	if err != nil {
		return report, err
	}
	if alreadyApplied {
		if err := validateMigrationHistory(ctx, tx, productModeMigrationSet()); err != nil {
			return report, err
		}
		if err := validateCutoverSchema(ctx, tx); err != nil {
			return report, err
		}
		var stateRows int
		if err := tx.QueryRow(ctx, `
			select count(*)
			from cutover_control
			where singleton
			  and contract_version=$1
			  and old_service_stopped
			  and auto_restart_disabled
		`, migrations.ProductMode().Version).Scan(&stateRows); err != nil || stateRows != 1 {
			return report, cutoverError(ErrSchemaDrift, "cutover control state is invalid")
		}
		report.SchemaVersion = migrations.ProductMode().Version
		report.Duration = time.Since(started)
		return report, nil
	}

	if err := applyMigrations(ctx, tx, migrations.Base()); err != nil {
		return report, cutoverError(ErrSchemaDrift, "legacy migration history is invalid")
	}
	if err := validateSchema(ctx, tx); err != nil {
		return report, cutoverError(ErrSchemaDrift, "legacy schema contract is invalid")
	}
	if err := lockCutoverTables(ctx, tx); err != nil {
		return report, err
	}
	if err := requireNoApplicationSessionsTx(ctx, tx); err != nil {
		return report, err
	}
	if err := preflightCutover(ctx, tx); err != nil {
		return report, err
	}

	before, err := captureAuthoritativeSnapshot(ctx, tx)
	if err != nil {
		return report, cutoverError(ErrSchemaDrift, "cannot inventory authoritative rows")
	}
	report.AuthoritativeRows = before.counts
	if err := tx.QueryRow(ctx, `
		select
		  (select count(*) from sessions where revoked_at is null),
		  (select count(*) from opportunities)
		    + (select count(*) from alerts)
		    + (select count(*) from execution_previews)
		    + (select count(*) from read_model_events)
	`).Scan(&report.SessionsRevoked, &report.DerivedRowsCleared); err != nil {
		return report, cutoverError(ErrSchemaDrift, "cannot inventory transient rows")
	}

	productMode := migrations.ProductMode()
	if _, err := tx.Exec(ctx, productMode.SQL); err != nil {
		return report, classifyMigrationError(err)
	}
	if _, err := tx.Exec(ctx,
		"insert into schema_migrations(version, checksum) values($1,$2)",
		productMode.Version,
		checksum(productMode.SQL),
	); err != nil {
		return report, cutoverError(ErrSchemaDrift, "cannot record product-mode migration")
	}

	if runner.afterCopy != nil {
		if err := runner.afterCopy(ctx, tx); err != nil {
			return report, cutoverError(ErrSchemaDrift, "post-copy validation failed")
		}
	}
	if err := validateCutoverSchema(ctx, tx); err != nil {
		return report, err
	}
	after, err := captureAuthoritativeSnapshot(ctx, tx)
	if err != nil {
		return report, cutoverError(ErrSchemaDrift, "cannot validate authoritative rows")
	}
	if err := compareAuthoritativeSnapshots(before, after); err != nil {
		return report, err
	}
	if _, err := tx.Exec(ctx, `
		insert into cutover_control(
		  singleton, contract_version, phase, backup_id, backup_sha256,
		  backup_verified_at, old_service_stopped, auto_restart_disabled,
		  validated_at
		) values(true,$1,'validated',$2,$3,$4,$5,$6,now())
	`, productMode.Version, runner.proof.BackupID, runner.proof.BackupSHA256,
		runner.proof.BackupVerifiedAt.UTC(), runner.proof.OldServiceStopped,
		runner.proof.AutoRestartDisabled); err != nil {
		return report, cutoverError(ErrSchemaDrift, "cannot persist cutover state")
	}
	if err := tx.Commit(ctx); err != nil {
		return report, cutoverError(ErrCutoverBusy, "cutover commit failed")
	}
	report.SchemaVersion = productMode.Version
	report.Duration = time.Since(started)
	return report, nil
}

func requireCutoverIdentity(ctx context.Context, conn *pgxpool.Conn) error {
	var identity string
	if err := conn.QueryRow(ctx, "select current_setting('application_name')").Scan(&identity); err != nil {
		return cutoverError(ErrCutoverIdentity, "cannot verify cutover identity")
	}
	if identity != CutoverDBIdentity {
		return cutoverError(ErrCutoverIdentity, "unexpected database application identity")
	}
	return nil
}

func requireNoApplicationSessions(ctx context.Context, conn *pgxpool.Conn) error {
	return requireNoDatabaseSessions(ctx, conn)
}

func requireNoApplicationSessionsTx(ctx context.Context, tx pgx.Tx) error {
	return requireNoDatabaseSessions(ctx, tx)
}

type sessionQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func requireNoDatabaseSessions(ctx context.Context, query sessionQueryer) error {
	var applicationSessions, otherSessions int
	if err := query.QueryRow(ctx, `
		select
		  count(*) filter (where application_name like $1),
		  count(*) filter (where application_name not like $1)
		from pg_stat_activity
		where datname=current_database()
		  and pid<>pg_backend_pid()
		  and backend_type='client backend'
		  and application_name<>'pg_isready'
	`, ApplicationDBIdentity+"%").Scan(&applicationSessions, &otherSessions); err != nil {
		return cutoverError(ErrActiveApplicationSessions, "cannot verify database session fence")
	}
	if applicationSessions != 0 {
		return cutoverError(
			ErrActiveApplicationSessions,
			fmt.Sprintf("%d application session(s) remain", applicationSessions),
		)
	}
	if otherSessions != 0 {
		return cutoverError(
			ErrCutoverBusy,
			fmt.Sprintf("%d unidentified database session(s) remain", otherSessions),
		)
	}
	return nil
}

func migrationApplied(ctx context.Context, tx pgx.Tx, migration migrations.Migration) (bool, error) {
	var existing string
	err := tx.QueryRow(
		ctx,
		"select checksum from schema_migrations where version=$1",
		migration.Version,
	).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, cutoverError(ErrSchemaDrift, "cannot read migration ledger")
	}
	if existing != checksum(migration.SQL) {
		return false, cutoverError(ErrSchemaDrift, "product-mode migration checksum drift")
	}
	return true, nil
}

func validateMigrationHistory(
	ctx context.Context,
	tx pgx.Tx,
	set []migrations.Migration,
) error {
	for _, migration := range set {
		applied, err := migrationApplied(ctx, tx, migration)
		if err != nil {
			return err
		}
		if !applied {
			return cutoverError(
				ErrSchemaDrift,
				fmt.Sprintf("migration version %d is missing", migration.Version),
			)
		}
	}
	return nil
}

func lockCutoverTables(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		lock table
		  schema_migrations,
		  users,
		  sessions,
		  idempotency_records,
		  execution_previews,
		  opportunities,
		  alerts,
		  executions,
		  execution_legs,
		  execution_fills,
		  execution_transitions,
		  positions,
		  activities,
		  audit_events,
		  funding_observations,
		  read_model_events
		in access exclusive mode nowait
	`)
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "55P03" {
		return cutoverError(ErrCutoverBusy, "an in-flight database operation holds a scoped table lock")
	}
	return cutoverError(ErrCutoverBusy, "cannot acquire exclusive cutover table locks")
}

func preflightCutover(ctx context.Context, tx pgx.Tx) error {
	var collisions int64
	if err := tx.QueryRow(ctx, `
		select count(*)
		from (
		  select
		    principal_id,
		    case lower(environment)
		      when 'demo' then 'test'
		      when 'testnet' then 'test'
		      when 'test' then 'test'
		      when 'mainnet' then 'live'
		      when 'live' then 'live'
		    end as mode,
		    method,
		    path,
		    key
		  from idempotency_records
		  group by principal_id, mode, method, path, key
		  having count(*) > 1
		) conflicts
	`).Scan(&collisions); err != nil {
		return cutoverError(ErrSchemaDrift, "authoritative collision inventory failed")
	}
	if collisions != 0 {
		return cutoverError(
			ErrAuthoritativeCollision,
			fmt.Sprintf("%d idempotency scope collision(s)", collisions),
		)
	}

	var invalidEnvironments int64
	if err := tx.QueryRow(ctx, `
		select count(*)
		from (
		  select environment from users
		  union all select environment from idempotency_records
		  union all select environment from executions
		  union all select environment from execution_transitions
		  union all select environment from positions
		  union all select environment from activities
		  union all select environment from audit_events
		  union all select environment from funding_observations
		) scoped
		where lower(environment) not in ('demo','testnet','test','mainnet','live')
	`).Scan(&invalidEnvironments); err != nil {
		return cutoverError(ErrSchemaDrift, "product environment inventory failed")
	}
	if invalidEnvironments != 0 {
		return cutoverError(
			ErrSchemaDrift,
			fmt.Sprintf("%d row(s) use an unsupported product environment", invalidEnvironments),
		)
	}

	var missingExecution, missingFunding int64
	if err := tx.QueryRow(ctx, `
		select count(*)
		from execution_legs leg
		join executions execution on execution.id = leg.execution_id
		where (
		  select count(*)
		  from (
		    select distinct
		      item->>'accountId',
		      item->>'venue',
		      item->>'instrumentId',
		      lower(item->>'venueEnvironment')
		    from jsonb_array_elements(
		      case
		        when jsonb_typeof(execution.payload->'legs') = 'array'
		          then execution.payload->'legs'
		        else '[]'::jsonb
		      end
		      ||
		      case
		        when jsonb_typeof(execution.payload->'plannedLegs') = 'array'
		          then execution.payload->'plannedLegs'
		        else '[]'::jsonb
		      end
		    ) item
		    where item->>'role' = leg.role
		      and item->>'accountId' = leg.account_id
		      and item->>'instrumentId' = leg.instrument_id
		      and nullif(item->>'venue', '') is not null
		      and lower(item->>'venueEnvironment') in ('demo','testnet','live')
		      and (
		        lower(split_part(item->>'instrumentId', ':', 2))
		          not in ('demo','testnet','mainnet','live')
		        or (
		          lower(split_part(item->>'instrumentId', ':', 1))
		            = lower(item->>'venue')
		          and case lower(split_part(item->>'instrumentId', ':', 2))
		            when 'demo' then 'demo'
		            when 'testnet' then 'testnet'
		            when 'mainnet' then 'live'
		            when 'live' then 'live'
		          end = lower(item->>'venueEnvironment')
		        )
		      )
		      and (
		        (
		          lower(execution.environment) in ('demo','testnet','test')
		          and lower(item->>'venueEnvironment') in ('demo','testnet')
		        )
		        or (
		          lower(execution.environment) in ('mainnet','live')
		          and lower(item->>'venueEnvironment') = 'live'
		        )
		      )
		  ) candidates
		) <> 1
	`).Scan(&missingExecution); err != nil {
		return cutoverError(ErrSchemaDrift, "execution provenance inventory failed")
	}
	if err := tx.QueryRow(ctx, `
		select count(*)
		from funding_observations
		where (
		  case
		    when lower(environment) = 'demo' then 'demo'
		    when lower(environment) = 'testnet' then 'testnet'
		    when lower(environment) in ('mainnet','live') then 'live'
		    when lower(environment) = 'test'
		      and lower(split_part(instrument_id, ':', 2)) in ('demo','testnet')
		      then lower(split_part(instrument_id, ':', 2))
		    else null
		  end is null
		  or (
		    lower(split_part(instrument_id, ':', 2))
		      in ('demo','testnet','mainnet','live')
		    and case lower(split_part(instrument_id, ':', 2))
		      when 'demo' then 'demo'
		      when 'testnet' then 'testnet'
		      when 'mainnet' then 'live'
		      when 'live' then 'live'
		    end <> case
		      when lower(environment) = 'demo' then 'demo'
		      when lower(environment) = 'testnet' then 'testnet'
		      when lower(environment) in ('mainnet','live') then 'live'
		      when lower(environment) = 'test'
		        and lower(split_part(instrument_id, ':', 2)) in ('demo','testnet')
		        then lower(split_part(instrument_id, ':', 2))
		    end
		  )
		)
	`).Scan(&missingFunding); err != nil {
		return cutoverError(ErrSchemaDrift, "funding provenance inventory failed")
	}
	if missingExecution+missingFunding != 0 {
		return cutoverError(
			ErrMissingProvenance,
			fmt.Sprintf(
				"%d execution leg(s) and %d funding row(s) lack exact native provenance",
				missingExecution,
				missingFunding,
			),
		)
	}
	return nil
}

func classifyMigrationError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch {
		case postgresError.Code == "55P03":
			return cutoverError(ErrCutoverBusy, "cutover lock was lost")
		case strings.Contains(postgresError.Message, "authoritative_collision"):
			return cutoverError(ErrAuthoritativeCollision, "authoritative scope collision")
		case strings.Contains(postgresError.Message, "missing_native_provenance"):
			return cutoverError(ErrMissingProvenance, "native provenance validation failed")
		}
	}
	return cutoverError(ErrSchemaDrift, "product-mode migration failed")
}

type authoritativeSnapshot struct {
	counts  map[string]int64
	digests map[string]string
}

type snapshotSpec struct {
	table       string
	ignoredKeys string
}

var authoritativeSnapshotSpecs = []snapshotSpec{
	{table: "idempotency_records", ignoredKeys: "'{environment,mode,response_body,updated_at}'"},
	{table: "executions", ignoredKeys: "'{environment,mode,payload,updated_at}'"},
	{table: "execution_legs", ignoredKeys: "'{venue,venue_environment}'"},
	{table: "execution_fills", ignoredKeys: "'{payload}'"},
	{table: "execution_transitions", ignoredKeys: "'{environment,mode,payload}'"},
	{table: "positions", ignoredKeys: "'{environment,mode,payload,updated_at}'"},
	{table: "activities", ignoredKeys: "'{environment,mode,payload}'"},
	{table: "audit_events", ignoredKeys: "'{environment,mode,payload}'"},
	{table: "funding_observations", ignoredKeys: "'{environment,mode,venue_environment}'"},
}

func captureAuthoritativeSnapshot(ctx context.Context, tx pgx.Tx) (authoritativeSnapshot, error) {
	snapshot := authoritativeSnapshot{
		counts:  make(map[string]int64, len(authoritativeSnapshotSpecs)),
		digests: make(map[string]string, len(authoritativeSnapshotSpecs)),
	}
	for _, spec := range authoritativeSnapshotSpecs {
		query := fmt.Sprintf(`
			with normalized as (
			  select (to_jsonb(row_value) - %s::text[])::text as body
			  from %s row_value
			)
			select
			  count(*),
			  coalesce(md5(string_agg(body, E'\n' order by body)), md5(''))
			from normalized
		`, spec.ignoredKeys, spec.table)
		var count int64
		var digest string
		if err := tx.QueryRow(ctx, query).Scan(&count, &digest); err != nil {
			return authoritativeSnapshot{}, err
		}
		snapshot.counts[spec.table] = count
		snapshot.digests[spec.table] = digest
	}
	return snapshot, nil
}

func compareAuthoritativeSnapshots(before, after authoritativeSnapshot) error {
	for _, spec := range authoritativeSnapshotSpecs {
		if before.counts[spec.table] != after.counts[spec.table] ||
			before.digests[spec.table] != after.digests[spec.table] {
			return cutoverError(
				ErrSchemaDrift,
				"authoritative row invariant changed for "+spec.table,
			)
		}
	}
	return nil
}

func validateCutoverSchema(ctx context.Context, tx pgx.Tx) error {
	modeTables := []string{
		"sessions",
		"idempotency_records",
		"execution_previews",
		"opportunities",
		"alerts",
		"executions",
		"execution_transitions",
		"positions",
		"activities",
		"audit_events",
		"funding_observations",
		"read_model_events",
	}
	for _, table := range modeTables {
		var modeType, nullable string
		if err := tx.QueryRow(ctx, `
			select data_type, is_nullable
			from information_schema.columns
			where table_schema='public' and table_name=$1 and column_name='mode'
		`, table).Scan(&modeType, &nullable); err != nil {
			return cutoverError(ErrSchemaDrift, "missing mode column on "+table)
		}
		if modeType != "text" || nullable != "NO" {
			return cutoverError(ErrSchemaDrift, "invalid mode column on "+table)
		}
		var legacyColumn bool
		if err := tx.QueryRow(ctx, `
			select exists(
			  select 1
			  from information_schema.columns
			  where table_schema='public' and table_name=$1 and column_name='environment'
			)
		`, table).Scan(&legacyColumn); err != nil {
			return cutoverError(ErrSchemaDrift, "cannot inspect legacy column on "+table)
		}
		if legacyColumn {
			return cutoverError(ErrSchemaDrift, "legacy product environment remains on "+table)
		}
	}

	for _, required := range []struct {
		table  string
		column string
	}{
		{table: "execution_legs", column: "venue"},
		{table: "execution_legs", column: "venue_environment"},
		{table: "funding_observations", column: "venue_environment"},
		{table: "read_model_events", column: "contract_version"},
	} {
		var nullable string
		if err := tx.QueryRow(ctx, `
			select is_nullable
			from information_schema.columns
			where table_schema='public' and table_name=$1 and column_name=$2
		`, required.table, required.column).Scan(&nullable); err != nil {
			return cutoverError(
				ErrSchemaDrift,
				"missing provenance column on "+required.table,
			)
		}
		if nullable != "NO" {
			return cutoverError(
				ErrSchemaDrift,
				"nullable provenance column on "+required.table,
			)
		}
	}
	var userEnvironment, cutoverTable bool
	if err := tx.QueryRow(ctx, `
		select
		  exists(
		    select 1 from information_schema.columns
		    where table_schema='public' and table_name='users' and column_name='environment'
		  ),
		  to_regclass('public.cutover_control') is not null
	`).Scan(&userEnvironment, &cutoverTable); err != nil {
		return cutoverError(ErrSchemaDrift, "cannot inspect cutover schema")
	}
	if userEnvironment || !cutoverTable {
		return cutoverError(ErrSchemaDrift, "principal or cutover-control schema is invalid")
	}
	var invalidRows int64
	if err := tx.QueryRow(ctx, `
		select
		  (select count(*) from execution_legs leg
		   join executions execution on execution.id=leg.execution_id
		   where not (
		     (execution.mode='test' and leg.venue_environment in ('demo','testnet'))
		     or (execution.mode='live' and leg.venue_environment='live')
		   ))
		  +
		  (select count(*) from funding_observations
		   where not (
		     (mode='test' and venue_environment in ('demo','testnet'))
		     or (mode='live' and venue_environment='live')
		   ))
	`).Scan(&invalidRows); err != nil {
		return cutoverError(ErrSchemaDrift, "cannot validate mode/provenance isolation")
	}
	if invalidRows != 0 {
		return cutoverError(
			ErrMissingProvenance,
			fmt.Sprintf("%d row(s) violate mode/native isolation", invalidRows),
		)
	}
	return validateSerializedProvenance(ctx, tx)
}

func validateSerializedProvenance(ctx context.Context, tx pgx.Tx) error {
	var invalidArrays, invalidFills, invalidPositions int64
	if err := tx.QueryRow(ctx, `
			with canonical_routes as (
			  select distinct
			    execution.id as execution_id,
			    item->>'role' as role,
			    item->>'accountId' as account_id,
			    item->>'venue' as venue,
			    item->>'instrumentId' as instrument_id,
			    lower(item->>'venueEnvironment') as venue_environment
			  from executions execution
			  cross join lateral jsonb_array_elements(
			    case
			      when jsonb_typeof(execution.payload->'plannedLegs')='array'
			        then execution.payload->'plannedLegs'
			      else '[]'::jsonb
			    end
			    ||
			    case
			      when jsonb_typeof(execution.payload->'legs')='array'
			        then execution.payload->'legs'
			      else '[]'::jsonb
			    end
			  ) item
			),
			planned_routes as (
			  select distinct
			    execution.id as execution_id,
			    item->>'role' as role,
			    item->>'accountId' as account_id,
			    item->>'venue' as venue,
			    item->>'instrumentId' as instrument_id,
			    lower(item->>'venueEnvironment') as venue_environment
			  from executions execution
			  cross join lateral jsonb_array_elements(
			    case
			      when jsonb_typeof(execution.payload->'plannedLegs')='array'
			        then execution.payload->'plannedLegs'
			      else '[]'::jsonb
			    end
			  ) item
			),
			route_arrays as (
		  select
		    execution_id,
		    payload->'legs' as legs
		  from execution_transitions
		  union all
		  select
		    execution_id,
		    response_body->'legs' as legs
		  from idempotency_records
		  where state='completed'
		    and execution_id is not null
		  union all
		  select
		    execution_id,
		    payload->'legs' as legs
		  from audit_events
		  where action like 'execution.%'
		),
		invalid_route_arrays as (
		  select count(*) as count
		  from route_arrays evidence
		  where jsonb_typeof(evidence.legs) is distinct from 'array'
		     or jsonb_array_length(
		       case
		         when jsonb_typeof(evidence.legs)='array'
		           then evidence.legs
		         else '[]'::jsonb
		       end
		     ) = 0
			  or exists (
			    select 1
			    from jsonb_array_elements(
			      case
			        when jsonb_typeof(evidence.legs)='array'
			          then evidence.legs
			        else '[]'::jsonb
			      end
			    ) item
			    where (
			      select count(*)
			      from canonical_routes route
			      where route.execution_id=evidence.execution_id
			        and item->>'role'=route.role
			        and item->>'accountId'=route.account_id
			        and item->>'venue'=route.venue
			        and item->>'instrumentId'=route.instrument_id
			        and lower(item->>'venueEnvironment')=route.venue_environment
			    ) <> 1
			  )
			  or exists (
			    select 1
			    from jsonb_array_elements(
			      case
			        when jsonb_typeof(evidence.legs)='array'
			          then evidence.legs
			        else '[]'::jsonb
			      end
			    ) item
			    group by
			      item->>'role',
			      item->>'accountId',
			      item->>'venue',
			      item->>'instrumentId',
			      lower(item->>'venueEnvironment')
			    having count(*) > 1
			  )
		),
		invalid_fill_routes as (
		  select count(*) as count
		  from execution_fills fill
		  join execution_legs leg on leg.leg_id=fill.leg_id
		  where fill.payload->'route'->>'role' is distinct from leg.role
		     or fill.payload->'route'->>'accountId' is distinct from leg.account_id
		     or fill.payload->'route'->>'venue' is distinct from leg.venue
		     or fill.payload->'route'->>'instrumentId' is distinct from leg.instrument_id
		     or lower(fill.payload->'route'->>'venueEnvironment')
		          is distinct from leg.venue_environment
		),
		invalid_position_routes as (
		  select count(*) as count
		  from positions position
		  where nullif(position.payload->>'openingExecutionId', '') is null
		     or (
		       select count(*)
		       from planned_routes route
		       where route.execution_id=position.payload->>'openingExecutionId'
		         and route.role in ('long','short')
		         and (
		           case route.role
		             when 'long' then position.payload->'longLeg'
		             when 'short' then position.payload->'shortLeg'
		           end
		         )->>'accountId'=route.account_id
		         and (
		           case route.role
		             when 'long' then position.payload->'longLeg'
		             when 'short' then position.payload->'shortLeg'
		           end
		         )->>'venue'=route.venue
		         and (
		           case route.role
		             when 'long' then position.payload->'longLeg'
		             when 'short' then position.payload->'shortLeg'
		           end
		         )->>'instrumentId'=route.instrument_id
		         and lower((
		           case route.role
		             when 'long' then position.payload->'longLeg'
		             when 'short' then position.payload->'shortLeg'
		           end
		         )->>'venueEnvironment')=route.venue_environment
		     ) <> 2
		)
		select
		  (select count from invalid_route_arrays),
		  (select count from invalid_fill_routes),
		  (select count from invalid_position_routes)
	`).Scan(&invalidArrays, &invalidFills, &invalidPositions); err != nil {
		return cutoverError(
			ErrSchemaDrift,
			"cannot validate serialized native provenance",
		)
	}
	if invalidArrays+invalidFills+invalidPositions != 0 {
		return cutoverError(
			ErrMissingProvenance,
			fmt.Sprintf(
				"%d route payload(s), %d fill payload(s), and %d position payload(s) lack exact native provenance",
				invalidArrays,
				invalidFills,
				invalidPositions,
			),
		)
	}
	return nil
}
