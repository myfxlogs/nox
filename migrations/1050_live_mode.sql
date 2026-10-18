-- 1050_live_mode.sql —— D-620：LIVE 授权链断层修。
-- ①execution_controls mode 枚举放开 'LIVE'（唯一真金导出位）。
-- ②NOX_LIVE_TRADE env 键退役（app_config 行删——布尔闸语义并入 live_gates 授权记录）。
-- ③live_gates 全量唯一约束改「仅 active 行唯一」部分唯一索引——
--   原约束含 revoked 行，kill/降级撤权后再 Grant 必撞键=撤权后永不可续权。

ALTER TABLE execution_controls DROP CONSTRAINT IF EXISTS execution_controls_mode_check;
ALTER TABLE execution_controls ADD CONSTRAINT execution_controls_mode_check
    CHECK (mode = ANY (ARRAY['READ_ONLY'::text, 'PAPER'::text, 'TEST'::text, 'LIVE'::text]));

DELETE FROM app_config WHERE key = 'NOX_LIVE_TRADE';

ALTER TABLE live_gates DROP CONSTRAINT IF EXISTS live_gates_scope_type_scope_key_account_key_strategy_key;
DROP INDEX IF EXISTS live_gates_active_idx;
CREATE UNIQUE INDEX IF NOT EXISTS live_gates_active_uniq
    ON live_gates (scope_type, scope_key, account_key, strategy)
    WHERE revoked_at IS NULL;
