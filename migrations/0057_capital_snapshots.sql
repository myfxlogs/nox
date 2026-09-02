-- 0057_capital_snapshots.sql - D-193-B：资本快照（时间点可清算净值）
-- 设计 SSOT: docs/步骤3资本事实与经济性系统-详细设计.md §3.2
-- scope_type: account=单账户快照, overall=总体快照
-- quality: actual/estimated/unknown；FX/余额/负债/源身份缺失 → unknown
-- 追加式：不删除，只追加更正事件；同 source_event_id 幂等。

CREATE TABLE IF NOT EXISTS capital_snapshots (
    snapshot_id    bigint PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
    scope_type     text NOT NULL,                -- account / overall
    scope_key      text NOT NULL,                -- account_key 或固定 portfolio key
    account_id     bigint REFERENCES capital_accounts(account_id), -- overall 快照可为 NULL
    source         text NOT NULL,                -- broker / exchange / paper / manual
    source_event_id text,                        -- 外部快照 ID，缺失时 NULL
    source_ts      timestamptz,                  -- 外部产生时间
    received_ts    timestamptz NOT NULL DEFAULT now(),
    cash_usd       double precision,
    restricted_cash_usd double precision,
    margin_used_usd double precision,
    free_margin_usd double precision,
    credit_usd     double precision,
    liabilities_usd double precision,
    accrued_fee_usd double precision,
    unrealized_pnl_usd double precision,
    liquidation_haircut_pct double precision,    -- 0~1，未实现盈利保守折价
    liquidatable_equity_usd double precision,    -- 用于本金和回撤（公式计算）
    equity_usd     double precision,             -- broker 报告权益，仅对账交叉检查
    quality        text NOT NULL DEFAULT 'unknown', -- actual/estimated/unknown
    raw_currency   text,
    raw_amount     double precision,
    fx_rate        double precision,
    fx_source      text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (scope_type = ANY (ARRAY['account','overall'])),
    CHECK (quality = ANY (ARRAY['actual','estimated','unknown'])),
    CHECK (liquidation_haircut_pct IS NULL OR (liquidation_haircut_pct >= 0 AND liquidation_haircut_pct <= 1))
);

CREATE INDEX IF NOT EXISTS capital_snapshots_scope_ts_idx
    ON capital_snapshots (scope_type, scope_key, source_ts DESC);
CREATE INDEX IF NOT EXISTS capital_snapshots_account_ts_idx
    ON capital_snapshots (account_id, source_ts DESC) WHERE account_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS capital_snapshots_event_id_uniq
    ON capital_snapshots (scope_type, scope_key, source_event_id)
    WHERE source_event_id IS NOT NULL;
