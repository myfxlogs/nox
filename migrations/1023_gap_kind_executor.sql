-- 1023_gap_kind_executor.sql —— okx idle 资金扫描缺口（STATE 行）：
-- fin_condition_gap kind 放 'executor'（venue 执行径缺位类缺口——如实记不臆造 API）。
ALTER TABLE fin_condition_gap
  DROP CONSTRAINT IF EXISTS fin_condition_gap_kind_check;
ALTER TABLE fin_condition_gap
  ADD CONSTRAINT fin_condition_gap_kind_check
  CHECK (kind IN ('credential', 'permission', 'capital', 'constraint', 'holding', 'transfer', 'executor'))
  NOT VALID;
