package httpapi

import (
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRegisteredRoutesExactlyMatchOpenAPIAndExcludeForbiddenSurfaces(t *testing.T) {
	data, err := os.ReadFile("../../api/exchange-gateway/v1/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	openAPI := make([]string, 0)
	for path, methods := range document.Paths {
		for method := range methods {
			openAPI = append(openAPI, strings.ToUpper(method)+" "+path)
		}
	}
	registered := make([]string, 0, len(Routes()))
	for _, route := range Routes() {
		registered = append(registered, route.Method+" "+route.Pattern)
	}
	sort.Strings(openAPI)
	sort.Strings(registered)
	if strings.Join(registered, "\n") != strings.Join(openAPI, "\n") {
		t.Fatalf("registered/OpenAPI routes differ\nregistered:\n%s\nOpenAPI:\n%s",
			strings.Join(registered, "\n"), strings.Join(openAPI, "\n"))
	}

	forbidden := []string{
		"orderbook", "candles", "ticker", "public-trades",
		"transfer", "deposit", "withdraw", "credential", "route-order",
		"custody", "grpc",
	}
	for _, route := range registered {
		lower := strings.ToLower(route)
		for _, term := range forbidden {
			if strings.Contains(lower, term) {
				t.Errorf("forbidden REST surface %q contains %q", route, term)
			}
		}
	}
}
