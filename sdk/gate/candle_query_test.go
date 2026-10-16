package sdk

import (
	"testing"
	"time"
)

func TestCandleQueryNeverCombinesLimitWithClosedWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	closed := candleQuery("1h", now.Add(-time.Hour), now, 25)
	if _, ok := closed["limit"]; ok || closed["from"] == "" || closed["to"] == "" {
		t.Fatalf("closed window query = %v", closed)
	}
	open := candleQuery("1h", time.Time{}, now, 25)
	if open["limit"] != "25" || open["to"] == "" {
		t.Fatalf("open window query = %v", open)
	}
}
