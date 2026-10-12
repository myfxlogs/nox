-- 0153 D-532 S2：radar_watchlist symbol 列方言归一——存量行按 venues[1]
-- 的 venue 后缀规则反推 canonical 基名（与写侧 divergence/canonical 同方言）。
-- 无法识别的行保留原值（幂等：重跑无匹配=零改动）。
-- v2 修（部署实证）：归一目标可能已存在（DOTUSDT→DOT 撞 DOT），且不同 venue
-- 方言可先后归一到同一名（BZ-USDT-SWAP + BZUSDT → BZ）——逐行处理：目标
-- 已存在则 venues 并入目标行+删源行，否则原地改名。
DO $$
DECLARE
	r   record;
	tgt text;
BEGIN
	FOR r IN
		SELECT symbol, venues, first_seen, last_seen, venues[1] AS v1
		FROM radar_watchlist
		WHERE (venues[1] = 'okx'     AND symbol ~ '-USD(T)?-SWAP$')
		   OR (venues[1] = 'gate'    AND symbol ~ '_USDT$')
		   OR (venues[1] = 'binance' AND symbol ~ 'USDT$')
	LOOP
		tgt := CASE r.v1
			WHEN 'okx'     THEN regexp_replace(r.symbol, '-USD(T)?-SWAP$', '')
			WHEN 'gate'    THEN regexp_replace(r.symbol, '_USDT$', '')
			WHEN 'binance' THEN regexp_replace(r.symbol, 'USDT$', '')
		END;
		IF EXISTS (SELECT 1 FROM radar_watchlist WHERE symbol = tgt) THEN
			UPDATE radar_watchlist
			SET venues     = ARRAY(SELECT DISTINCT unnest(venues || r.venues) ORDER BY 1),
			    first_seen = LEAST(first_seen, r.first_seen),
			    last_seen  = GREATEST(last_seen, r.last_seen)
			WHERE symbol = tgt;
			DELETE FROM radar_watchlist WHERE symbol = r.symbol;
		ELSE
			UPDATE radar_watchlist SET symbol = tgt WHERE symbol = r.symbol;
		END IF;
	END LOOP;
END $$;
