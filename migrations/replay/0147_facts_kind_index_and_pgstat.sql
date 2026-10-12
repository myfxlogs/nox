-- 0147 D-508 S3 facts 表治理：
-- ① 新覆盖索引 (kind, venue, symbol, ts DESC) 供 LatestFactsByKind / venue 等值谓词命中；
--    facts_kind_symbol_ts_idx 被新索引吸收（kind 前缀同序），删除收敛。
--    venue/venue_key 两枚前缀索引（0142 建）本票暂留，观察一期再裁。
-- ② pg_stat_statements 扩展——慢查询取证基建（compose shared_preload_libraries 随 S4 部署面同批）。
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
CREATE INDEX IF NOT EXISTS facts_kind_venue_symbol_ts_desc_idx ON facts (kind, venue, symbol, ts DESC);
DROP INDEX IF EXISTS facts_kind_symbol_ts_idx;
