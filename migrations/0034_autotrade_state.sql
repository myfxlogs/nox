-- D-072: 执行层收口——状态机表 + positions type 约束重建 + 历史回填

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
ALTER TABLE positions ADD CONSTRAINT positions_type_check CHECK (type IN
  ('CROSS_VENUE','CARRY','FUNDING_HEDGE','PERP_FUNDING','SWAPARB','MEANREVERT'));

-- 3. 按 oppID 前缀回填 type（含 CLOSED，使统计可回填）
UPDATE positions SET type='SWAPARB'    WHERE opportunity_id LIKE 'SWAPARB:%';
UPDATE positions SET type='MEANREVERT' WHERE opportunity_id LIKE 'MEANREVERT:%';

-- 4. strategy_stats 历史聚合回填（D-072 §4.3）
INSERT INTO strategy_stats (strategy, total_trades, wins, losses, total_pnl, avg_pnl, win_rate, updated_at)
SELECT type, COUNT(*), SUM((realized_pnl>0)::int), SUM((realized_pnl<=0)::int),
       SUM(realized_pnl), AVG(realized_pnl), AVG((realized_pnl>0)::float8), now()
FROM positions WHERE status='CLOSED' AND type<>'' GROUP BY type
ON CONFLICT (strategy) DO NOTHING;
