package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestClaimQueryDoesNotCompareOpaqueFactsCursorsLexically(t *testing.T) {
	source, err := os.ReadFile("work.go")
	if err != nil {
		t.Fatal(err)
	}
	query := strings.Join(strings.Fields(string(source)), " ")
	if strings.Contains(query, "i.facts_cursor>$2") || strings.Contains(query, "i.facts_cursor > $2") {
		t.Fatal("ClaimWork compares opaque facts cursors lexically")
	}
}

func TestFactPageQueryDoesNotCompareOpaqueCursorsLexically(t *testing.T) {
	source, err := os.ReadFile("work.go")
	if err != nil {
		t.Fatal(err)
	}
	query := strings.Join(strings.Fields(string(source)), " ")
	if strings.Contains(query, "f.cursor>$2") || strings.Contains(query, "f.cursor > $2") {
		t.Fatal("FactPage compares opaque cursors lexically")
	}
	if strings.Contains(query, "ORDER BY min(effective_at),cursor") {
		t.Fatal("FactPage uses opaque cursor text as a batch-order tie-breaker")
	}
	if strings.Contains(query, "ORDER BY effective_at,cursor,fact_id") {
		t.Fatal("authoritative fact loading orders opaque cursor text relatively")
	}
}
