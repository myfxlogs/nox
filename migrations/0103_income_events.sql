-- D-334ab 位点①：binance income 实数轨独立表（结算对齐采集，SSOT v2）。
-- 独立表根因：position_cashflows_per_leg_uniq 每腿每日唯一索引被 swapledger estimated 行占据，
-- actual 行写入即撞；且实数轨需要 tranId 幂等锚（facts 表无唯一约束）。
-- 唯一键三列 (venue, account_id, tran_id)=防 demo/real tranId 数值碰撞（提示词级修正，v2 tran_id 单列扩此）。
CREATE TABLE IF NOT EXISTS income_events (
    id          BIGSERIAL PRIMARY KEY,
    venue       TEXT        NOT NULL,
    account_id  INTEGER     NOT NULL,
    symbol      TEXT        NOT NULL,
    income_type TEXT        NOT NULL,
    amount_usd NUMERIC(20,8) NOT NULL,
    tran_id     BIGINT      NOT NULL,
    ts          TIMESTAMPTZ NOT NULL,
    position_id INTEGER,
    leg_no      INTEGER,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT income_events_tran_uniq UNIQUE (venue, account_id, tran_id)
);
CREATE INDEX IF NOT EXISTS income_events_ts_idx ON income_events (venue, ts);
