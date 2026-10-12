-- D-300 跨标的同质资产配对表（标的等价类：同 asset_class 不同 canonical 可配对）
CREATE TABLE asset_equiv (
  id bigserial PRIMARY KEY,
  asset_class text NOT NULL,          -- "GOLD" / "BTC" / "SILVER"
  canonical text NOT NULL,            -- "PAXGUSDT" / "XAUUSD" / "XAUTUSDT"
  venue_type text NOT NULL,           -- "perp" / "spot" / "mt5"
  venue text NOT NULL,                -- QuoteBus 运行时键：CEX=所名，MT 类=账号 ID（裁 8）
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (asset_class, canonical, venue)
);
CREATE INDEX idx_asset_equiv_class ON asset_equiv(asset_class) WHERE enabled;

-- v1 注册 GOLD=PAXG+XAUT+XAU 三 canonical（裁 8：XAU venue=Exness 实盘账号 3001157，非 'exness'；
-- 9-16 业主并存裁定+决策席裁 10/12：XAUT 并入本迁移，配对=PAXG-XAU/XAUT-XAU/PAXG-XAUT 三对）
INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue) VALUES
  ('GOLD', 'PAXGUSDT', 'perp', 'binance'),
  ('GOLD', 'XAUTUSDT', 'perp', 'binance'),
  ('GOLD', 'XAUUSD', 'mt5', '3001157');

-- D-300 opportunities 扩展：XASSET_CARRY type + asset_class + canonical_sell（卖腿 canonical）
ALTER TABLE opportunities DROP CONSTRAINT IF EXISTS opportunities_type_check;
ALTER TABLE opportunities ADD CONSTRAINT opportunities_type_check
  CHECK (type IN ('CROSS_VENUE','CARRY','FUNDING_HEDGE','PERP_FUNDING','XASSET_CARRY'));
ALTER TABLE opportunities ADD COLUMN IF NOT EXISTS asset_class text;      -- 跨标的套利的 asset_class
ALTER TABLE opportunities ADD COLUMN IF NOT EXISTS canonical_sell text;  -- 卖腿 canonical（XASSET_CARRY 填，其他 type 留空）
CREATE INDEX IF NOT EXISTS idx_opportunities_asset_class ON opportunities(asset_class) WHERE asset_class IS NOT NULL;
