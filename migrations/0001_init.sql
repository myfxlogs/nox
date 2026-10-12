-- 0001_init.sql: ARB 初始 schema（架构设计 §2.1-2.11）
-- 分区表 ticks 需要初始分区 + 分区创建函数

-- 2.1 ticks（报价时序，按月分区）
CREATE TABLE ticks (
    ts        TIMESTAMPTZ NOT NULL,
    venue     TEXT NOT NULL,
    symbol    TEXT NOT NULL,
    bid       DOUBLE PRECISION NOT NULL,
    ask       DOUBLE PRECISION NOT NULL,
    venue_type TEXT NOT NULL  -- mt4 / mt5 / spot / perp（D-028）
) PARTITION BY RANGE (ts);

CREATE INDEX idx_ticks_ts_brin ON ticks USING BRIN (ts);
CREATE INDEX idx_ticks_venue_symbol_ts ON ticks (venue, symbol, ts);

-- 初始分区（当月 + 下月，TickSampler 后续惰性创建）
CREATE TABLE ticks_2026_08 PARTITION OF ticks
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE ticks_2026_09 PARTITION OF ticks
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');

-- 2.2 facts（结构化事实）
CREATE TABLE facts (
    id     BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind   TEXT NOT NULL,
    venue  TEXT NOT NULL,
    symbol TEXT NOT NULL,
    value  DOUBLE PRECISION NOT NULL,
    unit   TEXT NOT NULL DEFAULT '',
    ts     TIMESTAMPTZ NOT NULL,
    src    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX facts_kind_symbol_ts_idx ON facts (kind, symbol, ts);

-- 2.3 rules（告警规则）
CREATE TABLE rules (
    id      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name    TEXT NOT NULL UNIQUE,
    kind    TEXT NOT NULL,
    cond    TEXT NOT NULL,
    level   TEXT NOT NULL CHECK (level IN ('info', 'warn', 'critical')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE
);

-- 2.4 trigger_states（规则状态机）
CREATE TABLE trigger_states (
    rule_id    BIGINT PRIMARY KEY REFERENCES rules (id) ON DELETE CASCADE,
    state      TEXT NOT NULL DEFAULT 'armed' CHECK (state IN ('armed', 'active', 'resolved')),
    since      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_value DOUBLE PRECISION
);

-- 2.5 alerts（告警）
CREATE TABLE alerts (
    id      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    rule_id BIGINT REFERENCES rules (id) ON DELETE CASCADE,  -- NULL = 系统级告警（D-027）
    ts      TIMESTAMPTZ NOT NULL DEFAULT now(),
    level   TEXT NOT NULL CHECK (level IN ('info', 'warn', 'critical')),
    message TEXT NOT NULL,
    acked   BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX alerts_ts_idx ON alerts (ts);

-- 2.6 opportunities（机会记录）
CREATE TABLE opportunities (
    id              TEXT PRIMARY KEY,
    type            TEXT NOT NULL CHECK (type IN ('CROSS_VENUE','CARRY','FUNDING_HEDGE')),
    venue_buy       TEXT NOT NULL,
    venue_sell      TEXT NOT NULL,
    price_buy       NUMERIC(20,8) NOT NULL,
    price_sell      NUMERIC(20,8) NOT NULL,
    gross_bps       NUMERIC(20,8) NOT NULL,
    spread_cost     NUMERIC(20,8) NOT NULL DEFAULT 0,
    commission_cost NUMERIC(20,8) NOT NULL DEFAULT 0,
    slippage_cost   NUMERIC(20,8) NOT NULL DEFAULT 0,
    swap_cost       NUMERIC(20,8) NOT NULL DEFAULT 0,
    net_bps         NUMERIC(20,8) NOT NULL,  -- 价差型=一次性 bps；息差型=年化 bps（D-029/D-031）
    breakeven_days  NUMERIC(10,2),
    quote_time      TIMESTAMPTZ NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_opportunities_expires_idx ON opportunities (expires_at DESC);
CREATE INDEX idx_opportunities_type_quote ON opportunities (type, quote_time DESC);

-- 2.7 symbol_map（符号映射）
CREATE TABLE symbol_map (
    venue          TEXT NOT NULL,
    venue_symbol   TEXT NOT NULL,
    canonical      TEXT NOT NULL,
    PRIMARY KEY (venue, venue_symbol)
);

-- 2.8 ledger（出入金台账）
CREATE TABLE ledger (
    id       BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    date     TIMESTAMPTZ NOT NULL,
    channel  TEXT NOT NULL,
    currency TEXT NOT NULL,
    amount   DOUBLE PRECISION NOT NULL,
    fee_rate DOUBLE PRECISION NOT NULL DEFAULT 0,
    tier     TEXT NOT NULL DEFAULT '',
    note     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX ledger_date_idx ON ledger (date);

-- 2.9 knowledge_entries（经验库）
CREATE TABLE knowledge_entries (
    id              BIGSERIAL PRIMARY KEY,
    ts              TIMESTAMPTZ NOT NULL DEFAULT now(),
    signature       TEXT NOT NULL UNIQUE,
    venue           TEXT NOT NULL DEFAULT '',
    symbol          TEXT NOT NULL DEFAULT '',
    verdict         TEXT NOT NULL,
    rationale       TEXT NOT NULL DEFAULT '',
    source          TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'active',
    validated_at    TIMESTAMPTZ,
    validation_note TEXT NOT NULL DEFAULT ''
);

-- 2.10 venue_accounts（账号凭据，加密存储）
CREATE TABLE venue_accounts (
    venue           TEXT NOT NULL,
    name            TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('mt4', 'mt5', 'cex')),
    credentials_enc BYTEA NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (venue, name)
);

-- 2.11 positions + position_legs + position_cashflows（持仓 · 交易台账）
CREATE TABLE positions (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    opportunity_id   TEXT NOT NULL,
    type             TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN
                     ('OPENING','OPEN','LEGGED','CLOSING','CLOSED','FAILED')),
    expected_net_bps NUMERIC(20,8) NOT NULL,
    realized_pnl     DOUBLE PRECISION,
    opened_at        TIMESTAMPTZ,
    closed_at        TIMESTAMPTZ,
    note             TEXT NOT NULL DEFAULT ''
);

CREATE TABLE position_legs (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    position_id    BIGINT NOT NULL REFERENCES positions(id) ON DELETE CASCADE,
    leg_no         SMALLINT NOT NULL,
    phase          TEXT NOT NULL CHECK (phase IN ('OPEN','CLOSE')),
    venue          TEXT NOT NULL,
    account        TEXT NOT NULL,
    symbol         TEXT NOT NULL,
    side           TEXT NOT NULL CHECK (side IN ('BUY','SELL')),
    qty            DOUBLE PRECISION NOT NULL,
    price_expected DOUBLE PRECISION NOT NULL,
    price_actual   DOUBLE PRECISION,
    order_id       TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL CHECK (status IN ('PENDING','SUBMITTED','FILLED','FAILED','UNKNOWN')),
    ts             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE position_cashflows (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    position_id BIGINT NOT NULL REFERENCES positions(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('funding','swap','commission')),
    venue       TEXT NOT NULL,
    amount_usd  DOUBLE PRECISION NOT NULL,
    ts          TIMESTAMPTZ NOT NULL
);

CREATE INDEX positions_status_idx ON positions (status, opened_at DESC);
CREATE INDEX position_legs_position_idx ON position_legs (position_id);

-- schema_migrations（core 启动时自跑）
CREATE TABLE IF NOT EXISTS schema_migrations (
    version  INTEGER PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO schema_migrations (version) VALUES (1);
