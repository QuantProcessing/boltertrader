package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantProcessing/funding-trader/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MigrationRunner struct {
	pool *pgxpool.Pool
}

var ErrProductModeMigrationRequired = errors.New(
	"product-mode cutover is required before serve",
)

func NewMigrationRunner(pool *pgxpool.Pool) *MigrationRunner {
	return &MigrationRunner{pool: pool}
}

func EmbeddedMigrationSQL() string {
	return migrations.InitSQL
}

func (r *MigrationRunner) Up(ctx context.Context) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(88442201)"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `create table if not exists schema_migrations (version integer primary key, checksum text not null, applied_at timestamptz not null default now())`); err != nil {
		return err
	}
	if err := applyMigrations(ctx, tx, migrations.Base()); err != nil {
		return err
	}
	productModeApplied, err := migrationApplied(ctx, tx, migrations.ProductMode())
	if err != nil {
		return err
	}
	if productModeApplied {
		if err := validateMigrationHistory(ctx, tx, productModeMigrationSet()); err != nil {
			return err
		}
		if err := validateCutoverSchema(ctx, tx); err != nil {
			return err
		}
		var validCutoverState int
		if err := tx.QueryRow(ctx, `
			select count(*)
			from cutover_control
			where singleton
			  and contract_version=$1
			  and phase in (
			    'validated',
			    'maintenance_smoke',
			    'reopened',
			    'restore_authorized',
			    'write_accepted'
			  )
			  and old_service_stopped
			  and auto_restart_disabled
		`, migrations.ProductMode().Version).Scan(&validCutoverState); err != nil {
			return cutoverError(
				ErrSchemaDrift,
				"cannot validate post-cutover serve boundary",
			)
		}
		if validCutoverState != 1 {
			return cutoverError(
				ErrSchemaDrift,
				"post-cutover control state is invalid",
			)
		}
		if err := applyMigrations(ctx, tx, migrations.PostCutoverMigrations()); err != nil {
			return cutoverError(ErrSchemaDrift, "ledger facts migration failed")
		}
		if err := validateMigrationHistory(ctx, tx, migrations.All()); err != nil {
			return err
		}
		if err := validateLedgerSchema(ctx, tx); err != nil {
			return cutoverError(ErrSchemaDrift, err.Error())
		}
		return tx.Commit(ctx)
	}
	if err := validateSchema(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return fmt.Errorf(
		"%w; run FT_STARTUP_MODE=migrate with verified backup proof while maintenance remains active",
		ErrProductModeMigrationRequired,
	)
}

func productModeMigrationSet() []migrations.Migration {
	return append(migrations.Base(), migrations.ProductMode())
}

func applyMigrations(ctx context.Context, tx pgx.Tx, set []migrations.Migration) error {
	for _, migration := range set {
		expectedChecksum := checksum(migration.SQL)
		var existing string
		err := tx.QueryRow(ctx, "select checksum from schema_migrations where version=$1", migration.Version).Scan(&existing)
		if err == nil {
			if existing != expectedChecksum {
				return fmt.Errorf("migration checksum drift for version %d", migration.Version)
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx, migration.SQL); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into schema_migrations(version, checksum) values($1, $2)", migration.Version, expectedChecksum); err != nil {
			return err
		}
	}
	return nil
}

func checksum(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func validateSchema(ctx context.Context, q queryer) error {
	required := map[string]map[string]struct {
		dataType   string
		isNullable string
		hasDefault bool
	}{
		"schema_migrations": {
			"version":    {dataType: "integer", isNullable: "NO"},
			"checksum":   {dataType: "text", isNullable: "NO"},
			"applied_at": {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"users": {
			"id":            {dataType: "text", isNullable: "NO"},
			"email":         {dataType: "text", isNullable: "NO"},
			"password_hash": {dataType: "text", isNullable: "NO"},
			"environment":   {dataType: "text", isNullable: "NO"},
			"last_login_at": {dataType: "timestamp with time zone", isNullable: "YES"},
			"created_at":    {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"sessions": {
			"id":                    {dataType: "text", isNullable: "NO"},
			"user_id":               {dataType: "text", isNullable: "NO"},
			"token_digest":          {dataType: "text", isNullable: "NO"},
			"csrf_digest":           {dataType: "text", isNullable: "NO"},
			"expires_at":            {dataType: "timestamp with time zone", isNullable: "NO"},
			"revoked_at":            {dataType: "timestamp with time zone", isNullable: "YES"},
			"created_at":            {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
			"last_authenticated_at": {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"idempotency_records": {
			"id":            {dataType: "text", isNullable: "NO"},
			"key":           {dataType: "text", isNullable: "NO"},
			"request_hash":  {dataType: "text", isNullable: "NO"},
			"response_code": {dataType: "integer", isNullable: "YES"},
			"response_body": {dataType: "jsonb", isNullable: "YES"},
			"principal_id":  {dataType: "text", isNullable: "NO", hasDefault: true},
			"environment":   {dataType: "text", isNullable: "NO", hasDefault: true},
			"method":        {dataType: "text", isNullable: "NO", hasDefault: true},
			"path":          {dataType: "text", isNullable: "NO", hasDefault: true},
			"execution_id":  {dataType: "text", isNullable: "YES"},
			"state":         {dataType: "text", isNullable: "NO", hasDefault: true},
			"updated_at":    {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
			"created_at":    {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"execution_previews": {
			"id":             {dataType: "text", isNullable: "NO"},
			"snapshot_id":    {dataType: "text", isNullable: "NO"},
			"principal_id":   {dataType: "text", isNullable: "NO"},
			"environment":    {dataType: "text", isNullable: "NO"},
			"opportunity_id": {dataType: "text", isNullable: "YES"},
			"operation":      {dataType: "text", isNullable: "NO"},
			"payload":        {dataType: "jsonb", isNullable: "NO"},
			"request_hash":   {dataType: "text", isNullable: "NO"},
			"can_submit":     {dataType: "boolean", isNullable: "NO"},
			"expires_at":     {dataType: "timestamp with time zone", isNullable: "NO"},
			"created_at":     {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"opportunities": {
			"id":               {dataType: "text", isNullable: "NO"},
			"symbol":           {dataType: "text", isNullable: "NO"},
			"status":           {dataType: "text", isNullable: "NO"},
			"payload":          {dataType: "jsonb", isNullable: "NO", hasDefault: true},
			"environment":      {dataType: "text", isNullable: "NO", hasDefault: true},
			"long_account_id":  {dataType: "text", isNullable: "YES"},
			"short_account_id": {dataType: "text", isNullable: "YES"},
			"net_apr":          {dataType: "numeric", isNullable: "YES"},
			"formula_version":  {dataType: "text", isNullable: "YES"},
			"observed_at":      {dataType: "timestamp with time zone", isNullable: "YES"},
			"updated_at":       {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
			"created_at":       {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"alerts": {
			"id":             {dataType: "text", isNullable: "NO"},
			"opportunity_id": {dataType: "text", isNullable: "YES"},
			"status":         {dataType: "text", isNullable: "NO"},
			"payload":        {dataType: "jsonb", isNullable: "NO", hasDefault: true},
			"environment":    {dataType: "text", isNullable: "NO", hasDefault: true},
			"account_id":     {dataType: "text", isNullable: "YES"},
			"instrument_id":  {dataType: "text", isNullable: "YES"},
			"observed_at":    {dataType: "timestamp with time zone", isNullable: "YES"},
			"updated_at":     {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
			"created_at":     {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"executions": {
			"id":                   {dataType: "text", isNullable: "NO"},
			"status":               {dataType: "text", isNullable: "NO"},
			"payload":              {dataType: "jsonb", isNullable: "NO", hasDefault: true},
			"principal_id":         {dataType: "text", isNullable: "NO", hasDefault: true},
			"environment":          {dataType: "text", isNullable: "NO", hasDefault: true},
			"operation_kind":       {dataType: "text", isNullable: "NO", hasDefault: true},
			"opportunity_id":       {dataType: "text", isNullable: "YES"},
			"position_id":          {dataType: "text", isNullable: "YES"},
			"parent_execution_id":  {dataType: "text", isNullable: "YES"},
			"lineage_id":           {dataType: "text", isNullable: "YES"},
			"idempotency_key":      {dataType: "text", isNullable: "YES"},
			"request_hash":         {dataType: "text", isNullable: "YES"},
			"reconciliation_state": {dataType: "text", isNullable: "NO", hasDefault: true},
			"manual_intervention":  {dataType: "boolean", isNullable: "NO", hasDefault: true},
			"version":              {dataType: "bigint", isNullable: "NO", hasDefault: true},
			"updated_at":           {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
			"completed_at":         {dataType: "timestamp with time zone", isNullable: "YES"},
			"created_at":           {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"execution_legs": {
			"leg_id":             {dataType: "text", isNullable: "NO"},
			"execution_id":       {dataType: "text", isNullable: "NO"},
			"role":               {dataType: "text", isNullable: "NO"},
			"account_id":         {dataType: "text", isNullable: "YES"},
			"instrument_id":      {dataType: "text", isNullable: "YES"},
			"side":               {dataType: "text", isNullable: "NO"},
			"requested_quantity": {dataType: "numeric", isNullable: "NO"},
			"filled_quantity":    {dataType: "numeric", isNullable: "NO"},
			"status":             {dataType: "text", isNullable: "NO"},
			"gateway_id":         {dataType: "text", isNullable: "YES"},
			"command_id":         {dataType: "text", isNullable: "YES"},
			"gateway_order_id":   {dataType: "text", isNullable: "YES"},
			"reduce_only":        {dataType: "boolean", isNullable: "NO", hasDefault: true},
			"safe_error":         {dataType: "jsonb", isNullable: "NO", hasDefault: true},
			"updated_at":         {dataType: "timestamp with time zone", isNullable: "NO"},
		},
		"execution_fills": {
			"fill_id":         {dataType: "text", isNullable: "NO"},
			"execution_id":    {dataType: "text", isNullable: "NO"},
			"leg_id":          {dataType: "text", isNullable: "NO"},
			"gateway_fill_id": {dataType: "text", isNullable: "YES"},
			"quantity":        {dataType: "numeric", isNullable: "NO"},
			"price":           {dataType: "numeric", isNullable: "NO"},
			"fee":             {dataType: "numeric", isNullable: "NO"},
			"fee_currency":    {dataType: "text", isNullable: "YES"},
			"occurred_at":     {dataType: "timestamp with time zone", isNullable: "NO"},
			"payload":         {dataType: "jsonb", isNullable: "NO", hasDefault: true},
		},
		"execution_transitions": {
			"id":           {dataType: "bigint", isNullable: "NO", hasDefault: true},
			"execution_id": {dataType: "text", isNullable: "NO"},
			"principal_id": {dataType: "text", isNullable: "NO"},
			"environment":  {dataType: "text", isNullable: "NO"},
			"from_status":  {dataType: "text", isNullable: "YES"},
			"to_status":    {dataType: "text", isNullable: "NO"},
			"kind":         {dataType: "text", isNullable: "NO"},
			"request_id":   {dataType: "text", isNullable: "NO"},
			"payload":      {dataType: "jsonb", isNullable: "NO", hasDefault: true},
			"occurred_at":  {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"positions": {
			"id":           {dataType: "text", isNullable: "NO"},
			"symbol":       {dataType: "text", isNullable: "NO"},
			"payload":      {dataType: "jsonb", isNullable: "NO", hasDefault: true},
			"principal_id": {dataType: "text", isNullable: "NO", hasDefault: true},
			"environment":  {dataType: "text", isNullable: "NO", hasDefault: true},
			"status":       {dataType: "text", isNullable: "NO", hasDefault: true},
			"updated_at":   {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
			"created_at":   {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"activities": {
			"id":           {dataType: "text", isNullable: "NO"},
			"type":         {dataType: "text", isNullable: "NO"},
			"payload":      {dataType: "jsonb", isNullable: "NO", hasDefault: true},
			"principal_id": {dataType: "text", isNullable: "NO", hasDefault: true},
			"environment":  {dataType: "text", isNullable: "NO", hasDefault: true},
			"position_id":  {dataType: "text", isNullable: "YES"},
			"execution_id": {dataType: "text", isNullable: "YES"},
			"occurred_at":  {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
			"created_at":   {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"audit_events": {
			"id":           {dataType: "bigint", isNullable: "NO", hasDefault: true},
			"request_id":   {dataType: "text", isNullable: "NO"},
			"action":       {dataType: "text", isNullable: "NO"},
			"outcome":      {dataType: "text", isNullable: "NO"},
			"email":        {dataType: "text", isNullable: "YES"},
			"principal_id": {dataType: "text", isNullable: "YES"},
			"environment":  {dataType: "text", isNullable: "NO", hasDefault: true},
			"target_id":    {dataType: "text", isNullable: "YES"},
			"execution_id": {dataType: "text", isNullable: "YES"},
			"payload":      {dataType: "jsonb", isNullable: "NO", hasDefault: true},
			"created_at":   {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"funding_observations": {
			"environment":       {dataType: "text", isNullable: "NO"},
			"account_id":        {dataType: "text", isNullable: "NO"},
			"instrument_id":     {dataType: "text", isNullable: "NO"},
			"funding_time":      {dataType: "timestamp with time zone", isNullable: "NO"},
			"observed_at":       {dataType: "timestamp with time zone", isNullable: "NO"},
			"next_funding_time": {dataType: "timestamp with time zone", isNullable: "YES"},
			"rate":              {dataType: "numeric", isNullable: "NO"},
			"mark_price":        {dataType: "numeric", isNullable: "YES"},
			"created_at":        {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"read_model_events": {
			"id":           {dataType: "bigint", isNullable: "NO", hasDefault: true},
			"boot_id":      {dataType: "text", isNullable: "NO"},
			"principal_id": {dataType: "text", isNullable: "YES"},
			"environment":  {dataType: "text", isNullable: "NO"},
			"kind":         {dataType: "text", isNullable: "NO"},
			"scope":        {dataType: "ARRAY", isNullable: "NO"},
			"entity_id":    {dataType: "text", isNullable: "YES"},
			"payload":      {dataType: "jsonb", isNullable: "NO"},
			"occurred_at":  {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
	}
	for table, columns := range required {
		for column, expected := range columns {
			var dataType, nullable string
			var defaultValue *string
			if err := q.QueryRow(ctx, `select data_type, is_nullable, column_default from information_schema.columns where table_schema='public' and table_name=$1 and column_name=$2`, table, column).Scan(&dataType, &nullable, &defaultValue); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("schema drift: missing column %s.%s", table, column)
				}
				return err
			}
			if dataType != expected.dataType || nullable != expected.isNullable {
				return fmt.Errorf("schema drift: invalid column %s.%s", table, column)
			}
			if expected.hasDefault && defaultValue == nil {
				return fmt.Errorf("schema drift: missing default %s.%s", table, column)
			}
		}
	}
	for _, unique := range []struct {
		table   string
		columns []string
	}{
		{"users", []string{"email"}},
		{"sessions", []string{"token_digest"}},
		{"idempotency_records", []string{"principal_id", "environment", "method", "path", "key"}},
		{"execution_previews", []string{"principal_id", "environment", "id"}},
		{"execution_legs", []string{"execution_id", "role"}},
	} {
		var exists bool
		if err := q.QueryRow(ctx, `select exists(
			select 1
			from pg_index i
			join pg_class t on t.oid = i.indrelid
			where t.relname = $1
			  and i.indisunique
			  and (select array_agg(a.attname::text order by k.ord)
			       from unnest(i.indkey) with ordinality k(attnum, ord)
			       join pg_attribute a on a.attrelid = t.oid and a.attnum = k.attnum) = $2::text[]
		)`, unique.table, "{"+strings.Join(unique.columns, ",")+"}").Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("schema drift: missing unique index %s(%s)", unique.table, strings.Join(unique.columns, ","))
		}
	}
	var fkOK bool
	if err := q.QueryRow(ctx, `select exists(
		select 1
		from pg_constraint c
		join pg_class child on child.oid = c.conrelid
		join pg_class parent on parent.oid = c.confrelid
		where c.contype = 'f'
		  and child.relname = 'sessions'
		  and parent.relname = 'users'
		  and c.confdeltype = 'c'
	)`).Scan(&fkOK); err != nil {
		return err
	}
	if !fkOK {
		return fmt.Errorf("schema drift: missing sessions.user_id cascade foreign key")
	}
	return nil
}

func validateLedgerSchema(ctx context.Context, q queryer) error {
	required := ledgerColumnSpecs()
	for table, columns := range required {
		for column, expected := range columns {
			var dataType, nullable string
			var defaultValue *string
			if err := q.QueryRow(ctx, `select data_type, is_nullable, column_default from information_schema.columns where table_schema='public' and table_name=$1 and column_name=$2`, table, column).Scan(&dataType, &nullable, &defaultValue); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("schema drift: missing column %s.%s", table, column)
				}
				return err
			}
			if dataType != expected.dataType || nullable != expected.isNullable {
				return fmt.Errorf("schema drift: invalid column %s.%s", table, column)
			}
			if expected.hasDefault && defaultValue == nil {
				return fmt.Errorf("schema drift: missing default %s.%s", table, column)
			}
		}
	}
	for _, validator := range []func(context.Context, queryer) error{
		validateLedgerIndexes,
		validateLedgerKeyConstraints,
		validateLedgerForeignKeys,
		validateLedgerChecks,
		validateLedgerTriggers,
		validateLedgerSequence,
	} {
		if err := validator(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

type ledgerColumnSpec struct {
	dataType   string
	isNullable string
	hasDefault bool
}

func ledgerColumnSpecs() map[string]map[string]ledgerColumnSpec {
	return map[string]map[string]ledgerColumnSpec{
		"ledger_funding_payment_facts": {
			"fact_id": {dataType: "text", isNullable: "NO"}, "supersedes_fact_id": {dataType: "text", isNullable: "YES"}, "sync_watermark_id": {dataType: "text", isNullable: "YES"},
			"payment_id": {dataType: "text", isNullable: "NO", hasDefault: true}, "position_id": {dataType: "text", isNullable: "NO", hasDefault: true}, "venue": {dataType: "text", isNullable: "NO", hasDefault: true}, "venue_environment": {dataType: "text", isNullable: "NO", hasDefault: true}, "product": {dataType: "text", isNullable: "NO", hasDefault: true}, "formula_version": {dataType: "text", isNullable: "NO", hasDefault: true},
			"principal_id": {dataType: "text", isNullable: "NO"}, "mode": {dataType: "text", isNullable: "NO"}, "account_id": {dataType: "text", isNullable: "NO"}, "instrument_id": {dataType: "text", isNullable: "NO"}, "window_start": {dataType: "timestamp with time zone", isNullable: "NO"}, "window_end": {dataType: "timestamp with time zone", isNullable: "NO"}, "pagination_traversal": {dataType: "text", isNullable: "NO", hasDefault: true},
			"source_ref": {dataType: "text", isNullable: "NO"}, "source_revision": {dataType: "text", isNullable: "NO"}, "source_version": {dataType: "text", isNullable: "NO"}, "observed_at": {dataType: "timestamp with time zone", isNullable: "NO"}, "effective_at": {dataType: "timestamp with time zone", isNullable: "NO"}, "amount": {dataType: "numeric", isNullable: "YES"}, "asset": {dataType: "text", isNullable: "YES"}, "payload_hash": {dataType: "text", isNullable: "NO"}, "sequence_id": {dataType: "bigint", isNullable: "NO", hasDefault: true}, "created_at": {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"ledger_fill_cashflow_facts": {
			"fact_id": {dataType: "text", isNullable: "NO"}, "supersedes_fact_id": {dataType: "text", isNullable: "YES"}, "sync_watermark_id": {dataType: "text", isNullable: "YES"}, "principal_id": {dataType: "text", isNullable: "NO"}, "mode": {dataType: "text", isNullable: "NO"}, "account_id": {dataType: "text", isNullable: "NO"}, "instrument_id": {dataType: "text", isNullable: "NO"},
			"window_start": {dataType: "timestamp with time zone", isNullable: "NO"}, "window_end": {dataType: "timestamp with time zone", isNullable: "NO"}, "pagination_traversal": {dataType: "text", isNullable: "NO", hasDefault: true}, "position_id": {dataType: "text", isNullable: "NO", hasDefault: true}, "lineage_id": {dataType: "text", isNullable: "NO", hasDefault: true}, "fill_id": {dataType: "text", isNullable: "NO"},
			"source_ref": {dataType: "text", isNullable: "NO"}, "source_revision": {dataType: "text", isNullable: "NO"}, "source_version": {dataType: "text", isNullable: "NO"}, "observed_at": {dataType: "timestamp with time zone", isNullable: "NO"}, "effective_at": {dataType: "timestamp with time zone", isNullable: "NO"}, "fee_amount": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "fee_asset": {dataType: "text", isNullable: "NO"}, "rebate_amount": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "rebate_asset": {dataType: "text", isNullable: "NO"},
			"source_asset": {dataType: "text", isNullable: "NO"}, "reporting_asset": {dataType: "text", isNullable: "NO"}, "conversion_source": {dataType: "text", isNullable: "NO", hasDefault: true}, "conversion_as_of": {dataType: "timestamp with time zone", isNullable: "YES"}, "rounding_mode": {dataType: "text", isNullable: "NO", hasDefault: true}, "scale": {dataType: "integer", isNullable: "NO", hasDefault: true}, "formula_version": {dataType: "text", isNullable: "NO", hasDefault: true}, "already_signed": {dataType: "boolean", isNullable: "NO", hasDefault: true}, "payload_hash": {dataType: "text", isNullable: "NO"}, "sequence_id": {dataType: "bigint", isNullable: "NO", hasDefault: true}, "created_at": {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"ledger_position_snapshot_facts": {
			"fact_id": {dataType: "text", isNullable: "NO"}, "supersedes_fact_id": {dataType: "text", isNullable: "YES"}, "sync_watermark_id": {dataType: "text", isNullable: "YES"}, "principal_id": {dataType: "text", isNullable: "NO"}, "mode": {dataType: "text", isNullable: "NO"}, "account_id": {dataType: "text", isNullable: "NO"}, "instrument_id": {dataType: "text", isNullable: "NO"}, "window_start": {dataType: "timestamp with time zone", isNullable: "NO"}, "window_end": {dataType: "timestamp with time zone", isNullable: "NO"}, "pagination_traversal": {dataType: "text", isNullable: "NO", hasDefault: true}, "position_id": {dataType: "text", isNullable: "NO"},
			"source_ref": {dataType: "text", isNullable: "NO"}, "source_revision": {dataType: "text", isNullable: "NO"}, "source_version": {dataType: "text", isNullable: "NO"}, "observed_at": {dataType: "timestamp with time zone", isNullable: "NO"}, "effective_at": {dataType: "timestamp with time zone", isNullable: "NO"}, "reporting_asset": {dataType: "text", isNullable: "NO"}, "closed": {dataType: "boolean", isNullable: "NO", hasDefault: true}, "as_of": {dataType: "timestamp with time zone", isNullable: "YES"}, "realized_trading_pnl": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "unrealized_trading_pnl": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "actual_funding_cash_flow": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "fees": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "verified": {dataType: "boolean", isNullable: "NO", hasDefault: true}, "residual": {dataType: "jsonb", isNullable: "NO", hasDefault: true}, "conversion_source": {dataType: "text", isNullable: "NO", hasDefault: true}, "conversion_as_of": {dataType: "timestamp with time zone", isNullable: "YES"}, "rounding_mode": {dataType: "text", isNullable: "NO", hasDefault: true}, "scale": {dataType: "integer", isNullable: "NO", hasDefault: true}, "formula_version": {dataType: "text", isNullable: "NO", hasDefault: true}, "payload_hash": {dataType: "text", isNullable: "NO"}, "sequence_id": {dataType: "bigint", isNullable: "NO", hasDefault: true}, "created_at": {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"ledger_lineage_facts": {
			"fact_id": {dataType: "text", isNullable: "NO"}, "principal_id": {dataType: "text", isNullable: "NO"}, "mode": {dataType: "text", isNullable: "NO"}, "account_id": {dataType: "text", isNullable: "NO"}, "instrument_id": {dataType: "text", isNullable: "NO"}, "venue_environment": {dataType: "text", isNullable: "NO"}, "product": {dataType: "text", isNullable: "NO"}, "lineage_id": {dataType: "text", isNullable: "NO"}, "position_id": {dataType: "text", isNullable: "NO"}, "source_ref": {dataType: "text", isNullable: "NO"}, "source_revision": {dataType: "text", isNullable: "NO"}, "source_version": {dataType: "text", isNullable: "NO"}, "observed_at": {dataType: "timestamp with time zone", isNullable: "NO"}, "effective_at": {dataType: "timestamp with time zone", isNullable: "NO"}, "active": {dataType: "boolean", isNullable: "NO", hasDefault: true}, "closed_at": {dataType: "timestamp with time zone", isNullable: "YES"}, "payload_hash": {dataType: "text", isNullable: "NO"}, "sequence_id": {dataType: "bigint", isNullable: "NO", hasDefault: true}, "created_at": {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"ledger_lineage_revision_facts": {
			"fact_id": {dataType: "text", isNullable: "NO"}, "supersedes_fact_id": {dataType: "text", isNullable: "YES"}, "lineage_fact_id": {dataType: "text", isNullable: "NO"}, "sync_watermark_id": {dataType: "text", isNullable: "YES"}, "principal_id": {dataType: "text", isNullable: "NO"}, "mode": {dataType: "text", isNullable: "NO"}, "account_id": {dataType: "text", isNullable: "NO"}, "instrument_id": {dataType: "text", isNullable: "NO"}, "revision": {dataType: "text", isNullable: "NO"}, "status": {dataType: "text", isNullable: "NO"}, "reason": {dataType: "text", isNullable: "NO", hasDefault: true}, "source_ref": {dataType: "text", isNullable: "NO"}, "source_revision": {dataType: "text", isNullable: "NO"}, "source_version": {dataType: "text", isNullable: "NO"}, "observed_at": {dataType: "timestamp with time zone", isNullable: "NO"}, "effective_at": {dataType: "timestamp with time zone", isNullable: "NO"}, "payload_hash": {dataType: "text", isNullable: "NO"}, "sequence_id": {dataType: "bigint", isNullable: "NO", hasDefault: true}, "created_at": {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"ledger_sync_watermarks": {
			"fact_id": {dataType: "text", isNullable: "NO"}, "principal_id": {dataType: "text", isNullable: "NO"}, "mode": {dataType: "text", isNullable: "NO"}, "account_id": {dataType: "text", isNullable: "NO"}, "instrument_id": {dataType: "text", isNullable: "NO"}, "window_start": {dataType: "timestamp with time zone", isNullable: "NO"}, "window_end": {dataType: "timestamp with time zone", isNullable: "NO"}, "pagination_traversal": {dataType: "text", isNullable: "NO", hasDefault: true}, "source_ref": {dataType: "text", isNullable: "NO"}, "cursor": {dataType: "text", isNullable: "NO"}, "revision": {dataType: "text", isNullable: "NO"}, "observed_at": {dataType: "timestamp with time zone", isNullable: "NO"}, "effective_through": {dataType: "timestamp with time zone", isNullable: "NO"}, "complete_through": {dataType: "timestamp with time zone", isNullable: "NO"}, "status": {dataType: "text", isNullable: "NO"}, "verified": {dataType: "boolean", isNullable: "NO", hasDefault: true}, "payload_hash": {dataType: "text", isNullable: "NO"}, "sequence_id": {dataType: "bigint", isNullable: "NO", hasDefault: true}, "created_at": {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"ledger_closed_position_projections": {
			"position_id": {dataType: "text", isNullable: "NO"}, "principal_id": {dataType: "text", isNullable: "NO"}, "mode": {dataType: "text", isNullable: "NO"}, "account_id": {dataType: "text", isNullable: "NO"}, "instrument_id": {dataType: "text", isNullable: "NO"}, "reporting_asset": {dataType: "text", isNullable: "NO"}, "realized_trading_pnl": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "unrealized_trading_pnl": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "actual_funding_cash_flow": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "fees": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "net_pnl": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "closed_trading_upnl": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "verified": {dataType: "boolean", isNullable: "NO", hasDefault: true}, "verification_status": {dataType: "text", isNullable: "NO", hasDefault: true}, "residual": {dataType: "jsonb", isNullable: "NO", hasDefault: true}, "watermark": {dataType: "jsonb", isNullable: "NO", hasDefault: true}, "as_of": {dataType: "timestamp with time zone", isNullable: "YES"}, "conversion_source": {dataType: "text", isNullable: "NO", hasDefault: true}, "conversion_as_of": {dataType: "timestamp with time zone", isNullable: "YES"}, "rounding_mode": {dataType: "text", isNullable: "NO", hasDefault: true}, "scale": {dataType: "integer", isNullable: "NO", hasDefault: true}, "formula_version": {dataType: "text", isNullable: "NO", hasDefault: true}, "updated_at": {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
		"ledger_portfolio_projections": {
			"principal_id": {dataType: "text", isNullable: "NO"}, "mode": {dataType: "text", isNullable: "NO"}, "reporting_asset": {dataType: "text", isNullable: "NO"}, "cumulative_realized_net_pnl": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "current_open_unrealized_pnl": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "total_net_pnl": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "funding_total": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "fee_total": {dataType: "numeric", isNullable: "NO", hasDefault: true}, "verification_status": {dataType: "text", isNullable: "NO", hasDefault: true}, "open_position_count": {dataType: "integer", isNullable: "NO", hasDefault: true}, "closed_position_count": {dataType: "integer", isNullable: "NO", hasDefault: true}, "residual": {dataType: "jsonb", isNullable: "NO", hasDefault: true}, "watermark": {dataType: "jsonb", isNullable: "NO", hasDefault: true}, "as_of": {dataType: "timestamp with time zone", isNullable: "YES"}, "conversion_source": {dataType: "text", isNullable: "NO", hasDefault: true}, "conversion_as_of": {dataType: "timestamp with time zone", isNullable: "YES"}, "rounding_mode": {dataType: "text", isNullable: "NO", hasDefault: true}, "scale": {dataType: "integer", isNullable: "NO", hasDefault: true}, "formula_version": {dataType: "text", isNullable: "NO", hasDefault: true}, "updated_at": {dataType: "timestamp with time zone", isNullable: "NO", hasDefault: true},
		},
	}
}

func validateLedgerIndexes(ctx context.Context, q queryer) error {
	type indexSpec struct {
		name      string
		table     string
		columns   []string
		unique    bool
		predicate string
	}
	specs := []indexSpec{
		{name: "ledger_funding_payment_facts_idempotency_idx", table: "ledger_funding_payment_facts", columns: []string{"principal_id", "mode", "account_id", "instrument_id", "window_start", "window_end", "pagination_traversal", "source_ref", "source_revision"}, unique: true},
		{name: "ledger_funding_payment_facts_successor_idx", table: "ledger_funding_payment_facts", columns: []string{"supersedes_fact_id"}, unique: true, predicate: "(supersedes_fact_id IS NOT NULL)"},
		{name: "ledger_funding_payment_facts_scope_revision_idx", table: "ledger_funding_payment_facts", columns: []string{"principal_id", "mode", "account_id", "instrument_id", "source_revision", "sequence_id"}},
		{name: "ledger_fill_cashflow_facts_idempotency_idx", table: "ledger_fill_cashflow_facts", columns: []string{"principal_id", "mode", "account_id", "instrument_id", "window_start", "window_end", "pagination_traversal", "fill_id", "source_ref", "source_revision"}, unique: true},
		{name: "ledger_fill_cashflow_facts_successor_idx", table: "ledger_fill_cashflow_facts", columns: []string{"supersedes_fact_id"}, unique: true, predicate: "(supersedes_fact_id IS NOT NULL)"},
		{name: "ledger_position_snapshot_facts_idempotency_idx", table: "ledger_position_snapshot_facts", columns: []string{"principal_id", "mode", "account_id", "instrument_id", "window_start", "window_end", "pagination_traversal", "position_id", "source_ref", "source_revision"}, unique: true},
		{name: "ledger_position_snapshot_facts_successor_idx", table: "ledger_position_snapshot_facts", columns: []string{"supersedes_fact_id"}, unique: true, predicate: "(supersedes_fact_id IS NOT NULL)"},
		{name: "ledger_lineage_facts_idempotency_idx", table: "ledger_lineage_facts", columns: []string{"principal_id", "mode", "account_id", "instrument_id", "lineage_id", "source_ref", "source_revision"}, unique: true},
		{name: "ledger_lineage_facts_active_scope_idx", table: "ledger_lineage_facts", columns: []string{"principal_id", "mode", "account_id", "instrument_id"}, unique: true, predicate: "active"},
		{name: "ledger_lineage_revision_facts_idempotency_idx", table: "ledger_lineage_revision_facts", columns: []string{"lineage_fact_id", "revision", "source_ref", "source_revision"}, unique: true},
		{name: "ledger_lineage_revision_facts_successor_idx", table: "ledger_lineage_revision_facts", columns: []string{"supersedes_fact_id"}, unique: true, predicate: "(supersedes_fact_id IS NOT NULL)"},
		{name: "ledger_sync_watermarks_idempotency_idx", table: "ledger_sync_watermarks", columns: []string{"principal_id", "mode", "account_id", "instrument_id", "window_start", "window_end", "pagination_traversal", "source_ref", "revision", "cursor"}, unique: true},
		{name: "ledger_sync_watermarks_scope_revision_idx", table: "ledger_sync_watermarks", columns: []string{"principal_id", "mode", "account_id", "instrument_id", "revision", "cursor", "sequence_id"}},
		{name: "ledger_closed_position_projections_scope_idx", table: "ledger_closed_position_projections", columns: []string{"principal_id", "mode", "reporting_asset", "updated_at"}},
	}
	for _, spec := range specs {
		var tableName string
		var unique bool
		var predicate *string
		var columns []string
		err := q.QueryRow(ctx, `
			select t.relname, i.indisunique, pg_get_expr(i.indpred, i.indrelid),
			  array_agg(a.attname order by ord.ordinality)
			from pg_index i
			join pg_class c on c.oid=i.indexrelid
			join pg_class t on t.oid=i.indrelid
			join unnest(i.indkey) with ordinality as ord(attnum, ordinality) on true
			join pg_attribute a on a.attrelid=t.oid and a.attnum=ord.attnum
			where c.relname=$1
			group by t.relname, i.indisunique, i.indpred, i.indrelid
		`, spec.name).Scan(&tableName, &unique, &predicate, &columns)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("schema drift: missing index %s", spec.name)
		}
		if err != nil {
			return err
		}
		if tableName != spec.table || unique != spec.unique || strings.Join(columns, ",") != strings.Join(spec.columns, ",") {
			return fmt.Errorf("schema drift: invalid index %s", spec.name)
		}
		gotPredicate := ""
		if predicate != nil {
			gotPredicate = *predicate
		}
		if gotPredicate != spec.predicate {
			return fmt.Errorf("schema drift: invalid index predicate %s", spec.name)
		}
	}
	return nil
}

func validateLedgerKeyConstraints(ctx context.Context, q queryer) error {
	type keySpec struct {
		table, constraintType string
		columns               []string
	}
	for _, spec := range []keySpec{
		{"ledger_funding_payment_facts", "p", []string{"fact_id"}},
		{"ledger_fill_cashflow_facts", "p", []string{"fact_id"}},
		{"ledger_position_snapshot_facts", "p", []string{"fact_id"}},
		{"ledger_lineage_facts", "p", []string{"fact_id"}},
		{"ledger_lineage_revision_facts", "p", []string{"fact_id"}},
		{"ledger_sync_watermarks", "p", []string{"fact_id"}},
		{"ledger_closed_position_projections", "p", []string{"principal_id", "mode", "reporting_asset", "position_id"}},
		{"ledger_portfolio_projections", "p", []string{"principal_id", "mode", "reporting_asset"}},
		{"ledger_funding_payment_facts", "u", []string{"sequence_id"}},
		{"ledger_fill_cashflow_facts", "u", []string{"sequence_id"}},
		{"ledger_position_snapshot_facts", "u", []string{"sequence_id"}},
		{"ledger_lineage_facts", "u", []string{"sequence_id"}},
		{"ledger_lineage_revision_facts", "u", []string{"sequence_id"}},
		{"ledger_sync_watermarks", "u", []string{"sequence_id"}},
	} {
		var exists bool
		if err := q.QueryRow(ctx, `
			select exists(
				select 1
				from pg_constraint c
				join pg_class t on t.oid=c.conrelid
				where t.relname=$1 and c.contype=$2
				  and (
				    select array_agg(a.attname::text order by ord.ordinality)
				    from unnest(c.conkey) with ordinality as ord(attnum, ordinality)
				    join pg_attribute a on a.attrelid=t.oid and a.attnum=ord.attnum
				  ) = $3::text[]
			)
		`, spec.table, spec.constraintType, "{"+strings.Join(spec.columns, ",")+"}").Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("schema drift: missing ledger %s constraint %s(%s)", spec.constraintType, spec.table, strings.Join(spec.columns, ","))
		}
	}
	return nil
}

func validateLedgerForeignKeys(ctx context.Context, q queryer) error {
	type fkSpec struct {
		table, column, refTable, refColumn string
	}
	for _, spec := range []fkSpec{
		{"ledger_funding_payment_facts", "supersedes_fact_id", "ledger_funding_payment_facts", "fact_id"},
		{"ledger_funding_payment_facts", "sync_watermark_id", "ledger_sync_watermarks", "fact_id"},
		{"ledger_fill_cashflow_facts", "supersedes_fact_id", "ledger_fill_cashflow_facts", "fact_id"},
		{"ledger_fill_cashflow_facts", "sync_watermark_id", "ledger_sync_watermarks", "fact_id"},
		{"ledger_position_snapshot_facts", "supersedes_fact_id", "ledger_position_snapshot_facts", "fact_id"},
		{"ledger_position_snapshot_facts", "sync_watermark_id", "ledger_sync_watermarks", "fact_id"},
		{"ledger_lineage_revision_facts", "supersedes_fact_id", "ledger_lineage_revision_facts", "fact_id"},
		{"ledger_lineage_revision_facts", "lineage_fact_id", "ledger_lineage_facts", "fact_id"},
		{"ledger_lineage_revision_facts", "sync_watermark_id", "ledger_sync_watermarks", "fact_id"},
	} {
		var exists bool
		if err := q.QueryRow(ctx, `
			select exists(
				select 1
				from pg_constraint c
				join pg_class child on child.oid=c.conrelid
				join pg_class parent on parent.oid=c.confrelid
				join pg_attribute child_col on child_col.attrelid=child.oid and child_col.attnum=c.conkey[1]
				join pg_attribute parent_col on parent_col.attrelid=parent.oid and parent_col.attnum=c.confkey[1]
				where c.contype='f'
				  and child.relname=$1 and child_col.attname=$2
				  and parent.relname=$3 and parent_col.attname=$4
			)
		`, spec.table, spec.column, spec.refTable, spec.refColumn).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("schema drift: missing foreign key %s.%s", spec.table, spec.column)
		}
	}
	return nil
}

func validateLedgerChecks(ctx context.Context, q queryer) error {
	type checkSpec struct {
		table, expression string
	}
	checks := []checkSpec{
		{"ledger_funding_payment_facts", ledgerModeCheckExpression()},
		{"ledger_funding_payment_facts", "window_start < window_end"},
		{"ledger_funding_payment_facts", "instrument_id <> ''"},
		{"ledger_funding_payment_facts", "amount is not null and asset is not null"},
		{"ledger_fill_cashflow_facts", ledgerModeCheckExpression()},
		{"ledger_fill_cashflow_facts", "already_signed or fee_amount >= 0"},
		{"ledger_fill_cashflow_facts", "already_signed or rebate_amount >= 0"},
		{"ledger_fill_cashflow_facts", "window_start < window_end"},
		{"ledger_position_snapshot_facts", ledgerModeCheckExpression()},
		{"ledger_position_snapshot_facts", "window_start < window_end"},
		{"ledger_lineage_facts", ledgerModeCheckExpression()},
		{"ledger_lineage_revision_facts", ledgerModeCheckExpression()},
		{"ledger_sync_watermarks", ledgerModeCheckExpression()},
		{"ledger_sync_watermarks", "status = any array['unsupported','temporarily_unavailable','incomplete','complete_empty','complete']"},
		{"ledger_sync_watermarks", "window_start < window_end"},
		{"ledger_sync_watermarks", "instrument_id <> ''"},
		{"ledger_sync_watermarks", "verified = false or status = any array['complete','complete_empty']"},
		{"ledger_closed_position_projections", ledgerModeCheckExpression()},
		{"ledger_closed_position_projections", "verification_status = any array['unsupported','temporarily_unavailable','incomplete','provisional','verified','revised','blocked']"},
		{"ledger_closed_position_projections", "closed_trading_upnl = unrealized_trading_pnl"},
		{"ledger_closed_position_projections", "not verified or unrealized_trading_pnl = 0 and closed_trading_upnl = 0"},
		{"ledger_closed_position_projections", "net_pnl = realized_trading_pnl + unrealized_trading_pnl + actual_funding_cash_flow + fees"},
		{"ledger_portfolio_projections", ledgerModeCheckExpression()},
		{"ledger_portfolio_projections", "verification_status = any array['unsupported','temporarily_unavailable','incomplete','provisional','verified','revised','blocked']"},
		{"ledger_portfolio_projections", "total_net_pnl = cumulative_realized_net_pnl + current_open_unrealized_pnl"},
		{"ledger_portfolio_projections", "open_position_count >= 0 and closed_position_count >= 0"},
	}
	for _, spec := range checks {
		var defs []string
		if err := q.QueryRow(ctx, `
			select coalesce(array_agg(pg_get_constraintdef(c.oid)), '{}')
			from pg_constraint c
			join pg_class t on t.oid=c.conrelid
			where t.relname=$1 and c.contype='c'
		`, spec.table).Scan(&defs); err != nil {
			return err
		}
		matched := false
		expected := normalizeLedgerExpression(spec.expression)
		for _, definition := range defs {
			if normalizeLedgerExpression(definition) == expected {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("schema drift: missing ledger check %s expression %q; definitions=%q", spec.table, spec.expression, strings.Join(defs, "\n"))
		}
	}
	return nil
}

func ledgerModeCheckExpression() string {
	return "mode = any array['test','live']"
}

func normalizeLedgerExpression(value string) string {
	value = strings.ToLower(value)
	for _, cast := range []string{"::text", "::numeric", "::boolean", "::integer", "::bigint"} {
		value = strings.ReplaceAll(value, cast, "")
	}
	return strings.NewReplacer(
		"check", "",
		"(", "",
		")", "",
		" ", "",
		"\n", "",
		"\r", "",
		"\t", "",
	).Replace(value)
}

func validateLedgerTriggers(ctx context.Context, q queryer) error {
	type triggerSpec struct {
		name, table, function string
		event                 int16
	}
	for _, spec := range []triggerSpec{
		{"ledger_funding_payment_facts_no_update", "ledger_funding_payment_facts", "prevent_ledger_append_only_update", 16},
		{"ledger_funding_payment_facts_no_delete", "ledger_funding_payment_facts", "prevent_ledger_append_only_delete", 8},
		{"ledger_fill_cashflow_facts_no_update", "ledger_fill_cashflow_facts", "prevent_ledger_append_only_update", 16},
		{"ledger_fill_cashflow_facts_no_delete", "ledger_fill_cashflow_facts", "prevent_ledger_append_only_delete", 8},
		{"ledger_position_snapshot_facts_no_update", "ledger_position_snapshot_facts", "prevent_ledger_append_only_update", 16},
		{"ledger_position_snapshot_facts_no_delete", "ledger_position_snapshot_facts", "prevent_ledger_append_only_delete", 8},
		{"ledger_lineage_facts_controlled_update", "ledger_lineage_facts", "enforce_ledger_lineage_update", 16},
		{"ledger_lineage_facts_no_delete", "ledger_lineage_facts", "prevent_ledger_append_only_delete", 8},
		{"ledger_lineage_revision_facts_no_update", "ledger_lineage_revision_facts", "prevent_ledger_append_only_update", 16},
		{"ledger_lineage_revision_facts_no_delete", "ledger_lineage_revision_facts", "prevent_ledger_append_only_delete", 8},
		{"ledger_sync_watermarks_no_update", "ledger_sync_watermarks", "prevent_ledger_append_only_update", 16},
		{"ledger_sync_watermarks_no_delete", "ledger_sync_watermarks", "prevent_ledger_append_only_delete", 8},
	} {
		var tableName, functionName string
		var event int16
		var isBefore, isRow bool
		err := q.QueryRow(ctx, `
			select t.relname, p.proname, (tg.tgtype::int & 28)::int2,
			  (tg.tgtype::int & 2) <> 0,
			  (tg.tgtype::int & 1) <> 0
			from pg_trigger tg
			join pg_class t on t.oid=tg.tgrelid
			join pg_proc p on p.oid=tg.tgfoid
			where tg.tgname=$1 and not tg.tgisinternal
		`, spec.name).Scan(&tableName, &functionName, &event, &isBefore, &isRow)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("schema drift: missing trigger %s", spec.name)
		}
		if err != nil {
			return err
		}
		if tableName != spec.table || functionName != spec.function || event != spec.event || !isBefore || !isRow {
			return fmt.Errorf("schema drift: invalid trigger %s", spec.name)
		}
	}
	return nil
}

func validateLedgerSequence(ctx context.Context, q queryer) error {
	var exists bool
	if err := q.QueryRow(ctx, `select to_regclass('public.ledger_append_sequence') is not null`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("schema drift: missing global ledger append sequence")
	}
	var owned bool
	if err := q.QueryRow(ctx, `
		select exists(
			select 1
			from pg_depend d
			join pg_class seq on seq.oid=d.objid
			join pg_namespace ns on ns.oid=seq.relnamespace
			where d.classid='pg_class'::regclass
			  and d.deptype='a'
			  and ns.nspname='public'
			  and seq.relname='ledger_append_sequence'
		)
	`).Scan(&owned); err != nil {
		return err
	}
	if owned {
		return fmt.Errorf("schema drift: global ledger append sequence must not be owned by one column")
	}
	for _, table := range []string{
		"ledger_funding_payment_facts", "ledger_fill_cashflow_facts", "ledger_position_snapshot_facts",
		"ledger_lineage_facts", "ledger_lineage_revision_facts", "ledger_sync_watermarks",
	} {
		var defaultValue string
		var defaultDependsOnSequence bool
		if err := q.QueryRow(ctx, `
			select cols.column_default,
			  exists(
			    select 1
			    from pg_attrdef def
			    join pg_class tbl on tbl.oid=def.adrelid
			    join pg_namespace tbl_ns on tbl_ns.oid=tbl.relnamespace
			    join pg_attribute attr on attr.attrelid=tbl.oid and attr.attnum=def.adnum
			    join pg_depend dep on dep.classid='pg_attrdef'::regclass and dep.objid=def.oid
			    join pg_class seq on seq.oid=dep.refobjid
			    join pg_namespace seq_ns on seq_ns.oid=seq.relnamespace
			    where tbl_ns.nspname='public'
			      and tbl.relname=$1
			      and attr.attname='sequence_id'
			      and dep.refclassid='pg_class'::regclass
			      and seq_ns.nspname='public'
			      and seq.relname='ledger_append_sequence'
			  )
			from information_schema.columns cols
			where cols.table_schema='public' and cols.table_name=$1 and cols.column_name='sequence_id'
		`, table).Scan(&defaultValue, &defaultDependsOnSequence); err != nil {
			return err
		}
		if !strings.Contains(defaultValue, "ledger_append_sequence") || !defaultDependsOnSequence {
			return fmt.Errorf("schema drift: invalid sequence default %s.sequence_id", table)
		}
	}
	return nil
}
