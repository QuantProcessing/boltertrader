-- Native fill IDs are unique only within an account and instrument scope.
-- Preserve every ID, fingerprint and accounting value: widening the key must
-- not cause already-booked fills or fee/PnL entries to be applied a second time.
-- Product/lineage intentionally stay out of the identity. Reattributing the
-- same venue fact to another strategy must still conflict.
ALTER TABLE portfolio_fills DROP CONSTRAINT portfolio_fills_pkey;
ALTER TABLE portfolio_fills ADD PRIMARY KEY (principal_id, account_id, instrument_id, fill_id);

ALTER TABLE ledger_entries DROP CONSTRAINT ledger_entries_pkey;
ALTER TABLE ledger_entries ADD PRIMARY KEY (principal_id, account_id, instrument_id, entry_id);
