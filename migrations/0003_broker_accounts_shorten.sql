-- 0003_broker_accounts_shorten.sql - CFD venue 短化 + company 移除（D-033）
-- 业主决定：CFD 账号标识宁短勿长，venue 从 `{server}-{user}` 改为登录账号号（user_id）。
-- venue 是 ARB 内部路由键（Quote.Venue / 执行器键 / 符号映射），存量数据一并迁移。

-- 1. 符号映射 venue 先同步短账号号（先于账号表变更，用旧 venue 关联）。
--    跨经纪商同账号号导致目标碰撞时，此步同样可能冲突——部署为个人单机，忽略极端场景。
UPDATE broker_symbol_map m
SET venue = a.user_id::text
FROM broker_accounts a
WHERE m.venue = a.venue AND a.venue <> a.user_id::text;

-- 2. 账号 venue 短化。防御性防碰撞：目标 venue 已被其他行占用则跳过（保留旧长格式，不静默合并）。
UPDATE broker_accounts a
SET venue = a.user_id::text
WHERE a.venue <> a.user_id::text
  AND NOT EXISTS (SELECT 1 FROM broker_accounts x WHERE x.venue = a.user_id::text AND x.id <> a.id);

-- 3. company 列移除（业主决定不再展示）。
ALTER TABLE broker_accounts DROP COLUMN IF EXISTS company;
