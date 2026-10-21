-- 1069：broker_symbol_map 补 4 行（10-21 开仓管线通查——engine_swaparb_discover
-- 「symbol unmapped（促补映射）」实拍四符号：WTI_V6_CFD@15032313、ADAUSDm/ADAUSDT/
-- FILUSDT@3001157）。canonical 全对拍在册：CLUSDT perp hedge_canonical='WTI'
-- （binance/gate TRADING 实拍）、ADAUSDT/FILUSDT perp TRADING（binance/gate）。
-- contract_size：ADAUSDm 带 mt_symbol_meta facts=1000（D-140 S2 UnitContractSize
-- 实拍）先填；余三行 NULL=合法未知，D-042 broker 启动 BackfillSymbolContracts
-- 从 venue SymbolInfo.ContractSize 自动回填（禁手猜）。
-- ADA 双行组（ADAUSDm/ADAUSDT→同 canonical）：D-383 多行组须显式 primary——
-- 按 3001157 全表 m 尾惯例（AAVEUSDm→AAVEUSDT 族）标 ADAUSDm primary，
-- 唯一索引 broker_symbol_map_primary_unq（baseline:4451）护一行。
-- 返修（symmap-4 退回）：账户=运行时数据非 schema 种子——新装/测试库
-- broker_accounts 空则 FK broker_symbol_map_venue_fk 必炸。INSERT…SELECT…
-- WHERE EXISTS 守卫：户在册才插、户缺席=幂等跳过非错（映射随户补种）。
INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, contract_size, source, is_primary)
SELECT v.venue, v.broker_symbol, v.canonical, v.contract_size, v.source, v.is_primary
FROM (VALUES
       ('15032313', 'WTI_V6_CFD', 'WTI',   NULL, 'rule', false),
       ('3001157',  'ADAUSDm',    'ADAUSDT', 1000, 'meta', true),
       ('3001157',  'ADAUSDT',    'ADAUSDT', NULL, 'rule', false),
       ('3001157',  'FILUSDT',    'FILUSDT', NULL, 'rule', false)
     ) AS v(venue, broker_symbol, canonical, contract_size, source, is_primary)
WHERE EXISTS (SELECT 1 FROM broker_accounts WHERE venue = v.venue)
ON CONFLICT (venue, broker_symbol) DO NOTHING;
