package readiness

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestStaticChecksExactComponentsAndAggregate(t *testing.T) {
	checker := StaticChecks(map[string]Component{
		"process":    {Ready: true},
		"database":   {Ready: true},
		"gateway":    {Ready: false, Reason: "not-initialized"},
		"publicData": {Ready: false, Reason: "not-initialized"},
		"accounts":   {Ready: false, Reason: "not-initialized"},
		"execution":  {Ready: false, Reason: "not-initialized"},
	})
	result := checker.Check(context.Background())
	if result.Ready {
		t.Fatal("aggregate readiness should be false")
	}
	want := []string{"accounts", "database", "execution", "gateway", "maintenance", "process", "publicData"}
	if got := result.ComponentNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("component names = %v, want %v", got, want)
	}
}

func TestDiagnosticAccountDegradationDoesNotMakeReadableServiceUnready(t *testing.T) {
	checker := StaticChecks(map[string]Component{
		"maintenance": {Ready: true},
		"process":     {Ready: true},
		"database":    {Ready: true},
		"gateway":     {Ready: true},
		"publicData":  {Ready: true},
		"accounts": {
			Ready:  false,
			Reason: "catalog-unavailable",
			Details: map[string]string{
				"count": "4",
			},
		},
		"execution": {Ready: true},
	})
	result := checker.Check(context.Background())
	if !result.Ready {
		t.Fatalf("diagnostic account degradation made readable service unready: %+v", result)
	}
	if accounts := result.Components["accounts"]; accounts.Ready ||
		accounts.Reason != "catalog-unavailable" {
		t.Fatalf("account diagnostic was not preserved: %+v", accounts)
	}
}

type failingPinger struct{}

func (failingPinger) Ping(context.Context) error {
	return errors.New("postgres://user:secret-password@127.0.0.1/db connection failed")
}

func TestDatabaseReadinessDoesNotExposeRawError(t *testing.T) {
	result := WithDatabasePinger(failingPinger{}).Check(context.Background())
	database := result.Components["database"]
	if database.Ready || database.Reason != "database-unreachable" {
		t.Fatalf("unexpected database component: %+v", database)
	}
	got := database.Reason
	for key, value := range database.Details {
		got += key + value
	}
	if strings.Contains(got, "secret-password") || strings.Contains(got, "postgres://") {
		t.Fatalf("readiness leaked raw database error: %+v", database)
	}
}
