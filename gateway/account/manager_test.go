package account

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/config"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/shopspring/decimal"
)

const (
	testLighterPrivateKey = "00000000000000000000000000000000000000000000000000000000000000000000000000000001"
	testEVMPrivateKey     = "0000000000000000000000000000000000000000000000000000000000000001"
	testEVMAddress        = "0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf"
)

func TestManagerConstructsAllTwentyProfilesWithoutNetworkIO(t *testing.T) {
	accounts, provider := allTwentyAccountConfigs(t)
	transport := new(countingTransport)
	service, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(context.Background(), accounts, Options{
		Secrets:         provider,
		ExchangeCatalog: exchange.NewDefaultMemoryCatalog(),
		GatewayCatalog:  service,
		HTTPClient:      &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if got := len(manager.Accounts()); got != 20 {
		t.Fatalf("accounts = %d, want 20", got)
	}
	for _, account := range accounts {
		accountID := domain.AccountID(account.AccountID)
		row, err := manager.Capability(accountID)
		wantWriteReady := accountID != "lis"
		if err != nil || row.Venue != account.Venue || row.Product != account.Product || row.Settlement != account.Settlement || row.WriteReadyDefault != wantWriteReady {
			t.Fatalf("Capability(%s)=%+v err=%v", accountID, row, err)
		}
		routeStatus, err := manager.RouteStatus(accountID, account.Product, account.Settlement)
		if err != nil || routeStatus.AccountID != accountID || routeStatus.Product != exchange.Product(account.Product) {
			t.Fatalf("RouteStatus(%s)=%+v err=%v", accountID, routeStatus, err)
		}
		if account.Product == string(exchange.ProductSpot) {
			if client, err := manager.Spot(accountID); err != nil || client == nil {
				t.Fatalf("Spot(%s) client=%v err=%v", accountID, client, err)
			}
			if _, err := manager.Perp(accountID); !errors.Is(err, ErrProductMismatch) {
				t.Fatalf("Perp(%s) error=%v, want ErrProductMismatch", accountID, err)
			}
		} else {
			if client, err := manager.Perp(accountID); err != nil || client == nil {
				t.Fatalf("Perp(%s) client=%v err=%v", accountID, client, err)
			}
			if _, err := manager.Spot(accountID); !errors.Is(err, ErrProductMismatch) {
				t.Fatalf("Spot(%s) error=%v, want ErrProductMismatch", accountID, err)
			}
		}
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("factory construction performed %d HTTP requests", got)
	}
	formatted := fmt.Sprintf("%+v %#v", manager, manager)
	for _, secretValue := range []string{testLighterPrivateKey, testEVMPrivateKey, "api-secret-canary", "passphrase-canary"} {
		if strings.Contains(formatted, secretValue) {
			t.Fatalf("manager formatting leaked secret %q: %s", secretValue, formatted)
		}
	}
}

func TestManagerRefreshesAccountsIndependentlyAndPublishesCatalog(t *testing.T) {
	now := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	service, _ := catalog.NewService(time.Minute, func() time.Time { return now })
	good := &fakeHandle{instruments: []exchange.Instrument{accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "ETH-USDT", "ETHUSDT", "")}}
	bad := &fakeHandle{refreshErr: errors.New("api-secret-canary: venue unavailable")}
	handles := map[string]*fakeHandle{"good": good, "bad": bad}
	configs := []config.AccountConfig{
		{AccountID: "good", Venue: "binance", Product: "spot", Environment: "demo", Credentials: apiCredentials(t)},
		{AccountID: "bad", Venue: "okx", Product: "spot", Environment: "demo", Credentials: passphraseCredentials(t)},
	}
	manager, err := newManager(context.Background(), configs, Options{GatewayCatalog: service}, func(_ context.Context, account config.AccountConfig, _ Options) (clientHandle, error) {
		return handles[account.AccountID], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	results := manager.RefreshAll(context.Background())
	if len(results) != 2 || results[0].AccountID != "good" || results[0].Err != nil || results[1].AccountID != "bad" || results[1].Err == nil {
		t.Fatalf("refresh results = %+v", results)
	}
	if strings.Contains(results[1].Err.Error(), "api-secret-canary") {
		t.Fatalf("refresh error leaked native detail: %v", results[1].Err)
	}
	goodStatus, err := manager.Status("good")
	if err != nil || !goodStatus.ReadReady || !goodStatus.WriteReady || goodStatus.Reason != "" {
		t.Fatalf("good status=%+v err=%v", goodStatus, err)
	}
	badStatus, err := manager.Status("bad")
	if err != nil || badStatus.ReadReady || badStatus.WriteReady || badStatus.Reason != catalog.ReasonCatalogUnavailable {
		t.Fatalf("bad status=%+v err=%v", badStatus, err)
	}
	snapshot, err := manager.Instruments("good")
	if err != nil || len(snapshot.Instruments) != 1 || snapshot.Instruments[0].ID != "binance:demo:spot:ETH-USDT" {
		t.Fatalf("good instruments=%+v err=%v", snapshot, err)
	}
	if good.refreshes.Load() != 1 || bad.refreshes.Load() != 1 {
		t.Fatalf("refresh counts good=%d bad=%d", good.refreshes.Load(), bad.refreshes.Load())
	}
}

func TestManagerPrivateStreamStateParticipatesInWriteReadiness(t *testing.T) {
	service, _ := catalog.NewService(time.Minute, time.Now)
	handle := &fakeHandle{instruments: []exchange.Instrument{
		accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "ETH-USDT", "ETHUSDT", ""),
	}}
	manager, err := newManager(context.Background(), []config.AccountConfig{
		{AccountID: "good", Venue: "binance", Product: "spot", Environment: "demo", Credentials: apiCredentials(t)},
	}, Options{GatewayCatalog: service}, func(context.Context, config.AccountConfig, Options) (clientHandle, error) {
		return handle, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.Refresh(context.Background(), "good"); err != nil {
		t.Fatal(err)
	}
	if status, err := manager.Status("good"); err != nil || !status.WriteReady {
		t.Fatalf("initial status=%+v err=%v", status, err)
	}
	if err := manager.SetPrivateStreamState("good", false, ReasonPrivateStreamGap); err != nil {
		t.Fatal(err)
	}
	if status, err := manager.Status("good"); err != nil || status.WriteReady || status.Reason != ReasonPrivateStreamGap {
		t.Fatalf("gapped status=%+v err=%v", status, err)
	}
	if status, err := manager.RecoveryStatus("good"); err != nil || !status.WriteReady || status.Reason != "" {
		t.Fatalf("recovery status=%+v err=%v", status, err)
	}
	if err := manager.SetPrivateStreamState("good", true, ""); err != nil {
		t.Fatal(err)
	}
	if status, err := manager.Status("good"); err != nil || status.WriteReady || status.Reason != ReasonPrivateStreamStarting {
		t.Fatalf("reconnected-before-barrier status=%+v err=%v", status, err)
	}
	if err := manager.ObservePrivateStreamBarrier("good", "snapshot-1", 42); err != nil {
		t.Fatal(err)
	}
	if status, err := manager.Status("good"); err != nil || !status.WriteReady || status.Reason != "" {
		t.Fatalf("barrier-complete status=%+v err=%v", status, err)
	}
	if err := manager.SetPrivateStreamState("missing", false, ReasonPrivateStreamGap); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("unknown account error=%v", err)
	}
}

func TestManagerWriteReadinessRequiresExactLookupClient(t *testing.T) {
	service, _ := catalog.NewService(time.Minute, time.Now)
	handle := &fakeHandle{
		exactLookupUnavailable: true,
		instruments: []exchange.Instrument{
			accountTestInstrument(exchange.VenueGate, exchange.ProductPerp, "ETH-USDT", "ETH_USDT", "USDT"),
		},
	}
	manager, err := newManager(context.Background(), []config.AccountConfig{
		{AccountID: "gate", Venue: "gate", Product: "perp", Environment: "testnet", Settlement: "USDT", Credentials: apiCredentials(t)},
	}, Options{GatewayCatalog: service}, func(context.Context, config.AccountConfig, Options) (clientHandle, error) {
		return handle, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.Refresh(context.Background(), "gate"); err != nil {
		t.Fatal(err)
	}

	status, err := manager.Status("gate")
	if err != nil {
		t.Fatal(err)
	}
	if !status.ReadReady || status.WriteReady || status.Reason != "exact_lookup_unavailable" {
		t.Fatalf("status=%+v, want read-ready write-blocked by exact lookup", status)
	}

	resources := manager.Accounts()
	if len(resources) != 1 || resources[0].WriteReady || resources[0].Reason != "exact_lookup_unavailable" {
		t.Fatalf("accounts=%+v, want write-blocked by exact lookup", resources)
	}
}

func TestManagerConstructionFailureClosesPriorClientsAndRedactsCause(t *testing.T) {
	service, _ := catalog.NewService(time.Minute, time.Now)
	first := new(fakeHandle)
	configs := []config.AccountConfig{
		{AccountID: "first", Venue: "binance", Product: "spot", Environment: "demo", Credentials: apiCredentials(t)},
		{AccountID: "second", Venue: "okx", Product: "spot", Environment: "demo", Credentials: passphraseCredentials(t)},
	}
	_, err := newManager(context.Background(), configs, Options{GatewayCatalog: service}, func(_ context.Context, account config.AccountConfig, _ Options) (clientHandle, error) {
		if account.AccountID == "second" {
			return nil, errors.New("api-secret-canary construction detail")
		}
		return first, nil
	})
	if err == nil || strings.Contains(err.Error(), "api-secret-canary") {
		t.Fatalf("construction error = %v", err)
	}
	if got := first.closes.Load(); got != 1 {
		t.Fatalf("prior client closes = %d, want 1", got)
	}
	if status := service.Status("first"); status.Scope != (catalog.Scope{}) {
		t.Fatalf("failed construction left account registered in catalog: %+v", status)
	}
}

func TestManagerCloseIsConcurrentAndIdempotent(t *testing.T) {
	service, _ := catalog.NewService(time.Minute, time.Now)
	handle := new(fakeHandle)
	manager, err := newManager(context.Background(), []config.AccountConfig{{
		AccountID: "one", Venue: "binance", Product: "spot", Environment: "demo", Credentials: apiCredentials(t),
	}}, Options{GatewayCatalog: service}, func(context.Context, config.AccountConfig, Options) (clientHandle, error) {
		return handle, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := manager.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
	}
	wait.Wait()
	if got := handle.closes.Load(); got != 1 {
		t.Fatalf("client closes = %d, want 1", got)
	}
	if _, err := manager.Spot("one"); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Spot after close error = %v, want ErrManagerClosed", err)
	}
}

func TestManagerCloseRetriesOnlyFailedClientCleanup(t *testing.T) {
	service, _ := catalog.NewService(time.Minute, time.Now)
	stable := new(fakeHandle)
	flaky := &fakeHandle{closeFailures: 1}
	manager, err := newManager(context.Background(), []config.AccountConfig{
		{AccountID: "stable", Venue: "binance", Product: "spot", Environment: "demo", Credentials: apiCredentials(t)},
		{AccountID: "flaky", Venue: "okx", Product: "spot", Environment: "demo", Credentials: passphraseCredentials(t)},
	}, Options{GatewayCatalog: service}, func(_ context.Context, account config.AccountConfig, _ Options) (clientHandle, error) {
		if account.AccountID == "flaky" {
			return flaky, nil
		}
		return stable, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err == nil {
		t.Fatal("first Close succeeded, want retryable client cleanup failure")
	}
	if stable.closes.Load() != 1 || flaky.closes.Load() != 1 {
		t.Fatalf("first close counts stable=%d flaky=%d", stable.closes.Load(), flaky.closes.Load())
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if stable.closes.Load() != 1 || flaky.closes.Load() != 2 {
		t.Fatalf("retry close counts stable=%d flaky=%d", stable.closes.Load(), flaky.closes.Load())
	}
}

func TestManagerStartsInitialAndPeriodicCatalogRefreshAndStopsBeforeClientClose(t *testing.T) {
	service, _ := catalog.NewService(time.Minute, time.Now)
	handle := &fakeHandle{instruments: []exchange.Instrument{accountTestInstrument(exchange.VenueBinance, exchange.ProductSpot, "ETH-USDT", "ETHUSDT", "")}}
	manager, err := newManager(context.Background(), []config.AccountConfig{{
		AccountID: "one", Venue: "binance", Product: "spot", Environment: "demo", Credentials: apiCredentials(t),
	}}, Options{GatewayCatalog: service, RefreshInterval: 5 * time.Millisecond}, func(context.Context, config.AccountConfig, Options) (clientHandle, error) {
		return handle, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for handle.refreshes.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := handle.refreshes.Load(); got < 2 {
		t.Fatalf("refreshes = %d, want initial and periodic refresh", got)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	afterClose := handle.refreshes.Load()
	time.Sleep(15 * time.Millisecond)
	if got := handle.refreshes.Load(); got != afterClose {
		t.Fatalf("refresh loop continued after Close: before=%d after=%d", afterClose, got)
	}
}

type countingTransport struct{ calls atomic.Int64 }

func (transport *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return nil, errors.New("unexpected network request")
}

type fakeHandle struct {
	instruments            []exchange.Instrument
	exactLookupUnavailable bool
	refreshErr             error
	refreshes              atomic.Int64
	closes                 atomic.Int64
	closeFailures          int64
}

func (handle *fakeHandle) refresh(context.Context) ([]exchange.Instrument, error) {
	handle.refreshes.Add(1)
	return append([]exchange.Instrument(nil), handle.instruments...), handle.refreshErr
}

func (handle *fakeHandle) close() error {
	call := handle.closes.Add(1)
	if call <= handle.closeFailures {
		return errors.New("api-secret-canary close failure")
	}
	return nil
}

func (handle *fakeHandle) spot() (exchange.SpotClient, bool) { return nil, false }
func (handle *fakeHandle) perp() (exchange.PerpClient, bool) { return nil, false }
func (handle *fakeHandle) exactLookupReady() bool            { return !handle.exactLookupUnavailable }

func allTwentyAccountConfigs(t *testing.T) ([]config.AccountConfig, secret.StaticProvider) {
	t.Helper()
	api := apiCredentials(t)
	pass := passphraseCredentials(t)
	privateRef := mustRef(t, "env://PRIVATE_KEY")
	lighterRef := mustRef(t, "env://LIGHTER_KEY")
	accounts := []config.AccountConfig{
		{AccountID: "bns", Venue: "binance", Product: "spot", Environment: "live", Credentials: api},
		{AccountID: "bnp", Venue: "binance", Product: "perp", Environment: "live", Settlement: "USDT", Credentials: api},
		{AccountID: "oxs", Venue: "okx", Product: "spot", Environment: "live", Credentials: pass},
		{AccountID: "oxp", Venue: "okx", Product: "perp", Environment: "live", Settlement: "USDT", Credentials: pass},
		{AccountID: "lis", Venue: "lighter", Product: "spot", Environment: "live", Credentials: config.LighterCredentials{PrivateKeyRef: lighterRef, AccountIndex: 7, KeyIndex: 2}},
		{AccountID: "lip", Venue: "lighter", Product: "perp", Environment: "live", Settlement: "USDC", Credentials: config.LighterCredentials{PrivateKeyRef: lighterRef, AccountIndex: 7, KeyIndex: 2}},
		{AccountID: "hls", Venue: "hyperliquid", Product: "spot", Environment: "live", Credentials: config.PrivateKeyCredentials{PrivateKeyRef: privateRef, AccountAddress: testEVMAddress}},
		{AccountID: "hlp", Venue: "hyperliquid", Product: "perp", Environment: "live", Settlement: "USDC", Credentials: config.PrivateKeyCredentials{PrivateKeyRef: privateRef, AccountAddress: testEVMAddress}},
		{AccountID: "bys", Venue: "bybit", Product: "spot", Environment: "live", Credentials: api},
		{AccountID: "byu", Venue: "bybit", Product: "perp", Environment: "live", Settlement: "USDT", Credentials: api},
		{AccountID: "byc", Venue: "bybit", Product: "perp", Environment: "live", Settlement: "USDC", Credentials: api},
		{AccountID: "bgs", Venue: "bitget", Product: "spot", Environment: "live", Credentials: pass},
		{AccountID: "bgu", Venue: "bitget", Product: "perp", Environment: "live", Settlement: "USDT", Credentials: pass},
		{AccountID: "bgc", Venue: "bitget", Product: "perp", Environment: "live", Settlement: "USDC", Credentials: pass},
		{AccountID: "gts", Venue: "gate", Product: "spot", Environment: "live", Credentials: api},
		{AccountID: "gtu", Venue: "gate", Product: "perp", Environment: "live", Settlement: "USDT", Credentials: api},
		{AccountID: "ats", Venue: "aster", Product: "spot", Environment: "live", Credentials: config.AsterCredentials{UserAddress: testEVMAddress, APIWalletPrivateKeyRef: privateRef, ExpectedSigner: testEVMAddress}},
		{AccountID: "atp", Venue: "aster", Product: "perp", Environment: "live", Settlement: "USDT", Credentials: config.AsterCredentials{UserAddress: testEVMAddress, APIWalletPrivateKeyRef: privateRef, ExpectedSigner: testEVMAddress}},
		{AccountID: "nds", Venue: "nado", Product: "spot", Environment: "live", Settlement: "USDT0", Credentials: config.NadoCredentials{PrivateKeyRef: privateRef, Subaccount: "default"}},
		{AccountID: "ndp", Venue: "nado", Product: "perp", Environment: "live", Settlement: "USDT0", Credentials: config.NadoCredentials{PrivateKeyRef: privateRef, Subaccount: "default"}},
	}
	provider := secret.StaticProvider{
		"env://API_KEY":     "api-key-canary",
		"env://API_SECRET":  "api-secret-canary",
		"env://PASSPHRASE":  "passphrase-canary",
		"env://PRIVATE_KEY": testEVMPrivateKey,
		"env://LIGHTER_KEY": testLighterPrivateKey,
	}
	return accounts, provider
}

func apiCredentials(t *testing.T) config.APIKeySecretCredentials {
	t.Helper()
	return config.APIKeySecretCredentials{APIKeyRef: mustRef(t, "env://API_KEY"), APISecretRef: mustRef(t, "env://API_SECRET")}
}

func passphraseCredentials(t *testing.T) config.APIKeySecretPassphraseCredentials {
	t.Helper()
	return config.APIKeySecretPassphraseCredentials{
		APIKeyRef: mustRef(t, "env://API_KEY"), APISecretRef: mustRef(t, "env://API_SECRET"), PassphraseRef: mustRef(t, "env://PASSPHRASE"),
	}
}

func mustRef(t *testing.T, value string) secret.Ref {
	t.Helper()
	ref, err := secret.ParseRef(value)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func accountTestInstrument(venue exchange.Venue, product exchange.Product, symbol, nativeSymbol, settlement string) exchange.Instrument {
	base := strings.Split(symbol, "-")[0]
	quote := strings.TrimPrefix(symbol, base+"-")
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: venue, Product: product, Symbol: symbol, BaseAsset: base, QuoteAsset: quote,
			SettleAsset: settlement, NativeSymbol: nativeSymbol, NativeID: nativeSymbol,
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement: decimal.RequireFromString("0.01"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001"),
		},
	}
}

func TestManagerDynamicLifecycleFencesWritesAndPreservesOldGenerationOnFailedRotation(t *testing.T) {
	manager := newLifecycleTestManager(t, RuntimeModeTest)
	initial := desiredTestAccount(t, 1, 1, 1, "opaque_initial")
	created, err := manager.CreateRuntime(context.Background(), initial)
	if err != nil {
		t.Fatalf("CreateRuntime: %v", err)
	}
	if created.RuntimeVersion != 1 || created.LastAppliedDesiredStateRevision != 1 || created.LastAppliedRuntimeGeneration != 1 || created.ObservedAt.IsZero() {
		t.Fatalf("created runtime facts=%+v", created)
	}
	if err := manager.CheckWriteFence(initial.Fence()); err != nil {
		t.Fatalf("initial CheckWriteFence: %v", err)
	}

	manager.lifecycle.(*fakeRuntimeLifecycle).validateErr = errors.New("credential-canary")
	replacement := desiredTestAccount(t, 2, 2, 2, "opaque_replacement")
	if _, err := manager.RotateRuntime(context.Background(), replacement, 1); err == nil || strings.Contains(err.Error(), "credential-canary") {
		t.Fatalf("RotateRuntime error=%v, want redacted validation failure", err)
	}
	if err := manager.CheckWriteFence(initial.Fence()); err != nil {
		t.Fatalf("failed rotation fenced old generation: %v", err)
	}
	if current, err := manager.Runtime(initial.AccountID); err != nil || current.RuntimeVersion != 1 || current.LastAppliedDesiredStateRevision != 1 || current.LastAppliedRuntimeGeneration != 1 {
		t.Fatalf("failed rotation advanced runtime facts=%+v err=%v", current, err)
	}

	manager.lifecycle.(*fakeRuntimeLifecycle).validateErr = nil
	rotated, err := manager.RotateRuntime(context.Background(), replacement, 1)
	if err != nil {
		t.Fatalf("RotateRuntime: %v", err)
	}
	if rotated.RuntimeVersion != 2 || rotated.LastAppliedDesiredStateRevision != 2 || rotated.LastAppliedRuntimeGeneration != 2 || rotated.ObservedAt.Before(created.ObservedAt) {
		t.Fatalf("rotated runtime facts=%+v", rotated)
	}
	if !errors.Is(manager.CheckWriteFence(initial.Fence()), ErrStaleWriteFence) {
		t.Fatalf("old fence error=%v, want ErrStaleWriteFence", manager.CheckWriteFence(initial.Fence()))
	}
	if err := manager.CheckWriteFence(replacement.Fence()); err != nil {
		t.Fatalf("replacement CheckWriteFence: %v", err)
	}
}

func TestManagerDynamicLifecycleIsolationBlockersAndRestore(t *testing.T) {
	manager := newLifecycleTestManager(t, RuntimeModeTest)
	desired := desiredTestAccount(t, 1, 1, 1, "opaque_test")
	desired.Mode = RuntimeModeLive
	if _, err := manager.CreateRuntime(context.Background(), desired); !errors.Is(err, ErrRuntimeModeMismatch) {
		t.Fatalf("cross-mode CreateRuntime error=%v", err)
	}
	desired.Mode = RuntimeModeTest
	if _, err := manager.CreateRuntime(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	lifecycle := manager.lifecycle.(*fakeRuntimeLifecycle)
	lifecycle.blockers = RuntimeBlockers{OpenOrders: 1}
	if _, err := manager.DrainRuntime(context.Background(), desired.AccountID, 1, 2); !errors.Is(err, ErrRuntimeBlocked) {
		t.Fatalf("DrainRuntime error=%v, want ErrRuntimeBlocked", err)
	}
	lifecycle.blockers = RuntimeBlockers{}
	drained, err := manager.DrainRuntime(context.Background(), desired.AccountID, 1, 2)
	if err != nil || drained.State != RuntimeStateDraining {
		t.Fatalf("DrainRuntime=%+v err=%v", drained, err)
	}
	if !errors.Is(manager.CheckWriteFence(desired.Fence()), ErrStaleWriteFence) {
		t.Fatal("drain did not fence old writes")
	}

	restored := newLifecycleTestManager(t, RuntimeModeTest)
	if err := restored.RestoreRuntimes(context.Background(), []DesiredRuntime{drained.Desired}); err != nil {
		t.Fatalf("RestoreRuntimes: %v", err)
	}
	got, err := restored.Runtime(desired.AccountID)
	if err != nil || got.State != RuntimeStateDraining || restored.lifecycle.(*fakeRuntimeLifecycle).activations != 0 {
		t.Fatalf("restored=%+v err=%v activations=%d", got, err, restored.lifecycle.(*fakeRuntimeLifecycle).activations)
	}
}

func TestManagerRejectsRouteProfileModeConflictBeforeLifecycleValidation(t *testing.T) {
	tests := []struct {
		name        string
		managerMode RuntimeMode
		desiredMode RuntimeMode
		venue       string
		environment string
	}{
		{name: "Test rejects live", managerMode: RuntimeModeTest, desiredMode: RuntimeModeTest, venue: "binance", environment: "live"},
		{name: "Live rejects demo", managerMode: RuntimeModeLive, desiredMode: RuntimeModeLive, venue: "binance", environment: "demo"},
		{name: "Live rejects testnet", managerMode: RuntimeModeLive, desiredMode: RuntimeModeLive, venue: "bybit", environment: "testnet"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newLifecycleTestManager(t, test.managerMode)
			desired := desiredTestAccount(t, 1, 1, 1, "opaque_mode_conflict")
			desired.Mode = test.desiredMode
			desired.Venue = test.venue
			desired.VenueEnvironment = test.environment
			if _, err := manager.CreateRuntime(context.Background(), desired); err == nil {
				t.Fatal("cross-mode profile reached runtime creation")
			}
			lifecycle := manager.lifecycle.(*fakeRuntimeLifecycle)
			if lifecycle.validations != 0 || lifecycle.activations != 0 {
				t.Fatalf("lifecycle calls validations=%d activations=%d", lifecycle.validations, lifecycle.activations)
			}
		})
	}
}

func TestManagerLiveWritesAreDefaultDenied(t *testing.T) {
	manager := newLifecycleTestManager(t, RuntimeModeLive)
	desired := desiredTestAccount(t, 1, 1, 1, "opaque_live")
	desired.Mode = RuntimeModeLive
	desired.VenueEnvironment = "live"
	if _, err := manager.CreateRuntime(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(manager.CheckWriteFence(desired.Fence()), ErrLiveWriteDisabled) {
		t.Fatalf("live CheckWriteFence error=%v", manager.CheckWriteFence(desired.Fence()))
	}
}

func TestManagerRuntimeReadyFollowsAuthoritativeRuntimeState(t *testing.T) {
	manager := newLifecycleTestManager(t, RuntimeModeTest)
	if !manager.RuntimeReady() {
		t.Fatal("healthy manager with zero inventory must be control-plane ready")
	}
	desired := desiredTestAccount(t, 1, 1, 1, "opaque_ready")
	if _, err := manager.CreateRuntime(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	if !manager.RuntimeReady() {
		t.Fatal("active authoritative runtime did not report ready")
	}
	disabled := desiredTestAccount(t, 1, 1, 1, "opaque_disabled")
	disabled.AccountID = "disabled"
	if _, err := manager.CreateRuntime(context.Background(), disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.DisableRuntime(context.Background(), disabled.AccountID, 1, 2); err != nil {
		t.Fatal(err)
	}
	if !manager.RuntimeReady() {
		t.Fatal("disabled runtime made an unrelated active runtime unready")
	}
	if _, err := manager.DisableRuntime(context.Background(), desired.AccountID, 1, 2); err != nil {
		t.Fatal(err)
	}
	if !manager.RuntimeReady() {
		t.Fatal("account eligibility made the control plane unready")
	}
}

func TestManagerRuntimeReadyIgnoresOneUnreadyAccount(t *testing.T) {
	manager := newLifecycleTestManager(t, RuntimeModeTest)
	ready := desiredTestAccount(t, 1, 1, 1, "opaque_ready")
	if _, err := manager.CreateRuntime(context.Background(), ready); err != nil {
		t.Fatal(err)
	}
	unready := desiredTestAccount(t, 1, 1, 1, "opaque_unready")
	unready.AccountID = "unready"
	if _, err := manager.CreateRuntime(context.Background(), unready); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	runtime := manager.runtimes[unready.AccountID]
	runtime.Ready = false
	runtime.Reason = ReasonPrivateStreamGap
	manager.runtimes[unready.AccountID] = runtime
	manager.mu.Unlock()
	if !manager.RuntimeReady() {
		t.Fatal("one unready account made service readiness false")
	}
	serviceReady, active, readyCount := manager.RuntimeReadiness()
	if !serviceReady || active != 2 || readyCount != 1 {
		t.Fatalf("readiness service=%t active=%d ready=%d", serviceReady, active, readyCount)
	}
}

func TestManagerCloseDeactivatesActiveDynamicRuntime(t *testing.T) {
	manager := newLifecycleTestManager(t, RuntimeModeTest)
	if _, err := manager.CreateRuntime(context.Background(), desiredTestAccount(t, 1, 1, 1, "opaque_close")); err != nil {
		t.Fatal(err)
	}
	lifecycle := manager.lifecycle.(*fakeRuntimeLifecycle)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if manager.RuntimeReady() {
		t.Fatal("closed manager reported runtime ready")
	}
	if lifecycle.deactivations != 1 {
		t.Fatalf("deactivations=%d, want 1", lifecycle.deactivations)
	}
}

type fakeRuntimeLifecycle struct {
	validateErr   error
	blockers      RuntimeBlockers
	validations   int
	activations   int
	deactivations int
}

func (lifecycle *fakeRuntimeLifecycle) Validate(context.Context, DesiredRuntime) error {
	lifecycle.validations++
	return lifecycle.validateErr
}
func (lifecycle *fakeRuntimeLifecycle) Activate(context.Context, DesiredRuntime) error {
	lifecycle.activations++
	return nil
}

func (lifecycle *fakeRuntimeLifecycle) Deactivate(context.Context, domain.AccountID) error {
	lifecycle.deactivations++
	return nil
}
func (lifecycle *fakeRuntimeLifecycle) Blockers(context.Context, domain.AccountID) (RuntimeBlockers, error) {
	return lifecycle.blockers, nil
}

func newLifecycleTestManager(t *testing.T, mode RuntimeMode) *Manager {
	t.Helper()
	service, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newManager(context.Background(), nil, Options{
		GatewayCatalog: service, RuntimeMode: mode, Lifecycle: &fakeRuntimeLifecycle{},
	}, func(context.Context, config.AccountConfig, Options) (clientHandle, error) {
		return nil, errors.New("unexpected static build")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func desiredTestAccount(t *testing.T, revision, generation, credential uint64, refValue string) DesiredRuntime {
	t.Helper()
	ref, err := secret.ParseOpaqueRef(refValue)
	if err != nil {
		t.Fatal(err)
	}
	return DesiredRuntime{
		AccountID: "dynamic", Venue: "binance", VenueEnvironment: "demo", Mode: RuntimeModeTest,
		SecretRef: ref, DesiredStateRevision: revision, RuntimeGeneration: generation,
		CredentialVersion: credential, RequestedState: RuntimeStateActive, RotationFence: "rotation-fence",
	}
}
