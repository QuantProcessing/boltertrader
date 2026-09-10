-- Keep native fee facts and make their effect on spot inventory explicit.
ALTER TABLE portfolio_fills ADD COLUMN instrument_product text NOT NULL DEFAULT '';
ALTER TABLE portfolio_fills ADD COLUMN base_asset text NOT NULL DEFAULT '';
ALTER TABLE positions ADD COLUMN accounting_status text NOT NULL DEFAULT 'ready';

-- Historical fills did not retain product/base facts. Other-currency fees may
-- have reduced spot inventory; old OKX adapters also used the venue's reversed
-- sign. Preserve these records for review instead of silently rewriting money,
-- hashes, average costs, or order evidence during a schema upgrade.
UPDATE positions p SET accounting_status = 'legacy_fee_review'
WHERE EXISTS (
    SELECT 1 FROM portfolio_fills f
    WHERE f.principal_id=p.principal_id AND f.product_id=p.product_id
      AND f.lineage_id=p.lineage_id AND f.account_id=p.account_id
      AND f.instrument_id=p.instrument_id AND f.fee<>0
      AND (f.fee_currency<>f.settlement_currency OR f.instrument_id LIKE 'okx:%')
);
