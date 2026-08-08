ALTER TABLE platform_positions
    ADD COLUMN settlement_currency text NOT NULL DEFAULT '';

ALTER TABLE platform_portfolio_fills
    ADD COLUMN settlement_currency text NOT NULL DEFAULT '';

ALTER TABLE platform_unrealized_pnl
    ADD COLUMN currency text NOT NULL DEFAULT '';

ALTER TABLE platform_positions ALTER COLUMN settlement_currency DROP DEFAULT;
ALTER TABLE platform_portfolio_fills ALTER COLUMN settlement_currency DROP DEFAULT;
ALTER TABLE platform_unrealized_pnl ALTER COLUMN currency DROP DEFAULT;
