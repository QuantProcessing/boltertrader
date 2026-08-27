package contracts

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	gatewayhttp "github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type internalSchema struct {
	Ref                  string                    `yaml:"$ref"`
	Type                 any                       `yaml:"type"`
	Format               string                    `yaml:"format"`
	Enum                 []string                  `yaml:"enum"`
	Minimum              *float64                  `yaml:"minimum"`
	AdditionalProperties any                       `yaml:"additionalProperties"`
	Required             []string                  `yaml:"required"`
	Properties           map[string]internalSchema `yaml:"properties"`
	Items                *internalSchema           `yaml:"items"`
	AllOf                []internalSchema          `yaml:"allOf"`
	If                   *internalSchema           `yaml:"if"`
	Then                 *internalSchema           `yaml:"then"`
}

func TestPendingSnapshotSerializationStrictlyMatchesInternalOpenAPI(t *testing.T) {
	schemas := loadInternalSchemas(t)
	view := gatewayhttp.RuntimeView{
		AccountID: "account-a",
		Pending: &gatewayhttp.PendingSnapshotView{
			RuntimeSnapshotView: gatewayhttp.RuntimeSnapshotView{
				OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33",
				RuntimeID:   "018f47a8-2578-7d3c-9d7d-5e4f4a445c34", RequestedState: "creating",
				Routes: []gatewayhttp.RuntimeRouteView{{
					RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT",
					RouteProfileID: "binance.perp.usdt.v1", RequestedState: "active", Ready: true,
				}},
			},
			State: "candidate_ready",
		},
		Retiring:       []gatewayhttp.RuntimeSnapshotView{},
		Tombstoned:     false,
		Readiness:      gatewayhttp.RuntimeProjectionReadiness{Ready: false, Reason: "candidate_not_ready"},
		WriteReadiness: gatewayhttp.RuntimeProjectionReadiness{Ready: false, Reason: "candidate_not_ready"},
		Capabilities:   []gatewayhttp.RuntimeCapabilityView{},
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var serialized map[string]any
	if err := json.Unmarshal(encoded, &serialized); err != nil {
		t.Fatal(err)
	}
	if err := validateInternalSchema(serialized["pending"], schemas["PendingSnapshotView"], schemas, "PendingSnapshotView"); err != nil {
		t.Fatalf("actual pending serialization violates internal OpenAPI: %v; json=%s", err, encoded)
	}
}

func loadInternalSchemas(t *testing.T) map[string]internalSchema {
	t.Helper()
	data, err := os.ReadFile("../../api/exchange-gateway/v1/internal-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Components struct {
			Schemas map[string]internalSchema `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document.Components.Schemas
}

func TestInternalMarketWebSocketDeclaresMultiplexedResnapshotProtocol(t *testing.T) {
	data, err := os.ReadFile("../../api/exchange-gateway/v1/internal-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Description string `yaml:"description"`
				Content     map[string]struct {
					Schema struct {
						Ref string `yaml:"$ref"`
					} `yaml:"schema"`
				} `yaml:"content"`
			} `yaml:"responses"`
		} `yaml:"paths"`
		Components struct {
			Schemas map[string]internalSchema `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	switching := document.Paths["/internal/v1/market/ws"]["get"].Responses["101"]
	description := strings.ToLower(switching.Description)
	for _, phrase := range []string{"multiplexed", "websocket-derived snapshot", "native deltas", "full replacements", "no rest book", "resync_required", "new generation"} {
		if !strings.Contains(description, phrase) {
			t.Errorf("market ws description must contain %q: %s", phrase, description)
		}
	}
	if ref := switching.Content["application/json"].Schema.Ref; ref != "#/components/schemas/RealtimeMessage" {
		t.Fatalf("market ws message schema=%q", ref)
	}
	event := document.Components.Schemas["RealtimeMessage"]
	if !reflect.DeepEqual(event.Required, []string{"type"}) {
		t.Fatalf("RealtimeMessage required=%v", event.Required)
	}
	if got, want := event.Properties["type"].Enum, []string{"ack", "snapshot", "delta", "full", "checkpoint", "resync_required", "error", "pong"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RealtimeMessage event types=%v want=%v", got, want)
	}
}

func validateInternalSchema(value any, schema internalSchema, schemas map[string]internalSchema, path string) error {
	if schema.Ref != "" {
		const prefix = "#/components/schemas/"
		if !strings.HasPrefix(schema.Ref, prefix) {
			return fmt.Errorf("%s has unsupported ref %q", path, schema.Ref)
		}
		name := strings.TrimPrefix(schema.Ref, prefix)
		resolved, ok := schemas[name]
		if !ok {
			return fmt.Errorf("%s references missing schema %q", path, name)
		}
		return validateInternalSchema(value, resolved, schemas, path)
	}
	switch schema.Type {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		for _, required := range schema.Required {
			if _, exists := object[required]; !exists {
				return fmt.Errorf("%s is missing required field %q", path, required)
			}
		}
		for name, child := range object {
			property, exists := schema.Properties[name]
			if !exists {
				if schema.AdditionalProperties == false {
					return fmt.Errorf("%s has unknown field %q", path, name)
				}
				continue
			}
			if err := validateInternalSchema(child, property, schemas, path+"."+name); err != nil {
				return err
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok || schema.Items == nil {
			return fmt.Errorf("%s must be an array with an item schema", path)
		}
		for index, item := range items {
			if err := validateInternalSchema(item, *schema.Items, schemas, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		if len(schema.Enum) > 0 && !containsString(schema.Enum, text) {
			return fmt.Errorf("%s has value %q outside enum %v", path, text, schema.Enum)
		}
		if schema.Format == "uuid" {
			if _, err := uuid.Parse(text); err != nil {
				return fmt.Errorf("%s must be a UUID", path)
			}
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || math.Trunc(number) != number || (schema.Minimum != nil && number < *schema.Minimum) {
			return fmt.Errorf("%s must be an integer satisfying its minimum", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	default:
		return fmt.Errorf("%s has unsupported schema type %v", path, schema.Type)
	}
	return nil
}

type internalResponse struct {
	Content map[string]struct {
		Schema internalSchema `yaml:"schema"`
	} `yaml:"content"`
	Headers map[string]struct {
		Ref string `yaml:"$ref"`
	} `yaml:"headers"`
}

type internalRequestBody struct {
	Required bool `yaml:"required"`
	Content  map[string]struct {
		Schema internalSchema `yaml:"schema"`
	} `yaml:"content"`
}

type internalParameter struct {
	Ref      string         `yaml:"$ref"`
	Name     string         `yaml:"name"`
	In       string         `yaml:"in"`
	Required bool           `yaml:"required"`
	Schema   internalSchema `yaml:"schema"`
}

func TestInternalAuthorityOpenAPIIsCompleteStrictAndSeparatelyAuthenticated(t *testing.T) {
	data, err := os.ReadFile("../../api/exchange-gateway/v1/internal-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		OpenAPI  string                `yaml:"openapi"`
		Security []map[string][]string `yaml:"security"`
		Paths    map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
			RequestBody struct {
				Ref string `yaml:"$ref"`
			} `yaml:"requestBody"`
			Responses map[string]struct {
				Ref string `yaml:"$ref"`
			} `yaml:"responses"`
			Parameters []internalParameter `yaml:"parameters"`
		} `yaml:"paths"`
		Components struct {
			Responses     map[string]internalResponse    `yaml:"responses"`
			Parameters    map[string]internalParameter   `yaml:"parameters"`
			RequestBodies map[string]internalRequestBody `yaml:"requestBodies"`
			Headers       map[string]struct {
				Schema internalSchema `yaml:"schema"`
			} `yaml:"headers"`
			SecuritySchemes map[string]struct {
				Type   string `yaml:"type"`
				Scheme string `yaml:"scheme"`
			} `yaml:"securitySchemes"`
			Schemas map[string]internalSchema `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.OpenAPI != "3.1.0" || !reflect.DeepEqual(document.Security, []map[string][]string{{"gatewayAdapterAuth": {}}}) {
		t.Fatalf("internal OpenAPI/security=%q %#v", document.OpenAPI, document.Security)
	}
	scheme := document.Components.SecuritySchemes["gatewayAdapterAuth"]
	if len(document.Components.SecuritySchemes) != 1 || scheme.Type != "http" || scheme.Scheme != "bearer" {
		t.Fatalf("internal security schemes=%#v", document.Components.SecuritySchemes)
	}
	want := map[string][]string{
		"/internal/v1/runtime/status": {"get"}, "/internal/v1/runtime/readiness": {"get"},
		"/internal/v1/runtime/open-orders": {"get"}, "/internal/v1/runtime/positions": {"get"},
		"/internal/v1/runtime/uncertain-commands":                             {"get"},
		"/internal/v1/accounts/desired":                                       {"get"},
		"/internal/v1/accounts/desired/{accountId}":                           {"get"},
		"/internal/v1/accounts/desired/{accountId}/operations/{operationId}":  {"put"},
		"/internal/v1/accounts/desired/{accountId}/retirements/{operationId}": {"put"},
		"/internal/v1/accounts/runtime":                                       {"get"},
		"/internal/v1/accounts/runtime/{accountId}":                           {"get"},
		"/internal/v1/accounts/runtime/{accountId}/events":                    {"get"},
		"/internal/v1/catalog/venues":                                         {"get"}, "/internal/v1/catalog/instruments": {"get"},
		"/internal/v1/browser/catalog/venues":      {"get"},
		"/internal/v1/browser/catalog/instruments": {"get"},
		"/internal/v1/browser/market-data":         {"get"},
		"/internal/v1/browser/candles":             {"get"},
		"/internal/v1/browser/funding-history":     {"get"},
		"/internal/v1/market-data":                 {"get"}, "/internal/v1/candles": {"get"},
		"/internal/v1/market/ws":           {"get"},
		"/internal/v1/account/ws":          {"get"},
		"/internal/v1/market-facts":        {"get"},
		"/internal/v1/fresh-bbo":           {"get"},
		"/internal/v1/private-events":      {"get"},
		"/internal/v1/funding-board":       {"get"},
		"/internal/v1/funding-history":     {"get"},
		"/internal/v1/venues/connectivity": {"get"},
	}
	if len(document.Paths) != len(want) {
		t.Fatalf("internal paths=%d want=%d", len(document.Paths), len(want))
	}
	for path, methods := range want {
		got := make([]string, 0, len(document.Paths[path]))
		for method, operation := range document.Paths[path] {
			got = append(got, method)
			if operation.OperationID == "" {
				t.Errorf("%s %s has no operationId", method, path)
			}
		}
		sort.Strings(got)
		sort.Strings(methods)
		if !reflect.DeepEqual(got, methods) {
			t.Errorf("%s methods=%v want=%v", path, got, methods)
		}
	}
	for path, responseName := range map[string]string{
		"/internal/v1/runtime/status":    "RuntimeStatus",
		"/internal/v1/runtime/readiness": "RuntimeReadiness",
	} {
		got := document.Paths[path]["get"].Responses["200"].Ref
		wantRef := "#/components/responses/" + responseName
		if got != wantRef {
			t.Errorf("%s response ref=%q want=%q", path, got, wantRef)
		}
		envelope := document.Components.Responses[responseName].Content["application/json"].Schema
		if envelope.Type != "object" || envelope.AdditionalProperties != false || !reflect.DeepEqual(envelope.Required, []string{"data"}) || len(envelope.Properties) != 1 {
			t.Errorf("%s response envelope=%+v", responseName, envelope)
		}
		if got := envelope.Properties["data"].Ref; got != "#/components/schemas/"+responseName {
			t.Errorf("%s response data schema ref=%q", responseName, got)
		}
	}
	for _, success := range []struct {
		path   string
		method string
		status string
	}{
		{"/internal/v1/accounts/desired/{accountId}", "get", "200"},
		{"/internal/v1/accounts/desired/{accountId}/operations/{operationId}", "put", "200"},
		{"/internal/v1/accounts/desired/{accountId}/operations/{operationId}", "put", "202"},
		{"/internal/v1/accounts/desired/{accountId}/retirements/{operationId}", "put", "200"},
		{"/internal/v1/accounts/desired/{accountId}/retirements/{operationId}", "put", "202"},
	} {
		got := document.Paths[success.path][success.method].Responses[success.status].Ref
		wantResponse := "RuntimeView"
		if got != "#/components/responses/"+wantResponse {
			t.Errorf("%s %s response %s ref=%q want %s component response", success.method, success.path, success.status, got, wantResponse)
		}
	}
	runtimeEnvelope := document.Components.Responses["RuntimeView"].Content["application/json"].Schema
	if runtimeEnvelope.Type != "object" || runtimeEnvelope.AdditionalProperties != false || !reflect.DeepEqual(runtimeEnvelope.Required, []string{"data"}) || len(runtimeEnvelope.Properties) != 1 {
		t.Errorf("RuntimeView response envelope=%+v", runtimeEnvelope)
	}
	if got := runtimeEnvelope.Properties["data"].Ref; got != "#/components/schemas/RuntimeView" {
		t.Errorf("RuntimeView response data schema ref=%q", got)
	}
	for _, name := range []string{"DesiredApplyRequest", "RetireRequest", "DesiredRuntimeView", "RuntimeView", "WriteFence", "InternalErrorEnvelope"} {
		schema, ok := document.Components.Schemas[name]
		if !ok {
			t.Errorf("missing internal schema %s", name)
			continue
		}
		if schema.AdditionalProperties != false {
			t.Errorf("schema %s must reject unknown fields", name)
		}
	}
	for _, path := range []string{
		"/internal/v1/accounts/desired/{accountId}/operations/{operationId}",
		"/internal/v1/accounts/desired/{accountId}/retirements/{operationId}",
	} {
		operation := document.Paths[path]["put"]
		refs := make([]string, 0, len(operation.Parameters))
		for _, parameter := range operation.Parameters {
			refs = append(refs, parameter.Ref)
		}
		sort.Strings(refs)
		wantRefs := []string{"#/components/parameters/AccountID", "#/components/parameters/OperationID"}
		if !reflect.DeepEqual(refs, wantRefs) {
			t.Errorf("%s parameter refs=%v want=%v", path, refs, wantRefs)
		}
	}
	// Every write used to carry an X-Bolter-Mode attestation, declared as a
	// required header parameter and echoed back on every response, so that a
	// caller and the gateway could not disagree about which mode they were in.
	// There is one mode now and the header is gone; what this asserts is that it
	// stays gone, because a document that quietly reacquired it would describe a
	// header no process on either side of the boundary sets or reads.
	if _, declared := document.Components.Parameters["ModeAttestation"]; declared || len(document.Components.Headers) != 0 {
		t.Errorf("mode attestation parameter/header components=%v %v", declared, document.Components.Headers)
	}
	for responseName, response := range document.Components.Responses {
		if len(response.Headers) != 0 {
			t.Errorf("%s declares response headers=%v", responseName, response.Headers)
		}
	}
	for name, required := range map[string][]string{
		"RuntimeStatus":    {"accounts", "mode"},
		"RuntimeReadiness": {"ready"},
	} {
		schema, ok := document.Components.Schemas[name]
		if !ok {
			t.Errorf("missing internal schema %s", name)
			continue
		}
		if schema.AdditionalProperties != false {
			t.Errorf("schema %s must reject unknown fields", name)
		}
		sort.Strings(schema.Required)
		if !reflect.DeepEqual(schema.Required, required) {
			t.Errorf("%s required=%v want=%v", name, schema.Required, required)
		}
		if len(schema.Properties) != len(required) {
			t.Errorf("%s properties=%v want only %v", name, schema.Properties, required)
		}
	}
	desired := document.Components.Schemas["DesiredApplyRequest"]
	wantDesired := []string{"accountId", "nativeEnvironment", "operationId", "requestedState", "routes", "runtimeId", "secretRef", "venue"}
	sort.Strings(desired.Required)
	if !reflect.DeepEqual(desired.Required, wantDesired) {
		t.Fatalf("DesiredApplyRequest required=%v want=%v", desired.Required, wantDesired)
	}
	wantStates := []string{"active", "creating", "disabled", "removed", "removing"}
	gotStates := desired.Properties["requestedState"].Enum
	sort.Strings(gotStates)
	if !reflect.DeepEqual(gotStates, wantStates) {
		t.Errorf("DesiredApplyRequest requestedState=%v want=%v", gotStates, wantStates)
	}
	for _, name := range []string{"DesiredRuntimeView", "RuntimeView"} {
		forbidden := findForbiddenSchemaFields(name, document.Components.Schemas, []string{"secretRef", "previousSecretRef", "credentialBundle", "providerPath", "gatewayToken"})
		if len(forbidden) != 0 {
			t.Errorf("response schema %s exposes forbidden fields: %v", name, forbidden)
		}
	}
	for _, name := range []string{"RouteRuntimeView", "RuntimeSnapshotView", "PendingSnapshotView"} {
		schema := document.Components.Schemas[name]
		if schema.AdditionalProperties != false || len(schema.Properties) == 0 {
			t.Errorf("redacted schema %s is not strict: %+v", name, schema)
		}
	}
	runtimeSnapshot := document.Components.Schemas["RuntimeSnapshotView"]
	if operationID := runtimeSnapshot.Properties["operationId"]; operationID.Type != "string" || operationID.Format != "uuid" || !containsString(runtimeSnapshot.Required, "operationId") {
		t.Errorf("RuntimeSnapshotView operationId must be a required UUID: %+v", runtimeSnapshot)
	}
	desiredView := document.Components.Schemas["DesiredRuntimeView"]
	if desiredView.Properties["active"].Ref != "#/components/schemas/RuntimeSnapshotView" || desiredView.Properties["pending"].Ref != "#/components/schemas/PendingSnapshotView" {
		t.Errorf("DesiredRuntimeView snapshot refs=%+v", desiredView.Properties)
	}
	runtimeView := document.Components.Schemas["RuntimeView"]
	if runtimeView.Properties["active"].Ref != "#/components/schemas/RuntimeSnapshotView" || runtimeView.Properties["pending"].Ref != "#/components/schemas/PendingSnapshotView" || runtimeView.Properties["retiring"].Items == nil || runtimeView.Properties["retiring"].Items.Ref != "#/components/schemas/RuntimeSnapshotView" {
		t.Errorf("RuntimeView snapshot refs=%+v", runtimeView.Properties)
	}
	if runtimeView.Properties["tombstoned"].Type != "boolean" || !containsString(runtimeView.Required, "tombstoned") {
		t.Errorf("RuntimeView does not expose explicit tombstone truth: %+v", runtimeView)
	}
	for _, field := range []string{"runtimeId", "readiness", "writeReadiness", "blockedReason", "capabilities", "observedAt"} {
		if _, exists := runtimeView.Properties[field]; !exists {
			t.Errorf("RuntimeView missing operational field %q: %+v", field, runtimeView.Properties)
		}
	}
	for _, field := range []string{"readiness", "writeReadiness", "blockedReason", "capabilities"} {
		if !containsString(runtimeView.Required, field) {
			t.Errorf("RuntimeView unconditional field %q is not required: %v", field, runtimeView.Required)
		}
	}
	activeOnly := []string{"observedAt", "runtimeId"}
	for _, field := range activeOnly {
		if containsString(runtimeView.Required, field) {
			t.Errorf("RuntimeView active-only field %q is unconditionally required: %v", field, runtimeView.Required)
		}
	}
	if len(runtimeView.AllOf) != 1 || runtimeView.AllOf[0].If == nil || runtimeView.AllOf[0].Then == nil ||
		!reflect.DeepEqual(runtimeView.AllOf[0].If.Required, []string{"active"}) {
		t.Fatalf("RuntimeView active conditional=%+v", runtimeView.AllOf)
	}
	conditionalRequired := append([]string(nil), runtimeView.AllOf[0].Then.Required...)
	sort.Strings(conditionalRequired)
	if !reflect.DeepEqual(conditionalRequired, activeOnly) {
		t.Errorf("RuntimeView active conditional required=%v want=%v", conditionalRequired, activeOnly)
	}
	pendingStates := append([]string(nil), document.Components.Schemas["PendingSnapshotView"].Properties["state"].Enum...)
	sort.Strings(pendingStates)
	wantPendingStates := []string{"applied", "candidate_failed", "candidate_ready", "prepared"}
	if !reflect.DeepEqual(pendingStates, wantPendingStates) {
		t.Errorf("PendingSnapshotView states=%v want=%v", pendingStates, wantPendingStates)
	}
	wantErrors := []string{
		"ACCOUNT_RUNTIME_BLOCKED", "CANDIDATE_NOT_READY", "CREDENTIAL_EXPIRED", "CREDENTIAL_INVALID", "CREDENTIAL_PERMISSION_DENIED", "DESIRED_ACCOUNT_NOT_FOUND", "DESIRED_STATE_REVISION_CONFLICT",
		"GATEWAY_ADAPTER_AUTH_REQUIRED", "HANDOFF_DRAIN_BLOCKED", "IF_MATCH_REQUIRED", "IF_NONE_MATCH_REQUIRED",
		"INTERNAL_ERROR", "INVALID_ACCOUNT_TRANSITION", "LIVE_PRIVATE_WRITE_DISABLED",
		"OPERATION_ID_CONFLICT", "PROMOTION_FENCE_CONFLICT", "RETIRED_SECRET_REFERENCE",
		"STALE_RUNTIME_FENCE", "VALIDATION_ERROR", "VENUE_RATE_LIMITED", "VENUE_RESPONSE_INVALID", "VENUE_UNAVAILABLE",
	}
	gotErrors := document.Components.Schemas["InternalErrorEnvelope"].Properties["error"].Properties["code"].Enum
	sort.Strings(gotErrors)
	if !reflect.DeepEqual(gotErrors, wantErrors) {
		t.Errorf("internal stable error codes=%v want=%v", gotErrors, wantErrors)
	}
}

func TestFindForbiddenSchemaFieldsTraversesNestedRuntimeSchemas(t *testing.T) {
	schemas := map[string]internalSchema{
		"RuntimeView": {
			Properties: map[string]internalSchema{
				"active": {Ref: "#/components/schemas/RuntimeSnapshotView"},
			},
		},
		"RuntimeSnapshotView": {
			Properties: map[string]internalSchema{
				"routes": {Items: &internalSchema{Ref: "#/components/schemas/RouteRuntimeView"}},
			},
		},
		"RouteRuntimeView": {
			Properties: map[string]internalSchema{
				"providerPath": {Type: "string"},
			},
		},
	}

	got := findForbiddenSchemaFields("RuntimeView", schemas, []string{"providerPath"})
	want := []string{"RuntimeView.active.routes[].providerPath"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nested forbidden fields=%v want=%v", got, want)
	}
}

func TestFindForbiddenSchemaFieldsReportsSharedReferencesAndStopsCycles(t *testing.T) {
	schemas := map[string]internalSchema{
		"RuntimeView": {
			Properties: map[string]internalSchema{
				"providerPath": {Type: "string"},
				"active":       {Ref: "#/components/schemas/RuntimeSnapshotView"},
				"retiring":     {Items: &internalSchema{Ref: "#/components/schemas/RuntimeSnapshotView"}},
			},
		},
		"RuntimeSnapshotView": {
			Properties: map[string]internalSchema{
				"secretRef": {Type: "string"},
				"routes":    {Items: &internalSchema{Ref: "#/components/schemas/RouteRuntimeView"}},
				"account":   {Ref: "#/components/schemas/RuntimeView"},
			},
		},
		"RouteRuntimeView": {
			Properties: map[string]internalSchema{
				"gatewayToken": {Type: "string"},
				"parent":       {Ref: "#/components/schemas/RouteRuntimeView"},
			},
		},
	}

	got := findForbiddenSchemaFields("RuntimeView", schemas, []string{"secretRef", "providerPath", "gatewayToken"})
	want := []string{
		"RuntimeView.active.routes[].gatewayToken",
		"RuntimeView.active.secretRef",
		"RuntimeView.providerPath",
		"RuntimeView.retiring[].routes[].gatewayToken",
		"RuntimeView.retiring[].secretRef",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shared/cyclic forbidden fields=%v want=%v", got, want)
	}
}

func findForbiddenSchemaFields(root string, schemas map[string]internalSchema, forbiddenNames []string) []string {
	forbidden := make(map[string]struct{}, len(forbiddenNames))
	for _, name := range forbiddenNames {
		forbidden[name] = struct{}{}
	}

	activeRefs := make(map[string]bool)
	findings := make([]string, 0)
	var visit func(internalSchema, string)
	visit = func(schema internalSchema, path string) {
		if schema.Ref != "" {
			const schemaRefPrefix = "#/components/schemas/"
			if !strings.HasPrefix(schema.Ref, schemaRefPrefix) {
				return
			}
			name := strings.TrimPrefix(schema.Ref, schemaRefPrefix)
			if activeRefs[name] {
				return
			}
			activeRefs[name] = true
			visit(schemas[name], path)
			delete(activeRefs, name)
			return
		}

		propertyNames := make([]string, 0, len(schema.Properties))
		for name := range schema.Properties {
			propertyNames = append(propertyNames, name)
		}
		sort.Strings(propertyNames)
		for _, name := range propertyNames {
			propertyPath := path + "." + name
			if _, found := forbidden[name]; found {
				findings = append(findings, propertyPath)
			}
			visit(schema.Properties[name], propertyPath)
		}
		if schema.Items != nil {
			visit(*schema.Items, path+"[]")
		}
	}

	activeRefs[root] = true
	visit(schemas[root], root)
	sort.Strings(findings)
	return findings
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
