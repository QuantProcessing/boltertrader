package readiness

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DBPinger interface {
	Ping(context.Context) error
}

func WithDatabase(pool *pgxpool.Pool) Checker {
	return WithDatabasePinger(pool)
}

func WithDatabasePinger(pool DBPinger) Checker {
	return databaseChecker{pool: pool}
}

func WithDatabasePingerAndComponents(pool DBPinger, checks map[string]ComponentCheck) Checker {
	return WithComponents(WithDatabasePinger(pool), checks)
}

type databaseChecker struct {
	pool DBPinger
}

func (d databaseChecker) Check(ctx context.Context) Result {
	database := Component{Ready: true}
	if err := d.pool.Ping(ctx); err != nil {
		database = Component{Ready: false, Reason: "database-unreachable", Details: map[string]string{"category": "ping-failed"}}
	}
	return StaticChecks(map[string]Component{
		"process":    {Ready: true},
		"database":   database,
		"gateway":    {Ready: false, Reason: "not-initialized"},
		"publicData": {Ready: false, Reason: "not-initialized"},
		"accounts":   {Ready: false, Reason: "not-initialized"},
	}).Check(ctx)
}
