package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/QuantProcessing/boltertrader/services/notifications"
)

type NotificationStore struct{ db *sql.DB }

func NewNotificationStore(db *sql.DB) *NotificationStore { return &NotificationStore{db: db} }
func (s *NotificationStore) List(ctx context.Context, principal string) ([]notifications.Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, sealed, updated_at FROM notification_channels WHERE principal_id=$1 ORDER BY provider`, principal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []notifications.Record
	for rows.Next() {
		var provider string
		var sealed []byte
		var updated time.Time
		if err := rows.Scan(&provider, &sealed, &updated); err != nil {
			return nil, err
		}
		result = append(result, notifications.Record{Provider: notifications.Provider(provider), Sealed: sealed, UpdatedAt: updated})
	}
	return result, rows.Err()
}
func (s *NotificationStore) Get(ctx context.Context, principal string, provider notifications.Provider) (notifications.Record, error) {
	var sealed []byte
	var updated time.Time
	err := s.db.QueryRowContext(ctx, `SELECT sealed, updated_at FROM notification_channels WHERE principal_id=$1 AND provider=$2`, principal, string(provider)).Scan(&sealed, &updated)
	if err == sql.ErrNoRows {
		return notifications.Record{}, notifications.ErrNotFound
	}
	if err != nil {
		return notifications.Record{}, err
	}
	return notifications.Record{Provider: provider, Sealed: sealed, UpdatedAt: updated}, nil
}
func (s *NotificationStore) Put(ctx context.Context, principal string, record notifications.Record) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO notification_channels(principal_id,provider,sealed,updated_at) VALUES($1,$2,$3,$4) ON CONFLICT(principal_id,provider) DO UPDATE SET sealed=EXCLUDED.sealed,updated_at=EXCLUDED.updated_at`, principal, string(record.Provider), record.Sealed, record.UpdatedAt)
	return err
}
func (s *NotificationStore) Delete(ctx context.Context, principal string, provider notifications.Provider) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notification_channels WHERE principal_id=$1 AND provider=$2`, principal, string(provider))
	return err
}
