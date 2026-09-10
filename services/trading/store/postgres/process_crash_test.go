package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// The child runs the real manual submission, command transaction and recovery
// worker. Only Gateway is an HTTP fixture; no venue credentials are loaded.
func TestTradingRealPostgresProcessCrashWindows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires explicit isolated PostgreSQL")
	}
	if os.Getenv("BT_CRASH_CHILD") == "1" {
		tradingCrashChild(t)
		return
	}
	for _, phase := range []string{"after_created", "after_claimed", "during_http", "after_http", "lost_http", "after_persisted"} {
		t.Run(phase, func(t *testing.T) {
			db, _ := openIntegrationDatabase(t)
			var schema string
			if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
				t.Fatal(err)
			}
			var writes, reads atomic.Int32
			reached := make(chan struct{}, 1)
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					writes.Add(1)
					if phase == "during_http" {
						reached <- struct{}{}
						select {
						case <-r.Context().Done():
						case <-release:
						}
						return
					}
					if phase == "lost_http" {
						c, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = c.Close()
						}
						return
					}
					var request manual.GatewayRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						http.Error(w, "invalid", 400)
						return
					}
					_ = json.NewEncoder(w).Encode(manual.GatewayResult{CommandID: request.CommandID, AccountID: request.AccountID, State: manual.GatewayStateSubmitted, Handoff: crashEvidence("submitted", 1)})
					return
				}
				reads.Add(1)
				if writes.Load() == 0 {
					_ = json.NewEncoder(w).Encode(orders.HandoffResult{Outcome: orders.HandoffNotAttempted})
					return
				}
				_ = json.NewEncoder(w).Encode(crashEvidence("canceled", 2))
			}))
			defer func() { close(release); server.Close() }()
			child := func(mode string) *exec.Cmd {
				c := exec.Command(os.Args[0], "-test.run=^TestTradingRealPostgresProcessCrashWindows$", "-test.timeout=25s")
				c.Env = append(os.Environ(), "BT_CRASH_CHILD=1", "BT_CRASH_SCHEMA="+schema, "BT_CRASH_URL="+server.URL, "BT_CRASH_PHASE="+phase, "BT_CRASH_MODE="+mode)
				return c
			}
			c := child("submit")
			if phase == "during_http" {
				var output bytes.Buffer
				c.Stdout, c.Stderr = &output, &output
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				select {
				case <-reached:
				case <-time.After(15 * time.Second):
					_ = c.Process.Kill()
					_ = c.Wait()
					t.Fatalf("child never sent: %s", output.String())
				}
				if err := c.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := c.Wait(); err == nil {
					t.Fatal("killed child exited successfully")
				}
			} else {
				output, err := c.CombinedOutput()
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != 91 {
					t.Fatalf("crash: %v %s", err, output)
				}
			}
			for range 2 {
				if output, err := child("recover").CombinedOutput(); err != nil {
					t.Fatalf("recovery: %v %s", err, output)
				}
			}
			commands, err := NewCommandStore(db).ListOwned(context.Background(), "p", "manual")
			if err != nil || len(commands) != 1 {
				t.Fatalf("commands=%v err=%v", commands, err)
			}
			command := commands[0]
			want, wantWrites := orders.StateCanceled, int32(1)
			if phase == "after_created" {
				want, wantWrites = orders.StateFailed, 0
			}
			if phase == "after_claimed" {
				want, wantWrites = orders.StateUncertain, 0
			}
			if command.State != want || writes.Load() != wantWrites {
				t.Fatalf("state=%s writes=%d want=%s/%d", command.State, writes.Load(), want, wantWrites)
			}
			if phase == "after_claimed" && (command.Reconciliation == nil || command.Reconciliation.Reason != orders.ReconciliationLookupFailed || command.Reconciliation.AttemptCount < 1) {
				t.Fatalf("lost persistent diagnostics: %+v", command)
			}
			if phase == "after_created" && reads.Load() != 0 {
				t.Fatal("expired unsent command queried Gateway")
			}
		})
	}
}

func crashEvidence(state string, version int64) orders.HandoffResult {
	return orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: orders.ExecutionEvidence{
		CommandID: "gateway-command", GatewayOrderID: "gateway-order", ClientOrderID: "client-order", State: state, ResourceVersion: version,
		OrderEvents: []orders.OrderEventEvidence{{EventID: fmt.Sprintf("event-%d", version), State: state, ResourceVersion: version, OccurredAt: time.Date(2026, 9, 10, 0, 0, int(version), 0, time.UTC)}},
	}}
}

type crashCommandStore struct {
	*CommandStore
	phase string
}

func (s crashCommandStore) Create(ctx context.Context, c orders.Command) (orders.Command, bool, error) {
	result, replay, err := s.CommandStore.Create(ctx, c)
	if err == nil && !replay && s.phase == "after_created" {
		os.Exit(91)
	}
	return result, replay, err
}
func (s crashCommandStore) Transition(ctx context.Context, r orders.TransitionRequest, now time.Time) (orders.Command, error) {
	result, err := s.CommandStore.Transition(ctx, r, now)
	if err == nil && ((s.phase == "after_claimed" && r.ExpectedState == orders.StateAccepted) || (s.phase == "after_persisted" && r.State == orders.StateSubmitted)) {
		os.Exit(91)
	}
	return result, err
}

type crashHTTPGateway struct{ url, phase string }

func (g crashHTTPGateway) Execute(ctx context.Context, r manual.GatewayRequest) (manual.GatewayResult, error) {
	body, _ := json.Marshal(r)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(body))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return manual.GatewayResult{Handoff: orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown}}, err
	}
	defer response.Body.Close()
	var result manual.GatewayResult
	err = json.NewDecoder(response.Body).Decode(&result)
	if err == nil && g.phase == "after_http" {
		os.Exit(91)
	}
	return result, err
}
func (g crashHTTPGateway) Reconcile(ctx context.Context, _ orders.Command) (orders.HandoffResult, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, g.url, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return orders.HandoffResult{}, err
	}
	defer response.Body.Close()
	var result orders.HandoffResult
	err = json.NewDecoder(response.Body).Decode(&result)
	return result, err
}

func tradingCrashChild(t *testing.T) {
	config, err := pgx.ParseConfig(os.Getenv("BT_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = os.Getenv("BT_CRASH_SCHEMA")
	db := stdlib.OpenDB(*config)
	defer db.Close()
	recovering := os.Getenv("BT_CRASH_MODE") == "recover"
	phase := os.Getenv("BT_CRASH_PHASE")
	if recovering {
		phase = ""
	}
	now := time.Now().UTC()
	if recovering {
		now = now.Add(time.Hour)
	}
	commands := orders.NewService(crashCommandStore{NewCommandStore(db), phase}, func() time.Time { return now })
	commands.SetLiveWritesEnabled(true)
	gateway := crashHTTPGateway{os.Getenv("BT_CRASH_URL"), phase}
	service := manual.NewService(manual.Options{Commands: commands, Portfolio: portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now), Gateway: gateway, Catalog: submissionCatalog{}, Now: func() time.Time { return now }, Accounts: submissionAccounts(func(context.Context, string, string) (manual.Account, error) {
		return manual.Account{ID: "a", Venue: "binance", VenueEnvironment: "live", RuntimeID: "runtime", State: "active", ProductIDs: []string{"manual"}, Routes: []manual.AccountRoute{{RouteID: "perp", Product: "perp"}}}, nil
	})})
	_ = service.SetExecutionAdmission(true)
	request := manual.CommandRequest{Action: "place", LineageID: "l", IdempotencyKey: "k", Order: &manual.OrderRequest{AccountID: "a", InstrumentID: "BTC-USDT", Side: "buy", OrderType: "limit", Quantity: "1", LimitPrice: "100", TimeInForce: "gtc"}}
	result, err := service.Submit(context.Background(), "p", request)
	if recovering {
		if err != nil || result.Changed {
			t.Fatalf("replay changed: %+v %v", result, err)
		}
		runner, err := orders.NewReconciler(commands, gateway, service, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		return
	}
	if phase == "lost_http" {
		persisted, loadErr := commands.ListOwned(context.Background(), "p", "manual")
		if loadErr == nil && len(persisted) == 1 && persisted[0].State == orders.StateUncertain {
			os.Exit(91)
		}
	}
	t.Fatalf("crash point was not reached: %+v %v", result, err)
}
