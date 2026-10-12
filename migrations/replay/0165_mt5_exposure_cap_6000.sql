-- 0165 D-007 S2：MT5 两户 venue 敞口闸对齐业主批 $6000。
-- 背景：3001157:mt5=$1500/15032313:mt5=$1000 是 9-28 池值种子；闸6 死键修复后
-- （typed 键直查）旧值会冻结全部 MT5 腿入场（白名单名义 $1.2k-4.4k 即超 $1500）。
-- venue 串行性由 canonical 聚簇 cap（$6000）承担；cap=名义敞口上限非资金需求。
-- WHERE cap_usd<>6000 幂等（二跑零命中）。
UPDATE fin_venue_exposure_caps
SET cap_usd = 6000,
    note = note || ' D-007 业主批 $6000',
    updated_at = now()
WHERE venue_key IN ('3001157:mt5', '15032313:mt5')
  AND cap_usd <> 6000;
