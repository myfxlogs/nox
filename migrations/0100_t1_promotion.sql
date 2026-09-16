-- D-308：非加密 T1 标的晋级采集链——asset_equiv 十类 + broker_symbol_map 补 XAGUSD。
-- 晋级集合=雷达 T1 清单（fin_cex_universe 首扫实证：有对冲腿的 perp）。
-- NATGASUSDT 无腿不晋级；全部 ON CONFLICT 幂等。
INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue) VALUES
  -- GOLD 补跨所腿（D-303 只加了 binance）
  ('GOLD','XAU-USDT-SWAP','perp','okx'), ('GOLD','XAU_USDT','perp','gate'),
  -- WTI/BRENT/PLATINUM/PALLADIUM：三所 perp + mt5 腿@3001157
  ('WTI','CLUSDT','perp','binance'),('WTI','CL-USDT-SWAP','perp','okx'),('WTI','CL_USDT','perp','gate'),('WTI','WTI','mt5','3001157'),
  ('BRENT','BZUSDT','perp','binance'),('BRENT','BZ-USDT-SWAP','perp','okx'),('BRENT','BZ_USDT','perp','gate'),('BRENT','BRENT','mt5','3001157'),
  ('PLATINUM','XPTUSDT','perp','binance'),('PLATINUM','XPT-USDT-SWAP','perp','okx'),('PLATINUM','XPT_USDT','perp','gate'),('PLATINUM','XPTUSD','mt5','3001157'),
  ('PALLADIUM','XPDUSDT','perp','binance'),('PALLADIUM','XPD-USDT-SWAP','perp','okx'),('PALLADIUM','XPD_USDT','perp','gate'),('PALLADIUM','XPDUSD','mt5','3001157'),
  -- COPPER：仅 binance + mt5
  ('COPPER','COPPERUSDT','perp','binance'),('COPPER','XCUUSD','mt5','3001157'),
  -- SILVER：三所 perp + mt5（腿映射同步补，ticks 待 MT5 订阅 XAGUSDm——部署侧核对）
  ('SILVER','XAGUSDT','perp','binance'),('SILVER','XAG-USDT-SWAP','perp','okx'),('SILVER','XAG_USDT','perp','gate'),('SILVER','XAGUSD','mt5','3001157'),
  -- 指数：单所 perp + mt5
  ('NAS100','NAS100_USDT','perp','gate'),('NAS100','NAS100','mt5','3001157'),
  ('US30','US30_USDT','perp','gate'),('US30','US30','mt5','3001157'),
  ('SPX500','SPX500_USDT','perp','gate'),('SPX500','SPX500','mt5','3001157'),
  ('JP225','JP225-USDT-SWAP','perp','okx'),('JP225','JP225','mt5','3001157')
ON CONFLICT (asset_class, canonical, venue) DO NOTHING;

-- broker_symbol_map：补 XAGUSD 腿（contract_size 未知→NULL，校验跳过惯例；source='manual'）
INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, contract_size, source)
VALUES ('3001157','XAGUSDm','XAGUSD',NULL,'manual') ON CONFLICT (venue, broker_symbol) DO NOTHING;
