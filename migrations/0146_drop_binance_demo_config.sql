-- 0146 D-504 S3 修订（FK 挡删返修）：删除 CEX demo 配置行——binance demofapi 行
-- （cfg#25 demofws 私有流唯一驱动源）。income_events 外键引用 8 行 demo 市场
-- 噪声遥测（隔离测试网产物、非真钱审计、非持仓/paper 记录），随配置一并清理；
-- 先删依赖行再删配置行。real 域行同 venue 独立存在不受影响；demofws 支路代码
-- 暂留（行删后无配置自然不起，代码收编另案）。
DELETE FROM income_events
WHERE exchange_config_id IN (
  SELECT id FROM exchange_configs
  WHERE simulated = true AND venue = 'binance' AND base_url LIKE '%demofapi%');
DELETE FROM exchange_configs
WHERE simulated = true AND venue = 'binance' AND base_url LIKE '%demofapi%';
