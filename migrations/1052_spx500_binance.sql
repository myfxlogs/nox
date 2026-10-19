-- cfd 纸面保底 monitorPairs 缺对修·第二轮（combo#20 双告警部署后复发实证）：
-- 1051 误把 combo canonical_sell 的字面值 US500 当资产类注册——但 MT 侧符号
-- US500@15032313 经 broker_symbol_map 规范化后 canonical=SPX500，mt 腿永不
-- 以类 US500 产出 universe 行/簿对（join 键是 mtCanonical 非 native）。
-- 资产真类=SPX500（既有类，四户 mt canonical 在册）。binance 两个 TradFi
-- perp 符号同为该资产，归入 SPX500 类；SPCXUSD1 一并入册（同资产，USD1 计价，
-- depth 端点缺口另案在注记）。幻影 US500 类两行删除（零消费者）。
DELETE FROM asset_equiv
 WHERE asset_class='US500' AND venue IN ('binance','15032313');
INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue) VALUES
  ('SPX500','SPCXUSDT','perp','binance'),
  ('SPX500','SPCXUSD1','perp','binance')
ON CONFLICT (asset_class, canonical, venue) DO NOTHING;
-- 配套数据订正（生产已执行，留档）：xasset_allow_combos#20
--   canonical_sell US500→SPX500（broker 原名误作 canonical），
--   canonical_buy SPCXUSD1→SPCXUSDT（USD1 腿 depth 端点 400 缺数据面）。
