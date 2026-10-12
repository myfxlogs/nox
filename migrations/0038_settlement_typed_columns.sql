-- 0038_settlement_typed_columns: D-087 结算与单位统一
-- 设计 SSOT: docs/结算与单位统一-D-087设计.md §3.2/§3.4
--
-- 三件事：
--   1. position_settlements 表（结算幂等钩子，settlement.Settle 写入）
--   2. positions 两 typed 列（entry_spread / swap_bps_daily，字段语义分离）
--   3. SWAPARB 存量挪列（expected_net_bps 的入场 spread 挪入 entry_spread，net_bps 归 NULL 收敛型专用）

-- 1. position_settlements 表
CREATE TABLE IF NOT EXISTS position_settlements (
  position_id    bigint PRIMARY KEY REFERENCES positions(id),
  vkey           text,
  expected_usd   double precision,
  realized_usd   double precision,
  notional_usd   double precision,
  hold_days      double precision,
  skipped_reason text,
  settled_at     timestamptz NOT NULL DEFAULT now()
);

-- 2. positions 两 typed 列
ALTER TABLE positions ADD COLUMN IF NOT EXISTS expected_entry_spread_bps double precision;
ALTER TABLE positions ADD COLUMN IF NOT EXISTS expected_swap_bps_daily   double precision;
-- D-087 §3.4：expected_net_bps 改 NULLABLE（SWAPARB 挪至 typed 列后该列不再适用，收敛型仍用此列）
ALTER TABLE positions ALTER COLUMN expected_net_bps DROP NOT NULL;

-- 3. SWAPARB 存量挪列：入场 spread → entry_spread，net_bps 归 NULL（收敛型专用）
UPDATE positions SET expected_entry_spread_bps = expected_net_bps, expected_net_bps = NULL
WHERE type = 'SWAPARB' AND expected_net_bps IS NOT NULL;
