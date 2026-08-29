-- 0045_broker_accounts_is_real.sql - 账户实盘/demo 标记（D-144 ①）
-- 首个实盘账户（REDACTED_MT_ACCT）入池触发——组合引擎不区分 demo/实盘，随时可能开出
-- 「实盘多×demo 空」的假对冲仓（demo 腿盈亏非真钱，基差风险全在真金侧）。
-- is_real 系统字段取代 alias 命名约定（ICR 前缀）作为业主标注实盘的方式。
-- 默认 false（demo），存量行兼容；REDACTED_MT_ACCT 由 Claude 部署后 SQL 置 true。

ALTER TABLE broker_accounts ADD COLUMN IF NOT EXISTS is_real boolean NOT NULL DEFAULT false;
