-- 0153 D-532 S2：radar_watchlist symbol 列方言归一——存量行按 venues[1]
-- 的 venue 后缀规则反推 canonical 基名（与写侧 divergence/canonical 同方言）。
-- 无法识别的行保留原值（幂等：重跑无匹配=零改动）。
UPDATE radar_watchlist SET symbol = regexp_replace(symbol, '-USD(T)?-SWAP$', '')
	WHERE venues[1] = 'okx'   AND symbol ~ '-USD(T)?-SWAP$';
UPDATE radar_watchlist SET symbol = regexp_replace(symbol, '_USDT$', '')
	WHERE venues[1] = 'gate'  AND symbol ~ '_USDT$';
UPDATE radar_watchlist SET symbol = regexp_replace(symbol, 'USDT$', '')
	WHERE venues[1] = 'binance' AND symbol ~ 'USDT$';
