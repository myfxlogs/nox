-- 1017_gap_kind_transfer.sql —— 跨 venue 机会差调拨提案（业主令 10-14）：
-- fin_condition_gap kind 放 'transfer'（业主域划转请求类缺口）。NOT VALID 免存量校验。
ALTER TABLE fin_condition_gap
  DROP CONSTRAINT IF EXISTS fin_condition_gap_kind_check;
ALTER TABLE fin_condition_gap
  ADD CONSTRAINT fin_condition_gap_kind_check
  CHECK (kind IN ('credential', 'permission', 'capital', 'constraint', 'holding', 'transfer'))
  NOT VALID;
