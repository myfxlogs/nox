-- D-625 P2 前提检验+regime 判据种子（票禁线：判据 app_config 驱动禁硬编码字面量——
-- 值=实施席提案初值，裁决调整走配置不写码）。
INSERT INTO app_config (key, value) VALUES
  ('regime_vol_bps','8'),        -- stdev≥阈→volatile
  ('regime_trend_bps','6'),      -- |Δentry_spread|≥阈→trending
  ('regime_min_rows','12'),      -- 窗内行数<阈→illiquid（noise 同源阈）
  ('prec_stale_max_min','15')    -- 簿行龄>阈→前提塌（采集周期×3）
ON CONFLICT (key) DO NOTHING;
