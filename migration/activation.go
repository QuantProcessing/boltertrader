package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	storepkg "github.com/QuantProcessing/boltertrader/gateway/store"
	gatewaypostgres "github.com/QuantProcessing/boltertrader/gateway/store/postgres"
	"github.com/QuantProcessing/boltertrader/migration/authority"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ActivationBoundary string

const (
	ActivationBegun           ActivationBoundary = "begun"
	ActivationGatewayPromoted ActivationBoundary = "gateway-promoted"
	ActivationWritersGranted  ActivationBoundary = "writers-granted"
	ActivationConnectRestored ActivationBoundary = "connect-restored"
	ActivationServicesStarted ActivationBoundary = "services-started"
	ActivationVerified        ActivationBoundary = "verified"
)

type ActivationAttempt struct {
	ID                      string                                  `json:"id"`
	InputSHA256             string                                  `json:"inputSha256"`
	ExpectedPromotionSHA256 string                                  `json:"expectedPromotionSha256"`
	Boundary                ActivationBoundary                      `json:"boundary"`
	StartedAt               time.Time                               `json:"startedAt"`
	UpdatedAt               time.Time                               `json:"updatedAt"`
	GatewayBeforeSHA256     string                                  `json:"gatewayBeforeSha256,omitempty"`
	GatewayAfterSHA256      string                                  `json:"gatewayAfterSha256,omitempty"`
	JournalSHA256           string                                  `json:"journalSha256,omitempty"`
	EvidencePath            string                                  `json:"evidencePath,omitempty"`
	ProofSHA256             string                                  `json:"proofSha256,omitempty"`
	PriorVerification       *ActivationPriorVerificationObservation `json:"priorVerification,omitempty"`
	PreGrantObservation     *ActivationPreGrantObservation          `json:"preGrantObservation,omitempty"`
	GatewayBeforeHead       *ActivationGatewayHeadObservation       `json:"gatewayBeforeHead,omitempty"`
	GatewayPromotion        *ActivationGatewayPromotionObservation  `json:"gatewayPromotion,omitempty"`
	Observations            *ActivationObservations                 `json:"observations,omitempty"`
}

type ActivationAuthorityObservation struct {
	MatrixSHA256                string   `json:"matrixSha256"`
	MigrationSHA256             string   `json:"migrationSha256"`
	RuntimeRole                 string   `json:"runtimeRole"`
	RuntimeInherits             bool     `json:"runtimeInherits"`
	WriterMembership            bool     `json:"writerMembership"`
	FullCapabilities            bool     `json:"fullCapabilities"`
	EffectiveTablePrivileges    bool     `json:"effectiveTablePrivileges"`
	EffectiveSequencePrivileges bool     `json:"effectiveSequencePrivileges"`
	EffectiveRoutinePrivileges  bool     `json:"effectiveRoutinePrivileges"`
	OwnershipViolations         []string `json:"ownershipViolations,omitempty"`
	PrivilegeViolations         []string `json:"privilegeViolations,omitempty"`
}

type ActivationObservations struct {
	SchemaVersion               int                                    `json:"schemaVersion"`
	InputSHA256                 string                                 `json:"inputSha256"`
	Source                      DatabaseIdentity                       `json:"source"`
	Platform                    DatabaseIdentity                       `json:"platform"`
	Gateway                     DatabaseIdentity                       `json:"gateway"`
	Maintenance                 DatabaseIdentity                       `json:"maintenance"`
	PlatformWriterRole          string                                 `json:"platformWriterRole"`
	PlatformRuntimeRole         string                                 `json:"platformRuntimeRole"`
	GatewayWriterRole           string                                 `json:"gatewayWriterRole"`
	GatewayRuntimeRole          string                                 `json:"gatewayRuntimeRole"`
	SourceWriterRole            string                                 `json:"sourceWriterRole"`
	PlatformMigratorRole        string                                 `json:"platformMigratorRole"`
	GatewayMigratorRole         string                                 `json:"gatewayMigratorRole"`
	GatewayBeforeSHA256         string                                 `json:"gatewayBeforeSha256"`
	GatewayAfterSHA256          string                                 `json:"gatewayAfterSha256"`
	GatewayPromotion            ActivationGatewayPromotionObservation  `json:"gatewayPromotion"`
	PriorVerification           ActivationPriorVerificationObservation `json:"priorVerification"`
	PreGrantPlatformAuthority   ActivationAuthorityObservation         `json:"preGrantPlatformAuthority"`
	PreGrantGatewayAuthority    ActivationAuthorityObservation         `json:"preGrantGatewayAuthority"`
	PreGrantDMLProbeSHA256      string                                 `json:"preGrantDmlProbeSha256"`
	PlatformAuthority           ActivationAuthorityObservation         `json:"platformAuthority"`
	GatewayAuthority            ActivationAuthorityObservation         `json:"gatewayAuthority"`
	PostGrantDMLProbeSHA256     string                                 `json:"postGrantDmlProbeSha256"`
	SourceFenceVerified         bool                                   `json:"sourceFenceVerified"`
	TargetSessionsFenced        bool                                   `json:"targetSessionsFenced"`
	TargetSessionFenceSHA256    string                                 `json:"targetSessionFenceSha256"`
	RestoredTargetSessionsZero  bool                                   `json:"restoredTargetSessionsZero"`
	RestoredTargetSessionSHA256 string                                 `json:"restoredTargetSessionSha256"`
	PostgresIdentitySHA256      string                                 `json:"postgresIdentitySha256"`
	SupervisorStatusSHA256      string                                 `json:"supervisorStatusSha256"`
	SupervisorTopologySHA256    string                                 `json:"supervisorTopologySha256,omitempty"`
	TestModeVerified            bool                                   `json:"testModeVerified"`
	LivePrivateWritesDisabled   bool                                   `json:"livePrivateWritesDisabled"`
	VenueWritesDisabled         bool                                   `json:"venueWritesDisabled"`
}

type DatabaseIdentity struct {
	Service  string `json:"service"`
	Database string `json:"database"`
	Role     string `json:"role"`
}

type ActivationPriorEvent struct {
	Sequence       int       `json:"sequence"`
	Epoch          int       `json:"epoch"`
	Operation      Operation `json:"operation"`
	Name           string    `json:"name,omitempty"`
	EvidencePath   string    `json:"evidencePath"`
	ProofSHA256    string    `json:"proofSha256"`
	ArtifactPath   string    `json:"artifactPath,omitempty"`
	ArtifactSHA256 string    `json:"artifactSha256,omitempty"`
	InputSHA256    string    `json:"inputSha256,omitempty"`
}

type ActivationPriorVerificationPlan struct {
	SchemaVersion       int                    `json:"schemaVersion"`
	MigrationID         string                 `json:"migrationId"`
	ManifestStateSHA256 string                 `json:"manifestStateSha256"`
	Events              []ActivationPriorEvent `json:"events"`
}

type ActivationPriorVerificationObservation struct {
	AttemptID                   string `json:"attemptId"`
	PlanSHA256                  string `json:"planSha256"`
	ArtifactSetSHA256           string `json:"artifactSetSha256"`
	CurrentPostconditionsSHA256 string `json:"currentPostconditionsSha256"`
}

type ActivationPreGrantObservation struct {
	Platform       ActivationAuthorityObservation `json:"platform"`
	Gateway        ActivationAuthorityObservation `json:"gateway"`
	DMLProbeSHA256 string                         `json:"dmlProbeSha256"`
}

type ActivationGatewayHeadObservation struct {
	PendingSHA256      string `json:"pendingSha256"`
	ActiveSHA256       string `json:"activeSha256"`
	OperationSetSHA256 string `json:"operationSetSha256"`
	HeadSHA256         string `json:"headSha256"`
}

type ActivationGatewayPromotionObservation struct {
	Before ActivationGatewayHeadObservation `json:"before"`
	After  ActivationGatewayHeadObservation `json:"after"`
}

type ActivationTargetFenceObservation struct {
	SessionsZero bool   `json:"sessionsZero"`
	ProbeSHA256  string `json:"probeSha256"`
}

type ActivationRestoredTargetSessionsObservation struct {
	SessionsZero bool   `json:"sessionsZero"`
	ProbeSHA256  string `json:"probeSha256"`
}

type ActivationPostGrantObservation struct {
	Platform           ActivationAuthorityObservation `json:"platform"`
	Gateway            ActivationAuthorityObservation `json:"gateway"`
	TargetSessionsZero bool                           `json:"targetSessionsZero"`
	DMLProbeSHA256     string                         `json:"dmlProbeSha256"`
}

type ActivationRunningPostGrantDMLObservation struct {
	PlatformPassed bool   `json:"platformPassed"`
	GatewayPassed  bool   `json:"gatewayPassed"`
	ProbeSHA256    string `json:"probeSha256"`
}

type ActivationVerificationFacts struct {
	Prior               ActivationPriorVerificationObservation      `json:"prior"`
	PreGrant            ActivationPreGrantObservation               `json:"preGrant"`
	GatewayPromotion    ActivationGatewayPromotionObservation       `json:"gatewayPromotion"`
	Fence               ActivationTargetFenceObservation            `json:"fence"`
	PostGrant           ActivationPostGrantObservation              `json:"postGrant"`
	RestoredSessions    ActivationRestoredTargetSessionsObservation `json:"restoredSessions"`
	RunningPostGrantDML ActivationRunningPostGrantDMLObservation    `json:"runningPostGrantDml"`
}

type ActivationInput struct {
	MigrationID, ManifestPath, EvidenceDir, JournalPath                             string
	Source, Platform, Gateway, Maintenance                                          DatabaseIdentity
	SourceWriterRole, PlatformMigratorRole, PlatformRuntimeRole, PlatformWriterRole string
	GatewayMigratorRole, GatewayRuntimeRole, GatewayWriterRole                      string
	PriorVerification                                                               ActivationPriorVerificationPlan
	ManifestActivated                                                               bool `json:"-"`
}

// ActivationDependencies keeps the irreversible operation injectable and
// makes every completed boundary independently re-verifiable on replay.
type ActivationDependencies struct {
	Now                          func() time.Time
	LoadAttempt                  func(context.Context) (*ActivationAttempt, error)
	SaveAttempt                  func(context.Context, ActivationAttempt) error
	ExpectedPromotion            func(context.Context) (string, string, error)
	VerifyPriorState             func(context.Context, ActivationPriorVerificationPlan, ActivationAttempt) (ActivationPriorVerificationObservation, error)
	RecoverPriorState            func(context.Context, ActivationPriorVerificationPlan, ActivationAttempt) (ActivationPriorVerificationObservation, error)
	VerifySourceFence            func(context.Context) error
	VerifyPreGrantAuthority      func(context.Context, bool) (ActivationPreGrantObservation, error)
	StopServices                 func(context.Context) error
	ObserveGatewayPromotionHead  func(context.Context) (ActivationGatewayHeadObservation, error)
	PromoteGateway               func(context.Context) (ActivationGatewayPromotionObservation, error)
	VerifyGatewayPromotion       func(context.Context) (ActivationGatewayHeadObservation, error)
	FenceTargetConnect           func(context.Context) (ActivationTargetFenceObservation, error)
	GrantWriters                 func(context.Context) error
	VerifyPostGrantAuthority     func(context.Context) (ActivationPostGrantObservation, error)
	RestoreConnect               func(context.Context) error
	VerifyRestoredTargetSessions func(context.Context) (ActivationRestoredTargetSessionsObservation, error)
	StartServices                func(context.Context) error
	VerifyRunningPostGrantDML    func(context.Context) (ActivationRunningPostGrantDMLObservation, error)
	VerifyActivation             func(context.Context) error
	ContainPostGrantFailure      func(context.Context) error
	ObserveActivation            func(context.Context, ActivationInput, ActivationAttempt, string, ActivationVerificationFacts) (ActivationObservations, error)
	CompleteVerifiedAttempt      func(context.Context, ActivationInput, ActivationAttempt, string) (ExecutionResult, error)
}

type verifiedMigrationPromotion struct {
	Request            storepkg.MigrationPromotionRequest
	OperationSetSHA256 string
}

type migrationPromotionStore interface {
	PromoteMigrationPendingSet(context.Context, storepkg.MigrationPromotionRequest) (storepkg.MigrationPromotionResult, error)
	ListRuntimeStateForRecovery(context.Context) ([]storepkg.RuntimeState, error)
}

var openMigrationPromotionStore = func(ctx context.Context, service, database, role string) (migrationPromotionStore, func(), error) {
	pool, err := pgxpool.New(ctx, "service="+service+" dbname="+database)
	if err != nil {
		return nil, nil, fmt.Errorf("connect Gateway migration store")
	}
	var currentDatabase, currentRole string
	if err := pool.QueryRow(ctx, `SELECT current_database(),current_user`).Scan(&currentDatabase, &currentRole); err != nil || currentDatabase != database || currentRole != role {
		pool.Close()
		return nil, nil, fmt.Errorf("Gateway promotion identity mismatch")
	}
	return gatewaypostgres.NewStore(pool), pool.Close, nil
}

var acquireActivationPostgresLock = func(ctx context.Context, request ExecutionRequest) (func(context.Context) error, error) {
	pool, err := pgxpool.New(ctx, "service="+request.MaintenanceService+" dbname="+request.MaintenanceDatabase)
	if err != nil {
		return nil, fmt.Errorf("connect activation PostgreSQL lock")
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("acquire activation PostgreSQL lock connection")
	}
	closeConnection := func() {
		connection.Release()
		pool.Close()
	}
	var currentRole, currentDatabase string
	if err := connection.QueryRow(ctx, `SELECT current_user,current_database()`).Scan(&currentRole, &currentDatabase); err != nil || currentRole != request.MaintenanceRole || currentDatabase != request.MaintenanceDatabase {
		closeConnection()
		return nil, fmt.Errorf("activation PostgreSQL lock identity mismatch")
	}
	const lockKey = "G010:test:activation"
	var acquired bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, lockKey).Scan(&acquired); err != nil || !acquired {
		closeConnection()
		return nil, fmt.Errorf("activation PostgreSQL lock is held")
	}
	return func(releaseCtx context.Context) error {
		defer closeConnection()
		var unlocked bool
		if err := connection.QueryRow(releaseCtx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lockKey).Scan(&unlocked); err != nil || !unlocked {
			return fmt.Errorf("release activation PostgreSQL lock")
		}
		return nil
	}, nil
}

var verifyActivationAuthorityCatalogs = func(ctx context.Context, request ExecutionRequest) ([2]ActivationAuthorityObservation, error) {
	var observations [2]ActivationAuthorityObservation
	checks := []struct {
		database string
		migrator string
		runtime  string
		writer   string
		matrix   authority.Matrix
		version  string
		ledger   string
	}{
		{request.TargetDatabase, request.TargetRole, request.TargetRuntimeRole, request.TargetWriterRole, authority.PlatformV15(), `SELECT COALESCE(max(version),0) FROM platform_schema_migrations`, `SELECT version,checksum FROM platform_schema_migrations ORDER BY version`},
		{request.GatewayDatabase, request.GatewayRole, request.GatewayRuntimeRole, request.GatewayWriterRole, authority.GatewayV6(), `SELECT COALESCE(max(version),0) FROM schema_migrations`, `SELECT version,checksum FROM schema_migrations ORDER BY version`},
	}
	for _, check := range checks {
		index := 0
		if check.matrix.Name == authority.GatewayV6().Name {
			index = 1
		}
		if check.migrator != check.matrix.Roles.Migrator || check.runtime != check.matrix.Roles.Runtime || check.writer != check.matrix.Roles.Writer || request.MaintenanceRole != check.matrix.Roles.Maintenance {
			return observations, fmt.Errorf("activation authority identity does not match canonical Test matrix")
		}
		pool, err := pgxpool.New(ctx, "service="+request.MaintenanceService+" dbname="+check.database)
		if err != nil {
			return observations, fmt.Errorf("connect activation authority catalog")
		}
		observation, verifyErr := verifyActivationAuthorityCatalog(ctx, pool, request.MaintenanceRole, check.database, check.runtime, check.matrix, check.version, check.ledger)
		pool.Close()
		if verifyErr != nil {
			return observations, verifyErr
		}
		observations[index] = observation
	}
	return observations, nil
}

func verifyActivationAuthorityCatalog(ctx context.Context, pool *pgxpool.Pool, maintenanceRole, database, runtimeRole string, matrix authority.Matrix, schemaVersionQuery, ledgerQuery string) (ActivationAuthorityObservation, error) {
	var currentRole, currentDatabase string
	if err := pool.QueryRow(ctx, `SELECT current_user,current_database()`).Scan(&currentRole, &currentDatabase); err != nil || currentRole != maintenanceRole || currentDatabase != database {
		return ActivationAuthorityObservation{}, fmt.Errorf("activation authority connection identity mismatch")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ActivationAuthorityObservation{}, fmt.Errorf("begin activation authority verification")
	}
	return verifyActivationAuthorityCatalogTransaction(ctx, activationCatalogQuerier{tx: tx}, maintenanceRole, database, runtimeRole, matrix, schemaVersionQuery, ledgerQuery)
}

type activationAuthorityTransaction interface {
	authority.CatalogQuerier
	Exec(context.Context, string, ...any) error
	Rollback(context.Context) error
}

func verifyActivationAuthorityCatalogTransaction(ctx context.Context, tx activationAuthorityTransaction, maintenanceRole, database, runtimeRole string, matrix authority.Matrix, schemaVersionQuery, ledgerQuery string) (ActivationAuthorityObservation, error) {
	defer func() { _ = tx.Rollback(ctx) }()
	membershipQuery := `
		SELECT membership.admin_option,membership.inherit_option,membership.set_option
		FROM pg_auth_members membership
		JOIN pg_roles granted ON granted.oid=membership.roleid
		JOIN pg_roles member ON member.oid=membership.member
		WHERE granted.rolname=$1 AND member.rolname=$2`
	var adminOption, inheritOption, setOption bool
	if err := tx.QueryRow(ctx, membershipQuery, matrix.Roles.Migrator, maintenanceRole).Scan(&adminOption, &inheritOption, &setOption); err != nil || adminOption || inheritOption || !setOption {
		return ActivationAuthorityObservation{}, fmt.Errorf("activation maintenance migrator membership is not canonical")
	}
	if err := tx.Exec(ctx, `SET LOCAL ROLE `+quotePostgresIdentifier(matrix.Roles.Migrator)); err != nil {
		return ActivationAuthorityObservation{}, fmt.Errorf("assume activation migrator authority")
	}
	if err := tx.Exec(ctx, `GRANT CONNECT ON DATABASE `+quotePostgresIdentifier(database)+` TO `+quotePostgresIdentifier(runtimeRole)); err != nil {
		return ActivationAuthorityObservation{}, fmt.Errorf("stage activation authority CONNECT verification")
	}
	if err := tx.Exec(ctx, `RESET ROLE`); err != nil {
		return ActivationAuthorityObservation{}, fmt.Errorf("restore activation maintenance authority")
	}
	if err := tx.QueryRow(ctx, membershipQuery, runtimeRole, maintenanceRole).Scan(&adminOption, &inheritOption, &setOption); err != nil || !adminOption || inheritOption || setOption {
		return ActivationAuthorityObservation{}, fmt.Errorf("activation maintenance runtime membership is not canonical")
	}
	if err := tx.QueryRow(ctx, membershipQuery, matrix.Roles.Writer, maintenanceRole).Scan(&adminOption, &inheritOption, &setOption); err != nil || !adminOption || inheritOption || setOption {
		return ActivationAuthorityObservation{}, fmt.Errorf("activation maintenance writer membership is not canonical")
	}
	if err := tx.Exec(ctx, `GRANT `+quotePostgresIdentifier(runtimeRole)+` TO `+quotePostgresIdentifier(maintenanceRole)+` WITH SET TRUE`); err != nil {
		return ActivationAuthorityObservation{}, fmt.Errorf("stage activation runtime SET verification")
	}
	if err := tx.Exec(ctx, `SET LOCAL ROLE `+quotePostgresIdentifier(runtimeRole)); err != nil {
		return ActivationAuthorityObservation{}, fmt.Errorf("assume activation runtime authority")
	}
	migrationDigest, err := readVerifiedActivationLedger(ctx, tx, matrix, ledgerQuery)
	if err != nil {
		return ActivationAuthorityObservation{}, err
	}
	snapshot, err := authority.CollectPostgresRuntimeCatalog(ctx, tx, matrix, schemaVersionQuery, migrationDigest)
	if err != nil {
		return ActivationAuthorityObservation{}, err
	}
	result, err := authority.VerifyRuntimeCatalog(matrix, snapshot)
	if err != nil {
		return ActivationAuthorityObservation{}, fmt.Errorf("activation authority catalog rejected: %w", err)
	}
	return ActivationAuthorityObservation{
		MatrixSHA256: result.MatrixDigest, MigrationSHA256: result.MigrationDigest, RuntimeRole: result.RuntimeRole,
		RuntimeInherits: result.RuntimeInherits, WriterMembership: result.WriterMembership,
		FullCapabilities:         result.FullCapabilityGrants,
		EffectiveTablePrivileges: result.EffectiveTablePrivileges, EffectiveSequencePrivileges: result.EffectiveSequencePrivileges, EffectiveRoutinePrivileges: result.EffectiveRoutinePrivileges,
		OwnershipViolations: append([]string(nil), result.OwnershipViolations...), PrivilegeViolations: append([]string(nil), result.PrivilegeViolations...),
	}, nil
}

func readVerifiedActivationLedger(ctx context.Context, querier authority.CatalogQuerier, matrix authority.Matrix, query string) (string, error) {
	rows, err := querier.Query(ctx, query)
	if err != nil {
		return "", fmt.Errorf("AUTHORITY_CATALOG_QUERY_FAILED: migration-ledger")
	}
	defer rows.Close()
	entries := make([]authority.MigrationLedgerEntry, 0, matrix.SchemaVersion)
	for rows.Next() {
		var entry authority.MigrationLedgerEntry
		if err := rows.Scan(&entry.Version, &entry.Checksum); err != nil {
			return "", fmt.Errorf("AUTHORITY_CATALOG_QUERY_FAILED: migration-ledger")
		}
		entries = append(entries, entry)
	}
	if rows.Err() != nil {
		return "", fmt.Errorf("AUTHORITY_CATALOG_QUERY_FAILED: migration-ledger")
	}
	digest, err := authority.VerifyMigrationLedger(matrix, entries)
	if err != nil {
		return "", err
	}
	return digest, nil
}

type activationCatalogQuerier struct{ tx pgx.Tx }

var writeActivationManifestAtomic = writeManifestAtomic

func (querier activationCatalogQuerier) QueryRow(ctx context.Context, query string, args ...any) authority.CatalogRow {
	return querier.tx.QueryRow(ctx, query, args...)
}

func (querier activationCatalogQuerier) Query(ctx context.Context, query string, args ...any) (authority.CatalogRows, error) {
	return querier.tx.Query(ctx, query, args...)
}

func (querier activationCatalogQuerier) Exec(ctx context.Context, query string, args ...any) error {
	_, err := querier.tx.Exec(ctx, query, args...)
	return err
}

func (querier activationCatalogQuerier) Rollback(ctx context.Context) error {
	return querier.tx.Rollback(ctx)
}

func quotePostgresIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func activationCanonicalDMLProbe(matrix authority.Matrix, expectAllowed bool) string {
	var script strings.Builder
	script.WriteString(`BEGIN;
SELECT (
  current_user=:'maintenance_role'
  AND EXISTS (
    SELECT 1
    FROM pg_auth_members membership
    JOIN pg_roles granted ON granted.oid=membership.roleid
    JOIN pg_roles member ON member.oid=membership.member
    WHERE granted.rolname=:'runtime_role'
      AND member.rolname=:'maintenance_role'
      AND membership.admin_option
      AND NOT membership.inherit_option
      AND NOT membership.set_option
  )
) AS activation_membership_ok
\gset
\if :activation_membership_ok
\else
DO $activation_membership_guard$
BEGIN
  RAISE EXCEPTION 'activation maintenance runtime membership is not canonical';
END
$activation_membership_guard$;
\endif
GRANT :"runtime_role" TO :"maintenance_role" WITH SET TRUE;
SET LOCAL ROLE :"runtime_role";
`)
	for _, object := range matrix.Objects {
		if object.Kind != authority.Table || object.Class != authority.Canonical {
			continue
		}
		qualified := object.Schema + "." + object.Name
		probes := []struct {
			declaration string
			action      string
		}{
			{
				declaration: "DECLARE columns text;",
				action: `SELECT string_agg(quote_ident(attname),',' ORDER BY attnum) INTO columns
FROM pg_attribute WHERE attrelid='` + qualified + `'::regclass AND attnum>0 AND NOT attisdropped AND attgenerated='' AND attidentity<>'a';
IF columns IS NULL THEN RAISE EXCEPTION 'no insertable columns for ` + qualified + `'; END IF;
EXECUTE 'INSERT INTO ` + qualified + ` (' || columns || ') SELECT ' || columns || ' FROM ` + qualified + ` WHERE false';`,
			},
			{action: `EXECUTE 'DELETE FROM ` + qualified + ` WHERE false';`},
			{
				declaration: "DECLARE column_name text;",
				action: `SELECT quote_ident(attname) INTO column_name
FROM pg_attribute WHERE attrelid='` + qualified + `'::regclass AND attnum>0 AND NOT attisdropped AND attgenerated='' AND attidentity='' ORDER BY attnum LIMIT 1;
IF column_name IS NULL THEN RAISE EXCEPTION 'no updateable columns for ` + qualified + `'; END IF;
EXECUTE 'UPDATE ` + qualified + ` SET ' || column_name || '=' || column_name || ' WHERE false';`,
			},
		}
		for index, probe := range probes {
			tag := fmt.Sprintf("activation_dml_%s_%d", object.Name, index)
			script.WriteString("DO $" + tag + "$\n")
			if probe.declaration != "" {
				script.WriteString(probe.declaration + "\n")
			}
			script.WriteString("BEGIN\n")
			if expectAllowed {
				script.WriteString("  " + strings.ReplaceAll(probe.action, "\n", "\n  ") + "\n")
			} else {
				script.WriteString("  BEGIN\n    " + strings.ReplaceAll(probe.action, "\n", "\n    ") + "\n    RAISE EXCEPTION 'zero-row DML unexpectedly allowed for " + qualified + "';\n  EXCEPTION WHEN insufficient_privilege THEN NULL;\n  END;\n")
			}
			script.WriteString("END\n$" + tag + "$;\n")
		}
	}
	script.WriteString("SELECT true::text;\nROLLBACK;\n")
	return script.String()
}

func activationMeasuredProbeSHA(commands []Command, outputs []string) string {
	evidence := make([]commandEvidence, len(commands))
	for index := range commands {
		var output []byte
		if index < len(outputs) {
			output = []byte(outputs[index])
		}
		evidence[index] = commandDigest(commands[index], output)
	}
	data, _ := json.Marshal(evidence)
	return sha256Hex(data)
}

func activationSemanticPassSHA(commands []Command) string {
	type semanticPassFact struct {
		Sequence   int    `json:"sequence"`
		Executable string `json:"executable"`
		ArgvSHA256 string `json:"argvSha256"`
		InputSHA   string `json:"inputSha256,omitempty"`
		Passed     bool   `json:"passed"`
	}
	facts := make([]semanticPassFact, len(commands))
	for index, command := range commands {
		identity := commandDigest(command, nil)
		facts[index] = semanticPassFact{Sequence: index + 1, Executable: identity.Executable, ArgvSHA256: identity.ArgvSHA256, InputSHA: identity.InputSHA, Passed: true}
	}
	data, _ := json.Marshal(facts)
	return sha256Hex(data)
}

func ExecuteActivation(ctx context.Context, input ActivationInput, dependencies ActivationDependencies) (ExecutionResult, error) {
	postGrant := input.ManifestActivated
	fail := func(stage string, cause error) (ExecutionResult, error) {
		failure := fmt.Errorf("activation %s failed: %w", stage, cause)
		if postGrant && dependencies.ContainPostGrantFailure != nil {
			if containErr := dependencies.ContainPostGrantFailure(ctx); containErr != nil {
				return ExecutionResult{}, errors.Join(failure, fmt.Errorf("activation containment failed: %w", containErr))
			}
		}
		return ExecutionResult{}, failure
	}
	if err := validateActivationInput(input); err != nil {
		return fail("input validation", err)
	}
	if dependencies.LoadAttempt == nil || dependencies.SaveAttempt == nil || dependencies.ExpectedPromotion == nil ||
		dependencies.VerifyPriorState == nil || dependencies.RecoverPriorState == nil || dependencies.VerifySourceFence == nil || dependencies.VerifyPreGrantAuthority == nil || dependencies.StopServices == nil || dependencies.PromoteGateway == nil ||
		dependencies.ObserveGatewayPromotionHead == nil || dependencies.VerifyGatewayPromotion == nil || dependencies.FenceTargetConnect == nil || dependencies.GrantWriters == nil || dependencies.VerifyPostGrantAuthority == nil ||
		dependencies.RestoreConnect == nil || dependencies.VerifyRestoredTargetSessions == nil || dependencies.StartServices == nil || dependencies.VerifyRunningPostGrantDML == nil || dependencies.VerifyActivation == nil || dependencies.ContainPostGrantFailure == nil || dependencies.ObserveActivation == nil || dependencies.CompleteVerifiedAttempt == nil {
		return fail("dependency validation", fmt.Errorf("activation dependencies are incomplete"))
	}
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	inputSHA, err := activationInputSHA(input)
	if err != nil {
		return fail("input digest", err)
	}
	planSHA, err := activationPriorPlanSHA(input.PriorVerification)
	if err != nil {
		return fail("prior state verification", err)
	}
	attempt, err := dependencies.LoadAttempt(ctx)
	if err != nil {
		return fail("load activation attempt", err)
	}
	if attempt != nil && boundaryAtLeast(attempt.Boundary, ActivationWritersGranted) {
		postGrant = true
	}
	beforeSHA, expectedSHA, err := dependencies.ExpectedPromotion(ctx)
	if err != nil || !validSHA256(beforeSHA) || !validSHA256(expectedSHA) {
		if err == nil {
			err = fmt.Errorf("typed promotion snapshot is incomplete")
		}
		return fail("promotion snapshot", err)
	}
	newAttempt := attempt == nil
	if attempt == nil {
		orphan, orphanErr := recoverOrphanInitialActivationJournal(input.JournalPath, inputSHA, expectedSHA, planSHA)
		if orphanErr != nil {
			return fail("journal verification", orphanErr)
		}
		if orphan != nil {
			attempt = orphan
			newAttempt = false
		} else {
			stamp := now().UTC()
			idSeed := sha256.Sum256([]byte(inputSHA + "\x00" + stamp.Format(time.RFC3339Nano)))
			attempt = &ActivationAttempt{ID: hex.EncodeToString(idSeed[:16]), InputSHA256: inputSHA, ExpectedPromotionSHA256: expectedSHA, Boundary: ActivationBegun, StartedAt: stamp, UpdatedAt: stamp}
		}
	}
	if !newAttempt {
		if attempt.InputSHA256 != inputSHA || attempt.ExpectedPromotionSHA256 != expectedSHA {
			return fail("attempt input verification", fmt.Errorf("activation attempt input changed"))
		}
		journalAhead, journalErr := verifyActivationJournal(input.JournalPath, attempt.JournalSHA256, *attempt, planSHA)
		if journalErr != nil {
			return fail("journal verification", journalErr)
		}
		if journalAhead != nil && (attempt.PriorVerification == nil || journalAhead.Boundary == attempt.Boundary) {
			*attempt = *journalAhead
		}
	}
	var priorObservation ActivationPriorVerificationObservation
	legacyPriorRecovery := !newAttempt && attempt.PriorVerification == nil
	if newAttempt {
		priorObservation, err = dependencies.VerifyPriorState(ctx, input.PriorVerification, *attempt)
		if err != nil {
			return fail("prior state verification", err)
		}
	} else if attempt.PriorVerification != nil {
		priorObservation = *attempt.PriorVerification
		rebuilt, rebuildErr := dependencies.RecoverPriorState(ctx, input.PriorVerification, *attempt)
		if rebuildErr != nil || !reflect.DeepEqual(rebuilt, priorObservation) {
			if rebuildErr == nil {
				rebuildErr = fmt.Errorf("persisted prior verification changed")
			}
			return fail("prior state verification", rebuildErr)
		}
	}
	if !legacyPriorRecovery && !validActivationPriorVerificationObservation(priorObservation, *attempt, planSHA) {
		return fail("prior state verification", fmt.Errorf("returned invalid observations"))
	}
	var (
		legacySourceVerified  bool
		legacyServicesStopped bool
		legacyTargetFenced    bool
		legacyGatewayVerified bool
		legacyCurrentPreGrant ActivationPreGrantObservation
		legacyFence           ActivationTargetFenceObservation
		legacyPostGrant       ActivationPostGrantObservation
		legacyPostVerified    bool
		legacyPromotionSeen   bool
	)
	if legacyPriorRecovery {
		postGrant = true
		validWitness := attempt.Boundary != ActivationVerified && attempt.PreGrantObservation != nil && validActivationPreGrantObservation(*attempt.PreGrantObservation, false)
		if validWitness && boundaryAtLeast(attempt.Boundary, ActivationGatewayPromoted) {
			validWitness = attempt.GatewayBeforeHead != nil && attempt.GatewayPromotion != nil &&
				validActivationGatewayPromotionObservation(*attempt.GatewayPromotion, expectedSHA) && reflect.DeepEqual(*attempt.GatewayBeforeHead, attempt.GatewayPromotion.Before) &&
				attempt.GatewayBeforeSHA256 == attempt.GatewayPromotion.Before.HeadSHA256 && attempt.GatewayAfterSHA256 == attempt.GatewayPromotion.After.HeadSHA256
		} else if validWitness && attempt.Boundary == ActivationBegun {
			validWitness = attempt.GatewayBeforeHead != nil && validActivationGatewayBeforeHeadObservation(*attempt.GatewayBeforeHead, expectedSHA) && attempt.GatewayBeforeSHA256 == attempt.GatewayBeforeHead.HeadSHA256
		} else {
			validWitness = false
		}
		if !validWitness {
			return fail("legacy prior recovery witness", fmt.Errorf("legacy activation attempt witness is missing or invalid"))
		}
		if err := dependencies.StopServices(ctx); err != nil {
			return fail("legacy prior recovery service stop", err)
		}
		legacyServicesStopped = true
		legacyFence, err = dependencies.FenceTargetConnect(ctx)
		if err != nil || !validActivationTargetFenceObservation(legacyFence) {
			if err == nil {
				err = fmt.Errorf("typed observation is incomplete")
			}
			return fail("legacy prior recovery target CONNECT fence", err)
		}
		legacyTargetFenced = true
		if err := dependencies.VerifySourceFence(ctx); err != nil {
			return fail("legacy prior recovery source fence", err)
		}
		legacySourceVerified = true
		if boundaryAtLeast(attempt.Boundary, ActivationGatewayPromoted) {
			currentGatewayHead, headErr := dependencies.VerifyGatewayPromotion(ctx)
			if headErr != nil || !validActivationGatewayHeadObservation(currentGatewayHead, expectedSHA) || !reflect.DeepEqual(currentGatewayHead, attempt.GatewayPromotion.After) {
				if headErr == nil {
					headErr = fmt.Errorf("durable Gateway head changed")
				}
				return fail("legacy prior recovery Gateway head", headErr)
			}
			legacyGatewayVerified = true
			legacyPromotionSeen = true
		} else {
			currentGatewayHead, headErr := dependencies.ObserveGatewayPromotionHead(ctx)
			if headErr != nil || !validActivationGatewayHeadObservation(currentGatewayHead, expectedSHA) {
				return fail("legacy prior recovery Gateway head", headErr)
			}
			if !reflect.DeepEqual(currentGatewayHead, *attempt.GatewayBeforeHead) {
				verifiedAfter, verifyErr := dependencies.VerifyGatewayPromotion(ctx)
				if verifyErr != nil || !reflect.DeepEqual(verifiedAfter, currentGatewayHead) || !validActivationGatewayHeadObservation(verifiedAfter, expectedSHA) {
					return fail("legacy prior recovery Gateway head", verifyErr)
				}
				replayed, replayErr := dependencies.PromoteGateway(ctx)
				if replayErr != nil || !validActivationGatewayPromotionObservation(replayed, expectedSHA) || !reflect.DeepEqual(replayed.Before, verifiedAfter) || !reflect.DeepEqual(replayed.After, verifiedAfter) {
					return fail("legacy prior recovery Gateway promotion replay", replayErr)
				}
				promotion := ActivationGatewayPromotionObservation{Before: *attempt.GatewayBeforeHead, After: verifiedAfter}
				attempt.GatewayPromotion = &promotion
				attempt.GatewayAfterSHA256 = verifiedAfter.HeadSHA256
				attempt.Boundary = ActivationGatewayPromoted
				legacyGatewayVerified = true
				legacyPromotionSeen = true
			}
		}
		legacyCurrentPreGrant, err = dependencies.VerifyPreGrantAuthority(ctx, true)
		if err != nil || !validActivationPreGrantObservation(legacyCurrentPreGrant, true) {
			if err == nil {
				err = fmt.Errorf("typed observations are incomplete")
			}
			return fail("legacy prior recovery authority", err)
		}
		if !legacyPromotionSeen && legacyCurrentPreGrant.Platform.WriterMembership {
			return fail("legacy prior recovery authority", fmt.Errorf("writer membership is present before Gateway promotion"))
		}
		if boundaryAtLeast(attempt.Boundary, ActivationWritersGranted) || legacyCurrentPreGrant.Platform.WriterMembership {
			if !legacyCurrentPreGrant.Platform.WriterMembership {
				return fail("legacy prior recovery authority", fmt.Errorf("writer membership does not match the persisted boundary"))
			}
			legacyPostGrant, err = dependencies.VerifyPostGrantAuthority(ctx)
			if err != nil || !validActivationPostGrantObservation(legacyPostGrant) {
				if err == nil {
					err = fmt.Errorf("typed observations are incomplete")
				}
				return fail("legacy prior recovery sessions", err)
			}
			legacyPostVerified = true
		}
		priorObservation, err = dependencies.RecoverPriorState(ctx, input.PriorVerification, *attempt)
		if err != nil || !validActivationPriorVerificationObservation(priorObservation, *attempt, planSHA) {
			if err == nil {
				err = fmt.Errorf("returned invalid observations")
			}
			return fail("legacy prior recovery reconstruction", err)
		}
		attempt.PriorVerification = &priorObservation
		if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
			return fail("legacy prior recovery persistence", err)
		}
	}
	if attempt.Boundary == ActivationVerified {
		if err := verifyCompletedActivationAttempt(input, *attempt); err != nil {
			return fail("verified evidence verification", err)
		}
		if err := dependencies.VerifySourceFence(ctx); err != nil {
			return fail("source fence replay verification", err)
		}
		currentGatewayHead, err := dependencies.VerifyGatewayPromotion(ctx)
		if err != nil || attempt.GatewayPromotion == nil || !validActivationGatewayHeadObservation(currentGatewayHead, expectedSHA) || !reflect.DeepEqual(currentGatewayHead, attempt.GatewayPromotion.After) {
			if err == nil {
				err = fmt.Errorf("durable Gateway head changed")
			}
			return fail("Gateway promotion replay verification", err)
		}
		currentPostGrantDML, err := dependencies.VerifyRunningPostGrantDML(ctx)
		if err != nil || !validActivationRunningPostGrantDMLObservation(currentPostGrantDML) {
			if err == nil {
				err = fmt.Errorf("typed observation is incomplete")
			}
			return fail("running post-grant DML replay verification", err)
		}
		if err := dependencies.VerifyActivation(ctx); err != nil {
			return fail("replay verification", err)
		}
		facts, factsErr := activationFactsFromObservations(attempt.Observations)
		if factsErr != nil {
			return fail("replay observations", factsErr)
		}
		facts.Prior = priorObservation
		facts.GatewayPromotion = *attempt.GatewayPromotion
		facts.RunningPostGrantDML = currentPostGrantDML
		facts.PostGrant.DMLProbeSHA256 = currentPostGrantDML.ProbeSHA256
		observed, err := dependencies.ObserveActivation(ctx, input, *attempt, inputSHA, facts)
		if err != nil {
			return fail("replay observations", err)
		}
		if err := validateCurrentActivationObservations(input, *attempt, inputSHA, observed); err != nil {
			return fail("replay observations", err)
		}
		if attempt.Observations == nil || !equalActivationReplayObservations(observed, *attempt.Observations) {
			return fail("replay observations", fmt.Errorf("measured observations changed"))
		}
		return ExecutionResult{Result: Result{Phase: PhaseActivated, Replayed: true}, EvidencePath: attempt.EvidencePath, InputSHA256: inputSHA}, nil
	}

	if !legacySourceVerified {
		if err := dependencies.VerifySourceFence(ctx); err != nil {
			return fail("source fence verification", err)
		}
	}
	if !newAttempt && (attempt.PreGrantObservation == nil || !validActivationPreGrantObservation(*attempt.PreGrantObservation, false)) {
		return fail("pre-grant authority verification", fmt.Errorf("persisted historical observation is missing or invalid"))
	}
	allowCurrentWriterMembership := boundaryAtLeast(attempt.Boundary, ActivationWritersGranted) || attempt.Boundary == ActivationGatewayPromoted
	currentPreGrantObservation := legacyCurrentPreGrant
	if !legacyPriorRecovery {
		currentPreGrantObservation, err = dependencies.VerifyPreGrantAuthority(ctx, allowCurrentWriterMembership)
		if err != nil {
			return fail("pre-grant authority verification", err)
		}
		if !validActivationPreGrantObservation(currentPreGrantObservation, allowCurrentWriterMembership) {
			return fail("pre-grant authority verification", fmt.Errorf("typed observations are incomplete"))
		}
	}
	preGrantObservation := currentPreGrantObservation
	if newAttempt {
		attempt.PriorVerification = &priorObservation
		attempt.PreGrantObservation = &preGrantObservation
		if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
			return ExecutionResult{}, err
		}
	} else {
		preGrantObservation = *attempt.PreGrantObservation
	}
	var gatewayPromotionObservation ActivationGatewayPromotionObservation
	if boundaryAtLeast(attempt.Boundary, ActivationGatewayPromoted) {
		if attempt.GatewayBeforeHead == nil || !validActivationGatewayBeforeHeadObservation(*attempt.GatewayBeforeHead, expectedSHA) || attempt.GatewayBeforeSHA256 != attempt.GatewayBeforeHead.HeadSHA256 ||
			attempt.GatewayPromotion == nil || !validActivationGatewayPromotionObservation(*attempt.GatewayPromotion, expectedSHA) || !reflect.DeepEqual(attempt.GatewayPromotion.Before, *attempt.GatewayBeforeHead) || attempt.GatewayAfterSHA256 != attempt.GatewayPromotion.After.HeadSHA256 {
			return fail("Gateway promotion verification", fmt.Errorf("persisted promotion observation is missing or invalid"))
		}
		gatewayPromotionObservation = *attempt.GatewayPromotion
	}
	grantCrashCandidate := attempt.Boundary == ActivationGatewayPromoted && currentPreGrantObservation.Platform.WriterMembership
	if grantCrashCandidate {
		postGrant = true
	}
	fenceObservation := legacyFence
	if !legacyServicesStopped {
		if err := dependencies.StopServices(ctx); err != nil {
			return fail("service stop", err)
		}
	}
	if !legacyTargetFenced {
		fenceObservation, err = dependencies.FenceTargetConnect(ctx)
		if err != nil {
			return fail("target CONNECT fence", err)
		}
		if !validActivationTargetFenceObservation(fenceObservation) {
			return fail("target CONNECT fence", fmt.Errorf("typed observation is incomplete"))
		}
	}
	if boundaryAtLeast(attempt.Boundary, ActivationGatewayPromoted) && !legacyGatewayVerified {
		currentGatewayHead, err := dependencies.VerifyGatewayPromotion(ctx)
		if err != nil {
			return fail("Gateway promotion verification", err)
		}
		if !validActivationGatewayHeadObservation(currentGatewayHead, expectedSHA) || !reflect.DeepEqual(currentGatewayHead, attempt.GatewayPromotion.After) {
			return fail("Gateway promotion verification", fmt.Errorf("durable Gateway head changed"))
		}
	}
	if !boundaryAtLeast(attempt.Boundary, ActivationGatewayPromoted) {
		currentGatewayHead, err := dependencies.ObserveGatewayPromotionHead(ctx)
		if err != nil || !validActivationGatewayHeadObservation(currentGatewayHead, expectedSHA) {
			if err == nil {
				err = fmt.Errorf("typed Gateway head is incomplete")
			}
			return fail("Gateway before-head verification", err)
		}
		if attempt.GatewayBeforeHead == nil {
			if !validActivationGatewayBeforeHeadObservation(currentGatewayHead, expectedSHA) {
				return fail("Gateway before-head verification", fmt.Errorf("original Gateway before-head facts are missing"))
			}
			beforeHead := currentGatewayHead
			attempt.GatewayBeforeHead = &beforeHead
			attempt.GatewayBeforeSHA256 = beforeHead.HeadSHA256
			attempt.UpdatedAt = now().UTC()
			if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
				return fail("Gateway before-head persistence", err)
			}
		} else if !validActivationGatewayBeforeHeadObservation(*attempt.GatewayBeforeHead, expectedSHA) || attempt.GatewayBeforeSHA256 != attempt.GatewayBeforeHead.HeadSHA256 {
			return fail("Gateway before-head verification", fmt.Errorf("persisted Gateway before-head facts are invalid"))
		}
		originalBefore := *attempt.GatewayBeforeHead
		var promotedAfter ActivationGatewayHeadObservation
		if reflect.DeepEqual(currentGatewayHead, originalBefore) {
			promotionObservation, err := dependencies.PromoteGateway(ctx)
			if err != nil || !validActivationGatewayPromotionObservation(promotionObservation, expectedSHA) || !reflect.DeepEqual(promotionObservation.Before, originalBefore) {
				if err == nil {
					err = fmt.Errorf("promotion observation does not preserve the original before-head")
				}
				return fail("Gateway promotion", err)
			}
			promotedAfter = promotionObservation.After
		} else {
			verifiedAfter, err := dependencies.VerifyGatewayPromotion(ctx)
			if err != nil || !validActivationGatewayHeadObservation(verifiedAfter, expectedSHA) || !reflect.DeepEqual(verifiedAfter, currentGatewayHead) {
				if err == nil {
					err = fmt.Errorf("current Gateway head is not the exact promoted after-head")
				}
				return fail("Gateway promotion recovery", err)
			}
			replayedPromotion, err := dependencies.PromoteGateway(ctx)
			if err != nil || !validActivationGatewayPromotionObservation(replayedPromotion, expectedSHA) || !reflect.DeepEqual(replayedPromotion.Before, verifiedAfter) || !reflect.DeepEqual(replayedPromotion.After, verifiedAfter) {
				if err == nil {
					err = fmt.Errorf("idempotent Gateway promotion replay changed the verified after-head")
				}
				return fail("Gateway promotion recovery", err)
			}
			promotedAfter = verifiedAfter
		}
		currentGatewayHead, err = dependencies.VerifyGatewayPromotion(ctx)
		if err != nil || !validActivationGatewayHeadObservation(currentGatewayHead, expectedSHA) || !reflect.DeepEqual(currentGatewayHead, promotedAfter) {
			if err == nil {
				err = fmt.Errorf("durable Gateway head does not match promotion result")
			}
			return fail("Gateway promotion verification", err)
		}
		gatewayPromotionObservation = ActivationGatewayPromotionObservation{Before: originalBefore, After: promotedAfter}
		attempt.GatewayPromotion = &gatewayPromotionObservation
		attempt.GatewayAfterSHA256 = promotedAfter.HeadSHA256
		attempt.Boundary = ActivationGatewayPromoted
		attempt.UpdatedAt = now().UTC()
		if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
			return ExecutionResult{}, err
		}
	}
	postGrantObservation := legacyPostGrant
	postGrantVerified := legacyPostVerified
	if grantCrashCandidate {
		recoveredPostGrant, err := dependencies.VerifyPostGrantAuthority(ctx)
		if err != nil {
			return fail("post-grant authority recovery", err)
		}
		if !validActivationPostGrantObservation(recoveredPostGrant) {
			return fail("post-grant authority recovery", fmt.Errorf("typed observations are incomplete"))
		}
		attempt.Boundary, attempt.UpdatedAt = ActivationWritersGranted, now().UTC()
		if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
			return fail("writer grant recovery persistence", err)
		}
		postGrantObservation = recoveredPostGrant
		postGrantVerified = true
	}
	if !boundaryAtLeast(attempt.Boundary, ActivationWritersGranted) {
		if err := dependencies.GrantWriters(ctx); err != nil {
			return fail("writer grant", err)
		}
		postGrant = true
		attempt.Boundary, attempt.UpdatedAt = ActivationWritersGranted, now().UTC()
		if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
			return fail("writer boundary persistence", err)
		}
	}
	if !postGrantVerified {
		postGrantObservation, err = dependencies.VerifyPostGrantAuthority(ctx)
		if err != nil {
			return fail("post-grant authority verification", err)
		}
		if !validActivationPostGrantObservation(postGrantObservation) {
			return fail("post-grant authority verification", fmt.Errorf("typed observations are incomplete"))
		}
	}
	if err := dependencies.RestoreConnect(ctx); err != nil {
		return fail("CONNECT restore", err)
	}
	restoredSessionsObservation, err := dependencies.VerifyRestoredTargetSessions(ctx)
	if err != nil {
		return fail("restored target session verification", err)
	}
	if !validActivationRestoredTargetSessionsObservation(restoredSessionsObservation) {
		return fail("restored target session verification", fmt.Errorf("typed observation is incomplete"))
	}
	if !boundaryAtLeast(attempt.Boundary, ActivationConnectRestored) {
		attempt.Boundary, attempt.UpdatedAt = ActivationConnectRestored, now().UTC()
		if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
			return fail("CONNECT boundary persistence", err)
		}
	}
	if err := dependencies.StartServices(ctx); err != nil {
		return fail("service start", err)
	}
	if !boundaryAtLeast(attempt.Boundary, ActivationServicesStarted) {
		attempt.Boundary, attempt.UpdatedAt = ActivationServicesStarted, now().UTC()
		if err := persistActivationBoundary(ctx, input.JournalPath, dependencies.SaveAttempt, attempt); err != nil {
			return fail("service boundary persistence", err)
		}
	}
	if err := dependencies.VerifyActivation(ctx); err != nil {
		return fail("verification", err)
	}
	runningPostGrantDML, err := dependencies.VerifyRunningPostGrantDML(ctx)
	if err != nil || !validActivationRunningPostGrantDMLObservation(runningPostGrantDML) {
		if err == nil {
			err = fmt.Errorf("typed observation is incomplete")
		}
		return fail("running post-grant DML verification", err)
	}
	facts := ActivationVerificationFacts{Prior: priorObservation, PreGrant: preGrantObservation, GatewayPromotion: gatewayPromotionObservation, Fence: fenceObservation, PostGrant: postGrantObservation, RestoredSessions: restoredSessionsObservation, RunningPostGrantDML: runningPostGrantDML}
	observations, err := dependencies.ObserveActivation(ctx, input, *attempt, inputSHA, facts)
	if err != nil {
		return fail("observation", err)
	}
	if !activationObservationsMatchFacts(observations, facts) {
		return fail("observation", fmt.Errorf("measured facts changed"))
	}
	if err := validateCurrentActivationObservations(input, *attempt, inputSHA, observations); err != nil {
		return fail("observation", err)
	}
	verifiedAt := now().UTC()
	if !verifiedAt.After(attempt.StartedAt) {
		verifiedAt = attempt.StartedAt.Add(time.Nanosecond)
	}
	attempt.Boundary, attempt.UpdatedAt = ActivationVerified, verifiedAt
	attempt.Observations = &observations
	result, err := dependencies.CompleteVerifiedAttempt(ctx, input, *attempt, inputSHA)
	if err != nil {
		return fail("verified activation completion", err)
	}
	return result, nil
}

func activationObservationsMatchFacts(observations ActivationObservations, facts ActivationVerificationFacts) bool {
	return reflect.DeepEqual(observations.PriorVerification, facts.Prior) &&
		reflect.DeepEqual(observations.GatewayPromotion, facts.GatewayPromotion) &&
		reflect.DeepEqual(observations.PreGrantPlatformAuthority, facts.PreGrant.Platform) &&
		reflect.DeepEqual(observations.PreGrantGatewayAuthority, facts.PreGrant.Gateway) &&
		observations.PreGrantDMLProbeSHA256 == facts.PreGrant.DMLProbeSHA256 &&
		reflect.DeepEqual(observations.PlatformAuthority, facts.PostGrant.Platform) &&
		reflect.DeepEqual(observations.GatewayAuthority, facts.PostGrant.Gateway) &&
		observations.PostGrantDMLProbeSHA256 == facts.RunningPostGrantDML.ProbeSHA256 && facts.RunningPostGrantDML.PlatformPassed && facts.RunningPostGrantDML.GatewayPassed &&
		observations.TargetSessionsFenced == (facts.Fence.SessionsZero && facts.PostGrant.TargetSessionsZero) &&
		observations.TargetSessionFenceSHA256 == facts.Fence.ProbeSHA256 &&
		observations.RestoredTargetSessionsZero == facts.RestoredSessions.SessionsZero &&
		observations.RestoredTargetSessionSHA256 == facts.RestoredSessions.ProbeSHA256
}

func equalActivationReplayObservations(current, sealed ActivationObservations) bool {
	if !validSHA256(current.SupervisorTopologySHA256) {
		return false
	}
	if sealed.SupervisorTopologySHA256 == "" {
		current.SupervisorTopologySHA256 = ""
	} else if current.SupervisorTopologySHA256 != sealed.SupervisorTopologySHA256 {
		return false
	}
	// The raw digest remains sealed evidence, but owned process PIDs make it volatile across restarts.
	current.SupervisorStatusSHA256 = ""
	sealed.SupervisorStatusSHA256 = ""
	return reflect.DeepEqual(current, sealed)
}

func activationSupervisorTopologySHA256(status, readiness string) (string, error) {
	services := [...]string{"postgres", "gateway", "platform", "funding-worker", "web"}
	statusLines := strings.Split(status, "\n")
	readinessLines := strings.Split(readiness, "\n")
	if len(statusLines) != len(services) || len(readinessLines) != len(services) {
		return "", fmt.Errorf("supervisor topology must contain exactly %d status and readiness lines", len(services))
	}

	normalized := make([]string, 0, len(services)*2)
	for index, service := range services {
		prefix := service + " running pid="
		if !strings.HasPrefix(statusLines[index], prefix) {
			return "", fmt.Errorf("supervisor status for %s is invalid", service)
		}
		pid := strings.TrimPrefix(statusLines[index], prefix)
		if pid == "" || pid[0] == '0' {
			return "", fmt.Errorf("supervisor pid for %s is invalid", service)
		}
		for _, digit := range pid {
			if digit < '0' || digit > '9' {
				return "", fmt.Errorf("supervisor pid for %s is invalid", service)
			}
		}
		if _, err := strconv.ParseUint(pid, 10, 64); err != nil {
			return "", fmt.Errorf("supervisor pid for %s is invalid", service)
		}
		normalized = append(normalized, service+" running")
	}
	for index, service := range services {
		want := service + " ready"
		if readinessLines[index] != want {
			return "", fmt.Errorf("supervisor readiness for %s is invalid", service)
		}
		normalized = append(normalized, want)
	}
	return sha256Hex([]byte(strings.Join(normalized, "\n"))), nil
}

func activationPriorPlanSHA(plan ActivationPriorVerificationPlan) (string, error) {
	data, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("encode activation prior verification plan: %w", err)
	}
	return sha256Hex(data), nil
}

func validActivationPriorVerificationObservation(observation ActivationPriorVerificationObservation, attempt ActivationAttempt, planSHA string) bool {
	return observation.AttemptID == attempt.ID && observation.PlanSHA256 == planSHA && validSHA256(planSHA) &&
		validSHA256(observation.ArtifactSetSHA256) && validSHA256(observation.CurrentPostconditionsSHA256)
}

func verifyCompletedActivationAttempt(input ActivationInput, attempt ActivationAttempt) error {
	if attempt.EvidencePath == "" || !validSHA256(attempt.ProofSHA256) {
		return fmt.Errorf("verified activation attempt is missing evidence")
	}
	evidenceDir, err := filepath.Abs(input.EvidenceDir)
	if err != nil {
		return fmt.Errorf("verified activation evidence path is invalid")
	}
	evidencePath, err := filepath.Abs(attempt.EvidencePath)
	if err != nil || filepath.Dir(evidencePath) != evidenceDir {
		return fmt.Errorf("verified activation evidence path is outside the evidence directory")
	}
	info, err := os.Lstat(evidencePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("verified activation evidence is unavailable or unsafe")
	}
	data, err := os.ReadFile(evidencePath)
	if err != nil {
		return fmt.Errorf("read verified activation evidence: %w", err)
	}
	if sha256Hex(data) != attempt.ProofSHA256 {
		return fmt.Errorf("verified activation evidence changed")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return fmt.Errorf("verified activation evidence is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var proof operationProof
	if err := decoder.Decode(&proof); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("verified activation evidence is invalid")
	}
	if proof.SchemaVersion != 1 || proof.Operation != Activate || proof.Status != "pass" || proof.MigrationID != input.MigrationID || proof.GeneratedBy != evidenceGenerator ||
		proof.InputSHA != attempt.InputSHA256 || !validSHA256(proof.PreStateSHA) || len(proof.Commands) == 0 || proof.ActivationObservations == nil || attempt.Observations == nil ||
		!reflect.DeepEqual(proof.ActivationObservations, attempt.Observations) || !sameStringSet(proof.Checks, []string{"source-fenced", "gateway-promoted", "writers-granted", "connect-restored", "services-started", "activation-verified"}) {
		return fmt.Errorf("verified activation evidence is invalid")
	}
	if err := validateActivationObservations(input, attempt, attempt.InputSHA256, *proof.ActivationObservations); err != nil {
		return fmt.Errorf("verified activation evidence observations are invalid: %w", err)
	}
	preGrant := ActivationPreGrantObservation{
		Platform:       proof.ActivationObservations.PreGrantPlatformAuthority,
		Gateway:        proof.ActivationObservations.PreGrantGatewayAuthority,
		DMLProbeSHA256: proof.ActivationObservations.PreGrantDMLProbeSHA256,
	}
	if attempt.PreGrantObservation == nil || !reflect.DeepEqual(*attempt.PreGrantObservation, preGrant) {
		return fmt.Errorf("verified activation evidence historical pre-grant observation changed")
	}
	return nil
}

func validateActivationObservations(input ActivationInput, attempt ActivationAttempt, inputSHA string, observations ActivationObservations) error {
	platformMatrix, gatewayMatrix := authority.PlatformV15(), authority.GatewayV6()
	if observations.SchemaVersion != 1 || observations.InputSHA256 != inputSHA || observations.Source != input.Source || observations.Platform != input.Platform || observations.Gateway != input.Gateway || observations.Maintenance != input.Maintenance ||
		observations.SourceWriterRole != input.SourceWriterRole || observations.PlatformMigratorRole != input.PlatformMigratorRole || observations.PlatformRuntimeRole != input.PlatformRuntimeRole || observations.PlatformWriterRole != input.PlatformWriterRole || observations.GatewayMigratorRole != input.GatewayMigratorRole || observations.GatewayRuntimeRole != input.GatewayRuntimeRole || observations.GatewayWriterRole != input.GatewayWriterRole ||
		observations.GatewayBeforeSHA256 != attempt.GatewayBeforeSHA256 || observations.GatewayAfterSHA256 != attempt.GatewayAfterSHA256 ||
		attempt.GatewayBeforeHead == nil || !reflect.DeepEqual(*attempt.GatewayBeforeHead, observations.GatewayPromotion.Before) || attempt.GatewayBeforeSHA256 != attempt.GatewayBeforeHead.HeadSHA256 ||
		attempt.GatewayPromotion == nil || !reflect.DeepEqual(observations.GatewayPromotion, *attempt.GatewayPromotion) || !validActivationGatewayPromotionObservation(observations.GatewayPromotion, attempt.ExpectedPromotionSHA256) || attempt.GatewayAfterSHA256 != observations.GatewayPromotion.After.HeadSHA256 ||
		observations.PriorVerification.AttemptID != attempt.ID || !validSHA256(observations.PriorVerification.PlanSHA256) || !validSHA256(observations.PriorVerification.ArtifactSetSHA256) || !validSHA256(observations.PriorVerification.CurrentPostconditionsSHA256) ||
		!validActivationPreGrantObservation(ActivationPreGrantObservation{Platform: observations.PreGrantPlatformAuthority, Gateway: observations.PreGrantGatewayAuthority, DMLProbeSHA256: observations.PreGrantDMLProbeSHA256}, false) ||
		observations.PreGrantPlatformAuthority.RuntimeRole != input.PlatformRuntimeRole || observations.PreGrantGatewayAuthority.RuntimeRole != input.GatewayRuntimeRole ||
		observations.PlatformAuthority.MatrixSHA256 != platformMatrix.Digest() || observations.PlatformAuthority.MigrationSHA256 != platformMatrix.MigrationDigest || observations.PlatformAuthority.RuntimeRole != platformMatrix.Roles.Runtime || observations.PlatformAuthority.RuntimeRole != input.PlatformRuntimeRole ||
		observations.GatewayAuthority.MatrixSHA256 != gatewayMatrix.Digest() || observations.GatewayAuthority.MigrationSHA256 != gatewayMatrix.MigrationDigest || observations.GatewayAuthority.RuntimeRole != gatewayMatrix.Roles.Runtime || observations.GatewayAuthority.RuntimeRole != input.GatewayRuntimeRole ||
		!completeActivationAuthorityObservation(observations.PlatformAuthority) || !completeActivationAuthorityObservation(observations.GatewayAuthority) ||
		!validSHA256(observations.PostGrantDMLProbeSHA256) || !observations.SourceFenceVerified || !observations.TargetSessionsFenced || !validSHA256(observations.TargetSessionFenceSHA256) || !observations.RestoredTargetSessionsZero || !validSHA256(observations.RestoredTargetSessionSHA256) || !validSHA256(observations.PostgresIdentitySHA256) || !validSHA256(observations.SupervisorStatusSHA256) ||
		(observations.SupervisorTopologySHA256 != "" && !validSHA256(observations.SupervisorTopologySHA256)) ||
		!observations.TestModeVerified || !observations.LivePrivateWritesDisabled || !observations.VenueWritesDisabled {
		return fmt.Errorf("activation observations do not satisfy the canonical Test contract")
	}
	return nil
}

func validateCurrentActivationObservations(input ActivationInput, attempt ActivationAttempt, inputSHA string, observations ActivationObservations) error {
	if err := validateActivationObservations(input, attempt, inputSHA, observations); err != nil {
		return err
	}
	if !validSHA256(observations.SupervisorTopologySHA256) {
		return fmt.Errorf("current supervisor topology observation is invalid")
	}
	return nil
}

func completeActivationAuthorityObservation(observation ActivationAuthorityObservation) bool {
	return validActivationAuthorityObservation(observation, true)
}

func validActivationAuthorityObservation(observation ActivationAuthorityObservation, writerMembership bool) bool {
	return observation.RuntimeInherits && observation.WriterMembership == writerMembership && observation.FullCapabilities && observation.EffectiveTablePrivileges && observation.EffectiveSequencePrivileges && observation.EffectiveRoutinePrivileges && len(observation.OwnershipViolations) == 0 && len(observation.PrivilegeViolations) == 0
}

func validActivationPreGrantObservation(observation ActivationPreGrantObservation, resuming bool) bool {
	platformMatrix, gatewayMatrix := authority.PlatformV15(), authority.GatewayV6()
	if observation.Platform.MatrixSHA256 != platformMatrix.Digest() || observation.Platform.MigrationSHA256 != platformMatrix.MigrationDigest || observation.Platform.RuntimeRole != platformMatrix.Roles.Runtime ||
		observation.Gateway.MatrixSHA256 != gatewayMatrix.Digest() || observation.Gateway.MigrationSHA256 != gatewayMatrix.MigrationDigest || observation.Gateway.RuntimeRole != gatewayMatrix.Roles.Runtime {
		return false
	}
	membership := observation.Platform.WriterMembership
	if observation.Gateway.WriterMembership != membership || (!resuming && membership) {
		return false
	}
	return validSHA256(observation.DMLProbeSHA256) && validActivationAuthorityObservation(observation.Platform, membership) && validActivationAuthorityObservation(observation.Gateway, membership)
}

func activationGatewayHeadObservation(digest storepkg.MigrationPromotionDigest) ActivationGatewayHeadObservation {
	observation := ActivationGatewayHeadObservation{
		PendingSHA256: digest.PendingSHA256, ActiveSHA256: digest.ActiveSHA256, OperationSetSHA256: digest.OperationSetSHA256,
	}
	observation.HeadSHA256 = activationGatewayHeadSHA256(observation)
	return observation
}

func activationGatewayHeadSHA256(observation ActivationGatewayHeadObservation) string {
	data, _ := json.Marshal(struct {
		PendingSHA256      string `json:"pendingSha256"`
		ActiveSHA256       string `json:"activeSha256"`
		OperationSetSHA256 string `json:"operationSetSha256"`
	}{observation.PendingSHA256, observation.ActiveSHA256, observation.OperationSetSHA256})
	return sha256Hex(data)
}

func validActivationGatewayHeadObservation(observation ActivationGatewayHeadObservation, operationSetSHA string) bool {
	return validSHA256(observation.PendingSHA256) && validSHA256(observation.ActiveSHA256) && observation.OperationSetSHA256 == operationSetSHA && validSHA256(operationSetSHA) && observation.HeadSHA256 == activationGatewayHeadSHA256(observation)
}

func validActivationGatewayBeforeHeadObservation(observation ActivationGatewayHeadObservation, operationSetSHA string) bool {
	return validActivationGatewayHeadObservation(observation, operationSetSHA)
}

func validActivationGatewayPromotionObservation(observation ActivationGatewayPromotionObservation, operationSetSHA string) bool {
	return validActivationGatewayHeadObservation(observation.Before, operationSetSHA) && validActivationGatewayHeadObservation(observation.After, operationSetSHA)
}

func validActivationTargetFenceObservation(observation ActivationTargetFenceObservation) bool {
	return observation.SessionsZero && validSHA256(observation.ProbeSHA256)
}

func validActivationRestoredTargetSessionsObservation(observation ActivationRestoredTargetSessionsObservation) bool {
	return observation.SessionsZero && validSHA256(observation.ProbeSHA256)
}

func validActivationPostGrantObservation(observation ActivationPostGrantObservation) bool {
	return observation.TargetSessionsZero && validSHA256(observation.DMLProbeSHA256) && completeActivationAuthorityObservation(observation.Platform) && completeActivationAuthorityObservation(observation.Gateway)
}

func validActivationRunningPostGrantDMLObservation(observation ActivationRunningPostGrantDMLObservation) bool {
	return observation.PlatformPassed && observation.GatewayPassed && validSHA256(observation.ProbeSHA256)
}

func activationFactsFromObservations(observations *ActivationObservations) (ActivationVerificationFacts, error) {
	if observations == nil {
		return ActivationVerificationFacts{}, fmt.Errorf("verified activation observations are missing")
	}
	facts := ActivationVerificationFacts{
		Prior:            observations.PriorVerification,
		GatewayPromotion: observations.GatewayPromotion,
		PreGrant: ActivationPreGrantObservation{
			Platform: observations.PreGrantPlatformAuthority, Gateway: observations.PreGrantGatewayAuthority, DMLProbeSHA256: observations.PreGrantDMLProbeSHA256,
		},
		Fence: ActivationTargetFenceObservation{SessionsZero: observations.TargetSessionsFenced, ProbeSHA256: observations.TargetSessionFenceSHA256},
		PostGrant: ActivationPostGrantObservation{
			Platform: observations.PlatformAuthority, Gateway: observations.GatewayAuthority, TargetSessionsZero: observations.TargetSessionsFenced, DMLProbeSHA256: observations.PostGrantDMLProbeSHA256,
		},
		RestoredSessions:    ActivationRestoredTargetSessionsObservation{SessionsZero: observations.RestoredTargetSessionsZero, ProbeSHA256: observations.RestoredTargetSessionSHA256},
		RunningPostGrantDML: ActivationRunningPostGrantDMLObservation{PlatformPassed: true, GatewayPassed: true, ProbeSHA256: observations.PostGrantDMLProbeSHA256},
	}
	if !validActivationPreGrantObservation(facts.PreGrant, false) || !validActivationTargetFenceObservation(facts.Fence) || !validActivationPostGrantObservation(facts.PostGrant) || !validActivationRestoredTargetSessionsObservation(facts.RestoredSessions) || !validActivationRunningPostGrantDMLObservation(facts.RunningPostGrantDML) {
		return ActivationVerificationFacts{}, fmt.Errorf("verified activation measured facts are incomplete")
	}
	return facts, nil
}

func verifyActivationEnvironment() error {
	if os.Getenv("FT_MODE") != "test" {
		return fmt.Errorf("activation requires FT_MODE=test")
	}
	for _, name := range []string{"RUN_FULL", "BT_FUNDING_LIVE_ENABLED"} {
		if _, configured := os.LookupEnv(name); configured {
			return fmt.Errorf("activation environment contains forbidden %s", name)
		}
	}
	for _, name := range []string{"BT_GATEWAY_STARTUP_PROFILE", "BT_PLATFORM_STARTUP_PROFILE", "BT_FUNDING_STARTUP_PROFILE"} {
		if value := os.Getenv(name); value != "" && value != "normal" {
			return fmt.Errorf("activation requires %s=normal", name)
		}
	}
	for _, assignment := range os.Environ() {
		name, value, _ := strings.Cut(assignment, "=")
		upper := strings.ToUpper(name)
		venueWriteFlag := (strings.HasPrefix(upper, "BOLTER_ENABLE_") && strings.HasSuffix(upper, "_WRITES")) ||
			(strings.HasPrefix(upper, "BOLTER_ALLOW_") && strings.HasSuffix(upper, "_WRITES")) ||
			(strings.Contains(upper, "LIVE") && strings.Contains(upper, "WRITE")) || strings.HasSuffix(upper, "WRITES_ENABLED")
		if venueWriteFlag && value != "" && value != "0" {
			return fmt.Errorf("activation environment enables private or venue writes through %s", name)
		}
	}
	return nil
}

func readActivationJournal(path string) (ActivationAttempt, string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return ActivationAttempt{}, "", fmt.Errorf("activation journal is unavailable or unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ActivationAttempt{}, "", fmt.Errorf("read activation journal: %w", err)
	}
	journalSHA := sha256Hex(data)
	if err := rejectDuplicateKeys(data); err != nil {
		return ActivationAttempt{}, "", fmt.Errorf("activation journal changed")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var journal ActivationAttempt
	if err := decoder.Decode(&journal); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ActivationAttempt{}, "", fmt.Errorf("activation journal changed")
	}
	return journal, journalSHA, nil
}

func recoverOrphanInitialActivationJournal(path, inputSHA, expectedPromotionSHA, priorPlanSHA string) (*ActivationAttempt, error) {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("activation journal is unavailable or unsafe")
	}
	journal, journalSHA, err := readActivationJournal(path)
	if err != nil {
		return nil, err
	}
	idSeed := sha256.Sum256([]byte(inputSHA + "\x00" + journal.StartedAt.UTC().Format(time.RFC3339Nano)))
	expectedID := hex.EncodeToString(idSeed[:16])
	if journal.ID != expectedID || journal.InputSHA256 != inputSHA || journal.ExpectedPromotionSHA256 != expectedPromotionSHA || journal.Boundary != ActivationBegun ||
		journal.StartedAt.IsZero() || journal.StartedAt.Location() != time.UTC || !journal.UpdatedAt.Equal(journal.StartedAt) || journal.JournalSHA256 != "" ||
		journal.PriorVerification == nil || !validActivationPriorVerificationObservation(*journal.PriorVerification, journal, priorPlanSHA) ||
		journal.PreGrantObservation == nil || !validActivationPreGrantObservation(*journal.PreGrantObservation, false) ||
		journal.GatewayBeforeHead != nil || journal.GatewayBeforeSHA256 != "" || journal.GatewayPromotion != nil || journal.GatewayAfterSHA256 != "" ||
		journal.EvidencePath != "" || journal.ProofSHA256 != "" || journal.Observations != nil {
		return nil, fmt.Errorf("activation journal changed")
	}
	journal.JournalSHA256 = journalSHA
	return &journal, nil
}

func validPersistedActivationWitnesses(attempt ActivationAttempt, priorPlanSHA string) bool {
	if attempt.PriorVerification != nil && !validActivationPriorVerificationObservation(*attempt.PriorVerification, attempt, priorPlanSHA) {
		return false
	}
	if attempt.PreGrantObservation == nil || !validActivationPreGrantObservation(*attempt.PreGrantObservation, false) {
		return false
	}
	if attempt.GatewayBeforeHead == nil {
		if attempt.GatewayBeforeSHA256 != "" {
			return false
		}
	} else if !validActivationGatewayBeforeHeadObservation(*attempt.GatewayBeforeHead, attempt.ExpectedPromotionSHA256) || attempt.GatewayBeforeSHA256 != attempt.GatewayBeforeHead.HeadSHA256 {
		return false
	}
	if boundaryAtLeast(attempt.Boundary, ActivationGatewayPromoted) {
		if attempt.GatewayBeforeHead == nil || attempt.GatewayPromotion == nil || !validActivationGatewayPromotionObservation(*attempt.GatewayPromotion, attempt.ExpectedPromotionSHA256) ||
			!reflect.DeepEqual(*attempt.GatewayBeforeHead, attempt.GatewayPromotion.Before) || attempt.GatewayAfterSHA256 != attempt.GatewayPromotion.After.HeadSHA256 {
			return false
		}
	} else if attempt.GatewayPromotion != nil || attempt.GatewayAfterSHA256 != "" {
		return false
	}
	return attempt.Boundary != ActivationVerified && attempt.EvidencePath == "" && attempt.ProofSHA256 == "" && attempt.Observations == nil
}

func validActivationJournalAdvance(journal, authoritative ActivationAttempt, priorPlanSHA string) bool {
	order := map[ActivationBoundary]int{ActivationBegun: 1, ActivationGatewayPromoted: 2, ActivationWritersGranted: 3, ActivationConnectRestored: 4, ActivationServicesStarted: 5}
	if order[journal.Boundary] != order[authoritative.Boundary]+1 || journal.UpdatedAt.Before(authoritative.UpdatedAt) ||
		!validPersistedActivationWitnesses(authoritative, priorPlanSHA) || !validPersistedActivationWitnesses(journal, priorPlanSHA) {
		return false
	}
	normalized := journal
	normalized.Boundary = authoritative.Boundary
	normalized.UpdatedAt = authoritative.UpdatedAt
	if journal.Boundary == ActivationGatewayPromoted {
		if authoritative.GatewayBeforeHead == nil || authoritative.GatewayPromotion != nil || authoritative.GatewayAfterSHA256 != "" {
			return false
		}
		normalized.GatewayPromotion = nil
		normalized.GatewayAfterSHA256 = ""
	}
	return reflect.DeepEqual(normalized, authoritative)
}

func verifyActivationJournal(path, expectedSHA string, authoritative ActivationAttempt, priorPlanSHA string) (*ActivationAttempt, error) {
	journal, journalSHA, err := readActivationJournal(path)
	if err != nil {
		return nil, err
	}
	if journalSHA == expectedSHA {
		if journal.ID != authoritative.ID || journal.InputSHA256 != authoritative.InputSHA256 || journal.ExpectedPromotionSHA256 != authoritative.ExpectedPromotionSHA256 ||
			!journal.StartedAt.Equal(authoritative.StartedAt) || !boundaryAtLeast(authoritative.Boundary, journal.Boundary) ||
			!reflect.DeepEqual(journal.PriorVerification, authoritative.PriorVerification) || !reflect.DeepEqual(journal.PreGrantObservation, authoritative.PreGrantObservation) || !reflect.DeepEqual(journal.GatewayBeforeHead, authoritative.GatewayBeforeHead) || !reflect.DeepEqual(journal.GatewayPromotion, authoritative.GatewayPromotion) ||
			journal.GatewayBeforeSHA256 != authoritative.GatewayBeforeSHA256 || journal.GatewayAfterSHA256 != authoritative.GatewayAfterSHA256 {
			return nil, fmt.Errorf("activation journal changed")
		}
		return nil, nil
	}
	sameBoundaryPriorEnrichment := false
	if journal.Boundary == authoritative.Boundary && authoritative.PriorVerification == nil && journal.PriorVerification != nil &&
		validActivationPriorVerificationObservation(*journal.PriorVerification, authoritative, priorPlanSHA) {
		withoutPrior := journal
		withoutPrior.PriorVerification = nil
		sameBoundaryPriorEnrichment = reflect.DeepEqual(withoutPrior, authoritative)
	}
	if sameBoundaryPriorEnrichment {
		journal.JournalSHA256 = journalSHA
		return &journal, nil
	}
	sameBoundaryGatewayBeforeHeadEnrichment := false
	if journal.Boundary == ActivationBegun && authoritative.Boundary == ActivationBegun && authoritative.GatewayBeforeHead == nil && journal.GatewayBeforeHead != nil &&
		!journal.UpdatedAt.Before(authoritative.UpdatedAt) && validPersistedActivationWitnesses(authoritative, priorPlanSHA) && validPersistedActivationWitnesses(journal, priorPlanSHA) {
		withoutBeforeHead := journal
		withoutBeforeHead.GatewayBeforeHead = nil
		withoutBeforeHead.GatewayBeforeSHA256 = ""
		withoutBeforeHead.UpdatedAt = authoritative.UpdatedAt
		sameBoundaryGatewayBeforeHeadEnrichment = reflect.DeepEqual(withoutBeforeHead, authoritative)
	}
	if sameBoundaryGatewayBeforeHeadEnrichment {
		journal.JournalSHA256 = journalSHA
		return &journal, nil
	}
	if !validActivationJournalAdvance(journal, authoritative, priorPlanSHA) {
		return nil, fmt.Errorf("activation journal changed")
	}
	journal.JournalSHA256 = journalSHA
	return &journal, nil
}

func validateActivationInput(input ActivationInput) error {
	if strings.TrimSpace(input.MigrationID) == "" || strings.TrimSpace(input.ManifestPath) == "" || strings.TrimSpace(input.EvidenceDir) == "" || strings.TrimSpace(input.JournalPath) == "" {
		return fmt.Errorf("activation paths and migration id are required")
	}
	for label, identity := range map[string]DatabaseIdentity{"source": input.Source, "Platform": input.Platform, "Gateway": input.Gateway, "maintenance": input.Maintenance} {
		if _, err := postgresService(identity.Service); err != nil {
			return fmt.Errorf("%s %w", label, err)
		}
		if _, err := expectedTestIdentity(label+" database", identity.Database); err != nil {
			return err
		}
		if _, err := expectedTestIdentity(label+" role", identity.Role); err != nil {
			return err
		}
	}
	if _, err := expectedTestIdentity("Platform writer role", input.PlatformWriterRole); err != nil {
		return err
	}
	if _, err := expectedTestIdentity("source writer role", input.SourceWriterRole); err != nil {
		return err
	}
	if _, err := expectedTestIdentity("Platform migrator role", input.PlatformMigratorRole); err != nil {
		return err
	}
	if _, err := expectedTestIdentity("Platform runtime role", input.PlatformRuntimeRole); err != nil {
		return err
	}
	if _, err := expectedTestIdentity("Gateway migrator role", input.GatewayMigratorRole); err != nil {
		return err
	}
	if _, err := expectedTestIdentity("Gateway runtime role", input.GatewayRuntimeRole); err != nil {
		return err
	}
	if _, err := expectedTestIdentity("Gateway writer role", input.GatewayWriterRole); err != nil {
		return err
	}
	platformMatrix, gatewayMatrix := authority.PlatformV15(), authority.GatewayV6()
	if input.Source != (DatabaseIdentity{Service: "funding_source_test", Database: "bolter_funding_test", Role: "bolter_funding_test_migrator"}) ||
		input.Platform != (DatabaseIdentity{Service: "platform_target_test", Database: "bolter_platform_test", Role: platformMatrix.Roles.Migrator}) ||
		input.Gateway != (DatabaseIdentity{Service: "gateway_test", Database: "bolter_gateway_test", Role: gatewayMatrix.Roles.Migrator}) ||
		input.Maintenance != (DatabaseIdentity{Service: "platform_maintenance_test", Database: "bolter_maintenance_test", Role: platformMatrix.Roles.Maintenance}) ||
		input.Maintenance.Role != gatewayMatrix.Roles.Maintenance || input.SourceWriterRole != "bolter_funding_test_writer" ||
		input.PlatformMigratorRole != platformMatrix.Roles.Migrator || input.PlatformRuntimeRole != platformMatrix.Roles.Runtime || input.PlatformWriterRole != platformMatrix.Roles.Writer ||
		input.GatewayMigratorRole != gatewayMatrix.Roles.Migrator || input.GatewayRuntimeRole != gatewayMatrix.Roles.Runtime || input.GatewayWriterRole != gatewayMatrix.Roles.Writer {
		return fmt.Errorf("activation identities must match the canonical Test contract")
	}
	if input.Platform.Database == input.Gateway.Database || input.Platform.Role == input.Gateway.Role {
		return fmt.Errorf("Platform and Gateway identities must be separate")
	}
	if input.PriorVerification.SchemaVersion != 1 || input.PriorVerification.MigrationID != input.MigrationID || !validSHA256(input.PriorVerification.ManifestStateSHA256) || len(input.PriorVerification.Events) == 0 {
		return fmt.Errorf("activation prior verification plan is invalid")
	}
	for _, event := range input.PriorVerification.Events {
		if event.Sequence <= 0 || event.Epoch <= 0 || event.EvidencePath == "" || !validSHA256(event.ProofSHA256) {
			return fmt.Errorf("activation prior verification event is invalid")
		}
		if event.ArtifactPath != "" && !validSHA256(event.ArtifactSHA256) {
			return fmt.Errorf("activation prior verification artifact is invalid")
		}
	}
	return nil
}

func activationInputSHA(input ActivationInput) (string, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode activation input: %w", err)
	}
	return sha256Hex(data), nil
}

func boundaryAtLeast(got, want ActivationBoundary) bool {
	order := map[ActivationBoundary]int{ActivationBegun: 1, ActivationGatewayPromoted: 2, ActivationWritersGranted: 3, ActivationConnectRestored: 4, ActivationServicesStarted: 5, ActivationVerified: 6}
	return order[got] >= order[want] && order[got] != 0
}

func persistActivationBoundary(ctx context.Context, journalPath string, save func(context.Context, ActivationAttempt) error, attempt *ActivationAttempt) error {
	data, err := json.MarshalIndent(attempt, "", "  ")
	if err != nil {
		return fmt.Errorf("encode activation journal: %w", err)
	}
	data = append(data, '\n')
	if err := writeActivationJournal(journalPath, data); err != nil {
		return err
	}
	attempt.JournalSHA256 = sha256Hex(data)
	if err := save(ctx, *attempt); err != nil {
		return fmt.Errorf("persist activation attempt: %w", err)
	}
	return nil
}

func writeActivationJournal(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create activation journal directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".activation-journal-*")
	if err != nil {
		return fmt.Errorf("create activation journal: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("publish activation journal: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func buildActivationInput(manifest Manifest, request ExecutionRequest, journalPath string, priorPlan ActivationPriorVerificationPlan, activatedReplay bool) ActivationInput {
	return ActivationInput{
		MigrationID: manifest.MigrationID, ManifestPath: request.ManifestPath, EvidenceDir: request.EvidenceDir, JournalPath: journalPath,
		Source:           DatabaseIdentity{Service: request.SourceService, Database: request.SourceDatabase, Role: request.SourceRole},
		Platform:         DatabaseIdentity{Service: request.TargetService, Database: request.TargetDatabase, Role: request.TargetRole},
		Gateway:          DatabaseIdentity{Service: request.GatewayService, Database: request.GatewayDatabase, Role: request.GatewayRole},
		Maintenance:      DatabaseIdentity{Service: request.MaintenanceService, Database: request.MaintenanceDatabase, Role: request.MaintenanceRole},
		SourceWriterRole: request.SourceWriterRole, PlatformMigratorRole: request.TargetRole, PlatformRuntimeRole: request.TargetRuntimeRole, PlatformWriterRole: request.TargetWriterRole,
		GatewayMigratorRole: request.GatewayRole, GatewayRuntimeRole: request.GatewayRuntimeRole, GatewayWriterRole: request.GatewayWriterRole,
		PriorVerification: priorPlan, ManifestActivated: activatedReplay,
	}
}

func executeActivationLocked(ctx context.Context, request ExecutionRequest, options ExecuteOptions, manifest Manifest) (result ExecutionResult, err error) {
	if manifest.Workflow.Phase != PhaseActivationReady && manifest.Workflow.Phase != PhaseActivated {
		return ExecutionResult{}, fmt.Errorf("activation requires activation-ready preparation; current phase is %s", manifest.Workflow.Phase)
	}
	activatedReplay := manifest.Workflow.Phase == PhaseActivated
	executionStarted := false
	containmentArmed := false
	var earlyContain func(context.Context) error
	if activatedReplay {
		if options.Activation != nil {
			earlyContain = options.Activation.ContainPostGrantFailure
		} else if defaults, defaultsErr := defaultActivationDependenciesForPromotion(ctx, request, options, nil); defaultsErr == nil {
			earlyContain = defaults.ContainPostGrantFailure
		}
	}
	if options.Activation == nil {
		if err := verifyActivationEnvironment(); err != nil {
			return ExecutionResult{}, err
		}
		release, lockErr := acquireActivationPostgresLock(ctx, request)
		if lockErr != nil {
			return ExecutionResult{}, lockErr
		}
		containmentArmed = true
		defer func() {
			if releaseErr := release(ctx); releaseErr != nil {
				result = ExecutionResult{}
				if err != nil {
					err = fmt.Errorf("%v; %w", err, releaseErr)
				} else {
					err = releaseErr
				}
			}
		}()
	} else {
		// Injected activation dependencies are used only by tests that already
		// model exclusive execution without acquiring the real PostgreSQL lock.
		containmentArmed = true
	}
	if activatedReplay {
		defer func() {
			if err == nil || executionStarted || !containmentArmed || earlyContain == nil {
				return
			}
			if containErr := earlyContain(ctx); containErr != nil {
				err = errors.Join(err, fmt.Errorf("activation containment failed: %w", containErr))
			}
		}()
	}
	if manifest.Workflow.Phase == PhaseActivated {
		if err := validateActivatedManifestBinding(manifest, request); err != nil {
			return ExecutionResult{}, err
		}
	}
	journalPath := request.JournalPath
	if strings.TrimSpace(journalPath) == "" {
		journalPath = filepath.Join(request.EvidenceDir, "activation-attempt.json")
	}
	priorPlan, err := buildActivationPriorVerificationPlan(manifest, request)
	if err != nil {
		return ExecutionResult{}, err
	}
	input := buildActivationInput(manifest, request, journalPath, priorPlan, activatedReplay)
	var dependencies ActivationDependencies
	if options.Activation == nil {
		promotion, err := loadVerifiedMigrationPromotion(manifest, request.EvidenceDir)
		if err != nil {
			return ExecutionResult{}, err
		}
		defaults, err := defaultActivationDependenciesForPromotion(ctx, request, options, &promotion)
		if err != nil {
			return ExecutionResult{}, err
		}
		dependencies = defaults
		dependencies.VerifyPriorState = func(ctx context.Context, plan ActivationPriorVerificationPlan, attempt ActivationAttempt) (ActivationPriorVerificationObservation, error) {
			return verifyActivationPriorState(ctx, manifest, request, plan, attempt, options)
		}
		dependencies.RecoverPriorState = func(ctx context.Context, plan ActivationPriorVerificationPlan, attempt ActivationAttempt) (ActivationPriorVerificationObservation, error) {
			return recoverActivationPriorState(ctx, manifest, request, plan, attempt, options)
		}
	} else {
		dependencies = *options.Activation
	}
	dependencies.Now = options.Now
	dependencies.LoadAttempt = func(context.Context) (*ActivationAttempt, error) {
		if manifest.ActivationAttempt == nil {
			return nil, nil
		}
		copy := *manifest.ActivationAttempt
		return &copy, nil
	}
	dependencies.SaveAttempt = func(_ context.Context, attempt ActivationAttempt) error {
		manifest.ActivationAttempt = &attempt
		return writeActivationManifestAtomic(request.ManifestPath, manifest)
	}
	dependencies.CompleteVerifiedAttempt = func(ctx context.Context, _ ActivationInput, attempt ActivationAttempt, inputSHA string) (ExecutionResult, error) {
		return completeActivationManifest(ctx, request, attempt, inputSHA)
	}
	executionStarted = true
	result, err = ExecuteActivation(ctx, input, dependencies)
	if err != nil {
		return ExecutionResult{}, err
	}
	return result, nil
}

func verifyActivationPriorState(ctx context.Context, manifest Manifest, request ExecutionRequest, plan ActivationPriorVerificationPlan, attempt ActivationAttempt, options ExecuteOptions) (ActivationPriorVerificationObservation, error) {
	return activationPriorStateObservation(ctx, manifest, request, plan, attempt, options, true)
}

func recoverActivationPriorState(ctx context.Context, manifest Manifest, request ExecutionRequest, plan ActivationPriorVerificationPlan, attempt ActivationAttempt, options ExecuteOptions) (ActivationPriorVerificationObservation, error) {
	return activationPriorStateObservation(ctx, manifest, request, plan, attempt, options, false)
}

func activationPriorStateObservation(ctx context.Context, manifest Manifest, request ExecutionRequest, plan ActivationPriorVerificationPlan, attempt ActivationAttempt, options ExecuteOptions, runCurrentProbes bool) (ActivationPriorVerificationObservation, error) {
	expectedPlan, err := buildActivationPriorVerificationPlan(manifest, request)
	if err != nil || !reflect.DeepEqual(plan, expectedPlan) {
		return ActivationPriorVerificationObservation{}, fmt.Errorf("activation prior replay plan changed")
	}
	planSHA, err := activationPriorPlanSHA(plan)
	if err != nil {
		return ActivationPriorVerificationObservation{}, err
	}
	if err := revalidateActivationPriorEvidence(manifest, request); err != nil {
		return ActivationPriorVerificationObservation{}, err
	}
	run := options.Run
	if run == nil {
		run = runCommand
	}
	type artifactFact struct {
		Operation Operation `json:"operation"`
		Path      string    `json:"path,omitempty"`
		SHA256    string    `json:"sha256,omitempty"`
	}
	type currentPostconditionFact struct {
		Sequence   int    `json:"sequence"`
		Executable string `json:"executable"`
		ArgvSHA256 string `json:"argvSha256"`
		InputSHA   string `json:"inputSha256,omitempty"`
		Passed     bool   `json:"passed"`
	}
	artifacts := make([]artifactFact, 0, len(plan.Events))
	currentProofs := make([]currentPostconditionFact, 0, validateCommandCount)
	for _, prior := range plan.Events {
		if err := requireActivationPriorRegularFile(prior.EvidencePath); err != nil {
			return ActivationPriorVerificationObservation{}, fmt.Errorf("activation prior %s proof is unsafe", prior.Operation)
		}
		_, proofSHA, readErr := readProof(prior.EvidencePath)
		if readErr != nil || proofSHA != prior.ProofSHA256 {
			return ActivationPriorVerificationObservation{}, fmt.Errorf("activation prior %s proof changed", prior.Operation)
		}
		if prior.ArtifactPath != "" {
			if err := requireActivationPriorRegularFile(prior.ArtifactPath); err != nil {
				return ActivationPriorVerificationObservation{}, fmt.Errorf("activation prior %s artifact is unsafe", prior.Operation)
			}
			artifactSHA, digestErr := digestFile(prior.ArtifactPath, true)
			if digestErr != nil || artifactSHA != prior.ArtifactSHA256 {
				return ActivationPriorVerificationObservation{}, fmt.Errorf("activation prior %s artifact changed", prior.Operation)
			}
			artifacts = append(artifacts, artifactFact{Operation: prior.Operation, Path: prior.ArtifactPath, SHA256: artifactSHA})
		}
		replayRequest := request
		replayRequest.Operation = prior.Operation
		replayRequest.ID, replayRequest.ArtifactPath, replayRequest.BackfillPath, replayRequest.Verifier, replayRequest.Reason = "", "", "", "", ""
		switch prior.Operation {
		case Backup:
			replayRequest.ID, replayRequest.ArtifactPath = request.BackupID, prior.ArtifactPath
		case Restore:
			replayRequest.ArtifactPath, replayRequest.Verifier = prior.ArtifactPath, request.Verifier
		case FinalDelta:
			replayRequest.ArtifactPath = prior.ArtifactPath
		case Backfill:
			replayRequest.BackfillPath = request.BackfillPath
		case Validate:
			replayRequest.Verifier = request.Verifier
		}
		commands, applyRequest, _, inputSHA, planErr := executionPlan(manifest, replayRequest)
		if planErr != nil {
			return ActivationPriorVerificationObservation{}, fmt.Errorf("activation prior %s plan rejected: %w", prior.Operation, planErr)
		}
		if prior.Operation == Backfill {
			acceptedPath := filepath.Join(request.EvidenceDir, ".accepted-mapping-"+prior.ArtifactSHA256+".json")
			if err := requireActivationPriorRegularFile(acceptedPath); err != nil {
				return ActivationPriorVerificationObservation{}, fmt.Errorf("activation prior backfill accepted snapshot is unsafe")
			}
			acceptedSHA, digestErr := digestFile(acceptedPath, true)
			if digestErr != nil || acceptedSHA != prior.ArtifactSHA256 {
				return ActivationPriorVerificationObservation{}, fmt.Errorf("activation prior backfill accepted snapshot changed")
			}
			for index := range commands {
				if filepath.Base(commands[index].Name) == "account-route-backfill" {
					replaceCommandArg(commands[index].Args, "--mapping", acceptedPath)
				}
			}
			applyRequest.ArtifactPath = acceptedPath
			applyRequest.acceptedArtifactSHA = prior.ArtifactSHA256
		}
		replay, replayErr := replayExecution(manifest, request.EvidenceDir, applyRequest, commands, inputSHA)
		if replayErr != nil || replay == nil || !replay.Replayed || replay.EvidencePath != prior.EvidencePath {
			if replayErr == nil {
				replayErr = fmt.Errorf("event replay was not authoritative")
			}
			return ActivationPriorVerificationObservation{}, fmt.Errorf("activation prior %s replay failed: %w", prior.Operation, replayErr)
		}
		if prior.Operation != Validate {
			continue
		}
		if len(commands) != validateCommandCount {
			return ActivationPriorVerificationObservation{}, fmt.Errorf("activation prior validate replay does not contain %d current probes", validateCommandCount)
		}
		for index, command := range commands {
			if runCurrentProbes {
				result, runErr := run(ctx, command)
				if runErr != nil || verifyCommandPostcondition(Validate, index, command, result) != nil {
					return ActivationPriorVerificationObservation{}, fmt.Errorf("activation prior validate current postcondition %d failed", index+1)
				}
			}
			identity := commandDigest(command, nil)
			currentProofs = append(currentProofs, currentPostconditionFact{
				Sequence: index + 1, Executable: identity.Executable, ArgvSHA256: identity.ArgvSHA256, InputSHA: identity.InputSHA, Passed: true,
			})
		}
	}
	if len(currentProofs) != validateCommandCount {
		return ActivationPriorVerificationObservation{}, fmt.Errorf("activation prior validate current postconditions are incomplete")
	}
	artifactData, err := json.Marshal(artifacts)
	if err != nil {
		return ActivationPriorVerificationObservation{}, fmt.Errorf("encode activation prior artifact facts: %w", err)
	}
	currentData, err := json.Marshal(currentProofs)
	if err != nil {
		return ActivationPriorVerificationObservation{}, fmt.Errorf("encode activation prior current postconditions: %w", err)
	}
	return ActivationPriorVerificationObservation{
		AttemptID: attempt.ID, PlanSHA256: planSHA, ArtifactSetSHA256: sha256Hex(artifactData), CurrentPostconditionsSHA256: sha256Hex(currentData),
	}, nil
}

func requireActivationPriorRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe activation prior file")
	}
	return nil
}

func buildActivationPriorVerificationPlan(manifest Manifest, request ExecutionRequest) (ActivationPriorVerificationPlan, error) {
	if request.BackupID == "" || request.BackupArtifactPath == "" || request.FinalDeltaArtifactPath == "" || request.BackfillPath == "" || request.Verifier == "" {
		return ActivationPriorVerificationPlan{}, fmt.Errorf("activation prior replay inputs are required")
	}
	if manifest.BackupID == nil || *manifest.BackupID != request.BackupID || manifest.BackupSHA256 == nil || manifest.FinalDeltaSHA256 == nil {
		return ActivationPriorVerificationPlan{}, fmt.Errorf("activation prior replay inputs do not match manifest artifacts")
	}
	backupPath, err := filepath.Abs(request.BackupArtifactPath)
	if err != nil {
		return ActivationPriorVerificationPlan{}, fmt.Errorf("activation prior backup path is invalid")
	}
	deltaPath, err := filepath.Abs(request.FinalDeltaArtifactPath)
	if err != nil {
		return ActivationPriorVerificationPlan{}, fmt.Errorf("activation prior final delta path is invalid")
	}
	backfillPath, err := filepath.Abs(request.BackfillPath)
	if err != nil {
		return ActivationPriorVerificationPlan{}, fmt.Errorf("activation prior backfill path is invalid")
	}
	evidenceDir, err := filepath.Abs(request.EvidenceDir)
	if err != nil {
		return ActivationPriorVerificationPlan{}, fmt.Errorf("activation prior evidence directory is invalid")
	}
	plan := ActivationPriorVerificationPlan{SchemaVersion: 1, MigrationID: manifest.MigrationID}
	counts := map[Operation]int{}
	for _, event := range manifest.Workflow.Events {
		switch event.Operation {
		case Backup, Restore, FinalDelta, Fence, Backfill, Validate:
		default:
			continue
		}
		if event.Sequence <= 0 || event.Epoch <= 0 || !validSHA256(event.ProofSHA) {
			return ActivationPriorVerificationPlan{}, fmt.Errorf("activation prior %s event binding is invalid", event.Operation)
		}
		prior := ActivationPriorEvent{
			Sequence: event.Sequence, Epoch: event.Epoch, Operation: event.Operation, Name: event.Name,
			EvidencePath: filepath.Join(evidenceDir, fmt.Sprintf("%03d-%s.json", event.Sequence, event.Operation)),
			ProofSHA256:  event.ProofSHA, ArtifactSHA256: event.ArtifactSHA, InputSHA256: event.InputSHA,
		}
		switch event.Operation {
		case Backup, Restore:
			prior.ArtifactPath = backupPath
			if event.ArtifactSHA != *manifest.BackupSHA256 {
				return ActivationPriorVerificationPlan{}, fmt.Errorf("activation prior backup/restore artifact binding mismatch")
			}
		case FinalDelta:
			prior.ArtifactPath = deltaPath
			if event.ArtifactSHA != *manifest.FinalDeltaSHA256 {
				return ActivationPriorVerificationPlan{}, fmt.Errorf("activation prior final delta artifact binding mismatch")
			}
		case Backfill:
			prior.ArtifactPath = backfillPath
			if event.Name == "" || filepath.Base(backfillPath) != event.Name || !validSHA256(event.ArtifactSHA) {
				return ActivationPriorVerificationPlan{}, fmt.Errorf("activation prior backfill artifact binding mismatch")
			}
		case Fence, Validate:
			if event.ArtifactSHA != "" {
				return ActivationPriorVerificationPlan{}, fmt.Errorf("activation prior %s event has unexpected artifact", event.Operation)
			}
		}
		counts[event.Operation]++
		plan.Events = append(plan.Events, prior)
	}
	for _, operation := range []Operation{Backup, Restore, FinalDelta, Fence, Backfill, Validate} {
		if counts[operation] != 1 {
			return ActivationPriorVerificationPlan{}, fmt.Errorf("activation prior replay requires exactly one %s event", operation)
		}
	}
	state := struct {
		MigrationID string                 `json:"migrationId"`
		Epoch       int                    `json:"epoch"`
		Phase       Phase                  `json:"phase"`
		Events      []ActivationPriorEvent `json:"events"`
	}{MigrationID: manifest.MigrationID, Epoch: manifest.Workflow.Epoch, Phase: PhaseActivationReady, Events: plan.Events}
	data, err := json.Marshal(state)
	if err != nil {
		return ActivationPriorVerificationPlan{}, fmt.Errorf("encode activation prior manifest state: %w", err)
	}
	plan.ManifestStateSHA256 = sha256Hex(data)
	return plan, nil
}

func revalidateActivationPriorEvidence(manifest Manifest, request ExecutionRequest) error {
	importCommit := ""
	if manifest.ImportCommit != nil {
		importCommit = *manifest.ImportCommit
	}
	if err := VerifyEvidence(EvidenceVerificationRequest{ManifestPath: request.ManifestPath, EvidenceDir: request.EvidenceDir, ImportCommit: importCommit}); err != nil {
		return fmt.Errorf("activation prior evidence revalidation failed: %w", err)
	}
	for _, event := range manifest.Workflow.Events {
		if !operationNeedsProof(event.Operation) || event.Operation == Activate || event.Operation == RecordActivation {
			continue
		}
		path := filepath.Join(request.EvidenceDir, fmt.Sprintf("%03d-%s.json", event.Sequence, event.Operation))
		data, digest, err := readProof(path)
		if err != nil || digest != event.ProofSHA {
			return fmt.Errorf("activation prior %s evidence changed", event.Operation)
		}
		if err := rejectDuplicateKeys(data); err != nil {
			return fmt.Errorf("activation prior %s evidence is invalid", event.Operation)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		var proof operationProof
		if err := decoder.Decode(&proof); err != nil || decoder.Decode(&struct{}{}) != io.EOF || proof.SchemaVersion != 1 || proof.Operation != event.Operation || proof.Status != "pass" || proof.MigrationID != manifest.MigrationID || proof.GeneratedBy != evidenceGenerator || proof.InputSHA != event.InputSHA || !validSHA256(proof.PreStateSHA) || len(proof.Commands) == 0 {
			return fmt.Errorf("activation prior %s evidence is invalid", event.Operation)
		}
		for _, command := range proof.Commands {
			if command.Executable == "" || !validSHA256(command.ArgvSHA256) || !validSHA256(command.OutputSHA) || (command.InputSHA != "" && !validSHA256(command.InputSHA)) {
				return fmt.Errorf("activation prior %s command evidence is invalid", event.Operation)
			}
		}
	}
	return nil
}

func validateActivatedManifestBinding(manifest Manifest, request ExecutionRequest) error {
	if manifest.ActivationAttempt == nil || manifest.ActivationAttempt.Boundary != ActivationVerified || manifest.ActivationAttempt.Observations == nil {
		return fmt.Errorf("activated manifest is missing its verified attempt")
	}
	var activationEvent *Event
	for index := range manifest.Workflow.Events {
		if manifest.Workflow.Events[index].Operation == Activate {
			if activationEvent != nil {
				return fmt.Errorf("activated manifest has ambiguous activation evidence")
			}
			activationEvent = &manifest.Workflow.Events[index]
		}
	}
	if activationEvent == nil || activationEvent.ProofSHA != manifest.ActivationAttempt.ProofSHA256 || activationEvent.InputSHA != manifest.ActivationAttempt.InputSHA256 {
		return fmt.Errorf("activated manifest attempt/event evidence binding mismatch")
	}
	expectedPath := filepath.Join(request.EvidenceDir, fmt.Sprintf("%03d-%s.json", activationEvent.Sequence, Activate))
	actualPath, actualErr := filepath.Abs(manifest.ActivationAttempt.EvidencePath)
	expectedPath, expectedErr := filepath.Abs(expectedPath)
	if actualErr != nil || expectedErr != nil || actualPath != expectedPath {
		return fmt.Errorf("activated manifest evidence path binding mismatch")
	}
	data, err := os.ReadFile(actualPath)
	if err != nil {
		return fmt.Errorf("read activated manifest evidence: %w", err)
	}
	var proof operationProof
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proof); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("activated manifest evidence is invalid")
	}
	preActivation := manifest
	preActivation.Workflow.Events = append([]Event(nil), manifest.Workflow.Events[:len(manifest.Workflow.Events)-1]...)
	preActivation.Workflow.Phase = PhaseActivationReady
	preActivation.TargetActivationAt, preActivation.CutoverAt = nil, nil
	preActivation.OneWriter.TargetCanonicalWritesEnabled = false
	if preActivation.ActivationAttempt != nil {
		copy := *preActivation.ActivationAttempt
		copy.Boundary, copy.EvidencePath, copy.ProofSHA256, copy.Observations = ActivationServicesStarted, "", "", nil
		preActivation.ActivationAttempt = &copy
	}
	if proof.Subject != proofStateDigest(preActivation) {
		return fmt.Errorf("activated manifest evidence subject binding mismatch")
	}
	return nil
}

func completeActivationManifest(_ context.Context, request ExecutionRequest, attempt ActivationAttempt, inputSHA string) (ExecutionResult, error) {
	manifestData, err := os.ReadFile(request.ManifestPath)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("read activation manifest: %w", err)
	}
	manifest, err := DecodeManifest(manifestData)
	if err != nil {
		return ExecutionResult{}, err
	}
	if manifest.Workflow.Phase != PhaseActivationReady || manifest.ActivationAttempt == nil || manifest.ActivationAttempt.ID != attempt.ID || manifest.ActivationAttempt.Boundary != ActivationServicesStarted {
		return ExecutionResult{}, fmt.Errorf("activation manifest is not at the verified completion boundary")
	}
	proof := operationProof{
		SchemaVersion: 1, Operation: Activate, Status: "pass", MigrationID: manifest.MigrationID,
		Subject: proofStateDigest(manifest), Checks: []string{"source-fenced", "gateway-promoted", "writers-granted", "connect-restored", "services-started", "activation-verified"},
		GeneratedBy: evidenceGenerator, PreStateSHA: sha256Hex(manifestData), InputSHA: inputSHA,
		Commands:               []commandEvidence{{Executable: "canonical-activation", ArgvSHA256: inputSHA, OutputSHA: sha256Hex([]byte("verified"))}},
		ActivationObservations: attempt.Observations,
	}
	path, err := writeExecutionEvidence(request.EvidenceDir, len(manifest.Workflow.Events)+1, Activate, proof)
	if err != nil {
		return ExecutionResult{}, err
	}
	_, proofSHA, err := readProof(path)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("read activation evidence after publication: %w", err)
	}
	attempt.EvidencePath, attempt.ProofSHA256 = path, proofSHA
	manifest.ActivationAttempt = &attempt
	applyRequest := Request{Operation: Activate, ProofPath: path, ActivationAt: attempt.StartedAt.Format(time.RFC3339Nano), CutoverAt: attempt.UpdatedAt.Format(time.RFC3339Nano), generatedEvidence: &proof, capturedInputSHA: inputSHA}
	artifactSHA, verifiedProofSHA, err := requestDigests(manifest, applyRequest)
	if err != nil {
		return ExecutionResult{}, err
	}
	if verifiedProofSHA != proofSHA {
		return ExecutionResult{}, fmt.Errorf("activation evidence digest changed during completion")
	}
	if err := validatePredecessor(manifest, Activate); err != nil {
		return ExecutionResult{}, err
	}
	at := attempt.UpdatedAt.UTC().Format(time.RFC3339Nano)
	if err := transition(&manifest, applyRequest, artifactSHA, at); err != nil {
		return ExecutionResult{}, err
	}
	if problems := validateActivationOrder(manifest); len(problems) > 0 {
		return ExecutionResult{}, fmt.Errorf("migration evidence order rejected: %s", strings.Join(problems, "; "))
	}
	manifest.Workflow.Events = append(manifest.Workflow.Events, Event{
		Sequence: len(manifest.Workflow.Events) + 1, Epoch: manifest.Workflow.Epoch, Operation: Activate, At: at,
		Fingerprint: requestFingerprint(applyRequest, artifactSHA, proofSHA), ArtifactSHA: artifactSHA, InputSHA: inputSHA, ProofSHA: proofSHA,
		ActivationAt: applyRequest.ActivationAt, CutoverAt: applyRequest.CutoverAt,
	})
	if err := validateWorkflow(manifest); err != nil {
		return ExecutionResult{}, err
	}
	if err := writeActivationManifestAtomic(request.ManifestPath, manifest); err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{Result: Result{Phase: PhaseActivated}, EvidencePath: path, InputSHA256: inputSHA}, nil
}

type backfillPromotionOutput struct {
	SchemaVersion               int                                `json:"schemaVersion"`
	Accounts                    int                                `json:"accounts"`
	Routes                      int                                `json:"routes"`
	AccountIDs                  []string                           `json:"accountIds"`
	PromotionRequest            storepkg.MigrationPromotionRequest `json:"promotionRequest"`
	PromotionOperationSetSHA256 string                             `json:"promotionOperationSetSha256"`
}

func decodeBackfillPromotionOutput(data []byte, migrationID, fenceSHA string) (storepkg.MigrationPromotionRequest, error) {
	if err := rejectDuplicateKeys(data); err != nil {
		if strings.Contains(err.Error(), "multiple JSON values") {
			return storepkg.MigrationPromotionRequest{}, fmt.Errorf("backfill promotion output has trailing value")
		}
		return storepkg.MigrationPromotionRequest{}, fmt.Errorf("decode backfill promotion output: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var output backfillPromotionOutput
	if err := decoder.Decode(&output); err != nil {
		return storepkg.MigrationPromotionRequest{}, fmt.Errorf("decode backfill promotion output: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return storepkg.MigrationPromotionRequest{}, fmt.Errorf("backfill promotion output has trailing value")
	} else if !errors.Is(err, io.EOF) {
		return storepkg.MigrationPromotionRequest{}, fmt.Errorf("decode backfill promotion output trailing value")
	}
	if output.SchemaVersion != 1 || output.Accounts < 0 || output.Routes < output.Accounts || len(output.AccountIDs) != output.Accounts || len(output.PromotionRequest.Items) != output.Accounts {
		return storepkg.MigrationPromotionRequest{}, fmt.Errorf("backfill promotion output shape is invalid")
	}
	if output.PromotionRequest.MigrationID != migrationID {
		return storepkg.MigrationPromotionRequest{}, fmt.Errorf("backfill promotion migration mismatch")
	}
	if output.PromotionRequest.FenceSHA256 != fenceSHA {
		return storepkg.MigrationPromotionRequest{}, fmt.Errorf("backfill promotion fence mismatch")
	}
	for index := range output.AccountIDs {
		if output.AccountIDs[index] != string(output.PromotionRequest.Items[index].AccountID) || (index > 0 && output.AccountIDs[index-1] >= output.AccountIDs[index]) {
			return storepkg.MigrationPromotionRequest{}, fmt.Errorf("backfill promotion account set is invalid")
		}
	}
	digest, err := output.PromotionRequest.OperationSetSHA256()
	if err != nil || digest != output.PromotionOperationSetSHA256 {
		return storepkg.MigrationPromotionRequest{}, fmt.Errorf("backfill promotion digest mismatch")
	}
	return output.PromotionRequest, nil
}

func loadVerifiedMigrationPromotion(manifest Manifest, evidenceDir string) (verifiedMigrationPromotion, error) {
	if strings.TrimSpace(evidenceDir) == "" {
		return verifiedMigrationPromotion{}, fmt.Errorf("verified backfill promotion evidence is required")
	}
	fenceSHA := ""
	for _, event := range manifest.Workflow.Events {
		if event.Operation == Fence {
			if fenceSHA != "" || !validSHA256(event.ProofSHA) {
				return verifiedMigrationPromotion{}, fmt.Errorf("verified fence evidence is ambiguous")
			}
			fenceSHA = event.ProofSHA
		}
	}
	if fenceSHA == "" {
		return verifiedMigrationPromotion{}, fmt.Errorf("verified fence evidence is required")
	}
	var promotion *verifiedMigrationPromotion
	var priorBackfills []string
	for _, event := range manifest.Workflow.Events {
		if event.Operation != Backfill {
			continue
		}
		path := filepath.Join(evidenceDir, fmt.Sprintf("%03d-%s.json", event.Sequence, event.Operation))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return verifiedMigrationPromotion{}, fmt.Errorf("verified backfill evidence is unavailable or unsafe")
		}
		data, digest, err := readProof(path)
		if err != nil || digest != event.ProofSHA {
			return verifiedMigrationPromotion{}, fmt.Errorf("verified backfill evidence changed")
		}
		if err := rejectDuplicateKeys(data); err != nil {
			return verifiedMigrationPromotion{}, fmt.Errorf("decode verified backfill evidence: %w", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		var proof operationProof
		if err := decoder.Decode(&proof); err != nil {
			return verifiedMigrationPromotion{}, fmt.Errorf("decode verified backfill evidence: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); err == nil || !errors.Is(err, io.EOF) {
			return verifiedMigrationPromotion{}, fmt.Errorf("decode verified backfill evidence: trailing value")
		}
		preBackfill := manifest
		preBackfill.Workflow.Epoch = event.Epoch
		preBackfill.Workflow.Phase = PhaseFenced
		if len(priorBackfills) != 0 {
			preBackfill.Workflow.Phase = PhaseBackfilled
		}
		preBackfill.BackfillScripts = append([]string(nil), priorBackfills...)
		preBackfill.ValidationAt = nil
		expectedSubject := proofStateDigest(preBackfill) + "/" + event.Name + "/" + event.ArtifactSHA
		if proof.SchemaVersion != 1 || proof.Operation != Backfill || proof.Status != "pass" || proof.MigrationID != manifest.MigrationID || proof.GeneratedBy != evidenceGenerator || !validSHA256(proof.PreStateSHA) || proof.InputSHA != event.InputSHA || proof.Subject != expectedSubject || !sameStringSet(proof.Checks, []string{"connected-identities-verified", "source-high-water-equal", "platform-schema-migrated", "mapping-content-stable", "durable-idempotent-saga", "pending-only"}) {
			return verifiedMigrationPromotion{}, fmt.Errorf("verified backfill evidence is invalid")
		}
		priorBackfills = append(priorBackfills, event.Name)
		backfillCommand := 0
		for _, command := range proof.Commands {
			if command.Executable == "" || !validSHA256(command.ArgvSHA256) || !validSHA256(command.OutputSHA) || (command.InputSHA != "" && !validSHA256(command.InputSHA)) {
				return verifiedMigrationPromotion{}, fmt.Errorf("verified backfill command evidence is invalid")
			}
			if filepath.Base(command.Executable) == "account-route-backfill" {
				backfillCommand++
			}
		}
		if proof.PromotionRequest == nil && proof.PromotionOperationSetSHA256 == "" {
			continue
		}
		if promotion != nil || proof.PromotionRequest == nil || backfillCommand != 1 || proof.PromotionRequest.MigrationID != manifest.MigrationID || proof.PromotionRequest.FenceSHA256 != fenceSHA {
			return verifiedMigrationPromotion{}, fmt.Errorf("verified backfill promotion evidence is ambiguous or invalid")
		}
		operationSetSHA, err := proof.PromotionRequest.OperationSetSHA256()
		if err != nil || operationSetSHA != proof.PromotionOperationSetSHA256 {
			return verifiedMigrationPromotion{}, fmt.Errorf("verified backfill promotion digest mismatch")
		}
		for index := 1; index < len(proof.PromotionRequest.Items); index++ {
			if proof.PromotionRequest.Items[index-1].AccountID >= proof.PromotionRequest.Items[index].AccountID {
				return verifiedMigrationPromotion{}, fmt.Errorf("verified backfill promotion set is not canonical")
			}
		}
		copy := verifiedMigrationPromotion{Request: *proof.PromotionRequest, OperationSetSHA256: operationSetSHA}
		copy.Request.Items = append([]storepkg.MigrationPromotionItem(nil), proof.PromotionRequest.Items...)
		promotion = &copy
	}
	if promotion == nil {
		return verifiedMigrationPromotion{}, fmt.Errorf("verified backfill promotion evidence is required")
	}
	return *promotion, nil
}

func defaultActivationDependencies(ctx context.Context, request ExecutionRequest, options ExecuteOptions) (ActivationDependencies, error) {
	return defaultActivationDependenciesForPromotion(ctx, request, options, nil)
}

func observeActivationGatewayPromotionHead(ctx context.Context, store migrationPromotionStore, promotion verifiedMigrationPromotion) (ActivationGatewayHeadObservation, error) {
	states, err := store.ListRuntimeStateForRecovery(ctx)
	if err != nil {
		return ActivationGatewayHeadObservation{}, fmt.Errorf("read Gateway runtime heads: %w", err)
	}
	expected := make(map[string]storepkg.MigrationPromotionItem, len(promotion.Request.Items))
	for _, item := range promotion.Request.Items {
		expected[string(item.AccountID)] = item
	}
	matched := make(map[string]struct{}, len(expected))
	seen := make(map[string]struct{}, len(states))
	activeItems := make([]storepkg.MigrationPromotionItem, 0, len(states))
	pendingItems := make([]storepkg.MigrationPromotionItem, 0, len(expected))
	pendingCount, activeCount := 0, 0
	for _, state := range states {
		accountID := string(state.AccountID)
		if accountID == "" || state.Tombstoned || len(state.Retiring) != 0 {
			return ActivationGatewayHeadObservation{}, fmt.Errorf("Gateway runtime head contains conflicting state")
		}
		if _, duplicate := seen[accountID]; duplicate {
			return ActivationGatewayHeadObservation{}, fmt.Errorf("Gateway runtime head is duplicated")
		}
		seen[accountID] = struct{}{}
		wanted, required := expected[accountID]
		var activeItem *storepkg.MigrationPromotionItem
		if state.Active != nil {
			if !required {
				return ActivationGatewayHeadObservation{}, fmt.Errorf("Gateway active head conflicts with promotion set")
			}
			if string(state.Active.AccountID) != accountID {
				return ActivationGatewayHeadObservation{}, fmt.Errorf("Gateway active head identity changed")
			}
			item, itemErr := activationMigrationPromotionItem(state.Active.DesiredSnapshot)
			if itemErr != nil {
				return ActivationGatewayHeadObservation{}, itemErr
			}
			activeItem = &item
			activeItems = append(activeItems, item)
		}
		if state.Pending != nil {
			if state.Active != nil || string(state.Pending.AccountID) != accountID || !required {
				return ActivationGatewayHeadObservation{}, fmt.Errorf("Gateway pending head conflicts with promotion set")
			}
			item, itemErr := activationMigrationPromotionItem(state.Pending.DesiredSnapshot)
			if itemErr != nil || item != wanted {
				return ActivationGatewayHeadObservation{}, fmt.Errorf("Gateway pending head does not match promotion set")
			}
			pendingItems = append(pendingItems, item)
			pendingCount++
			matched[accountID] = struct{}{}
			continue
		}
		if activeItem == nil {
			return ActivationGatewayHeadObservation{}, fmt.Errorf("Gateway runtime head is missing state")
		}
		if required {
			if *activeItem != wanted {
				return ActivationGatewayHeadObservation{}, fmt.Errorf("Gateway active head does not match promotion set")
			}
			activeCount++
			matched[accountID] = struct{}{}
		}
	}
	if len(matched) != len(expected) || (pendingCount != 0 && activeCount != 0) {
		return ActivationGatewayHeadObservation{}, fmt.Errorf("Gateway runtime head does not contain one exact promotion state")
	}
	for _, items := range [][]storepkg.MigrationPromotionItem{activeItems, pendingItems} {
		validation := storepkg.MigrationPromotionRequest{MigrationID: promotion.Request.MigrationID, FenceSHA256: promotion.Request.FenceSHA256, Items: items}
		if _, err := validation.OperationSetSHA256(); err != nil {
			return ActivationGatewayHeadObservation{}, fmt.Errorf("Gateway runtime head is invalid")
		}
	}
	digest := storepkg.MigrationPromotionDigest{
		PendingSHA256:      activationMigrationStateSHA256("pending", pendingItems),
		ActiveSHA256:       activationMigrationStateSHA256("active", activeItems),
		OperationSetSHA256: promotion.OperationSetSHA256,
	}
	return activationGatewayHeadObservation(digest), nil
}

func activationMigrationPromotionItem(snapshot storepkg.DesiredSnapshot) (storepkg.MigrationPromotionItem, error) {
	routeSetSHA, err := storepkg.MigrationRouteSetSHA256(snapshot.Routes)
	if err != nil {
		return storepkg.MigrationPromotionItem{}, fmt.Errorf("Gateway runtime head route set is invalid")
	}
	item := storepkg.MigrationPromotionItem{
		AccountID: snapshot.AccountID, OperationID: snapshot.OperationID.String(), DesiredStateRevision: int64(snapshot.Fence.DesiredStateRevision),
		RuntimeGeneration: int64(snapshot.Fence.RuntimeGeneration), CredentialVersion: int64(snapshot.Fence.CredentialVersion),
		RouteSetSHA256: routeSetSHA, InputSHA256: snapshot.RequestDigest,
	}
	return item, nil
}

func activationMigrationStateSHA256(state string, items []storepkg.MigrationPromotionItem) string {
	items = append([]storepkg.MigrationPromotionItem(nil), items...)
	sort.Slice(items, func(i, j int) bool { return items[i].AccountID < items[j].AccountID })
	encoded, _ := json.Marshal(struct {
		State string                            `json:"state"`
		Items []storepkg.MigrationPromotionItem `json:"items"`
	}{state, items})
	return sha256Hex(encoded)
}

func defaultActivationDependenciesForPromotion(_ context.Context, request ExecutionRequest, options ExecuteOptions, promotion *verifiedMigrationPromotion) (ActivationDependencies, error) {
	run := options.Run
	if run == nil {
		run = runCommand
	}
	psql := func(service, database, script string, variables ...string) Command {
		args := []string{"--no-psqlrc", "--quiet", "--set", "ON_ERROR_STOP=1", "--dbname", "service=" + service + " dbname=" + database, "--tuples-only", "--no-align"}
		for _, variable := range variables {
			args = append(args, "--set", variable)
		}
		args = append(args, "--file", "-")
		return Command{Name: "psql", Args: args, Stdin: []byte(script)}
	}
	runChecked := func(ctx context.Context, command Command, expected string) (string, error) {
		result, err := run(ctx, command)
		if err != nil {
			return "", err
		}
		output := strings.TrimSpace(string(result.Output))
		if expected != "" && output != expected {
			return "", fmt.Errorf("postcondition mismatch")
		}
		return output, nil
	}
	sourceState := psql(request.SourceService, request.SourceDatabase, `SELECT (
  current_user=:'source_role'
  AND current_database()=:'source_database'
  AND EXISTS (SELECT 1 FROM pg_roles writer WHERE writer.rolname=:'writer_role' AND NOT writer.rolcanlogin)
  AND NOT EXISTS (SELECT 1 FROM pg_stat_activity activity WHERE activity.usename=:'writer_role' AND activity.pid<>pg_backend_pid())
)::text;
`, "source_role="+request.SourceRole, "source_database="+request.SourceDatabase, "writer_role="+request.SourceWriterRole)
	preGrantMembership := psql(request.MaintenanceService, request.MaintenanceDatabase, `SELECT pg_has_role(:'platform_runtime',:'platform_writer','USAGE')::text || '|' || pg_has_role(:'gateway_runtime',:'gateway_writer','USAGE')::text;`+"\n", "platform_runtime="+request.TargetRuntimeRole, "platform_writer="+request.TargetWriterRole, "gateway_runtime="+request.GatewayRuntimeRole, "gateway_writer="+request.GatewayWriterRole)
	dmlProbe := func(database, runtime string, matrix authority.Matrix, expectAllowed bool) Command {
		return psql(request.MaintenanceService, database, activationCanonicalDMLProbe(matrix, expectAllowed), "maintenance_role="+request.MaintenanceRole, "runtime_role="+runtime)
	}
	platformPreGrantProbe := dmlProbe(request.TargetDatabase, request.TargetRuntimeRole, authority.PlatformV15(), false)
	gatewayPreGrantProbe := dmlProbe(request.GatewayDatabase, request.GatewayRuntimeRole, authority.GatewayV6(), false)
	platformPostGrantProbe := dmlProbe(request.TargetDatabase, request.TargetRuntimeRole, authority.PlatformV15(), true)
	gatewayPostGrantProbe := dmlProbe(request.GatewayDatabase, request.GatewayRuntimeRole, authority.GatewayV6(), true)
	targetVariables := []string{"platform_database=" + request.TargetDatabase, "platform_runtime=" + request.TargetRuntimeRole, "gateway_database=" + request.GatewayDatabase, "gateway_runtime=" + request.GatewayRuntimeRole}
	targetBoundaryVariables := []string{
		"maintenance_role=" + request.MaintenanceRole,
		"platform_database=" + request.TargetDatabase, "platform_migrator=" + request.TargetRole, "platform_runtime=" + request.TargetRuntimeRole,
		"gateway_database=" + request.GatewayDatabase, "gateway_migrator=" + request.GatewayRole, "gateway_runtime=" + request.GatewayRuntimeRole,
	}
	fenceScript := `BEGIN;
SELECT (
  current_user=:'maintenance_role'
  AND 3=(
    SELECT count(*) FROM pg_auth_members membership
    JOIN pg_roles granted ON granted.oid=membership.roleid
    JOIN pg_roles member ON member.oid=membership.member
    WHERE member.rolname=:'maintenance_role'
      AND (granted.rolname IN (:'platform_migrator',:'gateway_migrator') OR granted.rolname='pg_signal_backend')
      AND NOT membership.admin_option
      AND NOT membership.inherit_option
      AND membership.set_option
  )
) AS activation_fence_roles_ok
\gset
\if :activation_fence_roles_ok
\else
ROLLBACK;
DO $activation_fence_role_guard$
BEGIN
  RAISE EXCEPTION 'activation fence role memberships are not canonical';
END
$activation_fence_role_guard$;
\endif
SET LOCAL ROLE :"platform_migrator";
REVOKE CONNECT ON DATABASE :"platform_database" FROM :"platform_runtime";
RESET ROLE;
SET LOCAL ROLE :"gateway_migrator";
REVOKE CONNECT ON DATABASE :"gateway_database" FROM :"gateway_runtime";
RESET ROLE;
SET LOCAL ROLE "pg_signal_backend";
SELECT NOT EXISTS (
  SELECT 1 FROM pg_stat_activity activity
  WHERE activity.pid<>pg_backend_pid()
    AND activity.usename IN (:'platform_runtime',:'gateway_runtime')
    AND NOT (
      (activity.datname=:'platform_database' AND activity.usename=:'platform_runtime')
      OR (activity.datname=:'gateway_database' AND activity.usename=:'gateway_runtime')
    )
) AS activation_owned_sessions
\gset
\if :activation_owned_sessions
\else
ROLLBACK;
DO $activation_unowned_session$
BEGIN
  RAISE EXCEPTION 'unowned target runtime session';
END
$activation_unowned_session$;
\endif
SELECT COALESCE(bool_and(pg_terminate_backend(activity.pid,5000)),true) AS activation_sessions_terminated
FROM pg_stat_activity activity
WHERE activity.pid<>pg_backend_pid() AND (
  (activity.datname=:'platform_database' AND activity.usename=:'platform_runtime')
  OR (activity.datname=:'gateway_database' AND activity.usename=:'gateway_runtime')
)
\gset
\if :activation_sessions_terminated
\else
ROLLBACK;
DO $activation_unterminated_session$
BEGIN
  RAISE EXCEPTION 'unterminated target runtime session';
END
$activation_unterminated_session$;
\endif
SELECT pg_stat_clear_snapshot();
SELECT NOT EXISTS (
  SELECT 1 FROM pg_stat_activity activity
  WHERE activity.pid<>pg_backend_pid() AND (
    (activity.datname=:'platform_database' AND activity.usename=:'platform_runtime')
    OR (activity.datname=:'gateway_database' AND activity.usename=:'gateway_runtime')
  )
) AS activation_sessions_zero
\gset
\if :activation_sessions_zero
\else
ROLLBACK;
DO $activation_residual_session$
BEGIN
  RAISE EXCEPTION 'residual target runtime session';
END
$activation_residual_session$;
\endif
SELECT true::text;
COMMIT;
`
	fenceTarget := psql(request.MaintenanceService, request.MaintenanceDatabase, fenceScript, targetBoundaryVariables...)
	grant := psql(request.MaintenanceService, request.MaintenanceDatabase, `BEGIN; GRANT :"platform_writer" TO :"platform_runtime"; GRANT :"gateway_writer" TO :"gateway_runtime"; COMMIT;`+"\n", "platform_writer="+request.TargetWriterRole, "platform_runtime="+request.TargetRuntimeRole, "gateway_writer="+request.GatewayWriterRole, "gateway_runtime="+request.GatewayRuntimeRole)
	restoreScript := `BEGIN;
SELECT (
  current_user=:'maintenance_role'
  AND 2=(
    SELECT count(*) FROM pg_auth_members membership
    JOIN pg_roles granted ON granted.oid=membership.roleid
    JOIN pg_roles member ON member.oid=membership.member
    WHERE member.rolname=:'maintenance_role'
      AND granted.rolname IN (:'platform_migrator',:'gateway_migrator')
      AND NOT membership.admin_option
      AND NOT membership.inherit_option
      AND membership.set_option
  )
) AS activation_restore_roles_ok
\gset
\if :activation_restore_roles_ok
\else
ROLLBACK;
DO $activation_restore_role_guard$
BEGIN
  RAISE EXCEPTION 'activation restore role memberships are not canonical';
END
$activation_restore_role_guard$;
\endif
SET LOCAL ROLE :"platform_migrator";
GRANT CONNECT ON DATABASE :"platform_database" TO :"platform_runtime";
RESET ROLE;
SET LOCAL ROLE :"gateway_migrator";
GRANT CONNECT ON DATABASE :"gateway_database" TO :"gateway_runtime";
RESET ROLE;
SELECT (
  has_database_privilege(:'platform_runtime',:'platform_database','CONNECT')
  AND has_database_privilege(:'gateway_runtime',:'gateway_database','CONNECT')
) AS activation_restore_verified
\gset
\if :activation_restore_verified
SELECT true::text;
COMMIT;
\else
ROLLBACK;
DO $activation_restore_verification$
BEGIN
  RAISE EXCEPTION 'activation target CONNECT restore verification failed';
END
$activation_restore_verification$;
\endif
`
	restore := psql(request.MaintenanceService, request.MaintenanceDatabase, restoreScript, targetBoundaryVariables...)
	contain := psql(request.MaintenanceService, request.MaintenanceDatabase, fenceScript, targetBoundaryVariables...)
	postGrantTargetSessions := psql(request.MaintenanceService, request.MaintenanceDatabase, `-- post_grant_target_sessions
SELECT (NOT EXISTS (
  SELECT 1 FROM pg_stat_activity
  WHERE pid<>pg_backend_pid() AND usename IN (:'platform_runtime',:'gateway_runtime')
	))::text;
	`, "platform_runtime="+request.TargetRuntimeRole, "gateway_runtime="+request.GatewayRuntimeRole)
	restoredTargetSessions := psql(request.MaintenanceService, request.MaintenanceDatabase, `-- restored_target_sessions
SELECT (NOT EXISTS (
  SELECT 1 FROM pg_stat_activity
  WHERE pid<>pg_backend_pid() AND (
    (datname=:'platform_database' AND usename=:'platform_runtime') OR
    (datname=:'gateway_database' AND usename=:'gateway_runtime')
  )
))::text;
`, targetVariables...)
	readiness := psql(request.MaintenanceService, request.MaintenanceDatabase, `SELECT (pg_has_role(:'platform_runtime',:'platform_writer','member') AND pg_has_role(:'gateway_runtime',:'gateway_writer','member') AND has_database_privilege(:'platform_runtime',:'platform_database','CONNECT') AND has_database_privilege(:'gateway_runtime',:'gateway_database','CONNECT') AND EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=:'platform_database' AND usename=:'platform_runtime') AND EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=:'gateway_database' AND usename=:'gateway_runtime'))::text;`+"\n", "platform_database="+request.TargetDatabase, "platform_runtime="+request.TargetRuntimeRole, "platform_writer="+request.TargetWriterRole, "gateway_database="+request.GatewayDatabase, "gateway_runtime="+request.GatewayRuntimeRole, "gateway_writer="+request.GatewayWriterRole)
	postgresProbe := psql(request.MaintenanceService, request.MaintenanceDatabase, `SELECT pg_postmaster_start_time()::text;`+"\n")
	supervisor := filepath.Join(filepath.Dir(filepath.Dir(request.ManifestPath)), "scripts", "dev-platform")
	if request.repositoryRoot != "" {
		supervisor = filepath.Join(request.repositoryRoot, "scripts", "dev-platform")
	}
	normalSupervisor := func(action string) Command {
		return Command{Name: "env", Args: []string{"BT_GATEWAY_STARTUP_PROFILE=normal", "BT_PLATFORM_STARTUP_PROFILE=normal", "BT_FUNDING_STARTUP_PROFILE=normal", supervisor, action}}
	}
	validationSupervisor := func(action string) Command {
		return Command{Name: "env", Args: []string{"BT_GATEWAY_STARTUP_PROFILE=migration-validation", "BT_PLATFORM_STARTUP_PROFILE=migration-validation", "BT_FUNDING_STARTUP_PROFILE=migration-validation", supervisor, action}}
	}
	postgresIdentity := ""
	verifyPostgresIdentity := func(ctx context.Context) error {
		databaseStart, err := runChecked(ctx, postgresProbe, "")
		if err != nil {
			return err
		}
		sealed, err := sealedPostgresIdentity(request.StateDir)
		if err != nil {
			return err
		}
		if databaseStart == "" {
			return fmt.Errorf("PostgreSQL start time is unavailable")
		}
		observed := sealed + "|" + databaseStart
		if postgresIdentity == "" {
			postgresIdentity = observed
			return nil
		}
		if observed != postgresIdentity {
			return fmt.Errorf("PostgreSQL PID/start time changed during activation")
		}
		return nil
	}
	return ActivationDependencies{
		Now: options.Now,
		ExpectedPromotion: func(ctx context.Context) (string, string, error) {
			if err := verifyPostgresIdentity(ctx); err != nil {
				return "", "", err
			}
			if promotion == nil || !validSHA256(promotion.OperationSetSHA256) {
				return "", "", fmt.Errorf("verified Gateway promotion evidence is required")
			}
			digest, err := promotion.Request.OperationSetSHA256()
			if err != nil || digest != promotion.OperationSetSHA256 {
				return "", "", fmt.Errorf("verified Gateway promotion evidence is invalid")
			}
			return digest, digest, nil
		},
		VerifySourceFence: func(ctx context.Context) error { _, err := runChecked(ctx, sourceState, "true"); return err },
		VerifyPreGrantAuthority: func(ctx context.Context, resuming bool) (ActivationPreGrantObservation, error) {
			membership, err := runChecked(ctx, preGrantMembership, "")
			if err != nil || (membership != "false|false" && membership != "true|true") || (!resuming && membership != "false|false") {
				return ActivationPreGrantObservation{}, fmt.Errorf("pre-grant writer membership is partial or unexpected")
			}
			probeOutputs := []string{membership}
			if membership == "false|false" {
				output, err := runChecked(ctx, platformPreGrantProbe, "true")
				if err != nil {
					return ActivationPreGrantObservation{}, err
				}
				probeOutputs = append(probeOutputs, output)
				output, err = runChecked(ctx, gatewayPreGrantProbe, "true")
				if err != nil {
					return ActivationPreGrantObservation{}, err
				}
				probeOutputs = append(probeOutputs, output)
			}
			authorityObservations, err := verifyActivationAuthorityCatalogs(ctx, request)
			if err != nil {
				return ActivationPreGrantObservation{}, err
			}
			expectMembership := membership == "true|true"
			if !validActivationAuthorityObservation(authorityObservations[0], expectMembership) || !validActivationAuthorityObservation(authorityObservations[1], expectMembership) {
				return ActivationPreGrantObservation{}, fmt.Errorf("pre-grant authority catalog does not match writer membership state")
			}
			if err := verifyPostgresIdentity(ctx); err != nil {
				return ActivationPreGrantObservation{}, err
			}
			commands := []Command{preGrantMembership}
			if membership == "false|false" {
				commands = append(commands, platformPreGrantProbe, gatewayPreGrantProbe)
			}
			return ActivationPreGrantObservation{Platform: authorityObservations[0], Gateway: authorityObservations[1], DMLProbeSHA256: activationMeasuredProbeSHA(commands, probeOutputs)}, nil
		},
		StopServices: func(ctx context.Context) error {
			if _, err := runChecked(ctx, validationSupervisor("stop-app"), ""); err != nil {
				return err
			}
			return verifyPostgresIdentity(ctx)
		},
		ObserveGatewayPromotionHead: func(ctx context.Context) (ActivationGatewayHeadObservation, error) {
			if promotion == nil {
				return ActivationGatewayHeadObservation{}, fmt.Errorf("verified Gateway promotion evidence is required")
			}
			store, closeStore, err := openMigrationPromotionStore(ctx, request.GatewayService, request.GatewayDatabase, request.GatewayRole)
			if err != nil {
				return ActivationGatewayHeadObservation{}, err
			}
			defer closeStore()
			observation, err := observeActivationGatewayPromotionHead(ctx, store, *promotion)
			if err != nil {
				return ActivationGatewayHeadObservation{}, err
			}
			if err := verifyPostgresIdentity(ctx); err != nil {
				return ActivationGatewayHeadObservation{}, err
			}
			return observation, nil
		},
		PromoteGateway: func(ctx context.Context) (ActivationGatewayPromotionObservation, error) {
			if promotion == nil {
				return ActivationGatewayPromotionObservation{}, fmt.Errorf("verified Gateway promotion evidence is required")
			}
			store, closeStore, err := openMigrationPromotionStore(ctx, request.GatewayService, request.GatewayDatabase, request.GatewayRole)
			if err != nil {
				return ActivationGatewayPromotionObservation{}, err
			}
			defer closeStore()
			result, err := store.PromoteMigrationPendingSet(ctx, promotion.Request)
			if err != nil {
				return ActivationGatewayPromotionObservation{}, fmt.Errorf("promote verified Gateway pending set: %w", err)
			}
			observation := ActivationGatewayPromotionObservation{Before: activationGatewayHeadObservation(result.Before), After: activationGatewayHeadObservation(result.After)}
			if !validActivationGatewayPromotionObservation(observation, promotion.OperationSetSHA256) {
				return ActivationGatewayPromotionObservation{}, fmt.Errorf("Gateway promotion result is not evidence-bound")
			}
			if err := verifyPostgresIdentity(ctx); err != nil {
				return ActivationGatewayPromotionObservation{}, err
			}
			return observation, nil
		},
		VerifyGatewayPromotion: func(ctx context.Context) (ActivationGatewayHeadObservation, error) {
			if promotion == nil {
				return ActivationGatewayHeadObservation{}, fmt.Errorf("verified Gateway promotion evidence is required")
			}
			store, closeStore, err := openMigrationPromotionStore(ctx, request.GatewayService, request.GatewayDatabase, request.GatewayRole)
			if err != nil {
				return ActivationGatewayHeadObservation{}, err
			}
			defer closeStore()
			observation, err := observeActivationGatewayPromotionHead(ctx, store, *promotion)
			if err != nil {
				return ActivationGatewayHeadObservation{}, err
			}
			if observation.PendingSHA256 != activationMigrationStateSHA256("pending", nil) {
				return ActivationGatewayHeadObservation{}, fmt.Errorf("Gateway runtime head still contains pending state")
			}
			if err := verifyPostgresIdentity(ctx); err != nil {
				return ActivationGatewayHeadObservation{}, err
			}
			return observation, nil
		},
		FenceTargetConnect: func(ctx context.Context) (ActivationTargetFenceObservation, error) {
			output, err := runChecked(ctx, fenceTarget, "true")
			if err != nil {
				return ActivationTargetFenceObservation{}, err
			}
			if err := verifyPostgresIdentity(ctx); err != nil {
				return ActivationTargetFenceObservation{}, err
			}
			return ActivationTargetFenceObservation{SessionsZero: output == "true", ProbeSHA256: activationMeasuredProbeSHA([]Command{fenceTarget}, []string{output})}, nil
		},
		GrantWriters: func(ctx context.Context) error {
			if _, err := runChecked(ctx, grant, ""); err != nil {
				return err
			}
			return verifyPostgresIdentity(ctx)
		},
		VerifyPostGrantAuthority: func(ctx context.Context) (ActivationPostGrantObservation, error) {
			authorityObservations, err := verifyActivationAuthorityCatalogs(ctx, request)
			if err != nil {
				return ActivationPostGrantObservation{}, err
			}
			if !completeActivationAuthorityObservation(authorityObservations[0]) || !completeActivationAuthorityObservation(authorityObservations[1]) {
				return ActivationPostGrantObservation{}, fmt.Errorf("post-grant authority observations are incomplete")
			}
			sessions, err := runChecked(ctx, postGrantTargetSessions, "true")
			if err != nil {
				return ActivationPostGrantObservation{}, fmt.Errorf("post-grant target sessions are not zero: %w", err)
			}
			_, err = runChecked(ctx, platformPostGrantProbe, "true")
			if err != nil {
				return ActivationPostGrantObservation{}, err
			}
			_, err = runChecked(ctx, gatewayPostGrantProbe, "true")
			if err != nil {
				return ActivationPostGrantObservation{}, err
			}
			if err := verifyPostgresIdentity(ctx); err != nil {
				return ActivationPostGrantObservation{}, err
			}
			return ActivationPostGrantObservation{Platform: authorityObservations[0], Gateway: authorityObservations[1], TargetSessionsZero: sessions == "true", DMLProbeSHA256: activationSemanticPassSHA([]Command{platformPostGrantProbe, gatewayPostGrantProbe})}, nil
		},
		RestoreConnect: func(ctx context.Context) error {
			if _, err := runChecked(ctx, restore, "true"); err != nil {
				return err
			}
			return verifyPostgresIdentity(ctx)
		},
		VerifyRestoredTargetSessions: func(ctx context.Context) (ActivationRestoredTargetSessionsObservation, error) {
			output, err := runChecked(ctx, restoredTargetSessions, "true")
			if err != nil {
				return ActivationRestoredTargetSessionsObservation{}, err
			}
			if err := verifyPostgresIdentity(ctx); err != nil {
				return ActivationRestoredTargetSessionsObservation{}, err
			}
			return ActivationRestoredTargetSessionsObservation{SessionsZero: output == "true", ProbeSHA256: activationMeasuredProbeSHA([]Command{restoredTargetSessions}, []string{output})}, nil
		},
		StartServices: func(ctx context.Context) error {
			_, err := runChecked(ctx, normalSupervisor("start-app"), "")
			if err != nil {
				return err
			}
			if _, err := runChecked(ctx, normalSupervisor("wait"), ""); err != nil {
				return err
			}
			return verifyPostgresIdentity(ctx)
		},
		VerifyRunningPostGrantDML: func(ctx context.Context) (ActivationRunningPostGrantDMLObservation, error) {
			if _, err := runChecked(ctx, platformPostGrantProbe, "true"); err != nil {
				return ActivationRunningPostGrantDMLObservation{}, err
			}
			if _, err := runChecked(ctx, gatewayPostGrantProbe, "true"); err != nil {
				return ActivationRunningPostGrantDMLObservation{}, err
			}
			if err := verifyPostgresIdentity(ctx); err != nil {
				return ActivationRunningPostGrantDMLObservation{}, err
			}
			return ActivationRunningPostGrantDMLObservation{PlatformPassed: true, GatewayPassed: true, ProbeSHA256: activationSemanticPassSHA([]Command{platformPostGrantProbe, gatewayPostGrantProbe})}, nil
		},
		VerifyActivation: func(ctx context.Context) error {
			if _, err := runChecked(ctx, normalSupervisor("status"), ""); err != nil {
				return err
			}
			if _, err := runChecked(ctx, normalSupervisor("wait"), ""); err != nil {
				return err
			}
			if _, err := runChecked(ctx, readiness, "true"); err != nil {
				return err
			}
			return verifyPostgresIdentity(ctx)
		},
		ObserveActivation: func(ctx context.Context, input ActivationInput, attempt ActivationAttempt, inputSHA string, facts ActivationVerificationFacts) (ActivationObservations, error) {
			if err := verifyActivationEnvironment(); err != nil {
				return ActivationObservations{}, err
			}
			authorityObservations, err := verifyActivationAuthorityCatalogs(ctx, request)
			if err != nil {
				return ActivationObservations{}, err
			}
			if !reflect.DeepEqual(authorityObservations[0], facts.PostGrant.Platform) || !reflect.DeepEqual(authorityObservations[1], facts.PostGrant.Gateway) {
				return ActivationObservations{}, fmt.Errorf("post-grant authority changed before evidence publication")
			}
			if _, err := runChecked(ctx, sourceState, "true"); err != nil {
				return ActivationObservations{}, err
			}
			supervisorReadiness, err := runChecked(ctx, normalSupervisor("wait"), "")
			if err != nil {
				return ActivationObservations{}, err
			}
			if _, err := runChecked(ctx, readiness, "true"); err != nil {
				return ActivationObservations{}, err
			}
			supervisorStatus, err := runChecked(ctx, normalSupervisor("status"), "")
			if err != nil {
				return ActivationObservations{}, err
			}
			supervisorTopologySHA256, err := activationSupervisorTopologySHA256(supervisorStatus, supervisorReadiness)
			if err != nil {
				return ActivationObservations{}, err
			}
			if err := verifyPostgresIdentity(ctx); err != nil {
				return ActivationObservations{}, err
			}
			return ActivationObservations{
				SchemaVersion: 1, InputSHA256: inputSHA, Source: input.Source, Platform: input.Platform, Gateway: input.Gateway, Maintenance: input.Maintenance,
				SourceWriterRole: input.SourceWriterRole, PlatformMigratorRole: input.PlatformMigratorRole, PlatformRuntimeRole: input.PlatformRuntimeRole, PlatformWriterRole: input.PlatformWriterRole,
				GatewayMigratorRole: input.GatewayMigratorRole, GatewayRuntimeRole: input.GatewayRuntimeRole, GatewayWriterRole: input.GatewayWriterRole,
				GatewayBeforeSHA256: attempt.GatewayBeforeSHA256, GatewayAfterSHA256: attempt.GatewayAfterSHA256,
				GatewayPromotion:          facts.GatewayPromotion,
				PriorVerification:         facts.Prior,
				PreGrantPlatformAuthority: facts.PreGrant.Platform, PreGrantGatewayAuthority: facts.PreGrant.Gateway, PreGrantDMLProbeSHA256: facts.PreGrant.DMLProbeSHA256,
				PlatformAuthority: facts.PostGrant.Platform, GatewayAuthority: facts.PostGrant.Gateway, PostGrantDMLProbeSHA256: facts.RunningPostGrantDML.ProbeSHA256,
				SourceFenceVerified: true, TargetSessionsFenced: facts.Fence.SessionsZero && facts.PostGrant.TargetSessionsZero, TargetSessionFenceSHA256: facts.Fence.ProbeSHA256,
				RestoredTargetSessionsZero: facts.RestoredSessions.SessionsZero, RestoredTargetSessionSHA256: facts.RestoredSessions.ProbeSHA256,
				PostgresIdentitySHA256: sha256Hex([]byte(postgresIdentity)), SupervisorStatusSHA256: sha256Hex([]byte(supervisorStatus + "\n" + supervisorReadiness)), SupervisorTopologySHA256: supervisorTopologySHA256,
				TestModeVerified: true, LivePrivateWritesDisabled: true, VenueWritesDisabled: true,
			}, nil
		},
		ContainPostGrantFailure: func(ctx context.Context) error {
			var containmentErrors []error
			if _, err := runChecked(ctx, normalSupervisor("stop-app"), ""); err != nil {
				containmentErrors = append(containmentErrors, fmt.Errorf("stop app: %w", err))
			}
			if err := verifyPostgresIdentity(ctx); err != nil {
				containmentErrors = append(containmentErrors, fmt.Errorf("verify PostgreSQL identity after stop: %w", err))
			}
			if _, err := runChecked(ctx, contain, "true"); err != nil {
				containmentErrors = append(containmentErrors, fmt.Errorf("fence target sessions: %w", err))
			}
			if err := verifyPostgresIdentity(ctx); err != nil {
				containmentErrors = append(containmentErrors, fmt.Errorf("verify PostgreSQL identity after fence: %w", err))
			}
			return errors.Join(containmentErrors...)
		},
		CompleteVerifiedAttempt: func(context.Context, ActivationInput, ActivationAttempt, string) (ExecutionResult, error) {
			return ExecutionResult{}, fmt.Errorf("activation manifest completion dependency is required")
		},
	}, nil
}

func sealedPostgresIdentity(stateDir string) (string, error) {
	if strings.TrimSpace(stateDir) == "" {
		return "", fmt.Errorf("sealed Test supervisor state directory is required")
	}
	read := func(path string) (string, error) {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 256 {
			return "", fmt.Errorf("sealed PostgreSQL identity is unavailable or unsafe")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read sealed PostgreSQL identity: %w", err)
		}
		value := strings.TrimSpace(string(data))
		if value == "" || strings.ContainsAny(value, "\r\n|") {
			return "", fmt.Errorf("sealed PostgreSQL identity is invalid")
		}
		return value, nil
	}
	pid, err := read(filepath.Join(stateDir, "pids", "postgres.pid"))
	if err != nil {
		return "", err
	}
	start, err := read(filepath.Join(stateDir, "ownership", "postgres.start"))
	if err != nil {
		return "", err
	}
	return pid + "|" + start, nil
}
