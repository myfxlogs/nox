-- 0037_verification_stats: D-086 机会验证阶梯——验证统计表
-- 设计 SSOT: docs/机会验证阶梯设计.md
--
-- vkey = "{type}:{venue_buy}:{venue_sell}"（venue 含 :spot/:perp/:mt5 段，与 opportunities 列同构）
-- 平仓时增量 UPSERT（挂点与 UpdateStrategyStat 同位点）：
--   样本纳入条件 = realized_pnl IS NOT NULL 且 expected_usd > 0（负期望开仓=事故不入统计）
--   UPSERT: samples+1 / expected_usd+Δ / realized_usd+Δ / last_sample_at=closed_at
-- 时效：查询时 last_sample_at > now()-W 过滤（不物理删行）

CREATE TABLE verification_stats (
  vkey           text PRIMARY KEY,
  samples        integer NOT NULL DEFAULT 0,
  expected_usd   numeric NOT NULL DEFAULT 0,
  realized_usd   numeric NOT NULL DEFAULT 0,
  last_sample_at timestamptz,
  updated_at     timestamptz NOT NULL DEFAULT now()
);
