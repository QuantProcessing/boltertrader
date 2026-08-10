package ledgerfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresStoreRequiresPoolForAtomicPublicMethods(t *testing.T) {
	var pool *pgxpool.Pool
	store := ledgerfacts.NewPostgresStore(pool)
	if store == nil {
		t.Fatal("postgres store is required")
	}
	requireFundingAtomicStore(t, store)
	requireLineageAtomicStore(t, store)
}

func requireFundingAtomicStore(t *testing.T, store interface {
	IngestFundingPayments(context.Context, ledgerfacts.FundingIngestBatch) ([]ledgerfacts.FundingPaymentFact, ledgerfacts.SyncWatermark, error)
}) {
	t.Helper()
}

func requireLineageAtomicStore(t *testing.T, store interface {
	ReleaseLineageWithRevision(context.Context, string, ledgerfacts.LineageRevisionFact, time.Time) (ledgerfacts.LineageRevisionFact, error)
}) {
	t.Helper()
}
