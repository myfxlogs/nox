-- D-303：GOLD 等价类挂 XAUUSDT（Binance 金价指数永续，与 Exness XAUUSD 同锚）
-- 配对增量：XAUUSDT-XAU / XAUUSDT-PAXG / XAUUSDT-XAUT（GOLD 四 canonical → C(4,2)=6 对）
INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue) VALUES
  ('GOLD', 'XAUUSDT', 'perp', 'binance')
ON CONFLICT (asset_class, canonical, venue) DO NOTHING;
