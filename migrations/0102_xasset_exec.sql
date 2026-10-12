-- D-311 S1：XASSET_CARRY 执行通路解锁——positions_type_check 六值→七值 + fin_cex_universe.ct_val
-- 对照 0034 约束建法；幂等重放安全（DROP CONSTRAINT IF EXISTS + ADD COLUMN IF NOT EXISTS）。
ALTER TABLE positions DROP CONSTRAINT IF EXISTS positions_type_check;
ALTER TABLE positions ADD CONSTRAINT positions_type_check CHECK (type IN
  ('CROSS_VENUE','CARRY','FUNDING_HEDGE','PERP_FUNDING','SWAPARB','MEANREVERT','XASSET_CARRY'));

-- CEX 合约面值（1 张合约代表的基础资产数）：binance=1.0（qty=base）、okx=ctVal、gate=quanto_multiplier。
-- 历史行留 NULL，雷达下轮回填；NULL/≤0 → legContractSizeOf fail-closed（不猜合约规格）。
ALTER TABLE fin_cex_universe ADD COLUMN IF NOT EXISTS ct_val numeric;
