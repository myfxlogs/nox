-- 0122_broker_symbol_map_is_primary.sql - D-383 A2.1/A2.6：一 canonical 一主品种。
-- is_primary 列 + 部分唯一索引（DB 层强制不变量）；单行组固化为显式 primary（A8：
-- rule 复活变体后组变多行仍有指定行，canonical 不断流）；27 组种子按 A2.6 全量人工指定
--（幂等，重复执行安全）。号段注记：0121 已被在审分支 seat/zcode-d383b
--（0121_venue_account_snapshot_src）占用，本票取 0122 防并行撞号
--（KB shared-testdb-migration-number-collision）。
ALTER TABLE broker_symbol_map ADD COLUMN IF NOT EXISTS is_primary BOOLEAN NOT NULL DEFAULT false;

-- 单行组固化（恰一行 → true；canonical<>'' 同索引域）
UPDATE broker_symbol_map t SET is_primary = true
WHERE t.canonical <> '' AND NOT EXISTS (
	SELECT 1 FROM broker_symbol_map o
	WHERE o.venue = t.venue AND o.canonical = t.canonical AND o.broker_symbol <> t.broker_symbol);

-- A2.6 种子表（27 组全量；键=(venue, broker_symbol, canonical) 跨环境稳定，不依赖 id）
UPDATE broker_symbol_map SET is_primary = true WHERE venue='1279967'   AND broker_symbol='BTCUSDT'  AND canonical='BTCUSDT';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='1314615'   AND broker_symbol='BTCUSDT'  AND canonical='BTCUSDT';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='277842155' AND broker_symbol='BTCUSDTm' AND canonical='BTCUSDT';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='277842155' AND broker_symbol='USTECm'   AND canonical='NAS100';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='277842155' AND broker_symbol='US500m'   AND canonical='SPX500';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='277842155' AND broker_symbol='US30m'    AND canonical='US30';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='277842155' AND broker_symbol='XAUUSDm'  AND canonical='XAUUSD';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='3001157'   AND broker_symbol='BTCUSDTm' AND canonical='BTCUSDT';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='3001157'   AND broker_symbol='USTECm'   AND canonical='NAS100';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='3001157'   AND broker_symbol='US500m'   AND canonical='SPX500';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='3001157'   AND broker_symbol='US30m'    AND canonical='US30';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='3001157'   AND broker_symbol='XAUUSDm'  AND canonical='XAUUSD';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='39050818'  AND broker_symbol='UKOIL'    AND canonical='BRENT';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='39050818'  AND broker_symbol='USOIL'    AND canonical='WTI';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='39050818'  AND broker_symbol='GOLD'     AND canonical='XAUUSD';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='52993526'  AND broker_symbol='XBRUSD'   AND canonical='BRENT';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='52993526'  AND broker_symbol='XTIUSD'   AND canonical='WTI';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='15032313'  AND broker_symbol='XBRUSD'   AND canonical='BRENT';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='15032313'  AND broker_symbol='XTIUSD'   AND canonical='WTI';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='591959217' AND broker_symbol='SILVER'   AND canonical='SILVER';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='591959217' AND broker_symbol='WTI'      AND canonical='WTI';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='591959217' AND broker_symbol='GOLD'     AND canonical='XAUUSD';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='59892915'  AND broker_symbol='BTCUSDTm' AND canonical='BTCUSDT';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='59892915'  AND broker_symbol='USTECm'   AND canonical='NAS100';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='59892915'  AND broker_symbol='US500m'   AND canonical='SPX500';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='59892915'  AND broker_symbol='US30m'    AND canonical='US30';
UPDATE broker_symbol_map SET is_primary = true WHERE venue='59892915'  AND broker_symbol='XAUUSDm'  AND canonical='XAUUSD';

-- 部分唯一索引：一 (venue, canonical<>'') 至多一 primary（空 canonical meta 行不受约束，审计 R4）
CREATE UNIQUE INDEX IF NOT EXISTS broker_symbol_map_primary_uniq
	ON broker_symbol_map (venue, canonical) WHERE is_primary AND canonical <> '';
