package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/cutover"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCutoverRunnerMigratesAuthoritativeRowsAndRebuildsDerivedState(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	seedSuccessfulCutover(t, ctx, control)
	control.Reset()

	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cutoverPool.Close()
	report, err := NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.SchemaVersion != 4 || report.SessionsRevoked != 1 ||
		report.DerivedRowsCleared != 4 {
		t.Fatalf("cutover report=%+v", report)
	}

	assertColumnAbsent(t, ctx, control, "users", "environment")
	for _, table := range []string{
		"idempotency_records", "execution_previews", "opportunities", "alerts",
		"executions", "execution_transitions", "positions", "activities",
		"audit_events", "funding_observations", "read_model_events",
	} {
		assertColumnAbsent(t, ctx, control, table, "environment")
		assertColumnPresent(t, ctx, control, table, "mode")
	}
	assertColumnPresent(t, ctx, control, "sessions", "mode")
	assertColumnPresent(t, ctx, control, "read_model_events", "contract_version")

	var sessionsRevoked int
	if err := control.QueryRow(ctx, "select count(*) from sessions where revoked_at is not null").Scan(&sessionsRevoked); err != nil {
		t.Fatal(err)
	}
	if sessionsRevoked != 1 {
		t.Fatalf("revoked sessions=%d", sessionsRevoked)
	}
	var sessionMode string
	if err := control.QueryRow(ctx, "select mode from sessions where id='session-1'").Scan(&sessionMode); err != nil {
		t.Fatal(err)
	}
	if sessionMode != "test" {
		t.Fatalf("cutover session mode=%q, want test", sessionMode)
	}
	for _, table := range []string{"opportunities", "alerts", "execution_previews", "read_model_events"} {
		var count int
		if err := control.QueryRow(ctx, "select count(*) from "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("derived table %s count=%d", table, count)
		}
	}

	var mode, hash, responseMode string
	if err := control.QueryRow(ctx, `
		select mode, request_hash, response_body->>'mode'
		from idempotency_records where id='idem-1'
	`).Scan(&mode, &hash, &responseMode); err != nil {
		t.Fatal(err)
	}
	if mode != "test" || hash != "request-hash-1" || responseMode != "test" {
		t.Fatalf("idempotency mode=%q hash=%q responseMode=%q", mode, hash, responseMode)
	}
	assertCutoverRouteEvidence(t, ctx, control)

	rows, err := control.Query(ctx, `
		select role, venue, venue_environment
		from execution_legs where execution_id='exec-1' order by role
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	gotLegs := make([]string, 0, 2)
	for rows.Next() {
		var role, venue, venueEnvironment string
		if err := rows.Scan(&role, &venue, &venueEnvironment); err != nil {
			t.Fatal(err)
		}
		gotLegs = append(gotLegs, strings.Join([]string{role, venue, venueEnvironment}, ":"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotLegs, ",") != "long:binance:demo,short:okx:testnet" {
		t.Fatalf("execution provenance=%v", gotLegs)
	}

	fundingRows, err := control.Query(ctx, `
		select mode, venue_environment
		from funding_observations order by venue_environment
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer fundingRows.Close()
	gotFunding := make([]string, 0, 4)
	for fundingRows.Next() {
		var productMode, venueEnvironment string
		if err := fundingRows.Scan(&productMode, &venueEnvironment); err != nil {
			t.Fatal(err)
		}
		gotFunding = append(gotFunding, productMode+":"+venueEnvironment)
	}
	if strings.Join(gotFunding, ",") != "test:demo,test:demo,test:testnet,test:testnet" {
		t.Fatalf("funding provenance=%v", gotFunding)
	}

	var executionCount, legCount, fillCount, positionCount, activityCount, auditCount int
	if err := control.QueryRow(ctx, `
		select
		  (select count(*) from executions),
		  (select count(*) from execution_legs),
		  (select count(*) from execution_fills),
		  (select count(*) from positions),
		  (select count(*) from activities),
		  (select count(*) from audit_events)
	`).Scan(&executionCount, &legCount, &fillCount, &positionCount, &activityCount, &auditCount); err != nil {
		t.Fatal(err)
	}
	if executionCount != 1 || legCount != 2 || fillCount != 1 ||
		positionCount != 1 || activityCount != 1 || auditCount != 2 {
		t.Fatalf("authoritative counts executions=%d legs=%d fills=%d positions=%d activities=%d audit=%d",
			executionCount, legCount, fillCount, positionCount, activityCount, auditCount)
	}

	var phase, backupID string
	var schemaVersion int
	if err := control.QueryRow(ctx, `
		select phase, backup_id, contract_version from cutover_control where singleton
	`).Scan(&phase, &backupID, &schemaVersion); err != nil {
		t.Fatal(err)
	}
	if phase != "validated" || backupID != "backup-integration-1" || schemaVersion != 4 {
		t.Fatalf("cutover state phase=%q backup=%q version=%d", phase, backupID, schemaVersion)
	}
	stateStore := NewCutoverStateStore(control)
	if err := stateStore.AuthorizeRestore(ctx, "backup-integration-1"); err != nil {
		t.Fatalf("restore before reopen rejected: %v", err)
	}
	state, err := stateStore.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != cutover.PhaseRestore {
		t.Fatalf("restore phase=%q", state.Phase)
	}
}

func TestCutoverRunnerRejectsCompletedIdempotencyWithoutLegIdentity(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	seedSuccessfulCutover(t, ctx, control)
	if _, err := control.Exec(ctx, `
		update idempotency_records
		set response_body=jsonb_set(
		  response_body,
		  '{legs,0}',
		  '{"venueEnvironment":"demo"}'::jsonb
		)
		where id='idem-1'
	`); err != nil {
		t.Fatal(err)
	}
	control.Reset()

	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cutoverPool.Close()
	_, err = NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx)
	if !errors.Is(err, ErrMissingProvenance) {
		t.Fatalf("Run err=%v, want ErrMissingProvenance", err)
	}
	assertColumnPresent(t, ctx, control, "executions", "environment")
	assertColumnAbsent(t, ctx, control, "executions", "mode")
}

func TestCutoverRunnerAcceptsPartialExecutionEvidenceWithPlannedPositionRoutes(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	seedSuccessfulCutover(t, ctx, control)
	seedPartialExecutionCutover(t, ctx, control)
	control.Reset()

	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cutoverPool.Close()
	if _, err := NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var (
		executionMode            string
		idempotencyLegs          int
		positionLongEnvironment  string
		positionShortEnvironment string
	)
	if err := control.QueryRow(ctx, `
		select
		  (select mode from executions where id='exec-partial'),
		  (select jsonb_array_length(response_body->'legs')
		   from idempotency_records where id='idem-partial'),
		  (select payload->'longLeg'->>'venueEnvironment'
		   from positions where id='position-partial'),
		  (select payload->'shortLeg'->>'venueEnvironment'
		   from positions where id='position-partial')
	`).Scan(
		&executionMode,
		&idempotencyLegs,
		&positionLongEnvironment,
		&positionShortEnvironment,
	); err != nil {
		t.Fatal(err)
	}
	if executionMode != "test" || idempotencyLegs != 1 ||
		positionLongEnvironment != "demo" || positionShortEnvironment != "testnet" {
		t.Fatalf(
			"partial cutover mode=%q idempotencyLegs=%d positionEnvironments=%q/%q",
			executionMode,
			idempotencyLegs,
			positionLongEnvironment,
			positionShortEnvironment,
		)
	}
}

func TestServeMigrationRunnerAcceptsValidatedProductModeSchema(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	seedSuccessfulCutover(t, ctx, control)
	control.Close()

	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx); err != nil {
		cutoverPool.Close()
		t.Fatal(err)
	}
	cutoverPool.Close()

	applicationPool, err := OpenApplicationPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer applicationPool.Close()
	err = NewMigrationRunner(applicationPool).Up(ctx)
	if err != nil {
		t.Fatalf("product-mode serve validation error=%v", err)
	}
}

func TestServeMigrationRunnerAcceptsRecoverableExecutionWithPlannedRouteEvidence(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	seedSuccessfulCutover(t, ctx, control)
	control.Close()

	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx); err != nil {
		cutoverPool.Close()
		t.Fatal(err)
	}
	cutoverPool.Close()

	applicationPool, err := OpenApplicationPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer applicationPool.Close()
	if _, err := applicationPool.Exec(ctx, `
		insert into executions(
		  id, principal_id, mode, operation_kind, position_id, parent_execution_id,
		  lineage_id, idempotency_key, request_hash, status, reconciliation_state,
		  manual_intervention, payload, version, created_at, updated_at
		) values(
		  'exec-recoverable','principal-default','test','close','position-recoverable','exec-1',
		  'lineage-1','idem-recoverable','hash-recoverable','leg_1_working','pending',false,
		  '{
		    "id":"exec-recoverable",
		    "mode":"test",
		    "plannedLegs":[
		      {"role":"long","accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"},
		      {"role":"short","accountId":"okx-testnet","venue":"okx","instrumentId":"okx:testnet:perp:USDT:BTC-USDT","venueEnvironment":"testnet"}
		    ],
		    "legs":[
		      {"role":"long","accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"}
		    ]
		  }'::jsonb,
		  1,now(),now()
		);
		insert into execution_legs(
		  leg_id,execution_id,role,account_id,venue,instrument_id,venue_environment,
		  side,requested_quantity,filled_quantity,status,gateway_id,command_id,
		  gateway_order_id,reduce_only,safe_error,updated_at
		) values(
		  'exec-recoverable:long','exec-recoverable','long','binance-demo','binance',
		  'binance:demo:perp:USDT:BTC-USDT','demo','sell',5,5,'partial','gw-long',
		  'cmd-long','order-long',true,'{}'::jsonb,now()
		);
		insert into execution_transitions(
		  execution_id,principal_id,mode,from_status,to_status,kind,request_id,payload,occurred_at
		) values(
		  'exec-recoverable','principal-default','test','submitting','leg_1_working',
		  'leg_fill','request-recoverable',
		  '{"legs":[
		    {"role":"long","accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"},
		    {"role":"short","accountId":"okx-testnet","venue":"okx","instrumentId":"okx:testnet:perp:USDT:BTC-USDT","venueEnvironment":"testnet"}
		  ]}'::jsonb,now()
		);
		insert into audit_events(
		  request_id,principal_id,mode,action,outcome,execution_id,target_id,payload,created_at
		) values(
		  'request-recoverable','principal-default','test','execution.leg_fill','leg_1_working',
		  'exec-recoverable','exec-recoverable',
		  '{"legs":[
		    {"role":"long","accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"},
		    {"role":"short","accountId":"okx-testnet","venue":"okx","instrumentId":"okx:testnet:perp:USDT:BTC-USDT","venueEnvironment":"testnet"}
		  ]}'::jsonb,now()
		)
	`); err != nil {
		t.Fatal(err)
	}

	if err := NewMigrationRunner(applicationPool).Up(ctx); err != nil {
		t.Fatalf("recoverable execution blocked serve migration: %v", err)
	}
}

func TestCutoverRunnerMapsDemoTestnetAndMainnetWithoutConflatingNativeProvenance(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()

	for _, test := range []struct {
		name              string
		sourceEnvironment string
		venueEnvironment  string
		wantMode          string
	}{
		{name: "demo", sourceEnvironment: "demo", venueEnvironment: "demo", wantMode: "test"},
		{name: "testnet", sourceEnvironment: "testnet", venueEnvironment: "testnet", wantMode: "test"},
		{name: "mainnet", sourceEnvironment: "mainnet", venueEnvironment: "live", wantMode: "live"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetCutoverDatabase(t, ctx, control)
			applyLegacyMigrationsForTest(t, ctx, control)
			instrumentID := "binance:" + test.venueEnvironment + ":perp:USDT:BTC-USDT"
			payload := `{
			  "id":"exec-map",
			  "legs":[{
			    "role":"long",
			    "accountId":"account-map",
			    "venue":"binance",
			    "instrumentId":"` + instrumentID + `",
			    "venueEnvironment":"` + test.venueEnvironment + `"
			  }]
			}`
			if _, err := control.Exec(ctx, `
				insert into executions(
				  id,status,payload,principal_id,environment,operation_kind,
				  reconciliation_state,version
				) values('exec-map','hedged',$1::jsonb,'principal-default',$2,'open','ready',1)
			`, payload, test.sourceEnvironment); err != nil {
				t.Fatal(err)
			}
			if _, err := control.Exec(ctx, `
				insert into execution_legs(
				  leg_id,execution_id,role,account_id,instrument_id,side,
				  requested_quantity,filled_quantity,status,updated_at
				) values(
				  'exec-map:long','exec-map','long','account-map',$1,'buy',
				  1,1,'filled',now()
				)
			`, instrumentID); err != nil {
				t.Fatal(err)
			}
			if _, err := control.Exec(ctx, `
				insert into funding_observations(
				  environment,account_id,instrument_id,funding_time,observed_at,rate
				) values(
				  $1,'account-map',$2,'2026-07-30T08:00:00Z',
				  '2026-07-30T07:00:00Z',0.0001
				)
			`, test.sourceEnvironment, instrumentID); err != nil {
				t.Fatal(err)
			}
			control.Reset()

			cutoverPool, err := OpenCutoverPool(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx); err != nil {
				cutoverPool.Close()
				t.Fatal(err)
			}
			cutoverPool.Close()

			var executionMode, legEnvironment, fundingMode, fundingEnvironment string
			if err := control.QueryRow(ctx, `
				select
				  (select mode from executions where id='exec-map'),
				  (select venue_environment from execution_legs where leg_id='exec-map:long'),
				  (select mode from funding_observations limit 1),
				  (select venue_environment from funding_observations limit 1)
			`).Scan(
				&executionMode,
				&legEnvironment,
				&fundingMode,
				&fundingEnvironment,
			); err != nil {
				t.Fatal(err)
			}
			if executionMode != test.wantMode || fundingMode != test.wantMode ||
				legEnvironment != test.venueEnvironment ||
				fundingEnvironment != test.venueEnvironment {
				t.Fatalf(
					"executionMode=%q fundingMode=%q legEnvironment=%q fundingEnvironment=%q",
					executionMode,
					fundingMode,
					legEnvironment,
					fundingEnvironment,
				)
			}
		})
	}
}

func TestCutoverRunnerAbortsAuthoritativeCollisionWithoutPartialContract(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	for _, seed := range []struct {
		id          string
		environment string
	}{
		{id: "idem-demo", environment: "demo"},
		{id: "idem-testnet", environment: "testnet"},
	} {
		if _, err := control.Exec(ctx, `
			insert into idempotency_records(
			  id, key, request_hash, principal_id, environment, method, path, state
			) values($1,'same-key',$2,'principal-default',$3,'POST','/api/v1/orders','completed')
		`, seed.id, "hash-"+seed.id, seed.environment); err != nil {
			t.Fatal(err)
		}
	}
	control.Reset()

	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cutoverPool.Close()
	_, err = NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx)
	if !errors.Is(err, ErrAuthoritativeCollision) {
		t.Fatalf("Run error=%v, want ErrAuthoritativeCollision", err)
	}
	assertColumnAbsent(t, ctx, control, "idempotency_records", "mode")
	var versionFour int
	if err := control.QueryRow(ctx, "select count(*) from schema_migrations where version=4").Scan(&versionFour); err != nil {
		t.Fatal(err)
	}
	if versionFour != 0 {
		t.Fatal("failed cutover recorded version four")
	}
}

func TestCutoverRunnerRejectsMissingExecutionProvenance(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	if _, err := control.Exec(ctx, `
		insert into executions(
		  id, status, payload, principal_id, environment, operation_kind,
		  reconciliation_state, version
		) values(
		  'exec-missing','hedged',
		  '{"id":"exec-missing","mode":"test","legs":[{"role":"long","accountId":"acct","instrumentId":"opaque"}]}'::jsonb,
		  'principal-default','test','open','ready',1
		);
		insert into execution_legs(
		  leg_id, execution_id, role, account_id, instrument_id, side,
		  requested_quantity, filled_quantity, status, updated_at
		) values(
		  'exec-missing:long','exec-missing','long','acct','opaque','buy',
		  1,1,'filled',now()
		);
	`); err != nil {
		t.Fatal(err)
	}
	control.Reset()

	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cutoverPool.Close()
	_, err = NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx)
	if !errors.Is(err, ErrMissingProvenance) {
		t.Fatalf("Run error=%v, want ErrMissingProvenance", err)
	}
	assertColumnAbsent(t, ctx, control, "executions", "mode")
}

func TestCutoverRunnerRejectsConflictingCanonicalInstrumentProvenance(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()

	t.Run("execution leg", func(t *testing.T) {
		resetCutoverDatabase(t, ctx, control)
		applyLegacyMigrationsForTest(t, ctx, control)
		if _, err := control.Exec(ctx, `
			insert into executions(
			  id,status,payload,principal_id,environment,operation_kind,
			  reconciliation_state,version
			) values(
			  'exec-conflict','hedged',
			  '{
			    "id":"exec-conflict",
			    "mode":"test",
			    "legs":[{
			      "role":"long",
			      "accountId":"acct",
			      "venue":"binance",
			      "instrumentId":"okx:testnet:perp:USDT:BTC-USDT",
			      "venueEnvironment":"demo"
			    }]
			  }'::jsonb,
			  'principal-default','test','open','ready',1
			);
			insert into execution_legs(
			  leg_id,execution_id,role,account_id,instrument_id,side,
			  requested_quantity,filled_quantity,status,updated_at
			) values(
			  'exec-conflict:long','exec-conflict','long','acct',
			  'okx:testnet:perp:USDT:BTC-USDT','buy',1,1,'filled',now()
			)
		`); err != nil {
			t.Fatal(err)
		}
		control.Reset()
		cutoverPool, err := OpenCutoverPool(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer cutoverPool.Close()
		if _, err := NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx); !errors.Is(err, ErrMissingProvenance) {
			t.Fatalf("execution conflict error=%v", err)
		}
		assertColumnAbsent(t, ctx, control, "executions", "mode")
	})

	t.Run("funding observation", func(t *testing.T) {
		resetCutoverDatabase(t, ctx, control)
		applyLegacyMigrationsForTest(t, ctx, control)
		if _, err := control.Exec(ctx, `
			insert into funding_observations(
			  environment,account_id,instrument_id,funding_time,observed_at,rate
			) values(
			  'demo','acct','okx:testnet:perp:USDT:BTC-USDT',
			  '2026-07-30T08:00:00Z','2026-07-30T07:00:00Z',0.0001
			)
		`); err != nil {
			t.Fatal(err)
		}
		control.Reset()
		cutoverPool, err := OpenCutoverPool(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer cutoverPool.Close()
		if _, err := NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx); !errors.Is(err, ErrMissingProvenance) {
			t.Fatalf("funding conflict error=%v", err)
		}
		assertColumnAbsent(t, ctx, control, "funding_observations", "mode")
	})
}

func TestCutoverRunnerFencesOldApplicationSessionAndConcurrentTableLock(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)

	appPool, err := OpenApplicationPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := appPool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cutoverPool.Close()
	_, err = NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx)
	if !errors.Is(err, ErrActiveApplicationSessions) {
		t.Fatalf("active application session error=%v", err)
	}
	appPool.Close()

	tx, err := control.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "lock table executions in row exclusive mode"); err != nil {
		t.Fatal(err)
	}
	_, err = NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx)
	if !errors.Is(err, ErrCutoverBusy) {
		t.Fatalf("concurrent table lock error=%v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCutoverRunnerRejectsUnidentifiedIdleDatabaseSession(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	control.Reset()

	unknown := openNamedTestPool(t, ctx, dsn, "")
	defer unknown.Close()
	if err := unknown.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cutoverPool.Close()
	if _, err := NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx); !errors.Is(err, ErrCutoverBusy) {
		t.Fatalf("idle unidentified session error=%v, want ErrCutoverBusy", err)
	}
}

func TestCutoverTableLockFailsImmediatelyForInflightWrite(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	writer, err := control.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	if _, err := writer.Exec(ctx, "lock table executions in row exclusive mode"); err != nil {
		t.Fatal(err)
	}

	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cutoverPool.Close()
	tx, err := cutoverPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	started := time.Now()
	if err := lockCutoverTables(ctx, tx); !errors.Is(err, ErrCutoverBusy) {
		t.Fatalf("lock error=%v, want ErrCutoverBusy", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("NOWAIT cutover lock took %s", elapsed)
	}
}

func TestCutoverRunnerRollsBackWhenPostCopyValidationFails(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	seedSuccessfulCutover(t, ctx, control)
	control.Reset()

	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cutoverPool.Close()
	runner := NewCutoverRunner(cutoverPool, integrationCutoverProof())
	runner.afterCopy = func(context.Context, pgx.Tx) error {
		return errors.New("injected validation failure")
	}
	_, err = runner.Run(ctx)
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("Run error=%v, want ErrSchemaDrift", err)
	}
	assertColumnAbsent(t, ctx, control, "executions", "mode")
	assertColumnPresent(t, ctx, control, "executions", "environment")
	var versionFour int
	if err := control.QueryRow(ctx, "select count(*) from schema_migrations where version=4").Scan(&versionFour); err != nil {
		t.Fatal(err)
	}
	if versionFour != 0 {
		t.Fatal("validation failure committed version four")
	}
}

func TestCutoverRunnerRollsBackWhenSerializedRouteValidationFails(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	seedSuccessfulCutover(t, ctx, control)
	control.Reset()

	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cutoverPool.Close()
	runner := NewCutoverRunner(cutoverPool, integrationCutoverProof())
	runner.afterCopy = func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			update execution_fills
			set payload=payload-'route'
			where fill_id='exec-1:fill-1'
		`)
		return err
	}
	_, err = runner.Run(ctx)
	if !errors.Is(err, ErrMissingProvenance) {
		t.Fatalf("Run error=%v, want ErrMissingProvenance", err)
	}
	assertColumnAbsent(t, ctx, control, "executions", "mode")
	assertColumnPresent(t, ctx, control, "executions", "environment")
}

func TestCutoverRunnerRejectsHistoricalChecksumDriftAfterCutover(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	control.Reset()
	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cutoverPool.Close()
	if _, err := NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Exec(ctx, "update schema_migrations set checksum='drift' where version=2"); err != nil {
		t.Fatal(err)
	}
	control.Reset()
	if _, err := NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx); !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("rerun with historical drift error=%v", err)
	}
}

func TestCutoverStateStoreForbidsBlindRestoreAfterReopenOrWrite(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	defer control.Close()

	runFreshCutover := func(t *testing.T) *CutoverStateStore {
		t.Helper()
		resetCutoverDatabase(t, ctx, control)
		applyLegacyMigrationsForTest(t, ctx, control)
		control.Reset()
		cutoverPool, err := OpenCutoverPool(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cutoverPool.Close)
		if _, err := NewCutoverRunner(cutoverPool, integrationCutoverProof()).Run(ctx); err != nil {
			t.Fatal(err)
		}
		return NewCutoverStateStore(control)
	}

	t.Run("traffic reopened", func(t *testing.T) {
		store := runFreshCutover(t)
		if err := store.MarkTrafficReopened(ctx); err != nil {
			t.Fatal(err)
		}
		if err := store.AuthorizeRestore(ctx, "backup-integration-1"); !errors.Is(err, cutover.ErrRestoreForbidden) {
			t.Fatalf("restore after reopen error=%v", err)
		}
	})

	t.Run("new contract write accepted", func(t *testing.T) {
		store := runFreshCutover(t)
		if err := store.MarkWriteAccepted(ctx); err != nil {
			t.Fatal(err)
		}
		if err := store.AuthorizeRestore(ctx, "backup-integration-1"); !errors.Is(err, cutover.ErrRestoreForbidden) {
			t.Fatalf("restore after write error=%v", err)
		}
	})
}

func openNamedTestPool(t *testing.T, ctx context.Context, dsn, name string) *pgxpool.Pool {
	t.Helper()
	cfg, err := poolConfig(dsn, name)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func resetCutoverDatabase(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		drop table if exists cutover_control, schema_migrations, read_model_events,
		  funding_observations, execution_transitions, execution_fills, execution_legs,
		  execution_previews, audit_events, activities, positions, executions, alerts,
		  opportunities, idempotency_records, sessions, users cascade
	`); err != nil {
		t.Fatal(err)
	}
}

func assertCutoverRouteEvidence(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var (
		fillAccount, fillVenue, fillInstrument, fillEnvironment string
		transitionLongEnvironment, transitionShortEnvironment   string
		positionLongEnvironment, positionShortEnvironment       string
		idempotencyLongEnvironment, idempotencyShortEnvironment string
		auditLongEnvironment, auditShortEnvironment             string
		authAuditHasLegs                                        bool
	)
	if err := pool.QueryRow(ctx, `
		select
		  coalesce((select payload->'route'->>'accountId'
		            from execution_fills where fill_id='exec-1:fill-1'), ''),
		  coalesce((select payload->'route'->>'venue'
		            from execution_fills where fill_id='exec-1:fill-1'), ''),
		  coalesce((select payload->'route'->>'instrumentId'
		            from execution_fills where fill_id='exec-1:fill-1'), ''),
		  coalesce((select payload->'route'->>'venueEnvironment'
		            from execution_fills where fill_id='exec-1:fill-1'), ''),
		  coalesce((select payload->'legs'->0->>'venueEnvironment'
		            from execution_transitions where execution_id='exec-1'), ''),
		  coalesce((select payload->'legs'->1->>'venueEnvironment'
		            from execution_transitions where execution_id='exec-1'), ''),
		  coalesce((select payload->'longLeg'->>'venueEnvironment'
		            from positions where id='position-1'), ''),
		  coalesce((select payload->'shortLeg'->>'venueEnvironment'
		            from positions where id='position-1'), ''),
		  coalesce((select response_body->'legs'->0->>'venueEnvironment'
		            from idempotency_records where id='idem-1'), ''),
		  coalesce((select response_body->'legs'->1->>'venueEnvironment'
		            from idempotency_records where id='idem-1'), ''),
		  coalesce((select payload->'legs'->0->>'venueEnvironment'
		            from audit_events where action='execution.submit'), ''),
		  coalesce((select payload->'legs'->1->>'venueEnvironment'
		            from audit_events where action='execution.submit'), ''),
		  coalesce((select payload ? 'legs'
		            from audit_events where action='auth.login'), false)
	`).Scan(
		&fillAccount,
		&fillVenue,
		&fillInstrument,
		&fillEnvironment,
		&transitionLongEnvironment,
		&transitionShortEnvironment,
		&positionLongEnvironment,
		&positionShortEnvironment,
		&idempotencyLongEnvironment,
		&idempotencyShortEnvironment,
		&auditLongEnvironment,
		&auditShortEnvironment,
		&authAuditHasLegs,
	); err != nil {
		t.Fatal(err)
	}
	if fillAccount != "binance-demo" ||
		fillVenue != "binance" ||
		fillInstrument != "binance:demo:perp:USDT:BTC-USDT" ||
		fillEnvironment != "demo" {
		t.Fatalf(
			"fill route account=%q venue=%q instrument=%q environment=%q",
			fillAccount,
			fillVenue,
			fillInstrument,
			fillEnvironment,
		)
	}
	for surface, got := range map[string]string{
		"transition": transitionLongEnvironment + "," + transitionShortEnvironment,
		"position":   positionLongEnvironment + "," + positionShortEnvironment,
		"idempotency": idempotencyLongEnvironment + "," +
			idempotencyShortEnvironment,
		"audit": auditLongEnvironment + "," + auditShortEnvironment,
	} {
		if got != "demo,testnet" {
			t.Fatalf("%s native environments=%q", surface, got)
		}
	}
	if authAuditHasLegs {
		t.Fatal("authentication audit must not carry provider route evidence")
	}
}

func seedSuccessfulCutover(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		insert into users(id,email,password_hash,environment)
		values('principal-default','owner@example.com','hash','demo');
		insert into sessions(
		  id,user_id,token_digest,csrf_digest,expires_at
		) values(
		  'session-1','principal-default','token-digest','csrf-digest',now()+interval '1 hour'
		);
		insert into idempotency_records(
		  id,key,request_hash,response_code,response_body,principal_id,environment,
		  method,path,execution_id,state
		) values(
		  'idem-1','open-1','request-hash-1',200,
		  '{
		    "mode":"test",
		    "legs":[
		      {"role":"long","accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"},
		      {"role":"short","accountId":"okx-testnet","venue":"okx","instrumentId":"okx:testnet:perp:USDT:BTC-USDT","venueEnvironment":"testnet"}
		    ]
		  }'::jsonb,
		  'principal-default','demo','POST','/api/v1/orders','exec-1','completed'
		);
		insert into execution_previews(
		  id,snapshot_id,principal_id,environment,operation,payload,request_hash,
		  can_submit,expires_at
		) values(
		  'preview-1','snapshot-1','principal-default','demo','open',
		  '{"mode":"test"}'::jsonb,'preview-hash',true,now()+interval '1 hour'
		);
		insert into opportunities(id,symbol,status,payload,environment)
		values('opp-1','BTC-USDT','executable','{"mode":"test"}'::jsonb,'demo');
		insert into alerts(id,opportunity_id,status,payload,environment)
		values('alert-1','opp-1','open','{"mode":"test"}'::jsonb,'demo');
		insert into executions(
		  id,status,payload,principal_id,environment,operation_kind,opportunity_id,
		  lineage_id,idempotency_key,request_hash,reconciliation_state,
		  manual_intervention,version
		) values(
		  'exec-1','hedged',
		  '{
		    "id":"exec-1",
		    "principalId":"principal-default",
		    "mode":"test",
		    "plannedLegs":[
		      {"role":"long","accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"},
		      {"role":"short","accountId":"okx-testnet","venue":"okx","instrumentId":"okx:testnet:perp:USDT:BTC-USDT","venueEnvironment":"testnet"}
		    ],
		    "legs":[
		      {"role":"long","accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"},
		      {"role":"short","accountId":"okx-testnet","venue":"okx","instrumentId":"okx:testnet:perp:USDT:BTC-USDT","venueEnvironment":"testnet"}
		    ]
		  }'::jsonb,
		  'principal-default','demo','open','opp-1','lineage-1','open-1',
		  'request-hash-1','ready',false,1
		);
		insert into execution_legs(
		  leg_id,execution_id,role,account_id,instrument_id,side,requested_quantity,
		  filled_quantity,status,gateway_id,command_id,gateway_order_id,updated_at
		) values
		  ('exec-1:long','exec-1','long','binance-demo','binance:demo:perp:USDT:BTC-USDT','buy',5,5,'filled','gw-long','cmd-long','order-long',now()),
		  ('exec-1:short','exec-1','short','okx-testnet','okx:testnet:perp:USDT:BTC-USDT','sell',5,5,'filled','gw-short','cmd-short','order-short',now());
		insert into execution_fills(
		  fill_id,execution_id,leg_id,gateway_fill_id,quantity,price,fee,fee_currency,
		  occurred_at,payload
		) values(
		  'exec-1:fill-1','exec-1','exec-1:long','gateway-fill-1',5,100,0.25,'USDT',
		  now(),'{"venueEnvironment":"demo"}'::jsonb
		);
		insert into execution_transitions(
		  execution_id,principal_id,environment,from_status,to_status,kind,request_id,payload
		) values(
		  'exec-1','principal-default','demo','submitting','hedged','leg_filled',
		  'request-1','{"mode":"test","venueEnvironment":"demo"}'::jsonb
		);
		insert into positions(
		  id,symbol,payload,principal_id,environment,status
		) values(
		  'position-1','BTC-USDT',
		  '{
		    "lineageId":"lineage-1",
		    "openingExecutionId":"exec-1",
		    "mode":"test",
		    "longLeg":{"accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"},
		    "shortLeg":{"accountId":"okx-testnet","venue":"okx","instrumentId":"okx:testnet:perp:USDT:BTC-USDT","venueEnvironment":"testnet"}
		  }'::jsonb,
		  'principal-default','demo','open'
		);
		insert into activities(
		  id,type,payload,principal_id,environment,position_id,execution_id
		) values(
		  'activity-1','execution.hedged','{"mode":"test"}'::jsonb,
		  'principal-default','demo','position-1','exec-1'
		);
		insert into audit_events(
		  request_id,action,outcome,email,principal_id,environment,target_id,execution_id,payload
		) values(
		  'request-1','execution.submit','success','owner@example.com',
		  'principal-default','demo','exec-1','exec-1',
		  '{"mode":"test"}'::jsonb
		);
		insert into audit_events(
		  request_id,action,outcome,email,principal_id,environment,target_id,payload
		) values(
		  'request-auth','auth.login','success','owner@example.com',
		  'principal-default','demo','principal-default','{}'::jsonb
		);
		insert into funding_observations(
		  environment,account_id,instrument_id,funding_time,observed_at,rate
		) values
		  ('test','binance-demo','binance:demo:perp:USDT:BTC-USDT','2026-07-30T08:00:00Z','2026-07-30T07:00:00Z',0.0001),
		  ('test','okx-testnet','okx:testnet:perp:USDT:BTC-USDT','2026-07-30T08:00:00Z','2026-07-30T07:00:00Z',0.0002),
		  ('demo','shared-account','shared-instrument','2026-07-30T08:00:00Z','2026-07-30T07:00:00Z',0.0003),
		  ('testnet','shared-account','shared-instrument','2026-07-30T08:00:00Z','2026-07-30T07:00:00Z',0.0004);
		insert into read_model_events(
		  boot_id,principal_id,environment,kind,scope,entity_id,payload
		) values(
		  'old-boot','principal-default','demo','opportunity.upsert',
		  array['opportunities'],'opp-1','{"mode":"test"}'::jsonb
		);
	`); err != nil {
		t.Fatal(err)
	}
}

func seedPartialExecutionCutover(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		insert into idempotency_records(
		  id,key,request_hash,response_code,response_body,principal_id,environment,
		  method,path,execution_id,state
		) values(
		  'idem-partial','open-partial','request-hash-partial',202,
		  '{
		    "mode":"test",
		    "legs":[
		      {"role":"long","accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"}
		    ]
		  }'::jsonb,
		  'principal-default','demo','POST','/api/v1/orders','exec-partial','completed'
		);
		insert into executions(
		  id,status,payload,principal_id,environment,operation_kind,
		  lineage_id,idempotency_key,request_hash,reconciliation_state,
		  manual_intervention,version
		) values(
		  'exec-partial','failed',
		  '{
		    "id":"exec-partial",
		    "principalId":"principal-default",
		    "mode":"test",
		    "plannedLegs":[
		      {"role":"long","accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"},
		      {"role":"short","accountId":"okx-testnet","venue":"okx","instrumentId":"okx:testnet:perp:USDT:BTC-USDT","venueEnvironment":"testnet"}
		    ],
		    "legs":[
		      {"role":"long","accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"}
		    ]
		  }'::jsonb,
		  'principal-default','demo','open','lineage-partial','open-partial',
		  'request-hash-partial','ready',true,1
		);
		insert into execution_legs(
		  leg_id,execution_id,role,account_id,instrument_id,side,requested_quantity,
		  filled_quantity,status,gateway_id,command_id,gateway_order_id,updated_at
		) values(
		  'exec-partial:long','exec-partial','long','binance-demo',
		  'binance:demo:perp:USDT:BTC-USDT','buy',5,5,'filled',
		  'gw-partial-long','cmd-partial-long','order-partial-long',now()
		);
		insert into execution_transitions(
		  execution_id,principal_id,environment,from_status,to_status,kind,request_id,payload
		) values(
		  'exec-partial','principal-default','demo','submitting','failed',
		  'leg_filled','request-partial',
		  '{"mode":"test","legs":[
		    {"role":"long","accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"}
		  ]}'::jsonb
		);
		insert into positions(
		  id,symbol,payload,principal_id,environment,status
		) values(
		  'position-partial','BTC-USDT',
		  '{
		    "lineageId":"lineage-partial",
		    "openingExecutionId":"exec-partial",
		    "mode":"test",
		    "longLeg":{"accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"},
		    "shortLeg":{"accountId":"okx-testnet","venue":"okx","instrumentId":"okx:testnet:perp:USDT:BTC-USDT","venueEnvironment":"testnet"}
		  }'::jsonb,
		  'principal-default','demo','closed'
		);
		insert into audit_events(
		  request_id,action,outcome,email,principal_id,environment,target_id,execution_id,payload
		) values(
		  'request-partial','execution.submit','failed','owner@example.com',
		  'principal-default','demo','exec-partial','exec-partial',
		  '{"mode":"test","legs":[
		    {"role":"long","accountId":"binance-demo","venue":"binance","instrumentId":"binance:demo:perp:USDT:BTC-USDT","venueEnvironment":"demo"}
		  ]}'::jsonb
		);
	`); err != nil {
		t.Fatal(err)
	}
}

func integrationCutoverProof() cutover.Proof {
	return cutover.Proof{
		BackupID:            "backup-integration-1",
		BackupSHA256:        strings.Repeat("c", 64),
		BackupVerifiedAt:    time.Date(2026, 7, 30, 7, 0, 0, 0, time.UTC),
		OldServiceStopped:   true,
		AutoRestartDisabled: true,
	}
}

func assertColumnPresent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, column string) {
	t.Helper()
	var present bool
	if err := pool.QueryRow(ctx, `
		select exists(
		  select 1 from information_schema.columns
		  where table_schema='public' and table_name=$1 and column_name=$2
		)
	`, table, column).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatalf("expected %s.%s to exist", table, column)
	}
}

func assertColumnAbsent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, column string) {
	t.Helper()
	var present bool
	if err := pool.QueryRow(ctx, `
		select exists(
		  select 1 from information_schema.columns
		  where table_schema='public' and table_name=$1 and column_name=$2
		)
	`, table, column).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatalf("expected %s.%s to be absent", table, column)
	}
}
