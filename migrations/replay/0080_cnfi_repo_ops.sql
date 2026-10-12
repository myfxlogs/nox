-- D-270 逆回购周四操作闭环：工作流表 + cnfi_actions 加 rate_pct 列。
-- cnfi_repo_ops 管提醒→确认生命周期；cnfi_actions 管实际记账（operated 时写一行）。
-- 个人数据（金额/利率）只在 PG runtime，本 migration 无种子（D-047）。

CREATE TABLE IF NOT EXISTS cnfi_repo_ops (
    id                  BIGSERIAL PRIMARY KEY,
    opportunity_week    TEXT NOT NULL,             -- ISO 周 "2026-W37"，每周一条
    instrument_code     TEXT NOT NULL,             -- "204001" (GC001) — v1 主信号品种
    instrument_name     TEXT NOT NULL,             -- "GC001"
    rate_pct_at_trigger DOUBLE PRECISION NOT NULL, -- 触发时利率%
    expected_return_cny DOUBLE PRECISION,          -- 预期收益(元) = rate × idle_cny × days/365
    holding_days        INTEGER NOT NULL DEFAULT 3, -- 持有天数(GC001周四=3天，T+1结算享周末利息)
    signal_snapshot     TEXT,                      -- 触发时信号快照 JSON（GC001 rate + 30d avg + warn阈值）
    trigger_date        DATE NOT NULL,              -- 触发日期(周四)
    reminder_count      INTEGER NOT NULL DEFAULT 0, -- 已提醒次数
    last_reminder_at    TIMESTAMPTZ,                -- 最近提醒时间
    status              TEXT NOT NULL DEFAULT 'pending', -- pending/operated/skipped
    operated_amount_cny DOUBLE PRECISION,          -- 实际操作金额(operated时)
    operated_rate_pct   DOUBLE PRECISION,          -- 实际操作利率(operated时)
    operated_date       DATE,                      -- 实际操作日期(operated时)
    skip_reason         TEXT,                      -- 跳过原因(skipped时)
    confirmed_at        TIMESTAMPTZ,               -- 确认时间
    confirmed_via       TEXT,                      -- "feishu" / "dashboard"
    feishu_event_id     TEXT,                      -- 飞书事件ID(幂等去重，DB 层原子)
    feishu_message_id   TEXT,                      -- 飞书消息ID(审计)
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 每周每品种唯一（防重复创建机会）
CREATE UNIQUE INDEX IF NOT EXISTS idx_repo_ops_week_code
    ON cnfi_repo_ops (opportunity_week, instrument_code);

-- 查待确认（提醒循环用）
CREATE INDEX IF NOT EXISTS idx_repo_ops_status
    ON cnfi_repo_ops (status, trigger_date);

-- 逆回购操作记录实际利率（向后兼容，已有行 NULL）
ALTER TABLE cnfi_actions ADD COLUMN IF NOT EXISTS rate_pct DOUBLE PRECISION;
