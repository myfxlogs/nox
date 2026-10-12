-- D-423：fin_xasset_monitor venue 维度化——pair 级统计按 (pair, cex_venue, mt_venue)
-- 三元组隔离（借用他所簿的 floor/noise/capture=伪安全边际/伪收割线，498 实证开仓即收）。
-- 存量行 backfill=binance:perp×3001157:mt5（工具历史单源实证：binance fapi×Exness 3001157）。
ALTER TABLE fin_xasset_monitor
    ADD COLUMN IF NOT EXISTS cex_venue TEXT NOT NULL DEFAULT 'binance:perp',
    ADD COLUMN IF NOT EXISTS mt_venue  TEXT NOT NULL DEFAULT '3001157:mt5';

CREATE INDEX IF NOT EXISTS idx_xasset_monitor_triple
    ON fin_xasset_monitor (pair, cex_venue, mt_venue, ts DESC);
