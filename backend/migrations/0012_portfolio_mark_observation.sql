ALTER TABLE platform_positions
    ADD COLUMN mark_observed_at TIMESTAMPTZ NOT NULL DEFAULT '0001-01-01 00:00:00+00';

UPDATE platform_positions
SET mark_observed_at = updated_at
WHERE mark_price <> 0;
