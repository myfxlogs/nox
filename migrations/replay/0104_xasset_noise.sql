-- D-335 v2 S1：XASSET 入口安全边际闸数据源——真噪声列（24h 摆幅半径）。
-- v1 缺陷勘误：tracking_error_bps 实为当期基差水平（|tokenMid−xauMid|），非滚动噪声 σ——
-- 闸若用它与 |spread| 比较恒拦。noise_bps=(trailing 24h entry_spread_bps max−min)/2，样本 <12 NULL。
ALTER TABLE fin_xasset_monitor ADD COLUMN IF NOT EXISTS noise_bps NUMERIC;
