package contracts

import (
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
	if got, want := event.Properties["type"].Enum, []string{"ack", "snapshot", "delta", "full", "resync_required", "error", "pong"}; !reflect.DeepEqual(got, want) {
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

func TestInternalAccountAPITracksRegisteredRoutesAndSimpleConnection(t *testing.T) {
	data, err := os.ReadFile("../../api/exchange-gateway/v1/internal-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Security []map[string][]string     `yaml:"security"`
		Paths    map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Security, []map[string][]string{{"gatewayAdapterAuth": {}}}) {
		t.Fatal("internal authentication missing")
	}
	routes := append(gatewayhttp.InternalRoutes(), gatewayhttp.InternalAccountRoutes()...)
	expected := map[string]bool{}
	for _, route := range routes {
		expected[route.Pattern+" "+strings.ToLower(route.Method)] = true
		if _, ok := doc.Paths[route.Pattern][strings.ToLower(route.Method)]; !ok {
			t.Errorf("undocumented route: %+v", route)
		}
	}
	for path, methods := range doc.Paths {
		for method := range methods {
			if !expected[path+" "+method] {
				t.Errorf("documented route is not mounted: %s %s", method, path)
			}
		}
	}
	schemas := loadInternalSchemas(t)
	required := append([]string(nil), schemas["RuntimeView"].Required...)
	sort.Strings(required)
	if !reflect.DeepEqual(required, []string{"accountId", "connected", "observedAt", "runtimeId"}) {
		t.Fatalf("connection schema=%v", required)
	}
	add := schemas["AddAccount"]
	if add.AdditionalProperties != false || len(add.Properties) != 3 {
		t.Fatal("account addition has acquired extra configuration")
	}
	for _, name := range []string{"Account", "RuntimeView"} {
		if forbidden := findForbiddenSchemaFields(name, schemas, []string{"secretRef", "credentialBundle", "capabilities", "pending", "retiring"}); len(forbidden) != 0 {
			t.Fatalf("%s exposes %v", name, forbidden)
		}
	}
	for _, name := range []string{"DesiredApplyRequest", "PendingSnapshotView", "RuntimeCapabilityView"} {
		if _, ok := schemas[name]; ok {
			t.Errorf("obsolete schema remains: %s", name)
		}
	}
}
