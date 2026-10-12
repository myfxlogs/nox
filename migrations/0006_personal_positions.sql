-- 0006_personal_positions.sql: 个人持仓（财务线 TRX 持仓接入，D-046）
-- 非 broker/CEX 双腿模型，独立轻量表（设计文档 财务线-TRX持仓接入方案.md §3）。
-- 报警线挂在风险承载腿（对冲空单）；质押仓仅展示（warn/crit 为 NULL）。

CREATE TABLE personal_positions (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    asset        TEXT NOT NULL,                    -- 类别级资产名（'TRX'）
    kind         TEXT NOT NULL CHECK (kind IN ('staking','short_hedge','other')),
    price_symbol TEXT NOT NULL,                    -- 监控价格源 canonical（'TRXUSDT'）
    warn_high    DOUBLE PRECISION,                 -- NULL = 此持仓只展示不报警；≥ 此值 → warn 报警
    crit_high    DOUBLE PRECISION,                 -- NULL = 不报警；≥ 此值 → critical 报警（须 ≥ warn_high）
    note         TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN','CLOSED')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at    TIMESTAMPTZ
);

CREATE INDEX personal_positions_symbol_idx ON personal_positions (price_symbol);

-- 种子数据（v1 两持仓固定；阈值 0.36/0.3695 为工作线级推送边界允许值，金额/数量不进 git）
INSERT INTO personal_positions (asset, kind, price_symbol, warn_high, crit_high, note) VALUES
    ('TRX', 'staking',     'TRXUSDT', NULL,    NULL,    'TRX 质押（能量+投票收益，14 天解绑）'),
    ('TRX', 'short_hedge', 'TRXUSDT', 0.36,    0.3695,  '对冲空单（杠杆，保值腿）；0.36=手动离场预案，0.3695=强平底线');
