-- funding_carry hh 输入源修（评估假费病灶 D-642 发现一证实）：perp_perp paper
-- exited 中位持有 3.0h=探针退出纪律非策略持有语义——持有窗判据落表。
-- 先验值 168h=7d：与系统既有 carry 计划地平同口径（NOX_HARVEST_HORIZON_DAYS
-- 缺省 7/收割比较地平）；realized 账本有数后以实代先（eval.go 三序取径）。
ALTER TABLE fin_opp_template
  ADD COLUMN IF NOT EXISTS hold_hours_prior numeric
  CHECK (hold_hours_prior IS NULL OR hold_hours_prior > 0);
UPDATE fin_opp_template SET hold_hours_prior = 168 WHERE id = 'funding_carry';
