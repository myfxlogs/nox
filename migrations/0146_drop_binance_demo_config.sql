-- 0146 D-504 S3：删除 CEX demo 配置行——binance demofapi 行（cfg#25 demofws
-- 私有流唯一驱动源）。real 域行同 venue 独立存在不受影响；demofws 支路代码
-- 暂留（行删后无配置自然不起，代码收编另案）。
DELETE FROM exchange_configs
WHERE simulated = true AND venue = 'binance' AND base_url LIKE '%demofapi%';
