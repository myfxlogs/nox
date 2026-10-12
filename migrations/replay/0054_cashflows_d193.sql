-- 0054_cashflows_d193.sql - D-193-A：position_cashflows 事实身份字段扩展
-- 设计 SSOT: docs/步骤3资本事实与经济性系统-详细设计.md §3.5
-- 语义：新行强制 event_key（事实幂等键）；legacy 行允许 NULL。
-- event_key 优先用 broker/exchange 外部事件 ID；无外部 ID 时用确定性 hash。
-- quality: actual/estimated/unknown；新行默认 actual，legacy 行 NULL（查询时 COALESCE 为 legacy）。
-- kind CHECK 扩展 'other'（D-193 §3.5）。

ALTER TABLE position_cashflows ADD COLUMN IF NOT EXISTS account_id          bigint;
ALTER TABLE position_cashflows ADD COLUMN IF NOT EXISTS raw_currency        text;   -- D-193 审计 P1-2：设计 §3.5 字段名 raw_currency 非 currency
ALTER TABLE position_cashflows ADD COLUMN IF NOT EXISTS raw_amount          double precision;
ALTER TABLE position_cashflows ADD COLUMN IF NOT EXISTS fx_rate             double precision;
ALTER TABLE position_cashflows ADD COLUMN IF NOT EXISTS fx_source           text;
ALTER TABLE position_cashflows ADD COLUMN IF NOT EXISTS event_key           text;
ALTER TABLE position_cashflows ADD COLUMN IF NOT EXISTS external_event_id   text;   -- D-193 审计 P1-1：设计 §3.5 要求外部原始事件 ID
ALTER TABLE position_cashflows ADD COLUMN IF NOT EXISTS source              text;
ALTER TABLE position_cashflows ADD COLUMN IF NOT EXISTS quality             text;
ALTER TABLE position_cashflows ADD COLUMN IF NOT EXISTS source_ts           timestamptz;
ALTER TABLE position_cashflows ADD COLUMN IF NOT EXISTS received_ts         timestamptz;
ALTER TABLE position_cashflows ADD COLUMN IF NOT EXISTS occurred_at         timestamptz;  -- D-193 审计 P1-1：设计 §3.5 要求现金流发生时间

-- kind CHECK 扩展 'other'
ALTER TABLE position_cashflows DROP CONSTRAINT IF EXISTS position_cashflows_kind_check;
ALTER TABLE position_cashflows ADD CONSTRAINT position_cashflows_kind_check
    CHECK (kind = ANY (ARRAY['funding'::text, 'swap'::text, 'commission'::text, 'other'::text]));

-- event_key 幂等索引（新行强制唯一，legacy 行 NULL 不受约束）
CREATE UNIQUE INDEX IF NOT EXISTS position_cashflows_event_key_uniq
    ON position_cashflows (event_key) WHERE event_key IS NOT NULL;
