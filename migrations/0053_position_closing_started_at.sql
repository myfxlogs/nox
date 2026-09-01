-- D-189 P0-4：positions 加 closing_started_at 列（CLOSING 真实起始时间）。
-- 语义：进入 CLOSING 时写入 now()；reconcile 超时判定改用此列（不再用 opened_at）。
-- NULL 允许（OPEN/OPENING/LEGGED/CLOSED/FAILED 无此值）；CLOSING 但 NULL=异常 fail-closed。
-- 存量回填：现有 CLOSING 行补 now()，避免部署瞬间误重试。

ALTER TABLE positions ADD COLUMN IF NOT EXISTS closing_started_at TIMESTAMPTZ;

UPDATE positions
SET closing_started_at = now()
WHERE status = 'CLOSING' AND closing_started_at IS NULL;
