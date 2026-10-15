-- 0041_analyst_ro_role.sql - 前端分析员只读角色（D-091）
-- query_ro 工具用 arb_ro 角色连接，仅授予 public schema 表的 SELECT；
-- 不授予 INSERT/UPDATE/DELETE/DDL，确保 AI 工具循环不可写。
-- LOGIN+密码由部署时序设置（复审方，密码来自 .env ARB_AI_PG_DSN），migration 零凭据。
-- 幂等：DO 块内判断角色是否存在再创建。

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'arb_ro') THEN
        BEGIN
            CREATE ROLE arb_ro NOLOGIN;
        EXCEPTION WHEN duplicate_object THEN
            NULL; -- 集簇级对象：并行测试库互撞时后者如实跳过
        END;
    END IF;
END $$;

-- 授予所有现有及未来 public schema 表的 SELECT（D-091 query_ro 安全护栏）。
GRANT USAGE ON SCHEMA public TO arb_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO arb_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO arb_ro;
