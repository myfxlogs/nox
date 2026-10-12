-- 0128_paper_all_in.sql — D-410 S1：纸面全账口径三列。
-- est_round_trip_fee_usd=开仓一次性记账的往返费用估计（存正幅度，恒等式用减）；
-- convergence_usd=退出时冻结收敛捕获（active 行 NULL）；net_all_in_usd=毕业建议透出全账净额。
ALTER TABLE paper_spot_carry ADD COLUMN IF NOT EXISTS est_round_trip_fee_usd NUMERIC;
ALTER TABLE paper_spot_carry ADD COLUMN IF NOT EXISTS convergence_usd NUMERIC;
ALTER TABLE carry_graduations ADD COLUMN IF NOT EXISTS net_all_in_usd NUMERIC;

-- 存量回填（R0-B4）：venue→往返费率常量表，覆盖集=universe-radar CEX 源（binance/okx/gate/mexc）。
-- 耦合钉死：rate=2×ARB_XASSET_CEX_FEE_BPS（缺省 5.0 → 10.0bps 往返=开+平）——env 改值须同步
-- 本表与 scan.paperRoundTripFeeBps（P3 单源注释锚，测试断言与 cfg 值一致）。
-- spot 轨 venue=cex 名直配；cfd 轨 venue=mt×cex 组合串取第二段。映射外 venue → 保持 NULL
-- （拒填行：可算不可毕业，防坏数据播种首批基线）。
UPDATE paper_spot_carry p
SET est_round_trip_fee_usd = p.qty_usd * v.rate_bps / 10000
FROM (VALUES ('binance',10.0),('okx',10.0),('gate',10.0),('mexc',10.0)) AS v(cex,rate_bps)
WHERE p.est_round_trip_fee_usd IS NULL
  AND (p.venue = v.cex OR split_part(p.venue, '×', 2) = v.cex);
