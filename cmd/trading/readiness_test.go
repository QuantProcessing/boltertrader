package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDatabaseReadinessTracksOutageAndRecovery(t *testing.T) {
	var unavailable bool
	ping := func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Fatal("database probe is not bounded")
		}
		if unavailable {
			return errors.New("private connection details must not reach readiness")
		}
		return nil
	}
	for _, down := range []bool{false, true, false} {
		unavailable = down
		status := databaseReadiness(ping)
		if status.Ready == down || status.ObservedAt.IsZero() {
			t.Fatalf("outage=%t status=%+v", down, status)
		}
		if down && status.Reason != "database_unavailable" {
			t.Fatalf("database error leaked: %s", status.Reason)
		}
	}
}
