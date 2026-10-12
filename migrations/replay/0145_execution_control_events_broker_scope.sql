-- 0145 D-493 S5：execution_control_events.scope_type 域放宽——补 'broker'。
-- RemoveBroker 删除审计事件 scope_type='broker' + scope_key=venue（D-493 SSOT §S5）。
ALTER TABLE execution_control_events DROP CONSTRAINT IF EXISTS execution_control_events_scope_type_check;
ALTER TABLE execution_control_events ADD CONSTRAINT execution_control_events_scope_type_check
    CHECK (scope_type IN ('global','account','broker'));

COMMENT ON COLUMN execution_control_events.scope_type IS '事件域：global=总闸；account=账户级；broker=CFD 账号管理操作审计（D-493）';
