-- 0182_binance_earn_seed.sql - D-130 返修 A 案（复审退回注钦定）：binance:earn 行补种 + binance:funding 行摘除。
-- earn：库内 SELECT 复制 binance real spot 凭据（0114 okx 同式；NOT EXISTS 幂等；binance sapi
-- Simple Earn 与主网 key 同签——票面原「复用键」案被复审退回：行枚举源=exchange_configs
-- 一行一 querier，无 binance:earn 行则理财面永不枚举、查询径成生产死代码）。
-- funding：官方已废除并入现货（业主令域2①）→ 整行删（表无 enabled 列；与 earn 补种同批部署）。
INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, website)
  SELECT 'binance', 'earn', 'earn', api_key, api_secret, passphrase, base_url, simulated, website
    FROM exchange_configs
   WHERE venue = 'binance' AND venue_type = 'spot' AND NOT simulated
     AND NOT EXISTS (SELECT 1 FROM exchange_configs WHERE venue = 'binance' AND venue_type = 'earn');

DELETE FROM exchange_configs WHERE venue = 'binance' AND venue_type = 'funding';
