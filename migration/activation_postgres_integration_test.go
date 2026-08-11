package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	storepkg "github.com/QuantProcessing/boltertrader/gateway/store"
	gatewaypostgres "github.com/QuantProcessing/boltertrader/gateway/store/postgres"
	"github.com/QuantProcessing/boltertrader/migration/authority"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const activationPostgres16Bin = "/opt/homebrew/opt/postgresql@16/bin"

type activationPostgresCluster struct {
	adminDSN     string
	dataDir      string
	majorVersion int
	port         int
	serviceFile  string
}

func TestActivationPostgres16Harness(t *testing.T) {
	requireActivationPostgresAcceptance(t)

	cluster := startActivationPostgres16(t)
	if cluster.majorVersion != 16 {
		t.Fatalf("PostgreSQL major version=%d, want=16", cluster.majorVersion)
	}
}

func TestExecuteActivationRealPostgresLockTopology(t *testing.T) {
	requireActivationPostgresAcceptance(t)
	t.Setenv("FT_MODE", "test")
	t.Setenv("BT_DEV_ENABLE_LIVE_PRIVATE_WRITES", "0")
	t.Setenv("BT_LIVE_PRIVATE_WRITES_ENABLED", "0")

	cluster := startActivationPostgres16(t)
	request := cluster.bootstrapCanonicalActivation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	release, err := acquireActivationPostgresLock(ctx, request)
	if err != nil {
		t.Fatalf("acquire real activation owner lock: %v", err)
	}
	released := false
	defer func() {
		if released {
			return
		}
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer releaseCancel()
		if err := release(releaseCtx); err != nil {
			t.Errorf("release real activation owner lock: %v", err)
		}
	}()

	var competitorCommands []Command
	_, err = executeActivationLocked(ctx, request, ExecuteOptions{Run: func(_ context.Context, command Command) (CommandResult, error) {
		competitorCommands = append(competitorCommands, command)
		return CommandResult{}, nil
	}}, Manifest{MigrationID: "G010", Workflow: Workflow{Phase: PhaseActivated}})
	if err == nil || !strings.Contains(err.Error(), "activation PostgreSQL lock is held") {
		t.Fatalf("competing real activation error=%v", err)
	}
	if len(competitorCommands) != 0 {
		t.Fatalf("lock loser executed containment or child commands: %+v", competitorCommands)
	}

	supervisorStops := 0
	dependencies, err := defaultActivationDependencies(ctx, request, ExecuteOptions{Run: func(ctx context.Context, command Command) (CommandResult, error) {
		if filepath.Base(command.Name) == "dev-platform" {
			if len(command.Args) != 1 || command.Args[0] != "stop-app" {
				t.Fatalf("unexpected isolated activation supervisor command: %+v", command)
			}
			supervisorStops++
			return CommandResult{}, nil
		}
		result, runErr := activationPostgresCommandRunner(ctx, command)
		t.Logf("real activation SQL kind=%s output=%q error=%v", activationPostgresCommandKind(command), strings.TrimSpace(string(result.Output)), runErr)
		return result, runErr
	}})
	if err != nil {
		t.Fatal(err)
	}
	preGrant, err := dependencies.VerifyPreGrantAuthority(ctx, false)
	if err != nil {
		t.Fatalf("verify real pre-grant authority and rollback-only DML: %v", err)
	}
	assertActivationWriterMembership(t, preGrant.Platform, false)
	assertActivationWriterMembership(t, preGrant.Gateway, false)
	cluster.assertActivationMaintenanceMemberships(t)

	fence, err := dependencies.FenceTargetConnect(ctx)
	if err != nil || !fence.SessionsZero {
		t.Fatalf("production child psql fence under owner lock: observation=%+v error=%v", fence, err)
	}
	if err := dependencies.GrantWriters(ctx); err != nil {
		t.Fatalf("production child psql writer grant under owner lock: %v", err)
	}
	postGrant, err := dependencies.VerifyPostGrantAuthority(ctx)
	if err != nil {
		t.Fatalf("verify real post-grant authority and rollback-only DML: %v", err)
	}
	assertActivationWriterMembership(t, postGrant.Platform, true)
	assertActivationWriterMembership(t, postGrant.Gateway, true)
	cluster.assertActivationMaintenanceMemberships(t)
	if err := dependencies.RestoreConnect(ctx); err != nil {
		t.Fatalf("production child psql CONNECT restore under owner lock: %v", err)
	}
	if restored, err := dependencies.VerifyRestoredTargetSessions(ctx); err != nil || !restored.SessionsZero {
		t.Fatalf("verify restored target sessions: observation=%+v error=%v", restored, err)
	}
	if err := dependencies.ContainPostGrantFailure(ctx); err != nil {
		t.Fatalf("production child psql containment under owner lock: %v", err)
	}
	if supervisorStops != 1 {
		t.Fatalf("production containment stop-app calls=%d, want=1", supervisorStops)
	}

	cluster.resetActivationAuthority(t, true)
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := release(releaseCtx); err != nil {
		releaseCancel()
		t.Fatalf("release real activation owner lock: %v", err)
	}
	releaseCancel()
	released = true
	reacquireCtx, reacquireCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer reacquireCancel()
	reacquired, err := acquireActivationPostgresLock(reacquireCtx, request)
	if err != nil {
		t.Fatalf("reacquire activation lock after owner release: %v", err)
	}
	if err := reacquired(reacquireCtx); err != nil {
		t.Fatalf("release reacquired activation lock: %v", err)
	}
}

func TestExecuteActivationRealPostgresContainment(t *testing.T) {
	requireActivationPostgresAcceptance(t)

	t.Run("terminates only exact target sessions", func(t *testing.T) {
		cluster := startActivationPostgres16(t)
		request := cluster.bootstrapCanonicalActivation(t)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		dependencies, err := defaultActivationDependencies(ctx, request, ExecuteOptions{Run: activationPostgresFailureLoggingRunner(t)})
		if err != nil {
			t.Fatal(err)
		}

		platformPool := cluster.pool(t, request.TargetRuntimeRole, request.TargetDatabase)
		gatewayPool := cluster.pool(t, request.GatewayRuntimeRole, request.GatewayDatabase)
		unrelatedPool := cluster.pool(t, "bt_g010_cluster_admin", "postgres")
		platformSession, err := platformPool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		gatewaySession, err := gatewayPool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		unrelatedSession, err := unrelatedPool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		assertActivationSessionIdentity(t, ctx, platformSession, request.TargetRuntimeRole, request.TargetDatabase)
		assertActivationSessionIdentity(t, ctx, gatewaySession, request.GatewayRuntimeRole, request.GatewayDatabase)

		observation, err := dependencies.FenceTargetConnect(ctx)
		if err != nil || !observation.SessionsZero {
			t.Fatalf("contain exact target sessions: observation=%+v error=%v", observation, err)
		}
		if _, err := platformSession.Exec(ctx, `SELECT 1`); err == nil {
			t.Fatal("Platform target session survived containment")
		}
		if _, err := gatewaySession.Exec(ctx, `SELECT 1`); err == nil {
			t.Fatal("Gateway target session survived containment")
		}
		if _, err := unrelatedSession.Exec(ctx, `SELECT 1`); err != nil {
			t.Fatalf("containment terminated an unrelated PostgreSQL session: %v", err)
		}
		platformSession.Release()
		gatewaySession.Release()
		unrelatedSession.Release()
		platformPool.Close()
		gatewayPool.Close()
		unrelatedPool.Close()
		cluster.assertActivationRuntimeFacts(t, false, false)
		if err := dependencies.RestoreConnect(ctx); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unowned target-role session fails closed", func(t *testing.T) {
		cluster := startActivationPostgres16(t)
		request := cluster.bootstrapCanonicalActivation(t)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		dependencies, err := defaultActivationDependencies(ctx, request, ExecuteOptions{Run: activationPostgresFailureLoggingRunner(t)})
		if err != nil {
			t.Fatal(err)
		}
		admin := cluster.pool(t, "bt_g010_cluster_admin", "postgres")
		if _, err := admin.Exec(ctx, `GRANT CONNECT ON DATABASE bolter_gateway_test TO bolter_platform_test`); err != nil {
			t.Fatal(err)
		}

		exactPool := cluster.pool(t, request.TargetRuntimeRole, request.TargetDatabase)
		unownedPool := cluster.pool(t, request.TargetRuntimeRole, request.GatewayDatabase)
		exactSession, err := exactPool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		unownedSession, err := unownedPool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		assertActivationSessionIdentity(t, ctx, unownedSession, request.TargetRuntimeRole, request.GatewayDatabase)

		if _, err := dependencies.FenceTargetConnect(ctx); err == nil {
			t.Fatal("unowned target-role session was accepted")
		}
		if _, err := exactSession.Exec(ctx, `SELECT 1`); err != nil {
			t.Fatalf("failed containment terminated an exact session before rollback: %v", err)
		}
		if _, err := unownedSession.Exec(ctx, `SELECT 1`); err != nil {
			t.Fatalf("failed containment terminated the unowned session: %v", err)
		}
		cluster.assertActivationRuntimeFacts(t, true, false)

		exactSession.Release()
		unownedSession.Release()
		exactPool.Close()
		unownedPool.Close()
		if _, err := admin.Exec(ctx, `REVOKE CONNECT ON DATABASE bolter_gateway_test FROM bolter_platform_test`); err != nil {
			t.Fatal(err)
		}
		admin.Close()
		if _, err := dependencies.FenceTargetConnect(ctx); err != nil {
			t.Fatalf("cleanup containment after unowned session: %v", err)
		}
		if err := dependencies.RestoreConnect(ctx); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unterminable exact session fails closed", func(t *testing.T) {
		cluster := startActivationPostgres16(t)
		request := cluster.bootstrapCanonicalActivation(t)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		dependencies, err := defaultActivationDependencies(ctx, request, ExecuteOptions{Run: activationPostgresFailureLoggingRunner(t)})
		if err != nil {
			t.Fatal(err)
		}
		admin := cluster.pool(t, "bt_g010_cluster_admin", "postgres")
		for _, statement := range []string{
			`REVOKE pg_signal_backend FROM bolter_platform_test_admin`,
			`REVOKE bolter_platform_test, bolter_gateway_test FROM bolter_platform_test_admin`,
		} {
			if _, err := admin.Exec(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		platformPool := cluster.pool(t, request.TargetRuntimeRole, request.TargetDatabase)
		platformSession, err := platformPool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := dependencies.FenceTargetConnect(ctx); err == nil {
			t.Fatal("unterminable exact target session was accepted")
		}
		if _, err := platformSession.Exec(ctx, `SELECT 1`); err != nil {
			t.Fatalf("unterminable target session did not survive rollback: %v", err)
		}
		cluster.assertActivationRuntimeFacts(t, true, false)

		platformSession.Release()
		platformPool.Close()
		for _, statement := range []string{
			`GRANT pg_signal_backend TO bolter_platform_test_admin`,
			`GRANT bolter_platform_test, bolter_gateway_test TO bolter_platform_test_admin WITH ADMIN TRUE`,
			`GRANT bolter_platform_test, bolter_gateway_test TO bolter_platform_test_admin WITH INHERIT FALSE`,
			`GRANT bolter_platform_test, bolter_gateway_test TO bolter_platform_test_admin WITH SET FALSE`,
		} {
			if _, err := admin.Exec(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		admin.Close()
		if _, err := dependencies.FenceTargetConnect(ctx); err != nil {
			t.Fatalf("cleanup containment after unterminable session: %v", err)
		}
		if err := dependencies.RestoreConnect(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func TestExecuteActivationRealPostgresEvidenceIdentities(t *testing.T) {
	requireActivationPostgresAcceptance(t)

	cluster := startActivationPostgres16(t)
	request := cluster.bootstrapCanonicalActivation(t)
	harness := newActivationPostgresCrashHarness(t, cluster, request, activationPostgresFailpoint(""))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := ExecuteActivation(ctx, harness.input, harness.dependencies)
	if err != nil {
		t.Fatalf("execute real activation identity evidence: %v", err)
	}
	if result.Phase != PhaseActivated {
		t.Fatalf("real activation identity evidence result=%+v", result)
	}
	cluster.assertActivationEvidenceServiceIdentities(t, request, harness.attempt)
	cluster.assertActivationMaintenanceMemberships(t)
	cluster.resetActivationAuthority(t, true)
}

func TestExecuteActivationRealPostgresCrashRecovery(t *testing.T) {
	requireActivationPostgresAcceptance(t)

	for _, failpoint := range []activationPostgresFailpoint{
		activationPostgresBegunBoundary,
		activationPostgresGatewayBoundary,
		activationPostgresWritersBoundary,
		activationPostgresConnectBoundary,
		activationPostgresServicesStarted,
		activationPostgresFinalPublication,
		activationPostgresPromotionResponseLoss,
		activationPostgresGrantCommitted,
	} {
		t.Run(string(failpoint), func(t *testing.T) {
			cluster := startActivationPostgres16(t)
			request := cluster.bootstrapCanonicalActivation(t)
			harness := newActivationPostgresCrashHarness(t, cluster, request, failpoint)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			if _, err := ExecuteActivation(ctx, harness.input, harness.dependencies); err == nil || !strings.Contains(err.Error(), "injected "+string(failpoint)) {
				t.Fatalf("first activation at %s error=%v", failpoint, err)
			}
			wantConnect, wantWriter, wantBoundary := activationPostgresFailureFacts(failpoint)
			cluster.assertActivationRuntimeFacts(t, wantConnect, wantWriter)
			cluster.assertActivationMaintenanceMemberships(t)
			harness.assertPromotionState(t, failpoint != activationPostgresBegunBoundary)
			if harness.attempt == nil || harness.attempt.Boundary != wantBoundary {
				t.Fatalf("durable boundary after %s=%+v, want=%s", failpoint, harness.attempt, wantBoundary)
			}

			result, err := ExecuteActivation(ctx, harness.input, harness.dependencies)
			if err != nil {
				t.Fatalf("retry activation at %s: %v", failpoint, err)
			}
			if result.Phase != PhaseActivated {
				t.Fatalf("retry result=%+v", result)
			}
			cluster.assertActivationRuntimeFacts(t, true, true)
			cluster.assertActivationMaintenanceMemberships(t)
			harness.assertPromotionState(t, true)
			harness.assertExactlyOnce(t)
			cluster.resetActivationAuthority(t, true)
		})
	}
}

type activationPostgresFailpoint string

const (
	activationPostgresBegunBoundary         activationPostgresFailpoint = "begun-boundary"
	activationPostgresGatewayBoundary       activationPostgresFailpoint = "gateway-promoted-boundary"
	activationPostgresWritersBoundary       activationPostgresFailpoint = "writers-granted-boundary"
	activationPostgresConnectBoundary       activationPostgresFailpoint = "connect-restored-boundary"
	activationPostgresServicesStarted       activationPostgresFailpoint = "services-started-boundary"
	activationPostgresFinalPublication      activationPostgresFailpoint = "final-publication"
	activationPostgresPromotionResponseLoss activationPostgresFailpoint = "promotion-response-loss"
	activationPostgresGrantCommitted        activationPostgresFailpoint = "grant-commit-save-loss"
)

func activationPostgresFailureFacts(failpoint activationPostgresFailpoint) (connect, writer bool, boundary ActivationBoundary) {
	switch failpoint {
	case activationPostgresBegunBoundary:
		return true, false, ActivationBegun
	case activationPostgresGatewayBoundary:
		return false, false, ActivationGatewayPromoted
	case activationPostgresWritersBoundary:
		return false, true, ActivationWritersGranted
	case activationPostgresConnectBoundary:
		return false, true, ActivationConnectRestored
	case activationPostgresServicesStarted:
		return false, true, ActivationServicesStarted
	case activationPostgresFinalPublication:
		return false, true, ActivationServicesStarted
	case activationPostgresPromotionResponseLoss:
		return false, false, ActivationBegun
	case activationPostgresGrantCommitted:
		return false, true, ActivationGatewayPromoted
	default:
		panic("unknown activation PostgreSQL failpoint")
	}
}

type activationPostgresCrashHarness struct {
	cluster            *activationPostgresCluster
	request            ExecutionRequest
	input              ActivationInput
	dependencies       ActivationDependencies
	attempt            *ActivationAttempt
	failpoint          activationPostgresFailpoint
	failureInjected    bool
	grantCalls         int
	promotionCalls     int
	priorChecks        int
	preGrantChecks     int
	sourceFenceChecks  int
	postGrantChecks    int
	containmentCalls   int
	verificationChecks int
	completionCalls    int
	clockTicks         int
	promotionCommits   int
	gatewayPool        *pgxpool.Pool
	gatewayStore       *gatewaypostgres.Store
	promotion          verifiedMigrationPromotion
	pendingSnapshot    storepkg.DesiredSnapshot
	promotionBefore    ActivationGatewayHeadObservation
	promotionAfter     ActivationGatewayHeadObservation
}

func newActivationPostgresCrashHarness(t *testing.T, cluster *activationPostgresCluster, request ExecutionRequest, failpoint activationPostgresFailpoint) *activationPostgresCrashHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	gatewayPool, err := pgxpool.New(ctx, "service="+request.GatewayService+" dbname="+request.GatewayDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gatewayPool.Close)
	var gatewayDatabase, gatewayRole string
	if err := gatewayPool.QueryRow(ctx, `SELECT current_database(),current_user`).Scan(&gatewayDatabase, &gatewayRole); err != nil {
		t.Fatal(err)
	}
	if gatewayDatabase != request.GatewayDatabase || gatewayRole != request.GatewayRole {
		t.Fatalf("real Gateway store identity=%s/%s, want=%s/%s", gatewayRole, gatewayDatabase, request.GatewayRole, request.GatewayDatabase)
	}
	gatewayStore := gatewaypostgres.NewStore(gatewayPool)
	snapshot := activationPostgresGatewaySnapshot(t)
	pending, err := gatewayStore.PreparePending(ctx, snapshot)
	if err != nil {
		t.Fatalf("prepare real Gateway pending snapshot: %v", err)
	}
	promotionItem, err := activationMigrationPromotionItem(pending.DesiredSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	promotionRequest := storepkg.MigrationPromotionRequest{
		MigrationID: "G010", FenceSHA256: strings.Repeat("f", 64), Items: []storepkg.MigrationPromotionItem{promotionItem},
	}
	digest, err := promotionRequest.OperationSetSHA256()
	if err != nil {
		t.Fatal(err)
	}
	promotion := verifiedMigrationPromotion{Request: promotionRequest, OperationSetSHA256: digest}
	before, err := observeActivationGatewayPromotionHead(ctx, gatewayStore, promotion)
	if err != nil {
		t.Fatalf("observe real Gateway pending head: %v", err)
	}
	after := activationGatewayHeadObservation(storepkg.MigrationPromotionDigest{
		PendingSHA256:      activationMigrationStateSHA256("pending", nil),
		ActiveSHA256:       activationMigrationStateSHA256("active", promotionRequest.Items),
		OperationSetSHA256: digest,
	})
	dir := t.TempDir()
	evidenceDir := filepath.Join(dir, "evidence")
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	priorPlan := ActivationPriorVerificationPlan{
		SchemaVersion: 1, MigrationID: "G010", ManifestStateSHA256: strings.Repeat("4", 64),
		Events: []ActivationPriorEvent{{Sequence: 1, Epoch: 1, Operation: Validate, EvidencePath: filepath.Join(evidenceDir, "prior.json"), ProofSHA256: strings.Repeat("3", 64)}},
	}
	activationRequest := request
	activationRequest.ManifestPath = filepath.Join(dir, "manifest.json")
	activationRequest.EvidenceDir = evidenceDir
	input := buildActivationInput(Manifest{MigrationID: "G010"}, activationRequest, filepath.Join(evidenceDir, "attempt.json"), priorPlan, false)
	realDependencies, err := defaultActivationDependenciesForPromotion(ctx, request, ExecuteOptions{Run: activationPostgresFailureLoggingRunner(t)}, &promotion)
	if err != nil {
		t.Fatal(err)
	}
	harness := &activationPostgresCrashHarness{
		cluster: cluster, request: request, input: input, failpoint: failpoint,
		gatewayPool: gatewayPool, gatewayStore: gatewayStore, promotion: promotion, pendingSnapshot: pending.DesiredSnapshot,
		promotionBefore: before, promotionAfter: after,
	}
	harness.dependencies = ActivationDependencies{
		Now: func() time.Time {
			harness.clockTicks++
			return time.Date(2026, 8, 10, 4, 0, 0, harness.clockTicks, time.UTC)
		},
		LoadAttempt: func(context.Context) (*ActivationAttempt, error) {
			if harness.attempt == nil {
				return nil, nil
			}
			copy := *harness.attempt
			return &copy, nil
		},
		SaveAttempt: func(_ context.Context, value ActivationAttempt) error {
			shouldFail := !harness.failureInjected && harness.failpoint == activationPostgresGrantCommitted && value.Boundary == ActivationWritersGranted
			if shouldFail {
				harness.failureInjected = true
				return fmt.Errorf("injected %s", harness.failpoint)
			}
			copy := value
			harness.attempt = &copy
			return nil
		},
		ExpectedPromotion: realDependencies.ExpectedPromotion,
		VerifyPriorState: func(_ context.Context, plan ActivationPriorVerificationPlan, attempt ActivationAttempt) (ActivationPriorVerificationObservation, error) {
			harness.priorChecks++
			planSHA, err := activationPriorPlanSHA(plan)
			if err != nil {
				return ActivationPriorVerificationObservation{}, err
			}
			return ActivationPriorVerificationObservation{AttemptID: attempt.ID, PlanSHA256: planSHA, ArtifactSetSHA256: strings.Repeat("5", 64), CurrentPostconditionsSHA256: strings.Repeat("6", 64)}, nil
		},
		RecoverPriorState: func(_ context.Context, plan ActivationPriorVerificationPlan, attempt ActivationAttempt) (ActivationPriorVerificationObservation, error) {
			harness.priorChecks++
			planSHA, err := activationPriorPlanSHA(plan)
			if err != nil {
				return ActivationPriorVerificationObservation{}, err
			}
			return ActivationPriorVerificationObservation{AttemptID: attempt.ID, PlanSHA256: planSHA, ArtifactSetSHA256: strings.Repeat("5", 64), CurrentPostconditionsSHA256: strings.Repeat("6", 64)}, nil
		},
		VerifySourceFence: func(ctx context.Context) error {
			harness.sourceFenceChecks++
			return realDependencies.VerifySourceFence(ctx)
		},
		VerifyPreGrantAuthority: func(ctx context.Context, resuming bool) (ActivationPreGrantObservation, error) {
			harness.preGrantChecks++
			return realDependencies.VerifyPreGrantAuthority(ctx, resuming)
		},
		StopServices: func(context.Context) error {
			if !harness.failureInjected && harness.failpoint == activationPostgresBegunBoundary {
				harness.failureInjected = true
				return fmt.Errorf("injected %s", harness.failpoint)
			}
			return nil
		},
		ObserveGatewayPromotionHead: realDependencies.ObserveGatewayPromotionHead,
		PromoteGateway: func(ctx context.Context) (ActivationGatewayPromotionObservation, error) {
			harness.promotionCalls++
			observation, err := realDependencies.PromoteGateway(ctx)
			if err != nil {
				return ActivationGatewayPromotionObservation{}, err
			}
			if observation.Before != observation.After {
				harness.promotionCommits++
			}
			if !harness.failureInjected && harness.failpoint == activationPostgresPromotionResponseLoss {
				harness.failureInjected = true
				return ActivationGatewayPromotionObservation{}, fmt.Errorf("injected %s", harness.failpoint)
			}
			return observation, nil
		},
		VerifyGatewayPromotion: realDependencies.VerifyGatewayPromotion,
		FenceTargetConnect:     realDependencies.FenceTargetConnect,
		GrantWriters: func(ctx context.Context) error {
			if !harness.failureInjected && harness.failpoint == activationPostgresGatewayBoundary {
				harness.failureInjected = true
				return fmt.Errorf("injected %s", harness.failpoint)
			}
			harness.grantCalls++
			return realDependencies.GrantWriters(ctx)
		},
		VerifyPostGrantAuthority: func(ctx context.Context) (ActivationPostGrantObservation, error) {
			harness.postGrantChecks++
			if !harness.failureInjected && harness.failpoint == activationPostgresWritersBoundary {
				harness.failureInjected = true
				return ActivationPostGrantObservation{}, fmt.Errorf("injected %s", harness.failpoint)
			}
			return realDependencies.VerifyPostGrantAuthority(ctx)
		},
		RestoreConnect:               realDependencies.RestoreConnect,
		VerifyRestoredTargetSessions: realDependencies.VerifyRestoredTargetSessions,
		StartServices: func(context.Context) error {
			if !harness.failureInjected && harness.failpoint == activationPostgresConnectBoundary {
				harness.failureInjected = true
				return fmt.Errorf("injected %s", harness.failpoint)
			}
			return nil
		},
		VerifyRunningPostGrantDML: realDependencies.VerifyRunningPostGrantDML,
		VerifyActivation: func(context.Context) error {
			harness.verificationChecks++
			if !harness.failureInjected && harness.failpoint == activationPostgresServicesStarted {
				harness.failureInjected = true
				return fmt.Errorf("injected %s", harness.failpoint)
			}
			return nil
		},
		ContainPostGrantFailure: func(ctx context.Context) error {
			harness.containmentCalls++
			observation, err := realDependencies.FenceTargetConnect(ctx)
			if err != nil {
				return err
			}
			if !validActivationTargetFenceObservation(observation) {
				return fmt.Errorf("real containment observation is incomplete")
			}
			return nil
		},
		ObserveActivation: func(_ context.Context, input ActivationInput, attempt ActivationAttempt, inputSHA string, facts ActivationVerificationFacts) (ActivationObservations, error) {
			return ActivationObservations{
				SchemaVersion: 1, InputSHA256: inputSHA, Source: input.Source, Platform: input.Platform, Gateway: input.Gateway, Maintenance: input.Maintenance,
				SourceWriterRole: input.SourceWriterRole, PlatformMigratorRole: input.PlatformMigratorRole, PlatformRuntimeRole: input.PlatformRuntimeRole, PlatformWriterRole: input.PlatformWriterRole,
				GatewayMigratorRole: input.GatewayMigratorRole, GatewayRuntimeRole: input.GatewayRuntimeRole, GatewayWriterRole: input.GatewayWriterRole,
				GatewayBeforeSHA256: attempt.GatewayBeforeSHA256, GatewayAfterSHA256: attempt.GatewayAfterSHA256,
				GatewayPromotion: facts.GatewayPromotion, PriorVerification: facts.Prior,
				PreGrantPlatformAuthority: facts.PreGrant.Platform, PreGrantGatewayAuthority: facts.PreGrant.Gateway, PreGrantDMLProbeSHA256: facts.PreGrant.DMLProbeSHA256,
				PlatformAuthority: facts.PostGrant.Platform, GatewayAuthority: facts.PostGrant.Gateway, PostGrantDMLProbeSHA256: facts.RunningPostGrantDML.ProbeSHA256,
				SourceFenceVerified: true, TargetSessionsFenced: facts.Fence.SessionsZero && facts.PostGrant.TargetSessionsZero, TargetSessionFenceSHA256: facts.Fence.ProbeSHA256,
				RestoredTargetSessionsZero: facts.RestoredSessions.SessionsZero, RestoredTargetSessionSHA256: facts.RestoredSessions.ProbeSHA256,
				PostgresIdentitySHA256: strings.Repeat("7", 64), SupervisorStatusSHA256: strings.Repeat("8", 64),
				TestModeVerified: true, LivePrivateWritesDisabled: true, VenueWritesDisabled: true,
			}, nil
		},
		CompleteVerifiedAttempt: func(_ context.Context, _ ActivationInput, value ActivationAttempt, inputSHA string) (ExecutionResult, error) {
			harness.completionCalls++
			if !harness.failureInjected && harness.failpoint == activationPostgresFinalPublication {
				harness.failureInjected = true
				return ExecutionResult{}, fmt.Errorf("injected %s", harness.failpoint)
			}
			copy := value
			harness.attempt = &copy
			return ExecutionResult{Result: Result{Phase: PhaseActivated}, InputSHA256: inputSHA}, nil
		},
	}
	return harness
}

func activationPostgresGatewaySnapshot(t *testing.T) storepkg.DesiredSnapshot {
	t.Helper()
	operationID, err := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445d91")
	if err != nil {
		t.Fatal(err)
	}
	secretRef, err := secretstore.ParseOpaqueRef("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	accountID := domain.AccountID("account-runtime")
	return storepkg.DesiredSnapshot{
		SnapshotID: uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445d92"), OperationID: operationID, AccountID: accountID,
		Venue: "binance", NativeEnvironment: "demo", SecretRef: secretRef,
		Fence:          domain.AccountFence{AccountID: accountID, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1},
		RequestedState: storepkg.DesiredActive,
		Routes: []storepkg.RouteDefinition{
			{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: storepkg.RouteActive},
			{RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: storepkg.RouteActive},
		},
	}
}

func (harness *activationPostgresCrashHarness) assertPromotionState(t *testing.T, wantPromoted bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observed, err := observeActivationGatewayPromotionHead(ctx, harness.gatewayStore, harness.promotion)
	if err != nil {
		t.Fatal(err)
	}
	want := harness.promotionBefore
	if wantPromoted {
		want = harness.promotionAfter
	}
	if observed != want {
		t.Fatalf("real Gateway promotion head=%+v, want=%+v", observed, want)
	}
}

func (harness *activationPostgresCrashHarness) assertExactlyOnce(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	states, err := harness.gatewayStore.ListRuntimeStateForRecovery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].AccountID != harness.pendingSnapshot.AccountID || states[0].Active == nil || states[0].Pending != nil || len(states[0].Retiring) != 0 || states[0].Tombstoned {
		t.Fatalf("real Gateway recovery state=%+v", states)
	}
	active := states[0].Active.DesiredSnapshot
	if active.SnapshotID != harness.pendingSnapshot.SnapshotID || active.OperationID != harness.pendingSnapshot.OperationID || active.Fence != harness.pendingSnapshot.Fence || active.RequestDigest != harness.pendingSnapshot.RequestDigest {
		t.Fatalf("real Gateway active snapshot=%+v, want=%+v", active, harness.pendingSnapshot)
	}
	var operationRows, appliedRows, pendingHeads, activeHeads int
	if err := harness.gatewayPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM gateway_desired_operations WHERE operation_id=$1),
		(SELECT count(*) FROM gateway_desired_operations WHERE operation_id=$1 AND state='applied'),
		(SELECT count(*) FROM gateway_account_runtime_heads WHERE account_id=$2 AND pending_snapshot_id IS NOT NULL),
		(SELECT count(*) FROM gateway_account_runtime_heads WHERE account_id=$2 AND active_snapshot_id=$3)`,
		harness.pendingSnapshot.OperationID.String(), harness.pendingSnapshot.AccountID, harness.pendingSnapshot.SnapshotID,
	).Scan(&operationRows, &appliedRows, &pendingHeads, &activeHeads); err != nil {
		t.Fatal(err)
	}
	wantPromotionCalls := 1
	if harness.failpoint == activationPostgresPromotionResponseLoss {
		wantPromotionCalls = 2
	}
	wantContainment := 1
	if harness.failpoint == activationPostgresBegunBoundary || harness.failpoint == activationPostgresGatewayBoundary || harness.failpoint == activationPostgresPromotionResponseLoss {
		wantContainment = 0
	}
	if operationRows != 1 || appliedRows != 1 || pendingHeads != 0 || activeHeads != 1 || harness.promotionCommits != 1 || harness.promotionCalls != wantPromotionCalls || harness.grantCalls != 1 || harness.containmentCalls != wantContainment {
		t.Fatalf("exactly-once facts operationRows=%d appliedRows=%d pendingHeads=%d activeHeads=%d commits=%d promotionCalls=%d grantCalls=%d containment=%d; want promotionCalls=%d containment=%d",
			operationRows, appliedRows, pendingHeads, activeHeads, harness.promotionCommits, harness.promotionCalls, harness.grantCalls, harness.containmentCalls, wantPromotionCalls, wantContainment)
	}
	if harness.priorChecks != 2 || harness.sourceFenceChecks != 2 || harness.preGrantChecks != 2 || harness.postGrantChecks < 1 || harness.verificationChecks < 1 || harness.completionCalls < 1 {
		t.Fatalf("postcondition revalidation counts prior=%d source=%d preGrant=%d postGrant=%d verification=%d completion=%d",
			harness.priorChecks, harness.sourceFenceChecks, harness.preGrantChecks, harness.postGrantChecks, harness.verificationChecks, harness.completionCalls)
	}
}

func requireActivationPostgresAcceptance(t *testing.T) {
	t.Helper()
	if os.Getenv("BT_G010_POSTGRES_ACCEPT") != "1" {
		t.Skip("BT_G010_POSTGRES_ACCEPT=1 is required for isolated PostgreSQL 16 activation verification")
	}
}

func startActivationPostgres16(t *testing.T) *activationPostgresCluster {
	t.Helper()
	for _, name := range []string{"initdb", "pg_ctl", "postgres", "psql"} {
		path := filepath.Join(activationPostgres16Bin, name)
		if info, err := os.Stat(path); err != nil || info.Mode()&0o111 == 0 {
			t.Fatalf("PostgreSQL 16 acceptance binary is unavailable: %s", path)
		}
	}

	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	port := reserveActivationPostgresPort(t)
	adminUser := "bt_g010_cluster_admin"
	runActivationPostgresCommand(t, filepath.Join(activationPostgres16Bin, "initdb"),
		"--pgdata", dataDir, "--username", adminUser, "--encoding", "UTF8", "--no-locale",
		"--auth-local", "trust", "--auth-host", "trust")
	logPath := filepath.Join(root, "postgres.log")
	serverOptions := fmt.Sprintf("-h 127.0.0.1 -p %d -k /private/tmp -F -c fsync=off -c synchronous_commit=off -c full_page_writes=off", port)
	runActivationPostgresCommand(t, filepath.Join(activationPostgres16Bin, "pg_ctl"),
		"--pgdata", dataDir, "--log", logPath, "--wait", "--timeout", "15", "--options", serverOptions, "start")
	t.Cleanup(func() {
		command := exec.Command(filepath.Join(activationPostgres16Bin, "pg_ctl"), "--pgdata", dataDir, "--wait", "--timeout", "15", "--mode", "fast", "stop")
		if output, err := command.CombinedOutput(); err != nil {
			t.Errorf("stop isolated PostgreSQL 16 cluster: %v: %s", err, strings.TrimSpace(string(output)))
		}
	})

	adminDSN := fmt.Sprintf("postgresql://%s@127.0.0.1:%d/postgres?sslmode=disable", adminUser, port)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("configure isolated PostgreSQL 16 admin pool: %v", err)
	}
	defer pool.Close()
	var version int
	if err := pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer / 10000`).Scan(&version); err != nil {
		t.Fatalf("probe isolated PostgreSQL 16 version: %v", err)
	}

	serviceFile := filepath.Join(root, "pg_service.conf")
	serviceConfig := fmt.Sprintf(`[activation_cluster_admin]
host=127.0.0.1
port=%d
user=%s
sslmode=disable

[platform_maintenance_test]
host=127.0.0.1
port=%d
user=bolter_platform_test_admin
dbname=bolter_maintenance_test
sslmode=disable

[funding_source_test]
host=127.0.0.1
port=%d
user=bolter_platform_test_admin
dbname=bolter_funding_test
options=-c role=bolter_funding_test_migrator
sslmode=disable

[platform_target_test]
host=127.0.0.1
port=%d
user=bolter_platform_test_admin
dbname=bolter_platform_test
options=-c role=bolter_platform_test_migrator
sslmode=disable

[gateway_test]
host=127.0.0.1
port=%d
user=bolter_platform_test_admin
dbname=bolter_gateway_test
options=-c role=bolter_gateway_test_migrator
sslmode=disable
`, port, adminUser, port, port, port, port)
	if err := os.WriteFile(serviceFile, []byte(serviceConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	priorServiceFile, hadServiceFile := os.LookupEnv("PGSERVICEFILE")
	if err := os.Setenv("PGSERVICEFILE", serviceFile); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadServiceFile {
			_ = os.Setenv("PGSERVICEFILE", priorServiceFile)
		} else {
			_ = os.Unsetenv("PGSERVICEFILE")
		}
	})

	return &activationPostgresCluster{adminDSN: adminDSN, dataDir: dataDir, majorVersion: version, port: port, serviceFile: serviceFile}
}

func reserveActivationPostgresPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func runActivationPostgresCommand(t *testing.T, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		t.Fatalf("run %s %s: %v: %s", filepath.Base(name), strings.Join(args, " "), err, strings.TrimSpace(output.String()))
	}
}

func (cluster *activationPostgresCluster) dsn(user, database string) string {
	return "postgresql://" + user + "@127.0.0.1:" + strconv.Itoa(cluster.port) + "/" + database + "?sslmode=disable"
}

func (cluster *activationPostgresCluster) bootstrapCanonicalActivation(t *testing.T) ExecutionRequest {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin := cluster.pool(t, "bt_g010_cluster_admin", "postgres")
	defer admin.Close()

	for _, statement := range []string{
		`CREATE ROLE bolter_platform_test_admin LOGIN NOSUPERUSER CREATEDB CREATEROLE NOINHERIT NOREPLICATION`,
		`CREATE ROLE bolter_funding_test_migrator NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION`,
		`CREATE ROLE bolter_platform_test_migrator NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION`,
		`CREATE ROLE bolter_gateway_test_migrator NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION`,
		`CREATE ROLE bolter_funding_test_writer NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION`,
		`CREATE ROLE bolter_platform_test_writer NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION`,
		`CREATE ROLE bolter_gateway_test_writer NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION`,
		`CREATE ROLE bolter_platform_test LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION`,
		`CREATE ROLE bolter_gateway_test LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION`,
		`GRANT pg_signal_backend TO bolter_platform_test_admin`,
		`GRANT bolter_funding_test_migrator, bolter_platform_test_migrator, bolter_gateway_test_migrator TO bolter_platform_test_admin WITH SET TRUE`,
		`GRANT bolter_funding_test_migrator, bolter_platform_test_migrator, bolter_gateway_test_migrator TO bolter_platform_test_admin WITH INHERIT FALSE`,
		`GRANT bolter_funding_test_migrator, bolter_platform_test_migrator, bolter_gateway_test_migrator TO bolter_platform_test_admin WITH ADMIN FALSE`,
		`GRANT bolter_platform_test, bolter_gateway_test, bolter_funding_test_writer, bolter_platform_test_writer, bolter_gateway_test_writer TO bolter_platform_test_admin WITH ADMIN TRUE`,
		`GRANT bolter_platform_test, bolter_gateway_test, bolter_funding_test_writer, bolter_platform_test_writer, bolter_gateway_test_writer TO bolter_platform_test_admin WITH INHERIT FALSE`,
		`GRANT bolter_platform_test, bolter_gateway_test, bolter_funding_test_writer, bolter_platform_test_writer, bolter_gateway_test_writer TO bolter_platform_test_admin WITH SET FALSE`,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatalf("bootstrap canonical activation role: %v", err)
		}
	}
	for _, database := range []struct {
		name, owner, connect string
	}{
		{"bolter_funding_test", "bolter_funding_test_migrator", "bolter_funding_test_migrator, bolter_platform_test_admin"},
		{"bolter_platform_test", "bolter_platform_test_migrator", "bolter_platform_test_migrator, bolter_platform_test, bolter_platform_test_admin"},
		{"bolter_gateway_test", "bolter_gateway_test_migrator", "bolter_gateway_test_migrator, bolter_gateway_test, bolter_platform_test_admin"},
		{"bolter_maintenance_test", "bolter_platform_test_admin", "bolter_platform_test_admin"},
	} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+quotePostgresIdentifier(database.name)+" OWNER "+quotePostgresIdentifier(database.owner)); err != nil {
			t.Fatalf("create canonical activation database %s: %v", database.name, err)
		}
		if _, err := admin.Exec(ctx, "REVOKE CONNECT ON DATABASE "+quotePostgresIdentifier(database.name)+" FROM PUBLIC"); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, "GRANT CONNECT ON DATABASE "+quotePostgresIdentifier(database.name)+" TO "+database.connect); err != nil {
			t.Fatal(err)
		}
	}

	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cluster.applyMigrationsAndAuthority(t, "bolter_platform_test", authority.PlatformV15(), filepath.Join(repo, "backend", "migrations"))
	cluster.applyMigrationsAndAuthority(t, "bolter_gateway_test", authority.GatewayV6(), filepath.Join(repo, "gateway", "store", "postgres", "migrations"))
	cluster.assertActivationMaintenanceMemberships(t)

	stateDir := t.TempDir()
	pidData, err := os.ReadFile(filepath.Join(cluster.dataDir, "postmaster.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid := strings.TrimSpace(strings.SplitN(string(pidData), "\n", 2)[0])
	for path, value := range map[string]string{
		filepath.Join(stateDir, "pids", "postgres.pid"):        pid + "\n",
		filepath.Join(stateDir, "ownership", "postgres.start"): "isolated-pg16\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	request := ExecutionRequest{
		Operation: Activate, ManifestPath: filepath.Join(repo, "migration", "manifest.json"), EvidenceDir: t.TempDir(), StateDir: stateDir, repositoryRoot: repo,
		SourceService: "funding_source_test", SourceDatabase: "bolter_funding_test", SourceRole: "bolter_funding_test_migrator", SourceWriterRole: "bolter_funding_test_writer",
		TargetService: "platform_target_test", TargetDatabase: "bolter_platform_test", TargetRole: "bolter_platform_test_migrator", TargetRuntimeRole: "bolter_platform_test", TargetWriterRole: "bolter_platform_test_writer",
		GatewayService: "gateway_test", GatewayDatabase: "bolter_gateway_test", GatewayRole: "bolter_gateway_test_migrator", GatewayRuntimeRole: "bolter_gateway_test", GatewayWriterRole: "bolter_gateway_test_writer",
		MaintenanceService: "platform_maintenance_test", MaintenanceDatabase: "bolter_maintenance_test", MaintenanceRole: "bolter_platform_test_admin",
	}
	cluster.assertActivationServiceIdentities(t, request)
	return request
}

func (cluster *activationPostgresCluster) assertActivationServiceIdentities(t *testing.T, request ExecutionRequest) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, identity := range []DatabaseIdentity{
		{Service: request.SourceService, Database: request.SourceDatabase, Role: request.SourceRole},
		{Service: request.TargetService, Database: request.TargetDatabase, Role: request.TargetRole},
		{Service: request.GatewayService, Database: request.GatewayDatabase, Role: request.GatewayRole},
		{Service: request.MaintenanceService, Database: request.MaintenanceDatabase, Role: request.MaintenanceRole},
	} {
		observed := cluster.activationServiceIdentity(t, ctx, identity.Service, identity.Database)
		if observed != identity {
			t.Fatalf("canonical activation service %s identity=%+v, want=%+v", identity.Service, observed, identity)
		}
	}
}

func (cluster *activationPostgresCluster) assertActivationEvidenceServiceIdentities(t *testing.T, request ExecutionRequest, attempt *ActivationAttempt) {
	t.Helper()
	if attempt == nil || attempt.Observations == nil {
		t.Fatal("real activation evidence observations are missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observations := *attempt.Observations
	for _, identity := range []struct {
		label    string
		evidence DatabaseIdentity
		expected DatabaseIdentity
	}{
		{"source", observations.Source, DatabaseIdentity{Service: request.SourceService, Database: request.SourceDatabase, Role: request.SourceRole}},
		{"Platform", observations.Platform, DatabaseIdentity{Service: request.TargetService, Database: request.TargetDatabase, Role: request.TargetRole}},
		{"Gateway", observations.Gateway, DatabaseIdentity{Service: request.GatewayService, Database: request.GatewayDatabase, Role: request.GatewayRole}},
		{"maintenance", observations.Maintenance, DatabaseIdentity{Service: request.MaintenanceService, Database: request.MaintenanceDatabase, Role: request.MaintenanceRole}},
	} {
		observed := cluster.activationServiceIdentity(t, ctx, identity.expected.Service, identity.expected.Database)
		if identity.evidence != identity.expected || identity.evidence != observed {
			t.Fatalf("activation evidence %s identity=%+v, expected=%+v observed=%+v", identity.label, identity.evidence, identity.expected, observed)
		}
	}
	if observations.Platform.Role != request.TargetRole || observations.PlatformMigratorRole != request.TargetRole ||
		observations.Gateway.Role != request.GatewayRole || observations.GatewayMigratorRole != request.GatewayRole {
		t.Fatalf("activation evidence migrator identities are not bound to migration services: %+v", observations)
	}
	for _, runtime := range []DatabaseIdentity{
		{Database: request.TargetDatabase, Role: observations.PlatformRuntimeRole},
		{Database: request.GatewayDatabase, Role: observations.GatewayRuntimeRole},
	} {
		pool := cluster.pool(t, runtime.Role, runtime.Database)
		var database, role string
		err := pool.QueryRow(ctx, `SELECT current_database(),current_user`).Scan(&database, &role)
		pool.Close()
		if err != nil {
			t.Fatalf("probe activation evidence runtime %s: %v", runtime.Role, err)
		}
		if database != runtime.Database || role != runtime.Role {
			t.Fatalf("activation evidence runtime identity=%s/%s, want=%s/%s", role, database, runtime.Role, runtime.Database)
		}
	}
	if observations.PlatformRuntimeRole != request.TargetRuntimeRole || observations.GatewayRuntimeRole != request.GatewayRuntimeRole ||
		observations.PlatformRuntimeRole == observations.Platform.Role || observations.GatewayRuntimeRole == observations.Gateway.Role ||
		observations.PlatformAuthority.RuntimeRole != observations.PlatformRuntimeRole || observations.GatewayAuthority.RuntimeRole != observations.GatewayRuntimeRole {
		t.Fatalf("activation evidence runtime roles are not separately bound: %+v", observations)
	}
}

func (cluster *activationPostgresCluster) activationServiceIdentity(t *testing.T, ctx context.Context, service, database string) DatabaseIdentity {
	t.Helper()
	pool, err := pgxpool.New(ctx, "service="+service+" dbname="+database)
	if err != nil {
		t.Fatalf("open canonical activation service %s: %v", service, err)
	}
	defer pool.Close()
	var observed DatabaseIdentity
	observed.Service = service
	if err := pool.QueryRow(ctx, `SELECT current_database(),current_user`).Scan(&observed.Database, &observed.Role); err != nil {
		t.Fatalf("probe canonical activation service %s: %v", service, err)
	}
	return observed
}

func (cluster *activationPostgresCluster) applyMigrationsAndAuthority(t *testing.T, database string, matrix authority.Matrix, migrationDir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := cluster.pool(t, "bt_g010_cluster_admin", database)
	defer pool.Close()
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, "SET ROLE "+quotePostgresIdentifier(matrix.Roles.Migrator)); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = connection.Exec(context.Background(), "RESET ROLE") }()
	if _, err := connection.Exec(ctx, "ALTER SCHEMA public OWNER TO "+quotePostgresIdentifier(matrix.Roles.Migrator)); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(migrationDir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		versionText, _, ok := strings.Cut(name, "_")
		version, parseErr := strconv.Atoi(versionText)
		if !ok || parseErr != nil {
			t.Fatalf("parse migration version %s", name)
		}
		data, readErr := os.ReadFile(filepath.Join(migrationDir, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		sum := sha256.Sum256(data)
		tx, txErr := connection.Begin(ctx)
		if txErr != nil {
			t.Fatal(txErr)
		}
		if _, txErr = tx.Exec(ctx, string(data)); txErr == nil {
			ledger := "platform_schema_migrations"
			if matrix.Name == authority.GatewayV6().Name {
				ledger = "schema_migrations"
			}
			_, txErr = tx.Exec(ctx, "INSERT INTO "+ledger+"(version,checksum) VALUES ($1,$2)", version, hex.EncodeToString(sum[:]))
		}
		if txErr != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("apply migration %s: %v", name, txErr)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit migration %s: %v", name, err)
		}
	}

	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	runtimeRole, writerRole := quotePostgresIdentifier(matrix.Roles.Runtime), quotePostgresIdentifier(matrix.Roles.Writer)
	for _, statement := range []string{
		"REVOKE CONNECT ON DATABASE " + quotePostgresIdentifier(database) + " FROM PUBLIC",
		"REVOKE CONNECT ON DATABASE " + quotePostgresIdentifier(database) + " FROM " + writerRole,
		"GRANT CONNECT ON DATABASE " + quotePostgresIdentifier(database) + " TO " + runtimeRole,
		"REVOKE ALL ON SCHEMA public FROM PUBLIC",
		"REVOKE ALL ON SCHEMA public FROM " + runtimeRole + ", " + writerRole,
		"REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC",
		"REVOKE ALL ON ALL TABLES IN SCHEMA public FROM " + runtimeRole + ", " + writerRole,
		"REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC",
		"REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM " + runtimeRole + ", " + writerRole,
		"REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC",
		"REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM " + runtimeRole + ", " + writerRole,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM " + runtimeRole + ", " + writerRole,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM " + runtimeRole + ", " + writerRole,
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON FUNCTIONS FROM " + runtimeRole + ", " + writerRole,
		"GRANT USAGE ON SCHEMA public TO " + runtimeRole + ", " + writerRole,
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatalf("apply %s authority: %v", matrix.Name, err)
		}
	}
	for _, object := range matrix.Objects {
		kind := map[authority.ObjectKind]string{authority.Table: "TABLE", authority.Sequence: "SEQUENCE", authority.Routine: "FUNCTION"}[object.Kind]
		name := quotePostgresIdentifier(object.Schema) + "." + activationPostgresObjectName(object)
		for _, grant := range []struct {
			role       string
			privileges []string
		}{{matrix.Roles.Runtime, object.Runtime}, {matrix.Roles.Writer, object.Writer}} {
			if len(grant.privileges) == 0 {
				continue
			}
			if _, err := tx.Exec(ctx, "GRANT "+strings.Join(grant.privileges, ", ")+" ON "+kind+" "+name+" TO "+quotePostgresIdentifier(grant.role)); err != nil {
				t.Fatalf("grant %s on %s: %v", grant.role, object.Name, err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func activationPostgresObjectName(object authority.Object) string {
	if object.Kind != authority.Routine {
		return quotePostgresIdentifier(object.Name)
	}
	open := strings.IndexByte(object.Name, '(')
	return quotePostgresIdentifier(object.Name[:open]) + object.Name[open:]
}

func (cluster *activationPostgresCluster) pool(t *testing.T, user, database string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cluster.dsn(user, database))
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return pool
}

func (cluster *activationPostgresCluster) resetActivationAuthority(t *testing.T, revokeWriters bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin := cluster.pool(t, "bt_g010_cluster_admin", "postgres")
	defer admin.Close()
	statements := []string{
		`GRANT CONNECT ON DATABASE bolter_platform_test TO bolter_platform_test`,
		`GRANT CONNECT ON DATABASE bolter_gateway_test TO bolter_gateway_test`,
	}
	if revokeWriters {
		statements = append(statements,
			`REVOKE bolter_platform_test_writer FROM bolter_platform_test`,
			`REVOKE bolter_gateway_test_writer FROM bolter_gateway_test`)
	}
	for _, statement := range statements {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Errorf("reset isolated activation authority: %v", err)
		}
	}
}

func activationPostgresCommandRunner(ctx context.Context, command Command) (CommandResult, error) {
	if command.Name == "psql" {
		command.Name = filepath.Join(activationPostgres16Bin, "psql")
	}
	return runCommand(ctx, command)
}

func activationPostgresFailureLoggingRunner(t *testing.T) CommandRunner {
	t.Helper()
	return func(ctx context.Context, command Command) (CommandResult, error) {
		result, err := activationPostgresCommandRunner(ctx, command)
		if err != nil {
			t.Logf("real activation command failed kind=%s output=%q error=%v", activationPostgresCommandKind(command), strings.TrimSpace(string(result.Output)), err)
		}
		return result, err
	}
}

func activationPostgresCommandKind(command Command) string {
	script := string(command.Stdin)
	for _, kind := range []string{"pg_postmaster_start_time", "pg_has_role", "activation_dml", "pg_terminate_backend", "restored_target_sessions", "post_grant_target_sessions"} {
		if strings.Contains(script, kind) {
			return kind
		}
	}
	return filepath.Base(command.Name)
}

func assertActivationWriterMembership(t *testing.T, observation ActivationAuthorityObservation, want bool) {
	t.Helper()
	if observation.WriterMembership != want {
		t.Fatalf("real PostgreSQL writer membership=%t, want=%t; observation=%+v", observation.WriterMembership, want, observation)
	}
}

func assertActivationSessionIdentity(t *testing.T, ctx context.Context, connection *pgxpool.Conn, role, database string) {
	t.Helper()
	var currentRole, currentDatabase string
	if err := connection.QueryRow(ctx, `SELECT current_user,current_database()`).Scan(&currentRole, &currentDatabase); err != nil {
		t.Fatal(err)
	}
	if currentRole != role || currentDatabase != database {
		t.Fatalf("activation session identity=%s/%s, want=%s/%s", currentRole, currentDatabase, role, database)
	}
}

func (cluster *activationPostgresCluster) assertActivationRuntimeFacts(t *testing.T, wantConnect, wantWriterMembership bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin := cluster.pool(t, "bt_g010_cluster_admin", "postgres")
	defer admin.Close()
	var platformConnect, gatewayConnect, platformWriter, gatewayWriter bool
	if err := admin.QueryRow(ctx, `SELECT
		has_database_privilege('bolter_platform_test','bolter_platform_test','CONNECT'),
		has_database_privilege('bolter_gateway_test','bolter_gateway_test','CONNECT'),
		pg_has_role('bolter_platform_test','bolter_platform_test_writer','MEMBER'),
		pg_has_role('bolter_gateway_test','bolter_gateway_test_writer','MEMBER')`).Scan(
		&platformConnect, &gatewayConnect, &platformWriter, &gatewayWriter,
	); err != nil {
		t.Fatal(err)
	}
	if platformConnect != wantConnect || gatewayConnect != wantConnect || platformWriter != wantWriterMembership || gatewayWriter != wantWriterMembership {
		t.Fatalf("runtime facts platformConnect=%t gatewayConnect=%t platformWriter=%t gatewayWriter=%t; want connect=%t writer=%t",
			platformConnect, gatewayConnect, platformWriter, gatewayWriter, wantConnect, wantWriterMembership)
	}
}

func (cluster *activationPostgresCluster) assertActivationMaintenanceMemberships(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	admin := cluster.pool(t, "bt_g010_cluster_admin", "postgres")
	defer admin.Close()
	type options struct{ admin, inherit, set bool }
	want := map[string]options{
		"bolter_funding_test_migrator":  {set: true},
		"bolter_platform_test_migrator": {set: true},
		"bolter_gateway_test_migrator":  {set: true},
		"bolter_platform_test":          {admin: true},
		"bolter_gateway_test":           {admin: true},
		"bolter_funding_test_writer":    {admin: true},
		"bolter_platform_test_writer":   {admin: true},
		"bolter_gateway_test_writer":    {admin: true},
	}
	rows, err := admin.Query(ctx, `SELECT granted.rolname,membership.admin_option,membership.inherit_option,membership.set_option
		FROM pg_auth_members membership
		JOIN pg_roles granted ON granted.oid=membership.roleid
		JOIN pg_roles member ON member.oid=membership.member
		WHERE member.rolname='bolter_platform_test_admin' AND granted.rolname LIKE 'bolter_%_test%'
		ORDER BY granted.rolname`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := make(map[string]options, len(want))
	for rows.Next() {
		var role string
		var observed options
		if err := rows.Scan(&role, &observed.admin, &observed.inherit, &observed.set); err != nil {
			t.Fatal(err)
		}
		seen[role] = observed
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for role, expected := range want {
		if observed, ok := seen[role]; !ok || observed != expected {
			t.Fatalf("maintenance membership %s=%+v present=%t, want=%+v", role, observed, ok, expected)
		}
	}
}
