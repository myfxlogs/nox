-- 1030_perf_baseline.sql - D-538：业绩基线锚落表+历史序列归档标记。
-- 基线=业主令 10-15 实拍 $11,317（USDT+USD 口径）；历史业绩行不删不改值，
-- 仅标 archived=true 不参与基线后指标（读面谓词 archived=false）。
ALTER TABLE fin_nav_daily ADD COLUMN IF NOT EXISTS archived BOOLEAN NOT NULL DEFAULT false;
UPDATE fin_nav_daily SET archived=true WHERE day_start < '2026-10-15'::timestamptz;
INSERT INTO app_config (key, value, note) VALUES
('perf_baseline_usd', '11317.00', 'D-538 业绩基线锚（业主令 10-15 实拍；USDT+USD 池合计口径；改值须新 D#）'),
('perf_baseline_at', '2026-10-15T00:00:00Z', 'D-538 基线锚时点（UTC）')
ON CONFLICT (key) DO NOTHING;
