-- 1025_gate_earn_config.sql —— D-523③：gate:earn 子账户配置行回种（uni-lend 出借池
-- 接 rest_poll 快照写口+资金地图理财节点）。凭据=库内 gate real perp 行复制（1013 同式；
-- NOT EXISTS 幂等；明文列恒空→复制 *_enc bytea 直拷）。
INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, website,
                              api_key_enc, api_secret_enc, passphrase_enc)
  SELECT 'gate', 'earn', 'earn', api_key, api_secret, passphrase, base_url, simulated, website,
         api_key_enc, api_secret_enc, passphrase_enc
    FROM exchange_configs
   WHERE venue = 'gate' AND venue_type = 'perp' AND NOT simulated
     AND NOT EXISTS (SELECT 1 FROM exchange_configs WHERE venue = 'gate' AND venue_type = 'earn');
