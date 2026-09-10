package credential

import (
	"encoding/json"
	"testing"
)

func TestSchemaFieldsProduceAnAcceptedBundleAndDoNotShareStorage(t *testing.T) {
	registry := DefaultRegistry()
	for venue := range defaultAuthKinds {
		schema, err := registry.Schema(venue)
		if err != nil {
			t.Fatal(err)
		}
		metadata, secrets := map[string]any{}, map[string]any{}
		for _, field := range schema.Fields {
			var value any = "test-value"
			if field.ValueType == "integer" {
				value = 0
			}
			if field.Key == "accountAddress" || field.Key == "userAddress" || field.Key == "expectedSigner" {
				value = "0x1111111111111111111111111111111111111111"
			}
			if field.Metadata {
				metadata[field.Key] = value
			} else {
				secrets[field.Key] = value
			}
		}
		raw, err := json.Marshal(map[string]any{"schemaVersion": schema.SchemaVersion, "venue": venue, "authKind": schema.AuthKind, "metadata": metadata, "secrets": secrets})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := registry.Decode(venue, raw)
		if err != nil {
			t.Fatalf("%s schema disagrees with decoder: %v", venue, err)
		}
		decoded.Clear()
		schema.Fields[0].Key = "mutated"
		again, _ := registry.Schema(venue)
		if again.Fields[0].Key == "mutated" {
			t.Fatal("schema storage shared")
		}
	}
	for _, venue := range []string{"unknown", "BINANCE", "binance "} {
		if _, err := registry.Schema(venue); err == nil {
			t.Fatalf("unknown schema accepted: %q", venue)
		}
	}
}
