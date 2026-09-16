-- D-300 增量：GOLD 类挂 XAUTUSDT（业主 9-16 裁定 PAXG+XAUT 并存；XAUTUSDT=Binance Tether Gold 永续）
-- detector 扫 asset_class 内全部 enabled canonical → PAXG-XAU 与 XAUT-XAU 两对自动配对
INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue) VALUES
  ('GOLD', 'XAUTUSDT', 'perp', 'binance')
ON CONFLICT (asset_class, canonical, venue) DO NOTHING;
