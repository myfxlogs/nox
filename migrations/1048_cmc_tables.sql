-- 1047_cmc_tables.sql —— CMC Markets 采集器表族回归（STATE「CMC 采集器移植」票；
-- M3 切流时 fin_cmc_* 随 D-225 族件剔除不搬（1003 DROP），TRX 页三区业主令 10-19
-- 复工——schema 照 arb-archive 迁移源件 0065+0067+0069 终局形态合并重建，禁删史无涉
-- （NOX 库从未有此族表，纯新建）。
-- 装配键随迁（D-162：装配点 DB 驱动禁 env）——cmc_account_id=业主 CMC 账户号
-- （arb-archive wire_cmc.go 硬编码 38941616 的键化）；cmc_ws_disabled=false=WS 事件
-- 驱动默认开（true=回退全 REST，arb D-225 v2.1 同语义）。

CREATE TABLE IF NOT EXISTS fin_cmc_prices (
    id BIGSERIAL PRIMARY KEY,
    instrument_id VARCHAR(32) NOT NULL,
    bid_price NUMERIC(18,8),
    ask_price NUMERIC(18,8),
    mid_price NUMERIC(18,8),
    market_status VARCHAR(16),
    quote_time TIMESTAMPTZ,
    fetched_at TIMESTAMPTZ DEFAULT NOW(),
    src TEXT NOT NULL DEFAULT 'cmc_rest'
);
CREATE INDEX IF NOT EXISTS idx_cmc_prices_time ON fin_cmc_prices(fetched_at);

CREATE TABLE IF NOT EXISTS fin_cmc_account_summary (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL,
    total_account_value NUMERIC(18,2),
    total_cash_balance NUMERIC(18,2),
    total_unrealised_pnl NUMERIC(18,2),
    total_equity_free_for_margin NUMERIC(18,2),
    total_equity_used_in_margin NUMERIC(18,2),
    closeout_price NUMERIC(18,8),
    distance_to_closeout_pct NUMERIC(8,4),
    fetched_at TIMESTAMPTZ DEFAULT NOW(),
    alerts JSONB NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_cmc_summary_time ON fin_cmc_account_summary(fetched_at);

CREATE TABLE IF NOT EXISTS fin_cmc_positions (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL,
    instrument_id VARCHAR(32),
    direction VARCHAR(8),
    quantity NUMERIC(18,2),
    entry_price NUMERIC(18,8),
    unrealised_pnl NUMERIC(18,2),
    margin_amount NUMERIC(18,2),
    margin_percentage NUMERIC(8,4),
    evaluation_bid_price NUMERIC(18,8),
    evaluation_ask_price NUMERIC(18,8),
    opened_at TIMESTAMPTZ,
    order_id VARCHAR(64),
    fetched_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cmc_positions_time ON fin_cmc_positions(fetched_at);

CREATE TABLE IF NOT EXISTS fin_cmc_history (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL,
    transaction_id VARCHAR(64) UNIQUE,
    transaction_type VARCHAR(32),
    amount NUMERIC(18,2),
    balance NUMERIC(18,2),
    currency VARCHAR(8),
    order_id VARCHAR(32),
    transaction_time TIMESTAMPTZ,
    fetched_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cmc_history_time ON fin_cmc_history(transaction_time);
CREATE INDEX IF NOT EXISTS idx_cmc_history_txid ON fin_cmc_history(transaction_id);

INSERT INTO app_config (key, value, note) VALUES
  ('cmc_account_id', '38941616', 'CMC Markets 采集账户号（业主 CFD 户；arb-archive wire 硬编码键化）'),
  ('cmc_ws_disabled', 'false', 'CMC WS 事件驱动开关（true=回退全 REST，D-225 v2.1 同语义）')
ON CONFLICT (key) DO NOTHING;
