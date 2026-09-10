package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Exercise the published payload through the real boundary. Comparing the
// properties with the decoder also catches optional fields omitted by a spec.
func TestPublishedPlaceOrderContractMatchesHandler(t *testing.T) {
	data, err := os.ReadFile("../../api/exchange-gateway/v1/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string       `yaml:"required"`
				Properties map[string]any `yaml:"properties"`
				Example    map[string]any `yaml:"example"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	schema := document.Components.Schemas["PlaceOrderRequest"]
	fields := reflect.TypeFor[placeOrderBody]()
	if len(schema.Properties) != fields.NumField() {
		t.Fatalf("schema has %d properties, decoder has %d fields", len(schema.Properties), fields.NumField())
	}
	for i := 0; i < fields.NumField(); i++ {
		name := strings.Split(fields.Field(i).Tag.Get("json"), ",")[0]
		if _, ok := schema.Properties[name]; !ok {
			t.Fatalf("decoder field %q is missing from the schema", name)
		}
	}
	check := func(t *testing.T, body map[string]any, want int) {
		t.Helper()
		fixture := newHTTPFixture(t)
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/orders", bytes.NewReader(data))
		request.Header.Set("Authorization", "Bearer test-token")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "idem-1")
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("status=%d want=%d body=%s", recorder.Code, want, recorder.Body.String())
		}
		if want == http.StatusBadRequest && fixture.orders.placeCalls != 0 {
			t.Fatal("invalid contract request reached the backend")
		}
	}
	check(t, schema.Example, http.StatusAccepted)
	for _, field := range schema.Required {
		t.Run("missing_"+field, func(t *testing.T) {
			body := make(map[string]any)
			for key, value := range schema.Example {
				if key != field {
					body[key] = value
				}
			}
			check(t, body, http.StatusBadRequest)
		})
	}
	for _, field := range []string{"desired_state_revision", "runtime_generation", "credential_version"} {
		t.Run("obsolete_"+field, func(t *testing.T) {
			body := make(map[string]any)
			for key, value := range schema.Example {
				body[key] = value
			}
			body[field] = 1
			check(t, body, http.StatusBadRequest)
		})
	}
}
