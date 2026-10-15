-- 1031_snapshot_key_ts_idx.sql —— ExplorerIdleFunds「每键最新行」读径提速：
-- 原 DISTINCT ON 全表序实测 SG 60.8s（834k 行 seq scan + 磁盘外排 36MB），
-- 总览/ComputeNav/日报同源受害。复合覆盖索引支撑读侧「键枚举 + 逐键
-- LATERAL 探针」：distinct 键枚举走 index-only，每键取行=索引单点下探。
-- 迁移事务内禁 CONCURRENTLY（0185 先例注记）——一次建索引窗口写短暂排队
-- 属可接受部署代价。
CREATE INDEX IF NOT EXISTS fin_venue_account_snapshot_key_ts_idx
	ON fin_venue_account_snapshot (venue, venue_type, currency, fetched_at DESC)
	INCLUDE (src, balance_raw, equity_raw, free_margin_raw, buffer_multiple);
