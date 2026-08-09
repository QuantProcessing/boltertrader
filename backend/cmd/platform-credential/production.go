package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/config"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/audit"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/secret"
	"github.com/QuantProcessing/boltertrader/backend/internal/store/postgres"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type provisionRuntime struct {
	config config.Config
}

func (runtime *provisionRuntime) clear() {
	clear(runtime.config.GatewayToken)
	clear(runtime.config.ManualPreviewSigningKey)
}

func preflightProductionAccount(requestMode string) (provisionRuntime, error) {
	cfg, err := config.Load()
	if err != nil {
		return provisionRuntime{}, fmt.Errorf("PLATFORM_CONFIGURATION_REJECTED")
	}
	runtime := provisionRuntime{config: cfg}
	if cfg.Mode != "test" || requestMode != "test" || cfg.LivePrivateWritesEnabled {
		runtime.clear()
		return provisionRuntime{}, fmt.Errorf("PLATFORM_CONFIGURATION_REJECTED")
	}
	parsedGateway, err := url.Parse(cfg.GatewayURL)
	if err != nil || parsedGateway.Scheme != "http" || net.ParseIP(parsedGateway.Hostname()) == nil || !net.ParseIP(parsedGateway.Hostname()).IsLoopback() {
		runtime.clear()
		return provisionRuntime{}, fmt.Errorf("NON_LOOPBACK_GATEWAY_REJECTED")
	}
	return runtime, nil
}

func provisionProductionAccount(ctx context.Context, runtime provisionRuntime, request provisionRequest) (provisionResult, error) {
	cfg := runtime.config
	databaseURL := &url.URL{Scheme: "postgres", User: url.User(cfg.DatabaseRole), Host: "127.0.0.1:15432", Path: cfg.DatabaseName, RawQuery: "sslmode=disable"}
	database, err := sql.Open("pgx", databaseURL.String())
	if err != nil {
		return provisionResult{}, fmt.Errorf("PLATFORM_DATABASE_UNAVAILABLE")
	}
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return provisionResult{}, fmt.Errorf("PLATFORM_DATABASE_UNAVAILABLE")
	}
	var observedDatabase, observedRole string
	if err := database.QueryRowContext(ctx, `SELECT current_database(), current_user`).Scan(&observedDatabase, &observedRole); err != nil {
		return provisionResult{}, fmt.Errorf("PLATFORM_CONNECTED_IDENTITY_UNAVAILABLE")
	}
	if err := validateConnectedPlatformIdentity(observedDatabase, observedRole, cfg.DatabaseName, cfg.DatabaseRole); err != nil {
		return provisionResult{}, err
	}
	provider, err := productionSecretProvider(cfg)
	if err != nil {
		return provisionResult{}, fmt.Errorf("SECRET_STORE_UNAVAILABLE")
	}
	defer provider.Close()
	secretStore, err := secret.NewStore(secret.Config{Mode: secret.ModeTest, Namespace: secret.TestNamespace, KeyVersion: "v1"}, provider.Writer())
	if err != nil {
		return provisionResult{}, fmt.Errorf("SECRET_STORE_UNAVAILABLE")
	}
	auditService, err := audit.New(postgres.NewAuditStore(database), nil, func() string { return "audit_" + uuid.NewString() })
	if err != nil {
		return provisionResult{}, fmt.Errorf("PLATFORM_SERVICE_UNAVAILABLE")
	}
	operationID := secretstore.OperationID(uuid.NewSHA1(uuid.NameSpaceOID, []byte("platform-credential/"+request.IdempotencyKey)).String())
	gateway := &credentialGateway{baseURL: strings.TrimRight(cfg.GatewayURL, "/"), token: bytes.Clone(cfg.GatewayToken), client: &http.Client{Timeout: 10 * time.Second}}
	defer clear(gateway.token)
	service, err := accounts.New(accounts.Options{
		Store: postgres.NewAccountStore(database), Secrets: accounts.SecretStoreAdapter{Store: secretStore},
		Audit: accounts.AuditServiceAdapter{Service: auditService}, Gateway: gateway, Mode: secretstore.ModeTest,
		OperationID: func() secretstore.OperationID { return operationID },
	})
	if err != nil {
		return provisionResult{}, fmt.Errorf("PLATFORM_SERVICE_UNAVAILABLE")
	}
	routes := make([]accounts.AccountRoute, 0, len(request.Metadata.Routes))
	for _, route := range request.Metadata.Routes {
		routes = append(routes, accounts.AccountRoute{ID: route.RouteID, Product: accounts.Product(route.Product), SettlementAsset: route.SettlementAsset, RouteProfileID: route.RouteProfileID, RequestedState: accounts.RouteState(route.RequestedState)})
	}
	account, err := service.Create(accounts.WithRequestID(ctx, "platform-credential:"+request.IdempotencyKey), accounts.CreateRequest{
		Venue: request.Metadata.Venue, DisplayName: request.Metadata.DisplayName,
		ProductPolicy: append([]string(nil), request.Metadata.ProductPolicy...), Routes: routes,
		CredentialBundle: request.CredentialBundle,
	}, "platform-credential", request.IdempotencyKey)
	if err != nil {
		return provisionResult{}, fmt.Errorf("ACCOUNT_PROVISIONING_BLOCKED")
	}
	return provisionResult{AccountID: account.ID, OperationID: string(operationID), State: string(account.State)}, nil
}

func validateConnectedPlatformIdentity(observedDatabase, observedRole, expectedDatabase, expectedRole string) error {
	if observedDatabase != expectedDatabase || observedRole != expectedRole {
		return fmt.Errorf("CONNECTED_DATABASE_IDENTITY_MISMATCH")
	}
	return nil
}

type commandKeychainRunner struct{}

func (commandKeychainRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func productionSecretProvider(cfg config.Config) (*secretstore.FilesystemProvider, error) {
	var source secretstore.KeySource
	switch cfg.SecretKeySource {
	case "file":
		source = secretstore.FileKeySource{Path: cfg.SecretMasterKeyFile}
	case "keychain":
		source = secretstore.KeychainSource{Service: cfg.SecretKeychainService, Account: cfg.SecretKeychainAccount, Runner: commandKeychainRunner{}}
	default:
		return nil, fmt.Errorf("invalid key source")
	}
	return secretstore.NewFilesystemProvider(secretstore.FilesystemConfig{StateDir: cfg.SecretStoreDir, Mode: secretstore.ModeTest, KeySource: source})
}

type credentialGateway struct {
	baseURL string
	token   []byte
	client  *http.Client
}

type gatewayApplyRequest struct {
	OperationID          string         `json:"operationId"`
	AccountID            string         `json:"accountId"`
	Venue                string         `json:"venue"`
	NativeEnvironment    string         `json:"nativeEnvironment"`
	SecretRef            string         `json:"secretRef"`
	DesiredStateRevision int64          `json:"desiredStateRevision"`
	RuntimeGeneration    int64          `json:"runtimeGeneration"`
	CredentialVersion    int64          `json:"credentialVersion"`
	RequestedState       string         `json:"requestedState"`
	Routes               []gatewayRoute `json:"routes"`
}
type gatewayRoute struct {
	RouteID         string `json:"routeId"`
	Product         string `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
	RouteProfileID  string `json:"routeProfileId"`
	RequestedState  string `json:"requestedState"`
	Ready           bool   `json:"ready,omitempty"`
}
type gatewaySnapshot struct {
	OperationID          string         `json:"operationId"`
	DesiredStateRevision int64          `json:"desiredStateRevision"`
	RuntimeGeneration    int64          `json:"runtimeGeneration"`
	CredentialVersion    int64          `json:"credentialVersion"`
	RequestedState       string         `json:"requestedState"`
	Routes               []gatewayRoute `json:"routes"`
}
type gatewayEnvelope struct {
	Data struct {
		AccountID string           `json:"accountId"`
		Active    *gatewaySnapshot `json:"active"`
	} `json:"data"`
}

func (gateway *credentialGateway) ApplyDesired(ctx context.Context, apply accounts.DesiredApply) (accounts.GatewayRuntimeTruth, error) {
	routes := make([]gatewayRoute, 0, len(apply.Account.Routes))
	for _, route := range apply.Account.Routes {
		routes = append(routes, gatewayRoute{RouteID: route.ID, Product: string(route.Product), SettlementAsset: route.SettlementAsset, RouteProfileID: route.RouteProfileID, RequestedState: string(route.RequestedState)})
	}
	payload := gatewayApplyRequest{OperationID: string(apply.OperationID), AccountID: apply.Account.ID, Venue: apply.Account.Venue, NativeEnvironment: string(apply.Account.NativeEnvironment), SecretRef: apply.Account.SecretRef, DesiredStateRevision: apply.Account.DesiredStateRevision, RuntimeGeneration: apply.Account.RuntimeGeneration, CredentialVersion: apply.Account.CredentialVersion, RequestedState: string(apply.Account.State), Routes: routes}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return accounts.GatewayRuntimeTruth{}, fmt.Errorf("GATEWAY_REQUEST_REJECTED")
	}
	defer clear(encoded)
	path := gateway.baseURL + "/internal/v1/accounts/desired/" + url.PathEscape(apply.Account.ID) + "/operations/" + url.PathEscape(string(apply.OperationID))
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPut, path, bytes.NewReader(encoded))
	if err != nil {
		return accounts.GatewayRuntimeTruth{}, fmt.Errorf("GATEWAY_REQUEST_REJECTED")
	}
	httpRequest.Header.Set("Authorization", "Bearer "+string(gateway.token))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-Request-ID", string(apply.OperationID))
	httpRequest.Header.Set("X-Bolter-Mode", "test")
	response, err := gateway.client.Do(httpRequest)
	if err != nil {
		return accounts.GatewayRuntimeTruth{}, fmt.Errorf("GATEWAY_AUTHORITY_UNAVAILABLE")
	}
	defer response.Body.Close()
	if (response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted) || response.Header.Get("X-Bolter-Mode") != "test" {
		return accounts.GatewayRuntimeTruth{}, fmt.Errorf("GATEWAY_AUTHORITY_REJECTED")
	}
	var envelope gatewayEnvelope
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(&envelope); err != nil || envelope.Data.Active == nil || envelope.Data.AccountID != apply.Account.ID {
		return accounts.GatewayRuntimeTruth{}, fmt.Errorf("GATEWAY_AUTHORITY_RESPONSE_INVALID")
	}
	active := envelope.Data.Active
	truth := accounts.GatewayRuntimeTruth{OperationID: secretstore.OperationID(active.OperationID), AccountID: envelope.Data.AccountID, DesiredStateRevision: active.DesiredStateRevision, RuntimeGeneration: active.RuntimeGeneration, CredentialVersion: active.CredentialVersion, RequestedState: accounts.State(active.RequestedState), Active: true}
	for _, route := range active.Routes {
		truth.Routes = append(truth.Routes, accounts.GatewayRuntimeRoute{RouteID: route.RouteID, Product: accounts.Product(route.Product), SettlementAsset: route.SettlementAsset, RouteProfileID: route.RouteProfileID, RequestedState: accounts.RouteState(route.RequestedState), Ready: route.Ready})
	}
	return truth, nil
}

func (gateway *credentialGateway) GetRuntimeTruth(context.Context, string) (accounts.GatewayRuntimeTruth, error) {
	return accounts.GatewayRuntimeTruth{}, fmt.Errorf("GATEWAY_OPERATION_UNAVAILABLE")
}
func (gateway *credentialGateway) FinalizeRotation(context.Context, accounts.RotationCleanupRequest) (accounts.RotationCleanupProof, error) {
	return accounts.RotationCleanupProof{}, fmt.Errorf("GATEWAY_OPERATION_UNAVAILABLE")
}
func (gateway *credentialGateway) RetireAccount(context.Context, accounts.RetireRequest) (accounts.GatewayRetirement, error) {
	return accounts.GatewayRetirement{}, fmt.Errorf("GATEWAY_OPERATION_UNAVAILABLE")
}
