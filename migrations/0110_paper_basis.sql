-- 0110 D-354 S1/S2：纸面仓盈亏视图——基差采集与纸面行两列。
-- fin_cex_universe.last_basis_bps：radar 每轮落 (mark/index−1)×1e4（round 1 位；
-- mark/index 非正数→NULL fail-closed；okx 无 mark/index 面诚实 NULL）。
ALTER TABLE fin_cex_universe ADD COLUMN IF NOT EXISTS last_basis_bps NUMERIC;
-- paper_spot_carry：开仓时快照 uni 当轮 last_basis（NULL 传播）+ StepPaperSpotCarry 每轮更新 last。
ALTER TABLE paper_spot_carry ADD COLUMN IF NOT EXISTS entry_basis_bps NUMERIC;
ALTER TABLE paper_spot_carry ADD COLUMN IF NOT EXISTS last_basis_bps NUMERIC;
