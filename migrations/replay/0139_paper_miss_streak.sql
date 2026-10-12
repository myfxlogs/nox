-- 0139_paper_miss_streak.sql - D-474a S4：纸面行缺席轮次独立计数列。
-- miss 语义分家：缺席轮不再计入 low_streak（normalized 判据），独立 miss_streak
-- 连续缺席达阈 → exit_reason='data_lost'（失联截断证据不得毕业）。零新表。
ALTER TABLE paper_spot_carry ADD COLUMN IF NOT EXISTS miss_streak INTEGER NOT NULL DEFAULT 0;

-- D-474a S1 存量行回迁哨兵：active/demo 行 last_accrue_ts 置 NULL 标记
-- 「待按结算边界重算」（该列原语义=末次墙钟记账时刻，新语义=已结算水位）。
-- 运行时遇到 NULL → 以 entry_ts 为基重算全生命周期：覆盖全 → accrued_usd 直接替换；
-- 覆盖不全 → 保留旧 accrued 值、last_accrue_ts 保持 NULL 下轮重试（SSOT「不全则留」）。
-- exited 行不动（冻值）。
UPDATE paper_spot_carry SET last_accrue_ts = NULL
  WHERE status IN ('active', 'demo') AND last_accrue_ts IS NOT NULL;
