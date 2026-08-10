package reference

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/shopspring/decimal"
)

func TestServiceProjectsConfiguredCapabilitiesAndDeduplicatedReferenceData(t *testing.T) {
	instrument := catalog.Instrument{
		ID: "binance:demo:spot:BTC-USDT", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentDemo,
		Product: exchange.ProductSpot, Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
		Rules: catalog.Rules{PriceIncrement: decimal.RequireFromString("0.01"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001")},
	}
	registry := &fakeRegistry{
		accounts: []account.Resource{
			{AccountID: "b", Venue: exchange.VenueBinance, Product: exchange.ProductSpot, Environment: exchange.EnvironmentDemo, ReadReady: true},
			{AccountID: "a", Venue: exchange.VenueBinance, Product: exchange.ProductSpot, Environment: exchange.EnvironmentDemo, ReadReady: true},
		},
		rows: map[domain.AccountID]capability.Row{
			"a": capability.StaticMatrix().Rows[0],
			"b": capability.StaticMatrix().Rows[0],
		},
		snapshots: map[domain.AccountID]catalog.Snapshot{
			"a": {Revision: "rev-a", Generation: 1, LoadedAt: time.Unix(1, 0), Instruments: []catalog.Instrument{instrument}},
			"b": {Revision: "rev-b", Generation: 2, LoadedAt: time.Unix(2, 0), Instruments: []catalog.Instrument{instrument}},
		},
	}
	service, err := NewService(registry)
	if err != nil {
		t.Fatal(err)
	}
	accounts := service.Accounts()
	if len(accounts) != 2 || accounts[0].AccountID != "a" || accounts[1].AccountID != "b" {
		t.Fatalf("accounts = %+v", accounts)
	}
	capabilities, err := service.Capabilities("")
	if err != nil || len(capabilities) != 2 || capabilities[0].AccountID != "a" || !capabilities[0].Row.WriteReadyDefault {
		t.Fatalf("capabilities = %+v err=%v", capabilities, err)
	}
	instruments, err := service.Instruments("")
	if err != nil || len(instruments) != 1 || instruments[0].ID != instrument.ID {
		t.Fatalf("instruments = %+v err=%v", instruments, err)
	}
	rules, err := service.Rules("")
	if err != nil || len(rules) != 1 || rules[0].InstrumentID != instrument.ID || !rules[0].Rules.PriceIncrement.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("rules = %+v err=%v", rules, err)
	}
	payload, err := json.Marshal(struct {
		Accounts     []account.Resource
		Capabilities []AccountCapability
		Instruments  []catalog.Instrument
		Rules        []InstrumentRules
	}{accounts, capabilities, instruments, rules})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"native_symbol", "native_id", "BTCUSDT", "credentials", "api-secret-canary"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("reference payload leaked %q: %s", forbidden, payload)
		}
	}
}

func TestServiceFiltersByAccountAndReportsUnknownAccount(t *testing.T) {
	registry := &fakeRegistry{
		accounts: []account.Resource{{AccountID: "one"}},
		rows:     map[domain.AccountID]capability.Row{"one": capability.StaticMatrix().Rows[0]},
		snapshots: map[domain.AccountID]catalog.Snapshot{
			"one": {Instruments: []catalog.Instrument{{ID: "binance:demo:spot:BTC-USDT"}}},
		},
	}
	service, _ := NewService(registry)
	if got, err := service.Capabilities("one"); err != nil || len(got) != 1 {
		t.Fatalf("filtered capabilities=%+v err=%v", got, err)
	}
	if _, err := service.Instruments("missing"); !errors.Is(err, account.ErrUnknownAccount) {
		t.Fatalf("unknown account instruments error=%v", err)
	}
}

func TestServiceProjectsAllCapabilitiesForMultiRouteAccountOnce(t *testing.T) {
	rows := capability.StaticMatrix().Rows
	registry := &fakeRegistry{
		accounts: []account.Resource{
			{AccountID: "multi", Product: exchange.ProductSpot},
			{AccountID: "multi", Product: exchange.ProductPerp, Settlement: "USDT"},
		},
		multiRows: map[domain.AccountID][]capability.Row{"multi": {rows[0], rows[1]}},
	}
	service, err := NewService(registry)
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := service.Capabilities("")
	products := map[string]bool{}
	for _, item := range capabilities {
		products[item.Row.Product] = true
	}
	if err != nil || len(capabilities) != 2 || !products["spot"] || !products["perp"] {
		t.Fatalf("capabilities=%+v err=%v", capabilities, err)
	}
}

func TestRiskLimitsTruthfullyRemainUnavailableUntilExchangeContractExists(t *testing.T) {
	service, _ := NewService(&fakeRegistry{})
	reference := service.RiskLimits("")
	if reference.Available || reference.Reason != ReasonRiskLimitsNotNormalized || len(reference.Limits) != 0 {
		t.Fatalf("risk limits = %+v", reference)
	}
	encoded, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "tier") || strings.Contains(string(encoded), "notional") {
		t.Fatalf("unsupported risk limits invented data: %s", encoded)
	}
}

type fakeRegistry struct {
	accounts  []account.Resource
	rows      map[domain.AccountID]capability.Row
	multiRows map[domain.AccountID][]capability.Row
	snapshots map[domain.AccountID]catalog.Snapshot
}

func (registry *fakeRegistry) Capabilities(accountID domain.AccountID) ([]capability.Row, error) {
	if rows, ok := registry.multiRows[accountID]; ok {
		return append([]capability.Row(nil), rows...), nil
	}
	row, err := registry.Capability(accountID)
	if err != nil {
		return nil, err
	}
	return []capability.Row{row}, nil
}

func (registry *fakeRegistry) Accounts() []account.Resource {
	return append([]account.Resource(nil), registry.accounts...)
}

func (registry *fakeRegistry) Capability(accountID domain.AccountID) (capability.Row, error) {
	row, ok := registry.rows[accountID]
	if !ok {
		return capability.Row{}, account.ErrUnknownAccount
	}
	return row, nil
}

func (registry *fakeRegistry) Instruments(accountID domain.AccountID) (catalog.Snapshot, error) {
	snapshot, ok := registry.snapshots[accountID]
	if !ok {
		return catalog.Snapshot{}, account.ErrUnknownAccount
	}
	return snapshot, nil
}
