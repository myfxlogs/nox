-- 0176_probe_llm_loop.sql —— D-048：系统 LLM 探针闭环提案/审批面。
-- explorer_proposals：scan_kind + 'probe'；status + proposed/approved/running/closed/rejected；
-- probe JSONB（提案契约：purpose/falsify/venue_key/side/qty/notional/expected_cost/auto_close_at/basis）；
-- decision_ref（批准 D#，positions.decision_ref 同源透传）；probe_position_id（批准开单后关联仓）。
-- DROP+ADD CONSTRAINT 与 ADD COLUMN IF NOT EXISTS 幂等可重放。
ALTER TABLE explorer_proposals DROP CONSTRAINT IF EXISTS explorer_proposals_scan_kind_check;
ALTER TABLE explorer_proposals ADD CONSTRAINT explorer_proposals_scan_kind_check
  CHECK (scan_kind IN ('daily','weekly','probe'));
ALTER TABLE explorer_proposals DROP CONSTRAINT IF EXISTS explorer_proposals_status_check;
ALTER TABLE explorer_proposals ADD CONSTRAINT explorer_proposals_status_check
  CHECK (status IN ('new','submitted','admitted','denied','expired','proposed','approved','running','closed','rejected'));
ALTER TABLE explorer_proposals ADD COLUMN IF NOT EXISTS probe jsonb;
ALTER TABLE explorer_proposals ADD COLUMN IF NOT EXISTS decision_ref text;
ALTER TABLE explorer_proposals ADD COLUMN IF NOT EXISTS probe_position_id bigint;

COMMENT ON COLUMN explorer_proposals.probe IS 'D-048 探针契约 JSONB（scan_kind=probe 时必填；缺任一字段校验层拒入库）';
COMMENT ON COLUMN explorer_proposals.decision_ref IS 'D-048 批准引用 D#（ApproveProbe 时写入）';
COMMENT ON COLUMN explorer_proposals.probe_position_id IS 'D-048 批准开单后关联的 PROBE 仓 id';
