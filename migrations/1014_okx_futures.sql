-- 1014_okx_futures.sql —— gap#11 收口：venue_type 枚举放开 'futures' + okx:futures 配置行回种。
-- 凭据=库内 okx real perp 行复制（0182/1013 同式；NOT EXISTS 幂等）；
-- 明文列自 D-515 SealExchangeKeys 起恒空，凭据复制 *_enc 列（同 master key 下 bytea 直拷）。
-- 只放开 exchange_configs 枚举——futures 执行/采集径无 venue API 实证前不接（先行=账户可见性+能力账本匹配）。
ALTER TABLE exchange_configs DROP CONSTRAINT IF EXISTS exchange_configs_venue_type_chk;
ALTER TABLE exchange_configs ADD CONSTRAINT exchange_configs_venue_type_chk
    CHECK (venue_type IN ('mt4','mt5','perp','spot','funding','earn','option','unified','futures'));

INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, website,
                              api_key_enc, api_secret_enc, passphrase_enc)
  SELECT 'okx', 'futures', 'futures', api_key, api_secret, passphrase, base_url, simulated, website,
         api_key_enc, api_secret_enc, passphrase_enc
    FROM exchange_configs
   WHERE venue = 'okx' AND venue_type = 'perp' AND NOT simulated
     AND NOT EXISTS (SELECT 1 FROM exchange_configs WHERE venue = 'okx' AND venue_type = 'futures');
