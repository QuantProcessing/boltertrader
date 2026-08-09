package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/QuantProcessing/boltertrader/internal/verifiedartifact"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mappingArtifact struct {
	SchemaVersion int       `json:"schemaVersion"`
	Mappings      []mapping `json:"mappings"`
}

type mapping struct {
	Venue             string `json:"venue"`
	NativeEnvironment string `json:"nativeEnvironment"`
	LegacyProduct     string `json:"legacyProduct"`
	SettlementAsset   string `json:"settlementAsset"`
	RouteProfileID    string `json:"routeProfileId"`
	RouteID           string `json:"routeId"`
}

type backfillRequest struct {
	Mode             string
	SourceService    string
	SourceDatabase   string
	SourceRole       string
	PlatformService  string
	PlatformDatabase string
	PlatformRole     string
	GatewayService   string
	GatewayDatabase  string
	GatewayRole      string
	Artifact         mappingArtifact
}

type legacyProduct struct {
	Product         string
	SettlementAsset string
}

type legacyAccount struct {
	AccountID         string
	Venue             string
	NativeEnvironment string
	Mode              string
	OpaqueReference   string
	DesiredRevision   int64
	RuntimeGeneration int64
	CredentialVersion int64
	RequestedState    string
	Products          []legacyProduct
}

type plannedRoute struct {
	RouteID, Product, SettlementAsset, RouteProfileID, RequestedState string
}

type accountPlan struct {
	AccountID, Venue, NativeEnvironment, Mode, OpaqueReference, RequestedState string
	DesiredRevision, RuntimeGeneration, CredentialVersion                      int64
	OperationID, SnapshotID                                                    uuid.UUID
	RequestDigest                                                              string
	Routes                                                                     []plannedRoute
}

type backfillResult struct {
	Accounts   int      `json:"accounts"`
	Routes     int      `json:"routes"`
	AccountIDs []string `json:"accountIds"`
}

type connectedIdentity struct {
	database string
	role     string
}

type identityQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

var executeBackfill = executeProductionBackfill

var errGatewayPendingHeadConflict = errors.New("GATEWAY_PENDING_HEAD_CONFLICT")
var errGatewayOperationConflict = errors.New("GATEWAY_OPERATION_CONFLICT")

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("account-route-backfill", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mappingPath := flags.String("mapping", "", "checksum-verified route mapping artifact")
	mappingSHA := flags.String("mapping-sha256", "", "expected SHA-256 of the route mapping snapshot")
	mode := flags.String("mode", "test", "process mode")
	sourceService := flags.String("source-service", "", "non-secret legacy PostgreSQL service name")
	sourceDatabase := flags.String("source-database", "", "non-secret legacy PostgreSQL database name")
	sourceRole := flags.String("source-role", "", "expected legacy PostgreSQL connection role")
	platformService := flags.String("platform-service", "", "non-secret Platform PostgreSQL service name")
	platformDatabase := flags.String("platform-database", "", "non-secret Platform PostgreSQL database name")
	platformRole := flags.String("platform-role", "", "expected Platform PostgreSQL connection role")
	gatewayService := flags.String("gateway-service", "", "non-secret Gateway PostgreSQL service name")
	gatewayDatabase := flags.String("gateway-database", "", "non-secret Gateway PostgreSQL database name")
	gatewayRole := flags.String("gateway-role", "", "expected Gateway PostgreSQL connection role")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*mappingPath) == "" || !validExpectedSHA(*mappingSHA) {
		return fmt.Errorf("--mapping and --mapping-sha256 are required and positional arguments are not accepted")
	}
	if *mode != "test" {
		return fmt.Errorf("LIVE_PROVISIONING_DISABLED")
	}
	if err := validateIdentifier("source service", *sourceService, 64, true); err != nil {
		return err
	}
	if err := validateIdentifier("source database", *sourceDatabase, 63, false); err != nil {
		return err
	}
	if err := validateIdentifier("source role", *sourceRole, 63, false); err != nil {
		return err
	}
	if err := validateIdentifier("Platform service", *platformService, 64, true); err != nil {
		return err
	}
	if err := validateIdentifier("Platform database", *platformDatabase, 63, false); err != nil {
		return err
	}
	if err := validateIdentifier("Platform role", *platformRole, 63, false); err != nil {
		return err
	}
	if err := validateIdentifier("Gateway service", *gatewayService, 64, true); err != nil {
		return err
	}
	if err := validateIdentifier("Gateway database", *gatewayDatabase, 63, false); err != nil {
		return err
	}
	if err := validateIdentifier("Gateway role", *gatewayRole, 63, false); err != nil {
		return err
	}
	if *platformService == *gatewayService && *platformDatabase == *gatewayDatabase {
		return fmt.Errorf("PLATFORM_GATEWAY_DATABASES_MUST_BE_SEPARATE")
	}
	for _, identity := range []string{*sourceDatabase, *sourceRole, *platformDatabase, *platformRole, *gatewayDatabase, *gatewayRole} {
		if !containsIdentityToken(identity, "test") || containsIdentityToken(identity, "live") {
			return fmt.Errorf("CROSS_MODE_DATABASE_IDENTITY")
		}
	}
	if err := requireDistinctConnectedIdentities(
		connectedIdentity{database: *sourceDatabase, role: *sourceRole},
		connectedIdentity{database: *platformDatabase, role: *platformRole},
		connectedIdentity{database: *gatewayDatabase, role: *gatewayRole},
	); err != nil {
		return err
	}
	data, err := readMappingSnapshot(*mappingPath, *mappingSHA)
	if err != nil {
		return err
	}
	artifact, err := decodeMapping(data)
	if err != nil {
		return err
	}
	result, err := executeBackfill(context.Background(), backfillRequest{
		Mode: *mode, SourceService: *sourceService, SourceDatabase: *sourceDatabase, SourceRole: *sourceRole,
		PlatformService: *platformService, PlatformDatabase: *platformDatabase, PlatformRole: *platformRole,
		GatewayService: *gatewayService, GatewayDatabase: *gatewayDatabase, GatewayRole: *gatewayRole, Artifact: artifact,
	})
	if err != nil {
		return err
	}
	sort.Strings(result.AccountIDs)
	return json.NewEncoder(stdout).Encode(result)
}

func readMappingSnapshot(path, expectedSHA string) ([]byte, error) {
	data, digest, err := verifiedartifact.ReadSnapshot(path, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("MAPPING_ARTIFACT_UNAVAILABLE")
	}
	if digest != expectedSHA {
		clear(data)
		return nil, fmt.Errorf("MAPPING_CHECKSUM_MISMATCH")
	}
	return data, nil
}

func validExpectedSHA(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func executeProductionBackfill(ctx context.Context, request backfillRequest) (backfillResult, error) {
	source, err := pgxpool.New(ctx, postgresConnection(request.SourceService, request.SourceDatabase))
	if err != nil {
		return backfillResult{}, fmt.Errorf("LEGACY_SOURCE_UNAVAILABLE")
	}
	defer source.Close()
	sourceIdentity, err := verifyConnectedIdentity(ctx, source, "source", request.SourceDatabase, request.SourceRole, request.Mode)
	if err != nil {
		return backfillResult{}, err
	}
	accounts, err := readLegacyAccounts(ctx, source, request.Mode)
	if err != nil {
		return backfillResult{}, err
	}
	plans, err := buildAccountPlans(request.Mode, accounts, request.Artifact)
	if err != nil {
		return backfillResult{}, err
	}
	platform, err := pgxpool.New(ctx, postgresConnection(request.PlatformService, request.PlatformDatabase))
	if err != nil {
		return backfillResult{}, fmt.Errorf("PLATFORM_DATABASE_UNAVAILABLE")
	}
	defer platform.Close()
	platformIdentity, err := verifyConnectedIdentity(ctx, platform, "platform", request.PlatformDatabase, request.PlatformRole, request.Mode)
	if err != nil {
		return backfillResult{}, err
	}
	gateway, err := pgxpool.New(ctx, postgresConnection(request.GatewayService, request.GatewayDatabase))
	if err != nil {
		return backfillResult{}, fmt.Errorf("GATEWAY_DATABASE_UNAVAILABLE")
	}
	defer gateway.Close()
	gatewayIdentity, err := verifyConnectedIdentity(ctx, gateway, "gateway", request.GatewayDatabase, request.GatewayRole, request.Mode)
	if err != nil {
		return backfillResult{}, err
	}
	if err := requireDistinctConnectedIdentities(sourceIdentity, platformIdentity, gatewayIdentity); err != nil {
		return backfillResult{}, err
	}
	coordinator := &postgresCoordinator{platform: platform, gateway: gateway}
	if err := writeAccountPlans(ctx, coordinator, plans); err != nil {
		return backfillResult{}, err
	}
	result := backfillResult{Accounts: len(plans), AccountIDs: make([]string, 0, len(plans))}
	for _, plan := range plans {
		result.AccountIDs = append(result.AccountIDs, plan.AccountID)
		result.Routes += len(plan.Routes)
	}
	return result, nil
}

func postgresConnection(service, database string) string {
	return "service=" + service + " dbname=" + database
}

func verifyConnectedIdentity(ctx context.Context, querier identityQuerier, label, expectedDatabase, expectedRole, mode string) (connectedIdentity, error) {
	if mode != "test" || !containsIdentityToken(expectedDatabase, "test") || !containsIdentityToken(expectedRole, "test") || containsIdentityToken(expectedDatabase, "live") || containsIdentityToken(expectedRole, "live") {
		return connectedIdentity{}, fmt.Errorf("CROSS_MODE_DATABASE_IDENTITY")
	}
	var observed connectedIdentity
	if err := querier.QueryRow(ctx, `SELECT current_database(), current_user`).Scan(&observed.database, &observed.role); err != nil {
		return connectedIdentity{}, fmt.Errorf("%s_CONNECTED_IDENTITY_UNAVAILABLE", strings.ToUpper(label))
	}
	if observed.database != expectedDatabase || observed.role != expectedRole {
		return connectedIdentity{}, fmt.Errorf("CONNECTED_DATABASE_IDENTITY_MISMATCH")
	}
	return observed, nil
}

func requireDistinctConnectedIdentities(identities ...connectedIdentity) error {
	seen := make(map[connectedIdentity]struct{}, len(identities))
	for _, identity := range identities {
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("CONNECTED_DATABASE_IDENTITIES_MUST_BE_DISTINCT")
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func containsIdentityToken(value, want string) bool {
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(char rune) bool {
		return char == '_' || char == '-' || char == '.'
	}) {
		if token == want {
			return true
		}
	}
	return false
}

type legacyQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func readLegacyAccounts(ctx context.Context, source legacyQuerier, mode string) ([]legacyAccount, error) {
	rows, err := source.Query(ctx, `SELECT account_id,venue,venue_environment,secret_ref,desired_state_revision,runtime_generation,credential_version,state,product_ids FROM platform_accounts WHERE CASE WHEN venue_environment='live' THEN 'live' ELSE 'test' END=$1 ORDER BY account_id`, mode)
	if err != nil {
		return nil, fmt.Errorf("LEGACY_IDENTITY_READ_BLOCKED")
	}
	defer rows.Close()
	var accounts []legacyAccount
	for rows.Next() {
		var account legacyAccount
		var productIDs []byte
		if err := rows.Scan(&account.AccountID, &account.Venue, &account.NativeEnvironment, &account.OpaqueReference, &account.DesiredRevision, &account.RuntimeGeneration, &account.CredentialVersion, &account.RequestedState, &productIDs); err != nil {
			return nil, fmt.Errorf("LEGACY_IDENTITY_READ_BLOCKED")
		}
		account.Mode = "test"
		if account.NativeEnvironment == "live" {
			account.Mode = "live"
		}
		var products []string
		if err := json.Unmarshal(productIDs, &products); err != nil {
			return nil, fmt.Errorf("LEGACY_PRODUCT_READ_BLOCKED")
		}
		for _, product := range products {
			settlement := ""
			if separator := strings.LastIndexByte(product, '-'); separator >= 0 {
				settlement = strings.ToUpper(product[separator+1:])
			}
			account.Products = append(account.Products, legacyProduct{Product: product, SettlementAsset: settlement})
		}
		accounts = append(accounts, account)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("LEGACY_IDENTITY_READ_BLOCKED")
	}
	return accounts, nil
}

func buildAccountPlans(mode string, accounts []legacyAccount, artifact mappingArtifact) ([]accountPlan, error) {
	lookup := make(map[string]mapping, len(artifact.Mappings))
	for _, item := range artifact.Mappings {
		lookup[mappingKey(item.Venue, item.NativeEnvironment, item.LegacyProduct, item.SettlementAsset)] = item
	}
	plans := make([]accountPlan, 0, len(accounts))
	seenAccounts := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		if account.Mode != mode || (mode == "test" && account.NativeEnvironment == "live") {
			return nil, fmt.Errorf("CROSS_MODE_ACCOUNT")
		}
		if _, duplicate := seenAccounts[account.AccountID]; duplicate {
			return nil, fmt.Errorf("AMBIGUOUS_LEGACY_ACCOUNT")
		}
		seenAccounts[account.AccountID] = struct{}{}
		plan := accountPlan{AccountID: account.AccountID, Venue: account.Venue, NativeEnvironment: account.NativeEnvironment, Mode: account.Mode, OpaqueReference: account.OpaqueReference,
			DesiredRevision: account.DesiredRevision, RuntimeGeneration: account.RuntimeGeneration, CredentialVersion: account.CredentialVersion, RequestedState: account.RequestedState}
		seenRoutes := make(map[string]struct{}, len(account.Products))
		for _, legacy := range account.Products {
			item, ok := lookup[mappingKey(account.Venue, account.NativeEnvironment, legacy.Product, legacy.SettlementAsset)]
			if !ok {
				return nil, fmt.Errorf("MISSING_ROUTE_MAPPING")
			}
			if _, duplicate := seenRoutes[item.RouteID]; duplicate {
				return nil, fmt.Errorf("AMBIGUOUS_LEGACY_PROFILE")
			}
			seenRoutes[item.RouteID] = struct{}{}
			product := "spot"
			if strings.HasPrefix(legacy.Product, "perp") {
				product = "perp"
			}
			plan.Routes = append(plan.Routes, plannedRoute{RouteID: item.RouteID, Product: product, SettlementAsset: item.SettlementAsset, RouteProfileID: item.RouteProfileID, RequestedState: "active"})
		}
		if len(plan.Routes) == 0 || account.OpaqueReference == "" || account.DesiredRevision < 1 || account.RuntimeGeneration < 1 || account.CredentialVersion < 1 {
			return nil, fmt.Errorf("INCOMPLETE_LEGACY_ACCOUNT")
		}
		sort.Slice(plan.Routes, func(i, j int) bool { return plan.Routes[i].RouteID < plan.Routes[j].RouteID })
		plan.OperationID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("account-route-backfill/operation/"+account.AccountID))
		plan.SnapshotID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("account-route-backfill/snapshot/"+account.AccountID))
		digest := sha256.Sum256([]byte(planDigestInput(plan)))
		plan.RequestDigest = hex.EncodeToString(digest[:])
		plans = append(plans, plan)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].AccountID < plans[j].AccountID })
	return plans, nil
}

func mappingKey(venue, environment, product, settlement string) string {
	return strings.Join([]string{venue, environment, product, settlement}, "\x00")
}

func planDigestInput(plan accountPlan) string {
	parts := []string{plan.AccountID, plan.Venue, plan.NativeEnvironment, plan.Mode, plan.OpaqueReference, fmt.Sprint(plan.DesiredRevision), fmt.Sprint(plan.RuntimeGeneration), fmt.Sprint(plan.CredentialVersion), plan.RequestedState}
	for _, route := range plan.Routes {
		parts = append(parts, route.RouteID, route.Product, route.SettlementAsset, route.RouteProfileID, route.RequestedState)
	}
	return strings.Join(parts, "\x00")
}

type backfillCoordinator interface {
	Begin(context.Context) error
	StagePlatform(context.Context, accountPlan) error
	StageGateway(context.Context, accountPlan) error
	Commit(context.Context) error
	RollbackPlatform(context.Context)
	RollbackGateway(context.Context)
}

func writeAccountPlans(ctx context.Context, coordinator backfillCoordinator, plans []accountPlan) error {
	if err := coordinator.Begin(ctx); err != nil {
		return fmt.Errorf("BACKFILL_WRITE_BLOCKED")
	}
	committed := false
	defer func() {
		if !committed {
			coordinator.RollbackPlatform(context.Background())
			coordinator.RollbackGateway(context.Background())
		}
	}()
	for _, plan := range plans {
		if err := coordinator.StagePlatform(ctx, plan); err != nil {
			return fmt.Errorf("BACKFILL_WRITE_BLOCKED")
		}
		if err := coordinator.StageGateway(ctx, plan); err != nil {
			if errors.Is(err, errGatewayPendingHeadConflict) || errors.Is(err, errGatewayOperationConflict) {
				return err
			}
			return fmt.Errorf("BACKFILL_WRITE_BLOCKED")
		}
	}
	if err := coordinator.Commit(ctx); err != nil {
		return fmt.Errorf("BACKFILL_COMMIT_BLOCKED")
	}
	committed = true
	return nil
}

type postgresCoordinator struct {
	platform, gateway     *pgxpool.Pool
	platformTx, gatewayTx pgx.Tx
	commitPlatform        func(context.Context, pgx.Tx) error
}

func (coordinator *postgresCoordinator) Begin(ctx context.Context) error {
	var err error
	coordinator.platformTx, err = coordinator.platform.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	coordinator.gatewayTx, err = coordinator.gateway.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	return err
}

func (coordinator *postgresCoordinator) StagePlatform(ctx context.Context, plan accountPlan) error {
	if _, err := coordinator.platformTx.Exec(ctx, `DELETE FROM platform_account_routes WHERE account_id=$1`, plan.AccountID); err != nil {
		return err
	}
	for _, route := range plan.Routes {
		if _, err := coordinator.platformTx.Exec(ctx, `INSERT INTO platform_account_routes(account_id,route_id,product,settlement_asset,route_profile_id,requested_state) VALUES($1,$2,$3,$4,$5,$6)`, plan.AccountID, route.RouteID, route.Product, route.SettlementAsset, route.RouteProfileID, route.RequestedState); err != nil {
			return err
		}
	}
	result, err := coordinator.platformTx.Exec(ctx, `UPDATE platform_accounts SET mode=$2,native_environment=$3 WHERE account_id=$1 AND secret_ref=$4`, plan.AccountID, plan.Mode, plan.NativeEnvironment, plan.OpaqueReference)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("platform account identity mismatch")
	}
	return nil
}

func (coordinator *postgresCoordinator) StageGateway(ctx context.Context, plan accountPlan) error {
	return stageGatewayPlan(ctx, coordinator.gatewayTx, plan)
}

type gatewayPlanTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func stageGatewayPlan(ctx context.Context, transaction gatewayPlanTx, plan accountPlan) error {
	digest := plan.RequestDigest
	result, err := transaction.Exec(ctx, `INSERT INTO gateway_runtime_snapshots(snapshot_id,account_id,operation_id,request_digest,venue,native_environment,secret_ref,desired_state_revision,runtime_generation,credential_version,requested_state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (operation_id) DO NOTHING`, plan.SnapshotID, plan.AccountID, plan.OperationID, digest, plan.Venue, plan.NativeEnvironment, plan.OpaqueReference, plan.DesiredRevision, plan.RuntimeGeneration, plan.CredentialVersion, plan.RequestedState)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		var existingDigest string
		var existingSnapshot uuid.UUID
		if err := transaction.QueryRow(ctx, `SELECT request_digest,snapshot_id FROM gateway_runtime_snapshots WHERE operation_id=$1`, plan.OperationID).Scan(&existingDigest, &existingSnapshot); err != nil || existingDigest != digest || existingSnapshot != plan.SnapshotID {
			return errGatewayOperationConflict
		}
	}
	for _, route := range plan.Routes {
		if _, err := transaction.Exec(ctx, `INSERT INTO gateway_runtime_snapshot_routes(snapshot_id,route_id,product,settlement_asset,route_profile_id,requested_state) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT (snapshot_id,route_id) DO UPDATE SET product=EXCLUDED.product,settlement_asset=EXCLUDED.settlement_asset,route_profile_id=EXCLUDED.route_profile_id,requested_state=EXCLUDED.requested_state`, plan.SnapshotID, route.RouteID, route.Product, route.SettlementAsset, route.RouteProfileID, route.RequestedState); err != nil {
			return err
		}
	}
	if _, err := transaction.Exec(ctx, `INSERT INTO gateway_desired_operations(operation_id,snapshot_id,request_digest,state) VALUES($1,$2,$3,'prepared') ON CONFLICT (operation_id) DO NOTHING`, plan.OperationID, plan.SnapshotID, digest); err != nil {
		return err
	}
	result, err = transaction.Exec(ctx, `INSERT INTO gateway_account_runtime_heads(account_id,pending_snapshot_id) VALUES($1,$2) ON CONFLICT (account_id) DO UPDATE SET pending_snapshot_id=EXCLUDED.pending_snapshot_id,updated_at=now() WHERE gateway_account_runtime_heads.pending_snapshot_id IS NULL OR gateway_account_runtime_heads.pending_snapshot_id=EXCLUDED.pending_snapshot_id`, plan.AccountID, plan.SnapshotID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errGatewayPendingHeadConflict
	}
	return nil
}

func (coordinator *postgresCoordinator) Commit(ctx context.Context) error {
	if err := coordinator.gatewayTx.Commit(ctx); err != nil {
		return err
	}
	if coordinator.commitPlatform != nil {
		return coordinator.commitPlatform(ctx, coordinator.platformTx)
	}
	return coordinator.platformTx.Commit(ctx)
}
func (coordinator *postgresCoordinator) RollbackPlatform(ctx context.Context) {
	if coordinator.platformTx != nil {
		_ = coordinator.platformTx.Rollback(ctx)
	}
}
func (coordinator *postgresCoordinator) RollbackGateway(ctx context.Context) {
	if coordinator.gatewayTx != nil {
		_ = coordinator.gatewayTx.Rollback(ctx)
	}
}

func decodeMapping(data []byte) (mappingArtifact, error) {
	if len(data) == 0 || len(data) > 1<<20 {
		return mappingArtifact{}, fmt.Errorf("INVALID_MAPPING_ARTIFACT")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return mappingArtifact{}, fmt.Errorf("INVALID_MAPPING_ARTIFACT")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var artifact mappingArtifact
	if err := decoder.Decode(&artifact); err != nil {
		return mappingArtifact{}, fmt.Errorf("INVALID_MAPPING_ARTIFACT")
	}
	if err := requireEOF(decoder); err != nil {
		return mappingArtifact{}, fmt.Errorf("INVALID_MAPPING_ARTIFACT")
	}
	if artifact.SchemaVersion != 1 || len(artifact.Mappings) == 0 || len(artifact.Mappings) > 256 {
		return mappingArtifact{}, fmt.Errorf("INVALID_MAPPING_ARTIFACT")
	}
	registry := routeprofile.DefaultRegistry()
	seenLegacy := make(map[string]struct{}, len(artifact.Mappings))
	seenRoute := make(map[string]struct{}, len(artifact.Mappings))
	for _, item := range artifact.Mappings {
		mode := routeprofile.ModeTest
		if item.NativeEnvironment == string(routeprofile.EnvironmentLive) {
			mode = routeprofile.ModeLive
		}
		profile, err := registry.Resolve(mode, item.Venue, item.RouteProfileID)
		expectedProduct := routeprofile.ProductSpot
		if strings.HasPrefix(item.LegacyProduct, "perp") {
			expectedProduct = routeprofile.ProductPerp
		}
		if err != nil || profile.Product != expectedProduct || string(profile.NativeEnvironment) != item.NativeEnvironment || profile.SettlementAsset != item.SettlementAsset {
			return mappingArtifact{}, fmt.Errorf("ROUTE_PROFILE_CONFLICT")
		}
		if !safeToken(item.LegacyProduct) || !safeToken(item.RouteID) || item.SettlementAsset != strings.ToUpper(item.SettlementAsset) {
			return mappingArtifact{}, fmt.Errorf("INVALID_MAPPING_ARTIFACT")
		}
		legacyKey := strings.Join([]string{item.Venue, item.NativeEnvironment, item.LegacyProduct, item.SettlementAsset}, "\x00")
		if _, duplicate := seenLegacy[legacyKey]; duplicate {
			return mappingArtifact{}, fmt.Errorf("AMBIGUOUS_LEGACY_PROFILE")
		}
		seenLegacy[legacyKey] = struct{}{}
		routeKey := strings.Join([]string{item.Venue, item.NativeEnvironment, item.RouteID}, "\x00")
		if _, duplicate := seenRoute[routeKey]; duplicate {
			return mappingArtifact{}, fmt.Errorf("AMBIGUOUS_ROUTE_ID")
		}
		seenRoute[routeKey] = struct{}{}
	}
	slices.SortFunc(artifact.Mappings, func(left, right mapping) int {
		return strings.Compare(left.Venue+"\x00"+left.NativeEnvironment+"\x00"+left.LegacyProduct+"\x00"+left.SettlementAsset, right.Venue+"\x00"+right.NativeEnvironment+"\x00"+right.LegacyProduct+"\x00"+right.SettlementAsset)
	})
	return artifact, nil
}

func validateIdentifier(label, value string, max int, allowDash bool) error {
	if value == "" || len(value) > max {
		return fmt.Errorf("%s is required and must be non-secret", label)
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || (allowDash && char == '-') {
			continue
		}
		return fmt.Errorf("%s is required and must be non-secret", label)
	}
	return nil
}

func safeToken(value string) bool {
	if value == "" || len(value) > 64 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	return requireEOF(decoder)
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("invalid object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate field")
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid delimiter")
	}
	_, err = decoder.Token()
	return err
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
