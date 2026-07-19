package exchange

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestPublicSurfaceManifestDefinesEightProductRows(t *testing.T) {
	manifest := loadPublicSurfaceManifest(t, repositoryRoot(t))
	rows := productRowsByCode(t, manifest.ProductRows)

	want := map[string]exchangeProductRow{
		"BNS": {Venue: "Binance", Product: "Spot", FactoryConfig: "BinanceSpotConfig", AcceptanceTarget: "test-exchange-binance-demo-spot"},
		"BNP": {Venue: "Binance", Product: "USD-M Perp", FactoryConfig: "BinanceUSDPerpConfig", AcceptanceTarget: "test-exchange-binance-demo-perp"},
		"OXS": {Venue: "OKX", Product: "Spot", FactoryConfig: "OKXSpotConfig", AcceptanceTarget: "test-exchange-okx-demo-spot"},
		"OXP": {Venue: "OKX", Product: "USDT-linear SWAP", FactoryConfig: "OKXUSDTPerpConfig", AcceptanceTarget: "test-exchange-okx-demo-perp"},
		"LIS": {Venue: "Lighter", Product: "Spot", FactoryConfig: "LighterSpotConfig", AcceptanceTarget: "test-exchange-lighter-testnet-spot"},
		"LIP": {Venue: "Lighter", Product: "Perp", FactoryConfig: "LighterPerpConfig", AcceptanceTarget: "test-exchange-lighter-testnet-perp"},
		"HLS": {Venue: "Hyperliquid", Product: "Spot", FactoryConfig: "HyperliquidSpotConfig", AcceptanceTarget: "test-exchange-hyperliquid-testnet-spot"},
		"HLP": {Venue: "Hyperliquid", Product: "Standard Perp", FactoryConfig: "HyperliquidPerpConfig", AcceptanceTarget: "test-exchange-hyperliquid-testnet-perp"},
	}
	if len(rows) != len(want) {
		t.Fatalf("manifest product rows = %d, want %d: %v", len(rows), len(want), mapKeys(rows))
	}
	for code, wantRow := range want {
		t.Run(code, func(t *testing.T) {
			got, ok := rows[code]
			if !ok {
				t.Fatalf("missing product row %s", code)
			}
			if got.Venue != wantRow.Venue || got.Product != wantRow.Product || got.FactoryConfig != wantRow.FactoryConfig || got.AcceptanceTarget != wantRow.AcceptanceTarget {
				t.Fatalf("row %s metadata = %#v, want venue/product/config/target %#v", code, got, wantRow)
			}
			assertSameStrings(t, got.RESTMethods, expectedRESTMethodsForProduct(wantRow.Product))
			assertSameStrings(t, got.WebSocketMethods, expectedWebSocketMethodsForProduct(wantRow.Product))
			if len(got.Fixtures) == 0 {
				t.Fatalf("row %s must declare fixture coverage", code)
			}
			for _, fixture := range got.Fixtures {
				if !strings.HasSuffix(fixture, "_test.go") {
					t.Fatalf("row %s fixture %q must point at a test artifact", code, fixture)
				}
			}
		})
	}
}

func TestPublicSurfaceManifestDefinesRESTAndWebSocketMethodMatrix(t *testing.T) {
	manifest := loadPublicSurfaceManifest(t, repositoryRoot(t))
	assertMethodSupport(t, manifest.RESTMethods, map[string][2]bool{
		"Instruments":        {true, true},
		"OrderBook":          {true, true},
		"Candles":            {true, true},
		"PublicTrades":       {true, true},
		"PlaceOrder":         {true, true},
		"CancelOrder":        {true, true},
		"OpenOrders":         {true, true},
		"OrderHistory":       {true, true},
		"Fills":              {true, true},
		"Balances":           {true, true},
		"SpotAccount":        {true, false},
		"PerpAccount":        {false, true},
		"Positions":          {false, true},
		"FundingRate":        {false, true},
		"FundingRateHistory": {false, true},
		"SetLeverage":        {false, true},
	})
	assertMethodSupport(t, manifest.WebSocketMethods, map[string][2]bool{
		"WatchOrderBook":    {true, true},
		"WatchBBO":          {true, true},
		"WatchPublicTrades": {true, true},
		"WatchCandles":      {true, true},
		"WatchOrders":       {true, true},
		"WatchFills":        {true, true},
		"WatchBalances":     {true, true},
		"PlaceOrder":        {true, true},
		"CancelOrder":       {true, true},
		"WatchPositions":    {false, true},
		"WatchMarkPrice":    {false, true},
		"WatchFundingRate":  {false, true},
		"Close":             {true, true},
	})
}

func TestPublicSurfaceManifestDefinesAcceptanceTargets(t *testing.T) {
	manifest := loadPublicSurfaceManifest(t, repositoryRoot(t))
	got := make(map[string]exchangeAcceptanceTarget, len(manifest.AcceptanceTargets))
	for _, target := range manifest.AcceptanceTargets {
		if _, duplicate := got[target.Target]; duplicate {
			t.Fatalf("duplicate acceptance target %s", target.Target)
		}
		got[target.Target] = target
	}
	want := map[string]exchangeAcceptanceTarget{
		"test-exchange-binance-demo-spot":        {Env: "BOLTER_ENABLE_BINANCE_DEMO_WRITES=1", Package: "./exchange/...", TestSelector: "^TestExchangeBinanceSpotDemoAcceptance$"},
		"test-exchange-binance-demo-perp":        {Env: "BOLTER_ENABLE_BINANCE_DEMO_WRITES=1", Package: "./exchange/...", TestSelector: "^TestExchangeBinancePerpDemoAcceptance$"},
		"test-exchange-okx-demo-spot":            {Env: "BOLTER_ENABLE_OKX_DEMO_WRITES=1", Package: "./exchange/...", TestSelector: "^TestExchangeOKXSpotDemoAcceptance$"},
		"test-exchange-okx-demo-perp":            {Env: "BOLTER_ENABLE_OKX_DEMO_WRITES=1", Package: "./exchange/...", TestSelector: "^TestExchangeOKXPerpDemoAcceptance$"},
		"test-exchange-lighter-testnet-spot":     {Env: "BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1", Package: "./exchange/...", TestSelector: "^TestExchangeLighterSpotTestnetAcceptance$"},
		"test-exchange-lighter-testnet-perp":     {Env: "BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1", Package: "./exchange/...", TestSelector: "^TestExchangeLighterPerpTestnetAcceptance$"},
		"test-exchange-hyperliquid-testnet-spot": {Env: "BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1", Package: "./exchange/...", TestSelector: "^TestExchangeHyperliquidSpotTestnetAcceptance$"},
		"test-exchange-hyperliquid-testnet-perp": {Env: "BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1", Package: "./exchange/...", TestSelector: "^TestExchangeHyperliquidPerpTestnetAcceptance$"},
	}
	if len(got) != len(want) {
		t.Fatalf("acceptance target count = %d, want %d: %v", len(got), len(want), mapKeys(got))
	}
	for target, wantTarget := range want {
		t.Run(target, func(t *testing.T) {
			gotTarget, ok := got[target]
			if !ok {
				t.Fatalf("missing acceptance target %s", target)
			}
			wantTarget.Target = target
			if gotTarget != wantTarget {
				t.Fatalf("acceptance target = %#v, want %#v", gotTarget, wantTarget)
			}
		})
	}
}

func TestPublicSurfaceManifestDefinesAcceptanceStatus(t *testing.T) {
	manifest := loadPublicSurfaceManifest(t, repositoryRoot(t))
	rows := productRowsByCode(t, manifest.ProductRows)

	wantStatus := map[string]string{
		"BNS": "passed",
		"BNP": "passed",
		"OXS": "passed",
		"OXP": "passed",
		"LIS": "waived",
		"LIP": "passed",
		"HLS": "passed",
		"HLP": "passed",
	}
	allowedStatus := map[string]bool{
		"passed":         true,
		"waived":         true,
		"not_applicable": true,
	}
	requiredLISReason := []string{
		"Lighter Testnet ETH/USDC and LIT/USDC",
		"platform-provided one-sided books",
		"user accepted waiver",
		"no synthetic liquidity/self-trade was used",
	}

	for code, want := range wantStatus {
		t.Run(code, func(t *testing.T) {
			row, ok := rows[code]
			if !ok {
				t.Fatalf("missing product row %s", code)
			}
			got := row.Acceptance
			if !allowedStatus[got.Status] {
				t.Fatalf("row %s acceptance status %q is not one of passed, waived, not_applicable", code, got.Status)
			}
			if got.Status != want {
				t.Fatalf("row %s acceptance status = %q, want %q", code, got.Status, want)
			}
			if strings.TrimSpace(got.Reason) == "" {
				t.Fatalf("row %s acceptance reason must be non-empty", code)
			}
			if code == "LIS" {
				for _, required := range requiredLISReason {
					if !strings.Contains(got.Reason, required) {
						t.Fatalf("row LIS acceptance reason %q does not contain %q", got.Reason, required)
					}
				}
				if row.AcceptanceTarget != "test-exchange-lighter-testnet-spot" {
					t.Fatalf("row LIS acceptance target = %q, want test-exchange-lighter-testnet-spot", row.AcceptanceTarget)
				}
				assertSameStrings(t, row.RESTMethods, expectedRESTMethodsForProduct(row.Product))
				assertSameStrings(t, row.WebSocketMethods, expectedWebSocketMethodsForProduct(row.Product))
			}
		})
	}
}

func TestAcceptanceTargetsResolveToConcreteTestFunctions(t *testing.T) {
	root := repositoryRoot(t)
	manifest := loadPublicSurfaceManifest(t, root)
	tests := discoverExchangeTests(t, root)
	for _, target := range manifest.AcceptanceTargets {
		selector := strings.TrimSuffix(strings.TrimPrefix(target.TestSelector, "^"), "$")
		if selector == target.TestSelector || !strings.HasPrefix(selector, "TestExchange") {
			t.Errorf("acceptance target %s has non-exact selector %q", target.Target, target.TestSelector)
			continue
		}
		if !tests[selector] {
			t.Errorf("acceptance target %s references missing test function %s", target.Target, selector)
		}
	}
}

func TestPublicSurfaceManifestDefinesOperationsForEveryInterfaceMethod(t *testing.T) {
	manifest := loadPublicSurfaceManifest(t, repositoryRoot(t))
	rows := productRowsByCode(t, manifest.ProductRows)
	operations := operationsByID(t, manifest.Operations)

	want := map[string]exchangeOperation{}
	for _, method := range manifest.RESTMethods {
		id := "rest." + method.Method
		want[id] = exchangeOperation{
			ID:            id,
			Method:        method.Method,
			Transport:     "rest",
			Spot:          method.Spot,
			Perp:          method.Perp,
			ExternalCells: cellsForSupport(rows, method.Spot, method.Perp),
		}
	}
	for _, method := range manifest.WebSocketMethods {
		id := "ws." + method.Method
		want[id] = exchangeOperation{
			ID:            id,
			Method:        method.Method,
			Transport:     "websocket",
			Spot:          method.Spot,
			Perp:          method.Perp,
			ExternalCells: cellsForSupport(rows, method.Spot, method.Perp),
		}
	}
	for id, operation := range requiredLocalOperations(rows) {
		want[id] = operation
	}

	if len(operations) != len(want) {
		t.Fatalf("operation count = %d, want %d; got %v", len(operations), len(want), mapKeys(operations))
	}
	for id, wantOperation := range want {
		t.Run(id, func(t *testing.T) {
			got, ok := operations[id]
			if !ok {
				t.Fatalf("missing operation %s", id)
			}
			if got.Method != wantOperation.Method || got.Transport != wantOperation.Transport || got.Spot != wantOperation.Spot || got.Perp != wantOperation.Perp {
				t.Fatalf("operation identity = %#v, want %#v", got, wantOperation)
			}
			assertSameStrings(t, got.ExternalCells, wantOperation.ExternalCells)
			if got.Effect != "read" && got.Effect != "mutation" && got.Effect != "lifecycle" {
				t.Fatalf("operation %s has invalid effect %q", id, got.Effect)
			}
			if got.Credentials == "" || got.FundingRequirement == "" || got.Cleanup == "" {
				t.Fatalf("operation %s must declare credentials, funding requirement, and cleanup obligation: %#v", id, got)
			}
			if got.ExpectedAck == "" && got.ExpectedEvent == "" {
				t.Fatalf("operation %s must declare an expected acknowledgement or event", id)
			}
			if len(got.Tests) == 0 {
				t.Fatalf("operation %s must cite offline drift tests", id)
			}
			if got.Transport == "rest" {
				for _, testName := range []string{
					"TestOpenAPIBinanceRESTExecutionMatrix",
					"TestOpenAPIOKXRESTExecutionMatrix",
					"TestOpenAPILighterRESTExecutionMatrix",
					"TestOpenAPIHyperliquidRESTExecutionMatrix",
				} {
					if !stringSliceContains(got.Tests, testName) {
						t.Fatalf("REST operation %s missing execution matrix test %s", id, testName)
					}
				}
			}
			if got.Transport == "websocket" {
				for _, venue := range []string{"Binance", "OKX", "Lighter", "Hyperliquid"} {
					if !stringSliceContainsSubstring(got.Tests, venue) {
						t.Fatalf("WebSocket operation %s missing venue test for %s: %v", id, venue, got.Tests)
					}
				}
			}
		})
	}
}

func TestPublicSurfaceManifestDefinesOrderParameterCases(t *testing.T) {
	manifest := loadPublicSurfaceManifest(t, repositoryRoot(t))
	rows := productRowsByCode(t, manifest.ProductRows)
	cases := parameterCasesByID(t, manifest.ParameterCases)
	type expectedCase struct {
		operationID string
		support     [2]bool
	}
	want := map[string]expectedCase{
		"place_order.rest.market":                           {"rest.PlaceOrder", [2]bool{true, true}},
		"place_order.rest.limit_resting":                    {"rest.PlaceOrder", [2]bool{true, true}},
		"place_order.rest.limit_ioc":                        {"rest.PlaceOrder", [2]bool{true, true}},
		"place_order.rest.limit_post_only":                  {"rest.PlaceOrder", [2]bool{true, true}},
		"place_order.rest.client_order_id":                  {"rest.PlaceOrder", [2]bool{true, true}},
		"place_order.rest.perp_reduce_only":                 {"rest.PlaceOrder", [2]bool{false, true}},
		"place_order.ws.market":                             {"ws.PlaceOrder", [2]bool{true, true}},
		"place_order.ws.limit_resting":                      {"ws.PlaceOrder", [2]bool{true, true}},
		"place_order.ws.limit_ioc":                          {"ws.PlaceOrder", [2]bool{true, true}},
		"place_order.ws.limit_post_only":                    {"ws.PlaceOrder", [2]bool{true, true}},
		"place_order.ws.client_order_id":                    {"ws.PlaceOrder", [2]bool{true, true}},
		"place_order.ws.perp_reduce_only":                   {"ws.PlaceOrder", [2]bool{false, true}},
		"place_order.invalid.market_price_or_policy":        {"local.PlaceOrder.Validate", [2]bool{true, true}},
		"place_order.invalid.limit_missing_price_or_policy": {"local.PlaceOrder.Validate", [2]bool{true, true}},
		"place_order.invalid.non_positive_quantity":         {"local.PlaceOrder.Validate", [2]bool{true, true}},
		"place_order.invalid.spot_reduce_only":              {"local.PlaceOrder.Validate", [2]bool{true, false}},
		"place_order.invalid.missing_client_order_id":       {"local.PlaceOrder.Validate", [2]bool{true, true}},
		"place_order.invalid.bad_client_order_id":           {"local.PlaceOrder.Validate", [2]bool{true, true}},
		"cancel_order.rest.order_id":                        {"rest.CancelOrder", [2]bool{true, true}},
		"cancel_order.ws.order_id":                          {"ws.CancelOrder", [2]bool{true, true}},
		"cancel_order.rest.invalid_missing_order_id":        {"rest.CancelOrder", [2]bool{true, true}},
		"cancel_order.rest.invalid_nonportable_order_id":    {"rest.CancelOrder", [2]bool{true, true}},
		"cancel_order.ws.invalid_missing_order_id":          {"ws.CancelOrder", [2]bool{true, true}},
		"cancel_order.ws.invalid_nonportable_order_id":      {"ws.CancelOrder", [2]bool{true, true}},
	}
	if len(cases) != len(want) {
		t.Fatalf("parameter case count = %d, want %d; got %v", len(cases), len(want), mapKeys(cases))
	}
	for id, expected := range want {
		t.Run(id, func(t *testing.T) {
			got, ok := cases[id]
			if !ok {
				t.Fatalf("missing parameter case %s", id)
			}
			if got.OperationID != expected.operationID {
				t.Fatalf("parameter case %s operation_id = %q, want %q", id, got.OperationID, expected.operationID)
			}
			if got.Spot != expected.support[0] || got.Perp != expected.support[1] {
				t.Fatalf("parameter case %s applicability = spot:%v perp:%v, want %v", id, got.Spot, got.Perp, expected.support)
			}
			assertSameStrings(t, got.ExternalCells, cellsForSupport(rows, expected.support[0], expected.support[1]))
			if got.Credentials == "" || got.FundingRequirement == "" || got.ExpectedAck == "" || got.Cleanup == "" {
				t.Fatalf("parameter case %s must declare credentials, funding, expected ack, and cleanup: %#v", id, got)
			}
			if len(got.Tests) == 0 {
				t.Fatalf("parameter case %s must cite offline tests", id)
			}
		})
	}
}

func TestPublicSurfaceManifestReferencesExistingFixturesAndTests(t *testing.T) {
	root := repositoryRoot(t)
	manifest := loadPublicSurfaceManifest(t, root)
	tests := discoverExchangeTests(t, root)

	for _, row := range manifest.ProductRows {
		for _, fixture := range row.Fixtures {
			path := filepath.Join(root, filepath.FromSlash(fixture))
			info, err := os.Stat(path)
			if err != nil {
				t.Errorf("product row %s fixture %q: %v", row.Code, fixture, err)
				continue
			}
			if info.IsDir() {
				t.Errorf("product row %s fixture %q is a directory", row.Code, fixture)
			}
		}
	}

	for _, operation := range manifest.Operations {
		for _, testName := range operation.Tests {
			if !tests[testName] {
				t.Errorf("operation %s references missing test %s", operation.ID, testName)
			}
		}
	}
	for _, parameterCase := range manifest.ParameterCases {
		for _, testName := range parameterCase.Tests {
			if !tests[testName] {
				t.Errorf("parameter case %s references missing test %s", parameterCase.ID, testName)
			}
		}
	}
}

func TestPublicSurfaceManifestUsesPortableOrderIDForClientOrderIDCleanup(t *testing.T) {
	manifest := loadPublicSurfaceManifest(t, repositoryRoot(t))
	for _, parameterCase := range manifest.ParameterCases {
		if !strings.HasSuffix(parameterCase.ID, ".client_order_id") {
			continue
		}
		if !strings.Contains(parameterCase.Cleanup, "returned OrderID") {
			t.Errorf(
				"parameter case %s cleanup = %q, want portable returned OrderID cleanup",
				parameterCase.ID,
				parameterCase.Cleanup,
			)
		}
	}
}

func TestPublicSurfaceManifestAccountsForFactoryCredentialRequirements(t *testing.T) {
	manifest := loadPublicSurfaceManifest(t, repositoryRoot(t))
	for _, operation := range manifest.Operations {
		if operation.Transport != "rest" && operation.Transport != "websocket" {
			continue
		}
		if operation.Effect == "lifecycle" {
			continue
		}
		if operation.Credentials == "none" {
			t.Errorf(
				"network operation %s credentials = none; factory construction requires credentials",
				operation.ID,
			)
		}
	}
}

func productRowsByCode(t *testing.T, rows []exchangeProductRow) map[string]exchangeProductRow {
	t.Helper()
	byCode := make(map[string]exchangeProductRow, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Code) == "" {
			t.Fatal("manifest product row has empty code")
		}
		if _, duplicate := byCode[row.Code]; duplicate {
			t.Fatalf("duplicate manifest product row %s", row.Code)
		}
		byCode[row.Code] = row
	}
	return byCode
}

func requiredLocalOperations(rows map[string]exchangeProductRow) map[string]exchangeOperation {
	all := cellsForSupport(rows, true, true)
	spot := cellsForSupport(rows, true, false)
	perp := cellsForSupport(rows, false, true)
	tests := []string{"TestEightKnownConfigsInferAndConstructProductClients", "TestAllEightConfigsConstructConcurrentlyWithoutIO"}
	operations := map[string]exchangeOperation{
		"local.factory.New":                   {ID: "local.factory.New", Method: "New", Transport: "local", Spot: true, Perp: true, Effect: "lifecycle", Credentials: "constructor credentials only", FundingRequirement: "none", ExpectedEvent: "typed product client", Cleanup: "Close constructed client", ExternalCells: all, Tests: tests},
		"local.factory.WithAccountAddress":    {ID: "local.factory.WithAccountAddress", Method: "WithAccountAddress", Transport: "local", Spot: true, Perp: true, Effect: "lifecycle", Credentials: "account address only", FundingRequirement: "none", ExpectedEvent: "Hyperliquid account identity override", Cleanup: "none", ExternalCells: []string{"HLP", "HLS"}, Tests: []string{"TestHyperliquidAccountAddressOptionIsExplicitAndValidatedLocally", "TestHyperliquidAccountAddressOptionReachesAccountScopedSDKRequests"}},
		"local.factory.WithEnvironment":       {ID: "local.factory.WithEnvironment", Method: "WithEnvironment", Transport: "local", Spot: true, Perp: true, Effect: "lifecycle", Credentials: "none", FundingRequirement: "none", ExpectedEvent: "environment-bound config", Cleanup: "none", ExternalCells: all, Tests: tests},
		"local.factory.WithEndpoint":          {ID: "local.factory.WithEndpoint", Method: "WithEndpoint", Transport: "local", Spot: true, Perp: true, Effect: "lifecycle", Credentials: "none", FundingRequirement: "none", ExpectedEvent: "REST endpoint override", Cleanup: "none", ExternalCells: all, Tests: tests},
		"local.factory.WithWebSocketEndpoint": {ID: "local.factory.WithWebSocketEndpoint", Method: "WithWebSocketEndpoint", Transport: "local", Spot: true, Perp: true, Effect: "lifecycle", Credentials: "none", FundingRequirement: "none", ExpectedEvent: "WebSocket endpoint override", Cleanup: "none", ExternalCells: all, Tests: tests},
		"local.factory.WithHTTPClient":        {ID: "local.factory.WithHTTPClient", Method: "WithHTTPClient", Transport: "local", Spot: true, Perp: true, Effect: "lifecycle", Credentials: "none", FundingRequirement: "none", ExpectedEvent: "HTTP client override", Cleanup: "none", ExternalCells: all, Tests: tests},
		"local.SpotClient.WebSocket":          {ID: "local.SpotClient.WebSocket", Method: "WebSocket", Transport: "local", Spot: true, Perp: false, Effect: "lifecycle", Credentials: "none", FundingRequirement: "none", ExpectedEvent: "SpotWebSocket facet", Cleanup: "Close facet or client", ExternalCells: spot, Tests: []string{"TestAllEightConfigsExposeLazyWebSocketFacets", "TestWebSocketFacetMethodSets"}},
		"local.PerpClient.WebSocket":          {ID: "local.PerpClient.WebSocket", Method: "WebSocket", Transport: "local", Spot: false, Perp: true, Effect: "lifecycle", Credentials: "none", FundingRequirement: "none", ExpectedEvent: "PerpWebSocket facet", Cleanup: "Close facet or client", ExternalCells: perp, Tests: []string{"TestAllEightConfigsExposeLazyWebSocketFacets", "TestWebSocketFacetMethodSets"}},
		"local.SpotClient.Close":              {ID: "local.SpotClient.Close", Method: "Close", Transport: "local", Spot: true, Perp: false, Effect: "lifecycle", Credentials: "none", FundingRequirement: "none", ExpectedEvent: "closed client", Cleanup: "idempotent close", ExternalCells: spot, Tests: []string{"TestAllEightConfigsExposeLazyWebSocketFacets", "TestPublicWebSocketContextAndClientCloseAreIdempotent"}},
		"local.PerpClient.Close":              {ID: "local.PerpClient.Close", Method: "Close", Transport: "local", Spot: false, Perp: true, Effect: "lifecycle", Credentials: "none", FundingRequirement: "none", ExpectedEvent: "closed client", Cleanup: "idempotent close", ExternalCells: perp, Tests: []string{"TestAllEightConfigsExposeLazyWebSocketFacets", "TestPublicWebSocketContextAndClientCloseAreIdempotent"}},
	}
	constructors := map[string]struct {
		support [2]bool
		cells   []string
	}{
		"local.factory.BinanceSpotConfig":     {[2]bool{true, false}, []string{"BNS"}},
		"local.factory.BinanceUSDPerpConfig":  {[2]bool{false, true}, []string{"BNP"}},
		"local.factory.OKXSpotConfig":         {[2]bool{true, false}, []string{"OXS"}},
		"local.factory.OKXUSDTPerpConfig":     {[2]bool{false, true}, []string{"OXP"}},
		"local.factory.LighterSpotConfig":     {[2]bool{true, false}, []string{"LIS"}},
		"local.factory.LighterPerpConfig":     {[2]bool{false, true}, []string{"LIP"}},
		"local.factory.HyperliquidSpotConfig": {[2]bool{true, false}, []string{"HLS"}},
		"local.factory.HyperliquidPerpConfig": {[2]bool{false, true}, []string{"HLP"}},
	}
	for id, constructor := range constructors {
		method := strings.TrimPrefix(id, "local.factory.")
		operations[id] = exchangeOperation{ID: id, Method: method, Transport: "local", Spot: constructor.support[0], Perp: constructor.support[1], Effect: "lifecycle", Credentials: "constructor credentials only", FundingRequirement: "none", ExpectedEvent: "typed config", Cleanup: "none", ExternalCells: constructor.cells, Tests: tests}
	}
	return operations
}

func operationsByID(t *testing.T, operations []exchangeOperation) map[string]exchangeOperation {
	t.Helper()
	byID := make(map[string]exchangeOperation, len(operations))
	for _, operation := range operations {
		if strings.TrimSpace(operation.ID) == "" {
			t.Fatal("operation has empty id")
		}
		if _, duplicate := byID[operation.ID]; duplicate {
			t.Fatalf("duplicate operation %s", operation.ID)
		}
		byID[operation.ID] = operation
	}
	return byID
}

func parameterCasesByID(t *testing.T, cases []exchangeParameterCase) map[string]exchangeParameterCase {
	t.Helper()
	byID := make(map[string]exchangeParameterCase, len(cases))
	for _, parameterCase := range cases {
		if strings.TrimSpace(parameterCase.ID) == "" {
			t.Fatal("parameter case has empty id")
		}
		if _, duplicate := byID[parameterCase.ID]; duplicate {
			t.Fatalf("duplicate parameter case %s", parameterCase.ID)
		}
		byID[parameterCase.ID] = parameterCase
	}
	return byID
}

func cellsForSupport(rows map[string]exchangeProductRow, spot, perp bool) []string {
	cells := make([]string, 0, len(rows))
	for code, row := range rows {
		isSpot := strings.Contains(row.Product, "Spot")
		if (isSpot && spot) || (!isSpot && perp) {
			cells = append(cells, code)
		}
	}
	sort.Strings(cells)
	return cells
}

func expectedRESTMethodsForProduct(product string) []string {
	methods := []string{"Balances", "CancelOrder", "Candles", "Fills", "Instruments", "OpenOrders", "OrderBook", "OrderHistory", "PlaceOrder", "PublicTrades"}
	if strings.Contains(product, "Spot") {
		return append(methods, "SpotAccount")
	}
	return append(methods, "FundingRate", "FundingRateHistory", "PerpAccount", "Positions", "SetLeverage")
}

func expectedWebSocketMethodsForProduct(product string) []string {
	methods := []string{"CancelOrder", "Close", "PlaceOrder", "WatchBBO", "WatchBalances", "WatchCandles", "WatchFills", "WatchOrderBook", "WatchOrders", "WatchPublicTrades"}
	if strings.Contains(product, "Spot") {
		return methods
	}
	return append(methods, "WatchFundingRate", "WatchMarkPrice", "WatchPositions")
}

func assertMethodSupport(t *testing.T, got []exchangeMethodSupport, want map[string][2]bool) {
	t.Helper()
	byMethod := make(map[string][2]bool, len(got))
	for _, method := range got {
		if _, duplicate := byMethod[method.Method]; duplicate {
			t.Fatalf("duplicate method support row %s", method.Method)
		}
		byMethod[method.Method] = [2]bool{method.Spot, method.Perp}
	}
	if !reflect.DeepEqual(byMethod, want) {
		t.Fatalf("method support = %#v, want %#v", byMethod, want)
	}
}

func assertSameStrings(t *testing.T, got, want []string) {
	t.Helper()
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("strings = %v, want %v", got, want)
	}
}

func mapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func stringSliceContainsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
