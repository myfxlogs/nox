-- 0058_ledger_d193.sql - D-193-B：ledger 资本流水字段扩展
-- 设计 SSOT: docs/步骤3资本事实与经济性系统-详细设计.md §3.3
-- 原字段语义保留兼容；新字段 nullable，legacy 行 NULL。
-- kind: deposit/withdrawal/internal_transfer/external_transfer/fee/adjustment
-- internal_transfer 必须有 counterparty_account_id（成对记录）。

ALTER TABLE ledger ADD COLUMN IF NOT EXISTS account_id              bigint;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS kind                    text;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS counterparty_account_id bigint;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS raw_amount              double precision;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS raw_currency            text;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS amount_usd              double precision;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS fx_rate                 double precision;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS fx_source               text;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS external_event_id       text;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS event_key               text;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS quality                 text;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS source                  text;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS occurred_at             timestamptz;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS received_at             timestamptz;

ALTER TABLE ledger ADD CONSTRAINT ledger_kind_check
    CHECK (kind IS NULL OR kind = ANY (ARRAY['deposit','withdrawal','internal_transfer','external_transfer','fee','adjustment']));
ALTER TABLE ledger ADD CONSTRAINT ledger_quality_check
    CHECK (quality IS NULL OR quality = ANY (ARRAY['actual','estimated','unknown']));
-- D-193 审计 P1-9：internal_transfer 业务规则——必须有 counterparty_account_id（应用层校验）。
-- 不用 CHECK 约束：legacy 行 kind=NULL 不受约束，且新增 internal_transfer 行写入时由应用层校验成对记录。

CREATE UNIQUE INDEX IF NOT EXISTS ledger_event_key_uniq
    ON ledger (event_key) WHERE event_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS ledger_account_occurred_idx
    ON ledger (account_id, occurred_at DESC) WHERE account_id IS NOT NULL;
