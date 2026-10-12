-- D-308 部署后勘误：asset_equiv perp canonical 必须用 canonical 命名空间
-- （ticks/facts 全所同键 = <base>USDT：gate CL_USDT→存 CLUSDT、okx CL-USDT-SWAP→存 CLUSDT）。
-- 迁移 0100 误用 venue 原生合约名（CL_USDT/CL-USDT-SWAP/NAS100_USDT/JP225-USDT-SWAP 等），
-- 永不命中 ticks.symbol → 配对静默不成对。SILVER mt5 腿同理应为生态 canonical 'SILVER'
-- （broker_symbol_map 既有 10 venue XAGUSD(m)→SILVER），0100 误写 'XAGUSD'。
-- 形式=DELETE+INSERT 幂等（重放/重复执行安全，不撞 unique(asset_class,canonical,venue)）。
DELETE FROM asset_equiv
WHERE venue_type = 'perp'
  AND (canonical LIKE '%-USDT-SWAP' OR canonical LIKE '%\_USDT');
DELETE FROM asset_equiv
WHERE asset_class = 'SILVER' AND venue = '3001157' AND canonical = 'XAGUSD';

INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue) VALUES
  ('GOLD','XAUUSDT','perp','okx'),('GOLD','XAUUSDT','perp','gate'),
  ('WTI','CLUSDT','perp','okx'),('WTI','CLUSDT','perp','gate'),
  ('BRENT','BZUSDT','perp','okx'),('BRENT','BZUSDT','perp','gate'),
  ('PLATINUM','XPTUSDT','perp','okx'),('PLATINUM','XPTUSDT','perp','gate'),
  ('PALLADIUM','XPDUSDT','perp','okx'),('PALLADIUM','XPDUSDT','perp','gate'),
  ('SILVER','XAGUSDT','perp','okx'),('SILVER','XAGUSDT','perp','gate'),('SILVER','SILVER','mt5','3001157'),
  ('NAS100','NAS100USDT','perp','gate'),('US30','US30USDT','perp','gate'),
  ('SPX500','SPX500USDT','perp','gate'),('JP225','JP225USDT','perp','okx')
ON CONFLICT (asset_class, canonical, venue) DO NOTHING;

-- 兜底：fresh install 若先跑 0100 写入 XAGUSDm→XAGUSD，统一回生态 canonical SILVER。
UPDATE broker_symbol_map SET canonical = 'SILVER'
WHERE broker_symbol IN ('XAGUSD', 'XAGUSDm') AND canonical = 'XAGUSD';
