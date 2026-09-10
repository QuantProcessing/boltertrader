-- Retire the local application without rewriting order or accounting history.
-- Migration 005 remains immutable for installations that already applied it.
DROP TABLE arbitrage_executions;

-- No remaining application owns these recovery tasks. Actual venue orders and
-- exposure remain as recorded; this migration does not cancel or settle them.
DELETE FROM reconciliation_tasks t USING commands c
WHERE t.command_id = c.command_id AND c.product_id = 'arbitrage';

-- Release only unused local holds. A reservation linked to an order remains
-- untouched because its execution may already have reached the venue.
UPDATE reservations r SET status = 'released', updated_at = now()
WHERE r.product_id = 'arbitrage' AND r.status = 'held'
  AND NOT EXISTS (SELECT 1 FROM commands c WHERE c.reservation_id = r.reservation_id);
