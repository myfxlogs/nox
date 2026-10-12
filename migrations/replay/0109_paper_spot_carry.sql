-- D-350 阶段②：spot-carry 纸面验证轨（真实费率账，零执行风险）。
-- 开仓=扫描候选 net_spot_carry≥20%/yr 且持续性≥48h 且无同 symbol 活跃行（qty_usd 固定 $1000 虚拟）。
-- 累积=逐轮 last_funding_ann/100/365×间隔天×qty（雷达快照费率，诚实口径非逐结算值）。
-- 退出=|last_funding_ann|<10% 连续 2 轮（low_streak 计数）或满 7 天强评；不模拟价差损益。
-- 阶段③ demo 行复用本表（status='demo'，note 记双腿回执 order id）。
CREATE TABLE IF NOT EXISTS paper_spot_carry (
    id              BIGSERIAL PRIMARY KEY,
    venue           TEXT NOT NULL,
    symbol          TEXT NOT NULL,
    side            TEXT NOT NULL,              -- short_perp（正 funding 空永续收） / long_perp（负 funding 多永续收）
    entry_funding_ann NUMERIC NOT NULL,
    entry_ts        TIMESTAMPTZ NOT NULL DEFAULT now(),
    qty_usd         NUMERIC NOT NULL,
    last_funding_ann NUMERIC,
    accrued_usd     NUMERIC NOT NULL DEFAULT 0,
    last_accrue_ts  TIMESTAMPTZ,                -- 上次累积基准（间隔天计算锚；施工补列：设计列清单缺）
    low_streak      INT NOT NULL DEFAULT 0,     -- |funding|<10% 连续轮数（施工补列：退出双轮判据）
    exit_ts         TIMESTAMPTZ,
    exit_reason     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'active',  -- active/exited/demo
    note            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS paper_spot_carry_status_idx ON paper_spot_carry (status);
CREATE UNIQUE INDEX IF NOT EXISTS paper_spot_carry_active_uniq
    ON paper_spot_carry (venue, symbol) WHERE status IN ('active', 'demo');
