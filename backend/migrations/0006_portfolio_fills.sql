CREATE TABLE platform_portfolio_fills (
    fill_id text PRIMARY KEY,
    fill_hash text NOT NULL,
    command_id text NOT NULL,
    mode text NOT NULL CHECK (mode IN ('test','live')),
    product_id text NOT NULL,
    lineage_id text NOT NULL,
    account_id text NOT NULL,
    instrument_id text NOT NULL,
    side text NOT NULL CHECK (side IN ('buy','sell')),
    quantity numeric NOT NULL,
    price numeric NOT NULL,
    fee numeric NOT NULL,
    fee_currency text NOT NULL,
    realized_pnl numeric NOT NULL,
    occurred_at timestamptz NOT NULL
);

CREATE INDEX platform_portfolio_fills_scope ON platform_portfolio_fills(mode, account_id, product_id, occurred_at, fill_id);
