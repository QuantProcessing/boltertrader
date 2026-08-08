package work

import (
	"os"
	"strings"
	"testing"
)

func TestMemoryStoreDoesNotOrderFactsByOpaqueCursor(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(source)), " ")
	if strings.Contains(normalized, "store.facts[i].Cursor < store.facts[j].Cursor") {
		t.Fatal("MemoryStore orders facts by opaque cursor text")
	}
}
