package order

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	gatewaypostgres "github.com/QuantProcessing/boltertrader/gateway/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

const (
	crashChildEnv   = "GATEWAY_ORDER_CRASH_CHILD"
	crashDSNEnv     = "GATEWAY_ORDER_CRASH_DSN"
	crashSchemaEnv  = "GATEWAY_ORDER_CRASH_SCHEMA"
	crashVenueEnv   = "GATEWAY_ORDER_CRASH_VENUE"
	crashAccountEnv = "GATEWAY_ORDER_CRASH_ACCOUNT"
	crashPhaseEnv   = "GATEWAY_ORDER_CRASH_PHASE"
	crashModeEnv    = "GATEWAY_ORDER_CRASH_MODE"
)

func TestCoordinatorRealPostgresProcessCrashWindows(t *testing.T) {
	if os.Getenv(crashChildEnv) == "1" {
		runCoordinatorCrashChild(t)
		return
	}
	dsn := os.Getenv("GATEWAY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GATEWAY_TEST_POSTGRES_DSN is not set; skipping process crash test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	schema := fmt.Sprintf("gateway_order_crash_%d_%d", os.Getpid(), time.Now().UnixNano())
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	}()
	pool := crashPool(t, dsn, schema)
	defer pool.Close()
	if err := gatewaypostgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	venue := newCrashVenue()
	server := httptest.NewServer(venue)
	defer server.Close()

	tests := []struct {
		phase        string
		finalState   domain.OrderState
		venueCalls   int
		externalKill bool
	}{
		{phase: "after_prepared", finalState: domain.StateOpen, venueCalls: 1},
		{phase: "after_dispatching", finalState: domain.StateRejected, venueCalls: 0},
		{phase: "during_venue", finalState: domain.StateOpen, venueCalls: 1, externalKill: true},
		{phase: "after_venue", finalState: domain.StateOpen, venueCalls: 1},
		{phase: "after_committed", finalState: domain.StateOpen, venueCalls: 1},
	}
	for index, test := range tests {
		t.Run(test.phase, func(t *testing.T) {
			accountID := domain.AccountID(fmt.Sprintf("crash-%d-%s", index, strings.ReplaceAll(test.phase, "_", "-")))
			if err := gatewaypostgres.NewStore(pool).RegisterAccount(ctx, gatewaypostgres.AccountRegistration{
				AccountID: accountID, Venue: "binance", Product: "spot", Environment: "demo",
				ConfigHash: strings.Repeat("a", 64), Status: "enabled",
			}); err != nil {
				t.Fatalf("register account: %v", err)
			}

			if test.externalKill {
				received := venue.Block(accountID)
				command := crashChildCommand(dsn, schema, server.URL, accountID, test.phase, "initial")
				var output bytes.Buffer
				command.Stdout, command.Stderr = &output, &output
				if err := command.Start(); err != nil {
					t.Fatalf("start crash child: %v", err)
				}
				select {
				case <-received:
				case <-ctx.Done():
					_ = command.Process.Kill()
					t.Fatalf("venue handoff was not observed: %v output=%s", ctx.Err(), output.String())
				}
				if err := command.Process.Kill(); err != nil {
					t.Fatalf("kill child: %v", err)
				}
				if err := command.Wait(); err == nil {
					t.Fatalf("killed child exited successfully: %s", output.String())
				}
				venue.Unblock(accountID)
			} else {
				output, err := crashChildCommand(dsn, schema, server.URL, accountID, test.phase, "initial").CombinedOutput()
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 91 {
					t.Fatalf("crash child error=%v output=%s", err, output)
				}
			}

			output, err := crashChildCommand(dsn, schema, server.URL, accountID, "", "recover").CombinedOutput()
			if err != nil {
				t.Fatalf("restart child: %v output=%s", err, output)
			}
			var state domain.OrderState
			if err := pool.QueryRow(ctx, `SELECT state FROM orders WHERE account_id = $1`, accountID).Scan(&state); err != nil {
				t.Fatalf("read recovered order: %v", err)
			}
			if state != test.finalState {
				t.Fatalf("recovered state=%s, want %s", state, test.finalState)
			}
			if got := venue.Calls(accountID); got != test.venueCalls {
				t.Fatalf("venue writes=%d, want %d", got, test.venueCalls)
			}
		})
	}
}

func crashChildCommand(dsn, schema, venueURL string, accountID domain.AccountID, phase, mode string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestCoordinatorRealPostgresProcessCrashWindows$", "-test.v")
	command.Env = append(os.Environ(),
		crashChildEnv+"=1",
		crashDSNEnv+"="+dsn,
		crashSchemaEnv+"="+schema,
		crashVenueEnv+"="+venueURL,
		crashAccountEnv+"="+string(accountID),
		crashPhaseEnv+"="+phase,
		crashModeEnv+"="+mode,
	)
	return command
}

func runCoordinatorCrashChild(t *testing.T) {
	pool := crashPool(t, os.Getenv(crashDSNEnv), os.Getenv(crashSchemaEnv))
	defer pool.Close()
	accountID := domain.AccountID(os.Getenv(crashAccountEnv))
	instrument := crashInstrument()
	driver := &crashHTTPDriver{
		client: &http.Client{Timeout: 5 * time.Second}, endpoint: os.Getenv(crashVenueEnv),
		accountID: accountID,
	}
	router := &fakeRouter{route: Route{
		Driver: driver, Instrument: instrument, Product: exchange.ProductSpot, WriteReady: true,
		CheckWriteFence: func(account.WriteFence) error { return nil },
	}}
	phase := os.Getenv(crashPhaseEnv)
	hooks := Hooks{}
	switch phase {
	case "after_prepared":
		hooks.AfterPrepared = crashStoredCommandHook
	case "after_dispatching":
		hooks.AfterDispatching = crashStoredCommandHook
	case "after_venue":
		hooks.AfterVenue = crashStoredCommandHook
	case "after_committed":
		hooks.AfterCommitted = crashStoredCommandHook
	}
	coordinator, err := NewCoordinator(Options{
		Store: gatewaypostgres.NewStore(pool), Router: router,
		Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
		Hooks:    hooks,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if _, err := coordinator.RecoverAccount(context.Background(), accountID); err != nil {
		t.Fatalf("RecoverAccount: %v", err)
	}
	if os.Getenv(crashModeEnv) == "recover" {
		return
	}
	_, err = coordinator.Place(context.Background(), PlaceRequest{
		PrincipalID: "crash-principal", AccountID: accountID,
		RouteID:        "spot",
		InstrumentID:   "binance:demo:spot:BTC-USDT",
		IdempotencyKey: domain.IdempotencyKey("idem-" + strings.ReplaceAll(phase, "_", "-")),
		Side:           exchange.SideBuy, Type: exchange.OrderTypeLimit,
		Quantity: decimal.RequireFromString("0.001"), LimitPrice: decimal.RequireFromString("100"),
		LimitPolicy: exchange.LimitPolicyResting,
		WriteFence:  account.WriteFence{AccountID: accountID, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1},
	})
	if phase == "during_venue" {
		t.Fatal("during-venue child was not killed")
	}
	t.Fatalf("Place returned before crash: %v", err)
}

func crashStoredCommandHook(context.Context, store.StoredCommand) error {
	os.Exit(91)
	return nil
}

func crashPool(t *testing.T, dsn, schema string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse postgres: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("connect postgres schema: %v", err)
	}
	return pool
}

func crashInstrument() exchange.Instrument {
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueBinance, Product: exchange.ProductSpot,
			Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT",
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement:    decimal.RequireFromString("0.01"),
			QuantityIncrement: decimal.RequireFromString("0.001"),
			MinQuantity:       decimal.RequireFromString("0.001"),
		},
	}
}

type crashHTTPDriver struct {
	client    *http.Client
	endpoint  string
	accountID domain.AccountID
}

func (driver *crashHTTPDriver) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	payload, _ := json.Marshal(map[string]string{"client_order_id": request.ClientOrderID})
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, driver.endpoint+"/place", bytes.NewReader(payload))
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	httpRequest.Header.Set("X-Account-ID", string(driver.accountID))
	response, err := driver.client.Do(httpRequest)
	if err != nil {
		return exchange.OrderAcknowledgement{}, exchange.ErrAmbiguousOutcome
	}
	defer response.Body.Close()
	return exchange.OrderAcknowledgement{
		Venue: exchange.VenueBinance, Product: exchange.ProductSpot,
		Operation: exchange.OrderOperationPlace, State: exchange.AckResting,
		NativeSymbol: request.Instrument.Identity.NativeSymbol,
		OrderType:    request.Type, OrderID: "venue-" + request.ClientOrderID,
		ClientOrderID: request.ClientOrderID,
	}, nil
}

func (driver *crashHTTPDriver) CancelOrder(context.Context, exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return exchange.OrderAcknowledgement{}, errors.New("cancel is not used by crash harness")
}

func (driver *crashHTTPDriver) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (ExactResult, error) {
	clientOrderID := request.ClientOrderID
	if clientOrderID == "" && strings.HasPrefix(request.VenueOrderID, "venue-") {
		clientOrderID = strings.TrimPrefix(request.VenueOrderID, "venue-")
	}
	query := url.Values{"account": {string(driver.accountID)}, "client": {clientOrderID}}
	response, err := driver.client.Get(driver.endpoint + "/lookup?" + query.Encode())
	if err != nil {
		return ExactResult{}, err
	}
	defer response.Body.Close()
	var payload struct {
		Found bool `json:"found"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return ExactResult{}, err
	}
	if !payload.Found {
		if request.VenueOrderID != "" {
			return ExactResult{
				Status: exchange.ExactOrderNotFound,
				Evidence: exchange.ExactOrderEvidence{
					Scope: exchange.ExactOrderScope{
						Venue: exchange.VenueBinance, Product: exchange.ProductSpot, Instrument: request.Instrument,
					},
					Method:        exchange.ExactOrderEvidenceByVenueOrderID,
					Authoritative: true, Complete: true, ObservedVenueOrderID: request.VenueOrderID,
				},
			}, nil
		}
		return notFoundExactResult(*request.Instrument, clientOrderID), nil
	}
	result := foundExactResult(*request.Instrument, clientOrderID, "venue-"+clientOrderID, domain.StateOpen)
	if request.VenueOrderID != "" {
		result.Evidence.Method = exchange.ExactOrderEvidenceByVenueOrderID
		result.Evidence.ObservedClientOrderID = ""
		result.Evidence.ObservedVenueOrderID = request.VenueOrderID
	}
	return result, nil
}

type crashVenue struct {
	mu       sync.Mutex
	orders   map[string]bool
	calls    map[domain.AccountID]int
	blocked  map[domain.AccountID]chan struct{}
	releases map[domain.AccountID]chan struct{}
}

func newCrashVenue() *crashVenue {
	return &crashVenue{
		orders: make(map[string]bool), calls: make(map[domain.AccountID]int),
		blocked: make(map[domain.AccountID]chan struct{}), releases: make(map[domain.AccountID]chan struct{}),
	}
}

func (venue *crashVenue) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/place":
		var payload map[string]string
		_ = json.NewDecoder(request.Body).Decode(&payload)
		accountID := domain.AccountID(request.Header.Get("X-Account-ID"))
		key := string(accountID) + "/" + payload["client_order_id"]
		venue.mu.Lock()
		venue.calls[accountID]++
		venue.orders[key] = true
		received := venue.blocked[accountID]
		release := venue.releases[accountID]
		if received != nil {
			select {
			case <-received:
			default:
				close(received)
			}
		}
		venue.mu.Unlock()
		if release != nil {
			<-release
		}
		writer.WriteHeader(http.StatusOK)
	case "/lookup":
		accountID := domain.AccountID(request.URL.Query().Get("account"))
		key := string(accountID) + "/" + request.URL.Query().Get("client")
		venue.mu.Lock()
		found := venue.orders[key]
		venue.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(map[string]bool{"found": found})
	default:
		http.NotFound(writer, request)
	}
}

func (venue *crashVenue) Block(accountID domain.AccountID) <-chan struct{} {
	venue.mu.Lock()
	defer venue.mu.Unlock()
	received := make(chan struct{})
	venue.blocked[accountID] = received
	venue.releases[accountID] = make(chan struct{})
	return received
}

func (venue *crashVenue) Unblock(accountID domain.AccountID) {
	venue.mu.Lock()
	defer venue.mu.Unlock()
	if release := venue.releases[accountID]; release != nil {
		close(release)
	}
	delete(venue.blocked, accountID)
	delete(venue.releases, accountID)
}

func (venue *crashVenue) Calls(accountID domain.AccountID) int {
	venue.mu.Lock()
	defer venue.mu.Unlock()
	return venue.calls[accountID]
}
