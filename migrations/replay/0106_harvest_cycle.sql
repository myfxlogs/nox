-- D-341 S1/S3/S4/S5：带内循环收割出场体系。
-- S1 收敛止盈：monitor 收割线列（band 统计所有者=monitor 工具，q25+MIN_CAPTURE 下限，样本<48 NULL）
-- + positions 开仓学习样本锚两列（D-327 同位写点落值）。
-- S5 价差史深回填：src 列区分 live/backfill（Exness ticks 保留窗诚实边界）。
-- S3 周期样本：positions.exit_kind（harvest/stop/carry_death/manual；logCloseTrigger 单写点 reason 映射）。
-- S4 双轨验证视图：estimated cashflow（real facts）×income_events（demo sim 费率）per-cycle 对照。
ALTER TABLE fin_xasset_monitor ADD COLUMN IF NOT EXISTS harvest_floor_bps NUMERIC;
ALTER TABLE fin_xasset_monitor ADD COLUMN IF NOT EXISTS src TEXT NOT NULL DEFAULT 'live';
ALTER TABLE positions ADD COLUMN IF NOT EXISTS entry_noise_bps NUMERIC;
ALTER TABLE positions ADD COLUMN IF NOT EXISTS entry_harvest_floor_bps NUMERIC;
ALTER TABLE positions ADD COLUMN IF NOT EXISTS exit_kind TEXT;

CREATE OR REPLACE VIEW v_cycle_realrate AS
SELECT p.id AS position_id,
       p.opportunity_id,
       p.status,
       p.opened_at,
       p.closed_at,
       p.realized_pnl,
       p.exit_kind,
       COALESCE(SUM(cf.amount_usd) FILTER (WHERE cf.kind IN ('swap','funding')), 0) AS estimated_carry_usd,
       COALESCE((SELECT SUM(ie.amount_usd) FROM income_events ie
                 WHERE ie.position_id = p.id), 0) AS simulated_carry_usd,
       p.realized_pnl - COALESCE((SELECT SUM(ie.amount_usd) FROM income_events ie
                 WHERE ie.position_id = p.id), 0) AS cycle_convergence_usd
FROM positions p
LEFT JOIN position_cashflows cf ON cf.position_id = p.id AND cf.leg_no IS NOT NULL
GROUP BY p.id;
