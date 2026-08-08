ALTER TABLE platform_positions ADD COLUMN principal_id text;
ALTER TABLE platform_portfolio_fills ADD COLUMN principal_id text;
ALTER TABLE platform_ledger_entries ADD COLUMN principal_id text;
ALTER TABLE platform_unrealized_pnl ADD COLUMN principal_id text;

UPDATE platform_portfolio_fills AS fill
SET principal_id = command.principal_id
FROM platform_commands AS command
WHERE command.command_id = fill.command_id
  AND command.mode = fill.mode
  AND command.product_id = fill.product_id
  AND command.lineage_id = fill.lineage_id
  AND command.account_id = fill.account_id;

WITH ownership AS (
    SELECT position.mode, position.product_id, position.lineage_id, position.account_id, position.instrument_id,
           min(command.principal_id) AS principal_id
    FROM platform_positions AS position
    JOIN platform_commands AS command
      ON command.mode = position.mode
     AND command.product_id = position.product_id
     AND command.lineage_id = position.lineage_id
     AND command.account_id = position.account_id
    GROUP BY position.mode, position.product_id, position.lineage_id, position.account_id, position.instrument_id
    HAVING count(DISTINCT command.principal_id) = 1
       AND bool_and(btrim(command.principal_id) <> '')
)
UPDATE platform_positions AS position
SET principal_id = ownership.principal_id
FROM ownership
WHERE position.mode = ownership.mode
  AND position.product_id = ownership.product_id
  AND position.lineage_id = ownership.lineage_id
  AND position.account_id = ownership.account_id
  AND position.instrument_id = ownership.instrument_id;

WITH ownership AS (
    SELECT entry.mode, entry.product_id, entry.lineage_id, entry.account_id, entry.instrument_id,
           min(command.principal_id) AS principal_id
    FROM platform_ledger_entries AS entry
    JOIN platform_commands AS command
      ON command.mode = entry.mode
     AND command.product_id = entry.product_id
     AND command.lineage_id = entry.lineage_id
     AND command.account_id = entry.account_id
    GROUP BY entry.mode, entry.product_id, entry.lineage_id, entry.account_id, entry.instrument_id
    HAVING count(DISTINCT command.principal_id) = 1
       AND bool_and(btrim(command.principal_id) <> '')
)
UPDATE platform_ledger_entries AS entry
SET principal_id = ownership.principal_id
FROM ownership
WHERE entry.mode = ownership.mode
  AND entry.product_id = ownership.product_id
  AND entry.lineage_id = ownership.lineage_id
  AND entry.account_id = ownership.account_id
  AND entry.instrument_id = ownership.instrument_id;

WITH ownership AS (
    SELECT pnl.mode, pnl.product_id, pnl.lineage_id, pnl.account_id, pnl.instrument_id,
           min(command.principal_id) AS principal_id
    FROM platform_unrealized_pnl AS pnl
    JOIN platform_commands AS command
      ON command.mode = pnl.mode
     AND command.product_id = pnl.product_id
     AND command.lineage_id = pnl.lineage_id
     AND command.account_id = pnl.account_id
    GROUP BY pnl.mode, pnl.product_id, pnl.lineage_id, pnl.account_id, pnl.instrument_id
    HAVING count(DISTINCT command.principal_id) = 1
       AND bool_and(btrim(command.principal_id) <> '')
)
UPDATE platform_unrealized_pnl AS pnl
SET principal_id = ownership.principal_id
FROM ownership
WHERE pnl.mode = ownership.mode
  AND pnl.product_id = ownership.product_id
  AND pnl.lineage_id = ownership.lineage_id
  AND pnl.account_id = ownership.account_id
  AND pnl.instrument_id = ownership.instrument_id;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM platform_positions WHERE principal_id IS NULL OR btrim(principal_id) = '')
       OR EXISTS (SELECT 1 FROM platform_portfolio_fills WHERE principal_id IS NULL OR btrim(principal_id) = '')
       OR EXISTS (SELECT 1 FROM platform_ledger_entries WHERE principal_id IS NULL OR btrim(principal_id) = '')
       OR EXISTS (SELECT 1 FROM platform_unrealized_pnl WHERE principal_id IS NULL OR btrim(principal_id) = '') THEN
        RAISE EXCEPTION 'principal accounting ownership is missing or ambiguous';
    END IF;
END
$$;

ALTER TABLE platform_positions ALTER COLUMN principal_id SET NOT NULL;
ALTER TABLE platform_portfolio_fills ALTER COLUMN principal_id SET NOT NULL;
ALTER TABLE platform_ledger_entries ALTER COLUMN principal_id SET NOT NULL;
ALTER TABLE platform_unrealized_pnl ALTER COLUMN principal_id SET NOT NULL;

ALTER TABLE platform_positions ADD CONSTRAINT platform_positions_principal_nonempty CHECK (btrim(principal_id) <> '');
ALTER TABLE platform_portfolio_fills ADD CONSTRAINT platform_portfolio_fills_principal_nonempty CHECK (btrim(principal_id) <> '');
ALTER TABLE platform_ledger_entries ADD CONSTRAINT platform_ledger_entries_principal_nonempty CHECK (btrim(principal_id) <> '');
ALTER TABLE platform_unrealized_pnl ADD CONSTRAINT platform_unrealized_pnl_principal_nonempty CHECK (btrim(principal_id) <> '');

ALTER TABLE platform_positions DROP CONSTRAINT platform_positions_pkey;
ALTER TABLE platform_positions ADD PRIMARY KEY (principal_id, mode, product_id, lineage_id, account_id, instrument_id);

ALTER TABLE platform_portfolio_fills DROP CONSTRAINT platform_portfolio_fills_pkey;
ALTER TABLE platform_portfolio_fills ADD PRIMARY KEY (principal_id, fill_id);

ALTER TABLE platform_ledger_entries DROP CONSTRAINT platform_ledger_entries_pkey;
ALTER TABLE platform_ledger_entries ADD PRIMARY KEY (principal_id, entry_id);

ALTER TABLE platform_unrealized_pnl DROP CONSTRAINT platform_unrealized_pnl_pkey;
ALTER TABLE platform_unrealized_pnl ADD PRIMARY KEY (principal_id, mode, product_id, lineage_id, account_id, instrument_id);

DROP INDEX platform_ledger_attribution;
CREATE INDEX platform_ledger_attribution ON platform_ledger_entries(principal_id, mode, product_id, lineage_id, account_id, instrument_id, occurred_at);

DROP INDEX platform_portfolio_fills_scope;
CREATE INDEX platform_portfolio_fills_scope ON platform_portfolio_fills(principal_id, mode, account_id, product_id, occurred_at, fill_id);
