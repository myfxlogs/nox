-- 0114_cex_subaccount_seeds.sql - D-363 S1：CEX 资金（funding）/理财（earn）子账户 config 种子行。
-- 核证点①（9-20 实证）：exchange_configs.venue_type 无 CHECK 约束/枚举（pg_constraint 空）——词表自由扩展。
-- 凭据 SELECT 复制同 venue spot 真配置行（不新录密钥）；NOT EXISTS 幂等防重跑重复；
-- 模拟行不种（D-360 real 过滤会丢，种了也白种）。binance earn 不种（核证点② locked 零仓省略）。
INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, website)
  SELECT 'okx', 'funding', 'funding', api_key, api_secret, passphrase, base_url, simulated, website
    FROM exchange_configs
   WHERE venue = 'okx' AND venue_type = 'spot' AND NOT simulated
     AND NOT EXISTS (SELECT 1 FROM exchange_configs WHERE venue = 'okx' AND venue_type = 'funding');

INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, website)
  SELECT 'okx', 'earn', 'earn', api_key, api_secret, passphrase, base_url, simulated, website
    FROM exchange_configs
   WHERE venue = 'okx' AND venue_type = 'spot' AND NOT simulated
     AND NOT EXISTS (SELECT 1 FROM exchange_configs WHERE venue = 'okx' AND venue_type = 'earn');

INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, website)
  SELECT 'binance', 'funding', 'funding', api_key, api_secret, passphrase, base_url, simulated, website
    FROM exchange_configs
   WHERE venue = 'binance' AND venue_type = 'spot' AND NOT simulated
     AND NOT EXISTS (SELECT 1 FROM exchange_configs WHERE venue = 'binance' AND venue_type = 'funding');
