-- 1013_binance_earn_reseed.sql —— 财务可见性两件套①：binance:earn 子账户配置行回种。
-- D-133 摘除令的历史约束「理财面只统计 USDT 不可达」已被新口径解除——本票改按
-- 「各币种快照行分开落、零换算（D-285）」采集 flexible position，币种枚举可达。
-- 凭据=库内 binance real spot 行复制（0182 同式；NOT EXISTS 幂等）。
INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, website)
  SELECT 'binance', 'earn', 'earn', api_key, api_secret, passphrase, base_url, simulated, website
    FROM exchange_configs
   WHERE venue = 'binance' AND venue_type = 'spot' AND NOT simulated
     AND NOT EXISTS (SELECT 1 FROM exchange_configs WHERE venue = 'binance' AND venue_type = 'earn');
