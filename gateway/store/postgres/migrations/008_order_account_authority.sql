-- 001 pointed orders.account_id and reconciliation_checkpoints.account_id at
-- gateway_accounts, a flat one-product-per-account table. No production code
-- path ever wrote a row into it: RegisterAccount, its only writer, had test
-- callers only. 007 declared gateway_desired_accounts the single account
-- authority but left these two references behind, so on any deployment whose
-- accounts came from the lifecycle plane every order placement failed
-- orders_account_id_fkey and surfaced as an unclassified 500.
--
-- Repoint both at the authority and drop the orphan. The two delete rules
-- differ on purpose: an order is durable evidence and pins the account it was
-- placed on, so retiring an account with order history is refused; a
-- reconciliation checkpoint is a disposable per-account cursor with no
-- evidentiary value, so it follows the account out.

ALTER TABLE orders DROP CONSTRAINT orders_account_id_fkey;
ALTER TABLE orders ADD CONSTRAINT orders_account_id_fkey
    FOREIGN KEY (account_id) REFERENCES gateway_desired_accounts(account_id);

ALTER TABLE reconciliation_checkpoints DROP CONSTRAINT reconciliation_checkpoints_account_id_fkey;
ALTER TABLE reconciliation_checkpoints ADD CONSTRAINT reconciliation_checkpoints_account_id_fkey
    FOREIGN KEY (account_id) REFERENCES gateway_desired_accounts(account_id) ON DELETE CASCADE;

DROP TABLE gateway_accounts;
