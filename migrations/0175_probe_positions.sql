-- 0175_probe_positions.sql D-047①：系统探针仓 PROBE 单腿路径——
-- positions +decision_ref（决策席批准引用 D#）+auto_close_at（到期自动平仓时刻）；
-- positions_type_check 七值→八值（+'PROBE'）。
-- DROP+ADD CONSTRAINT 与 ADD COLUMN IF NOT EXISTS 幂等可重放。
ALTER TABLE positions ADD COLUMN IF NOT EXISTS decision_ref text;
ALTER TABLE positions ADD COLUMN IF NOT EXISTS auto_close_at timestamptz;

ALTER TABLE positions DROP CONSTRAINT IF EXISTS positions_type_check;
ALTER TABLE positions ADD CONSTRAINT positions_type_check CHECK (type IN
  ('CROSS_VENUE','CARRY','FUNDING_HEDGE','PERP_FUNDING','SWAPARB','MEANREVERT','XASSET_CARRY','PROBE'));

-- 到期唤醒索引：posMon 查 type='PROBE' 且 OPEN 且 auto_close_at 到点。
CREATE INDEX IF NOT EXISTS idx_positions_probe_due
    ON positions (auto_close_at)
    WHERE type = 'PROBE' AND status IN ('OPEN','LEGGED');
