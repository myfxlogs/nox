-- 0074_cnfi_actions.sql - CN-FI 操作日志/投资笔记（D-242；研究库与实操库互链）
-- 金额/笔记=D-047 允许的 PG 运行时个人数据（禁入 git：本 migration 无种子）。

CREATE TABLE cnfi_actions (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code           text NOT NULL,                  -- 基金/国债代码
    layer          text NOT NULL CHECK (layer IN ('liquidity', 'coupon', 'locked', 'opportunity', 'none')),
    action_type    text NOT NULL CHECK (action_type IN ('buy', 'sell', 'dividend', 'note')),
    qty            numeric NOT NULL DEFAULT 0,
    amount_cny     numeric NOT NULL DEFAULT 0,     -- 成交金额（运行时个人数据）
    nav_at_action  numeric,                        -- 行动时净值（可得则记）
    signal_snapshot jsonb,                         -- 行动时信号状态快照（复盘燃料，D-242）
    rationale      text NOT NULL DEFAULT '',       -- 投资笔记：为什么做（强制，buy/sell 校验非空）
    action_date    date NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cnfi_actions_date_idx ON cnfi_actions (action_date DESC);
