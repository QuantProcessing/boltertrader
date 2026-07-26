package postgres

import (
	"context"
	"hash/fnv"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ownershipLockKey int32 = 0x470004

type AccountOwnership struct {
	mu        sync.Mutex
	conn      *pgxpool.Conn
	accountID domain.AccountID
	released  bool
}

func AcquireAccountOwnership(ctx context.Context, pool *pgxpool.Pool, accountID domain.AccountID) (*AccountOwnership, error) {
	if err := accountID.Validate(); err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, redactErr("acquire ownership connection", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, $2)`, ownershipLockKey, stableLockPart(string(accountID))).Scan(&acquired); err != nil {
		conn.Release()
		return nil, redactErr("acquire ownership lock", err)
	}
	if !acquired {
		conn.Release()
		return nil, ErrOwnershipUnavailable
	}
	return &AccountOwnership{conn: conn, accountID: accountID}, nil
}

func (ownership *AccountOwnership) Healthy() bool {
	if ownership == nil {
		return false
	}
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	if ownership.released || ownership.conn == nil || ownership.conn.Conn().IsClosed() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return ownership.conn.Ping(ctx) == nil
}

func (ownership *AccountOwnership) Release(ctx context.Context) error {
	if ownership == nil {
		return nil
	}
	ownership.mu.Lock()
	defer ownership.mu.Unlock()
	if ownership.released || ownership.conn == nil {
		return nil
	}
	conn := ownership.conn
	_, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1, $2)`, ownershipLockKey, stableLockPart(string(ownership.accountID)))
	ownership.released = true
	ownership.conn = nil
	if err != nil {
		hijacked := conn.Hijack()
		_ = hijacked.Close(context.Background())
		return redactErr("release ownership lock", err)
	}
	conn.Release()
	return nil
}

func stableLockPart(value string) int32 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(value))
	return int32(hash.Sum32())
}
