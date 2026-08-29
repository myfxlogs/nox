-- 0045_broker_accounts_is_real.sql - 账户实盘/demo 标记（D-144 ① 三态返修）
-- 首个实盘账户（REDACTED_MT_ACCT）入池触发——组合引擎不区分 demo/实盘，随时可能开出
-- 「实盘多×demo 空」的假对冲仓（demo 腿盈亏非真钱，基差风险全在真金侧）。
-- is_real 系统字段取代 alias 命名约定（ICR 前缀）作为业主标注实盘的方式。
-- 三态：true=实盘, false=demo, NULL=未知（默认 NULL，存量行兼容；组合引擎对未知不配对）。
-- REDACTED_MT_ACCT 由 Claude 部署后 SQL 置 true。
-- 返修：原 0045 为 NOT NULL DEFAULT false，此版改为 nullable（DROP NOT NULL + DROP DEFAULT）。

ALTER TABLE broker_accounts ADD COLUMN IF NOT EXISTS is_real boolean;
ALTER TABLE broker_accounts ALTER COLUMN is_real DROP NOT NULL;
ALTER TABLE broker_accounts ALTER COLUMN is_real DROP DEFAULT;
