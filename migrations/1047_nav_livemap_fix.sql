-- 1047_nav_livemap_fix.sql —— NAV 日切 liveMap 指纹注血腐败行清理
-- （STATE「NAV 日切 liveMap 指纹注血修复」票·待派加急，范围③④）。
--
-- 病灶（决策席 10-18 SG 三通道亲证）：liveMapFn 键=venue:type、池粒度=
-- venue:type:currency——MULTI 户 Equity=Σqty 变更指纹（binance:earn 101524.64
-- 等）被当真值覆写至该户每个币种池。fin_nav_daily day_start≥2026-10-17
-- （liveMapFn 上线后全部日切日）非 USD 桶全腐：USDT 桶 7064→108037、~85 桶
-- 雷同指纹值。USD 桶干净（mt 池免覆写）保留不动。
--
-- 清理=点名胜任行删除（票面禁线「清理 SQL 只动点名行」）：删后下一日切按修后
-- 分币粒度实拉值自然重写；被删日=诚实缺行非编数回填。

-- ① 腐败桶行清删：liveMapFn 上线日（2026-10-17）起全部非 USD 桶（USDT 桶同腐
-- 在域；含修件迟到时后续日切复发行——动态口径覆盖）。USD 桶诚实保留。
DELETE FROM fin_nav_daily
WHERE currency <> 'USD' AND day_start >= DATE '2026-10-17';

-- ② fin_strategy_budget XASSET_CARRY 陈切残留行（nav_usd=4247.63 化石——策略已
-- 退役 disabled，预算行永不再被 derive 覆写；清而非标=诚实消灭非粉饰）。
DELETE FROM fin_strategy_budget WHERE strategy = 'XASSET_CARRY';
