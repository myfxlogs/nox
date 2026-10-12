-- 0131_data_plane_venue.sql —— D-420 S2/S3：数据面与 venue 扩容（指数/能源 perp 补盲）。
-- S2：fin_cex_depth 顶档深度表（bid1/ask1 价+量、mid_depth_usd=中间价×可成交量 USD 名义
-- ——容量与滑点研究数据前提；v1 顶档够定性不做多档；保留 90 天滚动由采集器 DeleteCexDepthBefore）。
-- S3：油族 canonical 对齐——CEX 侧油基名=CL（CLUSDT/CL_USDT/CL-USDT-SWAP），MT 侧既有
-- canonical=WTI（USOILm/WTI 等十三行）→ join 键不相等=油行零产出。补 CL canonical 行
-- （primary WTI 符号全量、is_primary=true、source='manual'；与既有 WTI 行并存不迁移，
-- 既有 WTI 家族零破坏）。SPX500/NAS100/US30/JP225/XAU/XPT/XPD 映射已在库（0122 种子+人工），
-- 经生产只读复核非缺口（2026-09-23），本迁移不重复播种。
CREATE TABLE IF NOT EXISTS fin_cex_depth (
  id            BIGSERIAL PRIMARY KEY,
  venue         TEXT NOT NULL,
  symbol        TEXT NOT NULL,
  ts            TIMESTAMPTZ NOT NULL DEFAULT now(),
  bid1_usd      DOUBLE PRECISION NOT NULL,
  ask1_usd      DOUBLE PRECISION NOT NULL,
  bid1_qty      DOUBLE PRECISION NOT NULL,
  ask1_qty      DOUBLE PRECISION NOT NULL,
  mid_depth_usd DOUBLE PRECISION NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_fin_cex_depth_venue_symbol_ts ON fin_cex_depth (venue, symbol, ts DESC);

INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, source, is_primary) VALUES
  ('277842155', 'USOILm', 'CL', 'manual', true),
  ('3001157',   'USOILm', 'CL', 'manual', true),
  ('39050818',  'USOIL',  'CL', 'manual', true),
  ('59892915',  'USOILm', 'CL', 'manual', true),
  ('591959217', 'WTI',    'CL', 'manual', true)
ON CONFLICT DO NOTHING;
