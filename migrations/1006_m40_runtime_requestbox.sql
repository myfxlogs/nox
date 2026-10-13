-- D-246/D-247 M4.0：运行面降噪 + 请求箱分流迁移。
-- ① fin_employee_registry：清未登记心跳落行与已剔 daemon 族残行，部门标签收七值。
DELETE FROM fin_employee_registry WHERE role = '未登记';
DELETE FROM fin_employee_registry
  WHERE name LIKE 'cmc-ws:%'
     OR name LIKE 'wire_cmc:%'
     OR name LIKE 'price_alert%'
     OR name LIKE 'main:intra-%';
UPDATE fin_employee_registry SET dept = '研究' WHERE dept = '运营';
UPDATE fin_employee_registry SET dept = '账本' WHERE dept = '复盘';
UPDATE fin_employee_registry SET dept = '平台' WHERE dept IN ('IR', '运维', '合规');

-- ② fin_condition_gap：存量 runtime 请求转 waived（D-247 转票，运行时故障归告警不走请求箱），
-- kind 收白名单（credential/permission/capital/constraint/holding）。
UPDATE fin_condition_gap
  SET status = 'waived', resolve_note = 'D-247 转票', resolved_at = COALESCE(resolved_at, now())
  WHERE kind = 'runtime' AND status IN ('open', 'requested');
-- #10（权益 ≥10000 USD=整基金请求）可行性预检不过→收口转休眠，模板侧由预检落 dormant 约束行。
UPDATE fin_condition_gap
  SET status = 'waived', resolve_note = 'D-247 预检休眠（可行性不过）', resolved_at = COALESCE(resolved_at, now())
  WHERE id = 10 AND status IN ('open', 'requested');
-- baseline 已含同名约束（旧口径）→ 幂等口径：先收再放，NOT VALID 免存量行校验。
ALTER TABLE fin_condition_gap
  DROP CONSTRAINT IF EXISTS fin_condition_gap_kind_check;
ALTER TABLE fin_condition_gap
  ADD CONSTRAINT fin_condition_gap_kind_check
  CHECK (kind IN ('credential', 'permission', 'capital', 'constraint', 'holding'))
  NOT VALID;
