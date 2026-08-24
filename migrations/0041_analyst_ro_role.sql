-- 0041_analyst_ro_role.sql - 前端分析员只读角色（D-091）
-- query_ro 工具用 arb_ro 角色连接，仅授予 public schema 表的 SELECT；
-- 不授予 INSERT/UPDATE/DELETE/DDL，确保 AI 工具循环不可写。
-- 幂等：DO 块内判断角色是否存在再创建，密码可重复 ALTER 不报错。

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'arb_ro') THEN
        CREATE ROLE arb_ro LOGIN PASSWORD 'arb_ro';
    END IF;
END $$;

ALTER ROLE arb_ro WITH PASSWORD 'arb_ro';

-- 授予所有现有及未来 public schema 表的 SELECT（D-091 query_ro 安全护栏）。
GRANT USAGE ON SCHEMA public TO arb_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO arb_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT TO arb_ro;
