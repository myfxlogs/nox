-- 0044_broker_symbol_map_source.sql - 映射来源标记（D-140 S1）
-- source 列标记每行映射的来源：
--   manual = 人工录入（默认，存量行）
--   rule   = 归一规则库自动补录（canonical 归一即生效，CS 留 NULL=CS 门继续拦）
--   meta   = MT5 元数据采集自动 upsert（D-140 S2，仅填 CS IS NULL 的行）
-- 审计+一键回滚：DELETE WHERE source='rule' 撤销全部规则自动补录。

ALTER TABLE broker_symbol_map ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'manual';
