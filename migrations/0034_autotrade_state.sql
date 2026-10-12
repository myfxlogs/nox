-- D-072: 执行层收口——状态机表 + positions type 约束重建 + 历史回填
-- 实施修正④（2026-08-21 部署热修）：回填必须先于 ADD CONSTRAINT（首次部署 23514 实证）；
-- 空 type 行按 opportunity_id 首段数据驱动回填，无前缀者 CROSS_VENUE 兜底。

-- 1. autotrade_state 状态机单行表（D-072 §3.2）
CREATE TABLE IF NOT EXISTS autotrade_state (
    state       TEXT PRIMARY KEY,
    reason      TEXT NOT NULL DEFAULT '',
    halted_on   DATE,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO autotrade_state (state) VALUES ('RUNNING') ON CONFLICT DO NOTHING;

-- 2. positions type 约束重建（D-072 §4.1）
-- 现为 0001_init.sql:70 内联无名 CHECK（三值），不能按名 DROP
DO $$ DECLARE c text; BEGIN
  FOR c IN SELECT conname FROM pg_constraint
    WHERE conrelid = 'positions'::regclass AND contype = 'c'
      AND pg_get_constraintdef(oid) ILIKE '%type%'
  LOOP EXECUTE format('ALTER TABLE positions DROP CONSTRAINT %I', c); END LOOP;
END $$;

-- 3. 回填（必须先于 ADD CONSTRAINT：约束对全表存量行生效）
-- 3a. 按 oppID 前缀回填 SWAPARB/MEANREVERT（含 CLOSED，使统计可回填）
UPDATE positions SET type='SWAPARB'    WHERE opportunity_id LIKE 'SWAPARB:%';
UPDATE positions SET type='MEANREVERT' WHERE opportunity_id LIKE 'MEANREVERT:%';
-- 3b. 空 type 行按 opportunity_id 首段数据驱动回填（实施修正④：存量 type='' 使 ADD 约束 23514）
UPDATE positions SET type = split_part(opportunity_id, ':', 1)
  WHERE COALESCE(type, '') = '' AND COALESCE(opportunity_id, '') <> '';
-- 3c. 兜底：仍无 type（无 oppID 或前缀异常）→ 历史默认 CROSS_VENUE
UPDATE positions SET type = 'CROSS_VENUE'
  WHERE COALESCE(type, '') = '' OR type IS NULL;

-- 4. 六值约束（此刻全表 type 已在枚举内）
ALTER TABLE positions ADD CONSTRAINT positions_type_check CHECK (type IN
  ('CROSS_VENUE','CARRY','FUNDING_HEDGE','PERP_FUNDING','SWAPARB','MEANREVERT'));

-- 5. strategy_stats 历史聚合回填（D-072 §4.3；实施修正④：AVG(bool::float8) 在 PG 为 42846 非法 cast，改 CASE）
INSERT INTO strategy_stats (strategy, total_trades, wins, losses, total_pnl, avg_pnl, win_rate, updated_at)
SELECT type, COUNT(*),
       SUM(CASE WHEN realized_pnl > 0 THEN 1 ELSE 0 END),
       SUM(CASE WHEN realized_pnl <= 0 THEN 1 ELSE 0 END),
       SUM(realized_pnl), AVG(realized_pnl),
       AVG(CASE WHEN realized_pnl > 0 THEN 1.0 ELSE 0.0 END), now()
FROM positions WHERE status='CLOSED' AND type<>'' GROUP BY type
ON CONFLICT (strategy) DO NOTHING;
