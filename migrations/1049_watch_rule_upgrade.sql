-- 1049_watch_rule_upgrade.sql —— watch_rule 盯盘域升级（STATE「watch_rule 盯盘域升级」票；
-- arb price_alerts 能力归并进 watch_rule：level 三级路由+message 自定义文案。
-- price_alerts 表=迁移 1003 显式裁废域，不复活；level 走既有 alerts 分级径）。
ALTER TABLE watch_rule
    ADD COLUMN IF NOT EXISTS level   TEXT NOT NULL DEFAULT 'warn',
    ADD COLUMN IF NOT EXISTS message TEXT NOT NULL DEFAULT '';
ALTER TABLE watch_rule DROP CONSTRAINT IF EXISTS watch_rule_level_check;
ALTER TABLE watch_rule ADD CONSTRAINT watch_rule_level_check
    CHECK (level IN ('info','warn','critical'));
