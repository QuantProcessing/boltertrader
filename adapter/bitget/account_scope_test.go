package bitget

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
)

func TestBitgetAccountPositionsHonorProviderAndKindScope(t *testing.T) {
	tests := []struct {
		name           string
		provider       *instrumentProvider
		scope          []enums.InstrumentKind
		wantCategories []string
		wantPositions  int
	}{
		{
			name:           "spot only",
			provider:       bitgetAccountScopeProvider("SPOT"),
			scope:          []enums.InstrumentKind{enums.KindSpot, enums.KindPerp},
			wantCategories: nil,
		},
		{
			name:           "perp excluded by explicit spot scope",
			provider:       bitgetAccountScopeProvider(bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures),
			scope:          []enums.InstrumentKind{enums.KindSpot},
			wantCategories: nil,
		},
		{
			name:           "USDT only",
			provider:       bitgetAccountScopeProvider(bitgetsdk.ProductTypeUSDTFutures),
			scope:          []enums.InstrumentKind{enums.KindSpot, enums.KindPerp},
			wantCategories: []string{bitgetsdk.ProductTypeUSDTFutures},
			wantPositions:  1,
		},
		{
			name:           "USDC only",
			provider:       bitgetAccountScopeProvider(bitgetsdk.ProductTypeUSDCFutures),
			scope:          []enums.InstrumentKind{enums.KindSpot, enums.KindPerp},
			wantCategories: []string{bitgetsdk.ProductTypeUSDCFutures},
			wantPositions:  1,
		},
		{
			name:           "both derivative settlements",
			provider:       bitgetAccountScopeProvider(bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures),
			scope:          []enums.InstrumentKind{enums.KindPerp},
			wantCategories: []string{bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures},
			wantPositions:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var gotCategories []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v3/position/current-position" {
					t.Errorf("unexpected path %s", r.URL.Path)
					http.Error(w, "unexpected path", http.StatusNotFound)
					return
				}
				category := r.URL.Query().Get("category")
				mu.Lock()
				gotCategories = append(gotCategories, category)
				mu.Unlock()
				symbol := "BTCUSDT"
				if category == bitgetsdk.ProductTypeUSDCFutures {
					symbol = "BTCPERP"
				}
				writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
					map[string]any{"symbol": symbol, "category": category, "posSide": "long", "holdMode": "one_way_mode", "qty": "0.01"},
				}}})
			}))
			defer server.Close()

			client := newAccountClient(
				bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
				tt.provider,
				clock.NewRealClock(),
				tt.scope,
			)
			positions, err := client.Positions(context.Background())
			if err != nil {
				t.Fatalf("Positions: %v", err)
			}
			mu.Lock()
			sort.Strings(gotCategories)
			mu.Unlock()
			wantCategories := append([]string(nil), tt.wantCategories...)
			sort.Strings(wantCategories)
			if !reflect.DeepEqual(gotCategories, wantCategories) {
				t.Fatalf("queried categories=%v, want %v", gotCategories, wantCategories)
			}
			if !slices.Equal(client.positionCategories, tt.wantCategories) {
				t.Fatalf("stored position categories=%v, want %v", client.positionCategories, tt.wantCategories)
			}
			if len(positions) != tt.wantPositions {
				t.Fatalf("positions=%+v, want %d", positions, tt.wantPositions)
			}
		})
	}
}

func TestBitgetAccountPositionsFailClosedForUnknownOrMismatchedNonzeroRecords(t *testing.T) {
	tests := []struct {
		name   string
		record map[string]any
		want   []string
	}{
		{
			name:   "unknown symbol",
			record: map[string]any{"symbol": "UNKNOWN", "category": bitgetsdk.ProductTypeUSDTFutures, "posSide": "long", "qty": "1"},
			want:   []string{bitgetsdk.ProductTypeUSDTFutures, "UNKNOWN"},
		},
		{
			name:   "mismatched category",
			record: map[string]any{"symbol": "BTCUSDT", "category": bitgetsdk.ProductTypeUSDCFutures, "posSide": "long", "qty": "2"},
			want:   []string{bitgetsdk.ProductTypeUSDTFutures, "BTCUSDT", bitgetsdk.ProductTypeUSDCFutures},
		},
		{
			name:   "unparseable quantity is not flat",
			record: map[string]any{"symbol": "UNKNOWN", "category": bitgetsdk.ProductTypeUSDTFutures, "posSide": "long", "qty": "not-a-number"},
			want:   []string{bitgetsdk.ProductTypeUSDTFutures, "UNKNOWN"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := bitgetAccountScopeProvider(bitgetsdk.ProductTypeUSDTFutures)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{tt.record}}})
			}))
			defer server.Close()

			client := newAccountClient(
				bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
				provider,
				clock.NewRealClock(),
				[]enums.InstrumentKind{enums.KindPerp},
			)
			positions, err := client.Positions(context.Background())
			if err == nil {
				t.Fatalf("positions=%+v, want identity failure", positions)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error=%q, want context %q", err, want)
				}
			}
			if positions != nil {
				t.Fatalf("positions=%+v, want fail-closed nil snapshot", positions)
			}
		})
	}
}

func TestBitgetAccountPositionsSkipExplicitlyFlatUnknownRecord(t *testing.T) {
	provider := bitgetAccountScopeProvider(bitgetsdk.ProductTypeUSDTFutures)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{
			map[string]any{"symbol": "BTCUSDT", "category": bitgetsdk.ProductTypeUSDTFutures, "posSide": "long", "holdMode": "one_way_mode", "qty": "0.01"},
			map[string]any{"symbol": "UNKNOWN", "category": bitgetsdk.ProductTypeUSDTFutures, "posSide": "long", "qty": "0", "total": "0", "size": "0"},
		}}})
	}))
	defer server.Close()

	client := newAccountClient(
		bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
		provider,
		clock.NewRealClock(),
		[]enums.InstrumentKind{enums.KindPerp},
	)
	positions, err := client.Positions(context.Background())
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(positions) != 1 {
		t.Fatalf("positions=%+v, want only known nonzero record", positions)
	}
	wantID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}
	if positions[0].InstrumentID != wantID {
		t.Fatalf("position id=%s, want %s", positions[0].InstrumentID, wantID)
	}
}

func TestBitgetAccountKnownPositionQuantityMustBeExplicitAndValid(t *testing.T) {
	tests := []struct {
		name      string
		quantity  map[string]any
		wantError bool
	}{
		{name: "malformed quantity", quantity: map[string]any{"qty": "not-a-number"}, wantError: true},
		{name: "missing quantity", quantity: map[string]any{"qty": "", "total": "", "size": ""}, wantError: true},
		{name: "explicit zero", quantity: map[string]any{"qty": "0"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := map[string]any{
				"symbol":   "BTCUSDT",
				"category": bitgetsdk.ProductTypeUSDTFutures,
				"posSide":  "long",
				"holdMode": "one_way_mode",
			}
			for key, value := range tt.quantity {
				record[key] = value
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v3/account/settings":
					writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"accountMode": "unified"}})
				case "/api/v3/account/assets":
					writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"assets": []any{}}})
				case "/api/v3/position/current-position":
					writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{record}}})
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					http.Error(w, "unexpected path", http.StatusNotFound)
				}
			}))
			defer server.Close()

			client := newAccountClient(
				bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()),
				bitgetAccountScopeProvider(bitgetsdk.ProductTypeUSDTFutures),
				clock.NewRealClock(),
				[]enums.InstrumentKind{enums.KindPerp},
			)
			positions, err := client.Positions(context.Background())
			if tt.wantError {
				if err == nil || positions != nil {
					t.Fatalf("Positions returned positions=%+v err=%v, want fail-closed nil snapshot", positions, err)
				}
				for _, want := range []string{"quantity", "BTCUSDT"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("Positions error=%q, want context %q", err, want)
					}
				}
				if state, stateErr := client.AccountState(context.Background()); stateErr == nil || !reflect.DeepEqual(state, model.AccountState{}) {
					t.Fatalf("AccountState returned state=%+v err=%v, want fail-closed zero state", state, stateErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Positions explicit zero: %v", err)
			}
			if len(positions) != 0 {
				t.Fatalf("Positions explicit zero=%+v, want flat position omitted", positions)
			}
			if _, err := client.AccountState(context.Background()); err != nil {
				t.Fatalf("AccountState explicit zero: %v", err)
			}
		})
	}
}

func TestBitgetAuthoritativePositionQuantityUsesFirstExplicitAlias(t *testing.T) {
	tests := []struct {
		name      string
		record    bitgetsdk.PositionRecord
		want      string
		wantError bool
	}{
		{name: "qty", record: bitgetsdk.PositionRecord{Qty: "1"}, want: "1"},
		{name: "total fallback", record: bitgetsdk.PositionRecord{Total: "2"}, want: "2"},
		{name: "size fallback", record: bitgetsdk.PositionRecord{Size: "3"}, want: "3"},
		{name: "short sign", record: bitgetsdk.PositionRecord{Qty: "1", PosSide: "short"}, want: "-1"},
		{name: "first alias wins", record: bitgetsdk.PositionRecord{Qty: "0", Total: "not-a-number"}, want: "0"},
		{name: "malformed qty", record: bitgetsdk.PositionRecord{Qty: "not-a-number"}, wantError: true},
		{name: "malformed total", record: bitgetsdk.PositionRecord{Total: "not-a-number"}, wantError: true},
		{name: "malformed size", record: bitgetsdk.PositionRecord{Size: "not-a-number"}, wantError: true},
		{name: "all empty", record: bitgetsdk.PositionRecord{}, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bitgetAuthoritativePositionQuantity(tt.record)
			if tt.wantError {
				if err == nil {
					t.Fatalf("quantity=%s, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("bitgetAuthoritativePositionQuantity: %v", err)
			}
			if got.String() != tt.want {
				t.Fatalf("quantity=%s, want %s", got, tt.want)
			}
		})
	}
}

func TestBitgetAccountCapabilitiesMatchProviderAndKindScope(t *testing.T) {
	tests := []struct {
		name          string
		provider      *instrumentProvider
		scope         []enums.InstrumentKind
		wantKinds     []enums.InstrumentKind
		wantPositions bool
	}{
		{"spot only", bitgetAccountScopeProvider("SPOT"), nil, []enums.InstrumentKind{enums.KindSpot}, false},
		{"USDT only", bitgetAccountScopeProvider(bitgetsdk.ProductTypeUSDTFutures), nil, []enums.InstrumentKind{enums.KindPerp}, true},
		{"USDC only", bitgetAccountScopeProvider(bitgetsdk.ProductTypeUSDCFutures), nil, []enums.InstrumentKind{enums.KindPerp}, true},
		{"spot plus perp", bitgetAccountScopeProvider("SPOT", bitgetsdk.ProductTypeUSDTFutures), nil, []enums.InstrumentKind{enums.KindSpot, enums.KindPerp}, true},
		{"explicit spot", bitgetAccountScopeProvider("SPOT", bitgetsdk.ProductTypeUSDTFutures), []enums.InstrumentKind{enums.KindSpot}, []enums.InstrumentKind{enums.KindSpot}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caps := newAccountClient(bitgetsdk.NewClient(), tt.provider, clock.NewRealClock(), tt.scope).Capabilities()
			gotKinds := make([]enums.InstrumentKind, 0, len(caps.Products))
			for _, product := range caps.Products {
				if !product.Account {
					t.Fatalf("product missing account capability: %+v", product)
				}
				gotKinds = append(gotKinds, product.Kind)
			}
			if !reflect.DeepEqual(gotKinds, tt.wantKinds) {
				t.Fatalf("product kinds=%v, want %v", gotKinds, tt.wantKinds)
			}
			if caps.Reports.PositionReports != tt.wantPositions {
				t.Fatalf("position reports=%v, want %v", caps.Reports.PositionReports, tt.wantPositions)
			}
		})
	}
}

func bitgetAccountScopeProvider(categories ...string) *instrumentProvider {
	provider := newInstrumentProvider()
	instruments := make([]*model.Instrument, 0, len(categories))
	for _, category := range categories {
		var record bitgetsdk.Instrument
		switch category {
		case "SPOT":
			record = bitgetsdk.Instrument{Category: category, Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online"}
		case bitgetsdk.ProductTypeUSDTFutures:
			record = bitgetsdk.Instrument{Category: category, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online"}
		case bitgetsdk.ProductTypeUSDCFutures:
			record = bitgetsdk.Instrument{Category: category, Symbol: "BTCPERP", BaseCoin: "BTC", QuoteCoin: "USDC", Status: "online"}
		}
		if instrument := instrumentFromBitget(record); instrument != nil {
			instruments = append(instruments, instrument)
		}
	}
	provider.LoadSnapshot(instruments)
	return provider
}
