-- D-152 fixup：facts (kind,ts) 索引——pruneFactsKind 候选子查询（kind=? AND ts<cutoff）
-- 原只能靠 (kind,venue,symbol,ts) 索引全扫 kind 内全部行（funding 220 万行实测单批 15min+，
-- 日轮 janitor 每天白扫）；本索引让超窗候选范围寻址=秒级。普通 CREATE INDEX（迁移事务内
-- 禁 CONCURRENTLY），一次性建索引窗口内 facts 写入短暂排队属可接受部署代价。
CREATE INDEX IF NOT EXISTS facts_kind_ts_idx ON facts (kind, ts);
