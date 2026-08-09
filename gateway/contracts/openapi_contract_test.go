package contracts

import (
	"os"
	"reflect"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

type openAPIDocument struct {
	OpenAPI    string                          `yaml:"openapi"`
	Security   []map[string][]string           `yaml:"security"`
	Paths      map[string]map[string]operation `yaml:"paths"`
	Components struct {
		SecuritySchemes map[string]securityScheme `yaml:"securitySchemes"`
		Schemas         map[string]schema         `yaml:"schemas"`
	} `yaml:"components"`
}

type operation struct {
	OperationID string `yaml:"operationId"`
}

type securityScheme struct {
	Type   string `yaml:"type"`
	Scheme string `yaml:"scheme"`
}

type schema struct {
	Required   []string       `yaml:"required"`
	Properties map[string]any `yaml:"properties"`
	Ref        string         `yaml:"$ref"`
	AllOf      []schema       `yaml:"allOf"`
}

func TestOpenAPIInventoryAndSecurityAreStructurallyFrozen(t *testing.T) {
	document := readOpenAPI(t)
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q, want 3.1.0", document.OpenAPI)
	}

	wantOperations := map[string]map[string]string{
		"/v1/health/live":                 {"get": "getLiveHealth"},
		"/v1/health/ready":                {"get": "getReadyHealth"},
		"/v1/accounts":                    {"get": "listAccounts"},
		"/v1/accounts/{account_id}":       {"get": "getAccount"},
		"/v1/capabilities":                {"get": "listCapabilities"},
		"/v1/instruments":                 {"get": "listInstruments"},
		"/v1/instruments/{instrument_id}": {"get": "getInstrument"},
		"/v1/rules":                       {"get": "listRules"},
		"/v1/risk-limits":                 {"get": "listRiskLimits"},
		"/v1/orders":                      {"get": "listOrders", "post": "placeOrder"},
		"/v1/orders/{gateway_order_id}":   {"delete": "cancelOrder", "get": "getOrder", "patch": "amendOrder"},
		"/v1/open-orders":                 {"get": "listOpenOrders"},
		"/v1/fills":                       {"get": "listFills"},
		"/v1/balances":                    {"get": "listBalances"},
		"/v1/positions":                   {"get": "listPositions"},
		"/v1/funding-payments":            {"get": "listFundingPayments"},
		"/v1/funding-rates":               {"get": "listFundingRates"},
		"/v1/funding-rates/history":       {"get": "listFundingRateHistory"},
		"/v1/order-book":                  {"get": "getOrderBook"},
		"/v1/bbo":                         {"get": "getBBO"},
		"/v1/mark-price":                  {"get": "getMarkPrice"},
		"/v1/public/ws":                   {"get": "streamPublicMarket"},
		"/v1/accounts/{account_id}/instruments/{instrument_id}/leverage": {"put": "setLeverage"},
		"/v1/ws": {"get": "streamPrivateEvents"},
	}
	if !reflect.DeepEqual(document.Paths, operationDocument(wantOperations)) {
		t.Fatalf("OpenAPI path/method/operationId inventory changed\n got: %v\nwant: %v", flattenOperations(document.Paths), flattenExpected(wantOperations))
	}

	seenOperationIDs := make(map[string]string)
	for path, methods := range document.Paths {
		for method, operation := range methods {
			if operation.OperationID == "" {
				t.Errorf("%s %s has no operationId", method, path)
				continue
			}
			key := method + " " + path
			if prior, duplicate := seenOperationIDs[operation.OperationID]; duplicate {
				t.Errorf("operationId %q reused by %s and %s", operation.OperationID, prior, key)
			}
			seenOperationIDs[operation.OperationID] = key
		}
	}

	if len(document.Security) != 1 || len(document.Security[0]) != 1 {
		t.Fatalf("root security = %#v, want one bearerAuth requirement", document.Security)
	}
	if scopes, ok := document.Security[0]["bearerAuth"]; !ok || len(scopes) != 0 {
		t.Fatalf("root bearerAuth security = %#v, want empty scopes", document.Security[0])
	}
	scheme, ok := document.Components.SecuritySchemes["bearerAuth"]
	if !ok || scheme.Type != "http" || scheme.Scheme != "bearer" {
		t.Fatalf("bearerAuth scheme = %#v, want HTTP bearer", scheme)
	}
	if len(document.Components.SecuritySchemes) != 1 {
		t.Fatalf("security schemes = %v, want only bearerAuth", document.Components.SecuritySchemes)
	}
}

func TestOpenAPISchemaInventoryAndRequiredFieldsAreFrozen(t *testing.T) {
	document := readOpenAPI(t)
	want := map[string]struct {
		required   []string
		properties []string
	}{
		"PlaceOrderRequest": {
			required:   []string{"account_id", "credential_version", "desired_state_revision", "idempotency_key", "instrument_id", "quantity", "route_id", "runtime_generation", "side", "type"},
			properties: []string{"account_id", "credential_version", "desired_state_revision", "idempotency_key", "instrument_id", "limit_policy", "limit_price", "quantity", "reduce_only", "route_id", "runtime_generation", "side", "type"},
		},
		"OrderResponse": {
			required:   []string{"client_order_id", "command_id", "gateway_order_id", "resource_version", "state"},
			properties: []string{"client_order_id", "command_id", "gateway_order_id", "resource_version", "state"},
		},
		"ErrorEnvelope": {
			required:   []string{"error"},
			properties: []string{"error"},
		},
		"IdempotencyConflict": {
			required:   []string{"error", "existing_command_id"},
			properties: []string{"error", "existing_command_id"},
		},
		"PrivateEvent": {
			required:   []string{"account_id", "boot_id", "event_id", "kind", "occurred_at", "payload", "resource_version", "sequence"},
			properties: []string{"account_id", "boot_id", "event_id", "kind", "occurred_at", "payload", "resource_version", "sequence"},
		},
		"PublicMarketEvent": {
			required:   []string{"account_id", "boot_id", "event_id", "event_type", "instrument_id", "kind", "occurred_at", "payload", "sequence"},
			properties: []string{"account_id", "boot_id", "event_id", "event_type", "instrument_id", "kind", "occurred_at", "payload", "sequence"},
		},
		"BookLevel": {
			required:   []string{"price", "quantity"},
			properties: []string{"price", "quantity"},
		},
		"PageInfo": {
			required:   []string{"has_more", "has_more_known"},
			properties: []string{"cursor", "has_more", "has_more_known", "limit", "window_end", "window_start"},
		},
		"OptionalDecimal": {
			required:   []string{"valid", "value"},
			properties: []string{"valid", "value"},
		},
		"FundingRateProjection": {
			required:   []string{"mark_price", "rate"},
			properties: []string{"funding_time", "mark_price", "next_funding_time", "observed_at", "rate"},
		},
		"FundingRateResponse": {
			required:   []string{"account_id", "funding_rate", "instrument_id"},
			properties: []string{"account_id", "funding_rate", "instrument_id"},
		},
		"OrderBookProjection": {
			required:   []string{"account_id", "asks", "bids", "instrument_id", "observed_at", "page"},
			properties: []string{"account_id", "asks", "bids", "instrument_id", "observed_at", "page", "sequence"},
		},
		"BBOProjection": {
			required:   []string{"account_id", "ask", "bid", "instrument_id", "observed_at"},
			properties: []string{"account_id", "ask", "bid", "instrument_id", "observed_at"},
		},
		"MarkPriceProjection": {
			required:   []string{"account_id", "instrument_id", "observed_at", "price"},
			properties: []string{"account_id", "instrument_id", "observed_at", "price"},
		},
		"FundingHistoryProjection": {
			required:   []string{"account_id", "instrument_id", "items"},
			properties: []string{"account_id", "instrument_id", "items", "next_cursor"},
		},
		"CapabilityOperations": {
			required:   nil,
			properties: []string{"account", "account_funding_payments", "amend", "balances", "cancel", "fills", "funding", "funds_transfer", "leverage", "margin_mode", "open_orders", "order_history", "place", "position_mode", "positions", "private_stream"},
		},
		"FundingPayment": {
			required:   []string{"account_id", "amount", "fact_id", "instrument_id", "product", "settlement_asset", "venue", "venue_environment"},
			properties: []string{"account_id", "amount", "effective_at", "fact_id", "formula_version", "instrument_id", "observed_at", "payment_id", "product", "revision", "settlement_asset", "source_ref", "supersedes_fact_id", "venue", "venue_environment", "version"},
		},
		"FundingPaymentScope": {
			required:   []string{"account_id", "all_instruments", "end", "start"},
			properties: []string{"account_id", "all_instruments", "end", "instrument_id", "settlement_asset", "start"},
		},
		"FundingPaymentTraversal": {
			required:   []string{"has_more", "has_more_known"},
			properties: []string{"cursor", "has_more", "has_more_known", "limit"},
		},
		"FundingPaymentWatermark": {
			required:   []string{"observed_at", "revision", "verified"},
			properties: []string{"complete_through", "cursor", "effective_through", "observed_at", "revision", "verified"},
		},
		"FundingPaymentPage": {
			required:   []string{"account_id", "as_of", "end", "items", "page", "pagination_traversal", "scope", "start", "status", "watermark"},
			properties: []string{"account_id", "as_of", "complete_through", "end", "instrument_id", "items", "next_cursor", "page", "pagination_traversal", "revision", "scope", "settlement_asset", "start", "status", "watermark"},
		},
		"ResyncRequiredEvent": {
			required:   nil,
			properties: nil,
		},
	}
	if len(document.Components.Schemas) != len(want) {
		t.Fatalf("schema count = %d, want %d", len(document.Components.Schemas), len(want))
	}
	for name, expected := range want {
		got, ok := document.Components.Schemas[name]
		if !ok {
			t.Errorf("missing schema %s", name)
			continue
		}
		sort.Strings(got.Required)
		properties := make([]string, 0, len(got.Properties))
		for property := range got.Properties {
			properties = append(properties, property)
		}
		sort.Strings(properties)
		if !sameStrings(got.Required, expected.required) {
			t.Errorf("schema %s required = %v, want %v", name, got.Required, expected.required)
		}
		if !sameStrings(properties, expected.properties) {
			t.Errorf("schema %s properties = %v, want %v", name, properties, expected.properties)
		}
	}
	placeOrder := document.Components.Schemas["PlaceOrderRequest"]
	for _, name := range []string{"desired_state_revision", "runtime_generation", "credential_version"} {
		property, ok := placeOrder.Properties[name].(map[string]any)
		if !ok || property["type"] != "integer" || property["minimum"] != 1 {
			t.Errorf("PlaceOrderRequest property %s = %#v, want positive integer", name, placeOrder.Properties[name])
		}
	}
	resync := document.Components.Schemas["ResyncRequiredEvent"]
	if len(resync.AllOf) != 2 || resync.AllOf[0].Ref != "#/components/schemas/PrivateEvent" {
		t.Fatalf("ResyncRequiredEvent allOf = %#v, want PrivateEvent plus resync payload", resync.AllOf)
	}
	if len(resync.AllOf[1].Required) != 0 {
		t.Fatalf("ResyncRequiredEvent required = %v", resync.AllOf[1].Required)
	}
	if _, kindOK := resync.AllOf[1].Properties["kind"]; !kindOK {
		t.Fatalf("ResyncRequiredEvent properties = %v", resync.AllOf[1].Properties)
	}
	if _, payloadOK := resync.AllOf[1].Properties["payload"]; !payloadOK || len(resync.AllOf[1].Properties) != 2 {
		t.Fatalf("ResyncRequiredEvent properties = %v", resync.AllOf[1].Properties)
	}
}

func readOpenAPI(t *testing.T) openAPIDocument {
	t.Helper()
	data, err := os.ReadFile("../../api/exchange-gateway/v1/openapi.yaml")
	if err != nil {
		t.Fatalf("read OpenAPI: %v", err)
	}
	var document openAPIDocument
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse OpenAPI YAML: %v", err)
	}
	return document
}

func operationDocument(input map[string]map[string]string) map[string]map[string]operation {
	result := make(map[string]map[string]operation, len(input))
	for path, methods := range input {
		result[path] = make(map[string]operation, len(methods))
		for method, operationID := range methods {
			result[path][method] = operation{OperationID: operationID}
		}
	}
	return result
}

func flattenOperations(input map[string]map[string]operation) []string {
	result := make([]string, 0)
	for path, methods := range input {
		for method, operation := range methods {
			result = append(result, method+" "+path+"="+operation.OperationID)
		}
	}
	sort.Strings(result)
	return result
}

func flattenExpected(input map[string]map[string]string) []string {
	return flattenOperations(operationDocument(input))
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
