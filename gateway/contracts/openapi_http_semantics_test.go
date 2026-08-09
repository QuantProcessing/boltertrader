package contracts

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type transportDocument struct {
	Paths      map[string]map[string]transportOperation `yaml:"paths"`
	Components struct {
		Parameters map[string]transportParameter `yaml:"parameters"`
	} `yaml:"components"`
}

type transportOperation struct {
	Parameters  []transportParameter      `yaml:"parameters"`
	RequestBody transportRequestBody      `yaml:"requestBody"`
	Responses   map[string]map[string]any `yaml:"responses"`
}

type transportParameter struct {
	Name     string `yaml:"name"`
	In       string `yaml:"in"`
	Required bool   `yaml:"required"`
	Ref      string `yaml:"$ref"`
}

type transportRequestBody struct {
	Required bool                      `yaml:"required"`
	Content  map[string]map[string]any `yaml:"content"`
}

func TestOpenAPIDocumentsImplementedFiltersPaginationBodiesAndStatuses(t *testing.T) {
	data, err := os.ReadFile("../../api/exchange-gateway/v1/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document transportDocument
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}

	wantParameters := map[string][]string{
		"get /v1/accounts":                     {"query:pagination.limit"},
		"get /v1/accounts/{account_id}":        {"path:account_id!"},
		"get /v1/capabilities":                 {"query:account_id"},
		"get /v1/instruments":                  {"query:account_id", "query:pagination.limit", "query:product", "query:venue"},
		"get /v1/instruments/{instrument_id}":  {"path:instrument_id!"},
		"get /v1/rules":                        {"query:account_id"},
		"get /v1/risk-limits":                  {"query:account_id"},
		"post /v1/orders":                      {"header:Idempotency-Key!"},
		"get /v1/orders":                       {"query:account_id", "query:instrument_id", "query:pagination.cursor", "query:pagination.limit"},
		"get /v1/orders/{gateway_order_id}":    {"path:gateway_order_id!"},
		"patch /v1/orders/{gateway_order_id}":  {"path:gateway_order_id!"},
		"delete /v1/orders/{gateway_order_id}": {"path:gateway_order_id!"},
		"get /v1/open-orders":                  {"query:account_id", "query:instrument_id", "query:pagination.cursor", "query:pagination.limit"},
		"get /v1/fills":                        {"query:account_id", "query:gateway_order_id", "query:instrument_id", "query:pagination.cursor", "query:pagination.limit"},
		"get /v1/balances":                     {"query:account_id", "query:pagination.limit"},
		"get /v1/positions":                    {"query:account_id", "query:instrument_id", "query:pagination.limit"},
		"get /v1/funding-payments":             {"query:account_id!", "query:end!", "query:instrument_id", "query:pagination.cursor", "query:pagination.limit", "query:settlement_asset", "query:start!"},
		"get /v1/funding-rates":                {"query:account_id!", "query:instrument_id!"},
		"get /v1/funding-rates/history":        {"query:account_id!", "query:end", "query:instrument_id!", "query:pagination.cursor", "query:pagination.limit", "query:start"},
		"get /v1/order-book":                   {"query:account_id!", "query:instrument_id!", "query:limit"},
		"get /v1/bbo":                          {"query:account_id!", "query:instrument_id!"},
		"get /v1/mark-price":                   {"query:account_id!", "query:instrument_id!"},
		"get /v1/public/ws":                    {"query:account_id!", "query:instrument_id!", "query:kind!"},
		"put /v1/accounts/{account_id}/instruments/{instrument_id}/leverage": {"path:account_id!", "path:instrument_id!"},
		"get /v1/ws": {"query:account_id!", "query:cursor", "query:kind"},
	}
	for key, want := range wantParameters {
		method, path, _ := strings.Cut(key, " ")
		operation := document.Paths[path][method]
		got := parameterNames(operation.Parameters, document.Components.Parameters)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s parameters=%v want=%v", key, got, want)
		}
	}

	place := document.Paths["/v1/orders"]["post"]
	if !place.RequestBody.Required || place.RequestBody.Content["application/json"] == nil {
		t.Error("POST /v1/orders must require application/json body")
	}
	leverage := document.Paths["/v1/accounts/{account_id}/instruments/{instrument_id}/leverage"]["put"]
	if !leverage.RequestBody.Required || leverage.RequestBody.Content["application/json"] == nil {
		t.Error("PUT leverage must require application/json body")
	}

	wantStatuses := map[string][]string{
		"post /v1/orders":                                                    {"202", "409", "default"},
		"patch /v1/orders/{gateway_order_id}":                                {"501", "default"},
		"delete /v1/orders/{gateway_order_id}":                               {"202", "default"},
		"put /v1/accounts/{account_id}/instruments/{instrument_id}/leverage": {"501", "default"},
	}
	for key, want := range wantStatuses {
		method, path, _ := strings.Cut(key, " ")
		got := make([]string, 0, len(document.Paths[path][method].Responses))
		for status := range document.Paths[path][method].Responses {
			got = append(got, status)
		}
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s statuses=%v want=%v", key, got, want)
		}
	}
}

func TestOpenAPIPrivateEventDocumentsEnvelopeAndClosedKinds(t *testing.T) {
	data, err := os.ReadFile("../../api/exchange-gateway/v1/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	privateEvent := schemas["PrivateEvent"].(map[string]any)
	required := stringSlice(privateEvent["required"])
	wantRequired := []string{
		"account_id", "boot_id", "event_id", "kind", "occurred_at",
		"payload", "resource_version", "sequence",
	}
	sort.Strings(wantRequired)
	if !reflect.DeepEqual(required, wantRequired) {
		t.Fatalf("PrivateEvent required=%v want=%v", required, wantRequired)
	}
	properties := privateEvent["properties"].(map[string]any)
	kind := properties["kind"].(map[string]any)
	gotKinds := stringSlice(kind["enum"])
	wantKinds := []string{"account_health", "balance", "fill", "order", "position", "resync_required"}
	sort.Strings(wantKinds)
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("PrivateEvent kinds=%v want=%v", gotKinds, wantKinds)
	}
	payload := properties["payload"].(map[string]any)
	if payload["type"] != "object" {
		t.Fatalf("PrivateEvent payload=%v", payload)
	}
}

func TestOpenAPIPublicMarketContractIsClosedAndResnapshotBased(t *testing.T) {
	data, err := os.ReadFile("../../api/exchange-gateway/v1/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	paths := document["paths"].(map[string]any)
	wantResponseSchemas := map[string]string{
		"/v1/funding-payments":      "#/components/schemas/FundingPaymentPage",
		"/v1/funding-rates":         "#/components/schemas/FundingRateResponse",
		"/v1/funding-rates/history": "#/components/schemas/FundingHistoryProjection",
		"/v1/order-book":            "#/components/schemas/OrderBookProjection",
		"/v1/bbo":                   "#/components/schemas/BBOProjection",
		"/v1/mark-price":            "#/components/schemas/MarkPriceProjection",
	}
	for path, wantRef := range wantResponseSchemas {
		operation := paths[path].(map[string]any)["get"].(map[string]any)
		responses := operation["responses"].(map[string]any)
		success := responses["200"].(map[string]any)
		content := success["content"].(map[string]any)
		media := content["application/json"].(map[string]any)
		schema := media["schema"].(map[string]any)
		if schema["$ref"] != wantRef {
			t.Errorf("%s response schema=%v want=%s", path, schema, wantRef)
		}
	}

	publicWS := paths["/v1/public/ws"].(map[string]any)["get"].(map[string]any)
	responses := publicWS["responses"].(map[string]any)
	switching := responses["101"].(map[string]any)
	messageSchema := switching["x-websocket-message-schema"].(map[string]any)
	if messageSchema["$ref"] != "#/components/schemas/PublicMarketEvent" {
		t.Fatalf("public ws message schema=%v", messageSchema)
	}
	description := strings.ToLower(switching["description"].(string))
	for _, phrase := range []string{"snapshot", "no cursor replay", "resync_required", "policy violation"} {
		if !strings.Contains(description, phrase) {
			t.Errorf("public ws description must contain %q: %s", phrase, description)
		}
	}

	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	publicEvent := schemas["PublicMarketEvent"].(map[string]any)
	properties := publicEvent["properties"].(map[string]any)
	kind := properties["kind"].(map[string]any)
	gotKinds := stringSlice(kind["enum"])
	wantKinds := []string{"bbo", "funding_rate", "mark_price", "order_book"}
	sort.Strings(wantKinds)
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("PublicMarketEvent kinds=%v want=%v", gotKinds, wantKinds)
	}
	eventType := properties["event_type"].(map[string]any)
	gotEventTypes := stringSlice(eventType["enum"])
	wantEventTypes := []string{"resync_required", "snapshot", "update"}
	sort.Strings(wantEventTypes)
	if !reflect.DeepEqual(gotEventTypes, wantEventTypes) {
		t.Fatalf("PublicMarketEvent event types=%v want=%v", gotEventTypes, wantEventTypes)
	}
	payload := properties["payload"].(map[string]any)
	if payload["type"] != "object" {
		t.Fatalf("PublicMarketEvent payload=%v", payload)
	}
}

func TestOpenAPIErrorEnvelopeDocumentsCorrelationAndRetryability(t *testing.T) {
	data, err := os.ReadFile("../../api/exchange-gateway/v1/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	envelope := schemas["ErrorEnvelope"].(map[string]any)
	envelopeProperties := envelope["properties"].(map[string]any)
	errorProperty := envelopeProperties["error"].(map[string]any)

	required := stringSlice(errorProperty["required"])
	wantRequired := []string{"code", "correlation_id", "message", "retryable"}
	sort.Strings(wantRequired)
	if !reflect.DeepEqual(required, wantRequired) {
		t.Fatalf("ErrorEnvelope.error required=%v want=%v", required, wantRequired)
	}
	properties := errorProperty["properties"].(map[string]any)
	gotProperties := make([]string, 0, len(properties))
	for property := range properties {
		gotProperties = append(gotProperties, property)
	}
	sort.Strings(gotProperties)
	if !reflect.DeepEqual(gotProperties, wantRequired) {
		t.Fatalf("ErrorEnvelope.error properties=%v want=%v", gotProperties, wantRequired)
	}

	conflict := schemas["IdempotencyConflict"].(map[string]any)
	conflictProperties := conflict["properties"].(map[string]any)
	conflictError := conflictProperties["error"].(map[string]any)
	if ref, ok := conflictError["$ref"]; ok {
		t.Fatalf("IdempotencyConflict.error must be the error object, not nested ErrorEnvelope: ref=%v", ref)
	}
	conflictRequired := stringSlice(conflictError["required"])
	if !reflect.DeepEqual(conflictRequired, wantRequired) {
		t.Fatalf("IdempotencyConflict.error required=%v want=%v", conflictRequired, wantRequired)
	}
	conflictErrorProperties := conflictError["properties"].(map[string]any)
	gotConflictProperties := make([]string, 0, len(conflictErrorProperties))
	for property := range conflictErrorProperties {
		gotConflictProperties = append(gotConflictProperties, property)
	}
	sort.Strings(gotConflictProperties)
	if !reflect.DeepEqual(gotConflictProperties, wantRequired) {
		t.Fatalf("IdempotencyConflict.error properties=%v want=%v", gotConflictProperties, wantRequired)
	}
}

func TestOpenAPIEnumeratesRouteRuntimeStableErrors(t *testing.T) {
	data, err := os.ReadFile("../../api/exchange-gateway/v1/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	envelope := schemas["ErrorEnvelope"].(map[string]any)
	errorObject := envelope["properties"].(map[string]any)["error"].(map[string]any)
	code := errorObject["properties"].(map[string]any)["code"].(map[string]any)
	got := stringSlice(code["enum"])
	sort.Strings(got)
	want := []string{
		"CANDIDATE_NOT_READY", "HANDOFF_DRAIN_BLOCKED", "LIVE_WRITE_DISABLED", "OPERATION_ID_CONFLICT",
		"PRIVATE_STREAM_GAP", "PROMOTION_FENCE_CONFLICT", "RETIRED_SECRET_REFERENCE", "ROUTE_PROFILE_CONFLICT",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stable error codes=%v want=%v", got, want)
	}
}

func stringSlice(value any) []string {
	values, _ := value.([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.(string))
	}
	sort.Strings(result)
	return result
}

func parameterNames(parameters []transportParameter, shared map[string]transportParameter) []string {
	result := make([]string, 0, len(parameters))
	for _, parameter := range parameters {
		if parameter.Ref != "" {
			parameter = shared[strings.TrimPrefix(parameter.Ref, "#/components/parameters/")]
		}
		name := parameter.In + ":" + parameter.Name
		if parameter.Required {
			name += "!"
		}
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
