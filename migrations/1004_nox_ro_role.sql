-- 1004_nox_ro_role.sql - M3 前置⑤ 补偿：鲜装 nox_ro 只读角色（D-241⑧）
-- 生产切流靠 ALTER ROLE arb_ro RENAME TO nox_ro 覆盖；鲜装库 baseline 起无件补造。
-- 语义合并 replay/0041（建角色+public 全表 SELECT）+0047（凭据列级收权），幂等可重放。
-- LOGIN+密码由部署时序设置（NOX_AI_PG_DSN 凭据在 app_secrets），migration 零凭据。

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nox_ro') THEN
        BEGIN
            CREATE ROLE nox_ro NOLOGIN;
        EXCEPTION WHEN duplicate_object OR unique_violation THEN
            NULL; -- 集簇级对象：并行测试库互撞——双查 pg_roles 同窗时 CREATE
                  -- 撞 pg_authid_rolname_index 出 23505（非 42710），两码同吞
        END;
    END IF;
END $$;

GRANT USAGE ON SCHEMA public TO nox_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO nox_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO nox_ro;

-- broker_accounts：收回全表 SELECT，授予非凭据列（0047 口径；platform/user_id
-- 于现行 schema 非实列已剔，后加列不补授——与生产 arb_ro 权限集对齐）
REVOKE SELECT ON TABLE broker_accounts FROM nox_ro;
GRANT SELECT (
    id, venue, host, port, server, alias, status, created_at, commission_bps, is_real, website
) ON broker_accounts TO nox_ro;

-- exchange_configs：收回全表 SELECT，授予非凭据列（同 0047 口径）
REVOKE SELECT ON TABLE exchange_configs FROM nox_ro;
GRANT SELECT (
    id, venue, venue_type, label, base_url, created_at, simulated, website
) ON exchange_configs TO nox_ro;
