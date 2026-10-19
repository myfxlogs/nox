-- cfd 纸面保底 monitorPairs 缺对修（combo#20 `15032313×binance|US500` 双告警实证）：
-- 保底槽键=NJK(mtCanon)=US500，universe/簿候选依赖 asset_equiv class→canonical
-- 解析——US500 类零注册行→universe 产行缺席+簿候选退猜 US500USDT-US500（无此
-- 合约）。binance 实 universe 行=SPCXUSDT（USDT 计价 TradFi perp，TRADING）；
-- MT 腿 venue canonical=US500（15032313 户）。注册 US500 类双腿：
--   cex 腿 canonical=SPCXUSDT（binance universe 符号 canonical 形，ticks/facts
--   同键域）；mt 腿 canonical=US500。
-- 注：USD1 计价 SPCXUSD1 采集缺口=另案（决策席注记已区分），本行只解保底簿
-- /universe US500 面对级。
INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue) VALUES
  ('US500','SPCXUSDT','perp','binance'),
  ('US500','US500','mt5','15032313')
ON CONFLICT (asset_class, canonical, venue) DO NOTHING;
