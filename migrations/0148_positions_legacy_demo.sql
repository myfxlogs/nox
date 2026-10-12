-- 0148_positions_legacy_demo.sql —— D-512 T8：旧仓数据治理（A 项并票）。
-- environment NULL 行（实拍 476）标 legacy_demo——8-30 审计证据链物理保留（纯 UPDATE 零删除），
-- 经验库与交易数据物理隔离、决策链不读 knowledge_entries；聚合闸（ActiveNotionalByEnv /
-- AggregateStrategyStatsByEnvironment / DailyRealizedPnlByEnvironment）不再把 NULL 静默当 live。
UPDATE positions SET environment = 'legacy_demo' WHERE environment IS NULL;
