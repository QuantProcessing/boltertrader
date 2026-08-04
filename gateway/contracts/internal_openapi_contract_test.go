package contracts

import (
	"os"
	"reflect"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

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
			Parameters  []struct {
				Name     string `yaml:"name"`
				In       string `yaml:"in"`
				Required bool   `yaml:"required"`
			} `yaml:"parameters"`
		} `yaml:"paths"`
		Components struct {
			SecuritySchemes map[string]struct {
				Type   string `yaml:"type"`
				Scheme string `yaml:"scheme"`
			} `yaml:"securitySchemes"`
			Schemas map[string]struct {
				AdditionalProperties any            `yaml:"additionalProperties"`
				Required             []string       `yaml:"required"`
				Properties           map[string]any `yaml:"properties"`
			} `yaml:"schemas"`
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
		"/internal/v1/accounts/desired":                      {"get", "post"},
		"/internal/v1/accounts/desired/{accountId}":          {"delete", "get", "put"},
		"/internal/v1/accounts/desired/{accountId}/validate": {"post"},
		"/internal/v1/accounts/desired/{accountId}/rotate":   {"post"},
		"/internal/v1/accounts/desired/{accountId}/disable":  {"post"},
		"/internal/v1/accounts/desired/{accountId}/drain":    {"post"},
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
	for _, name := range []string{"DesiredAccount", "DesiredMutation", "RotationFence", "RuntimeAccount", "InternalErrorEnvelope"} {
		schema, ok := document.Components.Schemas[name]
		if !ok {
			t.Errorf("missing internal schema %s", name)
			continue
		}
		if schema.AdditionalProperties != false {
			t.Errorf("schema %s must reject unknown fields", name)
		}
	}
	desired := document.Components.Schemas["DesiredAccount"]
	wantDesired := []string{"accountId", "credentialVersion", "desiredStateRevision", "requestedState", "runtimeGeneration", "secretRef", "venue", "venueEnvironment"}
	sort.Strings(desired.Required)
	if !reflect.DeepEqual(desired.Required, wantDesired) {
		t.Fatalf("DesiredAccount required=%v want=%v", desired.Required, wantDesired)
	}
}
