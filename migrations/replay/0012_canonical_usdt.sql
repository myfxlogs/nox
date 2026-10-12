-- 0012_canonical_usdt.sql — D-056 canonical 统一映射：BTC/ETH 的 USD 报价 → USDT 后缀。
-- 使 CFD（BTCUSD）与 CEX（BTCUSDT）共桶，解除 CFD-CEX cross 结构性盲区（D-035 遗留）。
-- broker_symbol（原生符号）不变；仅 canonical 列归一。
UPDATE broker_symbol_map SET canonical = 'BTCUSDT' WHERE canonical = 'BTCUSD';
UPDATE broker_symbol_map SET canonical = 'ETHUSDT' WHERE canonical = 'ETHUSD';
