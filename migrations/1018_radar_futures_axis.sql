-- 1018_radar_futures_axis.sql —— 宇宙雷达仪器轴扩列：perp-only→+FUTURES。
-- 交割合约吃「到期收敛基差」非 funding——新轴列 contract_type/expiry_ts/basis_ann。
-- basis_ann=(mark/index−1)×365/days_to_expiry 年化 %；expiry/行情缺→NULL fail-closed。
ALTER TABLE fin_cex_universe ADD COLUMN IF NOT EXISTS contract_type text;
COMMENT ON COLUMN fin_cex_universe.contract_type IS '仪器轴：PERP/FUTURES（交割=含到期日合约）；NULL=perp 存量行';
ALTER TABLE fin_cex_universe ADD COLUMN IF NOT EXISTS expiry_ts timestamptz;
COMMENT ON COLUMN fin_cex_universe.expiry_ts IS '交割日（FUTURES 行；venue 仪器字段原值）';
ALTER TABLE fin_cex_universe ADD COLUMN IF NOT EXISTS last_basis_ann double precision;
COMMENT ON COLUMN fin_cex_universe.last_basis_ann IS '基差年化 %（FUTURES 评估轴；perp/缺数据=NULL）';
ALTER TABLE fin_cex_radar ADD COLUMN IF NOT EXISTS contract_type text;
COMMENT ON COLUMN fin_cex_radar.contract_type IS '仪器轴：PERP/FUTURES';
ALTER TABLE fin_cex_radar ADD COLUMN IF NOT EXISTS expiry_ts timestamptz;
ALTER TABLE fin_cex_radar ADD COLUMN IF NOT EXISTS basis_ann double precision;
