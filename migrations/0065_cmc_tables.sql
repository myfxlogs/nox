-- D-205 CMC 采集器 Phase 1：fin_cmc_* 4 表（含 margin_percentage 字段）

-- 实时报价
CREATE TABLE fin_cmc_prices (
    id BIGSERIAL PRIMARY KEY,
    instrument_id VARCHAR(32) NOT NULL,
    bid_price NUMERIC(18,8),
    ask_price NUMERIC(18,8),
    mid_price NUMERIC(18,8),
    market_status VARCHAR(16),
    quote_time TIMESTAMPTZ,
    fetched_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_cmc_prices_time ON fin_cmc_prices(fetched_at);

-- 账户摘要
CREATE TABLE fin_cmc_account_summary (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL,
    total_account_value NUMERIC(18,2),
    total_cash_balance NUMERIC(18,2),
    total_unrealised_pnl NUMERIC(18,2),
    total_equity_free_for_margin NUMERIC(18,2),
    total_equity_used_in_margin NUMERIC(18,2),
    closeout_price NUMERIC(18,8),
    distance_to_closeout_pct NUMERIC(8,4),
    fetched_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_cmc_summary_time ON fin_cmc_account_summary(fetched_at);

-- 持仓详情
CREATE TABLE fin_cmc_positions (
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
    fetched_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_cmc_positions_time ON fin_cmc_positions(fetched_at);

-- 历史记录（每日利息等）
CREATE TABLE fin_cmc_history (
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
CREATE INDEX idx_cmc_history_time ON fin_cmc_history(transaction_time);
CREATE INDEX idx_cmc_history_txid ON fin_cmc_history(transaction_id);
