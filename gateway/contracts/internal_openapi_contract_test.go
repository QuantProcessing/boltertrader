package contracts

import (
	"os"
	"reflect"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

type internalSchema struct {
	Ref                  string                    `yaml:"$ref"`
	Type                 any                       `yaml:"type"`
	Enum                 []string                  `yaml:"enum"`
	AdditionalProperties any                       `yaml:"additionalProperties"`
	Required             []string                  `yaml:"required"`
	Properties           map[string]internalSchema `yaml:"properties"`
}

type internalResponse struct {
	Content map[string]struct {
		Schema internalSchema `yaml:"schema"`
	} `yaml:"content"`
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
			Responses   map[string]struct {
				Ref string `yaml:"$ref"`
			} `yaml:"responses"`
			Parameters []struct {
				Name     string `yaml:"name"`
				In       string `yaml:"in"`
				Required bool   `yaml:"required"`
			} `yaml:"parameters"`
		} `yaml:"paths"`
		Components struct {
			Responses       map[string]internalResponse `yaml:"responses"`
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
		"/internal/v1/runtime/uncertain-commands":            {"get"},
			"/internal/v1/accounts/desired":                                      {"get"},
			"/internal/v1/accounts/desired/{accountId}":                          {"get"},
			"/internal/v1/accounts/desired/{accountId}/operations/{operationId}": {"put"},
			"/internal/v1/accounts/desired/{accountId}/retirements/{operationId}": {"put"},
		"/internal/v1/accounts/runtime":                      {"get"},
		"/internal/v1/accounts/runtime/{accountId}":          {"get"},
		"/internal/v1/accounts/runtime/{accountId}/events":   {"get"},
		"/internal/v1/catalog/venues":                        {"get"}, "/internal/v1/catalog/instruments": {"get"},
		"/internal/v1/market-data": {"get"}, "/internal/v1/private-events": {"get"},
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
	wantDesired := []string{"accountId", "credentialVersion", "desiredStateRevision", "nativeEnvironment", "operationId", "requestedState", "routes", "runtimeGeneration", "secretRef", "venue"}
	sort.Strings(desired.Required)
	if !reflect.DeepEqual(desired.Required, wantDesired) {
		t.Fatalf("DesiredApplyRequest required=%v want=%v", desired.Required, wantDesired)
	}
	for _, name := range []string{"DesiredRuntimeView", "RuntimeView"} {
		schema := document.Components.Schemas[name]
		for _, forbidden := range []string{"secretRef", "previousSecretRef", "credentialBundle", "providerPath", "gatewayToken"} {
			if _, ok := schema.Properties[forbidden]; ok {
				t.Errorf("response schema %s exposes %s", name, forbidden)
			}
		}
	}
	wantErrors := []string{
		"CANDIDATE_NOT_READY", "HANDOFF_DRAIN_BLOCKED", "LIVE_WRITE_DISABLED", "OPERATION_ID_CONFLICT",
		"PRIVATE_STREAM_GAP", "PROMOTION_FENCE_CONFLICT", "RETIRED_SECRET_REFERENCE", "ROUTE_PROFILE_CONFLICT",
	}
	gotErrors := document.Components.Schemas["InternalErrorEnvelope"].Properties["error"].Properties["code"].Enum
	sort.Strings(gotErrors)
	if !reflect.DeepEqual(gotErrors, wantErrors) {
		t.Errorf("internal stable error codes=%v want=%v", gotErrors, wantErrors)
	}
}
